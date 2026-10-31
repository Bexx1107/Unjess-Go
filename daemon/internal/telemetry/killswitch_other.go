//go:build !windows

package telemetry

import "fmt"

func showKillswitchDialog(reason string) {
	fmt.Printf("\n[UNJESS NOTICE] Application disabled by server: %s\n\n", reason)
}
