//go:build windows

package main

import (
	"os"
	"os/exec"
)

func restartSelf(path string) {
	if path == binPath() {
		// Installed as a scheduled task: ask the scheduler to start the new binary.
		c := exec.Command("cmd", "/c", "ping -n 3 127.0.0.1 >nul & schtasks /Run /TN \"Argus Agent\"")
		hideWindow(c)
		c.Start()
	} else {
		exec.Command(path, os.Args[1:]...).Start()
	}
	os.Exit(0)
}
