//go:build windows

package codeintel

import (
	"os/exec"
	"strconv"
	"syscall"
	"time"
)

// setHideWindow sets HideWindow=true on Windows processes to prevent console popups and enables process-tree kill.
func setHideWindow(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true

	cmd.WaitDelay = 1500 * time.Millisecond
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		_ = exec.Command("taskkill", "/F", "/T", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
		return cmd.Process.Kill()
	}
}
