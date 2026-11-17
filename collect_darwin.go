//go:build darwin

package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"encoding/xml"
	"net"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// macOS collector: Mach host stats + sysctl + libproc via libSystem trampolines,
// route sysctl for NIC counters, ioreg for GPU/disk, powermetrics (root) for power.

type Collector struct {
	prevCPU   []cpuTimes
	net       rates
	disk      rates
	procs     procTracker
	page      uint64
	memTotal  uint64
	ifNames   map[int]string
	fsCache   []FSStat
	gpuCache  []GPUStat
	diskCache []DiskIO
	bat       *Battery
	pm        *powermetrics
	users     map[uint32]string
}

func newCollector() *Collector {
	machInit()
	c := &Collector{page: sysctlInt("hw.pagesize"), memTotal: sysctlInt("hw.memsize"), users: map[uint32]string{}}
	if c.page == 0 {
		c.page = 16384
	}
	if isAdmin() {
		c.pm = startPowermetrics()
	}
	return c
}

var macNames = map[string]string{"11": "Big Sur", "12": "Monterey", "13": "Ventura", "14": "Sonoma", "15": "Sequoia", "26": "Tahoe", "27": ""}

// ---------------------------------------------------------------- static

var (
	uuidRe   = regexp.MustCompile(`"IOPlatformUUID" = "([^"]+)"`)
	agxRe    = regexp.MustCompile(`<class (AGX\w+|AMDRadeon\w+|IntelAccel\w+|nvAccelerator\w*)`)
	kextRe   = regexp.MustCompile(`\s(\S+\.\S+) \(([^)]+)\)`)
	gpuUtil  = regexp.MustCompile(`"Device Utilization %"=(\d+)`)
	gpuMem   = regexp.MustCompile(`"In use system memory"=(\d+)`)
	gpuModel = regexp.MustCompile(`"model" = "([^"]+)"`)
	batRe    = regexp.MustCompile(`(\d+)%;\s*([^;]+);`)
)

func (c *Collector) Static() *Static {
	s := baseStatic()
	ver := sysctlStr("kern.osproductversion")
	major := strings.Split(ver, ".")[0]
	s.Platform = strings.TrimSpace("macOS " + ver + " " + macNames[major])
	s.Kernel = "Darwin " + sysctlStr("kern.osrelease")
	s.Extra["Build"] = sysctlStr("kern.osversion")
	s.Vendor = "Apple"
	s.Model = sysctlStr("hw.model")
	s.CPUModel = sysctlStr("machdep.cpu.brand_string")
	s.CPUCores = int(sysctlInt("hw.physicalcpu"))
	s.CPUThreads = int(sysctlInt("hw.logicalcpu"))
	s.PCores = int(sysctlInt("hw.perflevel0.physicalcpu"))
	s.ECores = int(sysctlInt("hw.perflevel1.physicalcpu"))
	if s.ECores == 0 {
		s.PCores = 0
	}
	if f := sysctlInt("hw.cpufrequency_max"); f > 0 {
		s.CPUMaxMHz = float64(f) / 1e6
	}
	if s.Arch == "arm64" {
		s.CPUFlags = "ARMv8.x · NEON · AMX"
		if sysctlInt("hw.optional.arm.FEAT_SME") == 1 {
			s.CPUFlags = "ARMv9 · NEON · SME"
		}
	} else {
		s.CPUFlags = strings.ToLower(sysctlStr("machdep.cpu.features"))
	}
	s.MemTotal = c.memTotal
	if b := sysctlRaw("kern.boottime"); len(b) >= 8 {
		s.BootTime = int64(binary.LittleEndian.Uint64(b))
	}
	if out, err := cmdOut(5*time.Second, "ioreg", "-rd1", "-c", "IOPlatformExpertDevice"); err == nil {
		if m := uuidRe.FindStringSubmatch(out); m != nil {
			s.ID = m[1]
		}
	}
	if s.ID == "" {
		s.ID = "host-" + s.Hostname
	}
	if sysctlInt("kern.hv_vmm_present") == 1 {
		s.Virtual = "VM"
	}

	// Kernel extensions (drivers)
	kexts := map[string]string{}
	out, err := cmdOut(20*time.Second, "kmutil", "showloaded", "--list-only")
	if err != nil || out == "" {
		out, _ = cmdOut(20*time.Second, "kextstat", "-l")
	}
	for _, line := range strings.Split(out, "\n") {
		if m := kextRe.FindStringSubmatch(line); m != nil {
			kexts[m[1]] = m[2]
			s.Drivers = append(s.Drivers, Driver{Name: m[1], Version: m[2]})
		}
	}
	sort.Slice(s.Drivers, func(i, j int) bool { return s.Drivers[i].Name < s.Drivers[j].Name })

	// GPU driver class from IOAccelerator
	agx := ""
	if out, err := cmdOut(5*time.Second, "ioreg", "-r", "-d", "1", "-w", "0", "-c", "IOAccelerator"); err == nil {
		if m := agxRe.FindStringSubmatch(out); m != nil {
			agx = m[1]
		}
	}

	// system_profiler: hardware, displays/GPUs, USB, Thunderbolt
	if out, err := cmdOut(45*time.Second, "system_profiler", "-json", "-detailLevel", "mini", "SPHardwareDataType", "SPDisplaysDataType", "SPUSBDataType", "SPUSBHostDataType", "SPThunderboltDataType"); err == nil {
		var sp map[string][]map[string]any
		if json.Unmarshal([]byte(out), &sp) == nil {
			for _, hw := range sp["SPHardwareDataType"] {
				if n := str(hw["machine_name"]); n != "" {
					s.Model = n + " (" + s.Model + ")"
				}
				if chip := str(hw["chip_type"]); chip != "" && s.CPUModel == "" {
					s.CPUModel = chip
				}
				s.Firmware = strings.TrimSpace("iBoot " + str(hw["boot_rom_version"]))
				if v := str(hw["os_loader_version"]); v != "" {
					s.Extra["OS Loader"] = v
				}
				if v := str(hw["number_processors"]); v != "" {
					s.Extra["Processors"] = v
				}
				if v := str(hw["physical_memory"]); v != "" {
					s.Extra["Memory"] = v
				}
				if v := str(hw["activation_lock_status"]); v != "" {
					s.Extra["Activation Lock"] = strings.TrimPrefix(v, "activation_lock_")
				}
			}
			for _, d := range sp["SPDisplaysDataType"] {
				g := GPUInfo{Name: str(d["sppci_model"]), Vendor: strings.TrimPrefix(str(d["spdisplays_vendor"]), "sppci_vendor_")}
				if g.Vendor == "" {
					g.Vendor = strings.TrimPrefix(str(d["sppci_vendor"]), "sppci_vendor_")
				}
				g.Cores, _ = strconv.Atoi(str(d["sppci_cores"]))
				if mf := str(d["spdisplays_mtlgpufamilysupport"]); mf != "" {
					g.API = strings.ReplaceAll(strings.Title(strings.TrimPrefix(mf, "spdisplays_")), "Metal", "Metal ")
				}
				if v := str(d["spdisplays_vram"]); v != "" {
					g.VRAM = parseSize(v)
				} else if v := str(d["spdisplays_vram_shared"]); v != "" {
					g.VRAM = parseSize(v)
				}
				g.Bus = strings.TrimPrefix(str(d["sppci_bus"]), "spdisplays_")
				if agx != "" && (g.Vendor == "" || strings.Contains(strings.ToLower(g.Vendor), "apple")) {
					g.Driver = agx
					for k, v := range kexts {
						if strings.Contains(k, "AGX") {
							g.DriverVersion = v
							break
						}
					}
				}
				if g.DriverVersion == "" {
					g.DriverVersion = "macOS " + ver
				}
				var disp []string
				if arr, ok := d["spdisplays_ndrvs"].([]any); ok {
					for _, x := range arr {
						if dm, ok := x.(map[string]any); ok {
							n := str(dm["_name"])
							if r := str(dm["_spdisplays_resolution"]); r != "" {
								n += " " + r
							}
							disp = append(disp, n)
							s.Devices = append(s.Devices, Device{Bus: "display", Class: "Display", Name: str(dm["_name"]), Vendor: str(dm["_spdisplays_display-vendor-id"]), ID: str(dm["_spdisplays_resolution"])})
						}
					}
				}
				if len(disp) > 0 {
					g.Extra = "Displays: " + strings.Join(disp, ", ")
				}
				if g.Name != "" {
					s.GPUs = append(s.GPUs, g)
				}
			}
			var walk func(bus string, items []any)
			walk = func(bus string, items []any) {
				for _, x := range items {
					m, ok := x.(map[string]any)
					if !ok {
						continue
					}
					name := first(str(m["USBDeviceKeyProductName"]), str(m["_name"]))
					vend := first(str(m["USBDeviceKeyVendorName"]), str(m["manufacturer"]), str(m["vendor_name_key"]))
					id := first(str(m["USBDeviceKeyVendorID"]), str(m["vendor_id"]))
					if pid := first(str(m["USBDeviceKeyProductID"]), str(m["product_id"])); pid != "" {
						id += ":" + pid
					}
					if name != "" && !strings.Contains(strings.ToLower(name), "bus") && !strings.HasPrefix(name, "thunderbolt_bus") {
						s.Devices = append(s.Devices, Device{Bus: bus, Name: name, Vendor: vend, ID: id, Class: first(str(m["USBDeviceKeyLinkSpeed"]), str(m["device_speed"]), str(m["switch_uid_key"]))})
					}
					if sub, ok := m["_items"].([]any); ok {
						walk(bus, sub)
					}
				}
			}
			for k, bus := range map[string]string{"SPUSBDataType": "usb", "SPUSBHostDataType": "usb", "SPThunderboltDataType": "thunderbolt"} {
				arr := make([]any, len(sp[k]))
				for i := range sp[k] {
					arr[i] = sp[k][i]
				}
				walk(bus, arr)
			}
		}
	}
	if len(s.GPUs) == 0 && agx != "" {
		s.GPUs = append(s.GPUs, GPUInfo{Name: s.CPUModel + " GPU", Vendor: "Apple", Driver: agx})
	}

	// NICs, with hardware port names
	nics := nicAddrs()
	if out, err := cmdOut(5*time.Second, "networksetup", "-listallhardwareports"); err == nil {
		port := ""
		for _, l := range strings.Split(out, "\n") {
			if v, ok := strings.CutPrefix(l, "Hardware Port: "); ok {
				port = v
			} else if v, ok := strings.CutPrefix(l, "Device: "); ok {
				if n := nics[v]; n != nil {
					n.Driver = port
					n.Virt = false
				}
			}
		}
	}
	s.NICs = sortedNICs(nics)
	return s
}

func str(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	}
	return ""
}

func first(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

func parseSize(s string) uint64 {
	f := strings.Fields(s)
	if len(f) < 2 {
		return 0
	}
	v := atof(f[0])
	switch strings.ToUpper(f[1]) {
	case "TB":
		v *= 1 << 40
	case "GB":
		v *= 1 << 30
	case "MB":
		v *= 1 << 20
	}
	return uint64(v)
}

// ---------------------------------------------------------------- dynamic

func (c *Collector) Collect(tick int) *Metrics {
	m := &Metrics{T: time.Now().UnixMilli()}

	// CPU
	if t := cpuTicks(); len(t) > 0 {
		cur := make([]cpuTimes, len(t)+1)
		for i, v := range t {
			busy := uint64(v[0]) + uint64(v[1]) + uint64(v[3])
			total := busy + uint64(v[2])
			cur[i+1] = cpuTimes{busy, total}
			cur[0].busy += busy
			cur[0].total += total
		}
		m.CPU, m.Cores = cpuPct(c.prevCPU, cur)
		c.prevCPU = cur
	}

	// Load / uptime
	if b := sysctlRaw("vm.loadavg"); len(b) >= 24 {
		scale := float64(binary.LittleEndian.Uint64(b[16:]))
		if scale > 0 {
			m.Load = []float64{
				round2(float64(binary.LittleEndian.Uint32(b[0:])) / scale),
				round2(float64(binary.LittleEndian.Uint32(b[4:])) / scale),
				round2(float64(binary.LittleEndian.Uint32(b[8:])) / scale),
			}
		}
	}
	if b := sysctlRaw("kern.boottime"); len(b) >= 8 {
		m.Uptime = uint64(time.Now().Unix() - int64(binary.LittleEndian.Uint64(b)))
	}

	// Memory (Activity Monitor semantics)
	m.Mem.Total = c.memTotal
	if b := vmStats(); b != nil {
		u32 := func(off int) uint64 { return uint64(binary.LittleEndian.Uint32(b[off:])) }
		wired := u32(12)
		purgeable := u32(88)
		compressor := u32(128)
		external := u32(136)
		internal := u32(140)
		app := internal - min(purgeable, internal)
		m.Mem.Used = (app + wired + compressor) * c.page
		m.Mem.Wired = wired * c.page
		m.Mem.Compressed = compressor * c.page
		m.Mem.Cached = (external + purgeable) * c.page
		if m.Mem.Used > m.Mem.Total {
			m.Mem.Used = m.Mem.Total
		}
		m.Mem.Avail = m.Mem.Total - m.Mem.Used
	}
	if b := sysctlRaw("vm.swapusage"); len(b) >= 24 {
		m.Mem.SwapTotal = binary.LittleEndian.Uint64(b[0:])
		m.Mem.SwapUsed = binary.LittleEndian.Uint64(b[16:])
	}
	if lvl := sysctlInt("kern.memorystatus_level"); lvl > 0 && lvl <= 100 {
		m.Mem.Pressure = float64(100 - lvl)
	}

	// Network via NET_RT_IFLIST2 (64-bit counters)
	if c.ifNames == nil || tick%everyNICs == 0 {
		c.ifNames = map[int]string{}
		if ifs, err := net.Interfaces(); err == nil {
			for _, i := range ifs {
				c.ifNames[i.Index] = i.Name
			}
		}
	}
	c.net.begin()
	if rib, err := syscall.RouteRIB(6 /* NET_RT_IFLIST2 */, 0); err == nil {
		for len(rib) >= 4 {
			l := int(binary.LittleEndian.Uint16(rib))
			if l == 0 || l > len(rib) {
				break
			}
			msg := rib[:l]
			rib = rib[l:]
			if msg[3] != 0x12 || len(msg) < 112 { // RTM_IFINFO2
				continue
			}
			idx := int(binary.LittleEndian.Uint16(msg[12:]))
			name := c.ifNames[idx]
			if name == "" || strings.HasPrefix(name, "lo") {
				continue
			}
			const d = 32
			ip, op := binary.LittleEndian.Uint64(msg[d+24:]), binary.LittleEndian.Uint64(msg[d+40:])
			ie, oe := binary.LittleEndian.Uint64(msg[d+32:]), binary.LittleEndian.Uint64(msg[d+48:])
			ib, ob := binary.LittleEndian.Uint64(msg[d+64:]), binary.LittleEndian.Uint64(msg[d+72:])
			rx, tx, rp, tp := c.net.delta(name, rateKey{ib, ob, ip, op})
			if ib == 0 && ob == 0 {
				continue
			}
			m.Net = append(m.Net, NetStat{Name: name, Rx: round1(rx), Tx: round1(tx), RxTotal: ib, TxTotal: ob, RxPkts: round1(rp), TxPkts: round1(tp), Errs: ie + oe})
		}
	}

	// GPU + disk via ioreg (every other tick)
	if tick%2 == 0 {
		c.gpuCache = ioregGPU()
		c.diskCache = c.ioregDisks()
	}
	m.GPUs = append([]GPUStat(nil), c.gpuCache...)
	m.Disks = c.diskCache

	// Power / thermal (root only)
	if p := c.pm.snapshot(); p != nil {
		m.CPUWatts, m.SysWatts, m.Thermal = p.cpuW, p.sysW, p.thermal
		if p.cpuMHz > 0 {
			m.CPUMHz = p.cpuMHz
		}
		for i := range m.GPUs {
			if i == 0 {
				m.GPUs[i].Watts = p.gpuW
				m.GPUs[i].MHz = p.gpuMHz
				if m.GPUs[i].Util == 0 && p.gpuBusy > 0 {
					m.GPUs[i].Util = p.gpuBusy
				}
			}
		}
		if p.aneW > 0 {
			m.Temps = append(m.Temps, Sensor{"ANE power W", p.aneW})
		}
	}

	if tick%everyFS == 0 || c.fsCache == nil {
		c.fsCache = filesystems()
	}
	m.FS = c.fsCache

	if tick%everyProcs == 0 {
		m.Procs, m.NProcs = c.processes()
	}

	if tick%everyBattery == 0 {
		if out, err := cmdOut(3*time.Second, "pmset", "-g", "batt"); err == nil {
			if mm := batRe.FindStringSubmatch(out); mm != nil {
				st := strings.TrimSpace(mm[2])
				c.bat = &Battery{Percent: atof(mm[1]), Charging: st == "charging", State: st}
			}
		}
	}
	m.Battery = c.bat
	return m
}

func ioregGPU() []GPUStat {
	out, err := cmdOut(3*time.Second, "ioreg", "-r", "-d", "1", "-w", "0", "-c", "IOAccelerator")
	if err != nil {
		return nil
	}
	var res []GPUStat
	for _, blk := range strings.Split(out, "+-o ")[1:] {
		g := GPUStat{Name: "GPU"}
		if mm := gpuModel.FindStringSubmatch(blk); mm != nil {
			g.Name = mm[1]
		}
		if mm := gpuUtil.FindStringSubmatch(blk); mm != nil {
			g.Util = clampPct(atof(mm[1]))
		}
		if mm := gpuMem.FindStringSubmatch(blk); mm != nil {
			g.MemUsed = atou(mm[1])
		}
		res = append(res, g)
	}
	return res
}

var (
	diskStatsRe = regexp.MustCompile(`"Bytes \(Read\)"=(\d+).*?"Bytes \(Write\)"=(\d+)|"Bytes \(Write\)"=(\d+).*?"Bytes \(Read\)"=(\d+)`)
	bsdNameRe   = regexp.MustCompile(`"BSD Name" = "(disk\d+)"`)
)

func (c *Collector) ioregDisks() []DiskIO {
	out, err := cmdOut(3*time.Second, "ioreg", "-r", "-d", "2", "-w", "0", "-c", "IOBlockStorageDriver")
	if err != nil {
		return nil
	}
	c.disk.begin()
	var res []DiskIO
	var r, w uint64
	have := false
	for _, line := range strings.Split(out, "\n") {
		if mm := diskStatsRe.FindStringSubmatch(line); mm != nil {
			if mm[1] != "" {
				r, w = atou(mm[1]), atou(mm[2])
			} else {
				w, r = atou(mm[3]), atou(mm[4])
			}
			have = true
		} else if mm := bsdNameRe.FindStringSubmatch(line); mm != nil && have {
			rr, ww, _, _ := c.disk.delta(mm[1], rateKey{r, w, 0, 0})
			res = append(res, DiskIO{Name: mm[1], R: round1(rr), W: round1(ww)})
			have = false
		}
	}
	return res
}

func cstr(b []int8) string {
	n := 0
	for n < len(b) && b[n] != 0 {
		n++
	}
	u := make([]byte, n)
	for i := 0; i < n; i++ {
		u[i] = byte(b[i])
	}
	return string(u)
}

func filesystems() []FSStat {
	n, err := syscall.Getfsstat(nil, 2)
	if err != nil || n == 0 {
		return nil
	}
	buf := make([]syscall.Statfs_t, n)
	n, err = syscall.Getfsstat(buf, 2 /* MNT_NOWAIT */)
	if err != nil {
		return nil
	}
	var out []FSStat
	hasData := false
	for _, st := range buf[:n] {
		if cstr(st.Mntonname[:]) == "/System/Volumes/Data" {
			hasData = true
		}
	}
	for _, st := range buf[:n] {
		mnt, typ, dev := cstr(st.Mntonname[:]), cstr(st.Fstypename[:]), cstr(st.Mntfromname[:])
		switch typ {
		case "apfs", "hfs", "exfat", "msdos", "smbfs", "nfs", "afpfs", "ntfs", "webdav":
		default:
			continue
		}
		if mnt == "/" && hasData {
			continue
		}
		if mnt != "/System/Volumes/Data" && mnt != "/" {
			if strings.HasPrefix(mnt, "/System/Volumes/") || strings.HasPrefix(mnt, "/private/var/vm") || st.Flags&0x00100000 != 0 { // MNT_DONTBROWSE
				continue
			}
		}
		if st.Blocks == 0 {
			continue
		}
		bs := uint64(st.Bsize)
		label := mnt
		if mnt == "/System/Volumes/Data" {
			label = "/"
		}
		out = append(out, FSStat{Mount: label, Dev: dev, Type: typ, Total: st.Blocks * bs, Used: (st.Blocks - st.Bavail) * bs})
	}
	return out
}

func (c *Collector) processes() ([]Proc, int) {
	pids := listPids()
	list := make([]procSample, 0, len(pids))
	uids := map[int]uint32{}
	for _, pid := range pids {
		if pid <= 0 {
			continue
		}
		cpu, rss, ok := procTaskInfo(pid)
		if !ok {
			continue
		}
		uid, name, _ := procBSD(pid)
		uids[int(pid)] = uid
		list = append(list, procSample{pid: int(pid), name: name, cpuNs: cpu, mem: rss})
	}
	top := c.procs.top(list, false)
	for i := range top {
		uid := uids[top[i].PID]
		name, ok := c.users[uid]
		if !ok {
			name = strconv.Itoa(int(uid))
			if u, err := lookupUser(uid); err == nil {
				name = u
			}
			c.users[uid] = name
		}
		top[i].User = name
	}
	return top, len(pids)
}

func lookupUser(uid uint32) (string, error) {
	out, err := exec.Command("id", "-un", strconv.Itoa(int(uid))).Output()
	return strings.TrimSpace(string(out)), err
}

// ---------------------------------------------------------------- powermetrics

type pmSample struct {
	cpuW, gpuW, aneW, sysW  float64
	gpuMHz, gpuBusy, cpuMHz float64
	thermal                 string
	at                      time.Time
}

type powermetrics struct {
	mu   sync.Mutex
	last *pmSample
}

func startPowermetrics() *powermetrics {
	if _, err := exec.LookPath("powermetrics"); err != nil {
		return nil
	}
	p := &powermetrics{}
	go func() {
		for attempt := 0; attempt < 10; attempt++ {
			c := exec.Command("powermetrics", "-i", "2000", "--samplers", "cpu_power,gpu_power,thermal", "-f", "plist")
			out, err := c.StdoutPipe()
			if err != nil || c.Start() != nil {
				return
			}
			rd := bufio.NewReaderSize(out, 256<<10)
			for {
				doc, err := rd.ReadBytes(0)
				if len(doc) > 1 {
					if s := parsePM(bytes.TrimRight(doc, "\x00")); s != nil {
						p.mu.Lock()
						p.last = s
						p.mu.Unlock()
					}
				}
				if err != nil {
					break
				}
			}
			c.Wait()
			time.Sleep(10 * time.Second)
		}
	}()
	return p
}

func (p *powermetrics) snapshot() *pmSample {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.last == nil || time.Since(p.last.at) > 15*time.Second {
		return nil
	}
	return p.last
}

func parsePM(doc []byte) *pmSample {
	root, ok := parsePlist(doc).(map[string]any)
	if !ok {
		return nil
	}
	s := &pmSample{at: time.Now()}
	num := func(m map[string]any, k string) float64 {
		if v, ok := m[k].(float64); ok {
			return v
		}
		return 0
	}
	if proc, ok := root["processor"].(map[string]any); ok {
		s.cpuW = round1(num(proc, "cpu_power") / 1000)
		s.gpuW = round1(num(proc, "gpu_power") / 1000)
		s.aneW = round1(num(proc, "ane_power") / 1000)
		s.sysW = round1(num(proc, "combined_power") / 1000)
		if cl, ok := proc["clusters"].([]any); ok {
			for _, x := range cl {
				if cm, ok := x.(map[string]any); ok {
					if f := num(cm, "freq_hz") / 1e6; f > s.cpuMHz {
						s.cpuMHz = round1(f)
					}
				}
			}
		}
	}
	if gpu, ok := root["gpu"].(map[string]any); ok {
		s.gpuMHz = round1(num(gpu, "freq_hz"))
		if s.gpuMHz > 1e5 {
			s.gpuMHz = round1(s.gpuMHz / 1e6)
		}
		if ir, ok := gpu["idle_ratio"].(float64); ok {
			s.gpuBusy = clampPct((1 - ir) * 100)
		}
		if s.gpuW == 0 {
			s.gpuW = round1(num(gpu, "gpu_power") / 1000)
		}
	}
	if t, ok := root["thermal_pressure"].(string); ok {
		s.thermal = t
	}
	return s
}

func parsePlist(b []byte) any {
	d := xml.NewDecoder(bytes.NewReader(b))
	d.Strict = false
	for {
		tok, err := d.Token()
		if err != nil {
			return nil
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local != "plist" {
			return plistValue(d, se)
		}
	}
}

func plistValue(d *xml.Decoder, se xml.StartElement) any {
	switch se.Name.Local {
	case "dict":
		m := map[string]any{}
		key := ""
		for {
			tok, err := d.Token()
			if err != nil {
				return m
			}
			switch t := tok.(type) {
			case xml.StartElement:
				if t.Name.Local == "key" {
					var k string
					d.DecodeElement(&k, &t)
					key = k
				} else {
					m[key] = plistValue(d, t)
				}
			case xml.EndElement:
				return m
			}
		}
	case "array":
		var a []any
		for {
			tok, err := d.Token()
			if err != nil {
				return a
			}
			switch t := tok.(type) {
			case xml.StartElement:
				a = append(a, plistValue(d, t))
			case xml.EndElement:
				return a
			}
		}
	case "true":
		d.Skip()
		return true
	case "false":
		d.Skip()
		return false
	}
	var s string
	d.DecodeElement(&s, &se)
	if se.Name.Local == "integer" || se.Name.Local == "real" {
		v, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
		return v
	}
	return s
}
