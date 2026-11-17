//go:build !linux && !darwin && !windows

package main

import (
	"time"
)

// Minimal fallback for other platforms (FreeBSD, etc.): identity + interfaces.

type Collector struct{ procs procTracker }

func newCollector() *Collector { return &Collector{} }

func (c *Collector) Static() *Static {
	s := baseStatic()
	s.ID = "host-" + s.Hostname
	s.Platform = s.OS
	s.CPUCores = s.CPUThreads
	s.NICs = sortedNICs(nicAddrs())
	return s
}

func (c *Collector) Collect(tick int) *Metrics {
	return &Metrics{T: time.Now().UnixMilli()}
}
