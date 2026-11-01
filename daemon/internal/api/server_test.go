package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"unjess-daemon/internal/agent"
	"unjess-daemon/internal/config"
	"unjess-daemon/internal/updater"
)

func setupTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	cfgTempDir := t.TempDir()
	t.Setenv("UNJESS_STATE_PATH", filepath.Join(cfgTempDir, "harness_state.json"))

	origConfigDir := config.DefaultConfigDir
	origConfigPath := config.DefaultConfigPath
	origKeysPath := config.DefaultKeysPath
	origMCPPath := config.DefaultMCPPath
	config.DefaultConfigDir = cfgTempDir
	config.DefaultConfigPath = filepath.Join(cfgTempDir, "config.yaml")
	config.DefaultKeysPath = filepath.Join(cfgTempDir, "keys.yaml")
	config.DefaultMCPPath = filepath.Join(cfgTempDir, "mcp.json")
	t.Cleanup(func() {
		config.DefaultConfigDir = origConfigDir
		config.DefaultConfigPath = origConfigPath
		config.DefaultKeysPath = origKeysPath
		config.DefaultMCPPath = origMCPPath
	})

	tempDir, err := os.MkdirTemp("", "unjess_api_test_*")
	if err != nil {
		t.Fatalf("failed creating temp dir: %v", err)
	}

	settings := config.NewDefaultSettings()
	settings.Workspace = tempDir

	hm := agent.NewHarnessManager(settings)
	srv := NewServer(9757, "127.0.0.1", settings, hm)
	return srv, tempDir
}

func TestCORS(t *testing.T) {
	srv, tempDir := setupTestServer(t)
	defer os.RemoveAll(tempDir)

	handler := srv.corsMiddleware(srv.Routes())

	// 1. OPTIONS preflight
	reqOptions := httptest.NewRequest("OPTIONS", "/api/health", nil)
	recOptions := httptest.NewRecorder()
	handler.ServeHTTP(recOptions, reqOptions)

	if recOptions.Code != http.StatusOK {
		t.Errorf("expected 200 for OPTIONS, got %d", recOptions.Code)
	}
	origin := recOptions.Header().Get("Access-Control-Allow-Origin")
	if origin != "*" {
		t.Errorf("expected Access-Control-Allow-Origin '*', got '%s'", origin)
	}
	methods := recOptions.Header().Get("Access-Control-Allow-Methods")
	if methods == "" {
		t.Errorf("expected Access-Control-Allow-Methods header to be set")
	}

	// 2. GET /api/health with CORS
	reqGet := httptest.NewRequest("GET", "/api/health", nil)
	recGet := httptest.NewRecorder()
	handler.ServeHTTP(recGet, reqGet)

	if recGet.Code != http.StatusOK {
		t.Fatalf("expected 200 OK from /api/health, got %d: %s", recGet.Code, recGet.Body.String())
	}

	if recGet.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Errorf("expected CORS header on GET request")
	}

	var health map[string]any
	if err := json.NewDecoder(recGet.Body).Decode(&health); err != nil {
		t.Fatalf("failed decoding health response: %v", err)
	}

	if health["status"] != "ok" {
		t.Errorf("expected status ok, got %v", health["status"])
	}
	if health["version"] != updater.CurrentVersion+"-go" {
		t.Errorf("expected version %s-go, got %v", updater.CurrentVersion, health["version"])
	}
	if health["engine"] != "go" {
		t.Errorf("expected engine go, got %v", health["engine"])
	}
}

func TestHealthAndStatus(t *testing.T) {
	srv, tempDir := setupTestServer(t)
	defer os.RemoveAll(tempDir)

	handler := srv.Routes()

	// GET /api/status
	req := httptest.NewRequest("GET", "/api/status", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var status map[string]any
	if err := json.NewDecoder(w.Body).Decode(&status); err != nil {
		t.Fatalf("failed decoding: %v", err)
	}

	if status["engine"] != "go" {
		t.Errorf("expected engine 'go', got %v", status["engine"])
	}
}

func TestDesktopUIServing(t *testing.T) {
	srv, tempDir := setupTestServer(t)
	defer os.RemoveAll(tempDir)

	handler := srv.Routes()

	for _, path := range []string{"/", "/desktop", "/mobile", "/app", "/openclaw", "/studio"} {
		req := httptest.NewRequest("GET", path, nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("path %s returned status %d", path, w.Code)
		}
		ct := w.Header().Get("Content-Type")
		if ct != "text/html; charset=utf-8" {
			t.Errorf("path %s returned unexpected content-type %s", path, ct)
		}
	}
}

func TestSettingsEndpoints(t *testing.T) {
	srv, tempDir := setupTestServer(t)
	defer os.RemoveAll(tempDir)

	handler := srv.Routes()

	// 1. GET /api/studio/settings
	req := httptest.NewRequest("GET", "/api/studio/settings", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var getResp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&getResp); err != nil {
		t.Fatalf("failed decoding: %v", err)
	}
	if getResp["status"] != "ok" {
		t.Errorf("expected status ok")
	}

	// 2. POST /api/studio/settings
	updatePayload := map[string]any{
		"verbosity": "debug",
	}
	payloadBytes, _ := json.Marshal(updatePayload)
	reqPost := httptest.NewRequest("POST", "/api/studio/settings", bytes.NewReader(payloadBytes))
	wPost := httptest.NewRecorder()
	handler.ServeHTTP(wPost, reqPost)

	if wPost.Code != http.StatusOK {
		t.Fatalf("expected 200 from POST settings, got %d: %s", wPost.Code, wPost.Body.String())
	}
}

func TestWorkspacesAndFiles(t *testing.T) {
	srv, tempDir := setupTestServer(t)
	defer os.RemoveAll(tempDir)

	handler := srv.Routes()

	// 1. Select Workspace
	selPayload := map[string]any{
		"workspace": tempDir,
	}
	b, _ := json.Marshal(selPayload)
	reqSel := httptest.NewRequest("POST", "/api/harness/workspaces/select", bytes.NewReader(b))
	wSel := httptest.NewRecorder()
	handler.ServeHTTP(wSel, reqSel)

	if wSel.Code != http.StatusOK {
		t.Fatalf("select workspace failed: %d, body: %s", wSel.Code, wSel.Body.String())
	}

	// 2. Write file
	writeFilePayload := map[string]any{
		"workspace": tempDir,
		"path":      "hello.txt",
		"content":   "Hello Unjess Daemon",
	}
	bFile, _ := json.Marshal(writeFilePayload)
	reqWrite := httptest.NewRequest("POST", "/api/harness/file", bytes.NewReader(bFile))
	wWrite := httptest.NewRecorder()
	handler.ServeHTTP(wWrite, reqWrite)

	if wWrite.Code != http.StatusOK {
		t.Fatalf("write file failed: %d, body: %s", wWrite.Code, wWrite.Body.String())
	}

	// 3. Read file
	reqRead := httptest.NewRequest("GET", "/api/harness/file?workspace="+tempDir+"&path=hello.txt", nil)
	wRead := httptest.NewRecorder()
	handler.ServeHTTP(wRead, reqRead)

	if wRead.Code != http.StatusOK {
		t.Fatalf("read file failed: %d, body: %s", wRead.Code, wRead.Body.String())
	}
	var readData map[string]any
	_ = json.NewDecoder(wRead.Body).Decode(&readData)
	if readData["content"] != "Hello Unjess Daemon" {
		t.Errorf("unexpected content: %v", readData["content"])
	}

	// 4. Raw file
	reqRaw := httptest.NewRequest("GET", "/api/harness/raw?workspace="+tempDir+"&path=hello.txt", nil)
	wRaw := httptest.NewRecorder()
	handler.ServeHTTP(wRaw, reqRaw)

	if wRaw.Code != http.StatusOK {
		t.Fatalf("raw file failed: %d", wRaw.Code)
	}
	if wRaw.Body.String() != "Hello Unjess Daemon" {
		t.Errorf("raw content mismatch: %s", wRaw.Body.String())
	}

	// 5. List files
	reqList := httptest.NewRequest("GET", "/api/harness/files?workspace="+tempDir, nil)
	wList := httptest.NewRecorder()
	handler.ServeHTTP(wList, reqList)

	if wList.Code != http.StatusOK {
		t.Fatalf("list files failed: %d", wList.Code)
	}

	// 6. Delete file
	delPayload := map[string]any{
		"workspace": tempDir,
		"path":      "hello.txt",
	}
	bDel, _ := json.Marshal(delPayload)
	reqDel := httptest.NewRequest("DELETE", "/api/harness/file", bytes.NewReader(bDel))
	wDel := httptest.NewRecorder()
	handler.ServeHTTP(wDel, reqDel)

	if wDel.Code != http.StatusOK {
		t.Fatalf("delete file failed: %d", wDel.Code)
	}

	// Verify file is gone
	if _, err := os.Stat(filepath.Join(tempDir, "hello.txt")); !os.IsNotExist(err) {
		t.Errorf("file should have been deleted")
	}
}

func TestExecCommand(t *testing.T) {
	srv, tempDir := setupTestServer(t)
	defer os.RemoveAll(tempDir)

	handler := srv.Routes()

	execPayload := map[string]any{
		"workspace": tempDir,
		"command":   "echo UnjessDaemonExecTest",
	}
	b, _ := json.Marshal(execPayload)
	req := httptest.NewRequest("POST", "/api/harness/exec", bytes.NewReader(b))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("exec command failed: %d, body: %s", w.Code, w.Body.String())
	}

	var res map[string]any
	if err := json.NewDecoder(w.Body).Decode(&res); err != nil {
		t.Fatalf("failed decoding: %v", err)
	}

	if res["status"] != "ok" {
		t.Errorf("expected ok status, got %v", res["status"])
	}
}

func TestConversationsRoutes(t *testing.T) {
	srv, tempDir := setupTestServer(t)
	defer os.RemoveAll(tempDir)

	handler := srv.Routes()

	// 1. Create conversation
	createPayload := map[string]any{
		"title":     "Test Conv",
		"workspace": tempDir,
		"mode":      "agent",
	}
	b, _ := json.Marshal(createPayload)
	reqCreate := httptest.NewRequest("POST", "/api/harness/conversations", bytes.NewReader(b))
	wCreate := httptest.NewRecorder()
	handler.ServeHTTP(wCreate, reqCreate)

	if wCreate.Code != http.StatusOK {
		t.Fatalf("create conversation failed: %d, body: %s", wCreate.Code, wCreate.Body.String())
	}

	var createRes map[string]any
	_ = json.NewDecoder(wCreate.Body).Decode(&createRes)
	conv, ok := createRes["conversation"].(map[string]any)
	if !ok {
		t.Fatalf("expected conversation object in response: %v", createRes)
	}
	convID, _ := conv["id"].(string)
	if convID == "" {
		t.Fatalf("missing conversation ID")
	}

	// 2. Clear conversation
	reqClear := httptest.NewRequest("POST", "/api/harness/conversations/"+convID+"/clear", nil)
	wClear := httptest.NewRecorder()
	handler.ServeHTTP(wClear, reqClear)

	if wClear.Code != http.StatusOK {
		t.Fatalf("clear conversation failed: %d, body: %s", wClear.Code, wClear.Body.String())
	}

	// 3. Delete conversation
	reqDelete := httptest.NewRequest("DELETE", "/api/harness/conversations/"+convID, nil)
	wDelete := httptest.NewRecorder()
	handler.ServeHTTP(wDelete, reqDelete)

	if wDelete.Code != http.StatusOK {
		t.Fatalf("delete conversation failed: %d, body: %s", wDelete.Code, wDelete.Body.String())
	}
}

func TestWorkflowsAndAgentsCRUD(t *testing.T) {
	srv, tempDir := setupTestServer(t)
	defer os.RemoveAll(tempDir)

	handler := srv.Routes()

	// 1. List workflows
	reqWfList := httptest.NewRequest("GET", "/api/harness/workflows", nil)
	wWfList := httptest.NewRecorder()
	handler.ServeHTTP(wWfList, reqWfList)
	if wWfList.Code != http.StatusOK {
		t.Fatalf("list workflows failed: %d", wWfList.Code)
	}

	// 2. Save workflow
	wfPayload := map[string]any{
		"id":     "wf_test_1",
		"title":  "Test Workflow",
		"prompt": "Run tests and verify",
	}
	bWf, _ := json.Marshal(wfPayload)
	reqWfSave := httptest.NewRequest("POST", "/api/harness/workflows", bytes.NewReader(bWf))
	wWfSave := httptest.NewRecorder()
	handler.ServeHTTP(wWfSave, reqWfSave)
	if wWfSave.Code != http.StatusOK {
		t.Fatalf("save workflow failed: %d", wWfSave.Code)
	}

	// 3. Delete workflow
	reqWfDel := httptest.NewRequest("DELETE", "/api/harness/workflows/wf_test_1", nil)
	wWfDel := httptest.NewRecorder()
	handler.ServeHTTP(wWfDel, reqWfDel)
	if wWfDel.Code != http.StatusOK {
		t.Fatalf("delete workflow failed: %d", wWfDel.Code)
	}

	// 4. List agents
	reqAgList := httptest.NewRequest("GET", "/api/harness/agents", nil)
	wAgList := httptest.NewRecorder()
	handler.ServeHTTP(wAgList, reqAgList)
	if wAgList.Code != http.StatusOK {
		t.Fatalf("list agents failed: %d", wAgList.Code)
	}

	// 5. Save agent
	agPayload := map[string]any{
		"id":            "agent_test_1",
		"name":          "Tester",
		"system_prompt": "You are a test agent",
	}
	bAg, _ := json.Marshal(agPayload)
	reqAgSave := httptest.NewRequest("POST", "/api/harness/agents", bytes.NewReader(bAg))
	wAgSave := httptest.NewRecorder()
	handler.ServeHTTP(wAgSave, reqAgSave)
	if wAgSave.Code != http.StatusOK {
		t.Fatalf("save agent failed: %d", wAgSave.Code)
	}

	// 6. Delete agent
	reqAgDel := httptest.NewRequest("DELETE", "/api/harness/agents/agent_test_1", nil)
	wAgDel := httptest.NewRecorder()
	handler.ServeHTTP(wAgDel, reqAgDel)
	if wAgDel.Code != http.StatusOK {
		t.Fatalf("delete agent failed: %d", wAgDel.Code)
	}
}

func TestMCPAndSkillsEndpoints(t *testing.T) {
	srv, tempDir := setupTestServer(t)
	defer os.RemoveAll(tempDir)

	handler := srv.Routes()

	// 1. GET /api/harness/mcp
	reqMcp := httptest.NewRequest("GET", "/api/harness/mcp", nil)
	wMcp := httptest.NewRecorder()
	handler.ServeHTTP(wMcp, reqMcp)
	if wMcp.Code != http.StatusOK {
		t.Fatalf("list mcp failed: %d", wMcp.Code)
	}

	// 2. GET /api/harness/skills
	reqSkills := httptest.NewRequest("GET", "/api/harness/skills?workspace="+tempDir, nil)
	wSkills := httptest.NewRecorder()
	handler.ServeHTTP(wSkills, reqSkills)
	if wSkills.Code != http.StatusOK {
		t.Fatalf("list skills failed: %d", wSkills.Code)
	}

	// 3. GET /api/harness/state
	reqState := httptest.NewRequest("GET", "/api/harness/state", nil)
	wState := httptest.NewRecorder()
	handler.ServeHTTP(wState, reqState)
	if wState.Code != http.StatusOK {
		t.Fatalf("get state failed: %d", wState.Code)
	}

	// 4. GET /api/harness/live
	reqLive := httptest.NewRequest("GET", "/api/harness/live", nil)
	wLive := httptest.NewRecorder()
	handler.ServeHTTP(wLive, reqLive)
	if wLive.Code != http.StatusOK {
		t.Fatalf("get live failed: %d", wLive.Code)
	}

	// 5. POST /api/harness/abort
	reqAbort := httptest.NewRequest("POST", "/api/harness/abort", nil)
	wAbort := httptest.NewRecorder()
	handler.ServeHTTP(wAbort, reqAbort)
	if wAbort.Code != http.StatusOK {
		t.Fatalf("abort failed: %d", wAbort.Code)
	}
}

func TestSubagentEndpoints(t *testing.T) {
	srv, tempDir := setupTestServer(t)
	defer os.RemoveAll(tempDir)

	handler := srv.Routes()

	// 1. GET /api/harness/subagents initially empty
	reqList := httptest.NewRequest("GET", "/api/harness/subagents", nil)
	wList := httptest.NewRecorder()
	handler.ServeHTTP(wList, reqList)
	if wList.Code != http.StatusOK {
		t.Fatalf("expected 200 from GET /api/harness/subagents, got %d", wList.Code)
	}

	// 2. POST /api/harness/subagents/spawn
	spawnBody := map[string]any{
		"conv_id":   "conv_api_test",
		"workspace": tempDir,
		"subagents": []map[string]any{
			{
				"role":      "Test API Engineer",
				"prompt":    "Write api tests",
				"model":     "inherit",
				"type_name": "tester",
			},
		},
	}
	bodyBytes, _ := json.Marshal(spawnBody)
	reqSpawn := httptest.NewRequest("POST", "/api/harness/subagents/spawn", bytes.NewReader(bodyBytes))
	reqSpawn.Header.Set("Content-Type", "application/json")
	wSpawn := httptest.NewRecorder()
	handler.ServeHTTP(wSpawn, reqSpawn)

	if wSpawn.Code != http.StatusOK {
		t.Fatalf("expected 200 from spawn, got %d: %s", wSpawn.Code, wSpawn.Body.String())
	}

	var spawnResp map[string]any
	if err := json.NewDecoder(wSpawn.Body).Decode(&spawnResp); err != nil {
		t.Fatalf("failed decoding spawn resp: %v", err)
	}
	subagents, ok := spawnResp["subagents"].([]any)
	if !ok || len(subagents) == 0 {
		t.Fatalf("expected subagents in spawn resp, got: %+v", spawnResp)
	}
	firstSub := subagents[0].(map[string]any)
	subID := fmt.Sprint(firstSub["id"])

	// 3. GET /api/harness/subagents/{id}
	reqGet := httptest.NewRequest("GET", "/api/harness/subagents/"+subID, nil)
	wGet := httptest.NewRecorder()
	handler.ServeHTTP(wGet, reqGet)
	if wGet.Code != http.StatusOK {
		t.Fatalf("expected 200 from GET /api/harness/subagents/%s, got %d", subID, wGet.Code)
	}

	// 4. POST /api/harness/subagents/{id}/message
	msgBody := map[string]any{
		"message": "Continue test execution",
	}
	msgBytes, _ := json.Marshal(msgBody)
	reqMsg := httptest.NewRequest("POST", "/api/harness/subagents/"+subID+"/message", bytes.NewReader(msgBytes))
	reqMsg.Header.Set("Content-Type", "application/json")
	wMsg := httptest.NewRecorder()
	handler.ServeHTTP(wMsg, reqMsg)
	if wMsg.Code != http.StatusOK {
		t.Fatalf("expected 200 from message, got %d", wMsg.Code)
	}

	// 5. POST /api/harness/subagents/{id}/abort
	reqAbort := httptest.NewRequest("POST", "/api/harness/subagents/"+subID+"/abort", nil)
	wAbort := httptest.NewRecorder()
	handler.ServeHTTP(wAbort, reqAbort)
	if wAbort.Code != http.StatusOK {
		t.Fatalf("expected 200 from abort, got %d", wAbort.Code)
	}
}

func TestUpdatesEndpoints(t *testing.T) {
	srv, tempDir := setupTestServer(t)
	defer os.RemoveAll(tempDir)

	handler := srv.Routes()

	// 1. GET /api/harness/updates
	reqGet := httptest.NewRequest("GET", "/api/harness/updates", nil)
	wGet := httptest.NewRecorder()
	handler.ServeHTTP(wGet, reqGet)

	if wGet.Code != http.StatusOK {
		t.Fatalf("expected 200 from GET /api/harness/updates, got %d: %s", wGet.Code, wGet.Body.String())
	}

	var getResp map[string]any
	if err := json.NewDecoder(wGet.Body).Decode(&getResp); err != nil {
		t.Fatalf("failed decoding updates response: %v", err)
	}
	if getResp["status"] != "ok" {
		t.Errorf("expected status 'ok', got %v", getResp["status"])
	}
	if getResp["current_version"] != updater.CurrentVersion {
		t.Errorf("expected current_version '%s', got %v", updater.CurrentVersion, getResp["current_version"])
	}

	// 2. Mock update server for POST /api/harness/updates/check
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"version":      "2.9.9",
			"published_at": "2026-11-01",
			"download_url": "https://unjess.si/releases/Unjess-2.9.9.exe",
			"notes":        "Test update release",
			"changelog": []map[string]any{
				{
					"version": "2.9.9",
					"title":   "Autonomous Codebase Refactoring",
					"tag":     "FEATURE",
					"changes": []string{"Refactoring pipeline"},
				},
			},
		})
	}))
	defer mockServer.Close()

	checkBody := map[string]any{
		"update_server_url": mockServer.URL,
	}
	checkBytes, _ := json.Marshal(checkBody)
	reqCheck := httptest.NewRequest("POST", "/api/harness/updates/check", bytes.NewReader(checkBytes))
	reqCheck.Header.Set("Content-Type", "application/json")
	wCheck := httptest.NewRecorder()
	handler.ServeHTTP(wCheck, reqCheck)

	if wCheck.Code != http.StatusOK {
		t.Fatalf("expected 200 from POST /api/harness/updates/check, got %d: %s", wCheck.Code, wCheck.Body.String())
	}

	var checkResp map[string]any
	if err := json.NewDecoder(wCheck.Body).Decode(&checkResp); err != nil {
		t.Fatalf("failed decoding check response: %v", err)
	}
	if checkResp["latest_version"] != "2.9.9" {
		t.Errorf("expected latest_version '2.9.9', got %v", checkResp["latest_version"])
	}
	if checkResp["update_available"] != true {
		t.Errorf("expected update_available true, got %v", checkResp["update_available"])
	}
}

func TestMemoriesAPI(t *testing.T) {
	srv, tempDir := setupTestServer(t)
	defer os.RemoveAll(tempDir)

	tempMem := filepath.Join(tempDir, "api_memories.json")
	t.Setenv("UNJESS_MEMORY_PATH", tempMem)

	handler := srv.Routes()

	// 1. GET /api/harness/memories (empty)
	req1 := httptest.NewRequest("GET", "/api/harness/memories", nil)
	w1 := httptest.NewRecorder()
	handler.ServeHTTP(w1, req1)
	if w1.Code != http.StatusOK {
		t.Fatalf("expected 200 from GET /api/harness/memories, got %d: %s", w1.Code, w1.Body.String())
	}
	var res1 map[string]any
	_ = json.NewDecoder(w1.Body).Decode(&res1)
	if res1["status"] != "ok" || int(res1["count"].(float64)) != 0 {
		t.Fatalf("expected 0 initial memories, got %+v", res1)
	}

	// 2. POST /api/harness/memories (create memory)
	body := map[string]any{
		"key":       "user_stack_pref",
		"category":  "preference",
		"content":   "Prefers Go backend and React frontend",
		"workspace": "global",
		"tags":      []string{"go", "react"},
	}
	bodyBytes, _ := json.Marshal(body)
	req2 := httptest.NewRequest("POST", "/api/harness/memories", bytes.NewReader(bodyBytes))
	req2.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("expected 200 from POST /api/harness/memories, got %d: %s", w2.Code, w2.Body.String())
	}

	// 3. GET /api/harness/memories?q=react
	req3 := httptest.NewRequest("GET", "/api/harness/memories?q=react", nil)
	w3 := httptest.NewRecorder()
	handler.ServeHTTP(w3, req3)
	if w3.Code != http.StatusOK {
		t.Fatalf("expected 200 from GET /api/harness/memories?q=react, got %d", w3.Code)
	}
	var res3 map[string]any
	_ = json.NewDecoder(w3.Body).Decode(&res3)
	if int(res3["count"].(float64)) != 1 {
		t.Fatalf("expected 1 recalled memory, got %+v", res3)
	}

	// 4. DELETE /api/harness/memories/user_stack_pref
	req4 := httptest.NewRequest("DELETE", "/api/harness/memories/user_stack_pref", nil)
	w4 := httptest.NewRecorder()
	handler.ServeHTTP(w4, req4)
	if w4.Code != http.StatusOK {
		t.Fatalf("expected 200 from DELETE /api/harness/memories/user_stack_pref, got %d: %s", w4.Code, w4.Body.String())
	}

	// 5. GET /api/harness/memories (verify deleted)
	req5 := httptest.NewRequest("GET", "/api/harness/memories", nil)
	w5 := httptest.NewRecorder()
	handler.ServeHTTP(w5, req5)
	var res5 map[string]any
	_ = json.NewDecoder(w5.Body).Decode(&res5)
	if int(res5["count"].(float64)) != 0 {
		t.Fatalf("expected 0 memories after delete, got %+v", res5)
	}
}

func TestQuestionAndPlanEndpoints(t *testing.T) {
	srv, tempDir := setupTestServer(t)
	defer os.RemoveAll(tempDir)

	handler := srv.corsMiddleware(srv.Routes())

	// 1. Question approval endpoint
	qCh := make(chan string, 1)
	live := &agent.LiveTurn{
		Running:          true,
		ConvID:           "conv_q_test",
		AgentMode:        "Plan",
		PendingQuestion:  &agent.PendingQuestionApproval{ID: "q_api_1"},
		QuestionAnswerCh: qCh,
	}
	srv.Harness.SetLiveTurnForTest(live)

	qBody := map[string]any{
		"answers": "Choice A: Tailwind CSS",
		"skipped": false,
	}
	qBytes, _ := json.Marshal(qBody)
	reqQ := httptest.NewRequest("POST", "/api/harness/question/answer", bytes.NewReader(qBytes))
	reqQ.Header.Set("Content-Type", "application/json")
	wQ := httptest.NewRecorder()
	handler.ServeHTTP(wQ, reqQ)

	if wQ.Code != http.StatusOK {
		t.Fatalf("expected 200 from POST /api/harness/question/answer, got %d: %s", wQ.Code, wQ.Body.String())
	}
	var resQ map[string]any
	_ = json.NewDecoder(wQ.Body).Decode(&resQ)
	if resQ["status"] != "ok" || resQ["answers"] != "Choice A: Tailwind CSS" {
		t.Errorf("unexpected question resolution response: %+v", resQ)
	}

	// 2. Plan approval endpoint
	pCh := make(chan map[string]any, 1)
	live2 := &agent.LiveTurn{
		Running:        true,
		ConvID:         "conv_p_test",
		AgentMode:      "Plan",
		PendingPlan:    &agent.PendingPlanApproval{ID: "p_api_1", Title: "Test Plan"},
		PlanApprovalCh: pCh,
	}
	srv.Harness.SetLiveTurnForTest(live2)

	pBody := map[string]any{
		"approved": true,
		"feedback": "Approved, proceed with implementation",
	}
	pBytes, _ := json.Marshal(pBody)
	reqP := httptest.NewRequest("POST", "/api/harness/plan/approve", bytes.NewReader(pBytes))
	reqP.Header.Set("Content-Type", "application/json")
	wP := httptest.NewRecorder()
	handler.ServeHTTP(wP, reqP)

	if wP.Code != http.StatusOK {
		t.Fatalf("expected 200 from POST /api/harness/plan/approve, got %d: %s", wP.Code, wP.Body.String())
	}
	var resP map[string]any
	_ = json.NewDecoder(wP.Body).Decode(&resP)
	if resP["status"] != "ok" || resP["approved"] != true {
		t.Errorf("unexpected plan approval response: %+v", resP)
	}
}

func TestUsageAndContextEndpoints(t *testing.T) {
	srv, tempDir := setupTestServer(t)
	defer os.RemoveAll(tempDir)
	handler := srv.Routes()

	// 1. GET /api/harness/usage
	reqUsage := httptest.NewRequest("GET", "/api/harness/usage", nil)
	wUsage := httptest.NewRecorder()
	handler.ServeHTTP(wUsage, reqUsage)

	if wUsage.Code != http.StatusOK {
		t.Fatalf("expected 200 from GET /api/harness/usage, got %d: %s", wUsage.Code, wUsage.Body.String())
	}
	var usageRes map[string]any
	_ = json.NewDecoder(wUsage.Body).Decode(&usageRes)
	if _, ok := usageRes["markdown_report"]; !ok {
		t.Errorf("expected markdown_report in usage response: %+v", usageRes)
	}

	// 2. GET /api/harness/context
	reqCtx := httptest.NewRequest("GET", "/api/harness/context", nil)
	wCtx := httptest.NewRecorder()
	handler.ServeHTTP(wCtx, reqCtx)

	if wCtx.Code != http.StatusOK {
		t.Fatalf("expected 200 from GET /api/harness/context, got %d: %s", wCtx.Code, wCtx.Body.String())
	}
	var ctxRes map[string]any
	_ = json.NewDecoder(wCtx.Body).Decode(&ctxRes)
	if _, ok := ctxRes["total_context_tokens"]; !ok {
		t.Errorf("expected total_context_tokens in context response: %+v", ctxRes)
	}
	if _, ok := ctxRes["markdown_report"]; !ok {
		t.Errorf("expected markdown_report in context response: %+v", ctxRes)
	}
}

func TestWebProxy(t *testing.T) {
	srv, tempDir := setupTestServer(t)
	defer os.RemoveAll(tempDir)
	handler := srv.corsMiddleware(srv.authMiddleware(srv.Routes()))

	// 1. Missing url query param
	reqBad := httptest.NewRequest("GET", "/api/harness/web-proxy", nil)
	wBad := httptest.NewRecorder()
	handler.ServeHTTP(wBad, reqBad)
	if wBad.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing url, got %d", wBad.Code)
	}

	// 2. HTML target server
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("<!DOCTYPE html><html><head><title>Test Page</title></head><body><h1>Hello Proxy</h1><a href=\"/subpage\">Link</a></body></html>"))
	}))
	defer upstreamServer.Close()

	reqProxy := httptest.NewRequest("GET", "/api/harness/web-proxy?url="+upstreamServer.URL, nil)
	wProxy := httptest.NewRecorder()
	handler.ServeHTTP(wProxy, reqProxy)

	if wProxy.Code != http.StatusOK {
		t.Fatalf("expected 200 from web-proxy, got %d: %s", wProxy.Code, wProxy.Body.String())
	}
	if wProxy.Header().Get("X-Frame-Options") != "" {
		t.Errorf("expected X-Frame-Options to be stripped, got: %s", wProxy.Header().Get("X-Frame-Options"))
	}
	if wProxy.Header().Get("Content-Security-Policy") != "" {
		t.Errorf("expected Content-Security-Policy to be stripped, got: %s", wProxy.Header().Get("Content-Security-Policy"))
	}
	if wProxy.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Errorf("expected Access-Control-Allow-Origin '*', got: %s", wProxy.Header().Get("Access-Control-Allow-Origin"))
	}

	respBody := wProxy.Body.String()
	if !strings.Contains(respBody, "<base href=") {
		t.Errorf("expected injected base href, got: %s", respBody)
	}
	if !strings.Contains(respBody, "unjess-browser-nav") {
		t.Errorf("expected injected navigation bridge script, got: %s", respBody)
	}

	// 3. Non-HTML target server
	upstreamJSON := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"test": "ok"}`))
	}))
	defer upstreamJSON.Close()

	reqJSON := httptest.NewRequest("GET", "/api/harness/web-proxy?url="+upstreamJSON.URL, nil)
	wJSON := httptest.NewRecorder()
	handler.ServeHTTP(wJSON, reqJSON)

	if wJSON.Code != http.StatusOK {
		t.Fatalf("expected 200 for JSON proxy, got %d", wJSON.Code)
	}
	if wJSON.Body.String() != `{"test": "ok"}` {
		t.Errorf("expected raw json body, got: %s", wJSON.Body.String())
	}
}

func TestBrowserAction(t *testing.T) {
	srv, tempDir := setupTestServer(t)
	defer os.RemoveAll(tempDir)
	handler := srv.corsMiddleware(srv.authMiddleware(srv.Routes()))

	// Test invalid JSON
	reqBad := httptest.NewRequest("POST", "/api/harness/browser/action", bytes.NewReader([]byte("{invalid-json")))
	wBad := httptest.NewRecorder()
	handler.ServeHTTP(wBad, reqBad)
	if wBad.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid JSON, got %d", wBad.Code)
	}

	// Test valid action (cli fallback or screenshot)
	body := map[string]any{
		"action":    "cli",
		"text":      "--version",
		"workspace": tempDir,
	}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/api/harness/browser/action", bytes.NewReader(b))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 from browser/action, got %d: %s", w.Code, w.Body.String())
	}
	var res map[string]any
	if err := json.NewDecoder(w.Body).Decode(&res); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if res["status"] != "ok" {
		t.Errorf("expected status ok, got %v", res["status"])
	}
	if _, ok := res["vision_data_uri"]; !ok {
		t.Errorf("expected vision_data_uri in response: %+v", res)
	}
}

func TestScheduleEndpoints(t *testing.T) {
	srv, tempDir := setupTestServer(t)
	defer os.RemoveAll(tempDir)
	handler := srv.corsMiddleware(srv.authMiddleware(srv.Routes()))

	// 1. Initial list should be empty or valid
	reqList := httptest.NewRequest("GET", "/api/schedules", nil)
	wList := httptest.NewRecorder()
	handler.ServeHTTP(wList, reqList)
	if wList.Code != http.StatusOK {
		t.Fatalf("expected 200 from GET /api/schedules, got %d", wList.Code)
	}

	// 2. Create schedule
	createBody := map[string]any{
		"title":           "Daily Code Cleanup",
		"cron_expression": "0 22 * * *",
		"action":          "command",
		"prompt":          "echo 'Schedule Executed'",
		"workspace":       tempDir,
		"enabled":         true,
	}
	cb, _ := json.Marshal(createBody)
	reqCreate := httptest.NewRequest("POST", "/api/schedules", bytes.NewReader(cb))
	wCreate := httptest.NewRecorder()
	handler.ServeHTTP(wCreate, reqCreate)
	if wCreate.Code != http.StatusOK {
		t.Fatalf("expected 200 from POST /api/schedules, got %d: %s", wCreate.Code, wCreate.Body.String())
	}

	var resCreate map[string]any
	_ = json.NewDecoder(wCreate.Body).Decode(&resCreate)
	taskMap, ok := resCreate["schedule"].(map[string]any)
	if !ok {
		t.Fatalf("expected schedule object in response: %+v", resCreate)
	}
	taskID := taskMap["id"].(string)
	if taskID == "" {
		t.Fatalf("expected non-empty task ID")
	}

	// 3. Toggle schedule
	reqToggle := httptest.NewRequest("POST", "/api/schedules/"+taskID+"/toggle", nil)
	wToggle := httptest.NewRecorder()
	handler.ServeHTTP(wToggle, reqToggle)
	if wToggle.Code != http.StatusOK {
		t.Fatalf("expected 200 from POST toggle, got %d", wToggle.Code)
	}
	var resToggle map[string]any
	_ = json.NewDecoder(wToggle.Body).Decode(&resToggle)
	togTask := resToggle["schedule"].(map[string]any)
	if togTask["enabled"].(bool) != false {
		t.Errorf("expected schedule to be disabled after toggle")
	}

	// 4. Run schedule now
	reqRun := httptest.NewRequest("POST", "/api/schedules/"+taskID+"/run", nil)
	wRun := httptest.NewRecorder()
	handler.ServeHTTP(wRun, reqRun)
	if wRun.Code != http.StatusOK {
		t.Fatalf("expected 200 from run, got %d", wRun.Code)
	}

	// 5. Update schedule
	updateBody := map[string]any{
		"title": "Nightly Build & Test",
	}
	ub, _ := json.Marshal(updateBody)
	reqUpdate := httptest.NewRequest("PUT", "/api/schedules/"+taskID, bytes.NewReader(ub))
	wUpdate := httptest.NewRecorder()
	handler.ServeHTTP(wUpdate, reqUpdate)
	if wUpdate.Code != http.StatusOK {
		t.Fatalf("expected 200 from PUT update, got %d", wUpdate.Code)
	}

	// 6. Delete schedule
	reqDelete := httptest.NewRequest("DELETE", "/api/schedules/"+taskID, nil)
	wDelete := httptest.NewRecorder()
	handler.ServeHTTP(wDelete, reqDelete)
	if wDelete.Code != http.StatusOK {
		t.Fatalf("expected 200 from DELETE, got %d", wDelete.Code)
	}
}


