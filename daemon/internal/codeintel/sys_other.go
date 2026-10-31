//go:build !windows

package codeintel

import (
	"os/exec"
	"time"
)

// setHideWindow configures process termination and wait delay on non-Windows platforms.
func setHideWindow(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	cmd.WaitDelay = 1500 * time.Millisecond
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return cmd.Process.Kill()
	}
}
