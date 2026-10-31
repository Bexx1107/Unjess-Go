package llm

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ToolCall represents a tool/function call requested by the LLM.
type ToolCall struct {
	ID               string         `json:"id"`
	Name             string         `json:"name"`
	Arguments        map[string]any `json:"arguments"`
	ThoughtSignature string         `json:"thought_signature,omitempty"`
}

// ImageAttachment represents an image attached to a message.
type ImageAttachment struct {
	Name     string `json:"name,omitempty"`
	MimeType string `json:"mime_type"`
	Data     string `json:"data"` // raw base64
}

// Message represents a single chat turn.
type Message struct {
	Role             string            `json:"role"`
	Content          string            `json:"content"`
	Thinking         string            `json:"thinking,omitempty"`
	ThoughtSignature string            `json:"thought_signature,omitempty"`
	RawParts         []map[string]any  `json:"raw_parts,omitempty"`
	ToolCalls        []ToolCall        `json:"tool_calls,omitempty"`
	ToolCallID       string            `json:"tool_call_id,omitempty"`
	ToolName         string            `json:"tool_name,omitempty"`
	Images           []ImageAttachment `json:"images,omitempty"`
}

// ChatOptions holds all parameters required to perform a chat completion.
type ChatOptions struct {
	Model                 string            `json:"model"`
	Provider              string            `json:"provider"`
	SystemPrompt          string            `json:"system_prompt"`
	Messages              []Message         `json:"messages"`
	Tools                 []map[string]any  `json:"tools,omitempty"`
	TimeoutSec            float64           `json:"timeout_sec,omitempty"`
	ContextWindowOverride int               `json:"context_window_override,omitempty"`
	MaxTokens             int               `json:"max_tokens,omitempty"`
	ReasoningEffort       string            `json:"reasoning_effort,omitempty"` // "auto", "low", "medium", "high", "max", "none"
	OllamaBaseURL         string            `json:"ollama_base_url,omitempty"`
	OllamaAPIBaseURL      string            `json:"ollama_api_base_url,omitempty"`
	APIKeys               map[string]string `json:"api_keys,omitempty"`
	BaseURLs              map[string]string `json:"base_urls,omitempty"`
}

// NormalizeEffort normalizes a user/API reasoning effort string into a canonical tier:
// "auto", "low", "medium", "high", "max", or "none".
func NormalizeEffort(effort string) string {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "low", "minimal", "min":
		return "low"
	case "medium", "med", "balanced":
		return "medium"
	case "high":
		return "high"
	case "max", "xhigh", "extra_high", "maximum":
		return "max"
	case "none", "off", "disabled", "false":
		return "none"
	default:
		return "auto"
	}
}

// SupportsReasoningEffort returns true if the given model/provider supports reasoning effort / thinking depth controls.
func SupportsReasoningEffort(model string, provider string) bool {
	low := strings.ToLower(strings.TrimSpace(model))
	prov := strings.ToLower(strings.TrimSpace(provider))
	if prov == "" {
		prov = DetectProvider(model, "")
	}
	switch prov {
	case "anthropic":
		return strings.Contains(low, "-5") || strings.Contains(low, "-4") || strings.Contains(low, "3-7") || strings.Contains(low, "fable")
	case "google":
		return strings.Contains(low, "gemini-4") || strings.Contains(low, "gemini-3") || strings.Contains(low, "gemini-2.5") || strings.Contains(low, "antigravity") || strings.Contains(low, "deep-research") || strings.Contains(low, "pro") || strings.Contains(low, "flash")
	case "qwen":
		return strings.Contains(low, "qwen3") || strings.Contains(low, "qwq") || strings.Contains(low, "qvq") || strings.Contains(low, "deepseek") || strings.Contains(low, "glm-5") || strings.Contains(low, "qwen-max") || strings.Contains(low, "qwen-plus")
	case "kimi":
		return strings.Contains(low, "kimi-k") || strings.Contains(low, "thinking")
	case "openai":
		return strings.HasPrefix(low, "gpt-5") || strings.HasPrefix(low, "gpt-6") || strings.HasPrefix(low, "o1") || strings.HasPrefix(low, "o3") || strings.HasPrefix(low, "o4")
	case "xai":
		return strings.Contains(low, "grok-3") || strings.Contains(low, "grok-4")
	case "ollama", "ollama-api":
		return strings.Contains(low, "deepseek") || strings.Contains(low, "kimi") || strings.Contains(low, "glm") || strings.Contains(low, "qwen3") || strings.Contains(low, "qwq") || strings.Contains(low, ":cloud")
	default:
		return strings.Contains(low, "deepseek") || strings.Contains(low, "reason") || strings.Contains(low, "thinking") || strings.Contains(low, "qwq")
	}
}

// StreamCallbacks receives streaming chunks in real time.
type StreamCallbacks struct {
	OnText       func(chunk string)
	OnThinking   func(chunk string)
	OnToolStream func(toolName string, totalBytes int, preview string)
}

// ChatResult contains the accumulated response from the LLM.
type ChatResult struct {
	Content          string           `json:"content"`
	Thinking         string           `json:"thinking"`
	ThoughtSignature string           `json:"thought_signature,omitempty"`
	RawParts         []map[string]any `json:"raw_parts,omitempty"`
	ToolCalls        []ToolCall       `json:"tool_calls"`
	StopReason       string           `json:"stop_reason,omitempty"`
	InputTokens      int              `json:"input_tokens"`
	OutputTokens     int              `json:"output_tokens"`
}

var knownProviders = map[string]bool{
	"ollama":     true,
	"ollama-api": true,
	"openai":     true,
	"anthropic":  true,
	"google":     true,
	"groq":       true,
	"mistral":    true,
	"xai":        true,
	"openrouter": true,
	"cerebras":   true,
	"kimi":       true,
	"qwen":       true,
	"lmstudio":   true,
	"llamacpp":   true,
}

type customProviderEntry struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	BaseURL string   `json:"base_url"`
	APIKey  string   `json:"api_key,omitempty"`
	Models  []string `json:"models"`
}

func loadCustomProvidersForLLM() []customProviderEntry {
	dir := os.Getenv("UNJESS_CONFIG_DIR")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil
		}
		dir = filepath.Join(home, ".unjess")
	}
	data, err := os.ReadFile(filepath.Join(dir, "custom_providers.json"))
	if err != nil || len(data) == 0 {
		return nil
	}
	var list []customProviderEntry
	if err := json.Unmarshal(data, &list); err != nil {
		return nil
	}
	return list
}

// DetectProvider auto-detects the provider name from the model identifier.
func DetectProvider(model string, configuredProvider string) string {
	m := strings.TrimSpace(model)
	lower := strings.ToLower(m)
	customList := loadCustomProvidersForLLM()

	// If model has provider/model prefix (for known or custom providers), split and return provider.
	if idx := strings.Index(lower, "/"); idx != -1 {
		prefix := lower[:idx]
		if knownProviders[prefix] {
			return prefix
		}
		for _, cp := range customList {
			if strings.EqualFold(cp.ID, prefix) {
				return strings.ToLower(cp.ID)
			}
		}
	}

	for _, cp := range customList {
		for _, cm := range cp.Models {
			if strings.EqualFold(strings.TrimSpace(cm), m) {
				return strings.ToLower(cp.ID)
			}
		}
	}

	// If model ends with :cloud, use "ollama".
	if strings.HasSuffix(lower, ":cloud") {
		return "ollama"
	}

	// If model contains : (e.g. ornith-1.5:9b, qwen2.5-coder:7b), return "ollama".
	if strings.Contains(lower, ":") && !strings.HasPrefix(lower, "http:") && !strings.HasPrefix(lower, "https:") {
		return "ollama"
	}

	// Google Gemini / Gemma / Antigravity / Deep Research cloud models
	if strings.HasPrefix(lower, "gemini-") || strings.HasPrefix(lower, "gemma-") || strings.HasPrefix(lower, "antigravity-") || strings.HasPrefix(lower, "deep-research-") || strings.HasPrefix(lower, "learnlm-") {
		return "google"
	}

	// OpenAI GPT / o-series reasoning models
	if strings.HasPrefix(lower, "gpt-") || strings.HasPrefix(lower, "o1") || strings.HasPrefix(lower, "o3") || strings.HasPrefix(lower, "o4") || strings.HasPrefix(lower, "chatgpt-") {
		return "openai"
	}

	// Anthropic Claude models
	if strings.HasPrefix(lower, "claude-") {
		return "anthropic"
	}

	// xAI Grok models
	if strings.HasPrefix(lower, "grok-") {
		return "xai"
	}

	// Mistral models (when not local Ollama tag with ':')
	if strings.HasPrefix(lower, "codestral-") || strings.HasPrefix(lower, "pixtral-") || strings.HasPrefix(lower, "ministral-") || strings.HasPrefix(lower, "mistral-") {
		return "mistral"
	}

	// Moonshot Kimi models (when not ':cloud')
	if strings.HasPrefix(lower, "kimi-") || strings.HasPrefix(lower, "moonshot-") {
		return "kimi"
	}

	// Alibaba QwenCloud / DashScope models (when not local Ollama tag with ':')
	// Matches qwen3.8-max, qwen3-coder-plus, qwen3.7-plus, qwen2.5-max, qwen-max, qwq-plus, qvq-max, etc.
	if strings.HasPrefix(lower, "qwen") || strings.HasPrefix(lower, "qwq") || strings.HasPrefix(lower, "qvq") {
		return "qwen"
	}

	// QwenCloud also hosts non-colon deepseek-v4-* and glm-5.* models
	if strings.HasPrefix(lower, "deepseek-v4") || strings.HasPrefix(lower, "glm-5") {
		return "qwen"
	}

	// Otherwise return configuredProvider (default "ollama").
	if configuredProvider != "" {
		return configuredProvider
	}
	return "ollama"
}

// cleanModel strips provider prefixes (e.g. "openai/gpt-4o" -> "gpt-4o",
// but preserves model names for providers like openrouter: "openrouter/anthropic/claude" -> "anthropic/claude").
func cleanModel(model string, provider string) string {
	m := strings.TrimSpace(model)
	lower := strings.ToLower(m)
	if idx := strings.Index(lower, "/"); idx != -1 {
		prefix := lower[:idx]
		if prefix == strings.ToLower(provider) || knownProviders[prefix] {
			return m[idx+1:]
		}
		for _, cp := range loadCustomProvidersForLLM() {
			if strings.EqualFold(cp.ID, prefix) {
				return m[idx+1:]
			}
		}
	}
	return m
}

// ChatStream executes a streaming chat completion with the detected/specified LLM provider.
func ChatStream(ctx context.Context, opts ChatOptions, cb StreamCallbacks) (*ChatResult, error) {
	if opts.Provider == "" {
		opts.Provider = DetectProvider(opts.Model, "")
	}
	if opts.Provider == "kimi" {
		if getAPIKey(opts, "kimi", "moonshot", "MOONSHOT_API_KEY", "KIMI_API_KEY") == "" &&
			getAPIKey(opts, "qwen", "dashscope", "DASHSCOPE_API_KEY", "QWEN_API_KEY") != "" {
			opts.Provider = "qwen"
		}
	}
	opts.Model = cleanModel(opts.Model, opts.Provider)

	for _, cp := range loadCustomProvidersForLLM() {
		if strings.EqualFold(cp.ID, opts.Provider) {
			provID := strings.ToLower(cp.ID)
			opts.Provider = provID
			if opts.BaseURLs == nil {
				opts.BaseURLs = make(map[string]string)
			}
			if opts.BaseURLs[provID] == "" && cp.BaseURL != "" {
				opts.BaseURLs[provID] = cp.BaseURL
			}
			if opts.APIKeys == nil {
				opts.APIKeys = make(map[string]string)
			}
			if opts.APIKeys[provID] == "" && cp.APIKey != "" {
				opts.APIKeys[provID] = cp.APIKey
			}
			break
		}
	}

	timeout := 600 * time.Second
	if opts.TimeoutSec > 0 {
		timeout = time.Duration(opts.TimeoutSec * float64(time.Second))
		effort := NormalizeEffort(opts.ReasoningEffort)
		if (effort == "high" || effort == "max") && timeout < 600*time.Second && SupportsReasoningEffort(opts.Model, opts.Provider) {
			timeout = 600 * time.Second
		}
	}
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	switch opts.Provider {
	case "ollama", "ollama-api":
		return streamOllama(reqCtx, opts, cb)
	case "anthropic":
		return streamAnthropic(reqCtx, opts, cb)
	case "google":
		return streamGemini(reqCtx, opts, cb)
	case "openai", "groq", "openrouter", "mistral", "xai", "cerebras", "kimi", "qwen", "lmstudio", "llamacpp":
		return streamOpenAICompat(reqCtx, opts, cb)
	default:
		return streamOpenAICompat(reqCtx, opts, cb)
	}
}

// nextPowerOf2 computes the smallest power of 2 greater than or equal to n.
func nextPowerOf2(n int) int {
	if n <= 1 {
		return 1
	}
	p := 1
	for p < n {
		p <<= 1
	}
	return p
}

// estimateTokens estimates total token count from system prompt, messages, and tools.
func estimateTokens(opts ChatOptions) int {
	chars := len(opts.SystemPrompt)
	for _, msg := range opts.Messages {
		chars += len(msg.Content)
		chars += len(msg.Thinking)
		chars += len(msg.ToolName)
		chars += len(msg.ToolCallID)
		for _, tc := range msg.ToolCalls {
			chars += len(tc.Name)
			if len(tc.Arguments) > 0 {
				if b, err := json.Marshal(tc.Arguments); err == nil {
					chars += len(b)
				}
			}
		}
	}
	if len(opts.Tools) > 0 {
		if b, err := json.Marshal(opts.Tools); err == nil {
			chars += len(b)
		}
	}
	return chars / 3
}

// computeOllamaNumCtx computes dynamic num_ctx: max(16384, nextPowerOf2(estimatedTokens + 4096)), capped at 65536.
func computeOllamaNumCtx(opts ChatOptions) int {
	if opts.ContextWindowOverride > 0 {
		return opts.ContextWindowOverride
	}
	estTokens := estimateTokens(opts)
	target := estTokens + 4096
	numCtx := nextPowerOf2(target)
	if numCtx < 16384 {
		numCtx = 16384
	}
	if numCtx > 65536 {
		numCtx = 65536
	}
	return numCtx
}

// getAPIKey retrieves the API key for a provider from options or environment variables.
func getAPIKey(opts ChatOptions, provider string, envVars ...string) string {
	if opts.APIKeys != nil {
		if k, ok := opts.APIKeys[provider]; ok && strings.TrimSpace(k) != "" {
			return strings.TrimSpace(k)
		}
		if provider == "google" || provider == "gemini" {
			if k, ok := opts.APIKeys["google"]; ok && strings.TrimSpace(k) != "" {
				return strings.TrimSpace(k)
			}
			if k, ok := opts.APIKeys["gemini"]; ok && strings.TrimSpace(k) != "" {
				return strings.TrimSpace(k)
			}
		} else if provider == "ollama-api" || provider == "ollama" {
			if k, ok := opts.APIKeys["ollama-api"]; ok && strings.TrimSpace(k) != "" {
				return strings.TrimSpace(k)
			}
			if k, ok := opts.APIKeys["ollama"]; ok && strings.TrimSpace(k) != "" {
				return strings.TrimSpace(k)
			}
		}
	}
	for _, env := range envVars {
		if val := strings.TrimSpace(os.Getenv(env)); val != "" {
			return val
		}
	}
	return ""
}

// getBaseURL retrieves the base URL for a provider from options or defaults.
func getBaseURL(opts ChatOptions, provider string, defaultURL string) string {
	if opts.BaseURLs != nil {
		if u, ok := opts.BaseURLs[provider]; ok && u != "" {
			return strings.TrimRight(u, "/")
		}
	}
	return strings.TrimRight(defaultURL, "/")
}

// sanitizeSchema recursively strips unsupported keys for strict JSON Schema validators (e.g. Google Gemini).
func sanitizeSchema(val any) any {
	switch v := val.(type) {
	case map[string]any:
		cleaned := make(map[string]any, len(v))
		for k, item := range v {
			if k == "$schema" || k == "additionalProperties" || k == "$ref" || k == "definitions" || k == "$defs" {
				continue
			}
			cleaned[k] = sanitizeSchema(item)
		}
		return cleaned
	case []any:
		cleaned := make([]any, len(v))
		for i, item := range v {
			cleaned[i] = sanitizeSchema(item)
		}
		return cleaned
	default:
		return val
	}
}

const (
	openThinkTag  = "<think>"
	closeThinkTag = "</think>"
)

// ThinkStreamParser separates inline <think>...</think> blocks from content during streaming.
type ThinkStreamParser struct {
	cb       StreamCallbacks
	inThink  bool
	buf      string
	content  strings.Builder
	thinking strings.Builder
}

// NewThinkStreamParser creates a new ThinkStreamParser.
func NewThinkStreamParser(cb StreamCallbacks) *ThinkStreamParser {
	return &ThinkStreamParser{cb: cb}
}

// FeedThinking feeds explicitly separated thinking tokens.
func (p *ThinkStreamParser) FeedThinking(text string) {
	if text == "" {
		return
	}
	p.thinking.WriteString(text)
	if p.cb.OnThinking != nil {
		p.cb.OnThinking(text)
	}
}

// FeedContent feeds text content chunks, extracting any <think>...</think> blocks in real time.
func (p *ThinkStreamParser) FeedContent(text string) {
	if text == "" {
		return
	}
	p.buf += text

	for len(p.buf) > 0 {
		if !p.inThink {
			idx := strings.Index(p.buf, openThinkTag)
			if idx != -1 {
				if idx > 0 {
					chunk := p.buf[:idx]
					p.content.WriteString(chunk)
					if p.cb.OnText != nil {
						p.cb.OnText(chunk)
					}
				}
				p.inThink = true
				p.buf = p.buf[idx+len(openThinkTag):]
				continue
			}

			// Check if buffer ends with a partial prefix of "<think>"
			keep := 0
			for i := 1; i < len(openThinkTag) && i <= len(p.buf); i++ {
				suffix := p.buf[len(p.buf)-i:]
				if strings.HasPrefix(openThinkTag, suffix) {
					keep = i
				}
			}

			emitLen := len(p.buf) - keep
			if emitLen > 0 {
				chunk := p.buf[:emitLen]
				p.content.WriteString(chunk)
				if p.cb.OnText != nil {
					p.cb.OnText(chunk)
				}
				p.buf = p.buf[emitLen:]
			}
			break
		} else {
			idx := strings.Index(p.buf, closeThinkTag)
			if idx != -1 {
				if idx > 0 {
					chunk := p.buf[:idx]
					p.thinking.WriteString(chunk)
					if p.cb.OnThinking != nil {
						p.cb.OnThinking(chunk)
					}
				}
				p.inThink = false
				p.buf = p.buf[idx+len(closeThinkTag):]
				continue
			}

			// Check if buffer ends with a partial prefix of "</think>"
			keep := 0
			for i := 1; i < len(closeThinkTag) && i <= len(p.buf); i++ {
				suffix := p.buf[len(p.buf)-i:]
				if strings.HasPrefix(closeThinkTag, suffix) {
					keep = i
				}
			}

			emitLen := len(p.buf) - keep
			if emitLen > 0 {
				chunk := p.buf[:emitLen]
				p.thinking.WriteString(chunk)
				if p.cb.OnThinking != nil {
					p.cb.OnThinking(chunk)
				}
				p.buf = p.buf[emitLen:]
			}
			break
		}
	}
}

// Flush flushes any remaining buffered text.
func (p *ThinkStreamParser) Flush() {
	if len(p.buf) > 0 {
		if p.inThink {
			p.thinking.WriteString(p.buf)
			if p.cb.OnThinking != nil {
				p.cb.OnThinking(p.buf)
			}
		} else {
			p.content.WriteString(p.buf)
			if p.cb.OnText != nil {
				p.cb.OnText(p.buf)
			}
		}
		p.buf = ""
	}
}

// Result returns the finalized content and thinking text.
func (p *ThinkStreamParser) Result() (string, string) {
	p.Flush()
	return p.content.String(), p.thinking.String()
}
