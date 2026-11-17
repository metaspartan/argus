//go:build darwin

package main

// Direct libSystem calls without cgo, using the same trampoline technique as
// golang.org/x/sys/unix: the Go runtime's syscall.syscall* helpers switch to
// the system stack and call a C ABI function pointer.

import (
	"syscall"
	"unsafe"
)

//go:linkname syscall_syscall syscall.syscall
func syscall_syscall(fn, a1, a2, a3 uintptr) (r1, r2 uintptr, err syscall.Errno)

//go:linkname syscall_syscall6 syscall.syscall6
func syscall_syscall6(fn, a1, a2, a3, a4, a5, a6 uintptr) (r1, r2 uintptr, err syscall.Errno)

var libc_sysctlbyname_trampoline_addr uintptr

//go:cgo_import_dynamic libc_sysctlbyname sysctlbyname "/usr/lib/libSystem.B.dylib"

var libc_mach_host_self_trampoline_addr uintptr

//go:cgo_import_dynamic libc_mach_host_self mach_host_self "/usr/lib/libSystem.B.dylib"

var libc_host_processor_info_trampoline_addr uintptr

//go:cgo_import_dynamic libc_host_processor_info host_processor_info "/usr/lib/libSystem.B.dylib"

var libc_host_statistics64_trampoline_addr uintptr

//go:cgo_import_dynamic libc_host_statistics64 host_statistics64 "/usr/lib/libSystem.B.dylib"

var libc_vm_deallocate_trampoline_addr uintptr

//go:cgo_import_dynamic libc_vm_deallocate vm_deallocate "/usr/lib/libSystem.B.dylib"

var libc_task_self_trap_trampoline_addr uintptr

//go:cgo_import_dynamic libc_task_self_trap task_self_trap "/usr/lib/libSystem.B.dylib"

var libc_proc_pidinfo_trampoline_addr uintptr

//go:cgo_import_dynamic libc_proc_pidinfo proc_pidinfo "/usr/lib/libSystem.B.dylib"

var libc_proc_listallpids_trampoline_addr uintptr

//go:cgo_import_dynamic libc_proc_listallpids proc_listallpids "/usr/lib/libSystem.B.dylib"

var libc_mach_timebase_info_trampoline_addr uintptr

//go:cgo_import_dynamic libc_mach_timebase_info mach_timebase_info "/usr/lib/libSystem.B.dylib"

// sysctlRaw returns the raw bytes of a sysctl by name.
func sysctlRaw(name string) []byte {
	n, err := syscall.BytePtrFromString(name)
	if err != nil {
		return nil
	}
	var size uintptr
	if r, _, _ := syscall_syscall6(libc_sysctlbyname_trampoline_addr, uintptr(unsafe.Pointer(n)), 0, uintptr(unsafe.Pointer(&size)), 0, 0, 0); r != 0 || size == 0 {
		return nil
	}
	buf := make([]byte, size)
	if r, _, _ := syscall_syscall6(libc_sysctlbyname_trampoline_addr, uintptr(unsafe.Pointer(n)), uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)), 0, 0, 0); r != 0 {
		return nil
	}
	return buf[:size]
}

func sysctlStr(name string) string {
	b := sysctlRaw(name)
	for len(b) > 0 && b[len(b)-1] == 0 {
		b = b[:len(b)-1]
	}
	return string(b)
}

func sysctlInt(name string) uint64 {
	b := sysctlRaw(name)
	switch len(b) {
	case 4:
		return uint64(*(*uint32)(unsafe.Pointer(&b[0])))
	case 8:
		return *(*uint64)(unsafe.Pointer(&b[0]))
	}
	return 0
}

var (
	machHost, machTask uintptr
	tbNumer, tbDenom   uint64 = 1, 1
)

func machInit() {
	if machHost == 0 {
		machHost, _, _ = syscall_syscall(libc_mach_host_self_trampoline_addr, 0, 0, 0)
		machTask, _, _ = syscall_syscall(libc_task_self_trap_trampoline_addr, 0, 0, 0)
		var tb [2]uint32
		if r, _, _ := syscall_syscall(libc_mach_timebase_info_trampoline_addr, uintptr(unsafe.Pointer(&tb[0])), 0, 0); r == 0 && tb[0] > 0 && tb[1] > 0 {
			tbNumer, tbDenom = uint64(tb[0]), uint64(tb[1])
		}
	}
}

// cpuTicks returns [user, system, idle, nice] ticks per CPU.
func cpuTicks() [][4]uint32 {
	machInit()
	var count uint32
	var info uintptr
	var infoCnt uint32
	r, _, _ := syscall_syscall6(libc_host_processor_info_trampoline_addr, machHost, 2, /* PROCESSOR_CPU_LOAD_INFO */
		uintptr(unsafe.Pointer(&count)), uintptr(unsafe.Pointer(&info)), uintptr(unsafe.Pointer(&infoCnt)), 0)
	if r != 0 || info == 0 {
		return nil
	}
	out := make([][4]uint32, count)
	src := unsafe.Slice((*uint32)(unsafe.Pointer(info)), int(count)*4)
	for i := range out {
		copy(out[i][:], src[i*4:i*4+4])
	}
	syscall_syscall(libc_vm_deallocate_trampoline_addr, machTask, info, uintptr(infoCnt)*4)
	return out
}

// vmStats returns the raw vm_statistics64 structure (38 natural_t).
func vmStats() []byte {
	machInit()
	buf := make([]byte, 38*4)
	cnt := uint32(38)
	r, _, _ := syscall_syscall6(libc_host_statistics64_trampoline_addr, machHost, 4, /* HOST_VM_INFO64 */
		uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&cnt)), 0, 0)
	if r != 0 {
		return nil
	}
	return buf
}

func listPids() []int32 {
	n, _, _ := syscall_syscall(libc_proc_listallpids_trampoline_addr, 0, 0, 0)
	if int32(n) <= 0 {
		return nil
	}
	buf := make([]int32, int(int32(n))+64)
	n, _, _ = syscall_syscall(libc_proc_listallpids_trampoline_addr, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)*4), 0)
	if int32(n) <= 0 {
		return nil
	}
	return buf[:int(int32(n))]
}

// procTaskInfo returns (cpu ns, resident bytes) via PROC_PIDTASKINFO.
// CPU times are in Mach absolute units (41.67ns ticks on Apple Silicon).
func procTaskInfo(pid int32) (uint64, uint64, bool) {
	// struct proc_taskinfo: virtual_size u64, resident_size u64, total_user u64, total_system u64, ...
	var buf [96]byte
	n, _, _ := syscall_syscall6(libc_proc_pidinfo_trampoline_addr, uintptr(pid), 4 /* PROC_PIDTASKINFO */, 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), 0)
	if int(int32(n)) < 32 {
		return 0, 0, false
	}
	rss := *(*uint64)(unsafe.Pointer(&buf[8]))
	user := *(*uint64)(unsafe.Pointer(&buf[16]))
	sys := *(*uint64)(unsafe.Pointer(&buf[24]))
	return (user + sys) * tbNumer / tbDenom, rss, true
}

// procBSD returns (uid, name) via PROC_PIDTBSDINFO.
func procBSD(pid int32) (uint32, string, bool) {
	var buf [136]byte
	n, _, _ := syscall_syscall6(libc_proc_pidinfo_trampoline_addr, uintptr(pid), 3 /* PROC_PIDTBSDINFO */, 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), 0)
	if int(int32(n)) < 136 {
		return 0, "", false
	}
	cstr := func(b []byte) string {
		for i, c := range b {
			if c == 0 {
				return string(b[:i])
			}
		}
		return string(b)
	}
	name := cstr(buf[64:96])
	if name == "" {
		name = cstr(buf[48:64])
	}
	return *(*uint32)(unsafe.Pointer(&buf[20])), name, true
}
