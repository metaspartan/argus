package main

import (
	"bytes"
	"compress/gzip"
	"crypto/tls"
	"encoding/base32"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTOTPVector(t *testing.T) {
	// RFC 6238 Appendix B (SHA-1), truncated to 6 digits.
	secret := []byte("12345678901234567890")
	for _, c := range []struct {
		t    int64
		want string
	}{{59, "287082"}, {1111111109, "081804"}, {1234567890, "005924"}, {2000000000, "279037"}} {
		if got := totpCode(secret, c.t/30); got != c.want {
			t.Fatalf("t=%d got %s want %s", c.t, got, c.want)
		}
	}
}

func TestTOTPVerifyAndReplay(t *testing.T) {
	dir := t.TempDir()
	if !totpVerify(dir, "") {
		t.Fatal("no secret configured should not require a code")
	}
	raw := []byte("abcdefghijabcdefghij")
	enc := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw)
	os.WriteFile(filepath.Join(dir, "totp.secret"), []byte(enc), 0o600)
	totpLast.Store(0)
	code := totpCode(raw, time.Now().Unix()/30)
	if totpVerify(dir, "000000") && code != "000000" {
		t.Fatal("wrong code accepted")
	}
	if !totpVerify(dir, code) {
		t.Fatal("valid code rejected")
	}
	if totpVerify(dir, code) {
		t.Fatal("replayed code accepted")
	}
}

func TestCIDR(t *testing.T) {
	l := parseCIDRs("192.168.0.0/16, 100.64.0.0/10,10.1.2.3")
	for ip, want := range map[string]bool{"192.168.1.5": true, "100.67.72.16": true, "10.1.2.3": true, "10.1.2.4": false, "8.8.8.8": false, "127.0.0.1": true, "garbage": false} {
		if got := l.allows(ip); got != want {
			t.Errorf("%s: got %v want %v", ip, got, want)
		}
	}
	if !cidrList(nil).allows("8.8.8.8") {
		t.Error("empty list must allow all")
	}
	for _, p := range []string{"/api/agent/report", "/dl/argus-linux-amd64", "/install.sh", "/healthz"} {
		if !agentPath(p) {
			t.Errorf("%s must stay reachable for agents", p)
		}
	}
	if agentPath("/api/nodes") || agentPath("/") {
		t.Error("dashboard paths must be gated")
	}
}

func TestCaps(t *testing.T) {
	if c := (&config{Manage: true}).caps(); len(c) != len(allCaps) {
		t.Fatalf("--manage should grant all, got %v", c)
	}
	c := (&config{Allow: "docker, update,bogus"}).caps()
	if !hasCap(c, "docker") || !hasCap(c, "update") || hasCap(c, "exec") || len(c) != 2 {
		t.Fatalf("granular caps wrong: %v", c)
	}
	if len((&config{}).caps()) != 0 {
		t.Fatal("default must be read-only")
	}
	for kind, want := range map[string]string{"exec": "exec", "reboot": "power", "shutdown": "power", "upgrade": "update", "docker": "docker", "kill": "kill", "inventory": ""} {
		if capFor(kind) != want {
			t.Errorf("capFor(%s)=%s", kind, capFor(kind))
		}
	}
}

func TestPinnedTLS(t *testing.T) {
	cert, leaf, err := loadOrCreateCert(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) }))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	srv.StartTLS()
	defer srv.Close()
	good := &http.Client{Transport: &http.Transport{TLSClientConfig: pinnedTLS(spkiPin(leaf))}}
	if _, err := good.Get(srv.URL); err != nil {
		t.Fatalf("correct pin rejected: %v", err)
	}
	bad := &http.Client{Transport: &http.Transport{TLSClientConfig: pinnedTLS("sha256/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")}}
	if _, err := bad.Get(srv.URL); err == nil || !strings.Contains(err.Error(), "pin mismatch") {
		t.Fatalf("wrong pin accepted: %v", err)
	}
	roots := &http.Client{Transport: &http.Transport{TLSClientConfig: pinnedTLS("")}}
	if _, err := roots.Get(srv.URL); err == nil {
		t.Fatal("self-signed cert accepted without a pin")
	}
}

func TestGzipBombCapped(t *testing.T) {
	var zb bytes.Buffer
	zw := gzip.NewWriter(&zb)
	zw.Write(bytes.Repeat([]byte("A"), 64<<20)) // 64 MB of zeros compresses to ~64 KB
	zw.Close()
	r := httptest.NewRequest("POST", "/api/agent/report", bytes.NewReader(zb.Bytes()))
	r.Header.Set("Content-Encoding", "gzip")
	body, done, err := gzipBody(r, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	n, _ := io.Copy(io.Discard, body)
	if n > 4<<20 {
		t.Fatalf("decompressed %d bytes, cap not enforced", n)
	}
}

func TestLiteMetricsStripsHeavyFields(t *testing.T) {
	m := &Metrics{CPU: 5, Procs: []Proc{{PID: 1}}, Cores: []float64{1, 2}, Docker: &DockerState{Running: 2, Total: 3, Containers: []Container{{ID: "x"}}}}
	l := liteMetrics(m)
	if l.Procs != nil || l.Cores != nil || l.Docker.Containers != nil || l.Docker.Running != 2 || l.CPU != 5 {
		t.Fatalf("lite metrics wrong: %+v", l)
	}
	if m.Procs == nil || m.Docker.Containers == nil {
		t.Fatal("liteMetrics must not mutate the original")
	}
}
