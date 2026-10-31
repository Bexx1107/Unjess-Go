// Command unjessd runs the Unjess Go Daemon server for the Desktop GUI and Mobile Companion.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"unjess-daemon/internal/agent"
	"unjess-daemon/internal/api"
	"unjess-daemon/internal/config"
	"unjess-daemon/internal/skills"
	"unjess-daemon/internal/telemetry"
	"unjess-daemon/internal/updater"
)

func main() {
	portFlag := flag.Int("port", 9757, "HTTP server port (default: 9757)")
	hostFlag := flag.String("host", "0.0.0.0", "HTTP server bind address (default: 0.0.0.0)")
	parentExeFlag := flag.String("parent-exe", "", "Path to parent desktop executable (Unjess.exe)")
	flag.Parse()

	if *parentExeFlag != "" {
		updater.SetParentExecutable(*parentExeFlag)
	}

	// 1. Load configuration and merge saved keys/env vars
	settings := config.LoadConfig()

	// 2. Ensure builtin skills are seeded in ~/.unjess/skills
	if err := skills.EnsureBuiltinSkillsSeeded(); err != nil {
		log.Printf("Warning: failed seeding builtin skills: %v", err)
	}

	// 3. Initialize Agent Harness Manager
	hm := agent.NewHarnessManager(settings)

	// 4. Start background telemetry presence heartbeat & remote killswitch check
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	telemetry.StartHeartbeatLoop(ctx, updater.CurrentVersion, 60*time.Minute)

	// 5. Create API Server
	server := api.NewServer(*portFlag, *hostFlag, settings, hm)

	// 6. Print startup banner
	fmt.Printf("Unjess Go Daemon (unjessd) listening on http://127.0.0.1:%d\n", *portFlag)
	if *hostFlag == "0.0.0.0" {
		netInfo := config.GetNetworkInfo(settings, *portFlag)
		if lanIP, ok := netInfo["lan_ip"].(string); ok && lanIP != "127.0.0.1" && lanIP != "" {
			fmt.Printf("Mobile Companion available on http://%s:%d\n", lanIP, *portFlag)
		}
	}

	// 7. Handle OS signals for graceful shutdown
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	go func() {
		if err := server.Start(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Daemon failed to start: %v", err)
		}
	}()

	<-stop
	fmt.Println("\nShutting down Unjess Go Daemon...")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("Error during server shutdown: %v", err)
	}
	fmt.Println("Unjess Go Daemon stopped.")
}
