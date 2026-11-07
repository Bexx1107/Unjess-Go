//go:build windows

package main

import (
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"unjess-daemon/internal/version"
)

//go:embed Unjess.exe
var embeddedUnjessExe []byte

//go:embed unjessd.exe
var embeddedDaemonExe []byte

//go:embed unjess-cli.exe
var embeddedCliExe []byte

//go:embed Unjess-Uninstall.exe
var embeddedUninstallExe []byte

//go:embed unjess_icon.ico
var embeddedIcon []byte

var (
	user32          = syscall.NewLazyDLL("user32.dll")
	procMessageBoxW = user32.NewProc("MessageBoxW")
)

const (
	MB_OK              = 0x00000000
	MB_OKCANCEL        = 0x00000001
	MB_ICONINFORMATION = 0x00000040
	MB_ICONERROR       = 0x00000010
	IDOK               = 1
	CREATE_NO_WINDOW   = 0x08000000
)

func messageBox(caption, text string, uType uint32) int {
	captionPtr, _ := syscall.UTF16PtrFromString(caption)
	textPtr, _ := syscall.UTF16PtrFromString(text)
	ret, _, _ := procMessageBoxW.Call(0, uintptr(unsafe.Pointer(textPtr)), uintptr(unsafe.Pointer(captionPtr)), uintptr(uType))
	return int(ret)
}

func runHidden(cmdName string, args ...string) error {
	cmd := exec.Command(cmdName, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: CREATE_NO_WINDOW,
	}
	return cmd.Run()
}

func regSetString(key, valName, valData string) {
	_ = runHidden("reg.exe", "add", key, "/v", valName, "/d", valData, "/f")
}

func regSetDword(key, valName string, valData int) {
	_ = runHidden("reg.exe", "add", key, "/v", valName, "/t", "REG_DWORD", "/d", fmt.Sprintf("%d", valData), "/f")
}

func createShortcut(shortcutPath, targetPath, workingDir, iconPath, description string) {
	psScript := fmt.Sprintf(
		`$ws = New-Object -ComObject WScript.Shell; $s = $ws.CreateShortcut('%s'); $s.TargetPath = '%s'; $s.WorkingDirectory = '%s'; $s.IconLocation = '%s,0'; $s.Description = '%s'; $s.Save()`,
		strings.ReplaceAll(shortcutPath, "'", "''"),
		strings.ReplaceAll(targetPath, "'", "''"),
		strings.ReplaceAll(workingDir, "'", "''"),
		strings.ReplaceAll(iconPath, "'", "''"),
		strings.ReplaceAll(description, "'", "''"),
	)
	_ = runHidden("powershell.exe", "-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden", "-Command", psScript)
}

func main() {
	silent := false
	for _, arg := range os.Args[1:] {
		if arg == "--silent" || arg == "-s" || arg == "/S" {
			silent = true
			break
		}
	}

	localAppData := os.Getenv("LOCALAPPDATA")
	if localAppData == "" {
		if up := os.Getenv("USERPROFILE"); up != "" {
			localAppData = filepath.Join(up, "AppData", "Local")
		}
	}

	installDir := filepath.Join(localAppData, "Programs", "Unjess")

	if !silent {
		welcomeMsg := fmt.Sprintf(
			"Welcome to Unjess v%s Setup (unjess.si)\n\nClick OK to install Unjess — Autonomous AI Coding Harness:\n\n• Install Location: %s\n• Creates Desktop & Start Menu Shortcuts\n• Registers Uninstaller in Windows Installed Apps",
			version.Current,
			installDir,
		)
		result := messageBox("Unjess Setup", welcomeMsg, MB_OKCANCEL|MB_ICONINFORMATION)
		if result != IDOK {
			os.Exit(0)
		}
	}

	// 1. Stop any currently running instances
	_ = runHidden("powershell.exe", "-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden", "-Command", "Stop-Process -Name Unjess, unjess-desktop, unjessd -Force -ErrorAction SilentlyContinue")

	// 2. Ensure install directory exists
	if err := os.MkdirAll(installDir, 0755); err != nil {
		if !silent {
			messageBox("Unjess Setup Error", fmt.Sprintf("Failed to create directory %s: %v", installDir, err), MB_OK|MB_ICONERROR)
		}
		os.Exit(1)
	}

	targetExe := filepath.Join(installDir, "Unjess.exe")
	targetDaemon := filepath.Join(installDir, "unjessd.exe")
	targetCli := filepath.Join(installDir, "unjess-cli.exe")
	targetCmd := filepath.Join(installDir, "njss.cmd")
	targetUninstall := filepath.Join(installDir, "Unjess-Uninstall.exe")
	targetIcon := filepath.Join(installDir, "unjess_icon.ico")

	// 3. Write embedded files (both GUI and daemon are placed side-by-side, eliminating runtime dropper pattern)
	if err := os.WriteFile(targetExe, embeddedUnjessExe, 0755); err != nil {
		if !silent {
			messageBox("Unjess Setup Error", fmt.Sprintf("Failed to write %s: %v", targetExe, err), MB_OK|MB_ICONERROR)
		}
		os.Exit(1)
	}

	if len(embeddedDaemonExe) > 0 {
		if err := os.WriteFile(targetDaemon, embeddedDaemonExe, 0755); err != nil {
			if !silent {
				messageBox("Unjess Setup Error", fmt.Sprintf("Failed to write %s: %v", targetDaemon, err), MB_OK|MB_ICONERROR)
			}
			os.Exit(1)
		}
	}

	if len(embeddedCliExe) > 0 {
		_ = os.WriteFile(targetCli, embeddedCliExe, 0755)
		_ = os.WriteFile(targetCmd, []byte("@echo off\r\n\"%~dp0unjess-cli.exe\" %*\r\n"), 0755)
	}

	if err := os.WriteFile(targetUninstall, embeddedUninstallExe, 0755); err != nil {
		if !silent {
			messageBox("Unjess Setup Error", fmt.Sprintf("Failed to write %s: %v", targetUninstall, err), MB_OK|MB_ICONERROR)
		}
		os.Exit(1)
	}

	_ = os.WriteFile(targetIcon, embeddedIcon, 0644)

	// 4. Create Desktop & Start Menu shortcuts
	if userProfile := os.Getenv("USERPROFILE"); userProfile != "" {
		desktopShortcut := filepath.Join(userProfile, "Desktop", "Unjess.lnk")
		createShortcut(desktopShortcut, targetExe, installDir, targetIcon, "Unjess - Autonomous AI Coding Harness")
	}

	if appData := os.Getenv("APPDATA"); appData != "" {
		startMenuDir := filepath.Join(appData, "Microsoft", "Windows", "Start Menu", "Programs")
		_ = os.MkdirAll(startMenuDir, 0755)
		startMenuShortcut := filepath.Join(startMenuDir, "Unjess.lnk")
		createShortcut(startMenuShortcut, targetExe, installDir, targetIcon, "Unjess - Autonomous AI Coding Harness")
	}

	// 5. Add installDir to User PATH environment variable
	addPathPs := fmt.Sprintf(
		`$p = [Environment]::GetEnvironmentVariable('Path', 'User'); if ($p -split ';' -notcontains '%s') { [Environment]::SetEnvironmentVariable('Path', ($p.TrimEnd(';') + ';%s'), 'User') }`,
		strings.ReplaceAll(installDir, "'", "''"),
		strings.ReplaceAll(installDir, "'", "''"),
	)
	_ = runHidden("powershell.exe", "-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden", "-Command", addPathPs)

	// 6. Register in Windows Uninstall registry
	regKey := `HKCU\Software\Microsoft\Windows\CurrentVersion\Uninstall\Unjess`
	regSetString(regKey, "DisplayName", "Unjess")
	regSetString(regKey, "DisplayVersion", version.Current)
	regSetString(regKey, "Publisher", "unjess.si")
	regSetString(regKey, "URLInfoAbout", "https://unjess.si")
	regSetString(regKey, "DisplayIcon", targetIcon)
	regSetString(regKey, "InstallLocation", installDir)
	regSetString(regKey, "UninstallString", `"`+targetUninstall+`"`)
	regSetString(regKey, "QuietUninstallString", `"`+targetUninstall+`" --silent`)
	regSetDword(regKey, "NoModify", 1)
	regSetDword(regKey, "NoRepair", 1)
	regSetDword(regKey, "EstimatedSize", 32500)

	// 7. Complete and launch
	if !silent {
		messageBox(
			"Unjess Setup",
			fmt.Sprintf("Unjess v%s has been installed successfully!\n\nClick OK to launch Unjess.", version.Current),
			MB_OK|MB_ICONINFORMATION,
		)
	}

	// Launch Unjess.exe detached
	launchCmd := exec.Command(targetExe)
	launchCmd.Dir = installDir
	launchCmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: 0x00000008, // DETACHED_PROCESS
	}
	_ = launchCmd.Start()

	os.Exit(0)
}
