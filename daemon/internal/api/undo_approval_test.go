package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"unjess-daemon/internal/agent"
	"unjess-daemon/internal/tools"
)

func TestAPI_UndoEndpoint(t *testing.T) {
	srv, tempDir := setupTestServer(t)
	defer os.RemoveAll(tempDir)

	handler := srv.Routes()

	// Create test file
	testFile := filepath.Join(tempDir, "sample.txt")
	_ = os.WriteFile(testFile, []byte("Original Content\n"), 0644)

	// Create conversation
	convRes := srv.Harness.CreateConversation("Undo API Test", tempDir, "normal")
	conv := convRes["conversation"].(map[string]any)
	convID := conv["id"].(string)

	// Simulate turn modifying file
	ex := tools.NewToolExecutor(tempDir, 60)
	_, _ = ex.Execute(context.Background(), "edit_file", map[string]any{
		"path":       "sample.txt",
		"old_string": "Original Content",
		"new_string": "Modified Content",
	})

	userTurn := map[string]any{
		"id":        "turn_user_1",
		"role":      "user",
		"text":      "modify sample.txt",
		"timestamp": "10:00",
	}
	asstTurn := map[string]any{
		"id":        "turn_asst_1",
		"role":      "agent",
		"text":      "done modifying",
		"timestamp": "10:01",
		"snapshots": ex.Snapshots,
	}

	conv["turns"] = []any{userTurn, asstTurn}
	conv["messages"] = []any{
		map[string]any{"role": "user", "content": "modify sample.txt"},
		map[string]any{"role": "assistant", "content": "done modifying"},
	}

	// Verify modified content
	cur, _ := os.ReadFile(testFile)
	if string(cur) != "Modified Content\n" {
		t.Fatalf("expected modified content before undo, got %q", string(cur))
	}

	// Call POST /api/harness/undo
	undoBody := map[string]any{
		"conv_id": convID,
		"turn_id": "turn_asst_1",
	}
	bodyBytes, _ := json.Marshal(undoBody)
	req := httptest.NewRequest("POST", "/api/harness/undo", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var res map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("failed decoding response: %v", err)
	}

	if res["status"] != "ok" {
		t.Fatalf("expected status ok, got %v", res["status"])
	}

	// Verify file restored to original content
	cur, _ = os.ReadFile(testFile)
	if string(cur) != "Original Content\n" {
		t.Fatalf("expected original content after undo, got %q", string(cur))
	}
}

func TestAPI_SubagentsApproveEndpoint(t *testing.T) {
	srv, tempDir := setupTestServer(t)
	defer os.RemoveAll(tempDir)

	handler := srv.Routes()

	// 1. When no approval is pending, returns error
	noApprBody, _ := json.Marshal(map[string]any{
		"approved": true,
		"specs":    []map[string]any{},
	})
	req := httptest.NewRequest("POST", "/api/harness/subagents/approve", bytes.NewReader(noApprBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	var errRes map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &errRes)
	if errRes["status"] != "error" {
		t.Fatalf("expected status error, got %v", errRes)
	}

	// 2. Set up a pending approval on live turn
	ch := make(chan []map[string]any, 1)
	live := &agent.LiveTurn{
		Running:   true,
		ConvID:    "conv_appr",
		StartedAt: time.Now(),
		PendingSubagentApproval: &agent.PendingSubagentApproval{
			ID:           "appr_xyz",
			Specs:        []map[string]any{{"role": "Tester", "prompt": "test"}},
			DefaultModel: "default-model",
			CreatedAt:    time.Now().Unix(),
		},
		SubagentApprovalCh: ch,
	}

	srv.Harness.SetLiveTurnForTest(live)

	// Call POST /api/harness/subagents/approve with approval and updated specs
	approvedSpecs := []map[string]any{
		{"role": "Tester", "prompt": "test", "model": "gemini-2.5-flash"},
	}
	apprBody, _ := json.Marshal(map[string]any{
		"approved": true,
		"specs":    approvedSpecs,
	})
	reqAppr := httptest.NewRequest("POST", "/api/harness/subagents/approve", bytes.NewReader(apprBody))
	reqAppr.Header.Set("Content-Type", "application/json")
	recAppr := httptest.NewRecorder()
	handler.ServeHTTP(recAppr, reqAppr)

	if recAppr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", recAppr.Code)
	}
	var okRes map[string]any
	_ = json.Unmarshal(recAppr.Body.Bytes(), &okRes)
	if okRes["status"] != "ok" || okRes["approved"] != true {
		t.Fatalf("expected ok and approved=true, got %v", okRes)
	}

	// Verify channel received approved specs
	select {
	case received := <-ch:
		if len(received) != 1 || received[0]["model"] != "gemini-2.5-flash" {
			t.Fatalf("unexpected specs received: %v", received)
		}
	default:
		t.Fatalf("expected specs on channel")
	}
}
