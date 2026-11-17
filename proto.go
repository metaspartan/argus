package main

// Wire protocol shared by agent and hub. JSON over HTTP keep-alive.
// Static inventory is sent once (or when the hub asks); metrics every tick.

type Static struct {
	ID          string            `json:"id"`
	Hostname    string            `json:"hostname"`
	OS          string            `json:"os"`       // linux | darwin | windows
	Arch        string            `json:"arch"`     // amd64 | arm64 | riscv64 ...
	Platform    string            `json:"platform"` // "macOS 15.1", "Ubuntu 24.04 LTS"
	Kernel      string            `json:"kernel"`
	Model       string            `json:"model,omitempty"` // hardware model
	Vendor      string            `json:"vendor,omitempty"`
	Firmware    string            `json:"firmware,omitempty"`
	CPUModel    string            `json:"cpuModel"`
	CPUCores    int               `json:"cpuCores"`   // physical
	CPUThreads  int               `json:"cpuThreads"` // logical
	PCores      int               `json:"pCores,omitempty"`
	ECores      int               `json:"eCores,omitempty"`
	CPUMaxMHz   float64           `json:"cpuMaxMHz,omitempty"`
	CPUFlags    string            `json:"cpuFlags,omitempty"` // ISA / notable features
	MemTotal    uint64            `json:"memTotal"`
	BootTime    int64             `json:"bootTime"`
	GPUs        []GPUInfo         `json:"gpus"`
	NICs        []NICInfo         `json:"nics"`
	Storage     []StorageDev      `json:"storage,omitempty"`
	Devices     []Device          `json:"devices,omitempty"`
	Drivers     []Driver          `json:"drivers,omitempty"`
	Extra       map[string]string `json:"extra,omitempty"`
	Agent       string            `json:"agent"`
	Manage      bool              `json:"manage"` // agent accepts exec/power/kill
	Virtual     string            `json:"virt,omitempty"`
	Timezone    string            `json:"tz,omitempty"`
	CollectedAt int64             `json:"collectedAt"`
}

type GPUInfo struct {
	Name          string `json:"name"`
	Vendor        string `json:"vendor,omitempty"`
	Driver        string `json:"driver,omitempty"`
	DriverVersion string `json:"driverVersion,omitempty"`
	VRAM          uint64 `json:"vram,omitempty"`
	Cores         int    `json:"cores,omitempty"`
	Bus           string `json:"bus,omitempty"`
	API           string `json:"api,omitempty"` // Metal 3 / CUDA 12.4 / ...
	Extra         string `json:"extra,omitempty"`
}

type NICInfo struct {
	Name   string   `json:"name"`
	MAC    string   `json:"mac,omitempty"`
	Addrs  []string `json:"addrs,omitempty"`
	MTU    int      `json:"mtu,omitempty"`
	Speed  int64    `json:"speed,omitempty"` // Mbit/s
	Driver string   `json:"driver,omitempty"`
	Up     bool     `json:"up"`
	Virt   bool     `json:"virt,omitempty"`
}

type StorageDev struct {
	Name  string `json:"name"`
	Model string `json:"model,omitempty"`
	Size  uint64 `json:"size"`
	Kind  string `json:"kind,omitempty"` // nvme / ssd / hdd / mmc
}

type Device struct {
	Bus    string `json:"bus"` // pci / usb / platform
	Class  string `json:"class,omitempty"`
	Name   string `json:"name"`
	Vendor string `json:"vendor,omitempty"`
	ID     string `json:"id,omitempty"`
	Driver string `json:"driver,omitempty"`
}

type Driver struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
	Info    string `json:"info,omitempty"`
}

type Metrics struct {
	T        int64          `json:"t"` // unix ms
	Uptime   uint64         `json:"uptime"`
	Load     []float64      `json:"load,omitempty"`
	CPU      float64        `json:"cpu"`
	Cores    []float64      `json:"cores"`
	CPUMHz   float64        `json:"mhz,omitempty"`
	CPUWatts float64        `json:"cpuW,omitempty"`
	SysWatts float64        `json:"sysW,omitempty"`
	Mem      MemStat        `json:"mem"`
	GPUs     []GPUStat      `json:"gpus,omitempty"`
	Net      []NetStat      `json:"net"`
	Disks    []DiskIO       `json:"disks,omitempty"`
	FS       []FSStat       `json:"fs"`
	Temps    []Sensor       `json:"temps,omitempty"`
	Fans     []Sensor       `json:"fans,omitempty"`
	Procs    []Proc         `json:"procs,omitempty"`
	NProcs   int            `json:"nprocs,omitempty"`
	TCP      map[string]int `json:"tcp,omitempty"`
	Battery  *Battery       `json:"battery,omitempty"`
	Thermal  string         `json:"thermal,omitempty"` // macOS thermal pressure
	Docker   *DockerState   `json:"docker,omitempty"`
	Self     *SelfStat      `json:"self,omitempty"` // the agent's own footprint
}

type SelfStat struct {
	CPU float64 `json:"cpu"` // % of one core
	RSS uint64  `json:"rss"`
}

type DockerState struct {
	Engine     string      `json:"engine"` // docker | podman
	Version    string      `json:"version"`
	Images     int         `json:"images"`
	Driver     string      `json:"driver,omitempty"`
	Containers []Container `json:"containers"`
	Error      string      `json:"error,omitempty"`
}

type Container struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Image    string   `json:"image"`
	State    string   `json:"state"`  // running | exited | paused | restarting | created | dead
	Status   string   `json:"status"` // "Up 3 hours (healthy)"
	Health   string   `json:"health,omitempty"`
	Created  int64    `json:"created"`
	Ports    []string `json:"ports,omitempty"`
	Project  string   `json:"project,omitempty"` // compose project
	Service  string   `json:"service,omitempty"`
	CPU      float64  `json:"cpu"` // % of one core
	Mem      uint64   `json:"mem"`
	MemLimit uint64   `json:"memLimit,omitempty"`
	Rx       float64  `json:"rx"`
	Tx       float64  `json:"tx"`
	PIDs     int      `json:"pids,omitempty"`
}

type MemStat struct {
	Total      uint64  `json:"total"`
	Used       uint64  `json:"used"`
	Avail      uint64  `json:"avail"`
	Cached     uint64  `json:"cached,omitempty"`
	Wired      uint64  `json:"wired,omitempty"`
	Compressed uint64  `json:"compressed,omitempty"`
	SwapTotal  uint64  `json:"swapTotal"`
	SwapUsed   uint64  `json:"swapUsed"`
	Pressure   float64 `json:"pressure,omitempty"`
}

type GPUStat struct {
	Name     string  `json:"name"`
	Util     float64 `json:"util"` // %
	MemUsed  uint64  `json:"memUsed,omitempty"`
	MemTotal uint64  `json:"memTotal,omitempty"`
	Temp     float64 `json:"temp,omitempty"`
	Watts    float64 `json:"watts,omitempty"`
	MHz      float64 `json:"mhz,omitempty"`
	Fan      float64 `json:"fan,omitempty"`
}

type NetStat struct {
	Name    string  `json:"name"`
	Rx      float64 `json:"rx"` // bytes/s
	Tx      float64 `json:"tx"`
	RxTotal uint64  `json:"rxT"`
	TxTotal uint64  `json:"txT"`
	RxPkts  float64 `json:"rxP,omitempty"`
	TxPkts  float64 `json:"txP,omitempty"`
	Errs    uint64  `json:"errs,omitempty"`
}

type DiskIO struct {
	Name string  `json:"name"`
	R    float64 `json:"r"` // bytes/s
	W    float64 `json:"w"`
	Busy float64 `json:"busy,omitempty"` // %
}

type FSStat struct {
	Mount string `json:"mount"`
	Dev   string `json:"dev,omitempty"`
	Type  string `json:"type,omitempty"`
	Total uint64 `json:"total"`
	Used  uint64 `json:"used"`
}

type Sensor struct {
	Name  string  `json:"name"`
	Value float64 `json:"v"`
}

type Proc struct {
	PID  int     `json:"pid"`
	Name string  `json:"name"`
	CPU  float64 `json:"cpu"`
	Mem  uint64  `json:"mem"`
	User string  `json:"user,omitempty"`
}

type Battery struct {
	Percent  float64 `json:"pct"`
	Charging bool    `json:"charging"`
	State    string  `json:"state,omitempty"`
}

// Commands flow hub -> agent (piggybacked on report responses).
type Command struct {
	ID   string `json:"id"`
	Kind string `json:"kind"` // exec | kill | reboot | shutdown | inventory
	Arg  string `json:"arg,omitempty"`
}

type Result struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Arg    string `json:"arg,omitempty"`
	OK     bool   `json:"ok"`
	Output string `json:"output"`
	Code   int    `json:"code"`
	Ms     int64  `json:"ms"`
	At     int64  `json:"at"`
}

type Report struct {
	ID      string   `json:"id"`
	Static  *Static  `json:"static,omitempty"`
	M       *Metrics `json:"m"`
	Results []Result `json:"results,omitempty"`
}

type ReportResp struct {
	Cmds       []Command `json:"cmds,omitempty"`
	NeedStatic bool      `json:"needStatic,omitempty"`
	Interval   int       `json:"interval,omitempty"` // ms
}
