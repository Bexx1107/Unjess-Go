//go:build !windows

package updater

import (
	"os"
	"os/exec"
)

// FindDesktopExecutable returns an empty string on non-Windows platforms.
func FindDesktopExecutable() string {
	updaterMu.RLock()
	parent := parentDesktopExe
	updaterMu.RUnlock()
	if parent != "" {
		return parent
	}
	if p, err := exec.LookPath("unjess"); err == nil {
		return p
	}
	return ""
}

// RestartProcess restarts the executable on non-Windows platforms.
func RestartProcess(targetExe string) error {
	cmd := exec.Command(targetExe, os.Args[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	os.Exit(0)
	return nil
}
