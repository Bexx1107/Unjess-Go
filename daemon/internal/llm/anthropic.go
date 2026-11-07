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
)

func prepareAnthropicMessages(messages []Message) []map[string]any {
	var converted []map[string]any

	for _, msg := range messages {
		switch msg.Role {
		case "assistant":
			var blocks []any
			if msg.Content != "" {
				blocks = append(blocks, map[string]any{
					"type": "text",
					"text": msg.Content,
				})
			}
			for _, tc := range msg.ToolCalls {
				inputArgs := tc.Arguments
				if inputArgs == nil {
					inputArgs = map[string]any{}
				}
				blocks = append(blocks, map[string]any{
					"type":  "tool_use",
					"id":    tc.ID,
					"name":  tc.Name,
					"input": inputArgs,
				})
			}
			if len(blocks) == 0 {
				blocks = append(blocks, map[string]any{
					"type": "text",
					"text": "...",
				})
			}
			converted = append(converted, map[string]any{
				"role":    "assistant",
				"content": blocks,
			})

		case "tool":
			trBlock := map[string]any{
				"type":        "tool_result",
				"tool_use_id": msg.ToolCallID,
				"content":     msg.Content,
			}
			// Merge consecutive tool results into the same user turn
			if len(converted) > 0 && converted[len(converted)-1]["role"] == "user" {
				if existingBlocks, ok := converted[len(converted)-1]["content"].([]any); ok {
					converted[len(converted)-1]["content"] = append(existingBlocks, trBlock)
					continue
				}
			}
			converted = append(converted, map[string]any{
				"role":    "user",
				"content": []any{trBlock},
			})

		default: // "user"
			var blocks []any
			for _, img := range msg.Images {
				mime := img.MimeType
				if mime == "" {
					mime = "image/png"
				}
				blocks = append(blocks, map[string]any{
					"type": "image",
					"source": map[string]any{
						"type":       "base64",
						"media_type": mime,
						"data":       img.Data,
					},
				})
			}
			if msg.Content != "" {
				blocks = append(blocks, map[string]any{
					"type": "text",
					"text": msg.Content,
				})
			}
			if len(blocks) == 0 {
				blocks = append(blocks, map[string]any{
					"type": "text",
					"text": "...",
				})
			}
			converted = append(converted, map[string]any{
				"role":    "user",
				"content": blocks,
			})
		}
	}

	// Merge consecutive items with the same role
	var merged []map[string]any
	for _, item := range converted {
		if len(merged) == 0 {
			merged = append(merged, item)
			continue
		}
		last := merged[len(merged)-1]
		if last["role"] == item["role"] {
			var lastBlocks []any
			if lb, ok := last["content"].([]any); ok {
				lastBlocks = lb
			} else if s, ok := last["content"].(string); ok {
				lastBlocks = []any{map[string]any{"type": "text", "text": s}}
			}
			var itemBlocks []any
			if ib, ok := item["content"].([]any); ok {
				itemBlocks = ib
			} else if s, ok := item["content"].(string); ok {
				itemBlocks = []any{map[string]any{"type": "text", "text": s}}
			}
			last["content"] = append(lastBlocks, itemBlocks...)
		} else {
			merged = append(merged, item)
		}
	}

	// Ensure the first item is "user"
	if len(merged) > 0 && merged[0]["role"] == "assistant" {
		merged = append([]map[string]any{{
			"role": "user",
			"content": []any{
				map[string]any{
					"type": "text",
					"text": "[Context from prior session]",
				},
			},
		}}, merged...)
	}

	return merged
}

func prepareAnthropicTools(tools []map[string]any) []any {
	var prepared []any
	for _, tool := range tools {
		name, _ := tool["name"].(string)
		desc, _ := tool["description"].(string)
		params := tool["parameters"]
		if params == nil {
			params = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		prepared = append(prepared, map[string]any{
			"name":         name,
			"description":  desc,
			"input_schema": params,
		})
	}
	return prepared
}

func streamAnthropic(ctx context.Context, opts ChatOptions, cb StreamCallbacks) (*ChatResult, error) {
	baseURL := getBaseURL(opts, "anthropic", "https://api.anthropic.com/v1")
	apiKey := getAPIKey(opts, "anthropic", "ANTHROPIC_API_KEY")

	endpoint := strings.TrimRight(baseURL, "/") + "/messages"

	lowerModel := strings.ToLower(opts.Model)
	effort := NormalizeEffort(opts.ReasoningEffort)

	isAdaptiveModel := strings.Contains(lowerModel, "-5") ||
		strings.Contains(lowerModel, "fable") ||
		strings.Contains(lowerModel, "4-8") ||
		strings.Contains(lowerModel, "4-7") ||
		strings.Contains(lowerModel, "4-6")

	isBudgetThinkingModel := !isAdaptiveModel && (strings.Contains(lowerModel, "4-5") || strings.Contains(lowerModel, "3-7"))

	maxTokens := opts.MaxTokens
	if maxTokens <= 0 {
		if effort == "max" && isAdaptiveModel {
			maxTokens = 64000
		} else if effort == "high" || (effort == "auto" && (isAdaptiveModel || isBudgetThinkingModel)) {
			maxTokens = 32768
		} else {
			maxTokens = 16384
		}
	}

	sendAnthropicReq := func(tokens int, mode string, curEffort string, sysPrompt string) (*http.Response, error) {
		payload := map[string]any{
			"model":      opts.Model,
			"max_tokens": tokens,
			"stream":     true,
			"messages":   prepareAnthropicMessages(opts.Messages),
		}
		if sysPrompt != "" {
			payload["system"] = sysPrompt
		}
		if len(opts.Tools) > 0 {
			payload["tools"] = prepareAnthropicTools(opts.Tools)
		}

		if mode == "adaptive_with_effort" && curEffort != "none" {
			payload["thinking"] = map[string]any{
				"type":    "adaptive",
				"display": "summarized",
			}
			effortVal := curEffort
			if effortVal == "auto" {
				effortVal = "high"
			}
			payload["output_config"] = map[string]any{
				"effort": effortVal,
			}
		} else if mode == "adaptive_only" && curEffort != "none" {
			payload["thinking"] = map[string]any{
				"type":    "adaptive",
				"display": "summarized",
			}
		} else if mode == "budget" && curEffort != "none" {
			budget := 8192
			switch curEffort {
			case "low":
				budget = 2048
			case "medium":
				budget = 6144
			case "high", "max":
				budget = 12288
			}
			if budget >= tokens {
				budget = tokens / 2
			}
			if budget >= 1024 {
				payload["thinking"] = map[string]any{
					"type":          "enabled",
					"budget_tokens": budget,
				}
			}
		}

		bodyBytes, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal anthropic payload: %w", err)
		}

		req, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(bodyBytes))
		if err != nil {
			return nil, fmt.Errorf("failed to create anthropic request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "Unjess-Daemon/1.0")
		req.Header.Set("anthropic-version", "2023-06-01")
		if apiKey != "" {
			req.Header.Set("x-api-key", apiKey)
		}

		return http.DefaultClient.Do(req)
	}

	initialMode := "plain"
	if effort != "none" {
		if isAdaptiveModel {
			initialMode = "adaptive_with_effort"
		} else if isBudgetThinkingModel {
			initialMode = "budget"
		}
	}

	readStream := func(tokens int, mode string, curEffort string, sysPrompt string) (*ChatResult, string, string, string, error) {
		resp, err := sendAnthropicReq(tokens, mode, curEffort, sysPrompt)
		if err != nil {
			return nil, "", "", "", fmt.Errorf("cannot connect to Anthropic at %s: %w", endpoint, err)
		}

		if resp.StatusCode == http.StatusBadRequest {
			errBytes, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			errText := string(errBytes)
			lowerErr := strings.ToLower(errText)
			retryTokens := tokens
			if opts.MaxTokens <= 0 && retryTokens > 32768 && strings.Contains(lowerErr, "max_tokens") {
				retryTokens = 32768
			} else if opts.MaxTokens <= 0 && retryTokens > 16384 && strings.Contains(lowerErr, "max_tokens") {
				retryTokens = 16384
			} else if opts.MaxTokens <= 0 && retryTokens > 8192 && strings.Contains(lowerErr, "max_tokens") {
				retryTokens = 8192
			}

			if mode == "adaptive_with_effort" {
				resp, err = sendAnthropicReq(retryTokens, "adaptive_only", curEffort, sysPrompt)
				if err != nil {
					return nil, "", "", "", fmt.Errorf("cannot connect to Anthropic at %s: %w", endpoint, err)
				}
				if resp.StatusCode == http.StatusBadRequest {
					_ = resp.Body.Close()
					if opts.MaxTokens <= 0 && retryTokens > 8192 {
						retryTokens = 8192
					}
					resp, err = sendAnthropicReq(retryTokens, "plain", "none", sysPrompt)
					if err != nil {
						return nil, "", "", "", fmt.Errorf("cannot connect to Anthropic at %s: %w", endpoint, err)
					}
				}
			} else if mode != "plain" || retryTokens != tokens {
				if opts.MaxTokens <= 0 && retryTokens > 8192 && strings.Contains(lowerErr, "max_tokens") {
					retryTokens = 8192
				}
				resp, err = sendAnthropicReq(retryTokens, "plain", "none", sysPrompt)
				if err != nil {
					return nil, "", "", "", fmt.Errorf("cannot connect to Anthropic at %s: %w", endpoint, err)
				}
			} else {
				return nil, "", "", "", fmt.Errorf("anthropic API error (%d): %s", http.StatusBadRequest, errText)
			}
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			errBytes, _ := io.ReadAll(resp.Body)
			return nil, "", "", "", fmt.Errorf("anthropic API error (%d): %s", resp.StatusCode, string(errBytes))
		}

		parser := NewThinkStreamParser(cb)
		result := &ChatResult{}

		type anthropicToolBuilder struct {
			id   string
			name string
			json strings.Builder
		}
		toolBlocksByIndex := make(map[int]*anthropicToolBuilder)

		scanner := bufio.NewScanner(resp.Body)
		buf := make([]byte, 1024*1024)
		scanner.Buffer(buf, 10*1024*1024)

		currentEventType := ""
		stopReason := ""
		refusalExplanation := ""
		streamErrMsg := ""

		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}

			if strings.HasPrefix(line, "event:") {
				currentEventType = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
				continue
			}

			if !strings.HasPrefix(line, "data:") {
				continue
			}

			dataStr := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if dataStr == "" {
				continue
			}

			var genericEvt struct {
				Type string `json:"type"`
			}
			if err := json.Unmarshal([]byte(dataStr), &genericEvt); err != nil {
				continue
			}

			evtType := genericEvt.Type
			if evtType == "" {
				evtType = currentEventType
			}

			switch evtType {
			case "message_start":
				var startEvt struct {
					Message struct {
						Usage struct {
							InputTokens int `json:"input_tokens"`
						} `json:"usage"`
					} `json:"message"`
				}
				if err := json.Unmarshal([]byte(dataStr), &startEvt); err == nil {
					result.InputTokens = startEvt.Message.Usage.InputTokens
				}

			case "content_block_start":
				var blkStartEvt struct {
					Index        int `json:"index"`
					ContentBlock struct {
						Type string `json:"type"`
						ID   string `json:"id"`
						Name string `json:"name"`
					} `json:"content_block"`
				}
				if err := json.Unmarshal([]byte(dataStr), &blkStartEvt); err == nil {
					if blkStartEvt.ContentBlock.Type == "tool_use" {
						toolBlocksByIndex[blkStartEvt.Index] = &anthropicToolBuilder{
							id:   blkStartEvt.ContentBlock.ID,
							name: blkStartEvt.ContentBlock.Name,
						}
						if cb.OnToolStream != nil {
							cb.OnToolStream(blkStartEvt.ContentBlock.Name, 0, "")
						}
					}
				}

			case "content_block_delta":
				var deltaEvt struct {
					Index int `json:"index"`
					Delta struct {
						Type        string `json:"type"`
						Text        string `json:"text"`
						Thinking    string `json:"thinking"`
						PartialJSON string `json:"partial_json"`
					} `json:"delta"`
				}
				if err := json.Unmarshal([]byte(dataStr), &deltaEvt); err == nil {
					switch deltaEvt.Delta.Type {
					case "text_delta":
						if deltaEvt.Delta.Text != "" {
							parser.FeedContent(deltaEvt.Delta.Text)
						}
					case "thinking_delta":
						if deltaEvt.Delta.Thinking != "" {
							parser.FeedThinking(deltaEvt.Delta.Thinking)
						}
					case "input_json_delta":
						if tb, ok := toolBlocksByIndex[deltaEvt.Index]; ok {
							tb.json.WriteString(deltaEvt.Delta.PartialJSON)
							if cb.OnToolStream != nil {
								raw := tb.json.String()
								preview := raw
								if len(preview) > 600 {
									preview = preview[:240] + "\n...\n" + preview[len(preview)-320:]
								}
								cb.OnToolStream(tb.name, len(raw), preview)
							}
						}
					}
				}

			case "message_delta":
				var msgDeltaEvt struct {
					Delta struct {
						StopReason  string `json:"stop_reason"`
						StopDetails *struct {
							Type        string `json:"type"`
							Category    string `json:"category"`
							Explanation string `json:"explanation"`
						} `json:"stop_details"`
					} `json:"delta"`
					Usage struct {
						OutputTokens int `json:"output_tokens"`
					} `json:"usage"`
				}
				if err := json.Unmarshal([]byte(dataStr), &msgDeltaEvt); err == nil {
					if msgDeltaEvt.Usage.OutputTokens > 0 {
						result.OutputTokens = msgDeltaEvt.Usage.OutputTokens
					}
					if msgDeltaEvt.Delta.StopReason != "" {
						stopReason = msgDeltaEvt.Delta.StopReason
					}
					if msgDeltaEvt.Delta.StopDetails != nil && msgDeltaEvt.Delta.StopDetails.Explanation != "" {
						refusalExplanation = fmt.Sprintf("[%s] %s", msgDeltaEvt.Delta.StopDetails.Category, msgDeltaEvt.Delta.StopDetails.Explanation)
					}
				}

			case "error":
				var errEvt struct {
					Error struct {
						Type    string `json:"type"`
						Message string `json:"message"`
					} `json:"error"`
				}
				if err := json.Unmarshal([]byte(dataStr), &errEvt); err == nil && errEvt.Error.Message != "" {
					streamErrMsg = fmt.Sprintf("%s: %s", errEvt.Error.Type, errEvt.Error.Message)
				}

			case "message_stop":
				// Completed
			}
		}

		if err := scanner.Err(); err != nil && err != io.EOF {
			return nil, "", "", "", fmt.Errorf("error reading anthropic stream: %w", err)
		}

		// Finalize tool calls
		if len(toolBlocksByIndex) > 0 {
			var indices []int
			for idx := range toolBlocksByIndex {
				indices = append(indices, idx)
			}
			sort.Ints(indices)

			for _, idx := range indices {
				tb := toolBlocksByIndex[idx]
				args := make(map[string]any)
				rawJSON := strings.TrimSpace(tb.json.String())
				if rawJSON != "" {
					if err := json.Unmarshal([]byte(rawJSON), &args); err != nil {
						args = map[string]any{"_raw": rawJSON}
					}
				}
				result.ToolCalls = append(result.ToolCalls, ToolCall{
					ID:        tb.id,
					Name:      tb.name,
					Arguments: args,
				})
			}
		}

		content, thinking := parser.Result()
		result.Content = content
		result.Thinking = thinking

		return result, stopReason, refusalExplanation, streamErrMsg, nil
	}

	result, stopReason, refusalExplanation, streamErrMsg, err := readStream(maxTokens, initialMode, effort, opts.SystemPrompt)
	if err != nil {
		return nil, err
	}

	if stopReason == "refusal" || (stopReason == "max_tokens" && strings.TrimSpace(result.Content) == "" && len(result.ToolCalls) == 0) {
		if stopReason == "refusal" && (initialMode == "adaptive_with_effort" || initialMode == "adaptive_only") {
			cleanPrompt := stripThinkInstructions(opts.SystemPrompt)
			result, stopReason, refusalExplanation, streamErrMsg, err = readStream(maxTokens, "plain", "none", cleanPrompt)
			if err != nil {
				return nil, err
			}
		} else if stopReason == "max_tokens" && strings.TrimSpace(result.Content) == "" && len(result.ToolCalls) == 0 && effort == "max" {
			result, stopReason, refusalExplanation, streamErrMsg, err = readStream(maxTokens, initialMode, "high", opts.SystemPrompt)
			if err != nil {
				return nil, err
			}
		}
	}

	if stopReason == "refusal" && strings.TrimSpace(result.Content) == "" && len(result.ToolCalls) == 0 {
		return nil, fmt.Errorf("Anthropic API refusal (%s): %s", opts.Model, refusalExplanation)
	}
	if streamErrMsg != "" && strings.TrimSpace(result.Content) == "" && len(result.ToolCalls) == 0 {
		return nil, fmt.Errorf("Anthropic stream error (%s): %s", opts.Model, streamErrMsg)
	}

	return result, nil
}

func stripThinkInstructions(sysPrompt string) string {
	if !strings.Contains(sysPrompt, "<think>") && !strings.Contains(sysPrompt, "</think>") {
		return sysPrompt
	}
	var cleaned []string
	for _, line := range strings.Split(sysPrompt, "\n") {
		if strings.Contains(line, "<think>") || strings.Contains(line, "</think>") {
			continue
		}
		cleaned = append(cleaned, line)
	}
	return strings.Join(cleaned, "\n")
}
