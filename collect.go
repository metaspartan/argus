package main

import (
	"bufio"
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Shared collector helpers. Platform collectors live in collect_<os>.go and
// implement: newCollector() *Collector, (*Collector).Static() *Static,
// (*Collector).Collect(tick int) *Metrics.

// Cadences (in ticks) for the more expensive probes.
const (
	everyProcs   = 3
	everyFS      = 5
	everyTCP     = 5
	everyBattery = 15
	everyNICs    = 30
)

func cmdOut(timeout time.Duration, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	c := exec.CommandContext(ctx, name, args...)
	hideWindow(c)
	b, err := c.Output()
	return string(b), err
}

func readTrim(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(strings.TrimRight(string(b), "\x00"))
}

func readUint(path string) (uint64, bool) {
	s := readTrim(path)
	if s == "" {
		return 0, false
	}
	v, err := strconv.ParseUint(s, 10, 64)
	return v, err == nil
}

func atof(s string) float64 {
	v, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return v
}

func atou(s string) uint64 {
	v, _ := strconv.ParseUint(strings.TrimSpace(s), 10, 64)
	return v
}

func round1(v float64) float64 { return float64(int64(v*10+0.5)) / 10 }

func clampPct(v float64) float64 {
	if v < 0 || v != v {
		return 0
	}
	if v > 100 {
		return 100
	}
	return round1(v)
}

// ---------------------------------------------------------------- rates

type rateKey struct{ a, b, c, d uint64 }

// rates computes per-second deltas for monotonically increasing counters.
type rates struct {
	prev  map[string]rateKey
	prevT time.Time
	dt    float64
}

func (r *rates) begin() {
	now := time.Now()
	if r.prev == nil {
		r.prev = map[string]rateKey{}
	}
	if r.prevT.IsZero() {
		r.dt = 0
	} else {
		r.dt = now.Sub(r.prevT).Seconds()
	}
	r.prevT = now
}

// delta returns per-second rates for the four counters (0 on first sample / wrap).
func (r *rates) delta(name string, cur rateKey) (a, b, c, d float64) {
	p, ok := r.prev[name]
	r.prev[name] = cur
	if !ok || r.dt <= 0 {
		return
	}
	f := func(x, y uint64) float64 {
		if x < y {
			return 0
		}
		return float64(x-y) / r.dt
	}
	return f(cur.a, p.a), f(cur.b, p.b), f(cur.c, p.c), f(cur.d, p.d)
}

// ---------------------------------------------------------------- CPU ticks

type cpuTimes struct{ busy, total uint64 }

func cpuPct(prev, cur []cpuTimes) (total float64, cores []float64) {
	if len(prev) != len(cur) || len(cur) == 0 {
		return 0, make([]float64, max(len(cur)-1, 0))
	}
	pct := func(p, c cpuTimes) float64 {
		dt := float64(c.total - p.total)
		if dt <= 0 || c.total < p.total {
			return 0
		}
		return clampPct(float64(c.busy-p.busy) / dt * 100)
	}
	total = pct(prev[0], cur[0])
	cores = make([]float64, len(cur)-1)
	for i := 1; i < len(cur); i++ {
		cores[i-1] = pct(prev[i], cur[i])
	}
	return
}

// ---------------------------------------------------------------- processes

type procSample struct {
	pid   int
	name  string
	cpuNs uint64 // cumulative CPU time
	mem   uint64
	user  string
	pcpu  float64 // pre-computed (macOS ps)
}

type procTracker struct {
	prev  map[int]uint64
	prevT time.Time
}

const topN = 30

func (t *procTracker) top(list []procSample, precomputed bool) []Proc {
	now := time.Now()
	dt := now.Sub(t.prevT).Seconds()
	next := make(map[int]uint64, len(list))
	out := make([]Proc, 0, len(list))
	for _, p := range list {
		var pct float64
		if precomputed {
			pct = p.pcpu
		} else {
			next[p.pid] = p.cpuNs
			if old, ok := t.prev[p.pid]; ok && dt > 0 && p.cpuNs >= old {
				pct = float64(p.cpuNs-old) / 1e9 / dt * 100
			}
		}
		out = append(out, Proc{PID: p.pid, Name: p.name, CPU: round1(pct), Mem: p.mem, User: p.user})
	}
	if !precomputed {
		t.prev, t.prevT = next, now
	}
	// Top by CPU, plus the biggest memory consumers.
	sort.Slice(out, func(i, j int) bool {
		return out[i].CPU > out[j].CPU || (out[i].CPU == out[j].CPU && out[i].Mem > out[j].Mem)
	})
	if len(out) <= topN {
		return out
	}
	sel := out[:topN-10]
	rest := append([]Proc(nil), out[topN-10:]...)
	sort.Slice(rest, func(i, j int) bool { return rest[i].Mem > rest[j].Mem })
	return append(append([]Proc(nil), sel...), rest[:10]...)
}

// ---------------------------------------------------------------- NVIDIA

// nvsmi keeps one long-lived `nvidia-smi -lms` process streaming CSV,
// so we never pay process-spawn cost per tick.
type nvsmi struct {
	mu    sync.Mutex
	stats map[int]GPUStat
	seen  time.Time
}

const nvQuery = "index,name,utilization.gpu,memory.used,memory.total,temperature.gpu,power.draw,clocks.sm,fan.speed"

func nvidiaPath() string {
	if p, err := exec.LookPath("nvidia-smi"); err == nil {
		return p
	}
	for _, p := range []string{`C:\Windows\System32\nvidia-smi.exe`, `C:\Program Files\NVIDIA Corporation\NVSMI\nvidia-smi.exe`, "/usr/bin/nvidia-smi", "/usr/local/bin/nvidia-smi"} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

func startNvidiaSMI(interval time.Duration) *nvsmi {
	path := nvidiaPath()
	if path == "" {
		return nil
	}
	n := &nvsmi{stats: map[int]GPUStat{}}
	go func() {
		for attempt := 0; attempt < 20; attempt++ {
			c := exec.Command(path, "--query-gpu="+nvQuery, "--format=csv,noheader,nounits", "-lms", strconv.Itoa(int(interval.Milliseconds())))
			hideWindow(c)
			out, err := c.StdoutPipe()
			if err != nil || c.Start() != nil {
				return
			}
			sc := bufio.NewScanner(out)
			for sc.Scan() {
				f := strings.Split(sc.Text(), ",")
				if len(f) < 9 {
					continue
				}
				idx, err := strconv.Atoi(strings.TrimSpace(f[0]))
				if err != nil {
					continue
				}
				g := GPUStat{
					Name:     strings.TrimSpace(f[1]),
					Util:     atof(f[2]),
					MemUsed:  uint64(atof(f[3])) << 20,
					MemTotal: uint64(atof(f[4])) << 20,
					Temp:     atof(f[5]),
					Watts:    round1(atof(f[6])),
					MHz:      atof(f[7]),
					Fan:      atof(f[8]),
				}
				n.mu.Lock()
				n.stats[idx] = g
				n.seen = time.Now()
				n.mu.Unlock()
			}
			c.Wait()
			time.Sleep(time.Duration(attempt+1) * 5 * time.Second)
		}
	}()
	return n
}

func (n *nvsmi) snapshot() []GPUStat {
	if n == nil {
		return nil
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if time.Since(n.seen) > 30*time.Second {
		return nil
	}
	out := make([]GPUStat, 0, len(n.stats))
	for i := 0; i < 16; i++ {
		if g, ok := n.stats[i]; ok {
			out = append(out, g)
		}
	}
	return out
}

var cudaRe = regexp.MustCompile(`CUDA Version:\s*([0-9.]+)`)

func nvidiaStatic() []GPUInfo {
	path := nvidiaPath()
	if path == "" {
		return nil
	}
	cuda := ""
	if s, err := cmdOut(10*time.Second, path); err == nil {
		if m := cudaRe.FindStringSubmatch(s); m != nil {
			cuda = "CUDA " + m[1]
		}
	}
	q := "name,driver_version,memory.total,pci.bus_id,vbios_version,compute_cap"
	s, err := cmdOut(10*time.Second, path, "--query-gpu="+q, "--format=csv,noheader,nounits")
	if err != nil {
		q = "name,driver_version,memory.total,pci.bus_id,vbios_version"
		if s, err = cmdOut(10*time.Second, path, "--query-gpu="+q, "--format=csv,noheader,nounits"); err != nil {
			return nil
		}
	}
	var out []GPUInfo
	for _, line := range strings.Split(strings.TrimSpace(s), "\n") {
		f := strings.Split(line, ",")
		if len(f) < 5 {
			continue
		}
		for i := range f {
			f[i] = strings.TrimSpace(f[i])
		}
		g := GPUInfo{Name: f[0], Vendor: "NVIDIA", Driver: "nvidia", DriverVersion: f[1], VRAM: uint64(atof(f[2])) << 20, Bus: f[3], API: cuda, Extra: "VBIOS " + f[4]}
		if len(f) > 5 && f[5] != "" && !strings.Contains(f[5], "N/A") {
			g.Extra += " · SM " + f[5]
		}
		out = append(out, g)
	}
	return out
}

// ---------------------------------------------------------------- misc

func isVirtualNIC(name string) bool {
	n := strings.ToLower(name)
	for _, p := range []string{"lo", "docker", "veth", "br-", "virbr", "vmnet", "vboxnet", "tun", "tap", "utun", "awdl", "llw", "bridge", "anpi", "ap1", "gif", "stf", "cali", "flannel", "cni", "kube", "zt", "tailscale", "wg", "vethernet", "loopback", "isatap", "teredo", "lxc", "incus", "podman", "ipsec", "ppp", "dummy", "bond0.", "virtual"} {
		if strings.HasPrefix(n, p) || (len(p) > 4 && strings.Contains(n, p)) {
			return true
		}
	}
	return false
}

func nicAddrs() map[string]*NICInfo {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil
	}
	out := map[string]*NICInfo{}
	for _, i := range ifs {
		n := &NICInfo{Name: i.Name, MAC: i.HardwareAddr.String(), MTU: i.MTU, Up: i.Flags&net.FlagUp != 0, Virt: isVirtualNIC(i.Name) || i.Flags&net.FlagLoopback != 0}
		if as, err := i.Addrs(); err == nil {
			for _, a := range as {
				s := a.String()
				if strings.HasPrefix(s, "fe80:") {
					continue
				}
				n.Addrs = append(n.Addrs, s)
			}
		}
		out[i.Name] = n
	}
	return out
}

func sortedNICs(m map[string]*NICInfo) []NICInfo {
	out := make([]NICInfo, 0, len(m))
	for _, n := range m {
		out = append(out, *n)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Virt != out[j].Virt {
			return !out[i].Virt
		}
		if (len(out[i].Addrs) > 0) != (len(out[j].Addrs) > 0) {
			return len(out[i].Addrs) > 0
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func baseStatic() *Static {
	h, _ := os.Hostname()
	tz, _ := time.Now().Zone()
	return &Static{
		Hostname:    trimHost(h),
		OS:          runtime.GOOS,
		Arch:        runtime.GOARCH,
		CPUThreads:  runtime.NumCPU(),
		Agent:       version,
		Timezone:    tz,
		Extra:       map[string]string{},
		CollectedAt: time.Now().UnixMilli(),
	}
}

func exeDir() string {
	p, err := os.Executable()
	if err != nil {
		return "."
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		p = r
	}
	return filepath.Dir(p)
}
