package config

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"unjess-daemon/internal/version"
)

func TestDefaultSettings(t *testing.T) {
	s := NewDefaultSettings()
	if s.Provider != "ollama" {
		t.Errorf("expected provider 'ollama', got '%s'", s.Provider)
	}
	if s.Model != "qwen2.5-coder:7b" {
		t.Errorf("expected model 'qwen2.5-coder:7b', got '%s'", s.Model)
	}
	if s.OllamaBaseURL != "http://localhost:11434" {
		t.Errorf("expected ollama_base_url 'http://localhost:11434', got '%s'", s.OllamaBaseURL)
	}
	if s.MaxIterations != 200 {
		t.Errorf("expected max_iterations 200, got %d", s.MaxIterations)
	}
	if s.SubagentMaxTurns != 300 {
		t.Errorf("expected subagent_max_turns 300, got %d", s.SubagentMaxTurns)
	}
	if s.ConfirmCommands != false {
		t.Errorf("expected confirm_commands false, got %v", s.ConfirmCommands)
	}
	if s.CommandTimeout != 1200 {
		t.Errorf("expected command_timeout 1200, got %d", s.CommandTimeout)
	}
	if s.LLMTimeout != 300.0 {
		t.Errorf("expected llm_timeout 300.0, got %f", s.LLMTimeout)
	}
	if s.AccentColor != "#7dd3fc" {
		t.Errorf("expected accent_color '#7dd3fc', got '%s'", s.AccentColor)
	}
	if s.AutoUpdateEnabled != true {
		t.Errorf("expected auto_update_enabled true, got %v", s.AutoUpdateEnabled)
	}
	if s.UpdateServerURL != "https://unjess.si/releases/version.json" {
		t.Errorf("expected update_server_url 'https://unjess.si/releases/version.json', got '%s'", s.UpdateServerURL)
	}
	if len(s.IgnorePatterns) == 0 {
		t.Errorf("expected default ignore patterns, got empty")
	}
}

func TestSaveAndLoadConfig(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	keysPath := filepath.Join(tmpDir, "keys.yaml")

	s := NewDefaultSettings()
	s.Model = "deepseek-coder:6.7b"
	s.Provider = "ollama"
	s.APIKeys["openai"] = "sk-test-123"
	s.APIKeys["anthropic"] = "sk-ant-456"
	s.Workspace = "/some/temp/workspace"
	s.MaxIterations = 42

	if err := SaveConfigToPath(s, configPath, keysPath); err != nil {
		t.Fatalf("SaveConfigToPath failed: %v", err)
	}

	// Verify config.yaml does NOT contain api_keys or workspace
	cfgBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("failed to read config.yaml: %v", err)
	}
	cfgContent := string(cfgBytes)
	if strings.Contains(cfgContent, "api_keys:") {
		t.Errorf("config.yaml should not contain api_keys, found in:\n%s", cfgContent)
	}
	if strings.Contains(cfgContent, "workspace:") {
		t.Errorf("config.yaml should not contain workspace, found in:\n%s", cfgContent)
	}

	// Verify keys.yaml contains the API keys
	keysBytes, err := os.ReadFile(keysPath)
	if err != nil {
		t.Fatalf("failed to read keys.yaml: %v", err)
	}
	keysContent := string(keysBytes)
	if !strings.Contains(keysContent, "openai: sk-test-123") {
		t.Errorf("keys.yaml missing openai key:\n%s", keysContent)
	}

	// Load back
	loaded := LoadConfigFromPath(configPath, keysPath)
	if loaded.Model != "deepseek-coder:6.7b" {
		t.Errorf("expected model 'deepseek-coder:6.7b', got '%s'", loaded.Model)
	}
	if loaded.MaxIterations != 42 {
		t.Errorf("expected max_iterations 42, got %d", loaded.MaxIterations)
	}
	if loaded.APIKeys["openai"] != "sk-test-123" {
		t.Errorf("expected openai key 'sk-test-123', got '%s'", loaded.APIKeys["openai"])
	}
	if loaded.APIKeys["anthropic"] != "sk-ant-456" {
		t.Errorf("expected anthropic key 'sk-ant-456', got '%s'", loaded.APIKeys["anthropic"])
	}
}

func TestSerializeSettingsDict(t *testing.T) {
	s := NewDefaultSettings()
	s.Model = "claude-3-7-sonnet-latest"
	s.Provider = "anthropic"
	s.APIKeys["anthropic"] = "sk-ant-test"
	s.APIKeys["ollama-api"] = "ollama-key-xyz"

	dict := SerializeSettingsDict(s)

	requiredKeys := []string{
		"default_model", "model", "provider", "ollama_base_url", "ollama_api_base_url",
		"lmstudio_base_url", "llamacpp_base_url", "kimi_base_url", "qwen_base_url",
		"mashina_url", "api_keys", "ollama_api_key", "has_ollama_key", "openai_api_key",
		"anthropic_api_key", "google_api_key", "groq_api_key", "mistral_api_key",
		"xai_api_key", "openrouter_api_key", "cerebras_api_key", "kimi_api_key",
		"qwen_api_key", "mashina_collab_token", "max_iterations",
		"subagent_max_turns", "command_timeout", "llm_timeout", "planning_mode",
		"confirm_commands", "enable_compaction", "enable_stuck_detection",
		"context_window_override", "ignore_patterns", "verbosity", "language",
		"accent_color", "show_diffs", "show_cost", "show_stats", "mobile_enabled",
		"mobile_port", "mobile_pin", "max_cost_per_session", "max_tokens_per_message",
		"free_mode_enabled", "fallback_chain", "enable_rag",
		"shadow_worktree_mode", "smart_model_routing",
		"auto_update_enabled", "update_server_url", "current_version",
	}

	for _, k := range requiredKeys {
		if _, ok := dict[k]; !ok {
			t.Errorf("SerializeSettingsDict missing key: %s", k)
		}
	}

	if dict["auto_update_enabled"] != true {
		t.Errorf("expected auto_update_enabled true, got %v", dict["auto_update_enabled"])
	}
	if dict["current_version"] != version.Current {
		t.Errorf("expected current_version %q, got %v", version.Current, dict["current_version"])
	}
	if dict["update_server_url"] != "https://unjess.si/releases/version.json" {
		t.Errorf("expected default update_server_url, got %v", dict["update_server_url"])
	}

	if dict["anthropic_api_key"] != "sk-ant-test" {
		t.Errorf("expected anthropic_api_key 'sk-ant-test', got '%v'", dict["anthropic_api_key"])
	}
	if dict["has_ollama_key"] != true {
		t.Errorf("expected has_ollama_key true, got %v", dict["has_ollama_key"])
	}
	if dict["ollama_api_key"] != "ollama-key-xyz" {
		t.Errorf("expected ollama_api_key 'ollama-key-xyz', got '%v'", dict["ollama_api_key"])
	}
}

func TestApplySettingsUpdate(t *testing.T) {
	tmpDir := t.TempDir()
	origConfigPath := DefaultConfigPath
	origKeysPath := DefaultKeysPath
	origMCPPath := DefaultMCPPath
	defer func() {
		DefaultConfigPath = origConfigPath
		DefaultKeysPath = origKeysPath
		DefaultMCPPath = origMCPPath
	}()

	DefaultConfigPath = filepath.Join(tmpDir, "config.yaml")
	DefaultKeysPath = filepath.Join(tmpDir, "keys.yaml")
	DefaultMCPPath = filepath.Join(tmpDir, "mcp.json")

	// Create dummy mcp.json
	initialMCP := map[string]any{
		"mcpServers": map[string]any{
			"mashina-collab": map[string]any{
				"headers": map[string]any{
					"Authorization": "Bearer old-mashina",
				},
				"env": map[string]any{
					"MASHINA_COLLAB_TOKEN": "old-mashina",
				},
			},
		},
	}
	mcpBytes, _ := json.Marshal(initialMCP)
	_ = os.WriteFile(DefaultMCPPath, mcpBytes, 0644)

	s := NewDefaultSettings()

	body := map[string]any{
		"default_model":         "gpt-4o",
		"provider":              "openai",
		"openai_api_key":        "sk-new-openai",
		"mashina_collab_token":  "mashina-new-token",
		"max_iterations":        150,
		"planning_mode":         "on",
		"show_diffs":            false,
		"accent_color":          "#10b981",
		"fallback_chain":        "openai/gpt-4o,groq/llama-3.3-70b",
		"ignore_patterns":       ".git\nnode_modules\ncustom_dir",
		"auto_update_enabled":   false,
		"update_server_url":     "https://custom.example.com/version.json",
		"shadow_worktree_mode":  true,
		"smart_model_routing":   true,
	}

	if err := ApplySettingsUpdate(s, body); err != nil {
		t.Fatalf("ApplySettingsUpdate failed: %v", err)
	}

	if s.ShadowWorktreeMode != true {
		t.Errorf("expected shadow_worktree_mode true, got %v", s.ShadowWorktreeMode)
	}
	if s.SmartModelRouting != true {
		t.Errorf("expected smart_model_routing true, got %v", s.SmartModelRouting)
	}

	if s.Model != "gpt-4o" {
		t.Errorf("expected model 'gpt-4o', got '%s'", s.Model)
	}
	if s.Provider != "openai" {
		t.Errorf("expected provider 'openai', got '%s'", s.Provider)
	}
	if s.APIKeys["openai"] != "sk-new-openai" {
		t.Errorf("expected openai key 'sk-new-openai', got '%s'", s.APIKeys["openai"])
	}
	if s.MaxIterations != 150 {
		t.Errorf("expected max_iterations 150, got %d", s.MaxIterations)
	}
	if s.PlanningMode != "on" {
		t.Errorf("expected planning_mode 'on', got '%s'", s.PlanningMode)
	}
	if s.ShowDiffs != false {
		t.Errorf("expected show_diffs false, got %v", s.ShowDiffs)
	}
	if s.AccentColor != "#10b981" {
		t.Errorf("expected accent_color '#10b981', got '%s'", s.AccentColor)
	}
	if s.AutoUpdateEnabled != false {
		t.Errorf("expected auto_update_enabled false, got %v", s.AutoUpdateEnabled)
	}
	if s.UpdateServerURL != "https://custom.example.com/version.json" {
		t.Errorf("expected update_server_url 'https://custom.example.com/version.json', got '%s'", s.UpdateServerURL)
	}
	if len(s.FallbackChain) != 2 || s.FallbackChain[0] != "openai/gpt-4o" {
		t.Errorf("expected fallback chain parsed, got %v", s.FallbackChain)
	}
	if len(s.IgnorePatterns) != 3 || s.IgnorePatterns[2] != "custom_dir" {
		t.Errorf("expected ignore patterns parsed, got %v", s.IgnorePatterns)
	}

	// Verify mcp.json was updated
	mcpUpdatedBytes, err := os.ReadFile(DefaultMCPPath)
	if err != nil {
		t.Fatalf("failed to read updated mcp.json: %v", err)
	}
	var updatedMCP map[string]any
	if err := json.Unmarshal(mcpUpdatedBytes, &updatedMCP); err != nil {
		t.Fatalf("invalid json in updated mcp.json: %v", err)
	}
	servers := updatedMCP["mcpServers"].(map[string]any)
	mash := servers["mashina-collab"].(map[string]any)
	mashHeaders := mash["headers"].(map[string]any)
	if mashHeaders["Authorization"] != "Bearer mashina-new-token" {
		t.Errorf("expected mashina auth header updated, got '%v'", mashHeaders["Authorization"])
	}
}

func TestDiscoverModelsWithMockOllama(t *testing.T) {
	// Create mock Ollama server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{
				"models": [
					{
						"name": "qwen2.5-coder:7b",
						"model": "qwen2.5-coder:7b",
						"size": 4700000000,
						"details": {
							"family": "qwen2",
							"parameter_size": "7.6B",
							"quantization_level": "Q4_K_M"
						},
						"capabilities": ["tools"]
					}
				]
			}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer ts.Close()

	s := NewDefaultSettings()
	s.APIKeys["openai"] = "sk-test" // should include OpenAI models

	res := DiscoverModels(s, ts.URL, "")
	if res["status"] != "online" {
		t.Errorf("expected status online, got %v", res["status"])
	}
	if res["ollama_online"] != true {
		t.Errorf("expected ollama_online true, got %v", res["ollama_online"])
	}

	models, ok := res["models"].([]map[string]any)
	if !ok || len(models) == 0 {
		t.Fatalf("expected models list, got %v", res["models"])
	}

	foundOllama := false
	foundOpenAI := false
	for _, m := range models {
		if m["id"] == "qwen2.5-coder:7b" {
			foundOllama = true
			if m["family"] != "qwen2" {
				t.Errorf("expected family 'qwen2', got '%v'", m["family"])
			}
			if m["quantization"] != "Q4_K_M" {
				t.Errorf("expected quantization 'Q4_K_M', got '%v'", m["quantization"])
			}
			if m["size"].(int64) != 4700000000 {
				t.Errorf("expected size 4700000000, got %v", m["size"])
			}
		}
		if m["id"] == "gpt-6-astra" {
			foundOpenAI = true
		}
	}

	if !foundOllama {
		t.Errorf("expected qwen2.5-coder:7b in models list")
	}
	if !foundOpenAI {
		t.Errorf("expected gpt-6-astra in models list since openai key was configured")
	}
}

func TestGetNetworkInfo(t *testing.T) {
	s := NewDefaultSettings()
	s.MobilePort = 8080
	s.MobilePin = "1234"

	info := GetNetworkInfo(s, 8080)
	if info["status"] != "ok" {
		t.Errorf("expected status 'ok', got %v", info["status"])
	}
	if info["port"] != 8080 {
		t.Errorf("expected port 8080, got %v", info["port"])
	}
	if info["pin"] != "1234" {
		t.Errorf("expected pin '1234', got %v", info["pin"])
	}
	qrURL, ok := info["qr_data_url"].(string)
	if !ok || !strings.HasPrefix(qrURL, "data:image/png;base64,") {
		t.Errorf("expected valid base64 PNG QR code data URL, got: %v", info["qr_data_url"])
	}
	companionURL, ok := info["companion_url"].(string)
	if !ok || !strings.HasPrefix(companionURL, "http://") {
		t.Errorf("expected valid companion_url, got: %v", info["companion_url"])
	}
}
