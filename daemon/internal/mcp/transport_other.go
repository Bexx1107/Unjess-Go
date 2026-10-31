//go:build !windows

package mcp

import "syscall"

// sysProcAttr returns nil on non-Windows platforms.
func sysProcAttr() *syscall.SysProcAttr {
	return nil
}
