package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"unjess-daemon/internal/agent"
)

func TestAPI_CommandApproveEndpoint(t *testing.T) {
	srv, tempDir := setupTestServer(t)
	defer os.RemoveAll(tempDir)

	handler := srv.Routes()

	// 1. Call POST /api/harness/command/approve when no approval is pending -> error
	bodyNoAppr, _ := json.Marshal(map[string]any{"approved": true})
	req1 := httptest.NewRequest("POST", "/api/harness/command/approve", bytes.NewReader(bodyNoAppr))
	req1.Header.Set("Content-Type", "application/json")
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, req1)

	if rec1.Code != http.StatusOK {
		t.Fatalf("expected 200 with error payload, got %d", rec1.Code)
	}
	var errRes map[string]any
	_ = json.Unmarshal(rec1.Body.Bytes(), &errRes)
	if errRes["status"] != "error" {
		t.Fatalf("expected status error when no approval pending, got %v", errRes)
	}

	// 2. Set up pending command approval on live turn
	cmdCh := make(chan bool, 1)
	live := &agent.LiveTurn{
		Running:   true,
		ConvID:    "conv_cmd_api",
		StartedAt: time.Now(),
		PendingCommandApproval: &agent.PendingCommandApproval{
			ID:        "cmd_abc123",
			ToolName:  "run_command",
			Command:   "go build ./...",
			Arguments: map[string]any{"command": "go build ./..."},
			Workspace: tempDir,
			CreatedAt: time.Now().Unix(),
		},
		CommandApprovalCh: cmdCh,
	}

	srv.Harness.SetLiveTurnForTest(live)

	// Verify GET /api/harness/live contains pending_command_approval
	reqLive := httptest.NewRequest("GET", "/api/harness/live", nil)
	recLive := httptest.NewRecorder()
	handler.ServeHTTP(recLive, reqLive)
	if recLive.Code != http.StatusOK {
		t.Fatalf("expected 200 for live state, got %d", recLive.Code)
	}
	var liveMap map[string]any
	_ = json.Unmarshal(recLive.Body.Bytes(), &liveMap)
	if liveMap["pending_command_approval"] == nil {
		t.Fatalf("expected pending_command_approval in live state response")
	}

	// 3. Call POST /api/harness/command/approve with approved=true
	bodyAppr, _ := json.Marshal(map[string]any{"approved": true})
	req2 := httptest.NewRequest("POST", "/api/harness/command/approve", bytes.NewReader(bodyAppr))
	req2.Header.Set("Content-Type", "application/json")
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec2.Code)
	}
	var okRes map[string]any
	_ = json.Unmarshal(rec2.Body.Bytes(), &okRes)
	if okRes["status"] != "ok" || okRes["approved"] != true {
		t.Fatalf("expected ok and approved=true, got %v", okRes)
	}

	// Verify channel received true
	select {
	case approved := <-cmdCh:
		if !approved {
			t.Fatalf("expected true on channel, got false")
		}
	default:
		t.Fatalf("channel was empty after command approval")
	}
}

func TestAPI_SetAgentModeEndpoint(t *testing.T) {
	srv, tempDir := setupTestServer(t)
	defer os.RemoveAll(tempDir)

	handler := srv.Routes()

	// 1. Switch to Supervised
	body1, _ := json.Marshal(map[string]any{"agent_mode": "Supervised"})
	req1 := httptest.NewRequest("POST", "/api/harness/mode", bytes.NewReader(body1))
	req1.Header.Set("Content-Type", "application/json")
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, req1)

	if rec1.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec1.Code)
	}
	var res1 map[string]any
	_ = json.Unmarshal(rec1.Body.Bytes(), &res1)
	if res1["agent_mode"] != "Supervised" || res1["confirm_commands"] != true {
		t.Errorf("unexpected response from /api/harness/mode: %v", res1)
	}

	// 2. Switch to Autonomous
	body2, _ := json.Marshal(map[string]any{"mode": "Autonomous"})
	req2 := httptest.NewRequest("POST", "/api/harness/mode", bytes.NewReader(body2))
	req2.Header.Set("Content-Type", "application/json")
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)

	var res2 map[string]any
	_ = json.Unmarshal(rec2.Body.Bytes(), &res2)
	if res2["agent_mode"] != "Autonomous" || res2["confirm_commands"] != false {
		t.Errorf("unexpected response from /api/harness/mode: %v", res2)
	}

	// 3. Toggle caveman_mode and ponytail_mode
	body3, _ := json.Marshal(map[string]any{"caveman_mode": true, "ponytail_mode": true})
	req3 := httptest.NewRequest("POST", "/api/harness/mode", bytes.NewReader(body3))
	req3.Header.Set("Content-Type", "application/json")
	rec3 := httptest.NewRecorder()
	handler.ServeHTTP(rec3, req3)

	var res3 map[string]any
	_ = json.Unmarshal(rec3.Body.Bytes(), &res3)
	if res3["caveman_mode"] != true || res3["ponytail_mode"] != true {
		t.Errorf("expected caveman_mode=true and ponytail_mode=true, got %v", res3)
	}
}

func TestAPI_CommandApprove_SwitchToAutonomous(t *testing.T) {
	srv, tempDir := setupTestServer(t)
	defer os.RemoveAll(tempDir)

	handler := srv.Routes()

	cmdCh := make(chan bool, 1)
	live := &agent.LiveTurn{
		Running:   true,
		ConvID:    "conv_cmd_auto",
		AgentMode: "Supervised",
		StartedAt: time.Now(),
		PendingCommandApproval: &agent.PendingCommandApproval{
			ID:        "cmd_switch_api",
			ToolName:  "run_command",
			Command:   "cargo build",
			Arguments: map[string]any{"command": "cargo build"},
			Workspace: tempDir,
			CreatedAt: time.Now().Unix(),
		},
		CommandApprovalCh: cmdCh,
	}
	srv.Harness.SetLiveTurnForTest(live)

	// Approve with switch_to_autonomous: true
	bodyAppr, _ := json.Marshal(map[string]any{"approved": true, "switch_to_autonomous": true})
	req := httptest.NewRequest("POST", "/api/harness/command/approve", bytes.NewReader(bodyAppr))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	select {
	case approved := <-cmdCh:
		if !approved {
			t.Fatalf("expected true on channel, got false")
		}
	default:
		t.Fatalf("channel was empty after approval with switch_to_autonomous")
	}

	liveState := srv.Harness.GetLiveTurnState()
	if liveState["agent_mode"] != "Autonomous" {
		t.Errorf("expected agent_mode to be Autonomous, got %v", liveState["agent_mode"])
	}
}
