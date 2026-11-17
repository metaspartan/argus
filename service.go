package main

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode/utf16"
)

// Service installation for systemd, OpenRC, launchd and Windows Task Scheduler.

func defaultDataDir() string {
	if isAdmin() {
		return systemDataDir()
	}
	if d, err := os.UserConfigDir(); err == nil {
		return filepath.Join(d, "argus")
	}
	return "argus-data"
}

func systemDataDir() string {
	switch runtime.GOOS {
	case "windows":
		pd := os.Getenv("ProgramData")
		if pd == "" {
			pd = `C:\ProgramData`
		}
		return filepath.Join(pd, "Argus")
	case "darwin":
		return "/Library/Application Support/Argus"
	default:
		return "/var/lib/argus"
	}
}

func binPath() string {
	if runtime.GOOS == "windows" {
		pf := os.Getenv("ProgramFiles")
		if pf == "" {
			pf = `C:\Program Files`
		}
		return filepath.Join(pf, "Argus", "argus.exe")
	}
	return "/usr/local/bin/argus"
}

func copyFile(src, dst string, mode os.FileMode) error {
	if s, _ := filepath.Abs(src); s == dst {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	os.MkdirAll(filepath.Dir(dst), 0o755)
	tmp := dst + ".new"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	out.Close()
	if runtime.GOOS == "windows" {
		os.Rename(dst, dst+".old") // running exe can be renamed, not overwritten
	}
	return os.Rename(tmp, dst)
}

func run(name string, args ...string) error {
	c := exec.Command(name, args...)
	c.Stdout, c.Stderr = os.Stdout, os.Stderr
	return c.Run()
}

func installService(role string, args []string) {
	if !isAdmin() {
		fatal("install needs root / Administrator")
	}
	c := parseFlags(args)
	dataDir := systemDataDir()
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		fatal("%v", err)
	}
	lockDown(dataDir)
	if role == "agent" {
		if c.Hub == "" || c.Token == "" {
			fatal("install agent requires --hub and --token (the hub's enroll token)")
		}
		// Store the enroll token root-only; it never appears in the service definition.
		os.WriteFile(filepath.Join(dataDir, "enroll.token"), []byte(c.Token+"\n"), 0o600)
		cl := &http.Client{Timeout: 8 * time.Second, Transport: &http.Transport{TLSClientConfig: pinnedTLS(c.Pin)}}
		if res, err := cl.Get(strings.TrimRight(c.Hub, "/") + "/healthz"); err != nil {
			fmt.Fprintf(os.Stderr, "warning: hub not reachable yet (%v) — installing anyway; the agent retries\n", err)
		} else {
			res.Body.Close()
		}
	}
	if role == "hub" {
		readOrCreateSecret(filepath.Join(dataDir, "admin.token"), 24)
		readOrCreateSecret(filepath.Join(dataDir, "enroll.token"), 18)
		// Bring sibling agent binaries along so the hub can serve them.
		dist := filepath.Join(dataDir, "dist")
		os.MkdirAll(dist, 0o755)
		for _, dir := range []string{exeDir(), filepath.Join(exeDir(), "dist")} {
			ents, _ := os.ReadDir(dir)
			for _, e := range ents {
				if distRe.MatchString(e.Name()) {
					copyFile(filepath.Join(dir, e.Name()), filepath.Join(dist, e.Name()), 0o755)
				}
			}
		}
	}

	self, err := os.Executable()
	if err != nil {
		fatal("%v", err)
	}
	bin := binPath()
	if err := copyFile(self, bin, 0o755); err != nil {
		fatal("copy binary: %v", err)
	}

	argv := []string{role}
	if role == "hub" {
		argv = append(argv, "--listen", c.Listen, "--data", dataDir, "--tls", c.TLS)
		for _, kv := range [][2]string{{"--tls-cert", c.TLSCert}, {"--tls-key", c.TLSKey}, {"--host", c.Hosts}, {"--url", c.URL}} {
			if kv[1] != "" {
				argv = append(argv, kv[0], kv[1])
			}
		}
		if c.NoLocal {
			argv = append(argv, "--no-local")
		}
		if c.TrustProxy {
			argv = append(argv, "--trust-proxy")
		}
	} else {
		argv = append(argv, "--hub", c.Hub, "--state", dataDir)
		if c.Pin != "" {
			argv = append(argv, "--pin", c.Pin)
		}
	}
	if c.Manage {
		argv = append(argv, "--manage")
	} else if caps := c.caps(); len(caps) > 0 && role == "agent" {
		argv = append(argv, "--allow", strings.Join(caps, ","))
	}
	if c.AdminCIDR != "" && role == "hub" {
		argv = append(argv, "--admin-cidr", c.AdminCIDR)
	}
	if c.Interval != 2*time.Second {
		argv = append(argv, "--interval", c.Interval.String())
	}
	if c.Idle != 10*time.Second {
		argv = append(argv, "--idle-interval", c.Idle.String())
	}
	if c.Name != "" {
		argv = append(argv, "--name", c.Name)
	}
	svc := "argus-" + role
	caps := c.caps()
	hardened := role == "agent" && !hasCap(caps, "exec") && !hasCap(caps, "power") && !hasCap(caps, "update")
	if role == "agent" {
		os.Remove(filepath.Join(dataDir, "agent.key")) // fresh enrollment on (re)install
		cfg, _ := json.MarshalIndent(map[string]any{"hub": c.Hub, "pin": c.Pin, "name": c.Name, "caps": caps, "installed": time.Now().Format(time.RFC3339), "version": version}, "", "  ")
		os.WriteFile(filepath.Join(dataDir, "agent.json"), cfg, 0o600)
	}

	switch runtime.GOOS {
	case "linux", "freebsd":
		installUnix(svc, bin, argv, dataDir, hardened)
	case "darwin":
		installLaunchd(svc, bin, argv)
	case "windows":
		installWindows(svc, bin, argv, role, c.Listen)
	default:
		fatal("service install not supported on %s", runtime.GOOS)
	}
	fmt.Printf("✓ argus %s %s installed and started (%s)\n", role, version, svc)
	if role == "agent" {
		ok := false
		for i := 0; i < 40 && !ok; i++ {
			time.Sleep(500 * time.Millisecond)
			ok = readTrim(filepath.Join(dataDir, "agent.key")) != ""
		}
		if ok {
			fmt.Printf("✓ enrolled with %s — this machine is now live in the dashboard\n", c.Hub)
		} else {
			fmt.Printf("! not enrolled yet. Check: argus status   (the agent keeps retrying in the background)\n")
		}
		if len(caps) > 0 {
			fmt.Printf("  remote management: %s\n", strings.Join(caps, ", "))
		} else {
			fmt.Printf("  read-only agent (sandboxed); reinstall with --manage or --allow to enable actions\n")
		}
	}
	if role == "hub" {
		scheme := "https"
		if c.TLS == "off" && c.TLSCert == "" {
			scheme = "http"
		}
		_, port, _ := strings.Cut(c.Listen, ":")
		base := c.URL
		if base == "" {
			base = fmt.Sprintf("%s://%s:%s", scheme, lanIPs()[0], port)
		}
		fmt.Printf("  dashboard:    %s\n  admin token:  %s   (dashboard login)\n  enroll token: %s   (agent installs — see 'Add machine' in the dashboard)\n",
			base, readTrim(filepath.Join(dataDir, "admin.token")), readTrim(filepath.Join(dataDir, "enroll.token")))
		fmt.Printf("  2FA:          sudo argus totp enable   (recommended for internet-facing hubs)\n")
		if out, err := exec.Command("ufw", "status").Output(); err == nil && strings.Contains(string(out), "Status: active") {
			fmt.Printf("  firewall:     ufw is active — allow agents in with:  sudo ufw allow %s/tcp\n", port)
		}
	}
}

// lockDown makes the data directory readable by root/SYSTEM/Administrators only.
func lockDown(dir string) {
	if runtime.GOOS == "windows" {
		exec.Command("icacls", dir, "/inheritance:r", "/grant:r", "*S-1-5-18:(OI)(CI)F", "*S-1-5-32-544:(OI)(CI)F").Run()
		return
	}
	os.Chmod(dir, 0o700)
}

// hardening sandboxes read-only agents (no --manage): they only read /proc,
// /sys and the container socket, so the rest of the system is made read-only.
func hardening(on bool, dataDir string) string {
	if !on {
		return ""
	}
	return "NoNewPrivileges=yes\nProtectSystem=strict\nProtectHome=read-only\nPrivateTmp=yes\nProtectKernelTunables=yes\nProtectKernelModules=yes\nProtectControlGroups=yes\nRestrictSUIDSGID=yes\nLockPersonality=yes\nReadWritePaths=" + dataDir + "\n"
}

func shellQuote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\"'\\$`") {
		return s
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "$", `\$`, "`", "\\`").Replace(s) + `"`
}

func installUnix(svc, bin string, argv []string, dataDir string, hardened bool) {
	os.MkdirAll("/etc/argus", 0o700)
	q := make([]string, len(argv))
	for i, a := range argv {
		q[i] = shellQuote(a)
	}
	cmdline := bin + " " + strings.Join(q, " ")

	if st, err := os.Stat("/run/systemd/system"); err == nil && st.IsDir() {
		unit := fmt.Sprintf(`[Unit]
Description=Argus %s
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=%s
Restart=always
RestartSec=3
Nice=5
MemoryMax=256M
TasksMax=128
%s
[Install]
WantedBy=multi-user.target
`, strings.TrimPrefix(svc, "argus-"), cmdline, hardening(hardened, dataDir))
		if err := os.WriteFile("/etc/systemd/system/"+svc+".service", []byte(unit), 0o644); err != nil {
			fatal("%v", err)
		}
		run("systemctl", "daemon-reload")
		run("systemctl", "enable", svc)
		if err := run("systemctl", "restart", svc); err != nil {
			fatal("systemctl restart failed: %v", err)
		}
		return
	}
	if _, err := os.Stat("/sbin/openrc-run"); err == nil {
		script := fmt.Sprintf(`#!/sbin/openrc-run
description="Argus %s"
supervisor=supervise-daemon
command=%s
command_args=%q
respawn_delay=3
output_log=/var/log/%s.log
error_log=/var/log/%s.log
depend() { need net; }
`, svc, bin, strings.Join(q, " "), svc, svc)
		os.WriteFile("/etc/init.d/"+svc, []byte(script), 0o755)
		run("rc-update", "add", svc, "default")
		run("rc-service", svc, "restart")
		return
	}
	// Last resort: cron @reboot + start now.
	wrapper := "/etc/argus/" + svc + ".sh"
	os.WriteFile(wrapper, []byte(fmt.Sprintf("#!/bin/sh\nwhile true; do %s >>/var/log/%s.log 2>&1; sleep 3; done\n", cmdline, svc)), 0o700)
	cron, _ := exec.Command("crontab", "-l").Output()
	if !strings.Contains(string(cron), wrapper) {
		c := exec.Command("crontab", "-")
		c.Stdin = strings.NewReader(string(cron) + "@reboot " + wrapper + "\n")
		c.Run()
	}
	exec.Command("pkill", "-f", wrapper).Run()
	exec.Command("sh", "-c", "nohup "+wrapper+" >/dev/null 2>&1 &").Run()
	fmt.Println("note: no systemd/OpenRC found; installed via cron @reboot")
}

func installLaunchd(svc, bin string, argv []string) {
	label := "dev.argus." + strings.TrimPrefix(svc, "argus-")
	plistPath := "/Library/LaunchDaemons/" + label + ".plist"
	var args strings.Builder
	args.WriteString("    <string>" + xmlEsc(bin) + "</string>\n")
	for _, a := range argv {
		args.WriteString("    <string>" + xmlEsc(a) + "</string>\n")
	}
	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key>
  <array>
%s  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>ThrottleInterval</key><integer>5</integer>
  <key>ProcessType</key><string>Background</string>
  <key>StandardErrorPath</key><string>/var/log/%s.log</string>
  <key>StandardOutPath</key><string>/var/log/%s.log</string>
</dict>
</plist>
`, label, args.String(), svc, svc)
	exec.Command("launchctl", "bootout", "system/"+label).Run()
	if err := os.WriteFile(plistPath, []byte(plist), 0o600); err != nil {
		fatal("%v", err)
	}
	os.Chown(plistPath, 0, 0)
	if err := run("launchctl", "bootstrap", "system", plistPath); err != nil {
		run("launchctl", "load", "-w", plistPath)
	}
}

func xmlEsc(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace(s)
}

func installWindows(svc, bin string, argv []string, role, listen string) {
	task := map[string]string{"argus-agent": "Argus Agent", "argus-hub": "Argus Hub"}[svc]
	q := make([]string, len(argv))
	for i, a := range argv {
		if strings.ContainsAny(a, " \t\"") {
			a = `"` + strings.ReplaceAll(a, `"`, `\"`) + `"`
		}
		q[i] = a
	}
	xml := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo><Description>Argus fleet monitor (%s)</Description></RegistrationInfo>
  <Triggers><BootTrigger><Enabled>true</Enabled></BootTrigger></Triggers>
  <Principals><Principal id="Author"><UserId>S-1-5-18</UserId><RunLevel>HighestAvailable</RunLevel></Principal></Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <AllowHardTerminate>true</AllowHardTerminate>
    <StartWhenAvailable>true</StartWhenAvailable>
    <RunOnlyIfNetworkAvailable>false</RunOnlyIfNetworkAvailable>
    <IdleSettings><StopOnIdleEnd>false</StopOnIdleEnd><RestartOnIdle>false</RestartOnIdle></IdleSettings>
    <AllowStartOnDemand>true</AllowStartOnDemand>
    <Enabled>true</Enabled>
    <Hidden>false</Hidden>
    <RunOnlyIfIdle>false</RunOnlyIfIdle>
    <WakeToRun>false</WakeToRun>
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
    <Priority>6</Priority>
    <RestartOnFailure><Interval>PT1M</Interval><Count>999</Count></RestartOnFailure>
  </Settings>
  <Actions Context="Author"><Exec><Command>%s</Command><Arguments>%s</Arguments></Exec></Actions>
</Task>
`, role, xmlEsc(bin), xmlEsc(strings.Join(q, " ")))
	// schtasks wants UTF-16LE with BOM
	u := utf16.Encode([]rune(xml))
	buf := make([]byte, 2+2*len(u))
	buf[0], buf[1] = 0xFF, 0xFE
	for i, r := range u {
		binary.LittleEndian.PutUint16(buf[2+2*i:], r)
	}
	tmp := filepath.Join(os.TempDir(), "argus-task.xml")
	os.WriteFile(tmp, buf, 0o600)
	defer os.Remove(tmp)
	exec.Command("schtasks", "/End", "/TN", task).Run()
	if err := run("schtasks", "/Create", "/TN", task, "/XML", tmp, "/F"); err != nil {
		fatal("schtasks create failed: %v", err)
	}
	if role == "hub" {
		_, port, _ := strings.Cut(listen, ":")
		exec.Command("netsh", "advfirewall", "firewall", "delete", "rule", "name=Argus Hub").Run()
		exec.Command("netsh", "advfirewall", "firewall", "add", "rule", "name=Argus Hub", "dir=in", "action=allow", "protocol=TCP", "localport="+port).Run()
	}
	if err := run("schtasks", "/Run", "/TN", task); err != nil {
		fatal("schtasks run failed: %v", err)
	}
}

func uninstallService(purge bool) {
	if !isAdmin() {
		fatal("uninstall needs root / Administrator")
	}
	for _, svc := range []string{"argus-agent", "argus-hub"} {
		switch runtime.GOOS {
		case "windows":
			task := map[string]string{"argus-agent": "Argus Agent", "argus-hub": "Argus Hub"}[svc]
			exec.Command("schtasks", "/End", "/TN", task).Run()
			exec.Command("schtasks", "/Delete", "/TN", task, "/F").Run()
		case "darwin":
			label := "dev.argus." + strings.TrimPrefix(svc, "argus-")
			exec.Command("launchctl", "bootout", "system/"+label).Run()
			os.Remove("/Library/LaunchDaemons/" + label + ".plist")
		default:
			exec.Command("systemctl", "disable", "--now", svc).Run()
			os.Remove("/etc/systemd/system/" + svc + ".service")
			exec.Command("rc-service", svc, "stop").Run()
			exec.Command("rc-update", "del", svc).Run()
			os.Remove("/etc/init.d/" + svc)
			exec.Command("pkill", "-f", "/etc/argus/"+svc+".sh").Run()
			os.Remove("/etc/argus/" + svc + ".sh")
		}
	}
	if runtime.GOOS != "windows" {
		exec.Command("systemctl", "daemon-reload").Run()
		os.Remove(binPath())
	}
	if purge {
		os.RemoveAll(systemDataDir())
		os.RemoveAll("/etc/argus")
		fmt.Println("✓ argus services removed and all data, keys and tokens deleted")
		return
	}
	fmt.Println("✓ argus services removed (data kept in " + systemDataDir() + "; use --purge to delete)")
}

// statusCmd prints the local service state and checks the hub connection.
func statusCmd(c *config) {
	dir := systemDataDir()
	if !isAdmin() {
		dir = c.Data
	}
	fmt.Printf("argus %s %s/%s\n", version, runtime.GOOS, runtime.GOARCH)
	for _, svc := range []string{"argus-agent", "argus-hub"} {
		state := "not installed"
		switch runtime.GOOS {
		case "windows":
			task := map[string]string{"argus-agent": "Argus Agent", "argus-hub": "Argus Hub"}[svc]
			if out, err := exec.Command("schtasks", "/Query", "/TN", task, "/FO", "LIST").Output(); err == nil {
				for _, l := range strings.Split(string(out), "\n") {
					if strings.HasPrefix(strings.TrimSpace(l), "Status:") {
						state = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l), "Status:"))
					}
				}
			}
		case "darwin":
			label := "dev.argus." + strings.TrimPrefix(svc, "argus-")
			if err := exec.Command("launchctl", "print", "system/"+label).Run(); err == nil {
				state = "loaded"
			}
		default:
			if out, err := exec.Command("systemctl", "is-active", svc).Output(); err == nil || len(out) > 0 {
				if s := strings.TrimSpace(string(out)); s != "" && s != "inactive" || err == nil {
					state = s
				}
			}
		}
		fmt.Printf("  %-12s %s\n", svc, state)
	}
	var ac struct {
		Hub, Pin string
		Caps     []string
	}
	if b, err := os.ReadFile(filepath.Join(dir, "agent.json")); err == nil && json.Unmarshal(b, &ac) == nil && ac.Hub != "" {
		fmt.Printf("  hub          %s\n", ac.Hub)
		if ac.Pin != "" {
			fmt.Printf("  cert pin     %s\n", ac.Pin)
		}
		enrolled := readTrim(filepath.Join(dir, "agent.key")) != ""
		fmt.Printf("  enrolled     %v\n  caps         %v\n", enrolled, ac.Caps)
		cl := &http.Client{Timeout: 6 * time.Second, Transport: &http.Transport{TLSClientConfig: pinnedTLS(ac.Pin)}}
		if res, err := cl.Get(strings.TrimRight(ac.Hub, "/") + "/healthz"); err != nil {
			fmt.Printf("  reachable    NO — %v\n", err)
		} else {
			res.Body.Close()
			fmt.Printf("  reachable    yes (HTTP %d)\n", res.StatusCode)
		}
	}
	if readTrim(filepath.Join(dir, "admin.token")) != "" {
		fmt.Printf("  hub data     %s (2FA %v)\n", dir, totpEnabled(dir))
	}
	if runtime.GOOS == "linux" {
		if out, err := exec.Command("journalctl", "-u", "argus-agent", "-u", "argus-hub", "-n", "5", "--no-pager", "-o", "cat").Output(); err == nil && len(out) > 0 {
			fmt.Printf("  recent log:\n%s", out)
		}
	}
}
