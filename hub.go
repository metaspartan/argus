package main

import (
	"crypto/rand"
	"crypto/tls"
	"embed"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
)

//go:embed web
var webFS embed.FS

const (
	fastLen = 1800 // ~1h at 2s
	slowLen = 1440 // 24h at 1min
)

// point = [t, cpu%, mem%, gpu%, rx B/s, tx B/s, disk r B/s, disk w B/s, max temp °C]
type point [9]float64

type ring struct {
	buf  []point
	head int
	n    int
}

func newRing(n int) *ring { return &ring{buf: make([]point, n)} }
func (r *ring) push(p point) {
	r.buf[r.head] = p
	r.head = (r.head + 1) % len(r.buf)
	if r.n < len(r.buf) {
		r.n++
	}
}
func (r *ring) slice() []point {
	out := make([]point, 0, r.n)
	start := (r.head - r.n + len(r.buf)) % len(r.buf)
	for i := 0; i < r.n; i++ {
		out = append(out, r.buf[(start+i)%len(r.buf)])
	}
	return out
}

type node struct {
	Static  *Static
	Last    *Metrics
	Seen    int64
	Addr    string
	Local   bool
	KeyHash string
	fast    *ring
	slow    *ring
	acc     point
	accN    int
	accMin  int64
	cmds    []Command
	results []Result
	virt    map[string]bool
	wake    chan struct{}
}

func newNode() *node {
	return &node{fast: newRing(fastLen), slow: newRing(slowLen), virt: map[string]bool{}, wake: make(chan struct{}, 1)}
}

func (n *node) poke() {
	select {
	case n.wake <- struct{}{}:
	default:
	}
}

type sub struct {
	ch    chan []byte
	focus string
}

type Hub struct {
	cfg   *config
	mu    sync.RWMutex
	nodes map[string]*node

	subsMu sync.Mutex
	subs   map[*sub]struct{}
	focus  map[string]int

	sessions *sessionStore
	lim      *limiter
	audit    *auditLog
	admin    string
	enroll   string
	enrollMu sync.RWMutex

	tlsMode string // auto | cert | off
	pin     string // sha256/<b64 SPKI>
	certHex string // SHA-256 of the DER cert (for PowerShell pinning)

	localID   string
	localWake chan struct{}
}

func runHub(c *config) {
	if err := os.MkdirAll(c.Data, 0o700); err != nil {
		fatal("data dir: %v", err)
	}
	h := &Hub{cfg: c, nodes: map[string]*node{}, subs: map[*sub]struct{}{}, focus: map[string]int{},
		sessions: newSessionStore(c.Data), lim: newLimiter(), audit: openAudit(c.Data), localWake: make(chan struct{}, 1)}
	h.admin = c.AdminToken
	if h.admin == "" {
		h.admin = readOrCreateSecret(filepath.Join(c.Data, "admin.token"), 24)
	}
	h.enroll = readOrCreateSecret(filepath.Join(c.Data, "enroll.token"), 18)
	if c.Token != "" && c.Token != h.enroll && os.Getenv("ARGUS_TOKEN") != "" {
		h.enroll = c.Token
	}
	h.load()
	go h.persistLoop()
	if !c.NoLocal {
		go h.localAgent()
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/agent/enroll", h.handleEnroll)
	mux.HandleFunc("POST /api/agent/report", h.handleReport)
	mux.HandleFunc("GET /api/agent/poll", h.handlePoll)
	mux.HandleFunc("POST /api/login", h.handleLogin)
	mux.HandleFunc("POST /api/logout", h.handleLogout)
	mux.HandleFunc("GET /api/nodes", h.auth(h.handleNodes))
	mux.HandleFunc("GET /api/node/{id}", h.auth(h.handleNode))
	mux.HandleFunc("DELETE /api/node/{id}", h.auth(h.handleDelete))
	mux.HandleFunc("POST /api/node/{id}/cmd", h.auth(h.handleCmd))
	mux.HandleFunc("GET /api/stream", h.auth(h.handleStream))
	mux.HandleFunc("GET /api/info", h.auth(h.handleInfo))
	mux.HandleFunc("POST /api/enroll/rotate", h.auth(h.handleRotate))
	mux.HandleFunc("GET /install.sh", h.handleInstallSh)
	mux.HandleFunc("GET /install.ps1", h.handleInstallPs1)
	mux.HandleFunc("GET /dl/{file}", h.handleDownload)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	sub, _ := fs.Sub(webFS, "web")
	static := http.FileServer(http.FS(sub))
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		static.ServeHTTP(w, r)
	})

	srv := &http.Server{
		Addr:              c.Listen,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
		ErrorLog:          nil,
	}
	switch {
	case c.TLSCert != "" && c.TLSKey != "":
		cert, err := tls.LoadX509KeyPair(c.TLSCert, c.TLSKey)
		if err != nil {
			fatal("tls: %v", err)
		}
		h.tlsMode = "cert"
		srv.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}
	case c.TLS == "off":
		h.tlsMode = "off"
	default:
		var hosts []string
		for _, s := range strings.Split(c.Hosts, ",") {
			if s = strings.TrimSpace(s); s != "" {
				hosts = append(hosts, s)
			}
		}
		if c.URL != "" {
			if u, err := url.Parse(c.URL); err == nil && u.Hostname() != "" {
				hosts = append(hosts, u.Hostname())
			}
		}
		cert, leaf, err := loadOrCreateCert(c.Data, hosts)
		if err != nil {
			fatal("tls: %v", err)
		}
		h.tlsMode, h.pin, h.certHex = "auto", spkiPin(leaf), certSHA256Hex(leaf)
		srv.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}
	}
	srv.Handler = secureHeaders(mux, h.tlsMode != "off")

	logf("argus hub %s listening on %s (tls=%s, data %s)", version, c.Listen, h.tlsMode, c.Data)
	logf("dashboard:   %s", h.baseURLs()[0])
	logf("admin token: %s   (dashboard login — keep private)", h.admin)
	logf("enroll:      %s", h.unixCmd(h.baseURLs()[0], c.Manage))
	if h.pin != "" {
		logf("cert pin:    %s", h.pin)
	}
	var err error
	if h.tlsMode == "off" {
		err = srv.ListenAndServe()
	} else {
		err = srv.ListenAndServeTLS("", "")
	}
	fatal("%v", err)
}

func (h *Hub) scheme() string {
	if h.tlsMode == "off" {
		return "http"
	}
	return "https"
}

func (h *Hub) baseURLs() []string {
	if h.cfg.URL != "" {
		return []string{strings.TrimRight(h.cfg.URL, "/")}
	}
	_, port, _ := net.SplitHostPort(h.cfg.Listen)
	var out []string
	for _, ip := range lanIPs() {
		if strings.Contains(ip, ":") {
			ip = "[" + ip + "]"
		}
		out = append(out, h.scheme()+"://"+ip+":"+port)
	}
	return out
}

func randID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func lanIPs() []string {
	var out []string
	ifs, _ := net.Interfaces()
	for _, i := range ifs {
		if i.Flags&net.FlagUp == 0 || i.Flags&net.FlagLoopback != 0 || isVirtualNIC(i.Name) {
			continue
		}
		as, _ := i.Addrs()
		for _, a := range as {
			if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil && !ipn.IP.IsLinkLocalUnicast() {
				out = append(out, ipn.IP.String())
			}
		}
	}
	if len(out) == 0 {
		out = append(out, "localhost")
	}
	return out
}

// ---------------------------------------------------------------- auth

func (h *Hub) ip(r *http.Request) string { return clientIP(r, h.cfg.TrustProxy) }

func (h *Hub) secureReq(r *http.Request) bool {
	return r.TLS != nil || (h.cfg.TrustProxy && r.Header.Get("X-Forwarded-Proto") == "https")
}

// auth guards dashboard APIs: valid session cookie, plus CSRF defenses
// (custom header + same-origin) on state-changing requests.
func (h *Hub) auth(fn http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie("argus_session")
		if err != nil || !h.sessions.valid(c.Value) {
			http.Error(w, "unauthorized", 401)
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" {
			if r.Header.Get("X-Argus") != "1" {
				http.Error(w, "missing csrf header", 403)
				return
			}
			if o := r.Header.Get("Origin"); o != "" {
				if u, err := url.Parse(o); err != nil || u.Host != r.Host {
					http.Error(w, "cross-origin request refused", 403)
					return
				}
			}
		}
		fn(w, r)
	}
}

func (h *Hub) handleLogin(w http.ResponseWriter, r *http.Request) {
	ip := h.ip(r)
	if h.lim.blocked(ip) {
		http.Error(w, "too many attempts — try again in a few minutes", 429)
		return
	}
	var body struct{ Token string }
	json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body)
	if !ctEq(strings.TrimSpace(body.Token), h.admin) {
		h.lim.fail(ip)
		h.audit.log(ip, "login-failed", "", "")
		time.Sleep(400 * time.Millisecond)
		http.Error(w, "invalid token", 401)
		return
	}
	id := h.sessions.create()
	http.SetCookie(w, &http.Cookie{Name: "argus_session", Value: id, Path: "/", HttpOnly: true, Secure: h.secureReq(r), SameSite: http.SameSiteStrictMode, MaxAge: int(sessionTTL.Seconds())})
	h.audit.log(ip, "login", "", "")
	w.WriteHeader(204)
}

func (h *Hub) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie("argus_session"); err == nil {
		h.sessions.delete(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: "argus_session", Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
	w.WriteHeader(204)
}

var idRe = regexp.MustCompile(`^[A-Za-z0-9._:-]{4,128}$`)

func (h *Hub) handleEnroll(w http.ResponseWriter, r *http.Request) {
	ip := h.ip(r)
	if h.lim.blocked(ip) {
		http.Error(w, "too many attempts", 429)
		return
	}
	h.enrollMu.RLock()
	tok := h.enroll
	h.enrollMu.RUnlock()
	if !ctEq(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), tok) {
		h.lim.fail(ip)
		h.audit.log(ip, "enroll-rejected", "", "bad enroll token")
		http.Error(w, "invalid enroll token", 401)
		return
	}
	var body struct{ ID, Hostname string }
	if json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body) != nil || !idRe.MatchString(body.ID) {
		http.Error(w, "bad request", 400)
		return
	}
	key := randToken32()
	h.mu.Lock()
	n := h.nodes[body.ID]
	if n == nil {
		n = newNode()
		h.nodes[body.ID] = n
	}
	n.KeyHash = sha256hex(key)
	n.Addr = ip
	h.mu.Unlock()
	h.save()
	h.audit.log(ip, "enroll", body.ID, body.Hostname)
	writeJSON(w, map[string]string{"key": key})
}

func randToken32() string {
	b := make([]byte, 32)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// agentAuth verifies "Authorization: Agent <id>:<key>".
func (h *Hub) agentAuth(w http.ResponseWriter, r *http.Request) (string, *node) {
	ip := h.ip(r)
	if h.lim.blocked(ip) {
		http.Error(w, "too many attempts", 429)
		return "", nil
	}
	cred, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Agent ")
	id, key, ok2 := strings.Cut(cred, ":")
	if ok && ok2 {
		h.mu.RLock()
		n := h.nodes[id]
		h.mu.RUnlock()
		if n != nil && n.KeyHash != "" && ctEq(sha256hex(key), n.KeyHash) {
			return id, n
		}
	}
	h.lim.fail(ip)
	http.Error(w, "unauthorized agent", 401)
	return "", nil
}

// ---------------------------------------------------------------- cadence

// intervalFor picks how often an agent should report:
//
//	focused in a dashboard → 1s, any dashboard open → --interval, nobody → --idle-interval
func (h *Hub) intervalFor(id string) int {
	h.subsMu.Lock()
	defer h.subsMu.Unlock()
	switch {
	case h.focus[id] > 0:
		return int(min(time.Second, h.cfg.Interval).Milliseconds())
	case len(h.subs) > 0:
		return int(h.cfg.Interval.Milliseconds())
	}
	return int(h.cfg.Idle.Milliseconds())
}

func (h *Hub) pokeAll() {
	h.mu.RLock()
	for _, n := range h.nodes {
		n.poke()
	}
	h.mu.RUnlock()
	select {
	case h.localWake <- struct{}{}:
	default:
	}
}

func (h *Hub) pokeNode(id string) {
	if id == "" {
		return
	}
	if id == h.localID {
		select {
		case h.localWake <- struct{}{}:
		default:
		}
		return
	}
	h.mu.RLock()
	if n := h.nodes[id]; n != nil {
		n.poke()
	}
	h.mu.RUnlock()
}

// ---------------------------------------------------------------- ingest

func (h *Hub) handleReport(w http.ResponseWriter, r *http.Request) {
	id, _ := h.agentAuth(w, r)
	if id == "" {
		return
	}
	var rep Report
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<20)).Decode(&rep); err != nil || rep.ID != id {
		http.Error(w, "bad report", 400)
		return
	}
	resp := h.ingest(&rep, h.ip(r), false)
	writeJSON(w, resp)
}

func (h *Hub) handlePoll(w http.ResponseWriter, r *http.Request) {
	id, n := h.agentAuth(w, r)
	if id == "" {
		return
	}
	t := time.NewTimer(50 * time.Second)
	defer t.Stop()
	select {
	case <-n.wake:
	case <-t.C:
	case <-r.Context().Done():
		return
	}
	h.mu.Lock()
	cmds := n.cmds
	n.cmds = nil
	h.mu.Unlock()
	writeJSON(w, &ReportResp{Cmds: cmds, Interval: h.intervalFor(id)})
}

func (h *Hub) ingest(rep *Report, addr string, local bool) *ReportResp {
	resp := &ReportResp{Interval: h.intervalFor(rep.ID)}
	now := time.Now().UnixMilli()
	h.mu.Lock()
	n := h.nodes[rep.ID]
	if n == nil {
		if !local {
			h.mu.Unlock()
			resp.NeedStatic = true
			return resp
		}
		n = newNode()
		h.nodes[rep.ID] = n
	}
	var staticEv []byte
	if rep.Static != nil {
		n.Static = rep.Static
		n.virt = map[string]bool{}
		for _, nic := range rep.Static.NICs {
			if nic.Virt {
				n.virt[nic.Name] = true
			}
		}
		staticEv, _ = json.Marshal(map[string]any{"id": rep.ID, "static": rep.Static, "local": local})
	}
	if n.Static == nil {
		resp.NeedStatic = true
	}
	n.Seen, n.Addr, n.Local = now, addr, local
	var mEv []byte
	if rep.M != nil {
		n.Last = rep.M
		p := summarize(rep.M, n.virt)
		n.fast.push(p)
		minute := int64(p[0]) / 60
		if n.accN > 0 && minute != n.accMin {
			var a point
			for i := range a {
				a[i] = round1(n.acc[i] / float64(n.accN))
			}
			a[0] = float64(n.accMin * 60)
			n.slow.push(a)
			n.acc, n.accN = point{}, 0
		}
		n.accMin = minute
		for i := range p {
			n.acc[i] += p[i]
		}
		n.accN++
		mEv, _ = json.Marshal(map[string]any{"id": rep.ID, "m": rep.M, "p": p, "seen": now, "iv": resp.Interval})
	}
	var resEv [][]byte
	for _, res := range rep.Results {
		n.results = append(n.results, res)
		b, _ := json.Marshal(map[string]any{"id": rep.ID, "result": res})
		resEv = append(resEv, b)
	}
	if len(n.results) > 100 {
		n.results = n.results[len(n.results)-100:]
	}
	if !local {
		resp.Cmds, n.cmds = n.cmds, nil
	}
	h.mu.Unlock()

	if staticEv != nil {
		h.broadcast("node", staticEv)
	}
	if mEv != nil {
		h.broadcast("m", mEv)
	}
	for _, b := range resEv {
		h.broadcast("result", b)
	}
	return resp
}

func summarize(m *Metrics, virt map[string]bool) point {
	var p point
	p[0] = float64(m.T / 1000)
	p[1] = m.CPU
	if m.Mem.Total > 0 {
		p[2] = round1(float64(m.Mem.Used) / float64(m.Mem.Total) * 100)
	}
	if len(m.GPUs) > 0 {
		var s float64
		for _, g := range m.GPUs {
			s += g.Util
		}
		p[3] = round1(s / float64(len(m.GPUs)))
	}
	for _, n := range m.Net {
		if virt[n.Name] || n.Name == "lo" {
			continue
		}
		p[4] += n.Rx
		p[5] += n.Tx
	}
	for _, d := range m.Disks {
		p[6] += d.R
		p[7] += d.W
	}
	for _, t := range m.Temps {
		if t.Value > p[8] && t.Value < 150 && !strings.Contains(strings.ToLower(t.Name), "power") {
			p[8] = t.Value
		}
	}
	for _, g := range m.GPUs {
		p[8] = max(p[8], g.Temp)
	}
	return p
}

// localAgent monitors the hub machine in-process (no HTTP hop, same cadence rules).
func (h *Hub) localAgent() {
	col := newCollector()
	dk := newDocker()
	staticFn := func() *Static {
		st := col.Static()
		if h.cfg.Name != "" {
			st.Hostname = h.cfg.Name
		}
		st.Manage = h.cfg.Manage
		if dk != nil {
			st.Extra["Container engine"] = dk.describe()
		}
		return st
	}
	st := staticFn()
	h.localID = st.ID
	var stMu sync.Mutex
	ex := &executor{manage: h.cfg.Manage, docker: dk, onDone: func(r Result) {
		h.ingest(&Report{ID: st.ID, Results: []Result{r}}, "local", true)
	}, refresh: func() {
		ns := staticFn()
		stMu.Lock()
		st = ns
		stMu.Unlock()
		h.ingest(&Report{ID: ns.ID, Static: ns}, "local", true)
	}}
	var usage selfUsage
	iv := time.Duration(h.intervalFor(st.ID)) * time.Millisecond
	t := time.NewTicker(iv)
	sent := false
	lastNIC := nicFingerprint()
	for tick := 0; ; tick++ {
		m := col.Collect(tick)
		if dk != nil {
			m.Docker = dk.collect(tick)
		}
		m.Self = usage.sample()
		stMu.Lock()
		rep := &Report{ID: st.ID, M: m}
		if !sent {
			rep.Static, sent = st, true
		}
		stMu.Unlock()
		if tick > 0 && tick%everyNICs == 0 {
			if fp := nicFingerprint(); fp != lastNIC {
				lastNIC = fp
				go ex.refresh()
			}
		}
		if resp := h.ingest(rep, "local", true); resp.NeedStatic {
			sent = false
		}
	wait:
		select {
		case <-t.C:
		case <-h.localWake:
			// drain queued commands for the local node + re-evaluate cadence
			h.mu.Lock()
			var cmds []Command
			if n := h.nodes[st.ID]; n != nil {
				cmds, n.cmds = n.cmds, nil
			}
			h.mu.Unlock()
			for _, c := range cmds {
				go ex.run(c)
			}
			if d := time.Duration(h.intervalFor(st.ID)) * time.Millisecond; d != iv {
				faster := d < iv
				iv = d
				t.Reset(d)
				if faster {
					continue
				}
			}
			goto wait
		}
	}
}

// ---------------------------------------------------------------- API

type nodeView struct {
	ID     string   `json:"id"`
	Static *Static  `json:"static"`
	Last   *Metrics `json:"last,omitempty"`
	Seen   int64    `json:"seen"`
	Addr   string   `json:"addr,omitempty"`
	Local  bool     `json:"local,omitempty"`
	Hist   []point  `json:"hist,omitempty"`
}

func (h *Hub) handleNodes(w http.ResponseWriter, r *http.Request) {
	h.mu.RLock()
	out := make([]nodeView, 0, len(h.nodes))
	for id, n := range h.nodes {
		if n.Static == nil {
			continue
		}
		v := nodeView{ID: id, Static: n.Static, Last: n.Last, Seen: n.Seen, Addr: n.Addr, Local: n.Local}
		s := n.fast.slice()
		if len(s) > 300 {
			s = s[len(s)-300:]
		}
		v.Hist = s
		out = append(out, v)
	}
	h.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	writeJSON(w, map[string]any{"now": time.Now().UnixMilli(), "nodes": out})
}

func (h *Hub) handleNode(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	h.mu.RLock()
	n := h.nodes[id]
	if n == nil {
		h.mu.RUnlock()
		http.Error(w, "not found", 404)
		return
	}
	b, _ := json.Marshal(map[string]any{"id": id, "static": n.Static, "last": n.Last, "seen": n.Seen, "addr": n.Addr, "local": n.Local,
		"results": n.results, "hist": n.fast.slice(), "slow": n.slow.slice()})
	h.mu.RUnlock()
	w.Header().Set("Content-Type", "application/json")
	w.Write(b)
}

func (h *Hub) handleDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	h.mu.Lock()
	delete(h.nodes, id) // also revokes its agent key
	h.mu.Unlock()
	h.save()
	h.audit.log(h.ip(r), "forget", id, "agent key revoked")
	b, _ := json.Marshal(map[string]string{"id": id})
	h.broadcast("gone", b)
	w.WriteHeader(204)
}

var validKinds = map[string]bool{"exec": true, "kill": true, "reboot": true, "shutdown": true, "inventory": true, "docker": true}

func (h *Hub) handleCmd(w http.ResponseWriter, r *http.Request) {
	var c Command
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&c); err != nil || !validKinds[c.Kind] {
		http.Error(w, "bad command", 400)
		return
	}
	c.ID = randID()
	id := r.PathValue("id")
	h.mu.Lock()
	n := h.nodes[id]
	if n == nil {
		h.mu.Unlock()
		http.Error(w, "not found", 404)
		return
	}
	if len(n.cmds) >= 32 {
		h.mu.Unlock()
		http.Error(w, "command queue full", 429)
		return
	}
	n.cmds = append(n.cmds, c)
	h.mu.Unlock()
	h.pokeNode(id) // delivered instantly via the agent's long-poll
	h.audit.log(h.ip(r), "cmd:"+c.Kind, id, c.Arg)
	writeJSON(w, map[string]string{"id": c.ID})
}

func (h *Hub) handleRotate(w http.ResponseWriter, r *http.Request) {
	os.Remove(filepath.Join(h.cfg.Data, "enroll.token"))
	tok := readOrCreateSecret(filepath.Join(h.cfg.Data, "enroll.token"), 18)
	h.enrollMu.Lock()
	h.enroll = tok
	h.enrollMu.Unlock()
	h.audit.log(h.ip(r), "enroll-rotate", "", "")
	h.handleInfo(w, r)
}

func (h *Hub) handleInfo(w http.ResponseWriter, r *http.Request) {
	base := strings.TrimRight(hubURL(r, h), "/")
	if host := r.URL.Query().Get("host"); host == "" {
		if hn := strings.Split(r.Host, ":")[0]; hn == "localhost" || strings.HasPrefix(hn, "127.") || hn == "[::1]" {
			base = h.baseURLs()[0]
		}
	}
	h.enrollMu.RLock()
	enroll := h.enroll
	h.enrollMu.RUnlock()
	writeJSON(w, map[string]any{
		"version": version,
		"dist":    h.distList(),
		"hubOS":   runtime.GOOS + "/" + runtime.GOARCH,
		"tls":     h.tlsMode,
		"pin":     h.pin,
		"url":     base,
		"enroll":  enroll,
		"cmds": map[string]string{
			"unix": h.unixCmdTok(base, enroll, false), "unixManage": h.unixCmdTok(base, enroll, true),
			"win": h.winCmd(base, enroll, false), "winManage": h.winCmd(base, enroll, true),
			"winScript": h.winScript(base, enroll, true),
		},
		"interval": h.cfg.Interval.Milliseconds(),
		"idle":     h.cfg.Idle.Milliseconds(),
		"now":      time.Now().UnixMilli(),
	})
}

func hubURL(r *http.Request, h *Hub) string {
	if h.cfg.URL != "" {
		return h.cfg.URL
	}
	scheme := "http"
	if h.secureReq(r) {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(v)
}

// ---------------------------------------------------------------- install commands

func (h *Hub) unixCmd(base string, manage bool) string {
	h.enrollMu.RLock()
	defer h.enrollMu.RUnlock()
	return h.unixCmdTok(base, h.enroll, manage)
}

func (h *Hub) unixCmdTok(base, tok string, manage bool) string {
	m := ""
	if manage {
		m = " --manage"
	}
	if h.pin != "" {
		return fmt.Sprintf("curl -fsSLk --pinnedpubkey 'sha256//%s' %s/install.sh | sudo sh -s -- --token %s%s", strings.TrimPrefix(h.pin, "sha256/"), base, tok, m)
	}
	return fmt.Sprintf("curl -fsSL %s/install.sh | sudo sh -s -- --token %s%s", base, tok, m)
}

// winScript is a self-contained Windows PowerShell 5.1 installer. With a
// self-signed hub it pins the certificate's SHA-256 before downloading.
func (h *Hub) winScript(base, tok string, manage bool) string {
	var b strings.Builder
	b.WriteString("$ErrorActionPreference='Stop';$ProgressPreference='SilentlyContinue'\n")
	b.WriteString("[Net.ServicePointManager]::SecurityProtocol=[Net.SecurityProtocolType]::Tls12\n")
	if h.certHex != "" {
		fmt.Fprintf(&b, "$want='%s'\n", h.certHex)
		b.WriteString("[Net.ServicePointManager]::ServerCertificateValidationCallback={param($s,$c) [BitConverter]::ToString([Security.Cryptography.SHA256]::Create().ComputeHash($c.GetRawCertData())).Replace('-','') -eq $want}\n")
	}
	b.WriteString("$a=if($env:PROCESSOR_ARCHITECTURE -eq 'ARM64'){'arm64'}else{'amd64'}\n")
	b.WriteString("$t=Join-Path $env:TEMP ('argus-'+[guid]::NewGuid()+'.exe')\n")
	fmt.Fprintf(&b, "Invoke-WebRequest -UseBasicParsing \"%s/dl/argus-windows-$a.exe\" -OutFile $t\n", base)
	fmt.Fprintf(&b, "& $t install agent --hub '%s' --token '%s'", base, tok)
	if h.pin != "" {
		fmt.Fprintf(&b, " --pin '%s'", h.pin)
	}
	if manage {
		b.WriteString(" --manage")
	}
	b.WriteString("\nRemove-Item $t -Force\n")
	return b.String()
}

func (h *Hub) winCmd(base, tok string, manage bool) string {
	u := utf16.Encode([]rune(h.winScript(base, tok, manage)))
	raw := make([]byte, 2*len(u))
	for i, c := range u {
		binary.LittleEndian.PutUint16(raw[2*i:], c)
	}
	return "powershell -NoProfile -ExecutionPolicy Bypass -EncodedCommand " + base64.StdEncoding.EncodeToString(raw)
}

// ---------------------------------------------------------------- SSE

func (h *Hub) broadcast(event string, data []byte) {
	msg := make([]byte, 0, len(data)+len(event)+16)
	msg = append(msg, "event: "...)
	msg = append(msg, event...)
	msg = append(msg, "\ndata: "...)
	msg = append(msg, data...)
	msg = append(msg, "\n\n"...)
	h.subsMu.Lock()
	for s := range h.subs {
		select {
		case s.ch <- msg:
		default: // slow client: drop
		}
	}
	h.subsMu.Unlock()
}

func (h *Hub) handleStream(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "no streaming", 500)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	s := &sub{ch: make(chan []byte, 256), focus: r.URL.Query().Get("focus")}
	h.subsMu.Lock()
	first := len(h.subs) == 0
	h.subs[s] = struct{}{}
	if s.focus != "" {
		h.focus[s.focus]++
	}
	h.subsMu.Unlock()
	// Speed agents up the moment someone starts watching.
	if first {
		h.pokeAll()
	} else {
		h.pokeNode(s.focus)
	}
	defer func() {
		h.subsMu.Lock()
		delete(h.subs, s)
		if s.focus != "" {
			if h.focus[s.focus]--; h.focus[s.focus] <= 0 {
				delete(h.focus, s.focus)
			}
		}
		last := len(h.subs) == 0
		h.subsMu.Unlock()
		if last {
			h.pokeAll()
		} else {
			h.pokeNode(s.focus)
		}
	}()
	fmt.Fprintf(w, "retry: 2000\nevent: hello\ndata: {\"now\":%d}\n\n", time.Now().UnixMilli())
	fl.Flush()
	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case msg := <-s.ch:
			if _, err := w.Write(msg); err != nil {
				return
			}
			for i := len(s.ch); i > 0; i-- {
				w.Write(<-s.ch)
			}
			fl.Flush()
		case <-ping.C:
			fmt.Fprintf(w, "event: ping\ndata: {\"now\":%d}\n\n", time.Now().UnixMilli())
			fl.Flush()
		}
	}
}

// ---------------------------------------------------------------- persistence

type persisted struct {
	ID      string  `json:"id"`
	Static  *Static `json:"static"`
	Seen    int64   `json:"seen"`
	Addr    string  `json:"addr"`
	KeyHash string  `json:"keyHash,omitempty"`
	Slow    []point `json:"slow,omitempty"`
}

func (h *Hub) load() {
	b, err := os.ReadFile(filepath.Join(h.cfg.Data, "nodes.json"))
	if err != nil {
		return
	}
	var ps []persisted
	if json.Unmarshal(b, &ps) != nil {
		return
	}
	for _, p := range ps {
		n := newNode()
		n.Static, n.Seen, n.Addr, n.KeyHash = p.Static, p.Seen, p.Addr, p.KeyHash
		for _, pt := range p.Slow {
			n.slow.push(pt)
		}
		if p.Static != nil {
			for _, nic := range p.Static.NICs {
				if nic.Virt {
					n.virt[nic.Name] = true
				}
			}
		}
		h.nodes[p.ID] = n
	}
	logf("loaded %d known nodes", len(ps))
}

func (h *Hub) save() {
	h.mu.RLock()
	ps := make([]persisted, 0, len(h.nodes))
	for id, n := range h.nodes {
		ps = append(ps, persisted{ID: id, Static: n.Static, Seen: n.Seen, Addr: n.Addr, KeyHash: n.KeyHash, Slow: n.slow.slice()})
	}
	h.mu.RUnlock()
	b, _ := json.Marshal(ps)
	tmp := filepath.Join(h.cfg.Data, "nodes.json.tmp")
	if os.WriteFile(tmp, b, 0o600) == nil {
		os.Rename(tmp, filepath.Join(h.cfg.Data, "nodes.json"))
	}
}

func (h *Hub) persistLoop() {
	t := time.NewTicker(time.Minute)
	for range t.C {
		h.save()
	}
}

// ---------------------------------------------------------------- distribution

var distRe = regexp.MustCompile(`^argus-(linux|darwin|windows|freebsd)-(amd64|arm64|riscv64|arm|386|loong64|ppc64le|s390x|mips64le)(\.exe)?$`)

func (h *Hub) distDirs() []string {
	var d []string
	if h.cfg.Dist != "" {
		d = append(d, h.cfg.Dist)
	}
	ed := exeDir()
	return append(d, filepath.Join(h.cfg.Data, "dist"), "/dist", filepath.Join(ed, "dist"), ed, "dist")
}

func (h *Hub) findDist(name string) string {
	for _, d := range h.distDirs() {
		p := filepath.Join(d, name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	self := "argus-" + runtime.GOOS + "-" + runtime.GOARCH
	if strings.TrimSuffix(name, ".exe") == self {
		if p, err := os.Executable(); err == nil {
			return p
		}
	}
	return ""
}

func (h *Hub) distList() []string {
	seen := map[string]bool{}
	var out []string
	for _, d := range h.distDirs() {
		ents, _ := os.ReadDir(d)
		for _, e := range ents {
			if distRe.MatchString(e.Name()) && !seen[e.Name()] {
				seen[e.Name()] = true
				out = append(out, e.Name())
			}
		}
	}
	self := "argus-" + runtime.GOOS + "-" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		self += ".exe"
	}
	if !seen[self] {
		out = append(out, self)
	}
	sort.Strings(out)
	return out
}

func (h *Hub) handleDownload(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("file")
	if !distRe.MatchString(name) {
		http.Error(w, "unknown artifact", 404)
		return
	}
	p := h.findDist(name)
	if p == "" {
		http.Error(w, "binary "+name+" is not available on this hub — build it with ./build.sh into the hub's dist/ directory", 404)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", "attachment; filename="+name)
	http.ServeFile(w, r, p)
}

func (h *Hub) handleInstallSh(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/x-shellscript")
	s := strings.ReplaceAll(installSh, "{{HUB}}", hubURL(r, h))
	fmt.Fprint(w, strings.ReplaceAll(s, "{{PIN}}", h.pin))
}

func (h *Hub) handleInstallPs1(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	s := strings.ReplaceAll(installPs1, "{{HUB}}", hubURL(r, h))
	s = strings.ReplaceAll(s, "{{PIN}}", h.pin)
	fmt.Fprint(w, strings.ReplaceAll(s, "{{CERT}}", h.certHex))
}

const installSh = `#!/bin/sh
# Argus agent installer (served by your hub).
#   curl -fsSL {{HUB}}/install.sh | sudo sh -s -- --token ENROLL_TOKEN [--manage]
set -eu
HUB="{{HUB}}"
PIN="{{PIN}}"
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$(uname -m)" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  riscv64) ARCH=riscv64 ;;
  armv7l|armv6l|armhf) ARCH=arm ;;
  *) echo "argus: unsupported architecture $(uname -m)" >&2; exit 1 ;;
esac
case "$OS" in linux|darwin|freebsd) ;; *) echo "argus: unsupported OS $OS" >&2; exit 1 ;; esac
SUDO=""
if [ "$(id -u)" -ne 0 ]; then SUDO="sudo"; fi
TMP=$(mktemp 2>/dev/null || echo /tmp/argus.$$)
trap 'rm -f "$TMP"' EXIT
echo "→ fetching argus-$OS-$ARCH from $HUB"
if [ -n "$PIN" ]; then
  command -v curl >/dev/null 2>&1 || { echo "argus: curl is required (pinned TLS)" >&2; exit 1; }
  curl -fsSLk --pinnedpubkey "sha256//${PIN#sha256/}" "$HUB/dl/argus-$OS-$ARCH" -o "$TMP"
  set -- --pin "$PIN" "$@"
elif command -v curl >/dev/null 2>&1; then curl -fsSL "$HUB/dl/argus-$OS-$ARCH" -o "$TMP"
else wget -qO "$TMP" "$HUB/dl/argus-$OS-$ARCH"; fi
chmod +x "$TMP"
$SUDO "$TMP" install agent --hub "$HUB" "$@"
`

const installPs1 = `# Argus agent installer (served by your hub). Elevated Windows PowerShell:
#   & ([scriptblock]::Create((irm {{HUB}}/install.ps1))) -Token ENROLL_TOKEN [-Manage]
param([Parameter(Mandatory=$true)][string]$Token, [switch]$Manage, [string]$Name = "")
$ErrorActionPreference = 'Stop'; $ProgressPreference = 'SilentlyContinue'
$hub = '{{HUB}}'; $pin = '{{PIN}}'; $cert = '{{CERT}}'
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
if ($cert) {
  [Net.ServicePointManager]::ServerCertificateValidationCallback = {param($s,$c) [BitConverter]::ToString([Security.Cryptography.SHA256]::Create().ComputeHash($c.GetRawCertData())).Replace('-','') -eq $cert}
}
$arch = if ($env:PROCESSOR_ARCHITECTURE -eq 'ARM64') { 'arm64' } else { 'amd64' }
$tmp = Join-Path $env:TEMP ("argus-" + [guid]::NewGuid() + ".exe")
Invoke-WebRequest "$hub/dl/argus-windows-$arch.exe" -OutFile $tmp -UseBasicParsing
$a = @('install','agent','--hub',$hub,'--token',$Token)
if ($pin) { $a += @('--pin', $pin) }
if ($Manage) { $a += '--manage' }
if ($Name) { $a += @('--name', $Name) }
& $tmp @a
Remove-Item $tmp -Force -ErrorAction SilentlyContinue
`
