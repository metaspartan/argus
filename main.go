// Argus — tiny, dependency-free fleet monitor & manager.
// One static binary: `argus hub` serves the dashboard + API, `argus agent` reports in.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

var version = "0.2.0"

const usage = `argus %s — dependency-free fleet monitor

Usage:
  argus hub      [--listen :7070] [--data DIR] [--tls auto|off] [--tls-cert F --tls-key F]
                 [--host NAME,...] [--no-local] [--manage] [--trust-proxy]
  argus agent    --hub https://HOST:7070 --token ENROLL [--pin sha256/...] [--manage]
  argus install  hub|agent [flags]    install as a service (systemd/OpenRC/launchd/Task Scheduler)
  argus uninstall                     remove services
  argus token    [--data DIR] [--rotate-admin] [--rotate-enroll]   show / rotate hub secrets
  argus snapshot                      print local inventory + metrics as JSON
  argus version
`

type config struct {
	Listen, Data, Hub, Name, Dist string
	Token, AdminToken, Pin, State string
	TLS, TLSCert, TLSKey, Hosts   string
	URL                           string
	Manage, NoLocal, TrustProxy   bool
	RotateAdmin, RotateEnroll     bool
	Interval, Idle                time.Duration
}

func parseFlags(args []string) *config {
	c := &config{}
	fs := flag.NewFlagSet("argus", flag.ExitOnError)
	fs.StringVar(&c.Listen, "listen", ":7070", "hub listen address")
	fs.StringVar(&c.Data, "data", defaultDataDir(), "hub data directory")
	fs.StringVar(&c.TLS, "tls", "auto", "hub TLS: auto (self-signed, pinned) | off (behind a TLS proxy / trusted LAN)")
	fs.StringVar(&c.TLSCert, "tls-cert", "", "hub: PEM certificate (e.g. Let's Encrypt / tailscale cert)")
	fs.StringVar(&c.TLSKey, "tls-key", "", "hub: PEM private key")
	fs.StringVar(&c.URL, "url", os.Getenv("ARGUS_URL"), "hub: public URL agents should use (e.g. https://argus.example.com)")
	fs.StringVar(&c.Hosts, "host", "", "hub: extra DNS names/IPs for the self-signed cert, comma separated")
	fs.StringVar(&c.AdminToken, "admin-token", os.Getenv("ARGUS_ADMIN_TOKEN"), "hub: dashboard login token (default: generated, in <data>/admin.token)")
	fs.StringVar(&c.Token, "token", os.Getenv("ARGUS_TOKEN"), "enrollment token (hub: override; agent: used to enroll)")
	fs.StringVar(&c.Hub, "hub", os.Getenv("ARGUS_HUB"), "agent: hub URL")
	fs.StringVar(&c.Pin, "pin", os.Getenv("ARGUS_PIN"), "agent: hub certificate public-key pin (sha256/...) for self-signed hubs")
	fs.StringVar(&c.State, "state", "", "agent: state directory (agent key)")
	fs.BoolVar(&c.Manage, "manage", false, "allow remote management (exec, containers, kill, reboot, shutdown)")
	fs.BoolVar(&c.NoLocal, "no-local", false, "hub: don't monitor the hub machine itself")
	fs.BoolVar(&c.TrustProxy, "trust-proxy", false, "hub: trust X-Forwarded-For / -Proto from a reverse proxy")
	fs.BoolVar(&c.RotateAdmin, "rotate-admin", false, "token: generate a new admin token (logs everyone out)")
	fs.BoolVar(&c.RotateEnroll, "rotate-enroll", false, "token: generate a new enrollment token")
	fs.DurationVar(&c.Interval, "interval", 2*time.Second, "collection interval while a dashboard is watching")
	fs.DurationVar(&c.Idle, "idle-interval", 10*time.Second, "collection interval while nobody is watching")
	fs.StringVar(&c.Name, "name", "", "override reported hostname")
	fs.StringVar(&c.Dist, "dist", "", "hub: directory of agent binaries to serve (argus-<os>-<arch>)")
	fs.Parse(args)
	c.Interval = max(c.Interval, 500*time.Millisecond)
	c.Idle = max(c.Idle, c.Interval)
	if c.State == "" {
		c.State = c.Data
	}
	if c.Token == "" {
		c.Token = readTrim(filepath.Join(c.State, "enroll.token"))
	}
	return c
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, usage, version)
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	switch cmd {
	case "hub", "server":
		runHub(parseFlags(args))
	case "agent":
		c := parseFlags(args)
		if c.Hub == "" {
			fatal("agent requires --hub")
		}
		runAgent(c)
	case "install":
		if len(args) < 1 || (args[0] != "hub" && args[0] != "agent") {
			fatal("usage: argus install hub|agent [flags]")
		}
		installService(args[0], args[1:])
	case "uninstall":
		uninstallService()
	case "token", "tokens":
		c := parseFlags(args)
		admin, enroll := filepath.Join(c.Data, "admin.token"), filepath.Join(c.Data, "enroll.token")
		if c.RotateAdmin {
			os.Remove(admin)
			os.Remove(filepath.Join(c.Data, "sessions.json"))
		}
		if c.RotateEnroll {
			os.Remove(enroll)
		}
		os.MkdirAll(c.Data, 0o700)
		fmt.Printf("admin token (dashboard login): %s\nenroll token (agent install):   %s\n", readOrCreateSecret(admin, 24), readOrCreateSecret(enroll, 18))
		if c.RotateAdmin || c.RotateEnroll {
			fmt.Println("restart the hub to apply")
		}
	case "snapshot":
		col := newCollector()
		st := col.Static()
		col.Collect(0)
		time.Sleep(time.Second)
		m := col.Collect(3)
		if d := newDocker(); d != nil {
			d.collect(0)
			time.Sleep(time.Second)
			m.Docker = d.collect(2)
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(map[string]any{"static": st, "metrics": m})
	case "version", "-v", "--version":
		fmt.Printf("argus %s %s/%s\n", version, runtime.GOOS, runtime.GOARCH)
	default:
		fmt.Fprintf(os.Stderr, usage, version)
		os.Exit(2)
	}
}

func fatal(f string, a ...any) {
	fmt.Fprintf(os.Stderr, "argus: "+f+"\n", a...)
	os.Exit(1)
}

func logf(f string, a ...any) {
	fmt.Fprintf(os.Stderr, time.Now().Format("2006-01-02 15:04:05 ")+f+"\n", a...)
}

func trimHost(h string) string {
	return strings.TrimSuffix(strings.TrimSuffix(h, ".local"), ".lan")
}
