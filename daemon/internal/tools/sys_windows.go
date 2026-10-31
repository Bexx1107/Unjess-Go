//go:build windows

package tools

import (
	"os/exec"
	"syscall"
)

// setHideWindow sets HideWindow=true on Windows processes to prevent console popups.
func setHideWindow(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
}
