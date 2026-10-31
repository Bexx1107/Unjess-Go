//go:build windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"unsafe"
)

var (
	user32          = syscall.NewLazyDLL("user32.dll")
	procMessageBoxW = user32.NewProc("MessageBoxW")
)

const (
	MB_OK              = 0x00000000
	MB_YESNO           = 0x00000004
	MB_ICONINFORMATION = 0x00000040
	MB_ICONQUESTION    = 0x00000020
	IDYES              = 6
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

func main() {
	silent := false
	for _, arg := range os.Args[1:] {
		if arg == "--silent" || arg == "-s" || arg == "/S" {
			silent = true
			break
		}
	}

	if !silent {
		result := messageBox(
			"Unjess Uninstall",
			"Are you sure you want to completely uninstall Unjess from your computer?\n\n(Your chat history and API keys in ~/.unjess will be preserved.)",
			MB_YESNO|MB_ICONQUESTION,
		)
		if result != IDYES {
			os.Exit(0)
		}
	}

	// 1. Stop running Unjess.exe, unjess-desktop.exe, unjessd.exe via hidden PowerShell
	_ = runHidden("powershell.exe", "-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden", "-Command", "Stop-Process -Name Unjess, unjess-desktop, unjessd -Force -ErrorAction SilentlyContinue")

	// 2. Delete Desktop shortcut and Start Menu shortcut
	if userProfile := os.Getenv("USERPROFILE"); userProfile != "" {
		desktopShortcut := filepath.Join(userProfile, "Desktop", "Unjess.lnk")
		_ = os.Remove(desktopShortcut)
	}

	if appData := os.Getenv("APPDATA"); appData != "" {
		startMenuShortcut := filepath.Join(appData, "Microsoft", "Windows", "Start Menu", "Programs", "Unjess.lnk")
		_ = os.Remove(startMenuShortcut)
	}

	// 3. Delete Registry key HKCU\Software\Microsoft\Windows\CurrentVersion\Uninstall\Unjess
	_ = runHidden("reg.exe", "delete", `HKCU\Software\Microsoft\Windows\CurrentVersion\Uninstall\Unjess`, "/f")

	// 4. Show success dialog
	if !silent {
		messageBox(
			"Unjess Uninstall",
			"Unjess has been cleanly uninstalled from your computer.",
			MB_OK|MB_ICONINFORMATION,
		)
	}

	// 5. Spawns a detached hidden cmd.exe to delete install folders after process exits
	cmdLine := `cmd.exe /C "ping 127.0.0.1 -n 3 > nul & rmdir /s /q \"%LOCALAPPDATA%\Programs\Unjess\" & rmdir /s /q \"%LOCALAPPDATA%\Unjess\""`
	detachCmd := exec.Command("cmd.exe")
	detachCmd.SysProcAttr = &syscall.SysProcAttr{
		CmdLine:       cmdLine,
		HideWindow:    true,
		CreationFlags: CREATE_NO_WINDOW | 0x00000008, // DETACHED_PROCESS
	}
	_ = detachCmd.Start()

	os.Exit(0)
}
