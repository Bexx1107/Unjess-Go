//go:build !windows

package cli

// EnableVirtualTerminal is a no-op on non-Windows platforms (macOS, Linux)
// where virtual terminal / ANSI escape processing and UTF-8 are supported natively.
func EnableVirtualTerminal() {}
