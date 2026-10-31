//go:build !windows

package worktree

import "os/exec"

// setHideWindow is a no-op on non-Windows platforms.
func setHideWindow(cmd *exec.Cmd) {}
