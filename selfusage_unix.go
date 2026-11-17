//go:build !windows

package main

import (
	"os"
	"runtime"
	"runtime/metrics"
	"strconv"
	"strings"
	"syscall"
)

// processUsage returns cumulative CPU time (self + reaped children such as
// ioreg/nvidia-smi helpers, in ns) and resident memory in bytes.
func processUsage() (uint64, uint64) {
	var cpu uint64
	for _, who := range []int{syscall.RUSAGE_SELF, syscall.RUSAGE_CHILDREN} {
		var ru syscall.Rusage
		if syscall.Getrusage(who, &ru) == nil {
			cpu += uint64(ru.Utime.Nano() + ru.Stime.Nano())
		}
	}
	if runtime.GOOS == "linux" {
		if b, err := os.ReadFile("/proc/self/statm"); err == nil {
			if f := strings.Fields(string(b)); len(f) > 1 {
				if pages, err := strconv.ParseUint(f[1], 10, 64); err == nil {
					return cpu, pages * uint64(os.Getpagesize())
				}
			}
		}
	}
	// Elsewhere: Go-mapped memory is a close upper bound of RSS.
	s := []metrics.Sample{{Name: "/memory/classes/total:bytes"}, {Name: "/memory/classes/heap/released:bytes"}}
	metrics.Read(s)
	return cpu, s[0].Value.Uint64() - s[1].Value.Uint64()
}
