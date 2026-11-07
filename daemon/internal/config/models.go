package config

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/skip2/go-qrcode"
)

// CustomProvider defines a user-added OpenAI-compatible AI provider stored in ~/.unjess/custom_providers.json.
type CustomProvider struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	BaseURL     string   `json:"base_url"`
	APIKey      string   `json:"api_key,omitempty"`
	Models      []string `json:"models"`
	ContextSize int      `json:"context_size,omitempty"`
}

func customProvidersPath() string {
	return filepath.Join(DefaultConfigDir, "custom_providers.json")
}

// LoadCustomProviders reads user-defined custom AI providers from ~/.unjess/custom_providers.json.
func LoadCustomProviders() []CustomProvider {
	data, err := os.ReadFile(customProvidersPath())
	if err != nil || len(data) == 0 {
		return nil
	}
	var list []CustomProvider
	if err := json.Unmarshal(data, &list); err != nil {
		return nil
	}
	savedKeys, _ := loadSavedKeys(DefaultKeysPath)
	for i := range list {
		if list[i].APIKey == "" && savedKeys != nil {
			if k := savedKeys[list[i].ID]; k != "" {
				list[i].APIKey = k
			}
		}
	}
	return list
}

// SaveCustomProvider adds or updates a custom AI provider in ~/.unjess/custom_providers.json and syncs its API key to keys.yaml.
func SaveCustomProvider(cp CustomProvider) (CustomProvider, error) {
	cp.ID = strings.ToLower(strings.TrimSpace(cp.ID))
	if cp.ID == "" {
		return cp, fmt.Errorf("provider id is required")
	}
	if strings.TrimSpace(cp.Name) == "" {
		cp.Name = strings.ToUpper(cp.ID[:1]) + cp.ID[1:]
	}
	cp.BaseURL = strings.TrimRight(strings.TrimSpace(cp.BaseURL), "/")
	if cp.BaseURL == "" {
		return cp, fmt.Errorf("base_url is required (e.g. https://api.deepseek.com/v1)")
	}

	var cleanModels []string
	seen := make(map[string]bool)
	for _, m := range cp.Models {
		trimmed := strings.TrimSpace(m)
		if trimmed != "" && !seen[trimmed] {
			seen[trimmed] = true
			cleanModels = append(cleanModels, trimmed)
		}
	}
	cp.Models = cleanModels

	if strings.TrimSpace(cp.APIKey) != "" {
		s := LoadConfig()
		if s.APIKeys == nil {
			s.APIKeys = make(map[string]string)
		}
		s.APIKeys[cp.ID] = strings.TrimSpace(cp.APIKey)
		_ = SaveKeys(s.APIKeys, s.APIKeyPool, DefaultKeysPath)
	}

	existing := LoadCustomProviders()
	updated := false
	for i, item := range existing {
		if strings.EqualFold(item.ID, cp.ID) {
			if cp.APIKey == "" {
				cp.APIKey = item.APIKey
			}
			if len(cp.Models) == 0 {
				cp.Models = item.Models
			}
			existing[i] = cp
			updated = true
			break
		}
	}
	if !updated {
		existing = append(existing, cp)
	}

	// Store metadata in custom_providers.json (keeping APIKey in keys.yaml as well)
	if err := os.MkdirAll(DefaultConfigDir, 0755); err != nil {
		return cp, err
	}
	b, err := json.MarshalIndent(existing, "", "  ")
	if err != nil {
		return cp, err
	}
	if err := os.WriteFile(customProvidersPath(), b, 0600); err != nil {
		return cp, err
	}
	return cp, nil
}

// DeleteCustomProvider removes a custom AI provider by ID from ~/.unjess/custom_providers.json.
func DeleteCustomProvider(id string) (bool, error) {
	id = strings.ToLower(strings.TrimSpace(id))
	if id == "" {
		return false, fmt.Errorf("provider id is required")
	}
	existing := LoadCustomProviders()
	var filtered []CustomProvider
	removed := false
	for _, item := range existing {
		if strings.EqualFold(item.ID, id) {
			removed = true
			continue
		}
		filtered = append(filtered, item)
	}
	if !removed {
		return false, nil
	}
	b, err := json.MarshalIndent(filtered, "", "  ")
	if err != nil {
		return false, err
	}
	return true, os.WriteFile(customProvidersPath(), b, 0600)
}

// ProviderOrder defines the deterministic display order of cloud providers.
var ProviderOrder = []string{
	"google",
	"openai",
	"anthropic",
	"groq",
	"mistral",
	"xai",
	"openrouter",
	"cerebras",
	"kimi",
	"qwen",
	"ollama-api",
}

// KeyEnvMap maps provider identifiers to potential key names and environment variables.
var KeyEnvMap = map[string][]string{
	"google":     {"google", "GOOGLE_API_KEY", "GEMINI_API_KEY"},
	"anthropic":  {"anthropic", "ANTHROPIC_API_KEY"},
	"openai":     {"openai", "OPENAI_API_KEY"},
	"groq":       {"groq", "GROQ_API_KEY"},
	"mistral":    {"mistral", "MISTRAL_API_KEY"},
	"xai":        {"xai", "XAI_API_KEY"},
	"openrouter": {"openrouter", "OPENROUTER_API_KEY"},
	"cerebras":   {"cerebras", "CEREBRAS_API_KEY"},
	"kimi":       {"kimi", "moonshot", "MOONSHOT_API_KEY", "KIMI_API_KEY"},
	"qwen":       {"qwen", "dashscope", "DASHSCOPE_API_KEY", "QWEN_API_KEY"},
	"ollama-api": {"ollama-api", "OLLAMA_API_KEY"},
}

// CloudProviderModels contains October 2026 fallback models for each cloud provider when live API discovery is unreachable.
var CloudProviderModels = map[string][]map[string]any{
	"google": {
		{"id": "gemini-4-argon", "name": "Gemini 4 Argon", "provider": "Google", "desc": "Google DeepMind next-gen flagship reasoning model", "has_tools": true, "has_thinking": true, "supports_effort": true, "is_cloud": true},
		{"id": "gemini-3.8-flash", "name": "Gemini 3.8 Flash", "provider": "Google", "desc": "Google DeepMind high-speed hybrid reasoning model", "has_tools": true, "has_thinking": true, "supports_effort": true, "is_cloud": true},
		{"id": "gemini-3.7-flash", "name": "Gemini 3.7 Flash", "provider": "Google", "desc": "Google DeepMind multimodal coding & reasoning model", "has_tools": true, "has_thinking": true, "supports_effort": true, "is_cloud": true},
		{"id": "gemini-3.6-flash", "name": "Gemini 3.6 Flash", "provider": "Google", "desc": "Google DeepMind fast agentic coding model", "has_tools": true, "has_thinking": true, "supports_effort": true, "is_cloud": true},
		{"id": "gemini-3.5-flash", "name": "Gemini 3.5 Flash", "provider": "Google", "desc": "Google DeepMind balanced Flash model", "has_tools": true, "has_thinking": true, "supports_effort": true, "is_cloud": true},
		{"id": "gemini-3.1-pro-preview", "name": "Gemini 3.1 Pro Preview", "provider": "Google", "desc": "Google DeepMind Gemini 3.1 Pro frontier model", "has_tools": true, "has_thinking": true, "supports_effort": true, "is_cloud": true},
		{"id": "antigravity-preview-latest", "name": "Antigravity Preview Latest", "provider": "Google", "desc": "Google DeepMind agentic coding preview", "has_tools": true, "has_thinking": true, "supports_effort": true, "is_cloud": true},
		{"id": "deep-research-max-preview-04-2026", "name": "Deep Research Max (04-2026)", "provider": "Google", "desc": "Google DeepMind deep research & synthesis model", "has_tools": true, "has_thinking": true, "supports_effort": true, "is_cloud": true},
		{"id": "gemini-2.5-pro", "name": "Gemini 2.5 Pro", "provider": "Google", "desc": "Google DeepMind Pro reasoning model", "has_tools": true, "has_thinking": true, "supports_effort": true, "is_cloud": true},
		{"id": "gemini-2.5-flash", "name": "Gemini 2.5 Flash", "provider": "Google", "desc": "Google DeepMind hybrid reasoning model", "has_tools": true, "has_thinking": true, "supports_effort": true, "is_cloud": true},
	},
	"anthropic": {
		{"id": "claude-opus-5-5", "name": "Claude Opus 5.5", "provider": "Anthropic", "desc": "Anthropic October 2026 frontier reasoning & architecture flagship", "has_tools": true, "has_thinking": true, "supports_effort": true, "is_cloud": true},
		{"id": "claude-sonnet-5-5", "name": "Claude Sonnet 5.5", "provider": "Anthropic", "desc": "Anthropic October 2026 coding & agentic flagship", "has_tools": true, "has_thinking": true, "supports_effort": true, "is_cloud": true},
		{"id": "claude-fable-5-1", "name": "Claude Fable 5.1", "provider": "Anthropic", "desc": "Anthropic Fable 5.1 adaptive reasoning & synthesis model", "has_tools": true, "has_thinking": true, "supports_effort": true, "is_cloud": true},
		{"id": "claude-haiku-5-5", "name": "Claude Haiku 5.5", "provider": "Anthropic", "desc": "Anthropic high-speed adaptive reasoning model", "has_tools": true, "has_thinking": true, "supports_effort": true, "is_cloud": true},
		{"id": "claude-opus-5", "name": "Claude Opus 5", "provider": "Anthropic", "desc": "Anthropic Claude 5 deep reasoning model", "has_tools": true, "has_thinking": true, "supports_effort": true, "is_cloud": true},
		{"id": "claude-sonnet-5", "name": "Claude Sonnet 5", "provider": "Anthropic", "desc": "Anthropic Claude 5 coding & agentic model", "has_tools": true, "has_thinking": true, "supports_effort": true, "is_cloud": true},
		{"id": "claude-fable-5", "name": "Claude Fable 5", "provider": "Anthropic", "desc": "Anthropic Claude Fable 5 model", "has_tools": true, "has_thinking": true, "supports_effort": true, "is_cloud": true},
		{"id": "claude-opus-4-8", "name": "Claude Opus 4.8", "provider": "Anthropic", "desc": "Anthropic Opus 4.8 model", "has_tools": true, "has_thinking": true, "supports_effort": true, "is_cloud": true},
		{"id": "claude-sonnet-4-6", "name": "Claude Sonnet 4.6", "provider": "Anthropic", "desc": "Anthropic Sonnet 4.6 model", "has_tools": true, "has_thinking": true, "supports_effort": true, "is_cloud": true},
	},
	"openai": {
		{"id": "gpt-6-astra", "name": "GPT-6 Astra", "provider": "OpenAI", "desc": "OpenAI frontier reasoning & coding flagship", "has_tools": true, "has_thinking": true, "supports_effort": true, "is_cloud": true},
		{"id": "gpt-6-sol", "name": "GPT-6 Sol", "provider": "OpenAI", "desc": "OpenAI high-speed frontier reasoning model", "has_tools": true, "has_thinking": true, "supports_effort": true, "is_cloud": true},
		{"id": "gpt-6-luna", "name": "GPT-6 Luna", "provider": "OpenAI", "desc": "OpenAI lightweight agentic reasoning model", "has_tools": true, "has_thinking": true, "supports_effort": true, "is_cloud": true},
		{"id": "o4-mini", "name": "o4-mini", "provider": "OpenAI", "desc": "OpenAI fast STEM & code reasoning model", "has_tools": true, "has_thinking": true, "supports_effort": true, "is_cloud": true},
	},
	"groq": {
		{"id": "llama-3.3-70b-versatile", "name": "Llama 3.3 70B", "provider": "Groq", "desc": "Ultra-fast LPU inference", "has_tools": true, "has_thinking": false, "supports_effort": false, "is_cloud": true},
		{"id": "qwen-qwq-32b", "name": "Qwen QwQ 32B", "provider": "Groq", "desc": "High-speed reasoning on Groq LPU", "has_tools": true, "has_thinking": true, "supports_effort": true, "is_cloud": true},
		{"id": "deepseek-r1-distill-llama-70b", "name": "DeepSeek R1 Distill 70B", "provider": "Groq", "desc": "Reasoning model on Groq LPU", "has_tools": true, "has_thinking": true, "supports_effort": true, "is_cloud": true},
	},
	"mistral": {
		{"id": "mistral-large-latest", "name": "Mistral Large", "provider": "Mistral", "desc": "Mistral flagship reasoning", "has_tools": true, "has_thinking": false, "supports_effort": false, "is_cloud": true},
		{"id": "codestral-latest", "name": "Codestral", "provider": "Mistral", "desc": "Mistral code generation specialist", "has_tools": true, "has_thinking": false, "supports_effort": false, "is_cloud": true},
		{"id": "mistral-medium-latest", "name": "Mistral Medium", "provider": "Mistral", "desc": "Balanced multimodal reasoning", "has_tools": true, "has_thinking": false, "supports_effort": false, "is_cloud": true},
	},
	"xai": {
		{"id": "grok-4-latest", "name": "Grok 4", "provider": "xAI", "desc": "xAI frontier reasoning & coding flagship", "has_tools": true, "has_thinking": true, "supports_effort": true, "is_cloud": true},
		{"id": "grok-3-latest", "name": "Grok 3", "provider": "xAI", "desc": "xAI flagship reasoning model", "has_tools": true, "has_thinking": true, "supports_effort": true, "is_cloud": true},
	},
	"openrouter": {
		{"id": "openrouter/auto", "name": "OpenRouter Auto", "provider": "OpenRouter", "desc": "Auto-routed optimal frontier model", "has_tools": true, "has_thinking": true, "supports_effort": true, "is_cloud": true},
		{"id": "anthropic/claude-sonnet-5-5", "name": "Claude Sonnet 5.5 (OpenRouter)", "provider": "OpenRouter", "desc": "Via OpenRouter API", "has_tools": true, "has_thinking": true, "supports_effort": true, "is_cloud": true},
		{"id": "google/gemini-4-argon", "name": "Gemini 4 Argon (OpenRouter)", "provider": "OpenRouter", "desc": "Via OpenRouter API", "has_tools": true, "has_thinking": true, "supports_effort": true, "is_cloud": true},
		{"id": "deepseek/deepseek-v4-pro", "name": "DeepSeek V4 Pro (OpenRouter)", "provider": "OpenRouter", "desc": "Open-weights reasoning", "has_tools": true, "has_thinking": true, "supports_effort": true, "is_cloud": true},
	},
	"cerebras": {
		{"id": "llama-3.3-70b", "name": "Llama 3.3 70B (Cerebras)", "provider": "Cerebras", "desc": "Cerebras CS-3 wafer-scale inference", "has_tools": true, "has_thinking": false, "supports_effort": false, "is_cloud": true},
		{"id": "qwen-3-32b", "name": "Qwen 3 32B (Cerebras)", "provider": "Cerebras", "desc": "Wafer-scale ultra-fast coding & reasoning", "has_tools": true, "has_thinking": true, "supports_effort": true, "is_cloud": true},
	},
	"kimi": {
		{"id": "kimi-k3", "name": "Kimi K3", "provider": "Kimi", "desc": "Moonshot AI October 2026 flagship reasoning & coding model", "has_tools": true, "has_thinking": true, "supports_effort": true, "is_cloud": true},
		{"id": "kimi-k2.7-code", "name": "Kimi K2.7 Code", "provider": "Kimi", "desc": "Moonshot AI specialized agentic coding model", "has_tools": true, "has_thinking": true, "supports_effort": true, "is_cloud": true},
		{"id": "kimi-k2.7-code-highspeed", "name": "Kimi K2.7 Code Highspeed", "provider": "Kimi", "desc": "Moonshot AI ultra-fast coding model", "has_tools": true, "has_thinking": true, "supports_effort": true, "is_cloud": true},
		{"id": "kimi-k2.6", "name": "Kimi K2.6", "provider": "Kimi", "desc": "Moonshot AI multimodal reasoning model", "has_tools": true, "has_thinking": true, "supports_effort": true, "is_cloud": true},
	},
	"qwen": {
		{"id": "qwen3.8-max", "name": "Qwen 3.8 Max", "provider": "Qwen", "desc": "Alibaba QwenCloud October 2026 flagship reasoning model", "has_tools": true, "has_thinking": true, "supports_effort": true, "is_cloud": true},
		{"id": "qwen3.8-2.4t-a95b", "name": "Qwen 3.8 2.4T-A95B", "provider": "Qwen", "desc": "Alibaba QwenCloud ultra-scale 2.4T MoE flagship", "has_tools": true, "has_thinking": true, "supports_effort": true, "is_cloud": true},
		{"id": "qwen3.8-flash", "name": "Qwen 3.8 Flash", "provider": "Qwen", "desc": "Alibaba QwenCloud high-speed hybrid reasoning model", "has_tools": true, "has_thinking": true, "supports_effort": true, "is_cloud": true},
		{"id": "deepseek-v4.1-flash", "name": "DeepSeek V4.1 Flash", "provider": "Qwen", "desc": "DeepSeek V4.1 Flash on QwenCloud DashScope", "has_tools": true, "has_thinking": true, "supports_effort": true, "is_cloud": true},
		{"id": "deepseek-v4-pro", "name": "DeepSeek V4 Pro", "provider": "Qwen", "desc": "DeepSeek V4 Pro frontier reasoning on QwenCloud", "has_tools": true, "has_thinking": true, "supports_effort": true, "is_cloud": true},
		{"id": "glm-5.3-prime", "name": "GLM 5.3 Prime", "provider": "Qwen", "desc": "Zhipu GLM 5.3 Prime on QwenCloud DashScope", "has_tools": true, "has_thinking": true, "supports_effort": true, "is_cloud": true},
		{"id": "glm-5.3", "name": "GLM 5.3", "provider": "Qwen", "desc": "Zhipu GLM 5.3 on QwenCloud DashScope", "has_tools": true, "has_thinking": true, "supports_effort": true, "is_cloud": true},
		{"id": "qwen3.7-max", "name": "Qwen 3.7 Max", "provider": "Qwen", "desc": "Alibaba Qwen 3.7 Max model", "has_tools": true, "has_thinking": true, "supports_effort": true, "is_cloud": true},
	},
	"ollama-api": {
		{"id": "deepseek-v4-pro:cloud", "name": "DeepSeek V4 Pro (Cloud)", "provider": "Ollama Cloud", "desc": "Hosted Ollama Cloud model", "has_tools": true, "has_thinking": true, "supports_effort": true, "is_cloud": true},
		{"id": "deepseek-v4.1-flash:cloud", "name": "DeepSeek V4.1 Flash (Cloud)", "provider": "Ollama Cloud", "desc": "Hosted Ollama Cloud model", "has_tools": true, "has_thinking": true, "supports_effort": true, "is_cloud": true},
		{"id": "kimi-k3:cloud", "name": "Kimi K3 (Cloud)", "provider": "Ollama Cloud", "desc": "Hosted Ollama Cloud model", "has_tools": true, "has_thinking": true, "supports_effort": true, "is_cloud": true},
		{"id": "glm-5.3:cloud", "name": "GLM 5.3 (Cloud)", "provider": "Ollama Cloud", "desc": "Hosted Ollama Cloud model", "has_tools": true, "has_thinking": true, "supports_effort": true, "is_cloud": true},
		{"id": "mistral-large-4:cloud", "name": "Mistral Large 4 (Cloud)", "provider": "Ollama Cloud", "desc": "Hosted Ollama Cloud model", "has_tools": true, "has_thinking": true, "supports_effort": true, "is_cloud": true},
	},
}

// DiscoverModels fetches models dynamically from Ollama and appends cloud models for configured providers.
// Replicates _make_studio_get_models_handler from src/unjess/api/routes.py.
func DiscoverModels(s *Settings, overrideOllamaURL string, overrideOllamaKey string) map[string]any {
	if s == nil {
		s = LoadConfig()
	}

	customEndpoint := strings.TrimSpace(overrideOllamaURL)
	if customEndpoint == "" {
		if s.OllamaBaseURL != "" {
			customEndpoint = s.OllamaBaseURL
		} else if env := os.Getenv("OLLAMA_HOST"); env != "" {
			customEndpoint = env
		} else {
			customEndpoint = "http://localhost:11434"
		}
	}

	apiKey := strings.TrimSpace(overrideOllamaKey)
	if apiKey == "" && s.APIKeys != nil {
		apiKey = s.APIKeys["ollama-api"]
		if apiKey == "" {
			apiKey = s.APIKeys["ollama"]
		}
	}
	if apiKey == "" {
		apiKey = os.Getenv("OLLAMA_API_KEY")
	}

	cleanURL := strings.TrimRight(customEndpoint, "/")
	if cleanURL != "" && !strings.HasPrefix(cleanURL, "http://") && !strings.HasPrefix(cleanURL, "https://") {
		cleanURL = "http://" + cleanURL
	}
	cleanURL = strings.TrimSuffix(cleanURL, "/v1")
	cleanURL = strings.TrimSuffix(cleanURL, "/api/tags")
	cleanURL = strings.TrimSuffix(cleanURL, "/api")

	tagsURL := cleanURL + "/api/tags"
	v1ModelsURL := cleanURL + "/v1/models"

	parsedModels := make([]map[string]any, 0)
	ollamaOnline := false
	errorMsg := ""

	client := &http.Client{
		Timeout: 3 * time.Second,
	}

	req, err := http.NewRequest("GET", tagsURL, nil)
	if err == nil {
		if apiKey != "" && apiKey != "ollama" {
			req.Header.Set("Authorization", "Bearer "+apiKey)
		}
		resp, reqErr := client.Do(req)
		if reqErr == nil && resp.StatusCode == 200 {
			defer resp.Body.Close()
			var data struct {
				Models []struct {
					Name       string `json:"name"`
					Model      string `json:"model"`
					Size       int64  `json:"size"`
					RemoteHost string `json:"remote_host"`
					Details    struct {
						ParentModel       string `json:"parent_model"`
						Format            string `json:"format"`
						Family            string `json:"family"`
						ParameterSize     string `json:"parameter_size"`
						QuantizationLevel string `json:"quantization_level"`
					} `json:"details"`
					Capabilities []string `json:"capabilities"`
				} `json:"models"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&data); err == nil {
				ollamaOnline = true
				for _, m := range data.Models {
					name := m.Name
					if name == "" {
						name = m.Model
					}
					if name == "" {
						continue
					}
					isCloud := strings.Contains(cleanURL, "ollama.com") || strings.Contains(name, ":cloud") || m.RemoteHost != ""
					paramSize := m.Details.ParameterSize
					capabilities := m.Capabilities
					if capabilities == nil {
						capabilities = []string{}
					}

					parts := strings.Split(name, ":")
					baseName := parts[0]
					tagPart := ""
					if len(parts) > 1 {
						tagPart = parts[1]
					}

					words := strings.Fields(strings.ReplaceAll(baseName, "-", " "))
					for i, w := range words {
						if len(w) > 0 {
							words[i] = strings.ToUpper(w[:1]) + strings.ToLower(w[1:])
						}
					}
					cleanName := strings.Join(words, " ")
					if tagPart != "" && tagPart != "latest" && tagPart != "cloud" {
						cleanName = fmt.Sprintf("%s (%s)", cleanName, tagPart)
					} else if paramSize != "" {
						cleanName = fmt.Sprintf("%s (%s)", cleanName, paramSize)
					}

					hasTools := true
					if !isCloud {
						toolsFound := false
						for _, cap := range capabilities {
							if cap == "tools" {
								toolsFound = true
								break
							}
						}
						hasTools = toolsFound || true
					}

					hasThinking := false
					lowerName := strings.ToLower(name)
					for _, k := range []string{"deepseek", "kimi", "glm", "qwq", "r1"} {
						if strings.Contains(lowerName, k) {
							hasThinking = true
							break
						}
					}
					if !hasThinking {
						for _, cap := range capabilities {
							if cap == "thinking" {
								hasThinking = true
								break
							}
						}
					}

					desc := "Local model (" + paramSize + ")"
					if paramSize == "" {
						desc = "Local model (GGUF)"
					}
					if isCloud {
						desc = "Ollama Cloud model"
					}

					providerName := "Ollama"
					if isCloud {
						providerName = "Ollama Cloud"
					}

					parsedModels = append(parsedModels, map[string]any{
						"id":              name,
						"name":            cleanName,
						"raw_name":        name,
						"provider":        providerName,
						"is_cloud":        isCloud,
						"size":            m.Size,
						"parameter_size":  paramSize,
						"quantization":    m.Details.QuantizationLevel,
						"family":          m.Details.Family,
						"capabilities":    capabilities,
						"has_tools":       hasTools,
						"has_thinking":    hasThinking,
						"supports_effort": hasThinking || isCloud,
						"remote_host":     m.RemoteHost,
						"desc":            desc,
					})
				}
			}
		} else {
			if reqErr != nil {
				errorMsg = reqErr.Error()
			}
			// Fallback: try /v1/models
			v1Req, v1Err := http.NewRequest("GET", v1ModelsURL, nil)
			if v1Err == nil {
				if apiKey != "" && apiKey != "ollama" {
					v1Req.Header.Set("Authorization", "Bearer "+apiKey)
				}
				v1Resp, v1DoErr := client.Do(v1Req)
				if v1DoErr == nil && v1Resp.StatusCode == 200 {
					defer v1Resp.Body.Close()
					var v1Data struct {
						Data []struct {
							ID string `json:"id"`
						} `json:"data"`
					}
					if err := json.NewDecoder(v1Resp.Body).Decode(&v1Data); err == nil {
						ollamaOnline = true
						errorMsg = ""
						for _, item := range v1Data.Data {
							if item.ID != "" {
								parsedModels = append(parsedModels, map[string]any{
									"id":             item.ID,
									"name":           item.ID,
									"raw_name":       item.ID,
									"provider":       "Ollama",
									"is_cloud":       false,
									"size":           int64(0),
									"parameter_size": "",
									"quantization":   "",
									"family":         "",
									"capabilities":   []string{"tools"},
									"has_tools":      true,
									"has_thinking":   false,
									"remote_host":    "",
									"desc":           "Local server model",
								})
							}
						}
					}
				}
			}
		}
	}

	// Probe llama.cpp endpoint if configured or accessible
	probeClient := &http.Client{
		Timeout: 1500 * time.Millisecond,
	}

	llamaBase := strings.TrimRight(s.LlamaCppBaseURL, "/")
	if llamaBase == "" {
		llamaBase = "http://localhost:8080"
	}
	if !strings.HasPrefix(llamaBase, "http://") && !strings.HasPrefix(llamaBase, "https://") {
		llamaBase = "http://" + llamaBase
	}
	llamaModelsURL := llamaBase
	if !strings.HasSuffix(llamaModelsURL, "/v1") {
		llamaModelsURL += "/v1"
	}
	llamaModelsURL += "/models"

	if lReq, lErr := http.NewRequest("GET", llamaModelsURL, nil); lErr == nil {
		if lResp, lDoErr := probeClient.Do(lReq); lDoErr == nil && lResp.StatusCode == 200 {
			defer lResp.Body.Close()
			var lData struct {
				Data []struct {
					ID   string `json:"id"`
					Meta struct {
						Size    int64 `json:"size"`
						NParams int64 `json:"n_params"`
						NCtx    int64 `json:"n_ctx_train"`
					} `json:"meta"`
				} `json:"data"`
			}
			if err := json.NewDecoder(lResp.Body).Decode(&lData); err == nil && len(lData.Data) > 0 {
				for _, item := range lData.Data {
					if item.ID != "" {
						cleanName := filepath.Base(item.ID)
						cleanName = strings.TrimSuffix(cleanName, ".gguf")
						cleanName = strings.TrimSuffix(cleanName, ".GGUF")
						desc := "Local llama-server GGUF model"
						if item.Meta.NParams > 0 {
							desc = fmt.Sprintf("Local llama.cpp (%.1fB params)", float64(item.Meta.NParams)/1e9)
						}
						parsedModels = append(parsedModels, map[string]any{
							"id":              item.ID,
							"name":            cleanName,
							"raw_name":        item.ID,
							"provider":        "llama.cpp",
							"is_cloud":        false,
							"size":            item.Meta.Size,
							"parameter_size":  "",
							"quantization":    "",
							"family":          "llama.cpp",
							"capabilities":    []string{"tools"},
							"has_tools":       true,
							"has_thinking":    false,
							"supports_effort": false,
							"remote_host":     "",
							"desc":            desc,
						})
					}
				}
			}
		}
	}

	// Probe LM Studio endpoint if configured or accessible
	lmBase := strings.TrimRight(s.LMStudioBaseURL, "/")
	if lmBase == "" {
		lmBase = "http://localhost:1234"
	}
	if !strings.HasPrefix(lmBase, "http://") && !strings.HasPrefix(lmBase, "https://") {
		lmBase = "http://" + lmBase
	}
	lmModelsURL := lmBase
	if !strings.HasSuffix(lmModelsURL, "/v1") {
		lmModelsURL += "/v1"
	}
	lmModelsURL += "/models"

	if lmReq, lmErr := http.NewRequest("GET", lmModelsURL, nil); lmErr == nil {
		if lmResp, lmDoErr := probeClient.Do(lmReq); lmDoErr == nil && lmResp.StatusCode == 200 {
			defer lmResp.Body.Close()
			var lmData struct {
				Data []struct {
					ID string `json:"id"`
				} `json:"data"`
			}
			if err := json.NewDecoder(lmResp.Body).Decode(&lmData); err == nil && len(lmData.Data) > 0 {
				for _, item := range lmData.Data {
					if item.ID != "" {
						cleanName := filepath.Base(item.ID)
						parsedModels = append(parsedModels, map[string]any{
							"id":              item.ID,
							"name":            cleanName,
							"raw_name":        item.ID,
							"provider":        "LM Studio",
							"is_cloud":        false,
							"size":            int64(0),
							"parameter_size":  "",
							"quantization":    "",
							"family":          "lmstudio",
							"capabilities":    []string{"tools"},
							"has_tools":       true,
							"has_thinking":    false,
							"supports_effort": false,
							"remote_host":     "",
							"desc":            "Local LM Studio model",
						})
					}
				}
			}
		}
	}

	// Add models for configured cloud providers
	configuredProviders := make([]string, 0)
	if ollamaOnline {
		configuredProviders = append(configuredProviders, "ollama")
	}

	allKeys := make(map[string]string)
	if s.APIKeys != nil {
		for k, v := range s.APIKeys {
			allKeys[k] = v
		}
	}

	for _, prov := range ProviderOrder {
		aliases := KeyEnvMap[prov]
		hasKey := false
		for _, alias := range aliases {
			if allKeys[alias] != "" || os.Getenv(alias) != "" {
				hasKey = true
				break
			}
		}
		if hasKey {
			configuredProviders = append(configuredProviders, prov)
		}
	}

	// Fetch live models concurrently from configured cloud APIs (Gemini, OpenAI, Groq, Anthropic, etc.)
	var wg sync.WaitGroup
	var mu sync.Mutex
	liveModelsByProvider := make(map[string][]map[string]any)

	baseURLs := map[string]string{
		"kimi": s.KimiBaseURL,
		"qwen": s.QwenBaseURL,
	}

	for _, prov := range configuredProviders {
		if prov == "ollama" {
			continue
		}
		key := resolveProviderKey(prov, allKeys)
		if key == "" {
			continue
		}
		wg.Add(1)
		go func(p string, k string) {
			defer wg.Done()
			fetched := fetchLiveCloudModels(p, k, baseURLs)
			if len(fetched) > 0 {
				mu.Lock()
				liveModelsByProvider[p] = fetched
				mu.Unlock()
			}
		}(prov, key)
	}
	wg.Wait()

	// Append models (preferring live discovered models from API, falling back to modern defaults)
	for _, prov := range configuredProviders {
		if prov == "ollama" {
			continue
		}
		modelsToUse := liveModelsByProvider[prov]
		if len(modelsToUse) == 0 {
			modelsToUse = CloudProviderModels[prov]
		}
		for _, modelInfo := range modelsToUse {
			mID, _ := modelInfo["id"].(string)
			if mID == "" {
				continue
			}
			exists := false
			for _, existing := range parsedModels {
				if existing["id"] == mID {
					exists = true
					break
				}
			}
			if !exists {
				copyModel := make(map[string]any)
				for k, v := range modelInfo {
					copyModel[k] = v
				}
				parsedModels = append(parsedModels, copyModel)
			}
		}
	}

	// Append models from user-defined Custom Providers (~/.unjess/custom_providers.json)
	for _, cp := range LoadCustomProviders() {
		configuredProviders = append(configuredProviders, cp.ID)
		for _, mID := range cp.Models {
			exists := false
			for _, existing := range parsedModels {
				if existing["id"] == mID {
					exists = true
					break
				}
			}
			if !exists {
				lowM := strings.ToLower(mID)
				hasThinking := strings.Contains(lowM, "reason") || strings.Contains(lowM, "r1") || strings.Contains(lowM, "thinking") || strings.Contains(lowM, "qwq")
				parsedModels = append(parsedModels, map[string]any{
					"id":              mID,
					"name":            fmt.Sprintf("%s (%s)", mID, cp.Name),
					"raw_name":        mID,
					"provider":        cp.Name,
					"desc":            fmt.Sprintf("Custom provider %s (%s)", cp.Name, cp.BaseURL),
					"has_tools":       true,
					"has_thinking":    hasThinking,
					"supports_effort": hasThinking,
					"is_cloud":        true,
				})
			}
		}
	}

	// Default model selection:
	// Strictly preserve the user's saved model preference if configured!
	savedDefault := s.Model
	defaultModel := ""
	if savedDefault != "" {
		for _, m := range parsedModels {
			if m["id"] == savedDefault {
				defaultModel = savedDefault
				break
			}
		}
		if defaultModel == "" {
			// User has a specific model saved in config.yaml / localStorage; preserve it!
			defaultModel = savedDefault
		}
	}
	if defaultModel == "" && len(parsedModels) > 0 {
		if idStr, ok := parsedModels[0]["id"].(string); ok {
			defaultModel = idStr
		}
	}

	status := "offline"
	if ollamaOnline || len(configuredProviders) > 0 {
		status = "online"
	}

	retErr := ""
	if !ollamaOnline {
		retErr = errorMsg
	}

	return map[string]any{
		"status":               status,
		"ollama_online":        ollamaOnline,
		"endpoint":             cleanURL,
		"models":               parsedModels,
		"default_model":        defaultModel,
		"configured_providers": configuredProviders,
		"error":                retErr,
	}
}

// GetNetworkInfo detects the LAN IP and generates a QR code for mobile companion connection.
// Replicates _make_network_info_handler in src/unjess/api/routes.py.
func GetNetworkInfo(s *Settings, port int) map[string]any {
	if s == nil {
		s = LoadConfig()
	}
	if port <= 0 {
		port = s.MobilePort
	}
	if port <= 0 {
		port = 8080
	}

	lanIP := "127.0.0.1"
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err == nil {
		if localAddr, ok := conn.LocalAddr().(*net.UDPAddr); ok {
			lanIP = localAddr.IP.String()
		}
		_ = conn.Close()
	}

	if lanIP == "127.0.0.1" {
		if hostname, err := os.Hostname(); err == nil {
			if addrs, err := net.LookupHost(hostname); err == nil {
				for _, addr := range addrs {
					ip := net.ParseIP(addr)
					if ip != nil && ip.To4() != nil && !ip.IsLoopback() {
						lanIP = addr
						break
					}
				}
			}
		}
	}

	companionURL := fmt.Sprintf("http://%s:%d", lanIP, port)

	qrDataURL := ""
	pngBytes, err := qrcode.Encode(companionURL, qrcode.Medium, 200)
	if err == nil {
		qrDataURL = "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngBytes)
	}

	return map[string]any{
		"status":         "ok",
		"lan_ip":         lanIP,
		"port":           port,
		"mobile_port":    s.MobilePort,
		"companion_url":  companionURL,
		"qr_data_url":    qrDataURL,
		"mobile_enabled": s.MobileEnabled,
		"pin":            s.MobilePin,
	}
}

// resolveProviderKey retrieves the API key for a provider from allKeys or environment variables.
func resolveProviderKey(prov string, allKeys map[string]string) string {
	if aliases, ok := KeyEnvMap[prov]; ok {
		for _, alias := range aliases {
			if v := strings.TrimSpace(allKeys[alias]); v != "" {
				return v
			}
			if v := strings.TrimSpace(allKeys[strings.ToLower(alias)]); v != "" {
				return v
			}
			if v := strings.TrimSpace(os.Getenv(alias)); v != "" {
				return v
			}
		}
	}
	if v := strings.TrimSpace(allKeys[prov]); v != "" {
		return v
	}
	return ""
}

// geminiModelPriority assigns a sort priority to Google Gemini models so newest October 2026 models appear first.
func geminiModelPriority(id string) int {
	low := strings.ToLower(id)
	switch {
	case strings.HasPrefix(low, "gemini-4"):
		return 100
	case strings.HasPrefix(low, "gemini-3.8"):
		return 95
	case strings.HasPrefix(low, "gemini-3.7"):
		return 90
	case strings.HasPrefix(low, "gemini-3.6"):
		return 85
	case strings.HasPrefix(low, "gemini-3.5"):
		return 80
	case strings.HasPrefix(low, "gemini-3.1-pro"):
		return 78
	case strings.HasPrefix(low, "gemini-3"):
		return 75
	case low == "gemini-pro-latest" || low == "gemini-flash-latest":
		return 70
	case strings.HasPrefix(low, "gemma-4"):
		return 65
	case strings.HasPrefix(low, "gemini-2.5-pro"):
		return 60
	case strings.HasPrefix(low, "gemini-2.5-flash") && !strings.Contains(low, "lite") && !strings.Contains(low, "preview"):
		return 58
	case strings.HasPrefix(low, "gemini-2.5"):
		return 50
	case strings.HasPrefix(low, "gemini-2.0"):
		return 40
	case strings.HasPrefix(low, "gemma-3"):
		return 30
	default:
		return 20
	}
}

// fetchLiveCloudModels calls the provider's official models API to discover active models in real time.
func fetchLiveCloudModels(prov string, apiKey string, baseURLs map[string]string) []map[string]any {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return nil
	}

	client := &http.Client{Timeout: 4 * time.Second}
	var results []map[string]any

	switch prov {
	case "google":
		reqURL := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models?key=%s", apiKey)
		req, err := http.NewRequest("GET", reqURL, nil)
		if err != nil {
			return nil
		}
		resp, err := client.Do(req)
		if err != nil || resp.StatusCode != http.StatusOK {
			return nil
		}
		defer resp.Body.Close()

		var gData struct {
			Models []struct {
				Name                       string   `json:"name"`
				DisplayName                string   `json:"displayName"`
				Description                string   `json:"description"`
				SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
			} `json:"models"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&gData); err == nil && len(gData.Models) > 0 {
			for _, m := range gData.Models {
				hasGenerate := false
				for _, method := range m.SupportedGenerationMethods {
					if method == "generateContent" {
						hasGenerate = true
						break
					}
				}
				if !hasGenerate {
					continue
				}
				cleanID := strings.TrimPrefix(m.Name, "models/")
				lowID := strings.ToLower(cleanID)
				// Filter out non-chat / specialized media / TTS / image / embedding endpoints
				if strings.Contains(lowID, "embed") ||
					strings.Contains(lowID, "aqa") ||
					strings.Contains(lowID, "-tts") ||
					strings.Contains(lowID, "-image") ||
					strings.Contains(lowID, "imagen") ||
					strings.Contains(lowID, "veo") ||
					strings.Contains(lowID, "lyria") ||
					strings.Contains(lowID, "nano-banana") ||
					strings.Contains(lowID, "transcribe") ||
					strings.Contains(lowID, "robotics") ||
					strings.Contains(lowID, "computer-use") {
					continue
				}
				name := m.DisplayName
				if name == "" {
					name = cleanID
				}
				hasThinking := strings.Contains(lowID, "gemini-4") ||
					strings.Contains(lowID, "gemini-3") ||
					strings.Contains(lowID, "2.5") ||
					strings.Contains(lowID, "antigravity") ||
					strings.Contains(lowID, "deep-research") ||
					strings.Contains(lowID, "thinking") ||
					strings.Contains(lowID, "pro")
				results = append(results, map[string]any{
					"id":              cleanID,
					"name":            name,
					"raw_name":        cleanID,
					"provider":        "Google",
					"desc":            m.Description,
					"has_tools":       true,
					"has_thinking":    hasThinking,
					"supports_effort": hasThinking,
					"is_cloud":        true,
				})
			}
			sort.SliceStable(results, func(i, j int) bool {
				idI, _ := results[i]["id"].(string)
				idJ, _ := results[j]["id"].(string)
				pI := geminiModelPriority(idI)
				pJ := geminiModelPriority(idJ)
				if pI != pJ {
					return pI > pJ
				}
				return idI < idJ
			})
		}

	case "openai", "groq", "mistral", "openrouter", "xai", "cerebras", "kimi", "qwen":
		var endpoint string
		providerLabel := strings.ToUpper(prov[:1]) + prov[1:]
		switch prov {
		case "openai":
			endpoint = "https://api.openai.com/v1/models"
			providerLabel = "OpenAI"
		case "groq":
			endpoint = "https://api.groq.com/openai/v1/models"
			providerLabel = "Groq"
		case "mistral":
			endpoint = "https://api.mistral.ai/v1/models"
			providerLabel = "Mistral"
		case "openrouter":
			endpoint = "https://openrouter.ai/api/v1/models"
			providerLabel = "OpenRouter"
		case "xai":
			endpoint = "https://api.x.ai/v1/models"
			providerLabel = "xAI"
		case "cerebras":
			endpoint = "https://api.cerebras.ai/v1/models"
			providerLabel = "Cerebras"
		case "kimi":
			endpoint = "https://api.moonshot.ai/v1/models"
			if b, ok := baseURLs["kimi"]; ok && b != "" {
				endpoint = strings.TrimRight(b, "/") + "/models"
			}
			providerLabel = "Kimi"
		case "qwen":
			endpoint = "https://dashscope-intl.aliyuncs.com/compatible-mode/v1/models"
			if b, ok := baseURLs["qwen"]; ok && b != "" {
				endpoint = strings.TrimRight(b, "/") + "/models"
			}
			providerLabel = "Qwen"
		}

		doModelsReq := func(ep string) (*http.Response, error) {
			r, rErr := http.NewRequest("GET", ep, nil)
			if rErr != nil {
				return nil, rErr
			}
			r.Header.Set("Authorization", "Bearer "+apiKey)
			return client.Do(r)
		}

		resp, err := doModelsReq(endpoint)
		if err == nil && resp.StatusCode == http.StatusUnauthorized && (prov == "kimi" || prov == "qwen") {
			_ = resp.Body.Close()
			altEp := ""
			if prov == "kimi" {
				if strings.Contains(endpoint, "api.moonshot.ai") {
					altEp = "https://api.moonshot.cn/v1/models"
				} else {
					altEp = "https://api.moonshot.ai/v1/models"
				}
			} else if prov == "qwen" {
				if strings.Contains(endpoint, "dashscope-intl.aliyuncs.com") {
					altEp = "https://dashscope.aliyuncs.com/compatible-mode/v1/models"
				} else {
					altEp = "https://dashscope-intl.aliyuncs.com/compatible-mode/v1/models"
				}
			}
			if altEp != "" {
				resp, err = doModelsReq(altEp)
			}
		}
		if err != nil || resp == nil || resp.StatusCode != http.StatusOK {
			if resp != nil {
				_ = resp.Body.Close()
			}
			return nil
		}
		defer resp.Body.Close()

		var oData struct {
			Data []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&oData); err == nil && len(oData.Data) > 0 {
			for _, item := range oData.Data {
				id := item.ID
				if id == "" {
					continue
				}
				low := strings.ToLower(id)
				if strings.Contains(low, "whisper") || strings.Contains(low, "embedding") ||
					strings.Contains(low, "moderation") || strings.Contains(low, "tts") ||
					strings.Contains(low, "dall-e") || strings.Contains(low, "babbage") ||
					strings.Contains(low, "davinci") || strings.Contains(low, "qwen-image") ||
					strings.Contains(low, "qwen-audio") || strings.Contains(low, "-asr-") ||
					strings.Contains(low, "livetranslate") || strings.Contains(low, "-s2s-") ||
					strings.Contains(low, "-realtime") || strings.HasPrefix(low, "wan2") ||
					strings.Contains(low, "-ocr-") || strings.Contains(low, "z-image") ||
					strings.Contains(low, "pre-mock") || strings.Contains(low, "tingwu") ||
					strings.Contains(low, "ccai-pro") || strings.HasPrefix(low, "qwen-mt-") {
					continue
				}
				if prov == "qwen" && (strings.HasPrefix(low, "kimi/") || strings.HasPrefix(id, "ZHIPU/")) {
					continue
				}
				name := item.Name
				if name == "" {
					name = id
				}
				hasThinking := strings.Contains(low, "o1") || strings.Contains(low, "o3") ||
					strings.Contains(low, "o4") || strings.Contains(low, "gpt-6") || strings.Contains(low, "gpt-5") ||
					strings.Contains(low, "r1") || strings.Contains(low, "reason") ||
					strings.Contains(low, "thinking") || strings.Contains(low, "k3") ||
					strings.Contains(low, "k2") || strings.Contains(low, "qwen3") ||
					strings.Contains(low, "qwq") || strings.Contains(low, "deepseek-v4") ||
					strings.Contains(low, "glm-5") || strings.Contains(low, "qwen-max") ||
					strings.Contains(low, "qwen-plus") || strings.Contains(low, "grok-3") ||
					strings.Contains(low, "grok-4")
				results = append(results, map[string]any{
					"id":              id,
					"name":            name,
					"raw_name":        id,
					"provider":        providerLabel,
					"desc":            fmt.Sprintf("%s Cloud model (%s)", providerLabel, id),
					"has_tools":       true,
					"has_thinking":    hasThinking,
					"supports_effort": hasThinking,
					"is_cloud":        true,
				})
			}
			if prov == "qwen" || prov == "kimi" {
				sort.SliceStable(results, func(i, j int) bool {
					idI, _ := results[i]["id"].(string)
					idJ, _ := results[j]["id"].(string)
					pI := qwenKimiModelPriority(idI)
					pJ := qwenKimiModelPriority(idJ)
					if pI != pJ {
						return pI > pJ
					}
					return idI < idJ
				})
			}
		}

	case "anthropic":
		req, err := http.NewRequest("GET", "https://api.anthropic.com/v1/models", nil)
		if err != nil {
			return nil
		}
		req.Header.Set("x-api-key", apiKey)
		req.Header.Set("anthropic-version", "2023-06-01")
		resp, err := client.Do(req)
		if err != nil || resp.StatusCode != http.StatusOK {
			return nil
		}
		defer resp.Body.Close()

		var aData struct {
			Data []struct {
				ID          string `json:"id"`
				DisplayName string `json:"display_name"`
			} `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&aData); err == nil && len(aData.Data) > 0 {
			for _, item := range aData.Data {
				if item.ID == "" {
					continue
				}
				name := item.DisplayName
				if name == "" {
					name = item.ID
				}
				lowID := strings.ToLower(item.ID)
				hasThinking := strings.Contains(lowID, "3-7") ||
					strings.Contains(lowID, "-4") ||
					strings.Contains(lowID, "-5") ||
					strings.Contains(lowID, "fable") ||
					strings.Contains(lowID, "thinking")
				results = append(results, map[string]any{
					"id":              item.ID,
					"name":            name,
					"raw_name":        item.ID,
					"provider":        "Anthropic",
					"desc":            fmt.Sprintf("Anthropic model (%s)", item.ID),
					"has_tools":       true,
					"has_thinking":    hasThinking,
					"supports_effort": hasThinking,
					"is_cloud":        true,
				})
			}
			sort.SliceStable(results, func(i, j int) bool {
				idI, _ := results[i]["id"].(string)
				idJ, _ := results[j]["id"].(string)
				pI := anthropicModelPriority(idI)
				pJ := anthropicModelPriority(idJ)
				if pI != pJ {
					return pI > pJ
				}
				return idI < idJ
			})
		}
	}

	return results
}

func anthropicModelPriority(id string) int {
	low := strings.ToLower(id)
	switch {
	case low == "claude-opus-5-5":
		return 100
	case low == "claude-sonnet-5-5":
		return 98
	case low == "claude-fable-5-1":
		return 96
	case low == "claude-haiku-5-5":
		return 94
	case low == "claude-opus-5":
		return 90
	case low == "claude-sonnet-5":
		return 88
	case low == "claude-fable-5":
		return 86
	case strings.Contains(low, "4-8"):
		return 80
	case strings.Contains(low, "4-7"):
		return 78
	case strings.Contains(low, "4-6"):
		return 76
	case strings.Contains(low, "4-5"):
		return 70
	default:
		return 50
	}
}

func qwenKimiModelPriority(id string) int {
	low := strings.ToLower(id)
	switch {
	case low == "kimi-k3":
		return 100
	case low == "kimi-k2.7-code":
		return 98
	case low == "kimi-k2.7-code-highspeed":
		return 96
	case low == "kimi-k2.6":
		return 92
	case low == "qwen3.8-max":
		return 100
	case low == "qwen3.8-2.4t-a95b":
		return 98
	case low == "qwen3.8-flash":
		return 96
	case low == "qwen3.8-omni-flash":
		return 95
	case low == "deepseek-v4-pro":
		return 94
	case low == "deepseek-v4.1-flash":
		return 93
	case low == "glm-5.3-prime":
		return 92
	case low == "glm-5.3":
		return 91
	case strings.HasPrefix(low, "qwen3.8"):
		return 88
	case strings.HasPrefix(low, "deepseek-v4"):
		return 85
	case strings.HasPrefix(low, "qwen3.7-max"):
		return 82
	case strings.HasPrefix(low, "qwen3.7"):
		return 80
	case strings.HasPrefix(low, "glm-5"):
		return 78
	case strings.HasPrefix(low, "qwen3.6"):
		return 74
	case strings.HasPrefix(low, "qwen3.5"):
		return 70
	case strings.HasPrefix(low, "qwen3"):
		return 65
	default:
		return 40
	}
}
