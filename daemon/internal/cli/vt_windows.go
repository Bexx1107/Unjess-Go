//go:build windows

package cli

import (
	"syscall"
	"unsafe"
)

var (
	kernel32               = syscall.NewLazyDLL("kernel32.dll")
	procGetStdHandle       = kernel32.NewProc("GetStdHandle")
	procGetConsoleMode     = kernel32.NewProc("GetConsoleMode")
	procSetConsoleMode     = kernel32.NewProc("SetConsoleMode")
	procSetConsoleCP       = kernel32.NewProc("SetConsoleCP")
	procSetConsoleOutputCP = kernel32.NewProc("SetConsoleOutputCP")
)

const (
	stdOutputHandle = uint32(0xFFFFFFF5) // STD_OUTPUT_HANDLE = -11
	stdErrorHandle  = uint32(0xFFFFFFF4) // STD_ERROR_HANDLE = -12

	enableVirtualTerminalProcessing = 0x0004
)

// EnableVirtualTerminal enables Windows 10+ ANSI VT100 / Virtual Terminal Processing
// on stdout and stderr handles, and switches the console codepage to UTF-8 (65001).
func EnableVirtualTerminal() {
	// Set UTF-8 codepage for console input and output
	_, _, _ = procSetConsoleCP.Call(uintptr(65001))
	_, _, _ = procSetConsoleOutputCP.Call(uintptr(65001))

	// Enable ENABLE_VIRTUAL_TERMINAL_PROCESSING (0x0004) on stdout and stderr
	for _, stdHandle := range []uint32{stdOutputHandle, stdErrorHandle} {
		h, _, _ := procGetStdHandle.Call(uintptr(stdHandle))
		if h == 0 || h == ^uintptr(0) {
			continue
		}
		var mode uint32
		r, _, _ := procGetConsoleMode.Call(h, uintptr(unsafe.Pointer(&mode)))
		if r != 0 {
			mode |= enableVirtualTerminalProcessing
			_, _, _ = procSetConsoleMode.Call(h, uintptr(mode))
		}
	}
}
