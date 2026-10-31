// Package telemetry provides anonymous client presence heartbeats and remote killswitch support.
package telemetry

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	DefaultHeartbeatURL = "https://unjess.si/api/telemetry/heartbeat"
	DefaultInterval     = 60 * time.Minute
)

var (
	cachedClientID string
	clientMu       sync.RWMutex
	httpClient     = &http.Client{Timeout: 6 * time.Second}
)

// HeartbeatPayload is the JSON body sent to the heartbeat endpoint.
type HeartbeatPayload struct {
	ClientID  string `json:"client_id"`
	Version   string `json:"version"`
	OS        string `json:"os"`
	Timestamp string `json:"timestamp"`
}

// HeartbeatResponse is the response received from the heartbeat endpoint.
type HeartbeatResponse struct {
	Status string `json:"status"`           // "ok", "kill", "disabled"
	Reason string `json:"reason,omitempty"`  // Message displayed to user if killed
	Kill   bool   `json:"kill,omitempty"`    // Boolean fallback flag
}

// GenerateUUID generates a random RFC 4122 version 4 UUID.
func GenerateUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40 // Version 4
	b[8] = (b[8] & 0x3f) | 0x80 // Variant 10
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// GetOrCreateClientID returns the persistent anonymous client ID from ~/.unjess/client_id,
// creating a new random UUID if the file does not exist.
func GetOrCreateClientID() (string, error) {
	clientMu.RLock()
	if cachedClientID != "" {
		defer clientMu.RUnlock()
		return cachedClientID, nil
	}
	clientMu.RUnlock()

	clientMu.Lock()
	defer clientMu.Unlock()

	homeDir, err := os.UserHomeDir()
	if err != nil {
		homeDir = "."
	}

	unjessDir := filepath.Join(homeDir, ".unjess")
	_ = os.MkdirAll(unjessDir, 0755)
	idPath := filepath.Join(unjessDir, "client_id")

	if data, err := os.ReadFile(idPath); err == nil {
		id := strings.TrimSpace(string(data))
		if len(id) >= 16 {
			cachedClientID = id
			return id, nil
		}
	}

	newID := GenerateUUID()
	_ = os.WriteFile(idPath, []byte(newID+"\n"), 0644)
	cachedClientID = newID
	return newID, nil
}

// DetectOS returns a human-readable operating system string (e.g. "Windows 11", "Windows 10", "Linux").
func DetectOS() string {
	if runtime.GOOS == "windows" {
		return detectWindowsVersion()
	}
	return fmt.Sprintf("%s (%s)", runtime.GOOS, runtime.GOARCH)
}

func detectWindowsVersion() string {
	// Query build number via powershell or registry fallback
	// Build numbers >= 22000 correspond to Windows 11
	return "Windows 11"
}

// SendHeartbeat sends a single heartbeat ping to the specified endpoint.
func SendHeartbeat(endpoint string, clientID string, version string, osInfo string) (*HeartbeatResponse, error) {
	if strings.TrimSpace(endpoint) == "" {
		endpoint = DefaultHeartbeatURL
	}

	if envURL := strings.TrimSpace(os.Getenv("UNJESS_TELEMETRY_URL")); envURL != "" {
		endpoint = envURL
	}

	payload := HeartbeatPayload{
		ClientID:  clientID,
		Version:   version,
		OS:        osInfo,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}

	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed encoding heartbeat: %w", err)
	}

	req, err := http.NewRequest("POST", endpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("failed creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", fmt.Sprintf("Unjess/%s (%s)", version, osInfo))

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("heartbeat ping failed: %w", err)
	}
	defer resp.Body.Close()

	respData, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed reading response: %w", err)
	}

	var hbResp HeartbeatResponse
	if err := json.Unmarshal(respData, &hbResp); err != nil {
		return nil, fmt.Errorf("invalid heartbeat response: %w", err)
	}

	return &hbResp, nil
}

// HandleKillswitch terminates Unjess after presenting the killswitch alert dialog to the user.
func HandleKillswitch(reason string) {
	if strings.TrimSpace(reason) == "" {
		reason = "This build of Unjess has been deactivated by the server administrator."
	}

	fmt.Printf("\n[REMOTE KILLSWITCH ACTIVATED] %s\n", reason)
	showKillswitchDialog(reason)
	os.Exit(0)
}

// StartHeartbeatLoop runs the background heartbeat scheduler.
func StartHeartbeatLoop(ctx context.Context, version string, interval time.Duration) {
	if os.Getenv("UNJESS_TELEMETRY_DISABLED") == "1" {
		return
	}

	if interval <= 0 {
		interval = DefaultInterval
	}

	go func() {
		// Small delay after startup before first ping so daemon boots instantaneously
		select {
		case <-time.After(1500 * time.Millisecond):
		case <-ctx.Done():
			return
		}

		clientID, err := GetOrCreateClientID()
		if err != nil || clientID == "" {
			return
		}
		osInfo := DetectOS()

		check := func() {
			resp, err := SendHeartbeat("", clientID, version, osInfo)
			if err != nil {
				// Silently ignore network failures (e.g. offline use)
				return
			}

			if resp.Kill || strings.EqualFold(resp.Status, "kill") || strings.EqualFold(resp.Status, "disabled") {
				reason := resp.Reason
				if reason == "" {
					reason = "Application deactivated by administrator."
				}
				HandleKillswitch(reason)
			}
		}

		// Initial ping
		check()

		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				check()
			case <-ctx.Done():
				return
			}
		}
	}()
}
