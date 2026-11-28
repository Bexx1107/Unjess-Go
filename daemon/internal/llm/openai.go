package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

func getOpenAIBaseURL(opts ChatOptions, provider string) string {
	switch provider {
	case "openai":
		return getBaseURL(opts, provider, "https://api.openai.com/v1")
	case "groq":
		return getBaseURL(opts, provider, "https://api.groq.com/openai/v1")
	case "openrouter":
		return getBaseURL(opts, provider, "https://openrouter.ai/api/v1")
	case "mistral":
		return getBaseURL(opts, provider, "https://api.mistral.ai/v1")
	case "xai":
		return getBaseURL(opts, provider, "https://api.x.ai/v1")
	case "cerebras":
		return getBaseURL(opts, provider, "https://api.cerebras.ai/v1")
	case "kimi":
		return getBaseURL(opts, provider, "https://api.moonshot.ai/v1")
	case "qwen":
		return getBaseURL(opts, provider, "https://dashscope-intl.aliyuncs.com/compatible-mode/v1")
	case "lmstudio":
		u := getBaseURL(opts, provider, "http://localhost:1234/v1")
		if !strings.HasSuffix(u, "/v1") {
			u = u + "/v1"
		}
		return u
	case "llamacpp":
		u := getBaseURL(opts, provider, "http://localhost:8080/v1")
		if !strings.HasSuffix(u, "/v1") {
			u = u + "/v1"
		}
		return u
	default:
		return getBaseURL(opts, provider, "https://api.openai.com/v1")
	}
}

func getOpenAIAPIKey(opts ChatOptions, provider string) string {
	switch provider {
	case "openai":
		return getAPIKey(opts, provider, "OPENAI_API_KEY")
	case "groq":
		return getAPIKey(opts, provider, "GROQ_API_KEY")
	case "openrouter":
		return getAPIKey(opts, provider, "OPENROUTER_API_KEY")
	case "mistral":
		return getAPIKey(opts, provider, "MISTRAL_API_KEY")
	case "xai":
		return getAPIKey(opts, provider, "XAI_API_KEY")
	case "cerebras":
		return getAPIKey(opts, provider, "CEREBRAS_API_KEY")
	case "kimi":
		return getAPIKey(opts, provider, "moonshot", "MOONSHOT_API_KEY", "KIMI_API_KEY")
	case "qwen":
		return getAPIKey(opts, provider, "dashscope", "DASHSCOPE_API_KEY", "QWEN_API_KEY")
	default:
		envKey := strings.ToUpper(strings.ReplaceAll(provider, "-", "_")) + "_API_KEY"
		return getAPIKey(opts, provider, envKey)
	}
}

func prepareOpenAIMessages(opts ChatOptions) []map[string]any {
	var prepared []map[string]any
	noNativeTools := len(opts.Tools) == 0

	if opts.SystemPrompt != "" {
		prepared = append(prepared, map[string]any{
			"role":    "system",
			"content": opts.SystemPrompt,
		})
	}

	for _, msg := range opts.Messages {
		switch msg.Role {
		case "assistant":
			content := msg.Content
			if noNativeTools && strings.TrimSpace(content) == "" && len(msg.ToolCalls) > 0 {
				var parts []string
				for _, tc := range msg.ToolCalls {
					b, _ := json.Marshal(map[string]any{
						"name":      tc.Name,
						"arguments": tc.Arguments,
					})
					parts = append(parts, "<tool_call>"+string(b)+"</tool_call>")
				}
				content = strings.Join(parts, "\n")
			}
			entry := map[string]any{
				"role":    "assistant",
				"content": content,
			}
			if !noNativeTools {
				// DeepSeek R1/V3.2 and Kimi k2.5/k3 thinking models require reasoning_content on assistant tool-call turns
				if len(msg.ToolCalls) > 0 {
					if msg.Thinking != "" {
						entry["reasoning_content"] = msg.Thinking
					} else if opts.Provider == "kimi" {
						entry["reasoning_content"] = "Analyzing context and invoking tool."
					}
				}
				if len(msg.ToolCalls) > 0 {
					var tcs []map[string]any
					for _, tc := range msg.ToolCalls {
						argsForLLM := tc.Arguments
						if tc.Name == "render_ui_widget" && argsForLLM != nil {
							if htmlStr, ok := argsForLLM["html"].(string); ok && len(htmlStr) > 240 {
								slim := make(map[string]any, len(argsForLLM))
								for k, v := range argsForLLM {
									slim[k] = v
								}
								slim["html"] = fmt.Sprintf("[HTML widget rendered inline (%d chars)]", len(htmlStr))
								argsForLLM = slim
							}
						}
						argBytes, _ := json.Marshal(argsForLLM)
						tcs = append(tcs, map[string]any{
							"id":   tc.ID,
							"type": "function",
							"function": map[string]any{
								"name":      tc.Name,
								"arguments": string(argBytes),
							},
						})
					}
					entry["tool_calls"] = tcs
				}
			}
			prepared = append(prepared, entry)

		case "tool":
			if noNativeTools {
				toolLabel := msg.ToolName
				if toolLabel == "" {
					toolLabel = "tool"
				}
				prepared = append(prepared, map[string]any{
					"role":    "user",
					"content": fmt.Sprintf("[Rezultat orodja %s / Tool Result]:\n%s\n\nČe zgornji rezultati že vsebujejo odgovor (npr. povzetke ali trenutne podatke), uporabniku takoj konkretno odgovori v slovenščini. Če pa moraš za podrobnosti prebrati določeno spletno stran z zgornjega seznama, lahko pokličeš <tool_call>{\"name\": \"read_url\", \"arguments\": {\"url\": \"...\"}}</tool_call>.", toolLabel, msg.Content),
				})
			} else {
				prepared = append(prepared, map[string]any{
					"role":         "tool",
					"tool_call_id": msg.ToolCallID,
					"content":      msg.Content,
				})
			}

		default: // "user"
			if len(msg.Images) > 0 {
				var parts []map[string]any
				if msg.Content != "" {
					parts = append(parts, map[string]any{
						"type": "text",
						"text": msg.Content,
					})
				}
				for _, img := range msg.Images {
					mime := img.MimeType
					if mime == "" {
						mime = "image/png"
					}
					parts = append(parts, map[string]any{
						"type": "image_url",
						"image_url": map[string]any{
							"url": fmt.Sprintf("data:%s;base64,%s", mime, img.Data),
						},
					})
				}
				prepared = append(prepared, map[string]any{
					"role":    "user",
					"content": parts,
				})
			} else {
				prepared = append(prepared, map[string]any{
					"role":    "user",
					"content": msg.Content,
				})
			}
		}
	}

	return prepared
}

func prepareOpenAITools(tools []map[string]any) []any {
	var prepared []any
	for _, tool := range tools {
		if fn, hasFn := tool["function"]; hasFn {
			prepared = append(prepared, map[string]any{
				"type":     "function",
				"function": fn,
			})
		} else {
			name, _ := tool["name"].(string)
			desc, _ := tool["description"].(string)
			params := tool["parameters"]
			if params == nil {
				params = map[string]any{"type": "object", "properties": map[string]any{}}
			}
			prepared = append(prepared, map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        name,
					"description": desc,
					"parameters":  params,
				},
			})
		}
	}
	return prepared
}

func streamOpenAICompat(ctx context.Context, opts ChatOptions, cb StreamCallbacks) (*ChatResult, error) {
	baseURL := getOpenAIBaseURL(opts, opts.Provider)
	apiKey := getOpenAIAPIKey(opts, opts.Provider)

	endpoint := strings.TrimRight(baseURL, "/") + "/chat/completions"

	preparedMessages := prepareOpenAIMessages(opts)
	preparedTools := prepareOpenAITools(opts.Tools)

	payload := map[string]any{
		"model":    opts.Model,
		"messages": preparedMessages,
		"stream":   true,
	}
	if opts.Provider == "openai" || opts.Provider == "openrouter" {
		payload["stream_options"] = map[string]any{
			"include_usage": true,
		}
	}
	if opts.Provider == "llamacpp" {
		payload["stop"] = []string{"\nuser\n", "\nmodel\n", "\nsystem\n", "⸢/TURN⸥", "⸢TURN⸥", "<end_of_turn>"}
		payload["temperature"] = 0.2
	} else if opts.Provider == "lmstudio" {
		payload["temperature"] = 0.2
	}
	if opts.MaxTokens > 0 {
		lowerModel := strings.ToLower(opts.Model)
		if opts.Provider == "openai" && (strings.HasPrefix(lowerModel, "o1") || strings.HasPrefix(lowerModel, "o3") || strings.HasPrefix(lowerModel, "o4") || strings.HasPrefix(lowerModel, "gpt-5")) {
			payload["max_completion_tokens"] = opts.MaxTokens
		} else {
			payload["max_tokens"] = opts.MaxTokens
		}
	}

	effort := NormalizeEffort(opts.ReasoningEffort)
	supportsEffort := SupportsReasoningEffort(opts.Model, opts.Provider)
	hasTools := len(preparedTools) > 0

	buildOpenAIPayload := func(includeEffort bool, includeTools bool) ([]byte, error) {
		p := make(map[string]any, len(payload)+4)
		for k, v := range payload {
			p[k] = v
		}
		if includeTools && len(preparedTools) > 0 {
			p["tools"] = preparedTools
		}
		if opts.Provider == "llamacpp" || opts.Provider == "lmstudio" {
			p["chat_template_kwargs"] = map[string]any{
				"enable_thinking": effort != "none",
			}
		}
		if includeEffort && supportsEffort {
			if opts.Provider == "qwen" {
				if effort == "none" {
					p["enable_thinking"] = false
				} else {
					p["enable_thinking"] = true
					switch effort {
					case "low":
						p["thinking_budget"] = 2048
					case "medium":
						p["thinking_budget"] = 8192
					case "high", "auto":
						p["thinking_budget"] = 16384
					case "max":
						p["thinking_budget"] = 32768
					}
				}
			} else if effort != "none" {
				switch effort {
				case "low":
					p["reasoning_effort"] = "low"
				case "medium":
					p["reasoning_effort"] = "medium"
				case "high", "max", "auto":
					p["reasoning_effort"] = "high"
				}
			}
		}
		return json.Marshal(p)
	}

	doReq := func(ep string, includeEffort bool, includeTools bool) (*http.Response, error) {
		bodyBytes, err := buildOpenAIPayload(includeEffort, includeTools)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal openai payload: %w", err)
		}
		r, reqErr := http.NewRequestWithContext(ctx, "POST", ep, bytes.NewReader(bodyBytes))
		if reqErr != nil {
			return nil, fmt.Errorf("failed to create openai request: %w", reqErr)
		}
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("User-Agent", "Unjess-Daemon/1.0")
		if apiKey != "" {
			r.Header.Set("Authorization", "Bearer "+apiKey)
		}
		return http.DefaultClient.Do(r)
	}

	activeEndpoint := endpoint
	resp, err := doReq(activeEndpoint, supportsEffort, hasTools)
	if err != nil {
		return nil, fmt.Errorf("cannot connect to %s at %s: %w", opts.Provider, activeEndpoint, err)
	}

	// Automatic regional endpoint fallback for Moonshot (Kimi) and Alibaba QwenCloud (DashScope) on 401
	if resp.StatusCode == http.StatusUnauthorized && (opts.Provider == "kimi" || opts.Provider == "qwen") {
		altEndpoint := ""
		if opts.Provider == "kimi" {
			if strings.Contains(activeEndpoint, "api.moonshot.ai") {
				altEndpoint = "https://api.moonshot.cn/v1/chat/completions"
			} else if strings.Contains(activeEndpoint, "api.moonshot.cn") {
				altEndpoint = "https://api.moonshot.ai/v1/chat/completions"
			}
		} else if opts.Provider == "qwen" {
			if strings.Contains(activeEndpoint, "dashscope-intl.aliyuncs.com") {
				altEndpoint = "https://dashscope.aliyuncs.com/compatible-mode/v1/chat/completions"
			} else if strings.Contains(activeEndpoint, "dashscope.aliyuncs.com") {
				altEndpoint = "https://dashscope-intl.aliyuncs.com/compatible-mode/v1/chat/completions"
			}
		}
		if altEndpoint != "" {
			if altResp, altErr := doReq(altEndpoint, supportsEffort, hasTools); altErr == nil {
				if altResp.StatusCode == http.StatusOK || altResp.StatusCode == http.StatusBadRequest {
					_ = resp.Body.Close()
					resp = altResp
					activeEndpoint = altEndpoint
				} else {
					_ = altResp.Body.Close()
				}
			}
		}
	}

	// Automatic fallback without effort/thinking params if a model rejects them with 400
	if resp.StatusCode == http.StatusBadRequest && supportsEffort {
		_ = resp.Body.Close()
		resp, err = doReq(activeEndpoint, false, hasTools)
		if err != nil {
			return nil, fmt.Errorf("cannot connect to %s at %s: %w", opts.Provider, activeEndpoint, err)
		}
	}

	// Automatic fallback without tools if a model / llama.cpp Jinja chat template rejects tool calls (e.g. Cvetka-R1-27B)
	if (resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusInternalServerError || resp.StatusCode == http.StatusUnprocessableEntity) && hasTools {
		errBytes, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		errLower := strings.ToLower(string(errBytes))
		if opts.Provider == "llamacpp" || opts.Provider == "lmstudio" ||
			strings.Contains(errLower, "tool") || strings.Contains(errLower, "template") ||
			strings.Contains(errLower, "jinja") || strings.Contains(errLower, "parser") ||
			strings.Contains(errLower, "function") {
			resp, err = doReq(activeEndpoint, false, false)
			if err != nil {
				return nil, fmt.Errorf("cannot connect to %s at %s: %w", opts.Provider, activeEndpoint, err)
			}
		} else {
			return nil, fmt.Errorf("%s API error (%d): %s", opts.Provider, resp.StatusCode, string(errBytes))
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("%s API error (%d): %s", opts.Provider, resp.StatusCode, string(errBytes))
	}

	parser := NewThinkStreamParser(cb)
	result := &ChatResult{}

	type toolCallDeltaBuilder struct {
		id   string
		name string
		args strings.Builder
	}
	toolCallsByIndex := make(map[int]*toolCallDeltaBuilder)

	type openAIStreamChunk struct {
		Choices []struct {
			Index int `json:"index"`
			Delta struct {
				Role             string `json:"role"`
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
				Reasoning        string `json:"reasoning"`
				ToolCalls        []struct {
					Index    int    `json:"index"`
					ID       string `json:"id"`
					Type     string `json:"type"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"delta"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage *struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
		Error *struct {
			Code    any    `json:"code"`
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}

	scanner := bufio.NewScanner(resp.Body)
	buf := make([]byte, 1024*1024)
	scanner.Buffer(buf, 10*1024*1024)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}

		dataStr := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if dataStr == "[DONE]" {
			break
		}

		var chunk openAIStreamChunk
		if err := json.Unmarshal([]byte(dataStr), &chunk); err != nil {
			continue
		}

		if chunk.Error != nil && chunk.Error.Message != "" {
			return nil, fmt.Errorf("%s stream error (%v): %s", opts.Provider, chunk.Error.Code, chunk.Error.Message)
		}

		if chunk.Usage != nil {
			result.InputTokens = chunk.Usage.PromptTokens
			result.OutputTokens = chunk.Usage.CompletionTokens
		}

		for _, choice := range chunk.Choices {
			if choice.FinishReason != "" {
				result.StopReason = choice.FinishReason
			}
			delta := choice.Delta

			// Reasoning / Thinking content
			reasoning := delta.ReasoningContent
			if reasoning == "" {
				reasoning = delta.Reasoning
			}
			if reasoning != "" {
				parser.FeedThinking(reasoning)
			}

			// Text Content
			if delta.Content != "" {
				parser.FeedContent(delta.Content)
			}

			// Tool Call Deltas
			for _, tcDelta := range delta.ToolCalls {
				idx := tcDelta.Index
				b, exists := toolCallsByIndex[idx]
				if !exists {
					b = &toolCallDeltaBuilder{}
					toolCallsByIndex[idx] = b
				}
				if tcDelta.ID != "" {
					b.id = tcDelta.ID
				}
				if tcDelta.Function.Name != "" {
					b.name = tcDelta.Function.Name
					if cb.OnToolStream != nil {
						cb.OnToolStream(b.name, b.args.Len(), "")
					}
				}
				if tcDelta.Function.Arguments != "" {
					b.args.WriteString(tcDelta.Function.Arguments)
					if cb.OnToolStream != nil {
						raw := b.args.String()
						preview := raw
						if len(preview) > 600 {
							preview = preview[:240] + "\n...\n" + preview[len(preview)-320:]
						}
						cb.OnToolStream(b.name, len(raw), preview)
					}
				}
			}
		}
	}

	if err := scanner.Err(); err != nil && err != io.EOF {
		return nil, fmt.Errorf("error reading %s stream: %w", opts.Provider, err)
	}

	// Assemble final tool calls
	if len(toolCallsByIndex) > 0 {
		var indices []int
		for idx := range toolCallsByIndex {
			indices = append(indices, idx)
		}
		sort.Ints(indices)

		for _, idx := range indices {
			b := toolCallsByIndex[idx]
			tcID := b.id
			if tcID == "" {
				tcID = fmt.Sprintf("call_%d_%d", time.Now().UnixNano()%1000000, idx+1)
			}

			args := make(map[string]any)
			rawArgs := strings.TrimSpace(b.args.String())
			if rawArgs != "" {
				if err := json.Unmarshal([]byte(rawArgs), &args); err != nil {
					args = map[string]any{"_raw": rawArgs}
				}
			}

			result.ToolCalls = append(result.ToolCalls, ToolCall{
				ID:        tcID,
				Name:      b.name,
				Arguments: args,
			})
		}
	}

	content, thinking := parser.Result()
	result.Content = content
	result.Thinking = thinking

	return result, nil
}
