package config

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"unjess-daemon/internal/version"

	"gopkg.in/yaml.v3"
)

// Default paths and values
var (
	DefaultConfigDir   string
	DefaultConfigPath  string
	DefaultKeysPath    string
	DefaultMCPPath     string
	DefaultIgnoreRules = []string{
		".git", "__pycache__", "node_modules", ".venv", "venv",
		".mypy_cache", ".pytest_cache", ".ruff_cache", "dist", "build",
		"*.pyc", "*.pyo", ".DS_Store", "Thumbs.db",
	}
)

func init() {
	if envDir := strings.TrimSpace(os.Getenv("UNJESS_CONFIG_DIR")); envDir != "" {
		DefaultConfigDir = envDir
	} else if isRunningUnderGoTest() {
		if tmpDir, err := os.MkdirTemp("", "unjess_test_cfg_*"); err == nil {
			DefaultConfigDir = tmpDir
		} else {
			DefaultConfigDir = filepath.Join(os.TempDir(), "unjess_test_cfg")
		}
	} else {
		home, err := os.UserHomeDir()
		if err != nil {
			home = "."
		}
		DefaultConfigDir = filepath.Join(home, ".unjess")
	}
	DefaultConfigPath = filepath.Join(DefaultConfigDir, "config.yaml")
	DefaultKeysPath = filepath.Join(DefaultConfigDir, "keys.yaml")
	DefaultMCPPath = filepath.Join(DefaultConfigDir, "mcp.json")
}

func isRunningUnderGoTest() bool {
	if flag.Lookup("test.v") != nil || flag.Lookup("test.run") != nil {
		return true
	}
	if len(os.Args) > 0 {
		arg0 := strings.ToLower(filepath.Base(os.Args[0]))
		if strings.HasSuffix(arg0, ".test") || strings.HasSuffix(arg0, ".test.exe") || strings.Contains(arg0, "_test") {
			return true
		}
	}
	return false
}

// Settings holds all runtime configuration for Unjess, maintaining 100% compatibility
// with src/unjess/config.py and src/unjess/api/routes.py.
type Settings struct {
	// Model / Provider
	Provider string `yaml:"provider" json:"provider"`
	Model    string `yaml:"model" json:"model"`

	// API Keys (saved in keys.yaml, not config.yaml)
	APIKeys    map[string]string   `yaml:"api_keys,omitempty" json:"api_keys"`
	APIKeyPool map[string][]string `yaml:"api_key_pool,omitempty" json:"api_key_pool,omitempty"`

	// Base URLs for LLM backends
	OllamaBaseURL      string `yaml:"ollama_base_url" json:"ollama_base_url"`
	OllamaAPIBaseURL   string `yaml:"ollama_api_base_url" json:"ollama_api_base_url"`
	LlamaCppBaseURL    string `yaml:"llamacpp_base_url" json:"llamacpp_base_url"`
	LMStudioBaseURL    string `yaml:"lmstudio_base_url" json:"lmstudio_base_url"`
	KimiBaseURL        string `yaml:"kimi_base_url" json:"kimi_base_url"`
	QwenBaseURL        string `yaml:"qwen_base_url" json:"qwen_base_url"`
	MashinaURL         string `yaml:"mashina_url" json:"mashina_url"`
	MashinaCollabToken string `yaml:"mashina_collab_token" json:"mashina_collab_token"`

	// Workspace
	Workspace string `yaml:"workspace,omitempty" json:"workspace"`

	// Fallback Chain
	FallbackChain []string `yaml:"fallback_chain" json:"fallback_chain"`

	// Mobile Companion & Security
	MobilePin          string `yaml:"mobile_pin" json:"mobile_pin"`
	EnablePinAuth      bool   `yaml:"enable_pin_auth" json:"enable_pin_auth"`
	AllowNetworkAccess bool   `yaml:"allow_network_access" json:"allow_network_access"`
	NetworkPort        int    `yaml:"network_port" json:"network_port"`
	MobileEnabled      bool   `yaml:"mobile_enabled" json:"mobile_enabled"`
	MobilePort         int    `yaml:"mobile_port" json:"mobile_port"`

	// Behavior & Execution
	MaxIterations          int     `yaml:"max_iterations" json:"max_iterations"`
	SubagentMaxTurns       int     `yaml:"subagent_max_turns" json:"subagent_max_turns"`
	SubagentModel          string  `yaml:"subagent_model" json:"subagent_model"`
	AskBeforeSubagentSpawn bool    `yaml:"ask_before_subagent_spawn" json:"ask_before_subagent_spawn"`
	ConfirmCommands        bool    `yaml:"confirm_commands" json:"confirm_commands"`
	CommandTimeout         int     `yaml:"command_timeout" json:"command_timeout"`
	LLMTimeout             float64 `yaml:"llm_timeout" json:"llm_timeout"`
	EnableStuckDetection   bool    `yaml:"enable_stuck_detection" json:"enable_stuck_detection"`

	// Display & Theme
	Verbosity   string `yaml:"verbosity" json:"verbosity"`
	ShowDiffs   bool   `yaml:"show_diffs" json:"show_diffs"`
	ShowCost    bool   `yaml:"show_cost" json:"show_cost"`
	ShowStats   bool   `yaml:"show_stats" json:"show_stats"`
	AccentColor string `yaml:"accent_color" json:"accent_color"`

	// Budget & Free Mode
	MaxCostPerSession   float64 `yaml:"max_cost_per_session" json:"max_cost_per_session"`
	MaxTokensPerMessage int     `yaml:"max_tokens_per_message" json:"max_tokens_per_message"`
	FreeModeEnabled     bool    `yaml:"free_mode_enabled" json:"free_mode_enabled"`

	// Planning Mode & Context
	PlanningMode          string `yaml:"planning_mode" json:"planning_mode"`
	ReasoningEffort       string `yaml:"reasoning_effort" json:"reasoning_effort"`
	EnableCompaction      bool   `yaml:"enable_compaction" json:"enable_compaction"`
	ContextWindowOverride int    `yaml:"context_window_override" json:"context_window_override"`

	// RAG / Embeddings
	EnableRAG bool `yaml:"enable_rag" json:"enable_rag"`

	// Localization
	Language string `yaml:"language" json:"language"`

	// Virtual / Studio Sandbox Mode
	VirtualMode bool `yaml:"virtual_mode" json:"virtual_mode"`

	// Frontier & Worktree v2.1.0
	ShadowWorktreeMode bool `yaml:"shadow_worktree_mode" json:"shadow_worktree_mode"`
	SmartModelRouting  bool `yaml:"smart_model_routing" json:"smart_model_routing"`

	// Auto-Updater
	AutoUpdateEnabled bool   `yaml:"auto_update_enabled" json:"auto_update_enabled"`
	UpdateServerURL   string `yaml:"update_server_url" json:"update_server_url"`

	// Ignore Patterns
	IgnorePatterns []string `yaml:"ignore_patterns" json:"ignore_patterns"`
}

// NewDefaultSettings returns a Settings instance populated with all defaults.
func NewDefaultSettings() *Settings {
	ignorePatterns := make([]string, len(DefaultIgnoreRules))
	copy(ignorePatterns, DefaultIgnoreRules)

	return &Settings{
		Provider:               "ollama",
		Model:                  "qwen2.5-coder:7b",
		APIKeys:                make(map[string]string),
		APIKeyPool:             make(map[string][]string),
		OllamaBaseURL:          "http://localhost:11434",
		OllamaAPIBaseURL:       "https://ollama.com",
		LlamaCppBaseURL:        "http://localhost:8080",
		LMStudioBaseURL:        "http://localhost:1234",
		KimiBaseURL:            "https://api.moonshot.ai/v1",
		QwenBaseURL:            "https://dashscope-intl.aliyuncs.com/compatible-mode/v1",
		MashinaURL:             "https://mashina.si",
		MashinaCollabToken:     "",
		Workspace:              ".",
		FallbackChain:          []string{},
		MobilePin:              "",
		EnablePinAuth:          false,
		AllowNetworkAccess:     true,
		NetworkPort:            8080,
		MobileEnabled:          true,
		MobilePort:             8080,
		MaxIterations:          200,
		SubagentMaxTurns:       300,
		SubagentModel:          "inherit",
		AskBeforeSubagentSpawn: false,
		ConfirmCommands:        false,
		CommandTimeout:         1200,
		LLMTimeout:             300.0,
		EnableStuckDetection:   true,
		Verbosity:              "normal",
		ShowDiffs:              true,
		ShowCost:               true,
		ShowStats:              true,
		AccentColor:            "#7dd3fc",
		MaxCostPerSession:      0.0,
		MaxTokensPerMessage:    0,
		FreeModeEnabled:        false,
		PlanningMode:           "auto",
		ReasoningEffort:        "high",
		EnableCompaction:       true,
		ContextWindowOverride:  0,
		EnableRAG:              false,
		Language:               "en",
		VirtualMode:            false,
		ShadowWorktreeMode:     false,
		SmartModelRouting:      false,
		AutoUpdateEnabled:      true,
		UpdateServerURL:        "https://unjess.si/releases/version.json",
		IgnorePatterns:         ignorePatterns,
	}
}

// envKeyMap maps environment variable names to internal provider identifiers.
var envKeyMap = map[string]string{
	"OPENAI_API_KEY":       "openai",
	"ANTHROPIC_API_KEY":    "anthropic",
	"GOOGLE_API_KEY":       "google",
	"GEMINI_API_KEY":       "google",
	"GROQ_API_KEY":         "groq",
	"MISTRAL_API_KEY":      "mistral",
	"XAI_API_KEY":          "xai",
	"OPENROUTER_API_KEY":   "openrouter",
	"CEREBRAS_API_KEY":     "cerebras",
	"MOONSHOT_API_KEY":     "kimi",
	"KIMI_API_KEY":         "kimi",
	"DASHSCOPE_API_KEY":    "qwen",
	"QWEN_API_KEY":         "qwen",
	"OLLAMA_API_KEY":       "ollama-api",
	"MASHINA_COLLAB_TOKEN": "mashina",
	"MASHINA_TOKEN":        "mashina",
}

// loadEnvKeys merges API keys from environment variables into settings.
func loadEnvKeys(s *Settings) {
	if s.APIKeys == nil {
		s.APIKeys = make(map[string]string)
	}
	for envVar, providerName := range envKeyMap {
		val := os.Getenv(envVar)
		if val != "" {
			if curr, exists := s.APIKeys[providerName]; !exists || curr == "" {
				s.APIKeys[providerName] = val
			}
		}
	}

	if s.MashinaCollabToken == "" {
		tok := os.Getenv("MASHINA_COLLAB_TOKEN")
		if tok == "" {
			tok = os.Getenv("MASHINA_TOKEN")
		}
		if tok == "" {
			tok = s.APIKeys["mashina"]
		}
		if tok != "" {
			s.MashinaCollabToken = tok
		}
	}

	// Backward compatibility: if ollama key was saved previously under 'ollama'
	if s.APIKeys["ollama-api"] == "" {
		legacyKey := s.APIKeys["ollama"]
		if legacyKey != "" && legacyKey != "ollama" {
			s.APIKeys["ollama-api"] = legacyKey
		}
	}
}

// loadSavedKeys reads ~/.unjess/keys.yaml and returns active keys and key pools.
func loadSavedKeys(keysPath string) (map[string]string, map[string][]string) {
	keys := make(map[string]string)
	pool := make(map[string][]string)
	if keysPath == "" {
		keysPath = DefaultKeysPath
	}
	data, err := os.ReadFile(keysPath)
	if err != nil {
		return keys, pool
	}
	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil || raw == nil {
		return keys, pool
	}

	if poolRaw, ok := raw["key_pool"].(map[string]any); ok {
		for k, v := range poolRaw {
			if list, ok := v.([]any); ok {
				var strList []string
				for _, item := range list {
					if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
						strList = append(strList, strings.TrimSpace(s))
					}
				}
				if len(strList) > 0 {
					pool[k] = strList
				}
			}
		}
	}

	for k, v := range raw {
		if k == "key_pool" {
			continue
		}
		if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
			keys[k] = strings.TrimSpace(s)
		}
	}
	return keys, pool
}

// SaveKeys persists API keys and key pool to keys.yaml (0600 permissions).
func SaveKeys(apiKeys map[string]string, apiKeyPool map[string][]string, path string) error {
	if path == "" {
		path = DefaultKeysPath
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	toSave := make(map[string]any)
	for k, v := range apiKeys {
		trimmed := strings.TrimSpace(v)
		if trimmed != "" {
			toSave[k] = trimmed
		}
	}

	if apiKeyPool != nil {
		poolClean := make(map[string][]string)
		for k, vList := range apiKeyPool {
			var cleanList []string
			for _, item := range vList {
				trimmed := strings.TrimSpace(item)
				if trimmed != "" {
					cleanList = append(cleanList, trimmed)
				}
			}
			if len(cleanList) > 0 {
				poolClean[k] = cleanList
			}
		}
		if len(poolClean) > 0 {
			toSave["key_pool"] = poolClean
		}
	}

	if len(toSave) == 0 {
		if _, err := os.Stat(path); err == nil {
			_ = os.WriteFile(path, []byte(""), 0600)
		}
		return nil
	}

	data, err := yaml.Marshal(toSave)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		return err
	}
	_ = os.Chmod(path, 0600)
	return nil
}

// LoadConfig loads ~/.unjess/config.yaml, merges ~/.unjess/keys.yaml, and overlays environment variables.
func LoadConfig() *Settings {
	return LoadConfigFromPath(DefaultConfigPath, DefaultKeysPath)
}

// LoadConfigFromPath loads configuration from specific config and keys paths.
func LoadConfigFromPath(configPath, keysPath string) *Settings {
	s := NewDefaultSettings()
	if configPath == "" {
		configPath = DefaultConfigPath
	}
	if keysPath == "" {
		keysPath = DefaultKeysPath
	}

	if data, err := os.ReadFile(configPath); err == nil {
		_ = yaml.Unmarshal(data, s)
	}

	// Merge saved keys from keys.yaml
	savedKeys, savedPool := loadSavedKeys(keysPath)
	for provider, key := range savedKeys {
		if _, exists := s.APIKeys[provider]; !exists || s.APIKeys[provider] == "" {
			s.APIKeys[provider] = key
		}
	}
	for provider, poolList := range savedPool {
		if _, exists := s.APIKeyPool[provider]; !exists || len(s.APIKeyPool[provider]) == 0 {
			s.APIKeyPool[provider] = poolList
		}
	}

	// Overlay environment variables
	loadEnvKeys(s)

	// Resolve default model if empty
	if s.Model == "" && s.Provider != "" {
		defaults := map[string]string{
			"google":     "gemini-2.5-flash",
			"openai":     "gpt-4o-mini",
			"anthropic":  "claude-3-5-sonnet-latest",
			"groq":       "llama-3.3-70b-versatile",
			"mistral":    "codestral-latest",
			"xai":        "grok-2-1212",
			"openrouter": "openrouter/free",
			"cerebras":   "zai-glm-4.7",
			"ollama":     "qwen2.5-coder:7b",
			"ollama-api": "llama3.3",
			"llamacpp":   "llamacpp",
		}
		if def, ok := defaults[s.Provider]; ok {
			s.Model = def
		}
	}
	if s.Model == "" {
		s.Model = "qwen2.5-coder:7b"
	}
	if s.Provider == "" {
		s.Provider = "ollama"
	}

	return s
}

// SaveConfig persists the current Settings to ~/.unjess/config.yaml and saves API keys to ~/.unjess/keys.yaml.
func SaveConfig(s *Settings) error {
	return SaveConfigToPath(s, DefaultConfigPath, DefaultKeysPath)
}

// SaveConfigToPath persists Settings to specific config and keys paths.
// Workspace, APIKeys, and APIKeyPool are stripped from config.yaml.
func SaveConfigToPath(s *Settings, configPath, keysPath string) error {
	if s == nil {
		s = NewDefaultSettings()
	}
	if configPath == "" {
		configPath = DefaultConfigPath
	}
	if keysPath == "" {
		keysPath = DefaultKeysPath
	}

	dir := filepath.Dir(configPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	clone := *s
	clone.Workspace = ""
	clone.APIKeys = nil
	clone.APIKeyPool = nil

	data, err := yaml.Marshal(&clone)
	if err != nil {
		return err
	}

	if err := os.WriteFile(configPath, data, 0644); err != nil {
		return err
	}

	if len(s.APIKeys) == 0 && len(s.APIKeyPool) == 0 {
		// Never wipe existing saved keys on disk when SaveConfig is called with an uninitialized/empty APIKeys map
		savedKeys, savedPool := loadSavedKeys(keysPath)
		if len(savedKeys) > 0 {
			if s.APIKeys == nil {
				s.APIKeys = make(map[string]string)
			}
			for k, v := range savedKeys {
				s.APIKeys[k] = v
			}
		}
		if len(savedPool) > 0 {
			if s.APIKeyPool == nil {
				s.APIKeyPool = make(map[string][]string)
			}
			for k, v := range savedPool {
				s.APIKeyPool[k] = v
			}
		}
	}

	if len(s.APIKeys) > 0 || len(s.APIKeyPool) > 0 {
		if err := SaveKeys(s.APIKeys, s.APIKeyPool, keysPath); err != nil {
			return err
		}
	}
	return nil
}

// readMashinaCollabTokenFromMCP retrieves MASHINA_COLLAB_TOKEN from ~/.unjess/mcp.json if available.
func readMashinaCollabTokenFromMCP(mcpPath string) string {
	if mcpPath == "" {
		mcpPath = DefaultMCPPath
	}
	data, err := os.ReadFile(mcpPath)
	if err != nil {
		return ""
	}
	var mcpData map[string]any
	if err := json.Unmarshal(data, &mcpData); err != nil {
		return ""
	}
	var servers map[string]any
	if srvs, ok := mcpData["mcpServers"].(map[string]any); ok {
		servers = srvs
	} else if srvs, ok := mcpData["mcp_servers"].(map[string]any); ok {
		servers = srvs
	}
	if mashina, ok := servers["mashina-collab"].(map[string]any); ok {
		if headers, ok := mashina["headers"].(map[string]any); ok {
			if auth, ok := headers["Authorization"].(string); ok {
				if strings.HasPrefix(auth, "Bearer ") {
					return strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
				}
			}
		}
		if env, ok := mashina["env"].(map[string]any); ok {
			if tok, ok := env["MASHINA_COLLAB_TOKEN"].(string); ok {
				return tok
			}
		}
	}
	return ""
}

// syncMCPTokens syncs MASHINA_COLLAB_TOKEN to ~/.unjess/mcp.json.
func syncMCPTokens(mcpPath string, mashinaToken string, updateMashina bool) error {
	if !updateMashina {
		return nil
	}
	if mcpPath == "" {
		mcpPath = DefaultMCPPath
	}
	data, err := os.ReadFile(mcpPath)
	if err != nil {
		return nil // if mcp.json does not exist, nothing to update
	}
	var mcpData map[string]any
	if err := json.Unmarshal(data, &mcpData); err != nil {
		return err
	}
	serversKey := "mcpServers"
	servers, ok := mcpData["mcpServers"].(map[string]any)
	if !ok {
		serversKey = "mcp_servers"
		servers, ok = mcpData["mcp_servers"].(map[string]any)
	}
	if !ok || servers == nil {
		return nil
	}

	modified := false
	if updateMashina {
		if mash, ok := servers["mashina-collab"].(map[string]any); ok {
			headers, ok := mash["headers"].(map[string]any)
			if !ok || headers == nil {
				headers = make(map[string]any)
				mash["headers"] = headers
			}
			if mashinaToken != "" {
				headers["Authorization"] = "Bearer " + mashinaToken
			} else {
				headers["Authorization"] = ""
			}

			env, ok := mash["env"].(map[string]any)
			if !ok || env == nil {
				env = make(map[string]any)
				mash["env"] = env
			}
			env["MASHINA_COLLAB_TOKEN"] = mashinaToken
			if mashinaToken != "" {
				mash["enabled"] = true
			}
			modified = true
		}
	}

	if modified {
		mcpData[serversKey] = servers
		updatedBytes, err := json.MarshalIndent(mcpData, "", "  ")
		if err != nil {
			return err
		}
		return os.WriteFile(mcpPath, updatedBytes, 0644)
	}
	return nil
}

// SerializeSettingsDict returns the complete dictionary matching _serialize_settings_dict in src/unjess/api/routes.py.
func SerializeSettingsDict(s *Settings) map[string]any {
	if s == nil {
		s = LoadConfig()
	}
	if s.APIKeys == nil {
		s.APIKeys = make(map[string]string)
	}
	savedKeys, _ := loadSavedKeys(DefaultKeysPath)
	for k, v := range savedKeys {
		if existing, ok := s.APIKeys[k]; !ok || existing == "" {
			s.APIKeys[k] = v
		}
	}

	keysCopy := make(map[string]string)
	for k, v := range s.APIKeys {
		keysCopy[k] = v
	}

	ollamaKey := keysCopy["ollama-api"]
	if ollamaKey == "" {
		ollamaKey = keysCopy["ollama"]
	}

	googleKey := keysCopy["google"]
	if googleKey == "" {
		googleKey = keysCopy["gemini"]
	}

	mashinaToken := s.MashinaCollabToken
	if mashinaToken == "" {
		mashinaToken = keysCopy["mashina"]
	}
	if mashinaToken == "" {
		mashinaToken = keysCopy["mashina-collab"]
	}
	if mashinaToken == "" {
		mashinaToken = readMashinaCollabTokenFromMCP(DefaultMCPPath)
	}

	fallbackChain := s.FallbackChain
	if fallbackChain == nil {
		fallbackChain = []string{}
	}

	ignorePatterns := s.IgnorePatterns
	if ignorePatterns == nil {
		ignorePatterns = []string{}
	}

	return map[string]any{
		// Model & Provider
		"default_model":       s.Model,
		"model":               s.Model,
		"provider":            s.Provider,
		"ollama_base_url":     s.OllamaBaseURL,
		"ollama_api_base_url": s.OllamaAPIBaseURL,
		"lmstudio_base_url":   s.LMStudioBaseURL,
		"llamacpp_base_url":   s.LlamaCppBaseURL,
		"kimi_base_url":       s.KimiBaseURL,
		"qwen_base_url":       s.QwenBaseURL,
		"mashina_url":         s.MashinaURL,

		// API Keys
		"api_keys":             keysCopy,
		"ollama_api_key":       ollamaKey,
		"has_ollama_key":       ollamaKey != "",
		"openai_api_key":       keysCopy["openai"],
		"anthropic_api_key":    keysCopy["anthropic"],
		"google_api_key":       googleKey,
		"groq_api_key":         keysCopy["groq"],
		"mistral_api_key":      keysCopy["mistral"],
		"xai_api_key":          keysCopy["xai"],
		"openrouter_api_key":   keysCopy["openrouter"],
		"cerebras_api_key":     keysCopy["cerebras"],
		"kimi_api_key":         keysCopy["kimi"],
		"qwen_api_key":         keysCopy["qwen"],
		"mashina_collab_token": mashinaToken,

		// Behavior & Execution
		"max_iterations":            s.MaxIterations,
		"subagent_max_turns":        s.SubagentMaxTurns,
		"subagent_model":            s.SubagentModel,
		"ask_before_subagent_spawn": s.AskBeforeSubagentSpawn,
		"command_timeout":           s.CommandTimeout,
		"llm_timeout":               s.LLMTimeout,
		"planning_mode":             s.PlanningMode,
		"reasoning_effort":          s.ReasoningEffort,
		"confirm_commands":          s.ConfirmCommands,
		"enable_compaction":         s.EnableCompaction,
		"enable_stuck_detection":    s.EnableStuckDetection,
		"context_window_override":   s.ContextWindowOverride,
		"ignore_patterns":           ignorePatterns,

		// Display, Theme & Mobile
		"verbosity":      s.Verbosity,
		"language":       s.Language,
		"accent_color":   s.AccentColor,
		"show_diffs":     s.ShowDiffs,
		"show_cost":      s.ShowCost,
		"show_stats":     s.ShowStats,
		"mobile_enabled": s.MobileEnabled,
		"mobile_port":    s.MobilePort,
		"mobile_pin":     s.MobilePin,

		// Budget, Free Mode & RAG
		"max_cost_per_session":   s.MaxCostPerSession,
		"max_tokens_per_message": s.MaxTokensPerMessage,
		"free_mode_enabled":      s.FreeModeEnabled,
		"fallback_chain":         fallbackChain,
		"enable_rag":             s.EnableRAG,

		// Frontier & Worktree v2.1.0
		"shadow_worktree_mode": s.ShadowWorktreeMode,
		"smart_model_routing":  s.SmartModelRouting,

		// Auto-Updater
		"auto_update_enabled": s.AutoUpdateEnabled,
		"update_server_url": func() string {
			if s.UpdateServerURL != "" {
				return s.UpdateServerURL
			}
			return "https://unjess.si/releases/version.json"
		}(),
		"current_version": version.Current,
	}
}

// Helper conversion functions for loose types from JSON input.
func parseString(v any) (string, bool) {
	if v == nil {
		return "", false
	}
	return strings.TrimSpace(fmt.Sprint(v)), true
}

func parseInt(v any) (int, bool) {
	switch val := v.(type) {
	case int:
		return val, true
	case int64:
		return int(val), true
	case float64:
		return int(val), true
	case string:
		if n, err := strconv.Atoi(strings.TrimSpace(val)); err == nil {
			return n, true
		}
	}
	return 0, false
}

func parseFloat64(v any) (float64, bool) {
	switch val := v.(type) {
	case float64:
		return val, true
	case int:
		return float64(val), true
	case int64:
		return float64(val), true
	case string:
		if f, err := strconv.ParseFloat(strings.TrimSpace(val), 64); err == nil {
			return f, true
		}
	}
	return 0.0, false
}

func parseBool(v any) (bool, bool) {
	switch val := v.(type) {
	case bool:
		return val, true
	case string:
		trimmed := strings.ToLower(strings.TrimSpace(val))
		if trimmed == "true" || trimmed == "1" || trimmed == "yes" {
			return true, true
		} else if trimmed == "false" || trimmed == "0" || trimmed == "no" {
			return false, true
		}
	case int, int64, float64:
		n, _ := parseInt(v)
		return n != 0, true
	}
	return false, false
}

// ApplySettingsUpdate updates settings from a JSON body (e.g. POST /api/studio/settings),
// saves the configuration to disk, and synchronizes MCP tokens to mcp.json.
func ApplySettingsUpdate(s *Settings, body map[string]any) error {
	if s == nil {
		s = LoadConfig()
	}
	if s.APIKeys == nil {
		s.APIKeys = make(map[string]string)
	}
	// Ensure existing keys on disk are loaded into s.APIKeys before applying partial updates
	// (e.g., when setModel() posts {"default_model": "..."}), so keys.yaml is never wiped.
	savedKeys, savedPool := loadSavedKeys(DefaultKeysPath)
	for k, v := range savedKeys {
		if _, exists := s.APIKeys[k]; !exists || s.APIKeys[k] == "" {
			s.APIKeys[k] = v
		}
	}
	if s.APIKeyPool == nil {
		s.APIKeyPool = make(map[string][]string)
	}
	for k, v := range savedPool {
		if _, exists := s.APIKeyPool[k]; !exists || len(s.APIKeyPool[k]) == 0 {
			s.APIKeyPool[k] = v
		}
	}

	// Model & Provider
	if val, ok := body["default_model"]; ok && val != nil {
		s.Model = strings.TrimSpace(fmt.Sprint(val))
	} else if val, ok := body["model"]; ok && val != nil {
		s.Model = strings.TrimSpace(fmt.Sprint(val))
	}

	if val, ok := body["provider"]; ok && val != nil {
		s.Provider = strings.TrimSpace(fmt.Sprint(val))
	}

	// Base URLs
	urlFields := map[string]*string{
		"ollama_base_url":     &s.OllamaBaseURL,
		"ollama_api_base_url": &s.OllamaAPIBaseURL,
		"lmstudio_base_url":   &s.LMStudioBaseURL,
		"llamacpp_base_url":   &s.LlamaCppBaseURL,
		"kimi_base_url":       &s.KimiBaseURL,
		"qwen_base_url":       &s.QwenBaseURL,
		"mashina_url":         &s.MashinaURL,
	}
	for field, target := range urlFields {
		if val, ok := body[field]; ok && val != nil {
			*target = strings.TrimSpace(fmt.Sprint(val))
		}
	}

	// API Keys map
	if val, ok := body["api_keys"]; ok {
		if keyMap, ok := val.(map[string]any); ok {
			for k, v := range keyMap {
				strVal := strings.TrimSpace(fmt.Sprint(v))
				if strVal != "" && v != nil {
					s.APIKeys[k] = strVal
				} else {
					delete(s.APIKeys, k)
				}
			}
		} else if keyMapStr, ok := val.(map[string]string); ok {
			for k, v := range keyMapStr {
				strVal := strings.TrimSpace(v)
				if strVal != "" {
					s.APIKeys[k] = strVal
				} else {
					delete(s.APIKeys, k)
				}
			}
		}
	}

	// Ollama API key convenience
	if val, ok := body["ollama_api_key"]; ok {
		keyVal := strings.TrimSpace(fmt.Sprint(val))
		if keyVal != "" && val != nil {
			s.APIKeys["ollama"] = keyVal
			s.APIKeys["ollama-api"] = keyVal
		} else {
			delete(s.APIKeys, "ollama")
			delete(s.APIKeys, "ollama-api")
		}
	}

	// Cloud Provider Keys convenience mapping
	keyMapping := map[string]string{
		"openai_api_key":     "openai",
		"anthropic_api_key":  "anthropic",
		"google_api_key":     "google",
		"groq_api_key":       "groq",
		"mistral_api_key":    "mistral",
		"xai_api_key":        "xai",
		"openrouter_api_key": "openrouter",
		"cerebras_api_key":   "cerebras",
		"kimi_api_key":       "kimi",
		"qwen_api_key":       "qwen",
	}
	for bodyKey, prov := range keyMapping {
		if val, ok := body[bodyKey]; ok {
			valStr := strings.TrimSpace(fmt.Sprint(val))
			if valStr != "" && val != nil {
				s.APIKeys[prov] = valStr
				if prov == "google" {
					s.APIKeys["gemini"] = valStr
				}
			} else {
				delete(s.APIKeys, prov)
				if prov == "google" {
					delete(s.APIKeys, "gemini")
				}
			}
		}
	}

	// Mashina Collab token convenience & sync
	updateMashina := false
	mashinaVal := ""
	if val, ok := body["mashina_collab_token"]; ok {
		updateMashina = true
		if val != nil {
			mashinaVal = strings.TrimSpace(fmt.Sprint(val))
		}
		s.MashinaCollabToken = mashinaVal
		if mashinaVal != "" {
			s.APIKeys["mashina"] = mashinaVal
			s.APIKeys["mashina-collab"] = mashinaVal
		} else {
			delete(s.APIKeys, "mashina")
			delete(s.APIKeys, "mashina-collab")
		}
	}

	// Behavior & Execution
	if val, ok := body["max_iterations"]; ok {
		if n, ok := parseInt(val); ok {
			s.MaxIterations = n
		}
	}
	if val, ok := body["subagent_max_turns"]; ok {
		if n, ok := parseInt(val); ok {
			s.SubagentMaxTurns = n
		}
	}
	if val, ok := body["subagent_model"]; ok && val != nil {
		s.SubagentModel = strings.TrimSpace(fmt.Sprint(val))
	}
	if val, ok := body["ask_before_subagent_spawn"]; ok {
		if b, ok := parseBool(val); ok {
			s.AskBeforeSubagentSpawn = b
		}
	}
	if val, ok := body["command_timeout"]; ok {
		if n, ok := parseInt(val); ok {
			s.CommandTimeout = n
		}
	}
	if val, ok := body["llm_timeout"]; ok {
		if f, ok := parseFloat64(val); ok {
			s.LLMTimeout = f
		}
	}
	if val, ok := body["planning_mode"]; ok && val != nil {
		pm := strings.ToLower(strings.TrimSpace(fmt.Sprint(val)))
		if pm == "auto" || pm == "on" || pm == "off" {
			s.PlanningMode = pm
		}
	}
	if val, ok := body["reasoning_effort"]; ok && val != nil {
		eff := strings.ToLower(strings.TrimSpace(fmt.Sprint(val)))
		switch eff {
		case "auto", "low", "medium", "high", "max", "none":
			s.ReasoningEffort = eff
		}
	}
	if val, ok := body["confirm_commands"]; ok {
		if b, ok := parseBool(val); ok {
			s.ConfirmCommands = b
		}
	}
	if val, ok := body["enable_compaction"]; ok {
		if b, ok := parseBool(val); ok {
			s.EnableCompaction = b
		}
	}
	if val, ok := body["enable_stuck_detection"]; ok {
		if b, ok := parseBool(val); ok {
			s.EnableStuckDetection = b
		}
	}
	if val, ok := body["context_window_override"]; ok {
		if n, ok := parseInt(val); ok {
			s.ContextWindowOverride = n
		}
	}
	if val, ok := body["ignore_patterns"]; ok && val != nil {
		switch v := val.(type) {
		case string:
			lines := strings.Split(v, "\n")
			var pats []string
			for _, line := range lines {
				trimmed := strings.TrimSpace(line)
				if trimmed != "" {
					pats = append(pats, trimmed)
				}
			}
			s.IgnorePatterns = pats
		case []string:
			s.IgnorePatterns = v
		case []any:
			var pats []string
			for _, item := range v {
				if item != nil {
					trimmed := strings.TrimSpace(fmt.Sprint(item))
					if trimmed != "" {
						pats = append(pats, trimmed)
					}
				}
			}
			s.IgnorePatterns = pats
		}
	}

	// Display, Theme & Mobile
	if val, ok := body["verbosity"]; ok && val != nil {
		s.Verbosity = strings.TrimSpace(fmt.Sprint(val))
	}
	if val, ok := body["language"]; ok && val != nil {
		lang := strings.TrimSpace(fmt.Sprint(val))
		if lang == "en" || lang == "sl" {
			s.Language = lang
		}
	}
	if val, ok := body["accent_color"]; ok && val != nil {
		col := strings.TrimSpace(fmt.Sprint(val))
		if col != "" {
			s.AccentColor = col
		}
	}
	if val, ok := body["show_diffs"]; ok {
		if b, ok := parseBool(val); ok {
			s.ShowDiffs = b
		}
	}
	if val, ok := body["show_cost"]; ok {
		if b, ok := parseBool(val); ok {
			s.ShowCost = b
		}
	}
	if val, ok := body["show_stats"]; ok {
		if b, ok := parseBool(val); ok {
			s.ShowStats = b
		}
	}
	if val, ok := body["mobile_enabled"]; ok {
		if b, ok := parseBool(val); ok {
			s.MobileEnabled = b
			s.AllowNetworkAccess = b
		}
	}
	if val, ok := body["mobile_port"]; ok {
		if n, ok := parseInt(val); ok {
			s.MobilePort = n
			s.NetworkPort = n
		}
	}
	if val, ok := body["mobile_pin"]; ok && val != nil {
		s.MobilePin = strings.TrimSpace(fmt.Sprint(val))
	}

	// Budget, Free Mode & RAG
	if val, ok := body["max_cost_per_session"]; ok {
		if f, ok := parseFloat64(val); ok {
			s.MaxCostPerSession = f
		}
	}
	if val, ok := body["max_tokens_per_message"]; ok {
		if n, ok := parseInt(val); ok {
			s.MaxTokensPerMessage = n
		}
	}
	if val, ok := body["free_mode_enabled"]; ok {
		if b, ok := parseBool(val); ok {
			s.FreeModeEnabled = b
		}
	}
	if val, ok := body["fallback_chain"]; ok && val != nil {
		switch v := val.(type) {
		case string:
			items := strings.Split(v, ",")
			var clean []string
			for _, it := range items {
				trimmed := strings.TrimSpace(it)
				if trimmed != "" {
					clean = append(clean, trimmed)
				}
			}
			s.FallbackChain = clean
		case []string:
			s.FallbackChain = v
		case []any:
			var clean []string
			for _, it := range v {
				if it != nil {
					trimmed := strings.TrimSpace(fmt.Sprint(it))
					if trimmed != "" {
						clean = append(clean, trimmed)
					}
				}
			}
			s.FallbackChain = clean
		}
	}
	if val, ok := body["enable_rag"]; ok {
		if b, ok := parseBool(val); ok {
			s.EnableRAG = b
		}
	}

	// Auto-Updater
	if val, ok := body["auto_update_enabled"]; ok {
		if b, ok := parseBool(val); ok {
			s.AutoUpdateEnabled = b
		}
	}
	if val, ok := body["update_server_url"]; ok && val != nil {
		s.UpdateServerURL = strings.TrimSpace(fmt.Sprint(val))
	}

	// Frontier & Worktree v2.1.0
	if val, ok := body["shadow_worktree_mode"]; ok {
		if b, ok := parseBool(val); ok {
			s.ShadowWorktreeMode = b
		}
	}
	if val, ok := body["smart_model_routing"]; ok {
		if b, ok := parseBool(val); ok {
			s.SmartModelRouting = b
		}
	}

	// Persist to disk
	if err := SaveConfig(s); err != nil {
		return err
	}
	if err := SaveKeys(s.APIKeys, s.APIKeyPool, DefaultKeysPath); err != nil {
		return err
	}

	// Sync MCP configuration tokens
	_ = syncMCPTokens(DefaultMCPPath, mashinaVal, updateMashina)

	return nil
}
