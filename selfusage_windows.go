//go:build windows

package main

import (
	"encoding/binary"
	"syscall"
	"unsafe"
)

func processUsage() (uint64, uint64) {
	h, _ := syscall.GetCurrentProcess()
	var cr, ex, k, u syscall.Filetime
	var cpu uint64
	if syscall.GetProcessTimes(h, &cr, &ex, &k, &u) == nil {
		cpu = (ft(k.LowDateTime, k.HighDateTime) + ft(u.LowDateTime, u.HighDateTime)) * 100
	}
	var pmc [72]byte
	binary.LittleEndian.PutUint32(pmc[:], 72)
	var rss uint64
	if r, _, _ := pK32GetProcessMemoryInfo.Call(uintptr(h), uintptr(unsafe.Pointer(&pmc[0])), 72); r != 0 {
		rss = binary.LittleEndian.Uint64(pmc[16:])
	}
	return cpu, rss
}
