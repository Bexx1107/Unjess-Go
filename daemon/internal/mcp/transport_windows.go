//go:build windows

package mcp

import "syscall"

// sysProcAttr returns OS-specific process attributes to hide console window on Windows.
func sysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{HideWindow: true}
}
