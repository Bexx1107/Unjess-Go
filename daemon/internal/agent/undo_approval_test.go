package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"unjess-daemon/internal/tools"
)

func TestUndoToTurn_FileSnapshots(t *testing.T) {
	hm, tempDir := setupTestManager(t)
	wsDir := filepath.Join(tempDir, "test_ws")
	_ = os.MkdirAll(wsDir, 0755)
	_, err := hm.SelectWorkspace(wsDir)
	if err != nil {
		t.Fatalf("SelectWorkspace failed: %v", err)
	}

	convRes := hm.CreateConversation("Test Undo", wsDir, "normal")
	conv := convRes["conversation"].(map[string]any)
	convID := conv["id"].(string)

	// Create initial file
	helloPath := filepath.Join(wsDir, "hello.txt")
	if err := os.WriteFile(helloPath, []byte("Version 0\n"), 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	// Turn 1: edits hello.txt to "Version 1", creates new.txt
	t1Executor := tools.NewToolExecutor(wsDir, 60)
	_, ok := t1Executor.Execute(context.Background(), "write_file", map[string]any{
		"path":    "new.txt",
		"content": "Brand new file\n",
	})
	if !ok {
		t.Fatalf("write_file new.txt failed")
	}

	_, ok = t1Executor.Execute(context.Background(), "edit_file", map[string]any{
		"path":       "hello.txt",
		"old_string": "Version 0",
		"new_string": "Version 1",
	})
	if !ok {
		t.Fatalf("edit_file hello.txt failed")
	}

	if len(t1Executor.Snapshots) != 2 {
		t.Fatalf("expected 2 snapshots in t1, got %d", len(t1Executor.Snapshots))
	}

	userTurn1ID := "turn_u1"
	asstTurn1ID := "turn_a1"

	userTurn1 := map[string]any{
		"id":        userTurn1ID,
		"role":      "user",
		"text":      "Please modify hello.txt and create new.txt",
		"timestamp": "12:00",
	}
	asstTurn1 := map[string]any{
		"id":        asstTurn1ID,
		"role":      "agent",
		"text":      "I modified hello.txt and created new.txt",
		"timestamp": "12:01",
		"snapshots": t1Executor.Snapshots,
	}

	hm.mu.Lock()
	conv["turns"] = []any{userTurn1, asstTurn1}
	conv["messages"] = []any{
		map[string]any{"role": "user", "content": "Please modify hello.txt and create new.txt"},
		map[string]any{"role": "assistant", "content": "I modified hello.txt and created new.txt"},
	}
	_ = hm.saveStateLocked()
	hm.mu.Unlock()

	// Turn 2: edits hello.txt to "Version 2"
	t2Executor := tools.NewToolExecutor(wsDir, 60)
	_, ok = t2Executor.Execute(context.Background(), "edit_file", map[string]any{
		"path":       "hello.txt",
		"old_string": "Version 1",
		"new_string": "Version 2",
	})
	if !ok {
		t.Fatalf("edit_file hello.txt to Version 2 failed")
	}

	userTurn2ID := "turn_u2"
	asstTurn2ID := "turn_a2"

	userTurn2 := map[string]any{
		"id":        userTurn2ID,
		"role":      "user",
		"text":      "Please update hello.txt to Version 2",
		"timestamp": "12:02",
	}
	asstTurn2 := map[string]any{
		"id":        asstTurn2ID,
		"role":      "agent",
		"text":      "Updated to Version 2",
		"timestamp": "12:03",
		"snapshots": t2Executor.Snapshots,
	}

	hm.mu.Lock()
	conv["turns"] = []any{userTurn1, asstTurn1, userTurn2, asstTurn2}
	conv["messages"] = []any{
		map[string]any{"role": "user", "content": "Please modify hello.txt and create new.txt"},
		map[string]any{"role": "assistant", "content": "I modified hello.txt and created new.txt"},
		map[string]any{"role": "user", "content": "Please update hello.txt to Version 2"},
		map[string]any{"role": "assistant", "content": "Updated to Version 2"},
	}
	_ = hm.saveStateLocked()
	hm.mu.Unlock()

	// Check on-disk state before undo
	curHello, _ := os.ReadFile(helloPath)
	if string(curHello) != "Version 2\n" {
		t.Fatalf("expected Version 2 before undo, got %q", string(curHello))
	}
	if _, err := os.Stat(filepath.Join(wsDir, "new.txt")); err != nil {
		t.Fatalf("new.txt should exist before undo")
	}

	// Undo Turn 2 by turnID
	undoRes, err := hm.UndoToTurn(convID, asstTurn2ID, -1)
	if err != nil {
		t.Fatalf("UndoToTurn failed: %v", err)
	}

	if undoRes["turns_removed"].(int) != 2 {
		t.Fatalf("expected 2 turns removed (user + assistant), got %v", undoRes["turns_removed"])
	}

	// hello.txt should now be back to "Version 1\n"
	curHello, _ = os.ReadFile(helloPath)
	if string(curHello) != "Version 1\n" {
		t.Fatalf("expected Version 1 after undoing turn 2, got %q", string(curHello))
	}
	// new.txt should still exist
	if _, err := os.Stat(filepath.Join(wsDir, "new.txt")); err != nil {
		t.Fatalf("new.txt should still exist after undoing turn 2")
	}

	// Now Undo Turn 1 by turnIndex 1
	undoRes1, err := hm.UndoToTurn(convID, "", 1)
	if err != nil {
		t.Fatalf("UndoToTurn 1 failed: %v", err)
	}

	if undoRes1["turns_removed"].(int) != 2 {
		t.Fatalf("expected 2 turns removed for turn 1, got %v", undoRes1["turns_removed"])
	}

	// hello.txt should now be back to "Version 0\n"
	curHello, _ = os.ReadFile(helloPath)
	if string(curHello) != "Version 0\n" {
		t.Fatalf("expected Version 0 after undoing turn 1, got %q", string(curHello))
	}
	// new.txt should now be deleted!
	if _, err := os.Stat(filepath.Join(wsDir, "new.txt")); !os.IsNotExist(err) {
		t.Fatalf("new.txt should be deleted after undoing turn 1, err=%v", err)
	}
}

func TestSubagentModelRoutingAndProvider(t *testing.T) {
	hm, tempDir := setupTestManager(t)
	wsDir := filepath.Join(tempDir, "ws")
	_ = os.MkdirAll(wsDir, 0755)

	// Case 1: inherit with no custom setting uses defaultModel
	hm.settings.Model = "qwen2.5-coder:7b"
	hm.settings.Provider = "ollama"
	hm.settings.SubagentModel = "inherit"

	specs1 := []map[string]any{
		{"role": "TestSub1", "prompt": "test 1", "model": "inherit"},
	}
	runs1 := hm.SpawnSubagents(context.Background(), "conv_1", wsDir, specs1, false)
	if len(runs1) != 1 {
		t.Fatalf("expected 1 run, got %d", len(runs1))
	}
	if runs1[0].Model != "qwen2.5-coder:7b" {
		t.Errorf("expected qwen2.5-coder:7b, got %s", runs1[0].Model)
	}
	if runs1[0].Provider != "ollama" {
		t.Errorf("expected ollama, got %s", runs1[0].Provider)
	}

	// Case 2: SubagentModel set to Gemini
	hm.settings.SubagentModel = "gemini-2.5-flash"
	specs2 := []map[string]any{
		{"role": "TestSub2", "prompt": "test 2", "model": "inherit"},
	}
	runs2 := hm.SpawnSubagents(context.Background(), "conv_1", wsDir, specs2, false)
	if len(runs2) != 1 {
		t.Fatalf("expected 1 run, got %d", len(runs2))
	}
	if runs2[0].Model != "gemini-2.5-flash" {
		t.Errorf("expected gemini-2.5-flash, got %s", runs2[0].Model)
	}
	if runs2[0].Provider != "google" {
		t.Errorf("expected google provider, got %s", runs2[0].Provider)
	}

	// Case 3: Explicit spec override to Anthropic claude-3-7-sonnet
	specs3 := []map[string]any{
		{"role": "TestSub3", "prompt": "test 3", "model": "claude-3-7-sonnet"},
	}
	runs3 := hm.SpawnSubagents(context.Background(), "conv_1", wsDir, specs3, false)
	if len(runs3) != 1 {
		t.Fatalf("expected 1 run, got %d", len(runs3))
	}
	if runs3[0].Model != "claude-3-7-sonnet" {
		t.Errorf("expected claude-3-7-sonnet, got %s", runs3[0].Model)
	}
	if runs3[0].Provider != "anthropic" {
		t.Errorf("expected anthropic provider, got %s", runs3[0].Provider)
	}
}

func TestResolveSubagentApproval(t *testing.T) {
	hm, _ := setupTestManager(t)

	// No pending approval initially
	res := hm.ResolveSubagentApproval(nil, true)
	if res["status"] != "error" {
		t.Fatalf("expected error when no pending approval, got %v", res)
	}

	// Create a mock liveTurn with pending approval
	ch := make(chan []map[string]any, 1)
	live := &LiveTurn{
		Running:   true,
		ConvID:    "conv_test",
		StartedAt: time.Now(),
		PendingSubagentApproval: &PendingSubagentApproval{
			ID:           "appr_123456",
			Specs:        []map[string]any{{"role": "Coder", "model": "inherit"}},
			DefaultModel: "test-model",
			CreatedAt:    time.Now().Unix(),
		},
		SubagentApprovalCh: ch,
	}

	hm.liveMu.Lock()
	hm.liveTurn = live
	hm.liveMu.Unlock()

	// Check GetLiveTurnState includes pending_subagent_approval
	liveState := hm.GetLiveTurnState()
	if liveState["pending_subagent_approval"] == nil {
		t.Fatalf("expected pending_subagent_approval in live turn state")
	}

	// Approve with updated specs
	approvedSpecs := []map[string]any{
		{"role": "Coder", "model": "gpt-4o"},
	}
	apprRes := hm.ResolveSubagentApproval(approvedSpecs, true)
	if apprRes["status"] != "ok" || apprRes["approved"] != true {
		t.Fatalf("expected ok and approved=true, got %v", apprRes)
	}

	// Verify channel received specs
	select {
	case received := <-ch:
		if len(received) != 1 || received[0]["model"] != "gpt-4o" {
			t.Fatalf("unexpected specs received on channel: %v", received)
		}
	default:
		t.Fatalf("channel was empty after ResolveSubagentApproval")
	}

	// Verify live state no longer has pending approval
	liveState2 := hm.GetLiveTurnState()
	if liveState2["pending_subagent_approval"] != nil {
		t.Fatalf("expected pending_subagent_approval to be cleared")
	}
}
