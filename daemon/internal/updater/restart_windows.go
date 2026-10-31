//go:build windows

package updater

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// FindDesktopExecutable locates the running or installed Unjess.exe desktop application.
func FindDesktopExecutable() string {
	updaterMu.RLock()
	parent := parentDesktopExe
	updaterMu.RUnlock()

	if parent != "" && fileExists(parent) {
		return parent
	}

	// 1. Check if Unjess is running via PowerShell query
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden", "-Command",
		"(Get-Process -Name Unjess, unjess-desktop -ErrorAction SilentlyContinue | Select-Object -ExpandProperty Path -First 1)")
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	if out, err := cmd.Output(); err == nil {
		p := strings.TrimSpace(string(out))
		if p != "" && fileExists(p) {
			return p
		}
	}

	// 2. Check standard install location: %LOCALAPPDATA%\Programs\Unjess\Unjess.exe
	if localAppData := os.Getenv("LOCALAPPDATA"); localAppData != "" {
		installed := filepath.Join(localAppData, "Programs", "Unjess", "Unjess.exe")
		if fileExists(installed) {
			return installed
		}
	}

	// 3. Check adjacent to current daemon executable
	if daemonExe, err := os.Executable(); err == nil {
		dir := filepath.Dir(daemonExe)
		candidates := []string{
			filepath.Join(dir, "Unjess.exe"),
			filepath.Join(dir, "unjess-desktop.exe"),
			filepath.Join(dir, "..", "Unjess.exe"),
			filepath.Join(dir, "..", "dist", "Unjess.exe"),
		}
		for _, c := range candidates {
			if fileExists(c) {
				return c
			}
		}
	}

	return ""
}

// RestartProcess initiates a clean, detached restart of Unjess on Windows.
func RestartProcess(targetExe string) error {
	if strings.TrimSpace(targetExe) == "" {
		return fmt.Errorf("targetExe is empty")
	}

	cleanTarget := filepath.Clean(targetExe)
	currentPID := os.Getpid()
	workDir := filepath.Dir(cleanTarget)

	// PowerShell helper: sleeps 600ms, stops running Unjess/unjessd instances, and launches targetExe
	psScript := fmt.Sprintf(
		`Start-Sleep -Milliseconds 600; `+
			`Get-Process -Name Unjess, unjess-desktop, unjessd -ErrorAction SilentlyContinue | Where-Object { $_.Id -ne %d } | Stop-Process -Force -ErrorAction SilentlyContinue; `+
			`Start-Process -FilePath '%s' -WorkingDirectory '%s'; `+
			`Stop-Process -Id %d -Force -ErrorAction SilentlyContinue`,
		currentPID, cleanTarget, workDir, currentPID,
	)

	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden", "-Command", psScript)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x08000000, // CREATE_NO_WINDOW
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed starting restart helper: %w", err)
	}

	go func() {
		time.Sleep(800 * time.Millisecond)
		os.Exit(0)
	}()

	return nil
}
