package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Agent lifecycle:
//   1. enroll once with the enroll token → receive a per-machine key (stored 0600)
//   2. POST /api/agent/report every interval (interval is chosen by the hub:
//      idle when nobody watches, fast when a dashboard is open, 1s when focused)
//   3. hold one long-poll GET /api/agent/poll open so commands and cadence
//      changes arrive instantly without polling
//   4. command results are flushed immediately

var errUnauthorized = errors.New("unauthorized")

type agent struct {
	cfg    *config
	hub    string
	client *http.Client
	poller *http.Client
	col    *Collector
	docker *dockerClient
	ex     *executor

	mu         sync.Mutex
	st         *Static
	key        string
	pending    []Result
	sendStatic bool

	wake  chan struct{}
	ivCh  chan time.Duration
	usage selfUsage
}

func tuneRuntime() {
	// Agents are tiny: cap parallelism and heap so the footprint stays flat.
	if os.Getenv("GOMAXPROCS") == "" {
		runtime.GOMAXPROCS(2)
	}
	if os.Getenv("GOMEMLIMIT") == "" {
		debug.SetMemoryLimit(48 << 20)
	}
}

func runAgent(c *config) {
	tuneRuntime()
	tr := &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		DialContext:         (&net.Dialer{Timeout: 8 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSClientConfig:     pinnedTLS(c.Pin),
		MaxIdleConns:        4,
		IdleConnTimeout:     120 * time.Second,
		TLSHandshakeTimeout: 8 * time.Second,
		ForceAttemptHTTP2:   false,
	}
	a := &agent{
		cfg:    c,
		hub:    strings.TrimRight(c.Hub, "/"),
		client: &http.Client{Timeout: 20 * time.Second, Transport: tr},
		poller: &http.Client{Timeout: 75 * time.Second, Transport: tr},
		col:    newCollector(),
		docker: newDocker(),
		wake:   make(chan struct{}, 1),
		ivCh:   make(chan time.Duration, 1),
	}
	a.st = a.static()
	a.sendStatic = true
	a.ex = &executor{manage: c.Manage, docker: a.docker, onDone: a.addResult, refresh: func() {
		ns := a.static()
		a.mu.Lock()
		a.st, a.sendStatic = ns, true
		a.mu.Unlock()
	}}
	os.MkdirAll(c.State, 0o700)
	a.key = readTrim(filepath.Join(c.State, "agent.key"))
	logf("argus agent %s → %s (id %s, manage=%v, tls-pin=%v)", a.st.Hostname, a.hub, a.st.ID, c.Manage, c.Pin != "")
	if strings.HasPrefix(a.hub, "http://") && !isPrivateHost(a.hub) {
		logf("WARNING: hub URL is plain http over a public network — use https")
	}
	for a.key == "" {
		if err := a.enroll(); err != nil {
			logf("enroll failed: %v (retrying in 15s)", err)
			time.Sleep(15 * time.Second)
		}
	}
	go a.pollLoop()
	a.loop()
}

func isPrivateHost(u string) bool {
	h := strings.TrimPrefix(strings.TrimPrefix(u, "http://"), "https://")
	h = strings.Split(strings.Split(h, "/")[0], ":")[0]
	ip := net.ParseIP(h)
	if ip == nil {
		return strings.HasSuffix(h, ".local") || strings.HasSuffix(h, ".lan") || h == "localhost" || strings.HasSuffix(h, ".ts.net")
	}
	return ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || (ip.To4() != nil && ip.To4()[0] == 100 && ip.To4()[1]&0xC0 == 64) // CGNAT/Tailscale
}

func (a *agent) static() *Static {
	st := a.col.Static()
	if a.cfg.Name != "" {
		st.Hostname = a.cfg.Name
	}
	st.Manage = a.cfg.Manage
	if a.docker != nil {
		st.Extra["Container engine"] = a.docker.describe()
	}
	return st
}

func (a *agent) addResult(r Result) {
	a.mu.Lock()
	a.pending = append(a.pending, r)
	a.mu.Unlock()
	select {
	case a.wake <- struct{}{}:
	default:
	}
}

func (a *agent) enroll() error {
	tok := a.cfg.Token
	if tok == "" {
		return errors.New("no enroll token (pass --token or put it in " + filepath.Join(a.cfg.State, "enroll.token") + ")")
	}
	body, _ := json.Marshal(map[string]string{"id": a.st.ID, "hostname": a.st.Hostname})
	req, _ := http.NewRequest("POST", a.hub+"/api/agent/enroll", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	res, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 300))
		return fmt.Errorf("hub %d: %s", res.StatusCode, strings.TrimSpace(string(b)))
	}
	var out struct{ Key string }
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil || out.Key == "" {
		return errors.New("bad enroll response")
	}
	if err := os.WriteFile(filepath.Join(a.cfg.State, "agent.key"), []byte(out.Key+"\n"), 0o600); err != nil {
		logf("warning: cannot persist agent key: %v", err)
	}
	a.mu.Lock()
	a.key, a.sendStatic = out.Key, true
	a.mu.Unlock()
	logf("enrolled with hub")
	return nil
}

func (a *agent) do(c *http.Client, method, path string, body []byte) (*ReportResp, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, _ := http.NewRequest(method, a.hub+path, rd)
	a.mu.Lock()
	req.Header.Set("Authorization", "Agent "+a.st.ID+":"+a.key)
	a.mu.Unlock()
	req.Header.Set("Content-Type", "application/json")
	res, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode == 401 {
		io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
		return nil, errUnauthorized
	}
	if res.StatusCode != 200 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 512))
		return nil, fmt.Errorf("hub %d: %s", res.StatusCode, strings.TrimSpace(string(b)))
	}
	var rr ReportResp
	if err := json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(&rr); err != nil {
		return nil, err
	}
	return &rr, nil
}

func (a *agent) handle(rr *ReportResp) {
	for _, cmd := range rr.Cmds {
		go a.ex.run(cmd)
	}
	if rr.NeedStatic {
		a.mu.Lock()
		a.sendStatic = true
		a.mu.Unlock()
	}
	if rr.Interval > 0 {
		select {
		case a.ivCh <- time.Duration(rr.Interval) * time.Millisecond:
		default:
		}
	}
}

func (a *agent) onUnauthorized() {
	logf("hub rejected agent key — re-enrolling")
	os.Remove(filepath.Join(a.cfg.State, "agent.key"))
	for {
		if err := a.enroll(); err == nil {
			return
		} else {
			logf("re-enroll failed: %v (retrying in 30s)", err)
		}
		time.Sleep(30 * time.Second)
	}
}

func (a *agent) pollLoop() {
	backoff := time.Second
	for {
		rr, err := a.do(a.poller, "GET", "/api/agent/poll", nil)
		if err != nil {
			if err == errUnauthorized {
				time.Sleep(5 * time.Second) // report loop handles re-enrollment
			} else {
				time.Sleep(backoff)
				backoff = min(backoff*2, 30*time.Second)
			}
			continue
		}
		backoff = time.Second
		a.handle(rr)
	}
}

func (a *agent) loop() {
	interval := a.cfg.Idle
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	fails := 0
	var buf bytes.Buffer
	lastNIC := nicFingerprint()
	lastFull := time.Now()
	send := func(m *Metrics) {
		a.mu.Lock()
		rep := Report{ID: a.st.ID, M: m, Results: a.pending}
		if a.sendStatic {
			rep.Static = a.st
		}
		nres := len(a.pending)
		a.mu.Unlock()
		buf.Reset()
		json.NewEncoder(&buf).Encode(&rep)
		rr, err := a.do(a.client, "POST", "/api/agent/report", buf.Bytes())
		if err == errUnauthorized {
			a.onUnauthorized()
			return
		}
		if err != nil {
			fails++
			if fails == 1 || fails%30 == 0 {
				logf("report failed (%d): %v", fails, err)
			}
			a.mu.Lock()
			a.sendStatic = true
			a.mu.Unlock()
			return
		}
		if fails > 0 {
			logf("reconnected to hub")
		}
		fails = 0
		a.mu.Lock()
		a.pending = a.pending[nres:]
		if rep.Static != nil {
			a.sendStatic = false
		}
		a.mu.Unlock()
		a.handle(rr)
	}
	for tick := 0; ; tick++ {
		m := a.col.Collect(tick)
		if a.docker != nil {
			m.Docker = a.docker.collect(tick)
		}
		m.Self = a.usage.sample()
		// Inventory stays current without a refresh button: cheap NIC
		// fingerprint every ~minute, full re-scan every 6 hours.
		if tick > 0 && tick%everyNICs == 0 {
			if fp := nicFingerprint(); fp != lastNIC || time.Since(lastFull) > 6*time.Hour {
				lastNIC, lastFull = fp, time.Now()
				go a.ex.refresh()
			}
		}
		send(m)
	wait:
		select {
		case <-ticker.C:
		case d := <-a.ivCh:
			if d != interval && d >= 250*time.Millisecond {
				faster := d < interval
				interval = d
				ticker.Reset(d)
				if faster {
					continue // collect right away so the dashboard lights up instantly
				}
			}
			goto wait
		case <-a.wake:
			a.mu.Lock()
			rep := Report{ID: a.st.ID, Results: a.pending}
			nres := len(a.pending)
			a.mu.Unlock()
			buf.Reset()
			json.NewEncoder(&buf).Encode(&rep)
			if rr, err := a.do(a.client, "POST", "/api/agent/report", buf.Bytes()); err == nil {
				a.mu.Lock()
				a.pending = a.pending[nres:]
				a.mu.Unlock()
				a.handle(rr)
			}
			goto wait
		}
	}
}

func nicFingerprint() string {
	var b strings.Builder
	ifs, _ := net.Interfaces()
	for _, i := range ifs {
		b.WriteString(i.Name)
		b.WriteString(i.Flags.String())
		as, _ := i.Addrs()
		for _, a := range as {
			b.WriteString(a.String())
		}
	}
	return b.String()
}

// ---------------------------------------------------------------- self usage

type selfUsage struct {
	lastCPU uint64
	lastT   time.Time
}

func (s *selfUsage) sample() *SelfStat {
	cpu, rss := processUsage()
	now := time.Now()
	out := &SelfStat{RSS: rss}
	if !s.lastT.IsZero() && cpu >= s.lastCPU {
		out.CPU = round2(float64(cpu-s.lastCPU) / float64(now.Sub(s.lastT).Nanoseconds()) * 100)
	}
	s.lastCPU, s.lastT = cpu, now
	return out
}

func round2(v float64) float64 { return float64(int64(v*100+0.5)) / 100 }

// ---------------------------------------------------------------- executor

type executor struct {
	manage  bool
	docker  *dockerClient
	onDone  func(Result)
	refresh func()
}

const maxOutput = 256 << 10

func (e *executor) run(cmd Command) {
	start := time.Now()
	r := Result{ID: cmd.ID, Kind: cmd.Kind, Arg: cmd.Arg}
	defer func() {
		r.Ms = time.Since(start).Milliseconds()
		r.At = time.Now().UnixMilli()
		e.onDone(r)
	}()
	if cmd.Kind == "inventory" {
		e.refresh()
		r.OK, r.Output = true, "inventory refreshed"
		return
	}
	if !e.manage {
		r.Output = "management is disabled on this agent (install with --manage to allow it)"
		r.Code = -1
		return
	}
	switch cmd.Kind {
	case "exec":
		r.Output, r.Code, r.OK = shellExec(cmd.Arg, 5*time.Minute)
	case "docker":
		if e.docker == nil {
			r.Output = "no container engine found on this machine"
			return
		}
		op, id, _ := strings.Cut(cmd.Arg, " ")
		out, err := e.docker.action(op, strings.TrimSpace(id))
		r.Output, r.OK = out, err == nil
		if err != nil {
			r.Output = strings.TrimSpace(out + "\n" + err.Error())
		}
	case "kill":
		pid, err := strconv.Atoi(cmd.Arg)
		if err != nil || pid <= 1 {
			r.Output = "bad pid"
			return
		}
		p, err := os.FindProcess(pid)
		if err == nil {
			err = p.Kill()
		}
		if err != nil {
			r.Output = err.Error()
			return
		}
		r.OK, r.Output = true, fmt.Sprintf("killed %d", pid)
	case "reboot", "shutdown":
		var argv []string
		switch runtime.GOOS {
		case "windows":
			argv = map[string][]string{"reboot": {"shutdown", "/r", "/t", "3"}, "shutdown": {"shutdown", "/s", "/t", "3"}}[cmd.Kind]
		case "darwin":
			argv = map[string][]string{"reboot": {"shutdown", "-r", "+0"}, "shutdown": {"shutdown", "-h", "+0"}}[cmd.Kind]
		default:
			if _, err := exec.LookPath("systemctl"); err == nil {
				argv = []string{"systemctl", map[string]string{"reboot": "reboot", "shutdown": "poweroff"}[cmd.Kind]}
			} else {
				argv = []string{map[string]string{"reboot": "reboot", "shutdown": "poweroff"}[cmd.Kind]}
			}
		}
		r.OK, r.Output = true, cmd.Kind+" scheduled: "+strings.Join(argv, " ")
		go func() {
			time.Sleep(2 * time.Second)
			exec.Command(argv[0], argv[1:]...).Run()
		}()
	default:
		r.Output = "unknown command " + cmd.Kind
	}
}

func shellExec(script string, timeout time.Duration) (string, int, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var c *exec.Cmd
	if runtime.GOOS == "windows" {
		c = exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script)
	} else {
		sh := "/bin/sh"
		if _, err := os.Stat("/bin/bash"); err == nil {
			sh = "/bin/bash"
		}
		c = exec.CommandContext(ctx, sh, "-c", script)
	}
	hideWindow(c)
	var out limitedBuf
	c.Stdout, c.Stderr = &out, &out
	err := c.Run()
	code := 0
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		} else {
			code = -1
			out.WriteString("\n" + err.Error())
		}
	}
	if ctx.Err() != nil {
		out.WriteString("\n[timed out]")
	}
	return out.String(), code, err == nil
}

type limitedBuf struct {
	b     bytes.Buffer
	trunc bool
}

func (l *limitedBuf) Write(p []byte) (int, error) {
	if room := maxOutput - l.b.Len(); room > 0 {
		if len(p) > room {
			l.b.Write(p[:room])
			l.trunc = true
		} else {
			l.b.Write(p)
		}
	} else {
		l.trunc = true
	}
	return len(p), nil
}
func (l *limitedBuf) WriteString(s string) { l.Write([]byte(s)) }
func (l *limitedBuf) String() string {
	if l.trunc {
		return l.b.String() + "\n[output truncated]"
	}
	return l.b.String()
}
