package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var contextExceededRegex = regexp.MustCompile(`(?:n_prompt_tokens["']?\s*:\s*(\d+)|request\s*\((\d+)\s*tokens\)\s*exceeds)`)

func extractExceededContextTokens(errText string) int {
	matches := contextExceededRegex.FindStringSubmatch(errText)
	if len(matches) > 1 {
		for i := 1; i < len(matches); i++ {
			if matches[i] != "" {
				if n, err := strconv.Atoi(matches[i]); err == nil && n > 0 {
					return n
				}
			}
		}
	}
	return 0
}

func prepareOllamaMessages(opts ChatOptions) []map[string]any {
	var prepared []map[string]any

	if opts.SystemPrompt != "" {
		prepared = append(prepared, map[string]any{
			"role":    "system",
			"content": opts.SystemPrompt,
		})
	}

	for _, msg := range opts.Messages {
		entry := map[string]any{
			"role":    msg.Role,
			"content": msg.Content,
		}

		if msg.Role == "assistant" && msg.Thinking != "" {
			entry["thinking"] = msg.Thinking
		}

		if msg.Role == "tool" {
			if msg.ToolName != "" {
				entry["tool_name"] = msg.ToolName
			}
			if msg.ToolCallID != "" {
				entry["tool_call_id"] = msg.ToolCallID
			}
		}

		if len(msg.Images) > 0 {
			var images []string
			for _, img := range msg.Images {
				if img.Data != "" {
					images = append(images, img.Data)
				}
			}
			if len(images) > 0 {
				entry["images"] = images
			}
		}

		if len(msg.ToolCalls) > 0 {
			var tcs []map[string]any
			for _, tc := range msg.ToolCalls {
				tcs = append(tcs, map[string]any{
					"id":   tc.ID,
					"type": "function",
					"function": map[string]any{
						"name":      tc.Name,
						"arguments": tc.Arguments,
					},
				})
			}
			entry["tool_calls"] = tcs
		}

		prepared = append(prepared, entry)
	}

	return prepared
}

func prepareOllamaTools(tools []map[string]any) []any {
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

func streamOllama(ctx context.Context, opts ChatOptions, cb StreamCallbacks) (*ChatResult, error) {
	baseURL := opts.OllamaBaseURL
	var apiKey string

	if opts.Provider == "ollama-api" {
		baseURL = opts.OllamaAPIBaseURL
		if baseURL == "" {
			baseURL = getBaseURL(opts, "ollama-api", "https://api.ollama.com")
		}
		apiKey = getAPIKey(opts, "ollama-api", "OLLAMA_API_KEY")
	} else {
		if baseURL == "" {
			baseURL = getBaseURL(opts, "ollama", "http://localhost:11434")
		}
		apiKey = getAPIKey(opts, "ollama", "OLLAMA_API_KEY")
	}

	baseURL = strings.TrimRight(baseURL, "/")
	baseURL = strings.TrimSuffix(baseURL, "/v1")
	chatEndpoint := baseURL + "/api/chat"

	preparedMessages := prepareOllamaMessages(opts)
	preparedTools := prepareOllamaTools(opts.Tools)
	numCtx := computeOllamaNumCtx(opts)

	isCloudModel := opts.Provider == "ollama-api" || strings.HasSuffix(strings.ToLower(strings.TrimSpace(opts.Model)), ":cloud")

	buildPayload := func(currentCtx int) map[string]any {
		optionsMap := map[string]any{}
		if isCloudModel {
			if opts.ContextWindowOverride > 0 {
				optionsMap["num_ctx"] = opts.ContextWindowOverride
			}
		} else {
			optionsMap["num_ctx"] = currentCtx
		}
		if opts.MaxTokens > 0 {
			optionsMap["num_predict"] = opts.MaxTokens
		}

		payload := map[string]any{
			"model":    opts.Model,
			"messages": preparedMessages,
			"stream":   true,
		}
		effort := NormalizeEffort(opts.ReasoningEffort)
		if effort == "none" {
			payload["thinking"] = false
		} else if effort == "max" {
			payload["thinking"] = "max"
		} else if effort == "low" {
			payload["thinking"] = "low"
		} else if effort == "high" || effort == "medium" {
			payload["thinking"] = "high"
		}
		if len(optionsMap) > 0 {
			payload["options"] = optionsMap
		}
		if len(preparedTools) > 0 {
			payload["tools"] = preparedTools
		}
		return payload
	}

	sendReq := func(currentCtx int) (*http.Response, []byte, error) {
		bodyBytes, err := json.Marshal(buildPayload(currentCtx))
		if err != nil {
			return nil, nil, fmt.Errorf("failed to marshal ollama payload: %w", err)
		}

		req, err := http.NewRequestWithContext(ctx, "POST", chatEndpoint, bytes.NewReader(bodyBytes))
		if err != nil {
			return nil, nil, fmt.Errorf("failed to create ollama request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "Unjess-Daemon/1.0")
		if apiKey != "" && apiKey != "ollama" {
			req.Header.Set("Authorization", "Bearer "+apiKey)
		}

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, nil, fmt.Errorf("cannot connect to Ollama at %s: %w", baseURL, err)
		}
		if resp.StatusCode != http.StatusOK {
			errBytes, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			return resp, errBytes, nil
		}
		return resp, nil, nil
	}

	resp, bodyBytes, err := sendReq(numCtx)
	if err != nil {
		return nil, err
	}

	// Auto-fallback when model does not support vision/multimodal image input
	if resp != nil && resp.StatusCode == http.StatusBadRequest {
		errText := string(bodyBytes)
		lowerErr := strings.ToLower(errText)
		if strings.Contains(lowerErr, "image") || strings.Contains(lowerErr, "vision") || strings.Contains(lowerErr, "multimodal") {
			hasImages := false
			for i := range preparedMessages {
				if _, ok := preparedMessages[i]["images"]; ok {
					delete(preparedMessages[i], "images")
					hasImages = true
					if c, ok := preparedMessages[i]["content"].(string); ok {
						preparedMessages[i]["content"] = c + "\n[Note: Active model does not support raw image input; rely on the structured DOM/text output above.]"
					} else {
						preparedMessages[i]["content"] = "[Note: Active model does not support raw image input; rely on the structured DOM/text output above.]"
					}
				}
			}
			if hasImages {
				resp, bodyBytes, err = sendReq(numCtx)
				if err != nil {
					return nil, err
				}
			}
		}
	}

	// Auto-recovery if request exceeds Ollama context size
	if resp != nil && resp.StatusCode == http.StatusBadRequest {
		errText := string(bodyBytes)
		if strings.Contains(errText, "exceed_context_size_error") ||
			strings.Contains(errText, "exceeds the available context size") ||
			strings.Contains(errText, "exceeds available context size") {
			nPrompt := extractExceededContextTokens(errText)
			newCtx := numCtx * 2
			if nPrompt > 0 {
				newCtx = nPrompt + 4096
			}
			newCtx = nextPowerOf2(newCtx)
			if newCtx < 16384 {
				newCtx = 16384
			}
			if newCtx > 65536 {
				newCtx = 65536
			}

			// Retry with enlarged context
			resp, bodyBytes, err = sendReq(newCtx)
			if err != nil {
				return nil, err
			}
		} else {
			return nil, fmt.Errorf("ollama API error (%d): %s", resp.StatusCode, errText)
		}
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ollama API error (%d): %s", resp.StatusCode, string(bodyBytes))
	}
	defer resp.Body.Close()

	parser := NewThinkStreamParser(cb)
	result := &ChatResult{}
	scanner := bufio.NewScanner(resp.Body)
	// Allow large tokens buffer
	buf := make([]byte, 1024*1024)
	scanner.Buffer(buf, 10*1024*1024)

	type ollamaToolCallRaw struct {
		ID       string `json:"id"`
		Type     string `json:"type"`
		Function struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		} `json:"function"`
	}

	type ollamaChunk struct {
		Model      string `json:"model"`
		CreatedAt  string `json:"created_at"`
		Done       bool   `json:"done"`
		DoneReason string `json:"done_reason"`
		Message    struct {
			Role      string              `json:"role"`
			Content   string              `json:"content"`
			Thinking  string              `json:"thinking"`
			ToolCalls []ollamaToolCallRaw `json:"tool_calls"`
		} `json:"message"`
		PromptEvalCount int `json:"prompt_eval_count"`
		EvalCount       int `json:"eval_count"`
	}

	callSeq := 0
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var chunk ollamaChunk
		if err := json.Unmarshal([]byte(line), &chunk); err != nil {
			continue
		}

		// 1. Thinking from message.thinking
		if chunk.Message.Thinking != "" {
			parser.FeedThinking(chunk.Message.Thinking)
		}

		// 2. Content from message.content (with inline <think>...</think> extraction)
		if chunk.Message.Content != "" {
			parser.FeedContent(chunk.Message.Content)
		}

		// 3. Tool calls
		if len(chunk.Message.ToolCalls) > 0 {
			for _, tcRaw := range chunk.Message.ToolCalls {
				args := make(map[string]any)
				rawArgs := bytes.TrimSpace(tcRaw.Function.Arguments)
				if len(rawArgs) > 0 {
					// Try direct map unmarshal
					if err := json.Unmarshal(rawArgs, &args); err != nil {
						// Could be a JSON-encoded string
						var strArgs string
						if errStr := json.Unmarshal(rawArgs, &strArgs); errStr == nil {
							if errMap := json.Unmarshal([]byte(strArgs), &args); errMap != nil {
								args = map[string]any{"_raw": strArgs}
							}
						} else {
							args = map[string]any{"_raw": string(rawArgs)}
						}
					}
				}

				tcID := tcRaw.ID
				if tcID == "" {
					callSeq++
					tcID = fmt.Sprintf("call_%d_%d", time.Now().UnixNano()%1000000, callSeq)
				}

				result.ToolCalls = append(result.ToolCalls, ToolCall{
					ID:        tcID,
					Name:      tcRaw.Function.Name,
					Arguments: args,
				})
			}
		}

		// 4. Token counts and stop reason on done
		if chunk.Done {
			result.InputTokens = chunk.PromptEvalCount
			result.OutputTokens = chunk.EvalCount
			if chunk.DoneReason != "" {
				result.StopReason = chunk.DoneReason
			}
		}
	}

	if err := scanner.Err(); err != nil && err != io.EOF {
		return nil, fmt.Errorf("error reading ollama stream: %w", err)
	}

	content, thinking := parser.Result()
	result.Content = content
	result.Thinking = thinking

	return result, nil
}
