package telemetry

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateUUID(t *testing.T) {
	u1 := GenerateUUID()
	u2 := GenerateUUID()

	if len(u1) != 36 {
		t.Errorf("expected UUID length 36, got %d (%s)", len(u1), u1)
	}
	if u1 == u2 {
		t.Errorf("expected unique UUIDs, got duplicates: %s", u1)
	}

	parts := strings.Split(u1, "-")
	if len(parts) != 5 {
		t.Errorf("expected 5 segments separated by hyphens, got %d", len(parts))
	}
	// Check version 4 marker
	if !strings.HasPrefix(parts[2], "4") {
		t.Errorf("expected version 4 UUID, got segment %s", parts[2])
	}
}

func TestGetOrCreateClientID(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("USERPROFILE", tmpHome)
	t.Setenv("HOME", tmpHome)

	// Clear cached ID
	clientMu.Lock()
	cachedClientID = ""
	clientMu.Unlock()

	id1, err := GetOrCreateClientID()
	if err != nil {
		t.Fatalf("failed getting client ID: %v", err)
	}
	if len(id1) < 16 {
		t.Fatalf("client ID too short: %s", id1)
	}

	// Verify file was written in ~/.unjess/client_id
	idFile := filepath.Join(tmpHome, ".unjess", "client_id")
	data, err := os.ReadFile(idFile)
	if err != nil {
		t.Fatalf("client_id file not found: %v", err)
	}
	if strings.TrimSpace(string(data)) != id1 {
		t.Errorf("file content mismatch: expected %s, got %s", id1, string(data))
	}

	// Second call should return the exact same ID
	id2, err := GetOrCreateClientID()
	if err != nil {
		t.Fatalf("second call failed: %v", err)
	}
	if id1 != id2 {
		t.Errorf("client ID changed between calls: %s vs %s", id1, id2)
	}
}

func TestSendHeartbeatOK(t *testing.T) {
	var received HeartbeatPayload
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(HeartbeatResponse{
			Status: "ok",
		})
	}))
	defer ts.Close()

	resp, err := SendHeartbeat(ts.URL, "test-client-123", "2.0.3", "Windows 11")
	if err != nil {
		t.Fatalf("SendHeartbeat failed: %v", err)
	}
	if resp.Status != "ok" {
		t.Errorf("expected status 'ok', got %s", resp.Status)
	}
	if resp.Kill {
		t.Errorf("expected Kill to be false")
	}

	if received.ClientID != "test-client-123" {
		t.Errorf("expected client_id 'test-client-123', got %s", received.ClientID)
	}
	if received.Version != "2.0.3" {
		t.Errorf("expected version '2.0.3', got %s", received.Version)
	}
}

func TestSendHeartbeatKill(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(HeartbeatResponse{
			Status: "kill",
			Reason: "This build is deprecated. Please upgrade at unjess.si",
			Kill:   true,
		})
	}))
	defer ts.Close()

	resp, err := SendHeartbeat(ts.URL, "banned-client-000", "1.0.0", "Windows 10")
	if err != nil {
		t.Fatalf("SendHeartbeat failed: %v", err)
	}
	if resp.Status != "kill" {
		t.Errorf("expected status 'kill', got %s", resp.Status)
	}
	if !resp.Kill {
		t.Errorf("expected Kill to be true")
	}
	if !strings.Contains(resp.Reason, "deprecated") {
		t.Errorf("expected reason to contain 'deprecated', got %s", resp.Reason)
	}
}

func TestDetectOS(t *testing.T) {
	osStr := DetectOS()
	if osStr == "" {
		t.Errorf("DetectOS returned empty string")
	}
}
