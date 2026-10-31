package mcp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestMCPPresets(t *testing.T) {
	if len(MCPPresets) != 4 {
		t.Fatalf("expected 4 presets, got %d", len(MCPPresets))
	}

	expectedOrder := []string{"rea", "filesystem", "github", "fetch"}
	for i, name := range expectedOrder {
		if MCPPresets[i].Name != name {
			t.Errorf("preset %d: expected name %q, got %q", i, name, MCPPresets[i].Name)
		}
	}
}

func TestConfigLoadSaveDelete(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "mcp.json")

	// Initially empty
	cfg, err := LoadMCPConfigFromPath(cfgPath)
	if err != nil {
		t.Fatalf("LoadMCPConfigFromPath failed: %v", err)
	}
	if len(cfg) != 0 {
		t.Fatalf("expected 0 configs, got %d", len(cfg))
	}

	// Save a server
	s1 := ServerConfig{
		Name:        "test-server",
		Command:     "npx",
		Args:        []string{"-y", "test-pkg"},
		Enabled:     true,
		Description: "Test Server",
	}
	if err := SaveMCPServerToPath(cfgPath, s1); err != nil {
		t.Fatalf("SaveMCPServerToPath failed: %v", err)
	}

	// Reload
	cfg, err = LoadMCPConfigFromPath(cfgPath)
	if err != nil {
		t.Fatalf("LoadMCPConfigFromPath failed: %v", err)
	}
	if len(cfg) != 1 {
		t.Fatalf("expected 1 config, got %d", len(cfg))
	}
	if cfg["test-server"].Description != "Test Server" {
		t.Errorf("expected Description 'Test Server', got %q", cfg["test-server"].Description)
	}

	// Delete server
	if err := DeleteMCPServerFromPath(cfgPath, "test-server"); err != nil {
		t.Fatalf("DeleteMCPServerFromPath failed: %v", err)
	}

	cfg, err = LoadMCPConfigFromPath(cfgPath)
	if err != nil {
		t.Fatalf("LoadMCPConfigFromPath failed: %v", err)
	}
	if len(cfg) != 0 {
		t.Fatalf("expected 0 configs after delete, got %d", len(cfg))
	}
}

func TestResolveMashinaToken(t *testing.T) {
	// From Header
	cfg1 := ServerConfig{
		Headers: map[string]string{"Authorization": "Bearer test-header-tok"},
	}
	tok1 := ResolveMashinaToken(cfg1)
	if tok1 != "test-header-tok" {
		t.Errorf("expected 'test-header-tok', got %q", tok1)
	}

	// From Env map
	cfg2 := ServerConfig{
		Env: map[string]string{"MASHINA_COLLAB_TOKEN": "test-env-tok"},
	}
	tok2 := ResolveMashinaToken(cfg2)
	if tok2 != "test-env-tok" {
		t.Errorf("expected 'test-env-tok', got %q", tok2)
	}

	// From OS Env
	os.Setenv("MASHINA_COLLAB_TOKEN", "os-tok")
	defer os.Unsetenv("MASHINA_COLLAB_TOKEN")
	cfg3 := ServerConfig{}
	tok3 := ResolveMashinaToken(cfg3)
	if tok3 != "os-tok" {
		t.Errorf("expected 'os-tok', got %q", tok3)
	}
}

func TestHTTPTransportAndManager(t *testing.T) {
	// Mock HTTP server responding to MCP initialize, tools/list, and tools/call
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req RPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "initialize":
			resp := RPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result:  json.RawMessage(`{"capabilities":{},"serverInfo":{"name":"mock-server","version":"1.0"}}`),
			}
			_ = json.NewEncoder(w).Encode(resp)
		case "tools/list":
			resp := RPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result:  json.RawMessage(`{"tools":[{"name":"echo","description":"Echo back text","inputSchema":{"type":"object"}}]}`),
			}
			_ = json.NewEncoder(w).Encode(resp)
		case "tools/call":
			resp := RPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result:  json.RawMessage(`{"content":[{"type":"text","text":"echoed result"}]}`),
			}
			_ = json.NewEncoder(w).Encode(resp)
		default:
			// notifications etc
			w.WriteHeader(200)
		}
	}))
	defer server.Close()

	configs := map[string]ServerConfig{
		"mock": {
			Name:    "mock",
			URL:     server.URL,
			Enabled: true,
		},
	}

	mgr := NewManagerWithConfig(configs, "")
	defer mgr.DisconnectAll()

	if err := mgr.Connect("mock"); err != nil {
		t.Fatalf("mgr.Connect failed: %v", err)
	}

	tools := mgr.AvailableTools()
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	if tools[0].PrefixedName != "mcp_mock_echo" {
		t.Errorf("expected prefixed name 'mcp_mock_echo', got %q", tools[0].PrefixedName)
	}

	out, err := mgr.CallTool("mcp_mock_echo", map[string]any{"msg": "hi"})
	if err != nil {
		t.Fatalf("CallTool failed: %v", err)
	}
	if out != "echoed result" {
		t.Errorf("expected output 'echoed result', got %q", out)
	}

	// Test RefreshMCPTools
	ref := mgr.RefreshMCPTools()
	if ref["status"] != "ok" {
		t.Errorf("expected status 'ok', got %v", ref["status"])
	}
}

func TestResolveMashinaToken_FromEnvAuthorization(t *testing.T) {
	// 1. Token in cfg.Env["Authorization"]
	cfg1 := ServerConfig{
		Name:    "mashina-collab",
		Command: "https://mashina.si/api/collab/mcp",
		URL:     "https://mashina.si/api/collab/mcp",
		Env: map[string]string{
			"Authorization":        "mca_test_secret_123",
			"MASHINA_COLLAB_TOKEN": "",
		},
		Headers: map[string]string{
			"Authorization": "",
		},
	}
	tok1 := ResolveMashinaToken(cfg1)
	if tok1 != "mca_test_secret_123" {
		t.Errorf("expected token 'mca_test_secret_123', got %q", tok1)
	}

	// 2. Token in cfg.Env["Authorization"] with "Bearer " prefix
	cfg2 := ServerConfig{
		Name:    "mashina-collab",
		Command: "https://mashina.si/api/collab/mcp",
		Env: map[string]string{
			"authorization": "Bearer mca_bearer_token",
		},
	}
	tok2 := ResolveMashinaToken(cfg2)
	if tok2 != "mca_bearer_token" {
		t.Errorf("expected token 'mca_bearer_token', got %q", tok2)
	}
}

func TestLoadMCPConfigFromPath_PopulatesAuthorizationHeader(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "mcp.json")

	// Raw JSON matching the user's ~/.unjess/mcp.json
	rawJSON := `{
		"mcpServers": {
			"mashina-collab": {
				"command": "https://mashina.si/api/collab/mcp",
				"url": "https://mashina.si/api/collab/mcp",
				"enabled": true,
				"env": {
					"Authorization": "mca_loaded_token",
					"MASHINA_COLLAB_TOKEN": ""
				},
				"headers": {
					"Authorization": ""
				}
			}
		}
	}`
	if err := os.WriteFile(cfgPath, []byte(rawJSON), 0644); err != nil {
		t.Fatalf("failed to write test mcp.json: %v", err)
	}

	servers, err := LoadMCPConfigFromPath(cfgPath)
	if err != nil {
		t.Fatalf("LoadMCPConfigFromPath failed: %v", err)
	}

	srv, ok := servers["mashina-collab"]
	if !ok {
		t.Fatalf("expected server 'mashina-collab' to be loaded")
	}

	if srv.Headers["Authorization"] != "Bearer mca_loaded_token" {
		t.Errorf("expected header 'Bearer mca_loaded_token', got %q", srv.Headers["Authorization"])
	}
}
