//go:build windows

package telemetry

import (
	"syscall"
	"unsafe"
)

var (
	user32          = syscall.NewLazyDLL("user32.dll")
	procMessageBoxW = user32.NewProc("MessageBoxW")
)

const (
	mbOK       = 0x00000000
	mbIconStop = 0x00000010
)

// showKillswitchDialog displays a native Windows alert dialog explaining why Unjess was disabled.
func showKillswitchDialog(reason string) {
	captionPtr, _ := syscall.UTF16PtrFromString("Unjess Notice")
	text := "Unjess has been disabled by the server.\n\nReason: " + reason
	textPtr, _ := syscall.UTF16PtrFromString(text)
	_, _, _ = procMessageBoxW.Call(
		0,
		uintptr(unsafe.Pointer(textPtr)),
		uintptr(unsafe.Pointer(captionPtr)),
		uintptr(mbOK|mbIconStop),
	)
}
