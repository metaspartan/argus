package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Docker / Podman support via the Engine API on the local socket — no CLI,
// no SDK. Podman exposes the same API (podman.socket). Per-container CPU and
// memory come straight from cgroups on Linux (no stats API round-trips);
// elsewhere we use the one-shot stats endpoint.

type dockerClient struct {
	hc       *http.Client
	where    string
	mu       sync.Mutex
	engine   string
	version  string
	prev     map[string]ctrPrev
	pids     map[string]int
	cache    *DockerState
	lastInfo time.Time
	images   int
	driver   string
}

type ctrPrev struct {
	cpu    uint64 // ns
	rx, tx uint64
	t      time.Time
}

func dockerSockets() []string {
	var out []string
	if h := os.Getenv("DOCKER_HOST"); h != "" {
		out = append(out, h)
	}
	if runtime.GOOS == "windows" {
		return append(out, `npipe:////./pipe/docker_engine`, `npipe:////./pipe/podman-machine-default`)
	}
	out = append(out, "unix:///var/run/docker.sock", "unix:///run/docker.sock", "unix:///run/podman/podman.sock")
	if runtime.GOOS == "darwin" {
		homes, _ := filepath.Glob("/Users/*")
		for _, h := range homes {
			out = append(out, "unix://"+h+"/.docker/run/docker.sock", "unix://"+h+"/.orbstack/run/docker.sock", "unix://"+h+"/.colima/default/docker.sock")
		}
	}
	if uid := os.Getuid(); uid > 0 {
		out = append(out, fmt.Sprintf("unix:///run/user/%d/podman/podman.sock", uid), fmt.Sprintf("unix:///run/user/%d/docker.sock", uid))
	}
	return out
}

func newDocker() *dockerClient {
	for _, s := range dockerSockets() {
		var tr http.RoundTripper
		if strings.HasPrefix(s, "npipe://") {
			tr = npipeTransport(strings.ReplaceAll(strings.TrimPrefix(s, "npipe://"), "/", `\`))
		} else if dial := dialerFor(s); dial != nil {
			tr = &http.Transport{
				DialContext:     func(ctx context.Context, _, _ string) (net.Conn, error) { return dial(ctx) },
				MaxIdleConns:    2,
				IdleConnTimeout: 60 * time.Second,
			}
		}
		if tr == nil {
			continue
		}
		d := &dockerClient{
			where: s,
			hc:    &http.Client{Timeout: 10 * time.Second, Transport: tr},
			prev:  map[string]ctrPrev{},
			pids:  map[string]int{},
		}
		var v struct {
			Version    string
			APIVersion string `json:"ApiVersion"`
			Components []struct{ Name string }
		}
		if d.get("/version", &v) != nil {
			continue
		}
		d.engine, d.version = "docker", v.Version
		for _, c := range v.Components {
			if strings.Contains(strings.ToLower(c.Name), "podman") {
				d.engine = "podman"
			}
		}
		return d
	}
	return nil
}

func dialerFor(s string) func(context.Context) (net.Conn, error) {
	switch {
	case strings.HasPrefix(s, "unix://"):
		p := strings.TrimPrefix(s, "unix://")
		if _, err := os.Stat(p); err != nil {
			return nil
		}
		return func(ctx context.Context) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", p)
		}
	case strings.HasPrefix(s, "tcp://"):
		addr := strings.TrimPrefix(s, "tcp://")
		return func(ctx context.Context) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "tcp", addr)
		}
	}
	return nil
}

func (d *dockerClient) describe() string {
	return d.engine + " " + d.version
}

func (d *dockerClient) get(path string, v any) error {
	res, err := d.hc.Get("http://engine" + path)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 300))
		return fmt.Errorf("%s: %d %s", path, res.StatusCode, strings.TrimSpace(string(b)))
	}
	return json.NewDecoder(io.LimitReader(res.Body, 32<<20)).Decode(v)
}

type apiContainer struct {
	ID      string `json:"Id"`
	Names   []string
	Image   string
	State   string
	Status  string
	Created int64
	Ports   []struct {
		IP          string
		PrivatePort int
		PublicPort  int
		Type        string
	}
	Labels map[string]string
}

var healthRe = regexp.MustCompile(`\((healthy|unhealthy|health: starting)\)`)

// collect lists containers and their live usage. Cheap enough for every
// other tick: one list call + cgroup file reads.
func (d *dockerClient) collect(tick int) *DockerState {
	d.mu.Lock()
	defer d.mu.Unlock()
	// Full per-container stats while someone is looking; otherwise just
	// keep the running/total counts fresh every few ticks.
	if d.cache != nil && !detailMode.Load() && tick%3 != 0 {
		return d.cache
	}
	st := &DockerState{Engine: d.engine, Version: d.version}
	if time.Since(d.lastInfo) > time.Minute {
		var info struct {
			Images int
			Driver string
		}
		if d.get("/info", &info) == nil {
			d.images, d.driver, d.lastInfo = info.Images, info.Driver, time.Now()
		}
	}
	st.Images, st.Driver = d.images, d.driver
	var list []apiContainer
	if err := d.get("/containers/json?all=1", &list); err != nil {
		st.Error = err.Error()
		d.cache = st
		return st
	}
	now := time.Now()
	seen := map[string]bool{}
	for _, a := range list {
		c := Container{ID: a.ID[:min(12, len(a.ID))], Image: a.Image, State: a.State, Status: a.Status, Created: a.Created}
		if len(a.Names) > 0 {
			c.Name = strings.TrimPrefix(a.Names[0], "/")
		}
		if m := healthRe.FindStringSubmatch(a.Status); m != nil {
			c.Health = strings.TrimPrefix(m[1], "health: ")
		}
		c.Project = a.Labels["com.docker.compose.project"]
		c.Service = a.Labels["com.docker.compose.service"]
		if c.Project == "" {
			c.Project = a.Labels["io.podman.compose.project"]
		}
		portSeen := map[string]bool{}
		for _, p := range a.Ports {
			s := fmt.Sprintf("%d/%s", p.PrivatePort, p.Type)
			if p.PublicPort > 0 {
				s = fmt.Sprintf("%d→%d/%s", p.PublicPort, p.PrivatePort, p.Type)
			}
			if !portSeen[s] {
				portSeen[s] = true
				c.Ports = append(c.Ports, s)
			}
		}
		if a.State == "running" {
			seen[a.ID] = true
			d.usage(a.ID, &c, now)
		}
		st.Containers = append(st.Containers, c)
	}
	for id := range d.prev {
		if !seen[id] {
			delete(d.prev, id)
			delete(d.pids, id)
		}
	}
	sort.Slice(st.Containers, func(i, j int) bool {
		a, b := st.Containers[i], st.Containers[j]
		if (a.State == "running") != (b.State == "running") {
			return a.State == "running"
		}
		if a.Project != b.Project {
			return a.Project < b.Project
		}
		return a.Name < b.Name
	})
	for _, c := range st.Containers {
		st.Total++
		if c.State == "running" {
			st.Running++
		}
	}
	d.cache = st
	return st
}

// usage fills CPU / memory / network for a running container.
func (d *dockerClient) usage(id string, c *Container, now time.Time) {
	cpu, mem, limit, pids, ok := cgroupUsage(id)
	var rx, tx uint64
	if ok {
		pid, have := d.pids[id]
		if !have {
			var insp struct{ State struct{ Pid int } }
			if d.get("/containers/"+id+"/json", &insp) == nil {
				pid = insp.State.Pid
			}
			d.pids[id] = pid
		}
		if pid > 0 {
			rx, tx = procNetDev(pid)
		}
	} else {
		var s struct {
			CPUStats struct {
				CPUUsage struct {
					TotalUsage uint64 `json:"total_usage"`
				} `json:"cpu_usage"`
			} `json:"cpu_stats"`
			MemoryStats struct {
				Usage uint64
				Limit uint64
				Stats map[string]uint64
			} `json:"memory_stats"`
			PidsStats struct{ Current int } `json:"pids_stats"`
			Networks  map[string]struct {
				RxBytes uint64 `json:"rx_bytes"`
				TxBytes uint64 `json:"tx_bytes"`
			}
		}
		if d.get("/containers/"+id+"/stats?stream=false&one-shot=true", &s) != nil {
			return
		}
		cpu = s.CPUStats.CPUUsage.TotalUsage
		mem = s.MemoryStats.Usage
		if ic, ok := s.MemoryStats.Stats["inactive_file"]; ok && ic < mem {
			mem -= ic // match `docker stats`
		}
		limit, pids = s.MemoryStats.Limit, s.PidsStats.Current
		for _, n := range s.Networks {
			rx += n.RxBytes
			tx += n.TxBytes
		}
	}
	c.Mem, c.MemLimit, c.PIDs = mem, limit, pids
	if p, had := d.prev[id]; had {
		dt := now.Sub(p.t).Seconds()
		if dt > 0 {
			if cpu >= p.cpu {
				c.CPU = round1(float64(cpu-p.cpu) / 1e9 / dt * 100)
			}
			if rx >= p.rx {
				c.Rx = round1(float64(rx-p.rx) / dt)
			}
			if tx >= p.tx {
				c.Tx = round1(float64(tx-p.tx) / dt)
			}
		}
	}
	d.prev[id] = ctrPrev{cpu: cpu, rx: rx, tx: tx, t: now}
}

// cgroupUsage reads cgroup v2 (systemd or cgroupfs driver) or v1 accounting.
func cgroupUsage(id string) (cpu, mem, limit uint64, pids int, ok bool) {
	if runtime.GOOS != "linux" {
		return
	}
	for _, base := range []string{
		"/sys/fs/cgroup/system.slice/docker-" + id + ".scope",
		"/sys/fs/cgroup/docker/" + id,
		"/sys/fs/cgroup/machine.slice/libpod-" + id + ".scope",
		"/sys/fs/cgroup/libpod_parent/libpod-" + id,
	} {
		b, err := os.ReadFile(base + "/cpu.stat")
		if err != nil {
			continue
		}
		for _, l := range strings.Split(string(b), "\n") {
			if v, found := strings.CutPrefix(l, "usage_usec "); found {
				cpu = atou(v) * 1000
			}
		}
		mem, _ = readUint(base + "/memory.current")
		if st, err := os.ReadFile(base + "/memory.stat"); err == nil {
			for _, l := range strings.Split(string(st), "\n") {
				if v, found := strings.CutPrefix(l, "inactive_file "); found && atou(v) < mem {
					mem -= atou(v)
				}
			}
		}
		if s := readTrim(base + "/memory.max"); s != "max" {
			limit = atou(s)
		}
		if p, has := readUint(base + "/pids.current"); has {
			pids = int(p)
		}
		return cpu, mem, limit, pids, true
	}
	if v, has := readUint("/sys/fs/cgroup/cpuacct/docker/" + id + "/cpuacct.usage"); has {
		mem, _ = readUint("/sys/fs/cgroup/memory/docker/" + id + "/memory.usage_in_bytes")
		limit, _ = readUint("/sys/fs/cgroup/memory/docker/" + id + "/memory.limit_in_bytes")
		if limit > 1<<60 {
			limit = 0
		}
		return v, mem, limit, 0, true
	}
	return
}

func procNetDev(pid int) (rx, tx uint64) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/net/dev")
	if err != nil {
		return
	}
	for _, l := range strings.Split(string(b), "\n") {
		name, rest, ok := strings.Cut(l, ":")
		if !ok || strings.TrimSpace(name) == "lo" {
			continue
		}
		f := strings.Fields(rest)
		if len(f) >= 9 {
			rx += atou(f[0])
			tx += atou(f[8])
		}
	}
	return
}

var ctrIDRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)

// action performs start/stop/restart/pause/unpause/logs on a container.
func (d *dockerClient) action(op, id string) (string, error) {
	if !ctrIDRe.MatchString(id) {
		return "", fmt.Errorf("bad container id")
	}
	esc := url.PathEscape(id)
	switch op {
	case "start", "stop", "restart", "pause", "unpause", "kill":
		path := "/containers/" + esc + "/" + op
		if op == "stop" || op == "restart" {
			path += "?t=10"
		}
		hc := *d.hc
		hc.Timeout = 60 * time.Second
		res, err := hc.Post("http://engine"+path, "application/json", nil)
		if err != nil {
			return "", err
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(res.Body, 2000))
		if res.StatusCode >= 300 && res.StatusCode != 304 {
			return string(b), fmt.Errorf("%s failed: HTTP %d", op, res.StatusCode)
		}
		return fmt.Sprintf("%s %s: ok", op, id), nil
	case "logs":
		res, err := d.hc.Get("http://engine/containers/" + esc + "/logs?stdout=1&stderr=1&tail=300&timestamps=1")
		if err != nil {
			return "", err
		}
		defer res.Body.Close()
		raw, _ := io.ReadAll(io.LimitReader(res.Body, maxOutput))
		if res.StatusCode != 200 {
			return string(raw), fmt.Errorf("logs failed: HTTP %d", res.StatusCode)
		}
		return demuxLogs(raw), nil
	case "inspect":
		var v any
		if err := d.get("/containers/"+esc+"/json", &v); err != nil {
			return "", err
		}
		b, _ := json.MarshalIndent(v, "", "  ")
		return string(b), nil
	}
	return "", fmt.Errorf("unknown container action %q", op)
}

// demuxLogs strips Docker's 8-byte stream frame headers (non-TTY containers).
func demuxLogs(b []byte) string {
	if len(b) < 8 || b[0] > 2 || b[1] != 0 || b[2] != 0 || b[3] != 0 {
		return string(b)
	}
	var out strings.Builder
	for len(b) >= 8 {
		n := int(binary.BigEndian.Uint32(b[4:8]))
		b = b[8:]
		if n > len(b) {
			n = len(b)
		}
		out.Write(b[:n])
		b = b[n:]
	}
	return out.String()
}
