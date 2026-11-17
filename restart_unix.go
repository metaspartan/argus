//go:build !windows

package main

import (
	"os"
	"syscall"
)

// restartSelf re-execs the new binary in place (same PID, so systemd,
// launchd and OpenRC supervision is undisturbed).
func restartSelf(path string) {
	syscall.Exec(path, os.Args, os.Environ())
	os.Exit(0)
}
