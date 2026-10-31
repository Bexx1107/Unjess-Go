package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"unjess-daemon/internal/tools"
)

func TestResolveCommandApproval(t *testing.T) {
	hm, _ := setupTestManager(t)

	// No pending approval initially
	res := hm.ResolveCommandApproval(true)
	if res["status"] != "error" {
		t.Fatalf("expected error when no pending approval, got %v", res)
	}

	// Create a mock liveTurn with pending command approval
	ch := make(chan bool, 1)
	live := &LiveTurn{
		Running:   true,
		ConvID:    "conv_cmd_test",
		StartedAt: time.Now(),
		PendingCommandApproval: &PendingCommandApproval{
			ID:        "cmd_123456",
			ToolName:  "run_command",
			Command:   "git status",
			Arguments: map[string]any{"command": "git status"},
			Workspace: "/tmp/workspace",
			CreatedAt: time.Now().Unix(),
		},
		CommandApprovalCh: ch,
	}

	hm.liveMu.Lock()
	hm.liveTurn = live
	hm.liveMu.Unlock()

	// Check GetLiveTurnState includes pending_command_approval
	liveState := hm.GetLiveTurnState()
	pca, ok := liveState["pending_command_approval"].(*PendingCommandApproval)
	if !ok || pca == nil {
		t.Fatalf("expected pending_command_approval in live turn state, got %v", liveState["pending_command_approval"])
	}
	if pca.Command != "git status" {
		t.Errorf("expected command 'git status', got '%s'", pca.Command)
	}

	// Approve command
	apprRes := hm.ResolveCommandApproval(true)
	if apprRes["status"] != "ok" || apprRes["approved"] != true {
		t.Fatalf("expected ok and approved=true, got %v", apprRes)
	}

	// Verify channel received true
	select {
	case approved := <-ch:
		if !approved {
			t.Fatalf("expected true on channel, got false")
		}
	default:
		t.Fatalf("channel was empty after ResolveCommandApproval")
	}

	// Verify live state no longer has pending command approval
	liveState2 := hm.GetLiveTurnState()
	if liveState2["pending_command_approval"] != nil {
		t.Fatalf("expected pending_command_approval to be nil, got %v", liveState2["pending_command_approval"])
	}
}

func TestAbortTurn_CancelsCommandApproval(t *testing.T) {
	hm, _ := setupTestManager(t)

	ch := make(chan bool, 1)
	live := &LiveTurn{
		Running:   true,
		ConvID:    "conv_cmd_abort",
		StartedAt: time.Now(),
		PendingCommandApproval: &PendingCommandApproval{
			ID:        "cmd_987654",
			ToolName:  "run_command",
			Command:   "rm -rf *",
			Arguments: map[string]any{"command": "rm -rf *"},
			Workspace: "/tmp/workspace",
			CreatedAt: time.Now().Unix(),
		},
		CommandApprovalCh: ch,
	}

	hm.liveMu.Lock()
	hm.liveTurn = live
	hm.liveMu.Unlock()

	abortRes := hm.AbortTurn()
	if abortRes["status"] != "ok" || abortRes["aborted"] != true {
		t.Fatalf("expected status: ok, aborted: true, got %v", abortRes)
	}

	// Channel should have received false
	select {
	case approved := <-ch:
		if approved {
			t.Fatalf("expected false on abort, got true")
		}
	default:
		t.Fatalf("expected channel to receive false on abort")
	}

	// LiveTurn should have cleared pending approval
	hm.liveMu.RLock()
	if live.PendingCommandApproval != nil {
		t.Fatalf("expected PendingCommandApproval to be nil after abort")
	}
	if live.CommandApprovalCh != nil {
		t.Fatalf("expected CommandApprovalCh to be nil after abort")
	}
	hm.liveMu.RUnlock()
}

func TestExecuteWithoutRecording(t *testing.T) {
	tempDir := t.TempDir()
	testFile := filepath.Join(tempDir, "sample.txt")
	_ = os.WriteFile(testFile, []byte("line 1\nline 2\n"), 0644)

	executor := tools.NewToolExecutor(tempDir, 30)

	// Execute without recording
	res, ok := executor.ExecuteWithoutRecording(context.Background(), "read_file", map[string]any{"path": "sample.txt"})
	if !ok {
		t.Fatalf("ExecuteWithoutRecording failed: %s", res)
	}
	if !strings.Contains(res, "line 1") {
		t.Errorf("expected file contents, got: %s", res)
	}
	if len(executor.ToolCalls) != 0 {
		t.Fatalf("expected 0 recorded tool calls, got %d", len(executor.ToolCalls))
	}

	// Regular Execute records call
	res2, ok2 := executor.Execute(context.Background(), "read_file", map[string]any{"path": "sample.txt"})
	if !ok2 {
		t.Fatalf("Execute failed: %s", res2)
	}
	if len(executor.ToolCalls) != 1 {
		t.Fatalf("expected 1 recorded tool call, got %d", len(executor.ToolCalls))
	}
}

func TestActiveToolsFiltering(t *testing.T) {
	allDefs := tools.CoreToolDefinitions()
	findTool := func(defs []map[string]any, name string) bool {
		for _, d := range defs {
			if d["name"] == name {
				return true
			}
		}
		return false
	}

	// 1. Filesystem disabled
	req1 := ChatTurnRequest{
		AgentMode: "normal",
		Tools: map[string]bool{
			"filesystem": false,
		},
	}
	disabled1 := make(map[string]bool)
	if enabled, exists := req1.Tools["filesystem"]; exists && !enabled {
		disabled1["write_file"] = true
		disabled1["edit_file"] = true
	}
	filtered1 := make([]map[string]any, 0)
	for _, d := range allDefs {
		if !disabled1[d["name"].(string)] {
			filtered1 = append(filtered1, d)
		}
	}
	if findTool(filtered1, "write_file") || findTool(filtered1, "edit_file") {
		t.Errorf("filesystem tools should be removed")
	}
	if !findTool(filtered1, "read_file") {
		t.Errorf("read_file should remain")
	}

	// 2. Terminal disabled in normal mode -> run_command removed
	req2 := ChatTurnRequest{
		AgentMode: "normal",
		Tools: map[string]bool{
			"terminal": false,
		},
	}
	isSupervised2 := strings.EqualFold(strings.TrimSpace(req2.AgentMode), "supervised")
	disabled2 := make(map[string]bool)
	if enabled, exists := req2.Tools["terminal"]; exists && !enabled {
		if !isSupervised2 {
			disabled2["run_command"] = true
		}
	}
	filtered2 := make([]map[string]any, 0)
	for _, d := range allDefs {
		if !disabled2[d["name"].(string)] {
			filtered2 = append(filtered2, d)
		}
	}
	if findTool(filtered2, "run_command") {
		t.Errorf("run_command should be removed when terminal: false and not supervised")
	}

	// 3. Terminal disabled in supervised mode -> run_command MUST REMAIN for approval!
	req3 := ChatTurnRequest{
		AgentMode: "supervised",
		Tools: map[string]bool{
			"terminal": false,
		},
	}
	isSupervised3 := strings.EqualFold(strings.TrimSpace(req3.AgentMode), "supervised")
	disabled3 := make(map[string]bool)
	if enabled, exists := req3.Tools["terminal"]; exists && !enabled {
		if !isSupervised3 {
			disabled3["run_command"] = true
		}
	}
	filtered3 := make([]map[string]any, 0)
	for _, d := range allDefs {
		if !disabled3[d["name"].(string)] {
			filtered3 = append(filtered3, d)
		}
	}
	if !findTool(filtered3, "run_command") {
		t.Errorf("run_command MUST remain in supervised mode even if terminal is false")
	}

	// 4. Browser disabled -> web_search, read_url removed
	req4 := ChatTurnRequest{
		Tools: map[string]bool{
			"browser": false,
		},
	}
	disabled4 := make(map[string]bool)
	if enabled, exists := req4.Tools["browser"]; exists && !enabled {
		disabled4["web_search"] = true
		disabled4["read_url"] = true
	}
	filtered4 := make([]map[string]any, 0)
	for _, d := range allDefs {
		if !disabled4[d["name"].(string)] {
			filtered4 = append(filtered4, d)
		}
	}
	if findTool(filtered4, "web_search") || findTool(filtered4, "read_url") {
		t.Errorf("browser tools should be removed")
	}

	// 5. Git disabled -> git_status, git_diff removed
	req5 := ChatTurnRequest{
		Tools: map[string]bool{
			"git": false,
		},
	}
	disabled5 := make(map[string]bool)
	if enabled, exists := req5.Tools["git"]; exists && !enabled {
		disabled5["git_status"] = true
		disabled5["git_diff"] = true
	}
	filtered5 := make([]map[string]any, 0)
	for _, d := range allDefs {
		if !disabled5[d["name"].(string)] {
			filtered5 = append(filtered5, d)
		}
	}
	if findTool(filtered5, "git_status") || findTool(filtered5, "git_diff") {
		t.Errorf("git tools should be removed")
	}
}

func TestSetAgentMode_UnblocksCommandApproval(t *testing.T) {
	hm, _ := setupTestManager(t)

	ch := make(chan bool, 1)
	live := &LiveTurn{
		Running:   true,
		ConvID:    "conv_mode_switch",
		AgentMode: "Supervised",
		StartedAt: time.Now(),
		PendingCommandApproval: &PendingCommandApproval{
			ID:        "cmd_switch_test",
			ToolName:  "run_command",
			Command:   "go build ./...",
			Arguments: map[string]any{"command": "go build ./..."},
			Workspace: "/tmp/workspace",
			CreatedAt: time.Now().Unix(),
		},
		CommandApprovalCh: ch,
	}

	hm.liveMu.Lock()
	hm.liveTurn = live
	hm.liveMu.Unlock()

	// Switching to Autonomous mode should immediately approve and unblock the channel
	res := hm.SetAgentMode("Autonomous")
	if res["status"] != "ok" || res["agent_mode"] != "Autonomous" {
		t.Fatalf("expected ok with agent_mode Autonomous, got %v", res)
	}

	select {
	case approved := <-ch:
		if !approved {
			t.Fatalf("expected channel to receive true upon switching to Autonomous, got false")
		}
	default:
		t.Fatalf("channel was not unblocked by SetAgentMode")
	}

	// Verify liveTurn state
	state := hm.GetLiveTurnState()
	if state["agent_mode"] != "Autonomous" {
		t.Errorf("expected live turn agent_mode Autonomous, got %v", state["agent_mode"])
	}
	if state["pending_command_approval"] != nil {
		t.Errorf("expected pending_command_approval to be cleared, got %v", state["pending_command_approval"])
	}
}

func TestResolveCommandApprovalWithMode(t *testing.T) {
	hm, _ := setupTestManager(t)

	ch := make(chan bool, 1)
	live := &LiveTurn{
		Running:   true,
		ConvID:    "conv_resolve_mode",
		AgentMode: "Supervised",
		StartedAt: time.Now(),
		PendingCommandApproval: &PendingCommandApproval{
			ID:        "cmd_resolve_test",
			ToolName:  "run_command",
			Command:   "npm test",
			Arguments: map[string]any{"command": "npm test"},
			Workspace: "/tmp/workspace",
			CreatedAt: time.Now().Unix(),
		},
		CommandApprovalCh: ch,
	}

	hm.liveMu.Lock()
	hm.liveTurn = live
	hm.liveMu.Unlock()

	res := hm.ResolveCommandApprovalWithMode(true, true)
	if res["status"] != "ok" || res["approved"] != true {
		t.Fatalf("expected ok and approved=true, got %v", res)
	}

	select {
	case approved := <-ch:
		if !approved {
			t.Fatalf("expected channel to receive true, got false")
		}
	default:
		t.Fatalf("channel was empty after ResolveCommandApprovalWithMode")
	}

	state := hm.GetLiveTurnState()
	if state["agent_mode"] != "Autonomous" {
		t.Errorf("expected agent_mode to be Autonomous, got %v", state["agent_mode"])
	}
}
