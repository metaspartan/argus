//go:build !windows

package main

import (
	"os"
	"os/exec"
)

func hideWindow(*exec.Cmd) {}

func isAdmin() bool { return os.Geteuid() == 0 }
