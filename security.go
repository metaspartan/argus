package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Security model
//
//   admin token   → dashboard login only (never appears in install commands)
//   enroll token  → lets a NEW agent register; rotatable from the dashboard
//   agent key     → per-machine secret issued at enrollment, stored hashed on the hub,
//                   revoked when the machine is forgotten
//   sessions      → random server-side session IDs in an HttpOnly/SameSite=Strict cookie
//   TLS           → auto self-signed cert (agents + installers pin its public key),
//                   or your own cert, or off behind a reverse proxy
//
// Management (exec/kill/power) is additionally gated on each agent by --manage.

func sha256hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func ctEq(a, b string) bool {
	return len(a) > 0 && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func readOrCreateSecret(path string, n int) string {
	if b, err := os.ReadFile(path); err == nil {
		if s := strings.TrimSpace(string(b)); s != "" {
			return s
		}
	}
	b := make([]byte, n)
	rand.Read(b)
	s := base64.RawURLEncoding.EncodeToString(b)
	os.WriteFile(path, []byte(s+"\n"), 0o600)
	return s
}

// ---------------------------------------------------------------- sessions

type sessionStore struct {
	mu   sync.Mutex
	path string
	m    map[string]int64 // sha256(session id) -> expiry unix
}

const sessionTTL = 30 * 24 * time.Hour

func newSessionStore(dir string) *sessionStore {
	s := &sessionStore{path: filepath.Join(dir, "sessions.json"), m: map[string]int64{}}
	if b, err := os.ReadFile(s.path); err == nil {
		json.Unmarshal(b, &s.m)
	}
	return s
}

func (s *sessionStore) create() string {
	b := make([]byte, 32)
	rand.Read(b)
	id := base64.RawURLEncoding.EncodeToString(b)
	s.mu.Lock()
	now := time.Now().Unix()
	for k, exp := range s.m {
		if exp < now {
			delete(s.m, k)
		}
	}
	s.m[sha256hex(id)] = time.Now().Add(sessionTTL).Unix()
	s.saveLocked()
	s.mu.Unlock()
	return id
}

func (s *sessionStore) valid(id string) bool {
	if id == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.m[sha256hex(id)]
	return ok && exp > time.Now().Unix()
}

func (s *sessionStore) delete(id string) {
	s.mu.Lock()
	delete(s.m, sha256hex(id))
	s.saveLocked()
	s.mu.Unlock()
}

func (s *sessionStore) clear() {
	s.mu.Lock()
	s.m = map[string]int64{}
	s.saveLocked()
	s.mu.Unlock()
}

func (s *sessionStore) saveLocked() {
	b, _ := json.Marshal(s.m)
	os.WriteFile(s.path, b, 0o600)
}

// ---------------------------------------------------------------- rate limiting

// limiter tracks failed auth attempts per client IP (token bucket-ish).
type limiter struct {
	mu sync.Mutex
	m  map[string]*bucket
}

type bucket struct {
	fails int
	reset time.Time
}

func newLimiter() *limiter { return &limiter{m: map[string]*bucket{}} }

const maxFails = 10

func (l *limiter) blocked(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.m[ip]
	if b == nil || time.Now().After(b.reset) {
		return false
	}
	return b.fails >= maxFails
}

func (l *limiter) fail(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.m[ip]
	if b == nil || time.Now().After(b.reset) {
		b = &bucket{reset: time.Now().Add(10 * time.Minute)}
		l.m[ip] = b
	}
	b.fails++
	if len(l.m) > 10000 { // bound memory under spray attacks
		for k, v := range l.m {
			if time.Now().After(v.reset) {
				delete(l.m, k)
			}
		}
	}
}

func clientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if f := r.Header.Get("X-Forwarded-For"); f != "" {
			return strings.TrimSpace(strings.Split(f, ",")[0])
		}
	}
	h, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return h
}

// ---------------------------------------------------------------- TLS

// spkiPin returns "sha256/<base64>" of the certificate's SubjectPublicKeyInfo
// (the same format curl's --pinnedpubkey uses, minus one slash).
func spkiPin(cert *x509.Certificate) string {
	h := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return "sha256/" + base64.StdEncoding.EncodeToString(h[:])
}

func certSHA256Hex(cert *x509.Certificate) string {
	h := sha256.Sum256(cert.Raw)
	return strings.ToUpper(hex.EncodeToString(h[:]))
}

// loadOrCreateCert returns a persistent self-signed ECDSA P-256 certificate.
// It is regenerated only if missing or within 30 days of expiry. The key stays
// the same across renewals so agent pins remain valid.
func loadOrCreateCert(dir string, extraHosts []string) (tls.Certificate, *x509.Certificate, error) {
	certPath, keyPath := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	var key *ecdsa.PrivateKey
	if kb, err := os.ReadFile(keyPath); err == nil {
		if blk, _ := pem.Decode(kb); blk != nil {
			key, _ = x509.ParseECPrivateKey(blk.Bytes)
		}
	}
	if key == nil {
		var err error
		if key, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader); err != nil {
			return tls.Certificate{}, nil, err
		}
		kb, _ := x509.MarshalECPrivateKey(key)
		if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}), 0o600); err != nil {
			return tls.Certificate{}, nil, err
		}
		os.Remove(certPath)
	}
	if cb, err := os.ReadFile(certPath); err == nil {
		if blk, _ := pem.Decode(cb); blk != nil {
			if c, err := x509.ParseCertificate(blk.Bytes); err == nil && time.Until(c.NotAfter) > 30*24*time.Hour && sameHosts(c, extraHosts) {
				return tls.Certificate{Certificate: [][]byte{c.Raw}, PrivateKey: key, Leaf: c}, c, nil
			}
		}
	}
	host, _ := os.Hostname()
	tpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: "argus hub " + host, Organization: []string{"Argus"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}
	if host != "" {
		tpl.DNSNames = append(tpl.DNSNames, host, trimHost(host)+".local")
	}
	for _, h := range append(allIPs(), extraHosts...) {
		if ip := net.ParseIP(h); ip != nil {
			tpl.IPAddresses = append(tpl.IPAddresses, ip)
		} else if h != "" {
			tpl.DNSNames = append(tpl.DNSNames, h)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644)
	c, _ := x509.ParseCertificate(der)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: c}, c, nil
}

func sameHosts(c *x509.Certificate, extra []string) bool {
	have := map[string]bool{}
	for _, d := range c.DNSNames {
		have[d] = true
	}
	for _, ip := range c.IPAddresses {
		have[ip.String()] = true
	}
	for _, h := range extra {
		if h != "" && !have[h] {
			return false
		}
	}
	return true
}

func allIPs() []string {
	var out []string
	ifs, _ := net.Interfaces()
	for _, i := range ifs {
		as, _ := i.Addrs()
		for _, a := range as {
			if ipn, ok := a.(*net.IPNet); ok && !ipn.IP.IsLinkLocalUnicast() {
				out = append(out, ipn.IP.String())
			}
		}
	}
	return out
}

// pinnedTLS returns a client TLS config that trusts exactly the pinned public key
// (self-signed hub) — or the system roots when no pin is given.
func pinnedTLS(pin string) *tls.Config {
	if pin == "" {
		return &tls.Config{MinVersion: tls.VersionTLS12}
	}
	return &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: true, // replaced by the pin check below
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			if len(raw) == 0 {
				return errors.New("no server certificate")
			}
			c, err := x509.ParseCertificate(raw[0])
			if err != nil {
				return err
			}
			if got := spkiPin(c); !ctEq(got, pin) {
				return fmt.Errorf("hub certificate pin mismatch: got %s", got)
			}
			return nil
		},
	}
}

// ---------------------------------------------------------------- audit

type auditLog struct {
	mu sync.Mutex
	f  *os.File
}

func openAudit(dir string) *auditLog {
	f, err := os.OpenFile(filepath.Join(dir, "audit.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return &auditLog{}
	}
	return &auditLog{f: f}
}

func (a *auditLog) log(ip, action, node, detail string) {
	b, _ := json.Marshal(map[string]any{"t": time.Now().UTC().Format(time.RFC3339), "ip": ip, "action": action, "node": node, "detail": detail})
	logf("audit %s", b)
	if a.f == nil {
		return
	}
	a.mu.Lock()
	a.f.Write(append(b, '\n'))
	a.mu.Unlock()
}

// ---------------------------------------------------------------- headers

func secureHeaders(next http.Handler, tlsOn bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hd := w.Header()
		hd.Set("X-Content-Type-Options", "nosniff")
		hd.Set("X-Frame-Options", "DENY")
		hd.Set("Referrer-Policy", "no-referrer")
		hd.Set("Cross-Origin-Opener-Policy", "same-origin")
		hd.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		hd.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		if tlsOn {
			hd.Set("Strict-Transport-Security", "max-age=31536000")
		}
		next.ServeHTTP(w, r)
	})
}
