//go:build !windows

package mcp

import "syscall"

// sysProcAttr returns nil on non-Windows platforms.
func sysProcAttr() *syscall.SysProcAttr {
	return nil
}

// killProcessTree is a no-op here; the caller falls back to killing the direct child.
func killProcessTree(pid int) {}
