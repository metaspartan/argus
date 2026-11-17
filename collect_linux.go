//go:build linux

package main

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Linux collector: pure procfs/sysfs reads with a reusable buffer.

type drmGPU struct {
	name      string
	dev       string // sysfs device dir
	driver    string
	hwmon     string
	card      string
	load      string // devfreq / jetson load file
	loadScale float64
	freq      string
}

type sensorFile struct {
	name, path string
	scale      float64
}

type Collector struct {
	buf      []byte
	prevCPU  []cpuTimes
	net      rates
	disk     rates
	procs    procTracker
	nv       *nvsmi
	gpus     []drmGPU
	temps    []sensorFile
	fans     []sensorFile
	rapl     []string
	raplPrev map[string]uint64
	raplT    time.Time
	fsCache  []FSStat
	tcpCache map[string]int
	bat      *Battery
	batPath  string
	clk      float64
	page     uint64
	users    map[uint32]string
	freqs    []string
	diskOK   map[string]bool
}

func newCollector() *Collector {
	c := &Collector{buf: make([]byte, 0, 64<<10), clk: 100, page: uint64(os.Getpagesize())}
	c.nv = startNvidiaSMI(2 * time.Second)
	c.scanSensors()
	c.gpus = scanDRM(nil)
	c.users = parsePasswd()
	c.freqs, _ = filepath.Glob("/sys/devices/system/cpu/cpu[0-9]*/cpufreq/scaling_cur_freq")
	for _, p := range []string{"/sys/class/powercap/intel-rapl:0", "/sys/class/powercap/intel-rapl:1"} {
		if _, err := os.ReadFile(p + "/energy_uj"); err == nil {
			c.rapl = append(c.rapl, p)
		}
	}
	c.raplPrev = map[string]uint64{}
	if bs, _ := filepath.Glob("/sys/class/power_supply/*"); len(bs) > 0 {
		for _, b := range bs {
			if readTrim(b+"/type") == "Battery" {
				c.batPath = b
				break
			}
		}
	}
	c.diskOK = map[string]bool{}
	return c
}

// read reads a small file into the shared buffer (valid until next read).
func (c *Collector) read(path string) []byte {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	c.buf = c.buf[:0]
	for {
		if len(c.buf) == cap(c.buf) {
			c.buf = append(c.buf, 0)[:len(c.buf)]
		}
		n, err := f.Read(c.buf[len(c.buf):cap(c.buf)])
		c.buf = c.buf[:len(c.buf)+n]
		if err != nil || n == 0 {
			break
		}
	}
	return c.buf
}

// ---------------------------------------------------------------- static

func (c *Collector) Static() *Static {
	s := baseStatic()
	s.Kernel = readTrim("/proc/sys/kernel/osrelease")
	root := hostFS()
	if hn := readTrim(root + "/etc/hostname"); root != "" && hn != "" {
		s.Hostname = hn
	}
	osr := parseKV(readTrim(root+"/etc/os-release"), "=")
	s.Platform = strings.Trim(osr["PRETTY_NAME"], `"`)
	if s.Platform == "" {
		s.Platform = "Linux"
	}
	s.ID = readTrim(root + "/etc/machine-id")
	if s.ID == "" {
		s.ID = readTrim("/var/lib/dbus/machine-id")
	}
	if s.ID == "" {
		s.ID = "host-" + s.Hostname
	}
	// Hardware model
	s.Vendor = readTrim("/sys/class/dmi/id/sys_vendor")
	s.Model = strings.TrimSpace(readTrim("/sys/class/dmi/id/product_name") + " " + readTrim("/sys/class/dmi/id/product_version"))
	if dtm := readTrim("/proc/device-tree/model"); dtm != "" && (s.Model == "" || strings.Contains(strings.ToLower(s.Model), "to be filled")) {
		s.Model = dtm
	}
	if bv := readTrim("/sys/class/dmi/id/bios_version"); bv != "" {
		s.Firmware = strings.TrimSpace(readTrim("/sys/class/dmi/id/bios_vendor") + " " + bv + " " + readTrim("/sys/class/dmi/id/bios_date"))
	}
	if bb := readTrim("/sys/class/dmi/id/board_name"); bb != "" {
		s.Extra["Board"] = strings.TrimSpace(readTrim("/sys/class/dmi/id/board_vendor") + " " + bb)
	}
	if comp := readTrim("/proc/device-tree/compatible"); comp != "" {
		parts := strings.Split(comp, "\x00")
		s.Extra["SoC"] = parts[len(parts)-1]
	}
	// CPU
	c.cpuStatic(s)
	// Memory, boot
	mi := c.meminfo()
	s.MemTotal = mi["MemTotal"]
	for _, l := range strings.Split(readTrim("/proc/stat"), "\n") {
		if strings.HasPrefix(l, "btime ") {
			s.BootTime = int64(atou(l[6:]))
		}
	}
	// Virtualization
	s.Virtual = detectVirt(s)
	// GPUs
	ids := loadPCIIDs()
	c.gpus = scanDRM(ids)
	nv := nvidiaStatic()
	s.GPUs = append(s.GPUs, nv...)
	for _, g := range c.gpus {
		if len(nv) > 0 && g.driver == "nvidia" {
			continue
		}
		gi := GPUInfo{Name: g.name, Driver: g.driver, Bus: filepath.Base(g.dev)}
		gi.Vendor = pciVendorName(readTrim(g.dev+"/vendor"), ids)
		if v := readTrim("/sys/module/" + g.driver + "/version"); v != "" {
			gi.DriverVersion = v
		} else {
			gi.DriverVersion = "kernel " + s.Kernel
		}
		if v, ok := readUint(g.dev + "/mem_info_vram_total"); ok {
			gi.VRAM = v
		}
		if v := readTrim(g.dev + "/vbios_version"); v != "" {
			gi.Extra = "VBIOS " + v
		}
		if g.driver == "amdgpu" {
			gi.API = "ROCm/Vulkan"
		}
		s.GPUs = append(s.GPUs, gi)
	}
	// NICs
	nics := nicAddrs()
	for name, n := range nics {
		base := "/sys/class/net/" + name
		if v, err := strconv.ParseInt(readTrim(base+"/speed"), 10, 64); err == nil && v > 0 {
			n.Speed = v
		}
		if d, err := os.Readlink(base + "/device/driver"); err == nil {
			n.Driver = filepath.Base(d)
			if v := readTrim("/sys/module/" + n.Driver + "/version"); v != "" {
				n.Driver += " " + v
			}
		} else if !n.Virt {
			n.Virt = true
		}
	}
	s.NICs = sortedNICs(nics)
	// Block devices
	blocks, _ := os.ReadDir("/sys/block")
	for _, b := range blocks {
		n := b.Name()
		if strings.HasPrefix(n, "loop") || strings.HasPrefix(n, "ram") || strings.HasPrefix(n, "zram") || strings.HasPrefix(n, "dm-") || strings.HasPrefix(n, "sr") {
			continue
		}
		sz, _ := readUint("/sys/block/" + n + "/size")
		if sz == 0 {
			continue
		}
		d := StorageDev{Name: n, Size: sz * 512, Model: strings.TrimSpace(readTrim("/sys/block/"+n+"/device/vendor") + " " + readTrim("/sys/block/"+n+"/device/model"))}
		switch {
		case strings.HasPrefix(n, "nvme"):
			d.Kind = "nvme"
		case strings.HasPrefix(n, "mmcblk"):
			d.Kind = "mmc"
			if d.Model == "" {
				d.Model = readTrim("/sys/block/" + n + "/device/name")
			}
		case readTrim("/sys/block/"+n+"/queue/rotational") == "1":
			d.Kind = "hdd"
		default:
			d.Kind = "ssd"
		}
		s.Storage = append(s.Storage, d)
	}
	// Devices (PCI + USB) and drivers (kernel modules)
	s.Devices = scanPCI(ids)
	s.Devices = append(s.Devices, scanUSB()...)
	s.Drivers = scanModules()
	if v := readTrim("/sys/module/nvidia/version"); v != "" {
		s.Extra["NVIDIA kernel module"] = v
	}
	if v := readTrim("/proc/driver/nvidia/version"); v != "" {
		s.Extra["NVIDIA driver"] = strings.SplitN(v, "\n", 2)[0]
	}
	if libc := glibcVersion(); libc != "" {
		s.Extra["libc"] = libc
	}
	if init, err := os.Readlink("/proc/1/exe"); err == nil {
		s.Extra["Init"] = filepath.Base(init)
	}
	return s
}

func (c *Collector) cpuStatic(s *Static) {
	data := readTrim("/proc/cpuinfo")
	type core struct{ phys, core string }
	cores := map[core]bool{}
	parts := map[string]int{}
	var phys, model, isa, uarch, hw, impl, flags string
	for _, line := range strings.Split(data, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		switch k {
		case "model name", "Model", "cpu model":
			if model == "" {
				model = v
			}
		case "physical id":
			phys = v
		case "core id":
			cores[core{phys, v}] = true
		case "isa":
			isa = v
		case "uarch":
			uarch = v
		case "Hardware":
			hw = v
		case "CPU implementer":
			impl = v
		case "CPU part":
			parts[impl+":"+v]++
		case "flags", "Features":
			flags = v
		}
	}
	if len(cores) > 0 {
		s.CPUCores = len(cores)
	} else {
		s.CPUCores = s.CPUThreads
	}
	switch {
	case model != "":
		s.CPUModel = model
	case uarch != "":
		s.CPUModel = uarch
	case len(parts) > 0:
		var segs []string
		for k, n := range parts {
			segs = append(segs, fmt.Sprintf("%d× %s", n, armPart(k)))
		}
		sort.Sort(sort.Reverse(sort.StringSlice(segs)))
		s.CPUModel = strings.Join(segs, " + ")
	case hw != "":
		s.CPUModel = hw
	default:
		s.CPUModel = s.Arch
	}
	if hw != "" && s.CPUModel != hw {
		s.Extra["SoC"] = hw
	}
	if isa != "" {
		s.CPUFlags = isa
		if mmu := grepField(data, "mmu"); mmu != "" {
			s.CPUFlags += " · " + mmu
		}
	} else if flags != "" {
		var keep []string
		fs := " " + flags + " "
		for _, f := range []string{"avx", "avx2", "avx512f", "avx512_bf16", "avx_vnni", "amx_tile", "sha_ni", "aes", "vmx", "svm", "neon", "asimd", "sve", "sve2", "sha2", "sha3", "atomics", "bf16", "i8mm", "fp16"} {
			if strings.Contains(fs, " "+f+" ") {
				keep = append(keep, f)
			}
		}
		s.CPUFlags = strings.Join(keep, " ")
	}
	var maxKHz uint64
	files, _ := filepath.Glob("/sys/devices/system/cpu/cpu[0-9]*/cpufreq/cpuinfo_max_freq")
	for _, f := range files {
		if v, ok := readUint(f); ok && v > maxKHz {
			maxKHz = v
		}
	}
	s.CPUMaxMHz = float64(maxKHz) / 1000
	if g := readTrim("/sys/devices/system/cpu/cpufreq/policy0/scaling_governor"); g != "" {
		s.Extra["CPU governor"] = g
	}
}

func grepField(data, key string) string {
	for _, line := range strings.Split(data, "\n") {
		if k, v, ok := strings.Cut(line, ":"); ok && strings.TrimSpace(k) == key {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

var armParts = map[string]string{
	"0x41:0xd03": "Cortex-A53", "0x41:0xd04": "Cortex-A35", "0x41:0xd05": "Cortex-A55", "0x41:0xd07": "Cortex-A57",
	"0x41:0xd08": "Cortex-A72", "0x41:0xd09": "Cortex-A73", "0x41:0xd0a": "Cortex-A75", "0x41:0xd0b": "Cortex-A76",
	"0x41:0xd0c": "Neoverse-N1", "0x41:0xd0d": "Cortex-A77", "0x41:0xd40": "Neoverse-V1", "0x41:0xd41": "Cortex-A78",
	"0x41:0xd44": "Cortex-X1", "0x41:0xd46": "Cortex-A510", "0x41:0xd47": "Cortex-A710", "0x41:0xd48": "Cortex-X2",
	"0x41:0xd49": "Neoverse-N2", "0x41:0xd4d": "Cortex-A715", "0x41:0xd4e": "Cortex-X3", "0x41:0xd4f": "Neoverse-V2",
	"0x41:0xd80": "Cortex-A520", "0x41:0xd81": "Cortex-A720", "0x41:0xd82": "Cortex-X4", "0x41:0xd84": "Neoverse-V3",
	"0x41:0xd8e": "Neoverse-N3", "0x41:0xd85": "Cortex-X925", "0x41:0xd87": "Cortex-A725",
	"0x61:0x022": "Apple Icestorm", "0x61:0x023": "Apple Firestorm", "0x61:0x024": "Apple Icestorm", "0x61:0x025": "Apple Firestorm",
	"0x61:0x028": "Apple Icestorm", "0x61:0x029": "Apple Firestorm", "0x61:0x032": "Apple Blizzard", "0x61:0x033": "Apple Avalanche",
	"0xc0:0xac3": "Ampere-1", "0xc0:0xac4": "Ampere-1a", "0x4e:0x004": "NVIDIA Carmel", "0x51:0x001": "Qualcomm Oryon",
}

func armPart(k string) string {
	if n, ok := armParts[k]; ok {
		return n
	}
	return "ARM " + k
}

func detectVirt(s *Static) string {
	if strings.Contains(strings.ToLower(s.Kernel), "microsoft") {
		return "WSL"
	}
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return "Docker"
	}
	if b, _ := os.ReadFile("/proc/1/cgroup"); bytes.Contains(b, []byte("kubepods")) {
		return "Kubernetes"
	}
	if b, _ := os.ReadFile("/proc/1/environ"); bytes.Contains(b, []byte("container=lxc")) {
		return "LXC"
	}
	v := strings.ToLower(s.Vendor + " " + s.Model)
	for _, h := range []string{"qemu", "kvm", "vmware", "virtualbox", "xen", "hyper-v", "parallels", "bochs", "amazon ec2", "google compute"} {
		if strings.Contains(v, h) {
			return h
		}
	}
	if s.Vendor == "Microsoft Corporation" && strings.Contains(s.Model, "Virtual") {
		return "Hyper-V"
	}
	return ""
}

func glibcVersion() string {
	for _, p := range []string{"/lib/x86_64-linux-gnu/libc.so.6", "/lib/aarch64-linux-gnu/libc.so.6", "/lib/riscv64-linux-gnu/libc.so.6", "/lib64/libc.so.6", "/usr/lib/libc.so.6"} {
		if t, err := filepath.EvalSymlinks(p); err == nil {
			b := filepath.Base(t)
			if strings.HasPrefix(b, "libc-") {
				return "glibc " + strings.TrimSuffix(strings.TrimPrefix(b, "libc-"), ".so")
			}
			return "glibc"
		}
	}
	if m, _ := filepath.Glob("/lib/ld-musl-*"); len(m) > 0 {
		return "musl"
	}
	return ""
}

func parseKV(s, sep string) map[string]string {
	m := map[string]string{}
	for _, l := range strings.Split(s, "\n") {
		if k, v, ok := strings.Cut(l, sep); ok {
			m[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return m
}

func parsePasswd() map[uint32]string {
	m := map[uint32]string{}
	for _, l := range strings.Split(readTrim("/etc/passwd"), "\n") {
		f := strings.Split(l, ":")
		if len(f) > 2 {
			if id, err := strconv.ParseUint(f[2], 10, 32); err == nil {
				m[uint32(id)] = f[0]
			}
		}
	}
	return m
}

// ---------------------------------------------------------------- PCI / USB / modules

type pciDB struct {
	vendors map[string]string
	devices map[string]string // "vvvv:dddd"
}

func loadPCIIDs() *pciDB {
	db := &pciDB{vendors: map[string]string{}, devices: map[string]string{}}
	// Only load names for devices present on this machine.
	want := map[string]bool{}
	wantV := map[string]bool{}
	ents, _ := os.ReadDir("/sys/bus/pci/devices")
	for _, e := range ents {
		v := strings.TrimPrefix(readTrim("/sys/bus/pci/devices/"+e.Name()+"/vendor"), "0x")
		d := strings.TrimPrefix(readTrim("/sys/bus/pci/devices/"+e.Name()+"/device"), "0x")
		want[v+":"+d] = true
		wantV[v] = true
	}
	if len(want) == 0 {
		return db
	}
	var f *os.File
	for _, p := range []string{"/usr/share/hwdata/pci.ids", "/usr/share/misc/pci.ids", "/usr/share/pci.ids"} {
		if ff, err := os.Open(p); err == nil {
			f = ff
			break
		}
	}
	if f == nil {
		return db
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	cur := ""
	for sc.Scan() {
		l := sc.Text()
		if l == "" || l[0] == '#' {
			continue
		}
		if strings.HasPrefix(l, "C ") {
			break
		}
		if l[0] != '\t' {
			if len(l) > 6 {
				cur = l[:4]
				if wantV[cur] {
					db.vendors[cur] = strings.TrimSpace(l[4:])
				}
			}
			continue
		}
		if len(l) > 7 && l[1] != '\t' && wantV[cur] {
			k := cur + ":" + l[1:5]
			if want[k] {
				db.devices[k] = strings.TrimSpace(l[5:])
			}
		}
	}
	return db
}

func pciVendorName(hex string, db *pciDB) string {
	hex = strings.TrimPrefix(hex, "0x")
	switch hex {
	case "10de":
		return "NVIDIA"
	case "1002":
		return "AMD"
	case "8086":
		return "Intel"
	}
	if db != nil {
		if n, ok := db.vendors[hex]; ok {
			return n
		}
	}
	return hex
}

var pciClasses = map[string]string{
	"00": "Unclassified", "01": "Storage", "02": "Network", "03": "Display", "04": "Multimedia", "05": "Memory",
	"06": "Bridge", "07": "Communication", "08": "System", "09": "Input", "0a": "Docking", "0b": "Processor",
	"0c": "Serial bus", "0d": "Wireless", "0e": "Intelligent", "0f": "Satellite", "10": "Encryption", "11": "Signal processing",
	"12": "Accelerator", "13": "Instrumentation",
}

func scanPCI(db *pciDB) []Device {
	var out []Device
	ents, _ := os.ReadDir("/sys/bus/pci/devices")
	for _, e := range ents {
		base := "/sys/bus/pci/devices/" + e.Name()
		v := strings.TrimPrefix(readTrim(base+"/vendor"), "0x")
		d := strings.TrimPrefix(readTrim(base+"/device"), "0x")
		cls := strings.TrimPrefix(readTrim(base+"/class"), "0x")
		dev := Device{Bus: "pci", ID: e.Name() + " [" + v + ":" + d + "]"}
		if len(cls) >= 2 {
			dev.Class = pciClasses[cls[:2]]
		}
		dev.Vendor = pciVendorName(v, db)
		if n, ok := db.devices[v+":"+d]; ok {
			dev.Name = n
		} else {
			dev.Name = "Device " + d
		}
		if l, err := os.Readlink(base + "/driver"); err == nil {
			dev.Driver = filepath.Base(l)
		}
		out = append(out, dev)
	}
	return out
}

func scanUSB() []Device {
	var out []Device
	ents, _ := os.ReadDir("/sys/bus/usb/devices")
	for _, e := range ents {
		base := "/sys/bus/usb/devices/" + e.Name()
		vid := readTrim(base + "/idVendor")
		if vid == "" || vid == "1d6b" { // skip root hubs
			continue
		}
		pid := readTrim(base + "/idProduct")
		dev := Device{Bus: "usb", ID: vid + ":" + pid, Vendor: readTrim(base + "/manufacturer"), Name: readTrim(base + "/product")}
		if dev.Name == "" {
			dev.Name = "USB device " + vid + ":" + pid
		}
		if sp := readTrim(base + "/speed"); sp != "" {
			dev.Class = sp + " Mb/s"
		}
		// Driver of first interface
		ifs, _ := filepath.Glob(base + "/" + e.Name() + ":*/driver")
		for _, i := range ifs {
			if l, err := os.Readlink(i); err == nil {
				dev.Driver = filepath.Base(l)
				break
			}
		}
		out = append(out, dev)
	}
	return out
}

func scanModules() []Driver {
	var out []Driver
	for _, l := range strings.Split(readTrim("/proc/modules"), "\n") {
		f := strings.Fields(l)
		if len(f) < 3 {
			continue
		}
		d := Driver{Name: f[0], Version: readTrim("/sys/module/" + f[0] + "/version")}
		if sz, err := strconv.Atoi(f[1]); err == nil {
			d.Info = fmt.Sprintf("%d KB · refs %s", sz/1024, f[2])
		}
		if len(f) > 3 && f[3] != "-" {
			d.Info += " · used by " + strings.TrimSuffix(f[3], ",")
		}
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func scanDRM(db *pciDB) []drmGPU {
	var out []drmGPU
	cards, _ := filepath.Glob("/sys/class/drm/card[0-9]*")
	seen := map[string]bool{}
	for _, card := range cards {
		if strings.Contains(filepath.Base(card), "-") {
			continue // connector
		}
		dev, err := filepath.EvalSymlinks(card + "/device")
		if err != nil || seen[dev] {
			continue
		}
		seen[dev] = true
		g := drmGPU{dev: dev, card: card}
		if l, err := os.Readlink(dev + "/driver"); err == nil {
			g.driver = filepath.Base(l)
		}
		v := strings.TrimPrefix(readTrim(dev+"/vendor"), "0x")
		d := strings.TrimPrefix(readTrim(dev+"/device"), "0x")
		if db != nil {
			g.name = db.devices[v+":"+d]
		}
		if g.name == "" {
			if comp := readTrim(dev + "/of_node/compatible"); comp != "" {
				g.name = strings.Split(comp, "\x00")[0]
			} else if v != "" {
				g.name = pciVendorName(v, db) + " GPU " + d
			} else {
				g.name = g.driver + " GPU"
			}
		}
		if hw, _ := filepath.Glob(dev + "/hwmon/hwmon*"); len(hw) > 0 {
			g.hwmon = hw[0]
		}
		if df, _ := filepath.Glob(dev + "/devfreq/*"); len(df) > 0 {
			if _, err := os.Stat(df[0] + "/load"); err == nil {
				g.load = df[0] + "/load"
			}
			g.freq = df[0] + "/cur_freq"
		}
		out = append(out, g)
	}
	// NVIDIA Jetson (nvgpu, not DRM-visible on older L4T)
	for _, p := range []string{"/sys/devices/gpu.0/load", "/sys/devices/platform/gpu.0/load", "/sys/devices/platform/17000000.gpu/load"} {
		if _, err := os.Stat(p); err == nil {
			out = append(out, drmGPU{name: "NVIDIA Tegra GPU", dev: filepath.Dir(p), driver: "nvgpu", load: p, loadScale: 10})
			break
		}
	}
	// Generic devfreq GPUs (Mali/PowerVR on ARM & RISC-V SBCs) not tied to a DRM card
	if len(out) == 0 {
		dfs, _ := filepath.Glob("/sys/class/devfreq/*")
		for _, d := range dfs {
			if strings.Contains(strings.ToLower(filepath.Base(d)), "gpu") {
				g := drmGPU{name: filepath.Base(d), dev: d, freq: d + "/cur_freq"}
				if _, err := os.Stat(d + "/load"); err == nil {
					g.load = d + "/load"
				}
				out = append(out, g)
			}
		}
	}
	return out
}

func (c *Collector) scanSensors() {
	hws, _ := filepath.Glob("/sys/class/hwmon/hwmon*")
	for _, h := range hws {
		name := readTrim(h + "/name")
		if name == "amdgpu" || name == "nouveau" {
			continue // reported with the GPU
		}
		ins, _ := filepath.Glob(h + "/temp*_input")
		for _, in := range ins {
			lbl := readTrim(strings.TrimSuffix(in, "_input") + "_label")
			n := name
			if lbl != "" {
				n += " " + lbl
			}
			c.temps = append(c.temps, sensorFile{n, in, 1000})
		}
		fans, _ := filepath.Glob(h + "/fan*_input")
		for _, in := range fans {
			lbl := readTrim(strings.TrimSuffix(in, "_input") + "_label")
			n := name + " " + strings.TrimSuffix(filepath.Base(in), "_input")
			if lbl != "" {
				n = name + " " + lbl
			}
			c.fans = append(c.fans, sensorFile{n, in, 1})
		}
	}
	if len(c.temps) == 0 {
		zs, _ := filepath.Glob("/sys/class/thermal/thermal_zone*")
		for _, z := range zs {
			c.temps = append(c.temps, sensorFile{readTrim(z + "/type"), z + "/temp", 1000})
		}
	}
	if len(c.temps) > 40 {
		c.temps = c.temps[:40]
	}
}

// ---------------------------------------------------------------- dynamic

func (c *Collector) meminfo() map[string]uint64 {
	m := make(map[string]uint64, 16)
	b := c.read("/proc/meminfo")
	for len(b) > 0 {
		i := bytes.IndexByte(b, '\n')
		line := b
		if i >= 0 {
			line, b = b[:i], b[i+1:]
		} else {
			b = nil
		}
		k, v, ok := bytes.Cut(line, []byte(":"))
		if !ok {
			continue
		}
		switch string(k) {
		case "MemTotal", "MemAvailable", "MemFree", "Buffers", "Cached", "SReclaimable", "Shmem", "SwapTotal", "SwapFree":
			f := bytes.Fields(v)
			if len(f) > 0 {
				n, _ := strconv.ParseUint(string(f[0]), 10, 64)
				m[string(k)] = n * 1024
			}
		}
	}
	return m
}

func (c *Collector) Collect(tick int) *Metrics {
	m := &Metrics{T: time.Now().UnixMilli()}

	// CPU
	var cur []cpuTimes
	b := c.read("/proc/stat")
	for _, line := range bytes.Split(b, []byte("\n")) {
		if bytes.HasPrefix(line, []byte("cpu")) {
			f := bytes.Fields(line)
			if len(f) < 8 {
				continue
			}
			var v [10]uint64
			for i := 1; i < len(f) && i <= 10; i++ {
				v[i-1], _ = strconv.ParseUint(string(f[i]), 10, 64)
			}
			total := v[0] + v[1] + v[2] + v[3] + v[4] + v[5] + v[6] + v[7]
			idle := v[3] + v[4]
			cur = append(cur, cpuTimes{busy: total - idle, total: total})
		}
	}
	m.CPU, m.Cores = cpuPct(c.prevCPU, cur)
	c.prevCPU = cur

	// Frequency
	if len(c.freqs) > 0 {
		var sum float64
		n := 0
		for _, f := range c.freqs {
			if b := c.read(f); len(b) > 0 {
				sum += atof(string(b))
				n++
			}
		}
		if n > 0 {
			m.CPUMHz = round1(sum / float64(n) / 1000)
		}
	}

	// Load / uptime
	if f := bytes.Fields(c.read("/proc/loadavg")); len(f) >= 3 {
		m.Load = []float64{atof(string(f[0])), atof(string(f[1])), atof(string(f[2]))}
	}
	if f := bytes.Fields(c.read("/proc/uptime")); len(f) > 0 {
		m.Uptime = uint64(atof(string(f[0])))
	}

	// Memory
	mi := c.meminfo()
	m.Mem.Total = mi["MemTotal"]
	m.Mem.Avail = mi["MemAvailable"]
	if m.Mem.Avail == 0 {
		m.Mem.Avail = mi["MemFree"] + mi["Cached"] + mi["Buffers"]
	}
	m.Mem.Used = m.Mem.Total - min(m.Mem.Avail, m.Mem.Total)
	m.Mem.Cached = mi["Cached"] + mi["Buffers"] + mi["SReclaimable"]
	m.Mem.SwapTotal = mi["SwapTotal"]
	m.Mem.SwapUsed = mi["SwapTotal"] - min(mi["SwapFree"], mi["SwapTotal"])
	if p := c.read("/proc/pressure/memory"); len(p) > 0 {
		if i := bytes.Index(p, []byte("avg10=")); i >= 0 {
			if f := bytes.Fields(p[i+6:]); len(f) > 0 {
				m.Mem.Pressure = atof(string(f[0]))
			}
		}
	}

	// Network
	c.net.begin()
	b = c.read("/proc/net/dev")
	for _, line := range bytes.Split(b, []byte("\n")) {
		name, rest, ok := bytes.Cut(line, []byte(":"))
		if !ok {
			continue
		}
		f := bytes.Fields(rest)
		if len(f) < 16 {
			continue
		}
		n := string(bytes.TrimSpace(name))
		rxb, rxp, rxe := atou(string(f[0])), atou(string(f[1])), atou(string(f[2]))
		txb, txp, txe := atou(string(f[8])), atou(string(f[9])), atou(string(f[10]))
		rx, tx, rp, tp := c.net.delta(n, rateKey{rxb, txb, rxp, txp})
		if n == "lo" {
			continue
		}
		m.Net = append(m.Net, NetStat{Name: n, Rx: round1(rx), Tx: round1(tx), RxTotal: rxb, TxTotal: txb, RxPkts: round1(rp), TxPkts: round1(tp), Errs: rxe + txe})
	}

	// Disk IO
	c.disk.begin()
	b = c.read("/proc/diskstats")
	for _, line := range bytes.Split(b, []byte("\n")) {
		f := bytes.Fields(line)
		if len(f) < 14 {
			continue
		}
		n := string(f[2])
		ok, seen := c.diskOK[n]
		if !seen {
			_, err := os.Stat("/sys/block/" + n)
			ok = err == nil && !strings.HasPrefix(n, "loop") && !strings.HasPrefix(n, "ram") && !strings.HasPrefix(n, "zram") && !strings.HasPrefix(n, "dm-") && !strings.HasPrefix(n, "sr")
			c.diskOK[n] = ok
		}
		if !ok {
			continue
		}
		r, w, busy, _ := c.disk.delta(n, rateKey{atou(string(f[5])) * 512, atou(string(f[9])) * 512, atou(string(f[12])), 0})
		m.Disks = append(m.Disks, DiskIO{Name: n, R: round1(r), W: round1(w), Busy: clampPct(busy / 10)})
	}

	// GPUs
	if s := c.nv.snapshot(); len(s) > 0 {
		m.GPUs = append(m.GPUs, s...)
	}
	for _, g := range c.gpus {
		if g.driver == "nvidia" && c.nv != nil {
			continue
		}
		st := GPUStat{Name: g.name}
		if v, ok := readUint(g.dev + "/gpu_busy_percent"); ok {
			st.Util = float64(v)
		} else if g.load != "" {
			s := readTrim(g.load)
			if i := strings.IndexByte(s, '@'); i > 0 {
				s = s[:i]
			}
			st.Util = atof(s)
			if g.loadScale > 0 {
				st.Util /= g.loadScale
			}
		}
		if v, ok := readUint(g.dev + "/mem_info_vram_used"); ok {
			st.MemUsed = v
			st.MemTotal, _ = readUint(g.dev + "/mem_info_vram_total")
		}
		if g.hwmon != "" {
			if v, ok := readUint(g.hwmon + "/temp1_input"); ok {
				st.Temp = float64(v) / 1000
			}
			if v, ok := readUint(g.hwmon + "/power1_average"); ok {
				st.Watts = round1(float64(v) / 1e6)
			} else if v, ok := readUint(g.hwmon + "/power1_input"); ok {
				st.Watts = round1(float64(v) / 1e6)
			}
			if v, ok := readUint(g.hwmon + "/freq1_input"); ok {
				st.MHz = float64(v) / 1e6
			}
			if v, ok := readUint(g.hwmon + "/fan1_input"); ok {
				st.Fan = float64(v)
			}
		}
		if st.MHz == 0 {
			if v, ok := readUint(g.card + "/gt_act_freq_mhz"); ok {
				st.MHz = float64(v)
			} else if v, ok := readUint(g.freq); ok && g.freq != "" {
				st.MHz = float64(v) / 1e6
			}
		}
		st.Util = clampPct(st.Util)
		m.GPUs = append(m.GPUs, st)
	}

	// Sensors
	for _, s := range c.temps {
		if v, ok := readUint(s.path); ok && v > 0 {
			m.Temps = append(m.Temps, Sensor{s.name, round1(float64(v) / s.scale)})
		}
	}
	for _, s := range c.fans {
		if v, ok := readUint(s.path); ok {
			m.Fans = append(m.Fans, Sensor{s.name, float64(v)})
		}
	}

	// RAPL CPU package power
	if len(c.rapl) > 0 {
		now := time.Now()
		dt := now.Sub(c.raplT).Seconds()
		var watts float64
		for _, p := range c.rapl {
			e, ok := readUint(p + "/energy_uj")
			if !ok {
				continue
			}
			if old, had := c.raplPrev[p]; had && dt > 0 && e >= old {
				watts += float64(e-old) / 1e6 / dt
			}
			c.raplPrev[p] = e
		}
		c.raplT = now
		m.CPUWatts = round1(watts)
	}

	// Filesystems
	if tick%everyFS == 0 || c.fsCache == nil {
		c.fsCache = c.filesystems()
	}
	m.FS = c.fsCache

	// Processes
	if heavyTick(tick, everyProcs) {
		m.Procs, m.NProcs = c.processes()
	}

	// TCP
	if heavyTick(tick, everyTCP) {
		c.tcpCache = c.tcpStates()
	}
	m.TCP = c.tcpCache

	// Battery
	if c.batPath != "" && (tick%everyBattery == 0 || c.bat == nil) {
		pct, _ := readUint(c.batPath + "/capacity")
		st := readTrim(c.batPath + "/status")
		c.bat = &Battery{Percent: float64(pct), Charging: st == "Charging", State: st}
	}
	m.Battery = c.bat
	return m
}

var realFS = map[string]bool{"ext2": true, "ext3": true, "ext4": true, "xfs": true, "btrfs": true, "zfs": true, "f2fs": true, "vfat": true, "exfat": true, "ntfs": true, "ntfs3": true, "fuseblk": true, "bcachefs": true, "nfs": true, "nfs4": true, "cifs": true, "smb3": true, "jfs": true, "reiserfs": true, "hfsplus": true, "apfs": true, "erofs": false}

func (c *Collector) filesystems() []FSStat {
	var out []FSStat
	seen := map[string]bool{}
	for _, line := range strings.Split(string(c.read("/proc/self/mounts")), "\n") {
		f := strings.Fields(line)
		if len(f) < 3 || !realFS[f[2]] {
			continue
		}
		dev, mnt := f[0], strings.ReplaceAll(f[1], `\040`, " ")
		if root := hostFS(); root != "" {
			// Running in a container with the host's / mounted at $ARGUS_HOSTFS:
			// report only host filesystems, under their real mount points.
			if mnt != root && !strings.HasPrefix(mnt, root+"/") {
				continue
			}
		}
		if seen[dev] || strings.HasPrefix(mnt, "/snap/") || strings.HasPrefix(mnt, "/var/lib/docker") {
			continue
		}
		var st syscall.Statfs_t
		if syscall.Statfs(mnt, &st) != nil || st.Blocks == 0 {
			continue
		}
		seen[dev] = true
		bs := uint64(st.Bsize)
		if root := hostFS(); root != "" {
			if mnt = strings.TrimPrefix(mnt, root); mnt == "" {
				mnt = "/"
			}
		}
		out = append(out, FSStat{Mount: mnt, Dev: dev, Type: f[2], Total: st.Blocks * bs, Used: (st.Blocks - st.Bfree) * bs})
	}
	return out
}

func (c *Collector) processes() ([]Proc, int) {
	d, err := os.Open("/proc")
	if err != nil {
		return nil, 0
	}
	names, _ := d.Readdirnames(-1)
	d.Close()
	list := make([]procSample, 0, len(names))
	for _, n := range names {
		if n[0] < '0' || n[0] > '9' {
			continue
		}
		pid, err := strconv.Atoi(n)
		if err != nil {
			continue
		}
		b := c.read("/proc/" + n + "/stat")
		lp, rp := bytes.IndexByte(b, '('), bytes.LastIndexByte(b, ')')
		if lp < 0 || rp < lp {
			continue
		}
		f := bytes.Fields(b[rp+1:])
		if len(f) < 22 {
			continue
		}
		ut, st := atou(string(f[11])), atou(string(f[12]))
		rss := atou(string(f[21])) * c.page
		list = append(list, procSample{pid: pid, name: string(b[lp+1 : rp]), cpuNs: uint64(float64(ut+st) / c.clk * 1e9), mem: rss})
	}
	top := c.procs.top(list, false)
	for i := range top {
		var st syscall.Stat_t
		if syscall.Stat("/proc/"+strconv.Itoa(top[i].PID), &st) == nil {
			if u, ok := c.users[st.Uid]; ok {
				top[i].User = u
			} else {
				top[i].User = strconv.Itoa(int(st.Uid))
			}
		}
	}
	return top, len(list)
}

var tcpStateNames = map[string]string{"01": "established", "02": "syn_sent", "03": "syn_recv", "04": "fin_wait1", "05": "fin_wait2", "06": "time_wait", "07": "close", "08": "close_wait", "09": "last_ack", "0A": "listen", "0B": "closing"}

func (c *Collector) tcpStates() map[string]int {
	m := map[string]int{}
	for _, p := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Scan() // header
		for sc.Scan() {
			fl := strings.Fields(sc.Text())
			if len(fl) > 3 {
				if n, ok := tcpStateNames[fl[3]]; ok {
					m[n]++
				}
			}
		}
		f.Close()
	}
	return m
}

// hostFS is where the host's root filesystem is mounted when the agent runs
// in a container (docker-compose "host-agent" profile sets ARGUS_HOSTFS=/host).
func hostFS() string { return strings.TrimRight(os.Getenv("ARGUS_HOSTFS"), "/") }
