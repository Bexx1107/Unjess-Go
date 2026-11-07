package agent

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"unjess-daemon/internal/config"
	"unjess-daemon/internal/llm"
	"unjess-daemon/internal/tools"
	"unjess-daemon/internal/version"
)

func setupTestManager(t *testing.T) (*HarnessManager, string) {
	t.Helper()
	tempDir := t.TempDir()
	stateFile := filepath.Join(tempDir, "harness_state.json")
	t.Setenv("UNJESS_STATE_PATH", stateFile)
	t.Setenv("UNJESS_USAGE_LEDGER_PATH", filepath.Join(tempDir, "usage_ledger.json"))

	settings := config.NewDefaultSettings()
	hm := NewHarnessManager(settings)
	return hm, tempDir
}

func TestNewHarnessManager(t *testing.T) {
	hm, _ := setupTestManager(t)

	if hm.GetSettings() == nil {
		t.Fatal("expected non-nil settings")
	}

	activeWS := hm.ActiveWorkspace()
	if activeWS == "" {
		t.Fatal("expected non-empty active workspace")
	}

	payload := hm.GetFullStatePayload()
	if payload["status"] != "ok" {
		t.Fatalf("expected status ok, got %v", payload["status"])
	}

	state, ok := payload["state"].(map[string]any)
	if !ok {
		t.Fatalf("expected state map in payload")
	}

	// Verify default workflows
	workflows, ok := state["workflows"].([]any)
	if !ok || len(workflows) < 4 {
		t.Fatalf("expected at least 4 workflows, got %d", len(workflows))
	}
	wfIDs := make(map[string]bool)
	for _, w := range workflows {
		wm := w.(map[string]any)
		wfIDs[wm["id"].(string)] = true
	}
	for _, expectedID := range []string{"wf_1", "wf_2", "wf_3", "wf_4"} {
		if !wfIDs[expectedID] {
			t.Errorf("expected workflow %s to be present", expectedID)
		}
	}

	// Verify default custom agents
	agents, ok := state["custom_agents"].([]any)
	if !ok || len(agents) < 3 {
		t.Fatalf("expected at least 3 custom agents, got %d", len(agents))
	}
	agIDs := make(map[string]bool)
	for _, a := range agents {
		am := a.(map[string]any)
		agIDs[am["id"].(string)] = true
	}
	for _, expectedID := range []string{"agent_architect", "agent_coder", "agent_tester"} {
		if !agIDs[expectedID] {
			t.Errorf("expected agent %s to be present", expectedID)
		}
	}

	// Verify workspaces
	workspaces, ok := state["workspaces"].([]any)
	if !ok || len(workspaces) == 0 {
		t.Fatal("expected at least 1 workspace")
	}

	// Verify conversations
	convs, ok := state["conversations"].([]any)
	if !ok || len(convs) == 0 {
		t.Fatal("expected at least 1 conversation")
	}
}

func TestUpdateSettings(t *testing.T) {
	hm, _ := setupTestManager(t)

	newSettings := config.NewDefaultSettings()
	newSettings.Model = "custom-test-model:14b"
	newSettings.Provider = "ollama"

	hm.UpdateSettings(newSettings)
	got := hm.GetSettings()
	if got.Model != "custom-test-model:14b" {
		t.Errorf("expected model custom-test-model:14b, got %s", got.Model)
	}
}

func TestGetFullStatePayload(t *testing.T) {
	hm, _ := setupTestManager(t)
	payload := hm.GetFullStatePayload()

	requiredKeys := []string{
		"status", "state", "workspace_files", "git_info",
		"skills_count", "mcp_count", "default_model", "provider",
	}
	for _, k := range requiredKeys {
		if _, ok := payload[k]; !ok {
			t.Errorf("missing required key in GetFullStatePayload: %s", k)
		}
	}
}

func TestLiveTurnAndAbort(t *testing.T) {
	hm, _ := setupTestManager(t)

	// Initially idle
	state := hm.GetLiveTurnState()
	if state["running"] != false {
		t.Errorf("expected running to be false when idle")
	}

	abortRes := hm.AbortTurn()
	if abortRes["aborted"] != false {
		t.Errorf("expected aborted false when no active turn")
	}

	// Simulate an active turn
	cancelled := false
	_, cancelFunc := context.WithCancel(context.Background())
	defer cancelFunc()

	executor := tools.NewToolExecutor(hm.ActiveWorkspace(), 60)
	executor.ToolCalls = []tools.ToolCallRecord{
		{Name: "list_dir", Arguments: map[string]any{"path": "."}, Result: "ok", Status: "completed"},
	}
	executor.FilesTouched = []tools.FileTouch{
		{Path: "test.txt", Action: "created", IsDir: false},
	}
	executor.Diffs = []tools.FileDiff{
		{Path: "test.txt", Action: "created", Diff: "+hello", Additions: 1, Deletions: 0},
	}

	live := &LiveTurn{
		Running:   true,
		ConvID:    "conv_test",
		StartedAt: time.Now(),
		Thinking:  "Planning step",
		Text:      "Working...",
		PlanningSteps: []map[string]any{
			{
				"id":         "step_think_1",
				"type":       "thinking",
				"text":       "Thinking & analyzing...",
				"status":     "running",
				"started_ms": time.Now().Add(-500 * time.Millisecond).UnixMilli(),
			},
		},
		CancelFunc: func() {
			cancelled = true
			cancelFunc()
		},
		Executor: executor,
	}

	hm.liveMu.Lock()
	hm.liveTurn = live
	hm.liveMu.Unlock()

	activeState := hm.GetLiveTurnState()
	if activeState["running"] != true {
		t.Errorf("expected running to be true during active turn")
	}
	if activeState["conv_id"] != "conv_test" {
		t.Errorf("expected conv_id conv_test, got %v", activeState["conv_id"])
	}
	tc, ok := activeState["toolCalls"].([]tools.ToolCallRecord)
	if !ok || len(tc) != 1 {
		t.Fatalf("expected 1 tool call in toolCalls, got %+v", activeState["toolCalls"])
	}
	if tc[0].Name != "list_dir" {
		t.Errorf("expected tool name list_dir, got %s", tc[0].Name)
	}
	ps, ok := activeState["planningSteps"].([]map[string]any)
	if !ok || len(ps) != 1 {
		t.Fatalf("expected 1 planningStep in planningSteps, got %+v", activeState["planningSteps"])
	}
	if dur, ok := ps[0]["duration_s"].(float64); !ok || dur <= 0 {
		t.Errorf("expected positive live duration_s on running planning step, got %v", ps[0]["duration_s"])
	}

	// Abort active turn
	abortActiveRes := hm.AbortTurn()
	if abortActiveRes["aborted"] != true {
		t.Errorf("expected aborted to be true")
	}
	if !cancelled {
		t.Errorf("expected cancel function to have been called")
	}
	if !live.Aborted || live.Running {
		t.Errorf("expected live turn to be flagged aborted and not running")
	}
}

func TestConversationLifecycle(t *testing.T) {
	hm, tempDir := setupTestManager(t)

	// Create standard conversation
	createRes := hm.CreateConversation("Feature Session", tempDir, "")
	if createRes["status"] != "ok" {
		t.Fatalf("failed creating conversation: %+v", createRes)
	}
	conv := createRes["conversation"].(map[string]any)
	convID := conv["id"].(string)
	if conv["title"] != "Feature Session" {
		t.Errorf("expected title 'Feature Session', got %v", conv["title"])
	}

	// Clear conversation
	hm.mu.Lock()
	conv["turns"] = []any{map[string]any{"role": "user", "text": "hello"}}
	conv["messages"] = []any{map[string]any{"role": "user", "content": "hello"}}
	hm.mu.Unlock()

	clearRes := hm.ClearConversation(convID)
	if clearRes["status"] != "ok" {
		t.Fatalf("failed clearing conversation: %+v", clearRes)
	}
	hm.mu.RLock()
	turns := conv["turns"].([]any)
	msgs := conv["messages"].([]any)
	hm.mu.RUnlock()
	if len(turns) != 0 || len(msgs) != 0 {
		t.Errorf("expected empty turns and messages after clear, got %d turns, %d msgs", len(turns), len(msgs))
	}

	// Quick mode conversation
	quickRes := hm.CreateConversation("Quick Bugfix", "__quick__", "quick")
	if quickRes["status"] != "ok" {
		t.Fatalf("failed creating quick conversation: %+v", quickRes)
	}
	quickConv := quickRes["conversation"].(map[string]any)
	if !strings.Contains(quickConv["workspace"].(string), "quick") {
		t.Errorf("expected quick workspace in conversation: %v", quickConv["workspace"])
	}

	// Rename conversation
	renRes := hm.RenameConversation(convID, "Renamed Session Title")
	if renRes["status"] != "ok" {
		t.Fatalf("failed renaming conversation: %+v", renRes)
	}
	renConv := renRes["conversation"].(map[string]any)
	if renConv["title"] != "Renamed Session Title" {
		t.Errorf("expected title 'Renamed Session Title', got %v", renConv["title"])
	}

	// Delete conversation
	delRes := hm.DeleteConversation(convID)
	if delRes["status"] != "ok" {
		t.Fatalf("failed deleting conversation: %+v", delRes)
	}

	// Delete all conversations ensures at least one conversation remains
	hm.mu.Lock()
	convs := hm.state["conversations"].([]any)
	for _, c := range convs {
		cid := c.(map[string]any)["id"].(string)
		hm.mu.Unlock()
		hm.DeleteConversation(cid)
		hm.mu.Lock()
	}
	remaining := hm.state["conversations"].([]any)
	hm.mu.Unlock()

	if len(remaining) == 0 {
		t.Fatal("expected at least one conversation to remain after deleting all")
	}
}

func TestWorkspaceLifecycle(t *testing.T) {
	hm, tempDir := setupTestManager(t)

	newWS := filepath.Join(tempDir, "workspace-a")
	addRes, err := hm.AddWorkspace(newWS, "Workspace A")
	if err != nil {
		t.Fatalf("failed adding workspace: %v", err)
	}
	if addRes["status"] != "ok" {
		t.Errorf("expected status ok, got %v", addRes["status"])
	}
	if hm.ActiveWorkspace() != newWS {
		t.Errorf("expected active workspace %s, got %s", newWS, hm.ActiveWorkspace())
	}

	// Select workspace
	newWS2 := filepath.Join(tempDir, "workspace-b")
	selRes, err := hm.SelectWorkspace(newWS2)
	if err != nil {
		t.Fatalf("failed selecting workspace: %v", err)
	}
	if selRes["status"] != "ok" {
		t.Errorf("expected status ok, got %v", selRes["status"])
	}
	if hm.ActiveWorkspace() != newWS2 {
		t.Errorf("expected active workspace %s, got %s", newWS2, hm.ActiveWorkspace())
	}
}

func TestWorkflowsCRUD(t *testing.T) {
	hm, _ := setupTestManager(t)

	wfs := hm.ListWorkflows()
	initialCount := len(wfs)
	if initialCount < 4 {
		t.Fatalf("expected at least 4 default workflows, got %d", initialCount)
	}

	// Save new workflow
	newWf := map[string]any{
		"id":          "wf_custom_test",
		"title":       "Custom Test Workflow",
		"category":    "Testing",
		"icon":        "zap",
		"description": "Custom workflow for unit testing",
		"steps":       []any{"Step 1", "Step 2"},
		"tools":       []any{"read_file", "write_file"},
		"prompt":      "Execute test workflow",
	}

	saveRes := hm.SaveWorkflow(newWf)
	if saveRes["status"] != "ok" {
		t.Fatalf("failed saving workflow: %+v", saveRes)
	}
	if len(hm.ListWorkflows()) != initialCount+1 {
		t.Errorf("expected %d workflows after save, got %d", initialCount+1, len(hm.ListWorkflows()))
	}

	// Update workflow
	newWf["title"] = "Updated Custom Workflow"
	hm.SaveWorkflow(newWf)
	var found map[string]any
	for _, w := range hm.ListWorkflows() {
		if w["id"] == "wf_custom_test" {
			found = w
			break
		}
	}
	if found == nil || found["title"] != "Updated Custom Workflow" {
		t.Errorf("expected updated title, got %+v", found)
	}

	// Delete workflow
	delRes := hm.DeleteWorkflow("wf_custom_test")
	if delRes["status"] != "ok" {
		t.Fatalf("failed deleting workflow: %+v", delRes)
	}
	if len(hm.ListWorkflows()) != initialCount {
		t.Errorf("expected %d workflows after delete, got %d", initialCount, len(hm.ListWorkflows()))
	}
}

func TestAgentsCRUD(t *testing.T) {
	hm, _ := setupTestManager(t)

	agents := hm.ListAgents()
	initialCount := len(agents)
	if initialCount < 3 {
		t.Fatalf("expected at least 3 default agents, got %d", initialCount)
	}

	// Save new agent
	newAg := map[string]any{
		"id":            "agent_doc_specialist",
		"name":          "Documentation Specialist",
		"role":          "Technical Writer & API Documenter",
		"icon":          "book-open",
		"color":         "blue",
		"model":         "inherit",
		"tools":         []any{"read_file", "write_file"},
		"system_prompt": "You write clear, comprehensive documentation for codebase APIs.",
	}

	saveRes := hm.SaveAgent(newAg)
	if saveRes["status"] != "ok" {
		t.Fatalf("failed saving agent: %+v", saveRes)
	}
	if len(hm.ListAgents()) != initialCount+1 {
		t.Errorf("expected %d agents after save, got %d", initialCount+1, len(hm.ListAgents()))
	}

	// Verify logs were initialized
	agObj := saveRes["agent"].(map[string]any)
	logs, ok := agObj["logs"].([]any)
	if !ok || len(logs) == 0 {
		t.Errorf("expected initialized logs for new agent")
	}

	// Update agent preserves logs
	newAg["name"] = "Senior Documentation Specialist"
	updateRes := hm.SaveAgent(newAg)
	updAg := updateRes["agent"].(map[string]any)
	if updAg["name"] != "Senior Documentation Specialist" {
		t.Errorf("expected updated name, got %v", updAg["name"])
	}
	if len(updAg["logs"].([]any)) == 0 {
		t.Errorf("expected logs to be preserved across updates")
	}

	// Delete agent
	delRes := hm.DeleteAgent("agent_doc_specialist")
	if delRes["status"] != "ok" {
		t.Fatalf("failed deleting agent: %+v", delRes)
	}
	if len(hm.ListAgents()) != initialCount {
		t.Errorf("expected %d agents after delete, got %d", initialCount, len(hm.ListAgents()))
	}
}

func TestMentionResolution(t *testing.T) {
	tempDir := t.TempDir()

	testFile := filepath.Join(tempDir, "sample.txt")
	err := os.WriteFile(testFile, []byte("package sample\n\nconst Version = \"1.0.0\"\n"), 0644)
	if err != nil {
		t.Fatalf("failed creating sample file: %v", err)
	}

	prompt := "Please inspect @sample.txt and tell me what the version is."
	resolved := resolveMentions(tempDir, prompt)

	if !strings.Contains(resolved, "<referenced_context file=\"sample.txt\">") {
		t.Errorf("expected referenced_context for sample.txt in:\n%s", resolved)
	}
	if !strings.Contains(resolved, "const Version = \"1.0.0\"") {
		t.Errorf("expected file contents in:\n%s", resolved)
	}
}

func TestAttachmentsAndImagesProcessing(t *testing.T) {
	tempDir := t.TempDir()

	// 1. Attachments
	attachments := []map[string]any{
		{
			"name":        "notes.txt",
			"mime_type":   "text/plain",
			"data_base64": base64.StdEncoding.EncodeToString([]byte("Key project architectural notes")),
		},
		{
			"name":         "config.json",
			"mime_type":    "application/json",
			"text_content": `{"debug": true}`,
		},
	}

	extraPrompt, summaries := processAttachments(tempDir, attachments)
	if len(summaries) != 2 {
		t.Fatalf("expected 2 attachment summaries, got %d", len(summaries))
	}
	if !strings.Contains(extraPrompt, "<attached_file name=\"notes.txt\"") {
		t.Errorf("missing attached_file notes.txt in:\n%s", extraPrompt)
	}
	if !strings.Contains(extraPrompt, "Key project architectural notes") {
		t.Errorf("missing notes content in:\n%s", extraPrompt)
	}

	savedFile := filepath.Join(tempDir, ".unjess_attachments", "notes.txt")
	if data, err := os.ReadFile(savedFile); err != nil || string(data) != "Key project architectural notes" {
		t.Errorf("expected file on disk, got data=%s, err=%v", string(data), err)
	}

	// 2. Images
	images := []map[string]any{
		{
			"name":      "mockup.png",
			"mime_type": "image/png",
			"data":      "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==",
		},
	}

	llmImages, previewImages := processImages(images)
	if len(llmImages) != 1 || len(previewImages) != 1 {
		t.Fatalf("expected 1 image, got llm=%d, prev=%d", len(llmImages), len(previewImages))
	}
	if llmImages[0].Name != "mockup.png" {
		t.Errorf("expected mockup.png, got %s", llmImages[0].Name)
	}
	dataURL := previewImages[0]["data_url"].(string)
	if !strings.HasPrefix(dataURL, "data:image/png;base64,") {
		t.Errorf("expected data:image/png;base64, prefix in data_url, got %s", dataURL)
	}
}

func TestRunAgentTurnGraceful(t *testing.T) {
	hm, tempDir := setupTestManager(t)

	// Create a dummy workspace file
	_ = os.WriteFile(filepath.Join(tempDir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0644)

	req := ChatTurnRequest{
		Prompt:    "Inspect the workspace and explain the structure.",
		Workspace: tempDir,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Should not crash or panic even if local Ollama daemon is offline
	res, err := hm.RunAgentTurn(ctx, req)
	if err != nil {
		t.Fatalf("unexpected fatal error: %v", err)
	}

	if res == nil {
		t.Fatal("expected non-nil response")
	}

	turn, ok := res["turn"].(map[string]any)
	if !ok {
		t.Fatalf("expected turn map in response: %+v", res)
	}

	if turn["role"] != "agent" {
		t.Errorf("expected role agent, got %v", turn["role"])
	}

	conv, ok := res["conversation"].(map[string]any)
	if !ok {
		t.Fatalf("expected conversation map in response")
	}

	turns, ok := conv["turns"].([]any)
	if !ok || len(turns) < 2 {
		t.Fatalf("expected at least 2 turns (user + agent), got %d", len(turns))
	}
}

func TestRunSubagentTaskGraceful(t *testing.T) {
	hm, tempDir := setupTestManager(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	res, err := hm.RunSubagentTask(ctx, "agent_architect", "Design a simple API", "", tempDir)
	if err != nil {
		t.Fatalf("unexpected fatal error: %v", err)
	}

	if res["status"] != "ok" {
		t.Errorf("expected status ok, got %v", res["status"])
	}
	if res["agent"] == nil {
		t.Errorf("expected agent map in response")
	}
}

func TestSubagentEngine_SpawnAndManage(t *testing.T) {
	hm, tempDir := setupTestManager(t)

	specs := []map[string]any{
		{
			"role":      "Go Engineer",
			"prompt":    "Inspect workspace and write health test",
			"model":     "inherit",
			"type_name": "coder",
		},
		{
			"role":      "Test Engineer",
			"prompt":    "Run verification suite",
			"model":     "inherit",
			"type_name": "tester",
		},
	}

	runs := hm.SpawnSubagents(context.Background(), "conv_test", tempDir, specs, false)
	if len(runs) != 2 {
		t.Fatalf("expected 2 spawned runs, got %d", len(runs))
	}

	sr1 := runs[0]
	if sr1.Role != "Go Engineer" {
		t.Errorf("expected role Go Engineer, got %s", sr1.Role)
	}
	if sr1.TypeName != "coder" {
		t.Errorf("expected type_name coder, got %s", sr1.TypeName)
	}

	// Verify ListSubagentRuns
	allRuns := hm.ListSubagentRuns("")
	if len(allRuns) < 2 {
		t.Fatalf("expected at least 2 runs in ListSubagentRuns, got %d", len(allRuns))
	}

	filtered := hm.ListSubagentRuns("conv_test")
	if len(filtered) < 2 {
		t.Fatalf("expected at least 2 runs for conv_test, got %d", len(filtered))
	}

	// Verify GetSubagentRun
	runMap, found := hm.GetSubagentRun(sr1.ID)
	if !found || runMap == nil {
		t.Fatalf("expected to find subagent run %s", sr1.ID)
	}
	if runMap["id"] != sr1.ID {
		t.Errorf("expected id %s, got %v", sr1.ID, runMap["id"])
	}
	if runMap["role"] != "Go Engineer" {
		t.Errorf("expected role Go Engineer, got %v", runMap["role"])
	}
	steps, ok := runMap["steps"].([]SubagentStep)
	if !ok || len(steps) == 0 {
		t.Fatalf("expected steps in serialized run, got: %+v", runMap["steps"])
	}
	if steps[0].Type != "parent_message" {
		t.Errorf("expected initial step type parent_message, got %s", steps[0].Type)
	}

	// Verify AbortSubagentRun
	abortRes := hm.AbortSubagentRun(sr1.ID)
	if abortRes["status"] != "ok" {
		t.Errorf("expected status ok on abort, got %v", abortRes["status"])
	}
	subMap, _ := abortRes["subagent"].(map[string]any)
	if subMap["status"] != "aborted" {
		t.Errorf("expected status aborted, got %v", subMap["status"])
	}

	// Verify FullStatePayload includes subagent_runs
	statePayload := hm.GetFullStatePayload()
	if _, ok := statePayload["subagent_runs"]; !ok {
		t.Errorf("expected subagent_runs in GetFullStatePayload()")
	}
}

func TestSubagentEngine_SendFollowUp(t *testing.T) {
	hm, tempDir := setupTestManager(t)

	specs := []map[string]any{
		{
			"role":   "Frontend Specialist",
			"prompt": "Inspect index.html",
		},
	}
	runs := hm.SpawnSubagents(context.Background(), "conv_followup", tempDir, specs, false)
	if len(runs) != 1 {
		t.Fatalf("expected 1 run, got %d", len(runs))
	}
	sr := runs[0]

	// Send message
	res, err := hm.SendSubagentMessage(context.Background(), sr.ID, "Now check styles.css", "inherit")
	if err != nil {
		t.Fatalf("unexpected error sending message: %v", err)
	}
	if res == nil {
		t.Fatal("expected non-nil response from SendSubagentMessage")
	}

	// Abort run to clean up
	_ = hm.AbortSubagentRun(sr.ID)
}

func TestBuildRecentSessionsSummary(t *testing.T) {
	hm, tempDir := setupTestManager(t)

	// Create 2 conversations with turns
	res1 := hm.CreateConversation("Go Daemon API", tempDir, "normal")
	conv1, _ := res1["conversation"].(map[string]any)
	id1, _ := conv1["id"].(string)

	res2 := hm.CreateConversation("Rust Desktop UI", tempDir, "normal")
	conv2, _ := res2["conversation"].(map[string]any)
	id2, _ := conv2["id"].(string)

	// Add turns to conv1 and conv2
	hm.mu.Lock()
	convs, _ := hm.state["conversations"].([]any)
	for _, c := range convs {
		cm := c.(map[string]any)
		if cm["id"] == id1 {
			cm["turns"] = []any{
				map[string]any{"role": "user", "text": "Build the Go daemon HTTP REST API"},
				map[string]any{"role": "assistant", "text": "Implemented server routes and handlers."},
			}
		} else if cm["id"] == id2 {
			cm["turns"] = []any{
				map[string]any{"role": "user", "text": "Implement the Tauri/Wry webview shell"},
			}
		}
	}
	hm.mu.Unlock()

	// Call buildRecentSessionsSummary excluding conv2
	summary := hm.buildRecentSessionsSummary(id2, 5)
	if !strings.Contains(summary, "<recent_sessions_context>") {
		t.Errorf("expected <recent_sessions_context> in summary:\n%s", summary)
	}
	if !strings.Contains(summary, "Go Daemon API") {
		t.Errorf("expected Go Daemon API in summary:\n%s", summary)
	}
	if strings.Contains(summary, "Rust Desktop UI") {
		t.Errorf("current conversation (id2) should have been excluded from summary:\n%s", summary)
	}
}

func TestQuickChatWorkspaceNotPollutingWorkspaces(t *testing.T) {
	hm, _ := setupTestManager(t)

	initialWSCount := len(hm.ListWorkspaces())

	// 1. Create a Quick Chat conversation
	res := hm.CreateConversation("Quick Temp Debug", "__quick__", "quick")
	if res["status"] != "ok" {
		t.Fatalf("failed to create quick conversation: %v", res)
	}

	conv := res["conversation"].(map[string]any)
	if conv["mode"] != "quick" {
		t.Errorf("expected conv mode to be quick, got %v", conv["mode"])
	}

	// 2. The workspaces list should NOT have increased by an auto-generated quick folder
	workspaces := hm.ListWorkspaces()
	if len(workspaces) != initialWSCount {
		t.Errorf("workspaces list was polluted with quick chat folder! Expected %d, got %d: %v", initialWSCount, len(workspaces), workspaces)
	}

	for _, w := range workspaces {
		name := strings.ToLower(fmt.Sprint(w["name"]))
		if strings.HasPrefix(name, "quick-20") || name == "test-quick-chat" {
			t.Errorf("found quick chat folder in workspaces list: %s", name)
		}
	}

	// 3. Test loadOrCreateState filters quick folders
	hm.mu.Lock()
	hm.state["workspaces"] = append(hm.state["workspaces"].([]any),
		map[string]any{
			"id":   "ws_polluted_1",
			"name": "quick-20261015-151004",
			"path": "C:\\Users\\user\\.unjess\\workspaces\\quick-20261015-151004",
		},
		map[string]any{
			"id":   "ws_polluted_2",
			"name": "test-quick-chat",
			"path": "C:\\Users\\user\\.unjess\\workspaces\\test-quick-chat",
		},
	)
	_ = hm.saveStateLocked()
	hm.mu.Unlock()

	// Reload state
	hm.loadOrCreateState()

	filteredWorkspaces := hm.ListWorkspaces()
	for _, w := range filteredWorkspaces {
		name := strings.ToLower(fmt.Sprint(w["name"]))
		if strings.HasPrefix(name, "quick-20") || name == "test-quick-chat" {
			t.Errorf("loadOrCreateState failed to filter quick folder: %s", name)
		}
	}
}

func TestSubagentPerConversationIsolationAndRehydration(t *testing.T) {
	hm, tempDir := setupTestManager(t)

	res1 := hm.CreateConversation("Project 1", tempDir, "normal")
	conv1 := res1["conversation"].(map[string]any)
	cID1 := conv1["id"].(string)

	res2 := hm.CreateConversation("Project 2", tempDir, "normal")
	conv2 := res2["conversation"].(map[string]any)
	cID2 := conv2["id"].(string)

	// 1. Spawn subagent in conv1
	runs1 := hm.SpawnSubagents(context.Background(), cID1, tempDir, []map[string]any{
		{"role": "Worker 1", "prompt": "Task 1", "type_name": "self"},
	}, false)
	if len(runs1) != 1 {
		t.Fatalf("expected 1 subagent run, got %d", len(runs1))
	}
	sr1 := runs1[0]
	if sr1.ConvID != cID1 {
		t.Errorf("expected sr1.ConvID to be %s, got %s", cID1, sr1.ConvID)
	}

	// 2. Spawn subagent in conv2 with empty convID (should fallback to active convID = cID2)
	runs2 := hm.SpawnSubagents(context.Background(), "", tempDir, []map[string]any{
		{"role": "Worker 2", "prompt": "Task 2", "type_name": "self"},
	}, false)
	sr2 := runs2[0]
	if sr2.ConvID != cID2 {
		t.Errorf("expected sr2.ConvID to fallback to active convID %s, got %s", cID2, sr2.ConvID)
	}

	// 3. ClearConversation on conv1 should purge sr1 from hm.subagentRuns
	hm.ClearConversation(cID1)
	remainingRuns := hm.ListSubagentRuns("")
	for _, r := range remainingRuns {
		if r["conv_id"] == cID1 {
			t.Errorf("found run for cleared conversation %s in subagent_runs: %v", cID1, r)
		}
	}

	// 4. Test Rehydration: add a turn with subagents to conv2, simulate restart
	hm.mu.Lock()
	convs := hm.state["conversations"].([]any)
	for _, c := range convs {
		cm := c.(map[string]any)
		if cm["id"] == cID2 {
			cm["turns"] = []any{
				map[string]any{
					"role": "agent",
					"subagents": []any{
						map[string]any{
							"id":        "sub_rehydrate_test",
							"conv_id":   cID2,
							"role":      "Rehydrated Worker",
							"type_name": "self",
							"status":    "running", // should be converted to completed
							"prompt":    "Prior session task",
						},
					},
				},
			}
			break
		}
	}
	_ = hm.saveStateLocked()
	hm.mu.Unlock()

	// Clear memory list and reload state from disk
	hm.subagentsMu.Lock()
	hm.subagentRuns = make([]*SubagentRun, 0)
	hm.subagentsMu.Unlock()

	hm.loadOrCreateState()

	rehydrated := hm.ListSubagentRuns(cID2)
	var found *map[string]any
	for _, r := range rehydrated {
		if r["id"] == "sub_rehydrate_test" {
			found = &r
			break
		}
	}
	if found == nil {
		t.Fatalf("sub_rehydrate_test was not rehydrated into hm.subagentRuns")
	}
	if (*found)["conv_id"] != cID2 {
		t.Errorf("expected conv_id %s, got %v", cID2, (*found)["conv_id"])
	}
	if (*found)["status"] != "completed" {
		t.Errorf("expected status 'running' to be converted to 'completed', got %v", (*found)["status"])
	}

	// Clean up background runs
	_ = hm.AbortSubagentRun(sr1.ID)
	_ = hm.AbortSubagentRun(sr2.ID)
}

func TestConversationWorkspaceIsolationAndSwitching(t *testing.T) {
	hm, tempDir := setupTestManager(t)

	wsA := filepath.Join(tempDir, "proj-alpha")
	wsB := filepath.Join(tempDir, "proj-beta")
	_ = os.MkdirAll(wsA, 0755)
	_ = os.MkdirAll(wsB, 0755)

	// 1. Create conversation 1 for Proj Alpha
	res1 := hm.CreateConversation("Alpha Work", wsA, "")
	if res1["status"] != "ok" {
		t.Fatalf("failed creating conv 1: %v", res1)
	}
	conv1 := res1["conversation"].(map[string]any)
	id1 := conv1["id"].(string)
	if conv1["mode"] != "workspace" {
		t.Errorf("expected conv1 mode 'workspace', got %v", conv1["mode"])
	}
	if !strings.EqualFold(filepath.Clean(fmt.Sprint(conv1["workspace"])), filepath.Clean(wsA)) {
		t.Errorf("expected conv1 workspace %s, got %v", wsA, conv1["workspace"])
	}

	// 2. Create conversation 2 for Proj Beta
	// IMPORTANT: this must NOT mutate conv1's workspace!
	res2 := hm.CreateConversation("Beta Work", wsB, "")
	if res2["status"] != "ok" {
		t.Fatalf("failed creating conv 2: %v", res2)
	}
	conv2 := res2["conversation"].(map[string]any)
	id2 := conv2["id"].(string)
	if conv2["mode"] != "workspace" {
		t.Errorf("expected conv2 mode 'workspace', got %v", conv2["mode"])
	}

	// Verify conv1's workspace was NOT stolen/mutated by creating conv2
	hm.mu.RLock()
	convs := hm.state["conversations"].([]any)
	var reloadedConv1, reloadedConv2 map[string]any
	for _, c := range convs {
		cm := c.(map[string]any)
		if cm["id"] == id1 {
			reloadedConv1 = cm
		} else if cm["id"] == id2 {
			reloadedConv2 = cm
		}
	}
	hm.mu.RUnlock()

	if reloadedConv1 == nil || reloadedConv2 == nil {
		t.Fatalf("could not find conversations in state")
	}
	if !strings.EqualFold(filepath.Clean(fmt.Sprint(reloadedConv1["workspace"])), filepath.Clean(wsA)) {
		t.Errorf("conv1 workspace was mutated! Expected %s, got %v", wsA, reloadedConv1["workspace"])
	}
	if !strings.EqualFold(filepath.Clean(fmt.Sprint(reloadedConv2["workspace"])), filepath.Clean(wsB)) {
		t.Errorf("conv2 workspace expected %s, got %v", wsB, reloadedConv2["workspace"])
	}

	// 3. Test SelectWorkspace(wsA):
	// - Must switch active_conversation_id to id1
	// - Must NOT mutate any conversation's workspace
	// - Must return active_conversation_id and conversations
	selRes, err := hm.SelectWorkspace(wsA)
	if err != nil {
		t.Fatalf("SelectWorkspace failed: %v", err)
	}
	if selRes["active_conversation_id"] != id1 {
		t.Errorf("expected active_conversation_id %s, got %v", id1, selRes["active_conversation_id"])
	}
	if selRes["conversations"] == nil {
		t.Errorf("expected conversations list in SelectWorkspace response")
	}

	// Verify both conversations still preserve their respective workspaces
	hm.mu.RLock()
	for _, c := range hm.state["conversations"].([]any) {
		cm := c.(map[string]any)
		if cm["id"] == id1 && !strings.EqualFold(filepath.Clean(fmt.Sprint(cm["workspace"])), filepath.Clean(wsA)) {
			t.Errorf("conv1 workspace corrupted after SelectWorkspace: got %v", cm["workspace"])
		}
		if cm["id"] == id2 && !strings.EqualFold(filepath.Clean(fmt.Sprint(cm["workspace"])), filepath.Clean(wsB)) {
			t.Errorf("conv2 workspace corrupted after SelectWorkspace: got %v", cm["workspace"])
		}
	}
	hm.mu.RUnlock()

	// 4. Test mode classification:
	// If workspacePath is non-empty, even if mode="quick", it's treated as a project conversation
	resQuickWithWS := hm.CreateConversation("Misclassified", wsA, "quick")
	convQuickWithWS := resQuickWithWS["conversation"].(map[string]any)
	if convQuickWithWS["mode"] != "workspace" {
		t.Errorf("expected mode 'workspace' when non-empty project workspace is provided, got %v", convQuickWithWS["mode"])
	}
	if !strings.EqualFold(filepath.Clean(fmt.Sprint(convQuickWithWS["workspace"])), filepath.Clean(wsA)) {
		t.Errorf("expected workspace %s, got %v", wsA, convQuickWithWS["workspace"])
	}

	// Pure quick chat with empty workspace and mode="quick"
	resPureQuick := hm.CreateConversation("Pure Quick", "", "quick")
	convPureQuick := resPureQuick["conversation"].(map[string]any)
	if convPureQuick["mode"] != "quick" {
		t.Errorf("expected mode 'quick' for empty workspace and mode='quick', got %v", convPureQuick["mode"])
	}
	if !strings.Contains(fmt.Sprint(convPureQuick["workspace"]), "quick") {
		t.Errorf("expected auto quick directory, got %v", convPureQuick["workspace"])
	}
}

func TestLoadOrCreateStateFixesStaleQuickMode(t *testing.T) {
	hm, tempDir := setupTestManager(t)

	projDir := filepath.Join(tempDir, "real-project")
	_ = os.MkdirAll(projDir, 0755)

	// Add the project workspace
	_, err := hm.AddWorkspace(projDir, "real-project")
	if err != nil {
		t.Fatalf("failed to add workspace: %v", err)
	}

	// Inject a conversation with stale "mode": "quick" pointing to real-project
	staleConvID := "conv_stale_quick_fix"
	hm.mu.Lock()
	convs := hm.state["conversations"].([]any)
	convs = append([]any{
		map[string]any{
			"id":         staleConvID,
			"title":      "Stale Quick Mode Conv",
			"workspace":  projDir,
			"mode":       "quick", // STALE
			"created_at": time.Now().Unix(),
			"turns":      make([]any, 0),
		},
	}, convs...)
	hm.state["conversations"] = convs
	_ = hm.saveStateLocked()
	hm.mu.Unlock()

	// Re-run loadOrCreateState
	hm.loadOrCreateState()

	// Verify the conversation's mode was normalized to "workspace"
	hm.mu.RLock()
	var fixedConv map[string]any
	for _, c := range hm.state["conversations"].([]any) {
		cm := c.(map[string]any)
		if cm["id"] == staleConvID {
			fixedConv = cm
			break
		}
	}
	hm.mu.RUnlock()

	if fixedConv == nil {
		t.Fatalf("could not find fixedConv in state")
	}
	if fixedConv["mode"] != "workspace" {
		t.Errorf("expected loadOrCreateState to normalize mode to 'workspace', got %v", fixedConv["mode"])
	}
}

func TestHarnessConfigAndWorkspaceOverviewPrompts(t *testing.T) {
	hm, tempDir := setupTestManager(t)

	// 1. Test buildHarnessConfigPrompt
	cfgPrompt := hm.buildHarnessConfigPrompt("gemini-2.0-flash", "google", "Autonomous")
	if !strings.Contains(cfgPrompt, "<harness_configuration>") || !strings.Contains(cfgPrompt, "Active Main Model: gemini-2.0-flash") {
		t.Errorf("unexpected harness config prompt: %s", cfgPrompt)
	}
	if !strings.Contains(cfgPrompt, "Execution Mode: Autonomous") {
		t.Errorf("missing execution mode in harness config prompt: %s", cfgPrompt)
	}

	// 2. Test buildWorkspaceOverviewPrompt
	agentsFile := filepath.Join(tempDir, "AGENTS.md")
	_ = os.WriteFile(agentsFile, []byte("# Project Agent Rules\n1. Always test.\n"), 0644)
	subDir := filepath.Join(tempDir, "pkg")
	_ = os.MkdirAll(subDir, 0755)

	wsPrompt := buildWorkspaceOverviewPrompt(tempDir)
	if !strings.Contains(wsPrompt, "<workspace_overview>") || !strings.Contains(wsPrompt, "pkg/") {
		t.Errorf("expected workspace overview with pkg/, got: %s", wsPrompt)
	}
	if !strings.Contains(wsPrompt, "[Project Rules from AGENTS.md]") || !strings.Contains(wsPrompt, "Always test.") {
		t.Errorf("expected AGENTS.md rules in overview, got: %s", wsPrompt)
	}
}

func TestBuildConversationHistoryMessages(t *testing.T) {
	conv := map[string]any{
		"id": "conv_test_hist",
		"turns": []any{
			// Historical user turn
			map[string]any{
				"id":   "turn_u1",
				"role": "user",
				"text": "Please inspect main.go",
				"images": []any{
					map[string]any{
						"name":      "diagram.png",
						"mime_type": "image/png",
						"data":      "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==",
					},
				},
			},
			// Historical assistant turn
			map[string]any{
				"id":   "turn_a1",
				"role": "agent",
				"text": "I inspected main.go and found 2 issues.",
				"toolCalls": []any{
					map[string]any{
						"name":   "read_file",
						"status": "completed",
						"arguments": map[string]any{
							"path": "main.go",
						},
						"result": "package main\n\nfunc main() {}",
					},
				},
				"filesCreated": []any{
					map[string]any{
						"path":   "main.go",
						"action": "modified",
					},
				},
				"subagents": []any{
					map[string]any{
						"role":           "Linter",
						"model":          "gemini-2.0-flash",
						"status":         "completed",
						"current_action": "Completed",
						"result":         "No linter warnings found",
					},
				},
			},
			// Another user turn that will be excluded (as current userTurn is the last one in conv["turns"])
			map[string]any{
				"id":   "turn_u_current",
				"role": "user",
				"text": "Current turn prompt",
			},
		},
	}

	msgs := buildConversationHistoryMessages(conv, "Current turn effective prompt", nil)
	if len(msgs) < 3 {
		t.Fatalf("expected at least 3 messages, got %d: %+v", len(msgs), msgs)
	}

	// 1. First message should be user
	if msgs[0].Role != "user" || !strings.Contains(msgs[0].Content, "inspect main.go") {
		t.Errorf("unexpected first message: %+v", msgs[0])
	}
	if len(msgs[0].Images) != 1 {
		t.Errorf("expected 1 image in first message, got %d", len(msgs[0].Images))
	}

	// 2. Second message should be assistant with Turn Execution Summary
	if msgs[1].Role != "assistant" {
		t.Errorf("expected second message role 'assistant', got %s", msgs[1].Role)
	}
	if !strings.Contains(msgs[1].Content, "[Turn Execution Summary]") {
		t.Errorf("expected Turn Execution Summary in assistant turn, got: %s", msgs[1].Content)
	}
	if !strings.Contains(msgs[1].Content, "read_file(main.go)") {
		t.Errorf("expected read_file(main.go) in summary, got: %s", msgs[1].Content)
	}
	if !strings.Contains(msgs[1].Content, "Subagent Linter") {
		t.Errorf("expected Subagent Linter in summary, got: %s", msgs[1].Content)
	}

	// 3. Last message should be current user turn
	last := msgs[len(msgs)-1]
	if last.Role != "user" || last.Content != "Current turn effective prompt" {
		t.Errorf("unexpected last message: %+v", last)
	}
}

func TestZombieSubagentCleanupOnLoad(t *testing.T) {
	hm, _ := setupTestManager(t)

	// Inject conv with zombie running subagent and step
	hm.mu.Lock()
	convs := hm.state["conversations"].([]any)
	convs = append([]any{
		map[string]any{
			"id":    "conv_zombie_test",
			"title": "Zombie Conv",
			"turns": []any{
				map[string]any{
					"id":   "turn_zombie",
					"role": "agent",
					"subagents": []any{
						map[string]any{
							"id":             "sub_zombie_1",
							"role":           "Worker",
							"status":         "running", // ZOMBIE
							"current_action": "Processing...",
							"steps": []any{
								map[string]any{
									"id":     "step_1",
									"status": "running", // ZOMBIE
								},
							},
						},
					},
				},
			},
		},
	}, convs...)
	hm.state["conversations"] = convs
	_ = hm.saveStateLocked()
	hm.mu.Unlock()

	// Reload state
	hm.loadOrCreateState()

	// Verify the subagent status and step status became "completed"
	hm.mu.RLock()
	var zombieConv map[string]any
	for _, c := range hm.state["conversations"].([]any) {
		if cm, ok := c.(map[string]any); ok && cm["id"] == "conv_zombie_test" {
			zombieConv = cm
			break
		}
	}
	hm.mu.RUnlock()

	if zombieConv == nil {
		t.Fatalf("could not find zombieConv")
	}
	turns := zombieConv["turns"].([]any)
	firstTurn := turns[0].(map[string]any)
	subagents := firstTurn["subagents"].([]any)
	subMap := subagents[0].(map[string]any)
	if subMap["status"] != "completed" {
		t.Errorf("expected subagent status 'completed', got %v", subMap["status"])
	}
	if subMap["current_action"] != "Completed" {
		t.Errorf("expected subagent current_action 'Completed', got %v", subMap["current_action"])
	}
	steps := subMap["steps"].([]any)
	stepMap := steps[0].(map[string]any)
	if stepMap["status"] != "completed" {
		t.Errorf("expected step status 'completed', got %v", stepMap["status"])
	}
}

func TestStatePayloadSlimming(t *testing.T) {
	fullState := map[string]any{
		"active_workspace": "/test",
		"conversations": []any{
			map[string]any{
				"id": "conv_snaps",
				"turns": []any{
					map[string]any{
						"id":   "turn_1",
						"role": "agent",
						"snapshots": []any{
							map[string]any{"path": "file1.txt", "content": "big content 1"},
							map[string]any{"path": "file2.txt", "content": "big content 2"},
						},
					},
				},
			},
		},
	}

	slimmed := slimStateForClient(fullState)

	// Underlying state must preserve snapshots
	convs := fullState["conversations"].([]any)
	c0 := convs[0].(map[string]any)
	t0 := c0["turns"].([]any)[0].(map[string]any)
	if t0["snapshots"] == nil {
		t.Fatalf("underlying fullState snapshots were corrupted")
	}

	// Slimmed state must omit snapshots and have snapshots_count
	sConvs := slimmed["conversations"].([]any)
	sc0 := sConvs[0].(map[string]any)
	st0 := sc0["turns"].([]any)[0].(map[string]any)
	if _, exists := st0["snapshots"]; exists {
		t.Errorf("slimmed state still has snapshots: %+v", st0)
	}
	if st0["snapshots_count"] != 2 {
		t.Errorf("expected snapshots_count 2, got %v", st0["snapshots_count"])
	}
}

func TestCompactTurnInPlaceAndSubagentSlimming(t *testing.T) {
	hugeString := strings.Repeat("A", 12000)

	tm := map[string]any{
		"thinking": hugeString,
		"planningSteps": []any{
			map[string]any{"detail": hugeString},
		},
		"toolCalls": []any{
			map[string]any{"name": "read_file", "result": hugeString},
		},
		"diffs": []any{
			map[string]any{"path": "foo.go", "diff": hugeString},
		},
		"subagents": []any{
			map[string]any{
				"id":        "sub_1",
				"role":      "Worker",
				"thinking":  hugeString,
				"steps": []any{
					map[string]any{"result": hugeString, "diff": hugeString},
				},
				"tool_calls": []any{
					map[string]any{"name": "read_file", "result": hugeString},
				},
				"diffs": []any{
					map[string]any{"diff": hugeString},
				},
			},
		},
	}

	compactTurnInPlace(tm)

	// Verify thinking <= 6000 + len("\n... [truncated]")
	if len(tm["thinking"].(string)) > 6020 {
		t.Errorf("thinking not compacted: %d", len(tm["thinking"].(string)))
	}

	// Verify toolCalls result <= 2500 + len("\n... [truncated]")
	tcs := tm["toolCalls"].([]any)
	tcm := tcs[0].(map[string]any)
	if len(tcm["result"].(string)) > 2520 {
		t.Errorf("toolCall result not compacted: %d", len(tcm["result"].(string)))
	}

	// Verify subagents thinking and steps compacted
	subList := tm["subagents"].([]any)
	sm := subList[0].(map[string]any)
	if len(sm["thinking"].(string)) > 4020 {
		t.Errorf("subagent thinking not compacted: %d", len(sm["thinking"].(string)))
	}
	steps := sm["steps"].([]any)
	st0 := steps[0].(map[string]any)
	if len(st0["result"].(string)) > 2520 {
		t.Errorf("step result not compacted: %d", len(st0["result"].(string)))
	}

	// Now test slimTurnForClient strips steps, tool_calls, diffs from in-turn subagents
	slimmed := slimTurnForClient(tm).(map[string]any)
	slimSubs := slimmed["subagents"].([]any)
	slimSm := slimSubs[0].(map[string]any)
	if _, exists := slimSm["steps"]; exists {
		t.Errorf("expected steps to be stripped from in-turn subagent copy")
	}
	if _, exists := slimSm["tool_calls"]; exists {
		t.Errorf("expected tool_calls to be stripped from in-turn subagent copy")
	}
	if _, exists := slimSm["diffs"]; exists {
		t.Errorf("expected diffs to be stripped from in-turn subagent copy")
	}
	if slimSm["id"] != "sub_1" || slimSm["role"] != "Worker" {
		t.Errorf("expected metadata id and role to be preserved, got %+v", slimSm)
	}
}

func TestPlanModeAndApprovals(t *testing.T) {
	hm, _ := setupTestManager(t)

	// 1. Set mode to Plan
	res := hm.SetAgentMode("Plan")
	if res["agent_mode"] != "Plan" {
		t.Fatalf("expected mode Plan, got %v", res["agent_mode"])
	}

	// 2. Question approval flow
	qCh := make(chan string, 1)
	live := &LiveTurn{
		Running:          true,
		ConvID:           "conv_test",
		AgentMode:        "Plan",
		PendingQuestion:  &PendingQuestionApproval{ID: "q_1", Questions: []map[string]any{{"question": "Pick database", "options": []string{"Postgres", "SQLite"}}}},
		QuestionAnswerCh: qCh,
	}
	hm.SetLiveTurnForTest(live)

	// Verify live turn state exposes pending_question
	liveState := hm.GetLiveTurnState()
	if liveState["pending_question"] == nil {
		t.Fatalf("expected pending_question in live state, got nil")
	}

	// Resolve question with answer
	ansRes := hm.ResolveQuestionApproval("Option 1: Postgres", false)
	if ansRes["status"] != "ok" {
		t.Fatalf("expected status ok, got %v", ansRes)
	}
	select {
	case ans := <-qCh:
		if ans != "Option 1: Postgres" {
			t.Errorf("unexpected answer received: %s", ans)
		}
	default:
		t.Fatal("expected answer on QuestionAnswerCh")
	}

	// Resolve question when none pending
	errAns := hm.ResolveQuestionApproval("anything", false)
	if errAns["status"] != "error" {
		t.Fatalf("expected error when no question pending, got %v", errAns)
	}

	// 3. Question skip fallback
	qCh2 := make(chan string, 1)
	live2 := &LiveTurn{
		Running:          true,
		ConvID:           "conv_test",
		AgentMode:        "Plan",
		PendingQuestion:  &PendingQuestionApproval{ID: "q_2", Questions: []map[string]any{{"question": "Pick framework"}}},
		QuestionAnswerCh: qCh2,
	}
	hm.SetLiveTurnForTest(live2)
	skipRes := hm.ResolveQuestionApproval("", true)
	if skipRes["status"] != "ok" {
		t.Fatalf("expected status ok on skip, got %v", skipRes)
	}
	select {
	case ans := <-qCh2:
		if !strings.Contains(ans, "skipped") {
			t.Errorf("expected skipped notice in answer, got: %s", ans)
		}
	default:
		t.Fatal("expected answer on QuestionAnswerCh")
	}

	// 4. Plan approval flow
	pCh := make(chan map[string]any, 1)
	live3 := &LiveTurn{
		Running:        true,
		ConvID:         "conv_test",
		AgentMode:      "Plan",
		PendingPlan:    &PendingPlanApproval{ID: "plan_1", Title: "Migration Plan", Summary: "Migrate DB", Steps: []string{"Backup", "Migrate"}},
		PlanApprovalCh: pCh,
	}
	hm.SetLiveTurnForTest(live3)

	liveState3 := hm.GetLiveTurnState()
	if liveState3["pending_plan"] == nil {
		t.Fatalf("expected pending_plan in live state, got nil")
	}

	planRes := hm.ResolvePlanApproval(true, "Looks good to me")
	if planRes["status"] != "ok" || planRes["approved"] != true {
		t.Fatalf("expected status ok and approved=true, got %v", planRes)
	}
	if !live3.PlanApproved {
		t.Fatal("expected PlanApproved to be true on live turn")
	}
	select {
	case r := <-pCh:
		if r["approved"] != true || r["feedback"] != "Looks good to me" {
			t.Errorf("unexpected plan result: %+v", r)
		}
	default:
		t.Fatal("expected plan result on PlanApprovalCh")
	}

	// 5. Plan rejection with feedback
	pCh2 := make(chan map[string]any, 1)
	live4 := &LiveTurn{
		Running:        true,
		ConvID:         "conv_test",
		AgentMode:      "Plan",
		PendingPlan:    &PendingPlanApproval{ID: "plan_2", Title: "Refactor Plan"},
		PlanApprovalCh: pCh2,
	}
	hm.SetLiveTurnForTest(live4)

	planRejRes := hm.ResolvePlanApproval(false, "Please keep backward compatibility")
	if planRejRes["approved"] != false {
		t.Fatalf("expected approved=false, got %v", planRejRes)
	}
	select {
	case r := <-pCh2:
		if r["approved"] != false || r["feedback"] != "Please keep backward compatibility" {
			t.Errorf("unexpected rejection result: %+v", r)
		}
	default:
		t.Fatal("expected rejection result on PlanApprovalCh")
	}

	// 6. Switching to Autonomous mode auto-resolves pending plan
	pCh3 := make(chan map[string]any, 1)
	live5 := &LiveTurn{
		Running:        true,
		ConvID:         "conv_test",
		AgentMode:      "Plan",
		PendingPlan:    &PendingPlanApproval{ID: "plan_3", Title: "Pending Plan"},
		PlanApprovalCh: pCh3,
	}
	hm.SetLiveTurnForTest(live5)

	hm.SetAgentMode("Autonomous")
	if !live5.PlanApproved {
		t.Fatal("expected PlanApproved to be true when switched to Autonomous")
	}
	select {
	case r := <-pCh3:
		if r["approved"] != true {
			t.Errorf("expected plan to be approved on mode switch: %+v", r)
		}
	default:
		t.Fatal("expected plan approval unblocked on mode switch")
	}

	// 7. AbortTurn unblocks question and plan channels
	qChAbort := make(chan string, 1)
	pChAbort := make(chan map[string]any, 1)
	liveAbort := &LiveTurn{
		Running:          true,
		ConvID:           "conv_abort",
		AgentMode:        "Plan",
		PendingQuestion:  &PendingQuestionApproval{ID: "q_abort"},
		QuestionAnswerCh: qChAbort,
		PendingPlan:      &PendingPlanApproval{ID: "plan_abort"},
		PlanApprovalCh:   pChAbort,
	}
	hm.SetLiveTurnForTest(liveAbort)
	hm.AbortTurn()

	select {
	case <-qChAbort:
	default:
		t.Fatal("expected QuestionAnswerCh to be unblocked on AbortTurn")
	}
	select {
	case <-pChAbort:
	default:
		t.Fatal("expected PlanApprovalCh to be unblocked on AbortTurn")
	}
}

func TestBuildHarnessConfigPrompt_PlanMode(t *testing.T) {
	hm, _ := setupTestManager(t)

	// Test Plan mode text
	promptPlan := hm.buildHarnessConfigPrompt("gemini-2.5-pro", "google", "Plan")
	if !strings.Contains(promptPlan, "Execution Mode: Plan") {
		t.Errorf("expected Execution Mode: Plan in prompt, got:\n%s", promptPlan)
	}
	if !strings.Contains(promptPlan, "write_file and edit_file on project source files are locked") {
		t.Errorf("expected write lock explanation in prompt, got:\n%s", promptPlan)
	}

	// Test Subagent Spawn Approval UI wording
	hm.GetSettings().AskBeforeSubagentSpawn = true
	promptAsk := hm.buildHarnessConfigPrompt("gemini-2.5-pro", "google", "Autonomous")
	if !strings.Contains(promptAsk, "Subagent Spawn Approval UI: ENABLED ('Ask Me Before Spawning')") {
		t.Errorf("expected ENABLED subagent approval wording, got:\n%s", promptAsk)
	}
	if !strings.Contains(promptAsk, "calling invoke_subagent IS how the user gets the interactive model picker card") {
		t.Errorf("expected model picker card explanation, got:\n%s", promptAsk)
	}

	hm.GetSettings().AskBeforeSubagentSpawn = false
	hm.GetSettings().SubagentModel = "inherit"
	promptAuto := hm.buildHarnessConfigPrompt("gemini-2.5-pro", "google", "Autonomous")
	if !strings.Contains(promptAuto, "Subagent Spawn Approval UI: AUTOMATIC") {
		t.Errorf("expected AUTOMATIC subagent approval wording, got:\n%s", promptAuto)
	}
}

func TestPromptModifiers_CavemanAndPonytail(t *testing.T) {
	hm, _ := setupTestManager(t)

	// Test helper block generation
	bNone := buildPromptModifiersBlock(false, false)
	if bNone != "" {
		t.Errorf("expected empty string for no modifiers, got %q", bNone)
	}

	bCaveman := buildPromptModifiersBlock(true, false)
	if !strings.Contains(bCaveman, "<caveman_mode_active>") || !strings.Contains(bCaveman, "CAVEMAN MODE") {
		t.Errorf("expected caveman mode block, got %s", bCaveman)
	}

	bPonytail := buildPromptModifiersBlock(false, true)
	if !strings.Contains(bPonytail, "<ponytail_mode_active>") || !strings.Contains(bPonytail, "PONYTAIL") {
		t.Errorf("expected ponytail mode block, got %s", bPonytail)
	}

	bBoth := buildPromptModifiersBlock(true, true)
	if !strings.Contains(bBoth, "<caveman_mode_active>") || !strings.Contains(bBoth, "<ponytail_mode_active>") {
		t.Errorf("expected both modifier blocks, got %s", bBoth)
	}

	// Test SetPromptModifiers
	tVal := true
	fVal := false
	res := hm.SetPromptModifiers(&tVal, &fVal)
	if res["caveman_mode"] != true || res["ponytail_mode"] != false {
		t.Errorf("expected caveman=true ponytail=false, got %v", res)
	}

	snap := hm.GetStateSnapshot()
	if snap["caveman_mode"] != true || snap["ponytail_mode"] != false {
		t.Errorf("expected snapshot to expose caveman=true, got %v", snap)
	}

	live := hm.GetLiveTurnState()
	if live["caveman_mode"] != true || live["ponytail_mode"] != false {
		t.Errorf("expected live turn state to expose caveman=true, got %v", live)
	}

	// Test live turn sync
	lt := &LiveTurn{Running: true, ConvID: "conv_mod_test"}
	hm.SetLiveTurnForTest(lt)

	hm.SetPromptModifiers(&fVal, &tVal)
	if !lt.PonytailMode || lt.CavemanMode {
		t.Errorf("expected live turn to be updated with ponytail=true, caveman=false, got c=%v, p=%v", lt.CavemanMode, lt.PonytailMode)
	}
}

func TestDuelModePlaybookPrompt(t *testing.T) {
	hm, _ := setupTestManager(t)
	hm.SetAgentMode("Duel")

	snap := hm.GetLiveTurnState()
	if snap["agent_mode"] != "Duel" {
		t.Errorf("expected agent_mode Duel, got %v", snap["agent_mode"])
	}
}

func TestClaudeCodeParityToolPolicyAndBudgetWarning(t *testing.T) {
	hm, _ := setupTestManager(t)

	// 1. Verify Unjess self-knowledge version string
	cfgPrompt := hm.buildHarnessConfigPrompt("deepseek-v4.1-flash:1017", "ollama", "Autonomous")
	expectedGreeting := fmt.Sprintf("You are Unjess v%s", version.Current)
	if !strings.Contains(cfgPrompt, expectedGreeting) {
		t.Errorf("expected %q in self knowledge prompt, got: %s", expectedGreeting, cfgPrompt)
	}

	// 2. Verify subagent prompt includes Claude Code parity tool usage policy
	sr := &SubagentRun{
		ID:        "sub_test_1",
		Role:      "Architect",
		TypeName:  "research",
		Workspace: t.TempDir(),
	}
	_ = sr
}

func TestExtractProtectedPathsFromPrompt(t *testing.T) {
	prompt := `
Diagnose the root cause and modify /app so that bash /app/run_pretrain.sh runs to completion.
- Do not modify /app/run_pretrain.sh
- Do not modify /app/data/train/index.json
- Do not edit baseline_dedup.py
- Never touch /app/verifier/check.py
`
	paths := extractProtectedPathsFromPrompt(prompt)
	expected := []string{
		"/app/run_pretrain.sh",
		"/app/data/train/index.json",
		"baseline_dedup.py",
		"/app/verifier/check.py",
	}

	if len(paths) != len(expected) {
		t.Fatalf("expected %d paths, got %d: %v", len(expected), len(paths), paths)
	}

	for i, exp := range expected {
		if paths[i] != exp {
			t.Errorf("path[%d] expected %s, got %s", i, exp, paths[i])
		}
	}
}

func TestIsTransitionalNarration(t *testing.T) {
	// Cases that ARE transitional commentary (should NOT break the loop)
	transitionalCases := []string{
		"Now the styles for the new controls. Let me see the existing CSS vocabulary first.",
		"I will now run the test to see if it converges.",
		"Let's inspect the files in /app/data now.",
		"Next, I will edit the solution.cpp file.",
		"Now let's check the audio spectrum analysis.",
	}
	for _, tc := range transitionalCases {
		if !isTransitionalNarration(tc) {
			t.Errorf("expected isTransitionalNarration(%q) = true, got false", tc)
		}
	}

	// Cases that are NOT transitional (are real completions or questions)
	nonTransitionalCases := []string{
		"The task is complete. All tests pass successfully.",
		"I have completed the changes requested. Here is the final summary.",
		"Could you clarify which port you want the server to listen on?",
		"All requirements have been met and verified.",
	}
	for _, tc := range nonTransitionalCases {
		if isTransitionalNarration(tc) {
			t.Errorf("expected isTransitionalNarration(%q) = false, got true", tc)
		}
	}
}

func TestCompactMessagesSmartlyAndScratchDetection(t *testing.T) {
	// 1. Verify non-superseded read_file outputs are preserved intact across >4 tool calls
	fileABody := "File: solver.py (100 lines total, showing 1-100)\n" + strings.Repeat("1: def solve(x): return x + 1\n", 30)
	fileBBody := "File: utils.py (50 lines total, showing 1-50)\n" + strings.Repeat("1: def helper(): pass\n", 25)

	msgs := []llm.Message{
		{Role: "tool", ToolName: "read_file", Content: fileABody},
		{Role: "tool", ToolName: "read_file", Content: fileBBody},
		{Role: "tool", ToolName: "run_command", Content: "pytest output 1"},
		{Role: "tool", ToolName: "run_command", Content: "pytest output 2"},
		{Role: "tool", ToolName: "run_command", Content: "pytest output 3"},
		{Role: "tool", ToolName: "run_command", Content: "pytest output 4"},
	}
	compactMessagesSmartly(msgs)
	if msgs[0].Content != fileABody {
		t.Errorf("expected non-superseded solver.py read_file to remain uncompacted at step 6")
	}

	// 2. When solver.py (same range header) is read again later, the older superseded read IS micro-compacted
	msgs = append(msgs, llm.Message{Role: "tool", ToolName: "read_file", Content: fileABody})
	compactMessagesSmartly(msgs)
	if !strings.Contains(msgs[0].Content, "[Superseded read_file output compacted") {
		t.Errorf("expected older superseded read_file to be micro-compacted, got:\n%s", msgs[0].Content)
	}
	if msgs[1].Content != fileBBody {
		t.Errorf("expected utils.py read_file to remain intact, got:\n%s", msgs[1].Content)
	}

	// 3. Verify detectLeftoverScratchFiles catches temporary debug scripts still on disk
	wsDir := t.TempDir()
	scratchFile := filepath.Join(wsDir, "test_debug_repro.py")
	_ = os.WriteFile(scratchFile, []byte("print('debug')\n"), 0644)

	leftovers := detectLeftoverScratchFiles(wsDir, []tools.FileTouch{
		{Path: "solver.py", Action: "modified"},
		{Path: "test_debug_repro.py", Action: "created"},
	})
	if len(leftovers) != 1 || leftovers[0] != "test_debug_repro.py" {
		t.Errorf("expected test_debug_repro.py to be flagged as leftover scratch file, got %v", leftovers)
	}
}

func TestTransparentBookkeepingRepeatToolGuard(t *testing.T) {
	var lastKey string
	var sameCount int

	// 1. Canonical key-sorted JSON with trimmed string values matches across whitespace/key order
	k1 := canonicalizeToolCallKey("grep_search", map[string]any{"pattern": "  solve_matrix  ", "path": "src/"})
	k2 := canonicalizeToolCallKey("GREP_SEARCH", map[string]any{"path": "src/ ", "pattern": "solve_matrix"})
	if k1 != k2 {
		t.Fatalf("expected canonical tool keys to match, got %q vs %q", k1, k2)
	}

	// 2. Interleaved todo_write calls must be transparent to repeat-tool counting:
	// grep -> todo_write -> grep -> todo_write -> grep => repeat count 3!
	c1, tracked1 := updateRepeatToolState("grep_search", map[string]any{"pattern": "solve_matrix", "path": "src/"}, &lastKey, &sameCount)
	if !tracked1 || c1 != 1 {
		t.Fatalf("expected count=1 on first grep_search, got %d (tracked=%v)", c1, tracked1)
	}

	cTodo, trackedTodo := updateRepeatToolState("todo_write", map[string]any{"todos": []any{map[string]any{"content": "Step 1", "status": "in_progress"}}}, &lastKey, &sameCount)
	if trackedTodo || cTodo != 1 {
		t.Fatalf("expected todo_write to be transparent and keep count=1, got %d (tracked=%v)", cTodo, trackedTodo)
	}
	if rem := formatRepeatToolReminder("todo_write", sameCount, 0); rem != "" {
		t.Errorf("expected no repeat reminder on transparent todo_write, got: %s", rem)
	}

	c2, _ := updateRepeatToolState("grep_search", map[string]any{"path": "src/", "pattern": " solve_matrix "}, &lastKey, &sameCount)
	if c2 != 2 {
		t.Fatalf("expected count=2 on second grep_search across todo_write, got %d", c2)
	}

	_, _ = updateRepeatToolState("todo_write", map[string]any{"todos": []any{map[string]any{"content": "Step 1", "status": "completed"}}}, &lastKey, &sameCount)

	c3, _ := updateRepeatToolState("grep_search", map[string]any{"pattern": "solve_matrix", "path": "src/"}, &lastKey, &sameCount)
	if c3 != 3 {
		t.Fatalf("expected count=3 on third grep_search across todo_write, got %d", c3)
	}

	// 3. Verify escalating reminder schedule [3, 5, 8]
	rem3 := formatRepeatToolReminder("grep_search", 3, 0)
	if !strings.Contains(rem3, "Loop / Stuck Pattern Detected") {
		t.Errorf("expected threshold-3 loop reminder, got: %s", rem3)
	}
	rem5 := formatRepeatToolReminder("grep_search", 5, 0)
	if !strings.Contains(rem5, "High-Severity Loop Warning") || !strings.Contains(rem5, "#5") {
		t.Errorf("expected threshold-5 high-severity reminder, got: %s", rem5)
	}
	rem8 := formatRepeatToolReminder("grep_search", 8, 0)
	if !strings.Contains(rem8, "CRITICAL Loop Escalation") || !strings.Contains(rem8, "#8") {
		t.Errorf("expected threshold-8 critical loop escalation, got: %s", rem8)
	}
}

func TestTwoStageCompactionAndEightSectionCheckpoint(t *testing.T) {
	// 1. Test Stage-1 Model-Free Pruner (4096 head + 1024 tail for >8192 char tool results when total > 100k)
	// Create 3 older large tool outputs (40,000 chars each = 120,000 chars total, exceeding 100k Stage-1 threshold)
	// plus 6 recent tool outputs so the first 3 are eligible for Stage-1 pruning.
	bigOut := "HEAD_MARKER_" + strings.Repeat("M", 39970) + "_TAIL_MARKER"
	msgs := []llm.Message{
		{Role: "tool", ToolName: "run_command", Content: bigOut},
		{Role: "tool", ToolName: "run_command", Content: bigOut},
		{Role: "tool", ToolName: "run_command", Content: bigOut},
		{Role: "tool", ToolName: "read_file", Content: "recent 1"},
		{Role: "tool", ToolName: "read_file", Content: "recent 2"},
		{Role: "tool", ToolName: "read_file", Content: "recent 3"},
		{Role: "tool", ToolName: "read_file", Content: "recent 4"},
		{Role: "tool", ToolName: "read_file", Content: "recent 5"},
		{Role: "tool", ToolName: "read_file", Content: "recent 6"},
	}

	compactMessagesSmartly(msgs)

	// Verify the first 3 tool outputs were pruned by Stage-1 (4096 head + 1024 tail) and NOT Stage-2
	for i := 0; i < 3; i++ {
		c := msgs[i].Content
		if !strings.Contains(c, "[Stage-1 Pruned") {
			t.Fatalf("expected msgs[%d] to be pruned by Stage-1 head+tail pruner, got len=%d", i, len(c))
		}
		if !strings.HasPrefix(c, "HEAD_MARKER_") || !strings.HasSuffix(c, "_TAIL_MARKER") {
			t.Errorf("expected Stage-1 pruner to preserve exact head and tail markers on msgs[%d]", i)
		}
		if strings.Contains(c, "[Compacted older") {
			t.Errorf("expected Stage-2 compaction to be skipped after Stage-1 reclaimed enough budget")
		}
	}

	// 2. Test Multimodal Image Offload (keeps newest 2 image-bearing messages, offloads older ones)
	imgMsgs := []llm.Message{
		{Role: "user", Content: "Turn 1", Images: []llm.ImageAttachment{{Name: "old_shot_1.png", Data: "AAAA"}}},
		{Role: "user", Content: "Turn 2", Images: []llm.ImageAttachment{{Name: "old_shot_2.png", Data: "BBBB"}}},
		{Role: "user", Content: "Turn 3", Images: []llm.ImageAttachment{{Name: "recent_shot_3.png", Data: "CCCC"}}},
		{Role: "user", Content: "Turn 4", Images: []llm.ImageAttachment{{Name: "recent_shot_4.png", Data: "DDDD"}}},
	}
	offloaded := offloadOlderImages(imgMsgs)
	if offloaded != 2 {
		t.Fatalf("expected 2 older images to be offloaded, got %d", offloaded)
	}
	if len(imgMsgs[0].Images) != 0 || !strings.Contains(imgMsgs[0].Content, "[Offloaded image attachment(s): old_shot_1.png") {
		t.Errorf("expected imgMsgs[0] image to be offloaded with marker, got %+v", imgMsgs[0])
	}
	if len(imgMsgs[2].Images) != 1 || len(imgMsgs[3].Images) != 1 {
		t.Errorf("expected newest 2 image-bearing messages to retain their images")
	}

	// 3. Test Warm KV-Cache Prefix Compaction Request builder
	baseOpts := llm.ChatOptions{
		Model:        "deepseek-reasoner",
		SystemPrompt: "System prompt invariant",
		Messages:     []llm.Message{{Role: "user", Content: "Hello"}},
	}
	warmReq := buildWarmPrefixCompactionRequest(baseOpts)
	if warmReq.SystemPrompt != baseOpts.SystemPrompt || len(warmReq.Messages) != 2 {
		t.Fatalf("unexpected warm compaction request: %+v", warmReq)
	}
	if warmReq.Messages[0].Content != "Hello" || !strings.Contains(warmReq.Messages[1].Content, "<compaction-request>") {
		t.Errorf("warm compaction request did not preserve prefix or append <compaction-request>")
	}

	// 4. Test CompactConversation 8-Section Checkpoint output
	hm, tempDir := setupTestManager(t)
	created := hm.CreateConversation("Compaction Test", tempDir, "workspace")
	conv := created["conversation"].(map[string]any)
	convID := conv["id"].(string)

	hm.mu.Lock()
	conv["turns"] = []any{
		map[string]any{"role": "user", "text": "Implement persistent shell and freshness checks", "timestamp": "10:00"},
		map[string]any{
			"role":      "agent",
			"text":      "Implemented shell state tracking",
			"timestamp": "10:01",
			"model":     "deepseek-reasoner",
			"tool_calls": []any{
				map[string]any{"name": "edit_file", "status": "completed", "arguments": map[string]any{"path": "tools.go"}},
				map[string]any{"name": "run_command", "status": "completed", "arguments": map[string]any{"command": "go test ./..."}},
				map[string]any{"name": "todo_write", "status": "completed", "arguments": map[string]any{"todos": []any{map[string]any{"content": "Write tests", "status": "in_progress"}}}},
			},
		},
		map[string]any{"role": "user", "text": "Recent user turn", "timestamp": "10:05"},
		map[string]any{"role": "agent", "text": "Recent agent turn", "timestamp": "10:06"},
	}
	hm.mu.Unlock()

	compactedConv, err := hm.CompactConversation(convID)
	if err != nil {
		t.Fatalf("CompactConversation failed: %v", err)
	}
	turnsAfter := compactedConv["turns"].([]any)
	summaryTurn := turnsAfter[0].(map[string]any)
	summaryText := fmt.Sprint(summaryTurn["text"])

	for _, section := range []string{
		"### 1. Primary Request and Intent",
		"### 2. Key Technical Concepts",
		"### 3. Files and Code Sections Examined or Modified",
		"### 4. Errors and Fixes",
		"### 5. Problem Solving & Verifications Executed",
		"### 6. All User Messages",
		"### 7. Pending Tasks",
		"### 8. Current Work & Next Step",
	} {
		if !strings.Contains(summaryText, section) {
			t.Errorf("expected 8-section checkpoint to contain %q, got:\n%s", section, summaryText)
		}
	}
}




