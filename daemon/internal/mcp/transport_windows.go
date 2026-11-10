//go:build windows

package mcp

import (
	"os/exec"
	"strconv"
	"syscall"
)

// sysProcAttr returns OS-specific process attributes to hide console window on Windows.
func sysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{HideWindow: true}
}

// killProcessTree terminates pid and all of its descendants.
func killProcessTree(pid int) {
	cmd := exec.Command("taskkill", "/F", "/T", "/PID", strconv.Itoa(pid))
	cmd.SysProcAttr = sysProcAttr()
	_ = cmd.Run()
}
