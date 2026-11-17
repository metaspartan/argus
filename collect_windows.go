//go:build windows

package main

import (
	"encoding/binary"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

var (
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	ntdll    = syscall.NewLazyDLL("ntdll.dll")
	iphlpapi = syscall.NewLazyDLL("iphlpapi.dll")
	pdh      = syscall.NewLazyDLL("pdh.dll")
	powrprof = syscall.NewLazyDLL("powrprof.dll")
	dxgi     = syscall.NewLazyDLL("dxgi.dll")
	cfgmgr32 = syscall.NewLazyDLL("cfgmgr32.dll")

	pGetSystemTimes            = kernel32.NewProc("GetSystemTimes")
	pGlobalMemoryStatusEx      = kernel32.NewProc("GlobalMemoryStatusEx")
	pGetTickCount64            = kernel32.NewProc("GetTickCount64")
	pGetLogicalDriveStringsW   = kernel32.NewProc("GetLogicalDriveStringsW")
	pGetDriveTypeW             = kernel32.NewProc("GetDriveTypeW")
	pGetDiskFreeSpaceExW       = kernel32.NewProc("GetDiskFreeSpaceExW")
	pGetVolumeInformationW     = kernel32.NewProc("GetVolumeInformationW")
	pGetSystemPowerStatus      = kernel32.NewProc("GetSystemPowerStatus")
	pK32GetProcessMemoryInfo   = kernel32.NewProc("K32GetProcessMemoryInfo")
	pK32GetPerformanceInfo     = kernel32.NewProc("K32GetPerformanceInfo")
	pGetLogicalProcessorInfoEx = kernel32.NewProc("GetLogicalProcessorInformationEx")
	pNtQuerySystemInformation  = ntdll.NewProc("NtQuerySystemInformation")
	pRtlGetVersion             = ntdll.NewProc("RtlGetVersion")
	pGetIfTable2               = iphlpapi.NewProc("GetIfTable2")
	pFreeMibTable              = iphlpapi.NewProc("FreeMibTable")
	pGetIfEntry2               = iphlpapi.NewProc("GetIfEntry2")
	pGetTcpTable               = iphlpapi.NewProc("GetTcpTable")
	pCallNtPowerInformation    = powrprof.NewProc("CallNtPowerInformation")
	pCreateDXGIFactory1        = dxgi.NewProc("CreateDXGIFactory1")
	pCMLocateDevNodeW          = cfgmgr32.NewProc("CM_Locate_DevNodeW")
	pPdhOpenQuery              = pdh.NewProc("PdhOpenQueryW")
	pPdhAddEnglishCounter      = pdh.NewProc("PdhAddEnglishCounterW")
	pPdhCollectQueryData       = pdh.NewProc("PdhCollectQueryData")
	pPdhGetFormattedArray      = pdh.NewProc("PdhGetFormattedCounterArrayW")
)

type winGPU struct {
	name   string
	luid   string // "0x%08X_0x%08X" (high_low)
	vendor uint32
	vram   uint64
}

type Collector struct {
	prevCPU  []cpuTimes
	net      rates
	procs    procTracker
	nv       *nvsmi
	q        *pdhQuery
	gpus     []winGPU
	fsCache  []FSStat
	tcpCache map[string]int
	bat      *Battery
	maxMHz   float64
	nicMeta  map[string]bool
	ifIdx    []uint32
	ifBuf    []byte
	spiBuf   []byte
}

func newCollector() *Collector {
	c := &Collector{nv: startNvidiaSMI(2 * time.Second)}
	c.gpus = dxgiAdapters()
	c.q = newPDH()
	if c.q != nil {
		c.q.add("diskR", `\PhysicalDisk(*)\Disk Read Bytes/sec`)
		c.q.add("diskW", `\PhysicalDisk(*)\Disk Write Bytes/sec`)
		c.q.add("diskIdle", `\PhysicalDisk(*)\% Idle Time`)
		c.q.add("gpuEng", `\GPU Engine(*)\Utilization Percentage`)
		c.q.add("gpuMem", `\GPU Adapter Memory(*)\Dedicated Usage`)
		c.q.add("perf", `\Processor Information(_Total)\% Processor Performance`)
		c.q.add("thermal", `\Thermal Zone Information(*)\Temperature`)
		c.q.add("pagefile", `\Paging File(_Total)\% Usage`)
		c.q.collect()
	}
	for _, p := range powerInfo() {
		if float64(p[1]) > c.maxMHz {
			c.maxMHz = float64(p[1])
		}
	}
	return c
}

// ---------------------------------------------------------------- static

func (c *Collector) Static() *Static {
	s := baseStatic()
	const cv = `SOFTWARE\Microsoft\Windows NT\CurrentVersion`
	prod := regStr(cv, "ProductName")
	build := regStr(cv, "CurrentBuild")
	if b, _ := strconv.Atoi(build); b >= 22000 {
		prod = strings.Replace(prod, "Windows 10", "Windows 11", 1)
	}
	s.Platform = strings.TrimSpace(prod + " " + regStr(cv, "DisplayVersion"))
	ubr := regDword(cv, "UBR")
	s.Kernel = fmt.Sprintf("NT %s.%d", build, ubr)
	if v := rtlVersion(); v != "" {
		s.Kernel = v + "." + strconv.Itoa(int(ubr))
	}
	s.ID = regStr(`SOFTWARE\Microsoft\Cryptography`, "MachineGuid")
	if s.ID == "" {
		s.ID = "host-" + s.Hostname
	}
	const bios = `HARDWARE\DESCRIPTION\System\BIOS`
	s.Vendor = regStr(bios, "SystemManufacturer")
	s.Model = regStr(bios, "SystemProductName")
	s.Firmware = strings.TrimSpace(regStr(bios, "BIOSVendor") + " " + regStr(bios, "BIOSVersion") + " " + regStr(bios, "BIOSReleaseDate"))
	if bb := regStr(bios, "BaseBoardProduct"); bb != "" {
		s.Extra["Board"] = strings.TrimSpace(regStr(bios, "BaseBoardManufacturer") + " " + bb)
	}
	s.Extra["Edition"] = regStr(cv, "EditionID")
	if it := regQword(cv, "InstallDate"); it > 0 {
		s.Extra["Installed"] = time.Unix(int64(it), 0).Format("2006-01-02")
	}
	const cpu0 = `HARDWARE\DESCRIPTION\System\CentralProcessor\0`
	s.CPUModel = strings.TrimSpace(regStr(cpu0, "ProcessorNameString"))
	s.CPUFlags = regStr(cpu0, "Identifier")
	s.CPUCores = physicalCores()
	if s.CPUCores == 0 {
		s.CPUCores = s.CPUThreads
	}
	s.CPUMaxMHz = c.maxMHz
	if s.CPUMaxMHz == 0 {
		s.CPUMaxMHz = float64(regDword(cpu0, "~MHz"))
	}
	var ms memStatusEx
	ms.Length = uint32(unsafe.Sizeof(ms))
	pGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&ms)))
	s.MemTotal = ms.TotalPhys
	up, _, _ := pGetTickCount64.Call()
	s.BootTime = time.Now().Add(-time.Duration(up) * time.Millisecond).Unix()
	if strings.Contains(strings.ToLower(s.Model), "virtual") || strings.Contains(s.Vendor, "QEMU") || strings.Contains(s.Vendor, "VMware") {
		s.Virtual = s.Model
	}

	// GPUs via DXGI + driver info from the Display class registry keys
	nv := nvidiaStatic()
	drv := displayDrivers()
	for _, g := range c.gpus {
		if g.vendor == 0x10de && len(nv) > 0 {
			continue
		}
		gi := GPUInfo{Name: g.name, VRAM: g.vram, Vendor: vendorName(g.vendor), API: "DirectX 12"}
		if d, ok := drv[g.name]; ok {
			gi.Driver, gi.DriverVersion, gi.Extra = d[0], d[1], d[2]
		}
		s.GPUs = append(s.GPUs, gi)
	}
	for _, g := range nv {
		if d, ok := drv[g.Name]; ok {
			g.Extra = strings.TrimSpace(g.Extra + " · WDDM " + d[1])
		}
		s.GPUs = append(s.GPUs, g)
	}

	// NICs
	addrs := nicAddrs()
	c.nicMeta = map[string]bool{}
	for _, r := range ifTable() {
		if r.filter || (!r.hardware && !strings.HasPrefix(r.alias, "vEthernet")) {
			continue
		}
		n := addrs[r.alias]
		if n == nil {
			n = &NICInfo{Name: r.alias}
		}
		n.Driver = r.desc
		n.Speed = int64(r.speed / 1e6)
		n.Up = r.oper == 1
		n.Virt = !r.hardware
		c.nicMeta[r.alias] = true
		addrs[r.alias] = n
	}
	for k, n := range addrs {
		if (!c.nicMeta[k] && (n.Virt || len(n.Addrs) == 0)) || (!n.Up && len(n.Addrs) == 0) {
			delete(addrs, k)
		}
	}
	s.NICs = sortedNICs(addrs)

	// Devices & drivers from the PnP registry (present devices only)
	s.Devices, s.Drivers = pnpDevices()
	return s
}

func vendorName(v uint32) string {
	switch v {
	case 0x10de:
		return "NVIDIA"
	case 0x1002, 0x1022:
		return "AMD"
	case 0x8086:
		return "Intel"
	case 0x5143, 0x4d4f4351:
		return "Qualcomm"
	case 0x1414:
		return "Microsoft"
	}
	return fmt.Sprintf("0x%04x", v)
}

func rtlVersion() string {
	var v struct {
		Size                          uint32
		Major, Minor, Build, Platform uint32
		CSD                           [128]uint16
	}
	v.Size = uint32(unsafe.Sizeof(v))
	if r, _, _ := pRtlGetVersion.Call(uintptr(unsafe.Pointer(&v))); r != 0 {
		return ""
	}
	return fmt.Sprintf("NT %d.%d.%d", v.Major, v.Minor, v.Build)
}

func physicalCores() int {
	var n uint32
	pGetLogicalProcessorInfoEx.Call(0, 0, uintptr(unsafe.Pointer(&n)))
	if n == 0 {
		return 0
	}
	buf := make([]byte, n)
	if r, _, _ := pGetLogicalProcessorInfoEx.Call(0, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&n))); r == 0 {
		return 0
	}
	cores := 0
	for off := uint32(0); off+8 <= n; {
		size := binary.LittleEndian.Uint32(buf[off+4:])
		if size == 0 {
			break
		}
		cores++
		off += size
	}
	return cores
}

// ---------------------------------------------------------------- registry

func regOpen(path string) (syscall.Handle, bool) {
	var h syscall.Handle
	p, _ := syscall.UTF16PtrFromString(path)
	if syscall.RegOpenKeyEx(syscall.HKEY_LOCAL_MACHINE, p, 0, syscall.KEY_READ, &h) != nil {
		return 0, false
	}
	return h, true
}

func regVal(h syscall.Handle, name string) ([]byte, uint32) {
	n, _ := syscall.UTF16PtrFromString(name)
	var typ, size uint32
	if syscall.RegQueryValueEx(h, n, nil, &typ, nil, &size) != nil || size == 0 {
		return nil, 0
	}
	buf := make([]byte, size)
	if syscall.RegQueryValueEx(h, n, nil, &typ, &buf[0], &size) != nil {
		return nil, 0
	}
	return buf[:size], typ
}

func regValStr(h syscall.Handle, name string) string {
	b, typ := regVal(h, name)
	switch typ {
	case syscall.REG_SZ, syscall.REG_EXPAND_SZ, syscall.REG_MULTI_SZ:
		u := make([]uint16, len(b)/2)
		for i := range u {
			u[i] = binary.LittleEndian.Uint16(b[i*2:])
		}
		s := syscall.UTF16ToString(u)
		return strings.TrimSpace(s)
	case syscall.REG_DWORD:
		if len(b) >= 4 {
			return strconv.FormatUint(uint64(binary.LittleEndian.Uint32(b)), 10)
		}
	case syscall.REG_QWORD:
		if len(b) >= 8 {
			return strconv.FormatUint(binary.LittleEndian.Uint64(b), 10)
		}
	}
	return ""
}

func regStr(path, name string) string {
	h, ok := regOpen(path)
	if !ok {
		return ""
	}
	defer syscall.RegCloseKey(h)
	return regValStr(h, name)
}

func regDword(path, name string) uint32 {
	v, _ := strconv.ParseUint(regStr(path, name), 10, 32)
	return uint32(v)
}

func regQword(path, name string) uint64 {
	v, _ := strconv.ParseUint(regStr(path, name), 10, 64)
	return v
}

func regSubkeys(path string) []string {
	h, ok := regOpen(path)
	if !ok {
		return nil
	}
	defer syscall.RegCloseKey(h)
	var out []string
	buf := make([]uint16, 512)
	for i := uint32(0); ; i++ {
		n := uint32(len(buf))
		if syscall.RegEnumKeyEx(h, i, &buf[0], &n, nil, nil, nil, nil) != nil {
			break
		}
		out = append(out, syscall.UTF16ToString(buf[:n]))
	}
	return out
}

func cleanDesc(s string) string {
	// "@oem12.inf,%desc%;Friendly Name" -> "Friendly Name"
	if i := strings.LastIndexByte(s, ';'); i >= 0 && strings.HasPrefix(s, "@") {
		return s[i+1:]
	}
	return s
}

const classKey = `SYSTEM\CurrentControlSet\Control\Class\`

// displayDrivers maps adapter description -> [driver service, version, extra].
func displayDrivers() map[string][3]string {
	out := map[string][3]string{}
	const disp = classKey + `{4d36e968-e325-11ce-bfc1-08002be10318}`
	for _, k := range regSubkeys(disp) {
		h, ok := regOpen(disp + `\` + k)
		if !ok {
			continue
		}
		desc := regValStr(h, "DriverDesc")
		if desc != "" {
			extra := strings.Trim("dated "+regValStr(h, "DriverDate")+" · "+regValStr(h, "InfPath"), " ·")
			out[desc] = [3]string{regValStr(h, "ProviderName"), regValStr(h, "DriverVersion"), extra}
		}
		syscall.RegCloseKey(h)
	}
	return out
}

func devPresent(instanceID string) bool {
	var inst uint32
	p, _ := syscall.UTF16PtrFromString(instanceID)
	r, _, _ := pCMLocateDevNodeW.Call(uintptr(unsafe.Pointer(&inst)), uintptr(unsafe.Pointer(p)), 0)
	return r == 0
}

func pnpDevices() ([]Device, []Driver) {
	var devs []Device
	drv := map[string]Driver{}
	for _, bus := range []string{"PCI", "USB", "HDAUDIO", "ACPI", "SWD\\DRIVERENUM"} {
		base := `SYSTEM\CurrentControlSet\Enum\` + bus
		for _, hw := range regSubkeys(base) {
			for _, inst := range regSubkeys(base + `\` + hw) {
				id := bus + `\` + hw + `\` + inst
				if !devPresent(id) {
					continue
				}
				h, ok := regOpen(base + `\` + hw + `\` + inst)
				if !ok {
					continue
				}
				d := Device{Bus: strings.ToLower(strings.Split(bus, `\`)[0]), ID: hw, Class: regValStr(h, "Class")}
				d.Name = cleanDesc(regValStr(h, "FriendlyName"))
				if d.Name == "" {
					d.Name = cleanDesc(regValStr(h, "DeviceDesc"))
				}
				d.Vendor = cleanDesc(regValStr(h, "Mfg"))
				svc := regValStr(h, "Service")
				dk := regValStr(h, "Driver")
				syscall.RegCloseKey(h)
				if bus == "ACPI" && d.Class != "Processor" && d.Class != "System" {
					// keep ACPI list short
				}
				if bus == "ACPI" && d.Class == "Processor" {
					continue
				}
				if d.Name == "" {
					continue
				}
				d.Driver = svc
				if dk != "" {
					if ch, ok := regOpen(classKey + dk); ok {
						ver := regValStr(ch, "DriverVersion")
						prov := regValStr(ch, "ProviderName")
						date := regValStr(ch, "DriverDate")
						syscall.RegCloseKey(ch)
						if ver != "" {
							d.Driver = strings.TrimSpace(svc + " " + ver)
							key := svc + "|" + ver
							if svc == "" {
								key = d.Name + "|" + ver
							}
							if _, ok := drv[key]; !ok {
								name := svc
								if name == "" {
									name = d.Name
								}
								drv[key] = Driver{Name: name, Version: ver, Info: strings.Trim(prov+" · "+date+" · "+d.Name, " ·")}
							}
						}
					}
				}
				devs = append(devs, d)
			}
		}
	}
	out := make([]Driver, 0, len(drv))
	for _, d := range drv {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	sort.SliceStable(devs, func(i, j int) bool { return devs[i].Bus+devs[i].Class < devs[j].Bus+devs[j].Class })
	return devs, out
}

// ---------------------------------------------------------------- DXGI

var iidIDXGIFactory1 = syscall.GUID{Data1: 0x770aae78, Data2: 0xf26f, Data3: 0x4dba, Data4: [8]byte{0xa8, 0x29, 0x25, 0x3c, 0x83, 0xd1, 0xb3, 0x87}}

func comCall(obj uintptr, idx int, args ...uintptr) uintptr {
	vtbl := *(*uintptr)(unsafe.Pointer(obj))
	fn := *(*uintptr)(unsafe.Pointer(vtbl + uintptr(idx)*unsafe.Sizeof(uintptr(0))))
	r, _, _ := syscall.SyscallN(fn, append([]uintptr{obj}, args...)...)
	return r
}

func dxgiAdapters() (out []winGPU) {
	defer func() { recover() }()
	if pCreateDXGIFactory1.Find() != nil {
		return nil
	}
	var factory uintptr
	if r, _, _ := pCreateDXGIFactory1.Call(uintptr(unsafe.Pointer(&iidIDXGIFactory1)), uintptr(unsafe.Pointer(&factory))); r != 0 || factory == 0 {
		return nil
	}
	defer comCall(factory, 2)
	for i := uintptr(0); i < 16; i++ {
		var adapter uintptr
		if r := comCall(factory, 12, i, uintptr(unsafe.Pointer(&adapter))); r != 0 || adapter == 0 {
			break
		}
		var desc [312]byte
		comCall(adapter, 10, uintptr(unsafe.Pointer(&desc[0])))
		comCall(adapter, 2)
		flags := binary.LittleEndian.Uint32(desc[304:])
		if flags&2 != 0 { // DXGI_ADAPTER_FLAG_SOFTWARE
			continue
		}
		u := make([]uint16, 128)
		for j := range u {
			u[j] = binary.LittleEndian.Uint16(desc[j*2:])
		}
		g := winGPU{
			name:   syscall.UTF16ToString(u),
			vendor: binary.LittleEndian.Uint32(desc[256:]),
			vram:   binary.LittleEndian.Uint64(desc[272:]),
			luid:   fmt.Sprintf("0x%08x_0x%08x", binary.LittleEndian.Uint32(desc[300:]), binary.LittleEndian.Uint32(desc[296:])),
		}
		if g.vendor == 0x1414 { // Microsoft Basic Render
			continue
		}
		// The same physical adapter can surface under several LUIDs; merge them.
		dup := false
		for k := range out {
			if out[k].name == g.name && out[k].vendor == g.vendor && out[k].vram == g.vram {
				out[k].luid += "|" + g.luid
				dup = true
			}
		}
		if !dup {
			out = append(out, g)
		}
	}
	return out
}

// ---------------------------------------------------------------- PDH

type pdhQuery struct {
	h        uintptr
	counters map[string]uintptr
	buf      []byte
}

func newPDH() *pdhQuery {
	if pPdhOpenQuery.Find() != nil {
		return nil
	}
	q := &pdhQuery{counters: map[string]uintptr{}, buf: make([]byte, 64<<10)}
	if r, _, _ := pPdhOpenQuery.Call(0, 0, uintptr(unsafe.Pointer(&q.h))); r != 0 {
		return nil
	}
	return q
}

func (q *pdhQuery) add(key, path string) {
	var h uintptr
	p, _ := syscall.UTF16PtrFromString(path)
	if r, _, _ := pPdhAddEnglishCounter.Call(q.h, uintptr(unsafe.Pointer(p)), 0, uintptr(unsafe.Pointer(&h))); r == 0 {
		q.counters[key] = h
	}
}

func (q *pdhQuery) collect() { pPdhCollectQueryData.Call(q.h) }

const pdhFmtDouble = 0x00000200
const pdhFmtNoCap100 = 0x00008000
const pdhMoreData = 0x800007D2

// values returns instance -> value for a wildcard counter.
func (q *pdhQuery) values(key string) map[string]float64 {
	h, ok := q.counters[key]
	if !ok {
		return nil
	}
	for attempt := 0; attempt < 3; attempt++ {
		size := uint32(len(q.buf))
		var count uint32
		r, _, _ := pPdhGetFormattedArray.Call(h, pdhFmtDouble|pdhFmtNoCap100, uintptr(unsafe.Pointer(&size)), uintptr(unsafe.Pointer(&count)), uintptr(unsafe.Pointer(&q.buf[0])))
		if uint32(r) == pdhMoreData {
			q.buf = make([]byte, size+4096)
			continue
		}
		if r != 0 {
			return nil
		}
		out := make(map[string]float64, count)
		base := uintptr(unsafe.Pointer(&q.buf[0]))
		for i := uint32(0); i < count; i++ {
			item := base + uintptr(i)*24
			namePtr := *(*uintptr)(unsafe.Pointer(item))
			status := *(*uint32)(unsafe.Pointer(item + 8))
			val := *(*float64)(unsafe.Pointer(item + 16))
			if status != 0 && status != 1 { // PDH_CSTATUS_VALID_DATA / NEW_DATA
				continue
			}
			out[utf16PtrToString(namePtr)] += val
		}
		return out
	}
	return nil
}

func utf16PtrToString(p uintptr) string {
	if p == 0 {
		return ""
	}
	var u []uint16
	for i := uintptr(0); i < 1024; i++ {
		ch := *(*uint16)(unsafe.Pointer(p + i*2))
		if ch == 0 {
			break
		}
		u = append(u, ch)
	}
	return syscall.UTF16ToString(u)
}

// ---------------------------------------------------------------- iphlpapi

type ifRow struct {
	index            uint32
	alias, desc      string
	in, out          uint64
	inPkts, outPkts  uint64
	errs             uint64
	speed            uint64
	oper             uint32
	hardware, filter bool
}

func ifTable() []ifRow {
	var table uintptr
	if r, _, _ := pGetIfTable2.Call(uintptr(unsafe.Pointer(&table))); r != 0 || table == 0 {
		return nil
	}
	defer pFreeMibTable.Call(table)
	n := *(*uint32)(unsafe.Pointer(table))
	out := make([]ifRow, 0, n)
	for i := uint32(0); i < n; i++ {
		out = append(out, parseIfRow(unsafe.Slice((*byte)(unsafe.Pointer(table+8+uintptr(i)*ifRowSize)), ifRowSize)))
	}
	return out
}

const ifRowSize = 1352

// ifEntry refreshes one interface's counters (much cheaper than the full table).
func ifEntry(index uint32, buf []byte) (ifRow, bool) {
	clear(buf)
	binary.LittleEndian.PutUint32(buf[8:], index)
	if r, _, _ := pGetIfEntry2.Call(uintptr(unsafe.Pointer(&buf[0]))); r != 0 {
		return ifRow{}, false
	}
	return parseIfRow(buf), true
}

func parseIfRow(row []byte) ifRow {
	{
		u16 := func(off, n int) string {
			u := make([]uint16, n)
			for j := range u {
				u[j] = binary.LittleEndian.Uint16(row[off+j*2:])
			}
			return syscall.UTF16ToString(u)
		}
		le64 := func(off int) uint64 { return binary.LittleEndian.Uint64(row[off:]) }
		flags := row[1152]
		r := ifRow{
			index:    binary.LittleEndian.Uint32(row[8:]),
			alias:    u16(28, 257),
			desc:     u16(542, 257),
			oper:     binary.LittleEndian.Uint32(row[1156:]),
			speed:    le64(1200),
			in:       le64(1208),
			inPkts:   le64(1216) + le64(1224),
			errs:     le64(1240) + le64(1312),
			out:      le64(1280),
			outPkts:  le64(1288) + le64(1296),
			hardware: flags&1 != 0,
			filter:   flags&2 != 0,
		}
		return r
	}
}

// ---------------------------------------------------------------- structs

type memStatusEx struct {
	Length, MemoryLoad                          uint32
	TotalPhys, AvailPhys                        uint64
	TotalPageFile, AvailPageFile                uint64
	TotalVirtual, AvailVirtual, AvailExtVirtual uint64
}

type perfInfo struct {
	cb                                                                     uint32
	CommitTotal, CommitLimit, CommitPeak, PhysicalTotal, PhysicalAvailable uintptr
	SystemCache, KernelTotal, KernelPaged, KernelNonpaged, PageSize        uintptr
	HandleCount, ProcessCount, ThreadCount                                 uint32
}

func powerInfo() [][2]uint32 {
	n := 256
	buf := make([]byte, 24*n)
	if r, _, _ := pCallNtPowerInformation.Call(11, 0, 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf))); r != 0 {
		return nil
	}
	var out [][2]uint32
	for i := 0; i < n; i++ {
		rec := buf[i*24:]
		maxMHz, cur := binary.LittleEndian.Uint32(rec[4:]), binary.LittleEndian.Uint32(rec[8:])
		if maxMHz == 0 {
			break
		}
		out = append(out, [2]uint32{cur, maxMHz})
	}
	return out
}

// ---------------------------------------------------------------- dynamic

func ft(lo uint32, hi uint32) uint64 { return uint64(hi)<<32 | uint64(lo) }

func (c *Collector) Collect(tick int) *Metrics {
	m := &Metrics{T: time.Now().UnixMilli()}
	if c.q != nil {
		c.q.collect()
	}

	// CPU: aggregate via GetSystemTimes, per-core via NtQuerySystemInformation(8)
	var idle, kern, user syscall.Filetime
	pGetSystemTimes.Call(uintptr(unsafe.Pointer(&idle)), uintptr(unsafe.Pointer(&kern)), uintptr(unsafe.Pointer(&user)))
	it, kt, ut := ft(idle.LowDateTime, idle.HighDateTime), ft(kern.LowDateTime, kern.HighDateTime), ft(user.LowDateTime, user.HighDateTime)
	cur := []cpuTimes{{busy: kt + ut - it, total: kt + ut}}
	ncpu := c.prevCoresLen()
	buf := make([]byte, 48*ncpu)
	var retLen uint32
	if r, _, _ := pNtQuerySystemInformation.Call(8, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), uintptr(unsafe.Pointer(&retLen))); r == 0 {
		for i := 0; i+48 <= int(retLen); i += 48 {
			id := binary.LittleEndian.Uint64(buf[i:])
			k := binary.LittleEndian.Uint64(buf[i+8:])
			u := binary.LittleEndian.Uint64(buf[i+16:])
			cur = append(cur, cpuTimes{busy: k + u - id, total: k + u})
		}
	}
	m.CPU, m.Cores = cpuPct(c.prevCPU, cur)
	c.prevCPU = cur

	// Frequency: base * % Processor Performance (what Task Manager shows)
	if c.q != nil {
		if v := c.q.values("perf"); len(v) > 0 {
			for _, p := range v {
				m.CPUMHz = round1(c.maxMHz * p / 100)
			}
		}
	}

	// Memory
	var ms memStatusEx
	ms.Length = uint32(unsafe.Sizeof(ms))
	pGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&ms)))
	m.Mem.Total = ms.TotalPhys
	m.Mem.Avail = ms.AvailPhys
	m.Mem.Used = ms.TotalPhys - ms.AvailPhys
	if ms.TotalPageFile > ms.TotalPhys {
		m.Mem.SwapTotal = ms.TotalPageFile - ms.TotalPhys
		if c.q != nil {
			for _, p := range c.q.values("pagefile") {
				m.Mem.SwapUsed = uint64(float64(m.Mem.SwapTotal) * p / 100)
			}
		}
	}
	var pi perfInfo
	pi.cb = uint32(unsafe.Sizeof(pi))
	if r, _, _ := pK32GetPerformanceInfo.Call(uintptr(unsafe.Pointer(&pi)), uintptr(pi.cb)); r != 0 {
		m.Mem.Cached = uint64(pi.SystemCache) * uint64(pi.PageSize)
		m.NProcs = int(pi.ProcessCount)
	}
	up, _, _ := pGetTickCount64.Call()
	m.Uptime = uint64(up) / 1000

	// Network: discover relevant interfaces occasionally, then poll just those.
	if c.ifIdx == nil || tick%everyNICs == 0 {
		c.ifIdx = c.ifIdx[:0]
		for _, r := range ifTable() {
			if !r.filter && (r.hardware || strings.HasPrefix(r.alias, "vEthernet")) && (r.oper == 1 || r.in > 0) {
				c.ifIdx = append(c.ifIdx, r.index)
			}
		}
		if c.ifBuf == nil {
			c.ifBuf = make([]byte, ifRowSize)
		}
	}
	c.net.begin()
	for _, idx := range c.ifIdx {
		r, ok := ifEntry(idx, c.ifBuf)
		if !ok {
			continue
		}
		rx, tx, rp, tp := c.net.delta(r.alias, rateKey{r.in, r.out, r.inPkts, r.outPkts})
		if r.oper != 1 && r.in == 0 {
			continue
		}
		m.Net = append(m.Net, NetStat{Name: r.alias, Rx: round1(rx), Tx: round1(tx), RxTotal: r.in, TxTotal: r.out, RxPkts: round1(rp), TxPkts: round1(tp), Errs: r.errs})
	}

	if c.q != nil {
		// Disks
		rd, wr, idl := c.q.values("diskR"), c.q.values("diskW"), c.q.values("diskIdle")
		names := make([]string, 0, len(rd))
		for k := range rd {
			if k != "_Total" {
				names = append(names, k)
			}
		}
		sort.Strings(names)
		for _, k := range names {
			d := DiskIO{Name: k, R: round1(rd[k]), W: round1(wr[k])}
			if v, ok := idl[k]; ok {
				d.Busy = clampPct(100 - v)
			}
			m.Disks = append(m.Disks, d)
		}
		// GPUs (per adapter LUID: max over engine types of summed engine utilization)
		eng := c.q.values("gpuEng")
		mem := c.q.values("gpuMem")
		type acc map[string]float64
		perLuid := map[string]acc{}
		for inst, v := range eng {
			i := strings.Index(inst, "luid_")
			j := strings.Index(inst, "_phys_")
			e := strings.Index(inst, "engtype_")
			if i < 0 || j < 0 || e < 0 {
				continue
			}
			luid := strings.ToLower(inst[i+5 : j])
			if perLuid[luid] == nil {
				perLuid[luid] = acc{}
			}
			perLuid[luid][inst[e+8:]] += v
		}
		nvs := c.nv.snapshot()
		for _, g := range c.gpus {
			if g.vendor == 0x10de && len(nvs) > 0 {
				continue
			}
			st := GPUStat{Name: g.name, MemTotal: g.vram}
			for _, luid := range strings.Split(g.luid, "|") {
				for _, v := range perLuid[luid] {
					if v > st.Util {
						st.Util = v
					}
				}
				for inst, v := range mem {
					if strings.Contains(strings.ToLower(inst), luid) {
						st.MemUsed += uint64(v)
					}
				}
			}
			st.Util = clampPct(st.Util)
			m.GPUs = append(m.GPUs, st)
		}
		m.GPUs = append(m.GPUs, nvs...)
		// ACPI thermal zones (Kelvin)
		for k, v := range c.q.values("thermal") {
			if v > 200 {
				m.Temps = append(m.Temps, Sensor{Name: strings.TrimPrefix(k, `\_TZ.`), Value: round1(v - 273.15)})
			}
		}
		sort.Slice(m.Temps, func(i, j int) bool { return m.Temps[i].Name < m.Temps[j].Name })
	}

	if tick%everyFS == 0 || c.fsCache == nil {
		c.fsCache = volumes()
	}
	m.FS = c.fsCache

	if tick%everyProcs == 0 {
		m.Procs = c.processes()
	}

	if tick%everyTCP == 0 {
		c.tcpCache = tcpStates()
	}
	m.TCP = c.tcpCache

	if tick%everyBattery == 0 {
		var ps [12]byte
		if r, _, _ := pGetSystemPowerStatus.Call(uintptr(unsafe.Pointer(&ps[0]))); r != 0 && ps[1] != 128 && ps[1] != 255 && ps[2] != 255 {
			c.bat = &Battery{Percent: float64(ps[2]), Charging: ps[1]&8 != 0, State: map[byte]string{0: "on battery", 1: "AC power"}[ps[0]]}
		}
	}
	m.Battery = c.bat
	return m
}

func (c *Collector) prevCoresLen() int {
	n := len(c.prevCPU) - 1
	if n < 1 {
		n = 256
	}
	return n
}

func volumes() []FSStat {
	buf := make([]uint16, 512)
	n, _, _ := pGetLogicalDriveStringsW.Call(uintptr(len(buf)), uintptr(unsafe.Pointer(&buf[0])))
	var out []FSStat
	start := 0
	for i := 0; i < int(n); i++ {
		if buf[i] != 0 {
			continue
		}
		root := syscall.UTF16ToString(buf[start:i])
		start = i + 1
		if root == "" {
			continue
		}
		p, _ := syscall.UTF16PtrFromString(root)
		t, _, _ := pGetDriveTypeW.Call(uintptr(unsafe.Pointer(p)))
		if t != 2 && t != 3 && t != 4 { // removable, fixed, remote
			continue
		}
		var free, total, totalFree uint64
		if r, _, _ := pGetDiskFreeSpaceExW.Call(uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(&free)), uintptr(unsafe.Pointer(&total)), uintptr(unsafe.Pointer(&totalFree))); r == 0 || total == 0 {
			continue
		}
		label := make([]uint16, 261)
		fsname := make([]uint16, 261)
		pGetVolumeInformationW.Call(uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(&label[0])), 261, 0, 0, 0, uintptr(unsafe.Pointer(&fsname[0])), 261)
		out = append(out, FSStat{Mount: strings.TrimSuffix(root, `\`), Dev: syscall.UTF16ToString(label), Type: syscall.UTF16ToString(fsname), Total: total, Used: total - totalFree})
	}
	return out
}

// processes reads every process's name, CPU times and working set in ONE
// NtQuerySystemInformation(SystemProcessInformation) call — no per-process handles.
func (c *Collector) processes() []Proc {
	if c.spiBuf == nil {
		c.spiBuf = make([]byte, 1<<20)
	}
	var ret uint32
	for {
		r, _, _ := pNtQuerySystemInformation.Call(5, uintptr(unsafe.Pointer(&c.spiBuf[0])), uintptr(len(c.spiBuf)), uintptr(unsafe.Pointer(&ret)))
		if uint32(r) == 0xC0000004 { // STATUS_INFO_LENGTH_MISMATCH
			c.spiBuf = make([]byte, int(ret)+256<<10)
			continue
		}
		if r != 0 {
			return nil
		}
		break
	}
	b := c.spiBuf
	list := make([]procSample, 0, 512)
	for off := 0; off+152 <= len(b); {
		e := b[off:]
		next := binary.LittleEndian.Uint32(e)
		pid := int(binary.LittleEndian.Uint64(e[80:]))
		if pid != 0 {
			name := ""
			if l := int(binary.LittleEndian.Uint16(e[56:])); l > 0 {
				if p := uintptr(binary.LittleEndian.Uint64(e[64:])); p != 0 {
					name = syscall.UTF16ToString(unsafe.Slice((*uint16)(unsafe.Pointer(p)), l/2))
				}
			}
			if pid == 4 {
				name = "System"
			}
			cpu := (binary.LittleEndian.Uint64(e[40:]) + binary.LittleEndian.Uint64(e[48:])) * 100
			list = append(list, procSample{pid: pid, name: name, cpuNs: cpu, mem: binary.LittleEndian.Uint64(e[144:])})
		}
		if next == 0 {
			break
		}
		off += int(next)
	}
	return c.procs.top(list, false)
}

func tcpStates() map[string]int {
	var size uint32
	pGetTcpTable.Call(0, uintptr(unsafe.Pointer(&size)), 0)
	if size == 0 {
		return nil
	}
	buf := make([]byte, size+1024)
	size = uint32(len(buf))
	if r, _, _ := pGetTcpTable.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)), 0); r != 0 {
		return nil
	}
	names := map[uint32]string{1: "close", 2: "listen", 3: "syn_sent", 4: "syn_recv", 5: "established", 6: "fin_wait1", 7: "fin_wait2", 8: "close_wait", 9: "closing", 10: "last_ack", 11: "time_wait"}
	n := binary.LittleEndian.Uint32(buf)
	out := map[string]int{}
	for i := uint32(0); i < n && int(4+i*20+4) <= len(buf); i++ {
		if s, ok := names[binary.LittleEndian.Uint32(buf[4+i*20:])]; ok {
			out[s]++
		}
	}
	return out
}

var _ = os.Getpid
