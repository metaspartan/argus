package main

import (
	"compress/gzip"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ---------------------------------------------------------------- capabilities

var allCaps = []string{"exec", "docker", "kill", "power", "update"}

// caps resolves --manage / --allow into the capability list the agent enforces.
func (c *config) caps() []string {
	if c.Manage {
		return allCaps
	}
	var out []string
	for _, s := range strings.Split(c.Allow, ",") {
		s = strings.TrimSpace(strings.ToLower(s))
		for _, k := range allCaps {
			if s == k || (s == "all" && k != "") {
				out = append(out, k)
			}
		}
	}
	return out
}

func hasCap(caps []string, k string) bool {
	for _, c := range caps {
		if c == k {
			return true
		}
	}
	return false
}

// capFor maps a command kind to the capability it needs.
func capFor(kind string) string {
	switch kind {
	case "exec":
		return "exec"
	case "docker":
		return "docker"
	case "kill":
		return "kill"
	case "reboot", "shutdown":
		return "power"
	case "upgrade":
		return "update"
	}
	return ""
}

// ---------------------------------------------------------------- detail mode

// detailMode is set while someone has this machine open in a dashboard.
// Expensive probes (process table, TCP states, per-container stats) only run
// at full rate then; otherwise they back off heavily.
var detailMode atomic.Bool

func heavyTick(tick, every int) bool {
	if detailMode.Load() {
		return tick%every == 0
	}
	return tick%(every*10) == 0
}

// ---------------------------------------------------------------- TOTP (RFC 6238)

func totpPath(dir string) string { return filepath.Join(dir, "totp.secret") }

func totpCode(secret []byte, step int64) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(step))
	m := hmac.New(sha1.New, secret)
	m.Write(msg[:])
	sum := m.Sum(nil)
	off := sum[len(sum)-1] & 0xf
	v := (binary.BigEndian.Uint32(sum[off:]) & 0x7fffffff) % 1000000
	return fmt.Sprintf("%06d", v)
}

var totpLast atomic.Int64 // last accepted step (replay protection)

func totpVerify(dir, code string) bool {
	raw := readTrim(totpPath(dir))
	if raw == "" {
		return true
	}
	secret, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(raw)
	if err != nil {
		return false
	}
	code = strings.TrimSpace(code)
	now := time.Now().Unix() / 30
	for _, s := range []int64{now - 1, now, now + 1} {
		if ctEq(totpCode(secret, s), code) && s > totpLast.Load() {
			totpLast.Store(s)
			return true
		}
	}
	return false
}

func totpEnabled(dir string) bool { return readTrim(totpPath(dir)) != "" }

func totpCmd(dir, action string) {
	switch action {
	case "enable":
		b := make([]byte, 20)
		rand.Read(b)
		sec := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b)
		os.MkdirAll(dir, 0o700)
		if err := os.WriteFile(totpPath(dir), []byte(sec+"\n"), 0o600); err != nil {
			fatal("%v", err)
		}
		host, _ := os.Hostname()
		u := fmt.Sprintf("otpauth://totp/Argus:%s?secret=%s&issuer=Argus&digits=6&period=30", url.PathEscape(host), sec)
		fmt.Printf("Two-factor login enabled. Add this to your authenticator app:\n\n  %s\n\n  secret: %s\n\nThe dashboard now asks for a 6-digit code (no hub restart needed).\n", u, sec)
	case "disable":
		os.Remove(totpPath(dir))
		fmt.Println("Two-factor login disabled.")
	default:
		fmt.Printf("two-factor login: %v\nusage: argus totp enable|disable [--data DIR]\n", map[bool]string{true: "enabled", false: "disabled"}[totpEnabled(dir)])
	}
}

// ---------------------------------------------------------------- admin network allowlist

type cidrList []*net.IPNet

func parseCIDRs(s string) cidrList {
	var out cidrList
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if !strings.Contains(p, "/") {
			if strings.Contains(p, ":") {
				p += "/128"
			} else {
				p += "/32"
			}
		}
		_, n, err := net.ParseCIDR(p)
		if err != nil {
			fatal("bad --admin-cidr entry %q", p)
		}
		out = append(out, n)
	}
	return out
}

func (l cidrList) allows(ip string) bool {
	if len(l) == 0 {
		return true
	}
	addr := net.ParseIP(ip)
	if addr == nil {
		return false
	}
	if addr.IsLoopback() {
		return true
	}
	for _, n := range l {
		if n.Contains(addr) {
			return true
		}
	}
	return false
}

// agentPath reports routes agents/installers must always reach.
func agentPath(p string) bool {
	return strings.HasPrefix(p, "/api/agent/") || strings.HasPrefix(p, "/dl/") || p == "/install.sh" || p == "/install.ps1" || p == "/healthz"
}

// ---------------------------------------------------------------- gzip

type gzWriter struct {
	http.ResponseWriter
	gz *gzip.Writer
}

func (g *gzWriter) Write(b []byte) (int, error) { return g.gz.Write(b) }

var gzPool = sync.Pool{New: func() any { w, _ := gzip.NewWriterLevel(io.Discard, gzip.BestSpeed); return w }}

// gzipped compresses dashboard responses (assets, history JSON). It skips the
// event stream, long-polls and binary downloads.
func gzipped(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") || p == "/api/stream" || strings.HasPrefix(p, "/api/agent/") || strings.HasPrefix(p, "/dl/") {
			next.ServeHTTP(w, r)
			return
		}
		gz := gzPool.Get().(*gzip.Writer)
		gz.Reset(w)
		defer func() { gz.Close(); gzPool.Put(gz) }()
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Add("Vary", "Accept-Encoding")
		w.Header().Del("Content-Length")
		next.ServeHTTP(&gzWriter{ResponseWriter: w, gz: gz}, r)
	})
}

// gzipBody transparently decompresses agent reports sent with Content-Encoding: gzip.
func gzipBody(r *http.Request, limit int64) (io.Reader, func(), error) {
	body := io.LimitReader(r.Body, limit)
	if r.Header.Get("Content-Encoding") != "gzip" {
		return body, func() {}, nil
	}
	gz, err := gzip.NewReader(body)
	if err != nil {
		return nil, func() {}, err
	}
	return io.LimitReader(gz, limit*4), func() { gz.Close() }, nil // cap decompressed size (zip bombs)
}

// ---------------------------------------------------------------- self-update

func distName() string {
	n := "argus-" + runtime.GOOS + "-" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		n += ".exe"
	}
	return n
}

func fileSHA256(p string) string {
	f, err := os.Open(p)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil))
}

// selfUpdate downloads this platform's binary from the hub over the agent's
// (pinned) TLS connection, verifies its SHA-256 against the hub's header,
// swaps it in place and restarts.
func (a *agent) selfUpdate() (string, error) {
	req, _ := http.NewRequest("GET", a.hub+"/dl/"+distName(), nil)
	a.mu.Lock()
	req.Header.Set("Authorization", "Agent "+a.st.ID+":"+a.key)
	a.mu.Unlock()
	cl := *a.client
	cl.Timeout = 5 * time.Minute
	res, err := cl.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return "", fmt.Errorf("download failed: HTTP %d", res.StatusCode)
	}
	want := res.Header.Get("X-Argus-SHA256")
	newVer := res.Header.Get("X-Argus-Version")
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	if r, err := filepath.EvalSymlinks(self); err == nil {
		self = r
	}
	tmp := self + ".new"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return "", fmt.Errorf("cannot write next to %s: %v", self, err)
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(res.Body, 200<<20))
	f.Close()
	if err != nil {
		os.Remove(tmp)
		return "", err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if want == "" || !ctEq(got, want) {
		os.Remove(tmp)
		return "", fmt.Errorf("checksum mismatch (got %s, hub says %q)", got[:12], want)
	}
	if got == fileSHA256(self) {
		os.Remove(tmp)
		return "already up to date (" + version + ")", nil
	}
	if runtime.GOOS == "windows" {
		os.Remove(self + ".old")
		if err := os.Rename(self, self+".old"); err != nil {
			os.Remove(tmp)
			return "", err
		}
	}
	if err := os.Rename(tmp, self); err != nil {
		return "", err
	}
	go func() {
		time.Sleep(1500 * time.Millisecond) // let the result reach the hub
		restartSelf(self)
	}()
	return fmt.Sprintf("updated %s → %s (%d bytes, sha256 %s…); restarting", version, newVer, n, got[:12]), nil
}
