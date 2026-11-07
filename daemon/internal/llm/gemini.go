package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

func prepareGeminiContents(messages []Message) []map[string]any {
	var contents []map[string]any

	for _, msg := range messages {
		switch msg.Role {
		case "assistant":
			if len(msg.RawParts) > 0 {
				contents = append(contents, map[string]any{
					"role":  "model",
					"parts": msg.RawParts,
				})
				continue
			}

			var parts []map[string]any
			if msg.Thinking != "" {
				thoughtPart := map[string]any{
					"thought": true,
					"text":    msg.Thinking,
				}
				if msg.ThoughtSignature != "" && len(msg.ToolCalls) == 0 {
					thoughtPart["thoughtSignature"] = msg.ThoughtSignature
				}
				parts = append(parts, thoughtPart)
			}
			if msg.Content != "" {
				parts = append(parts, map[string]any{"text": msg.Content})
			}
			for i, tc := range msg.ToolCalls {
				args := tc.Arguments
				if args == nil {
					args = map[string]any{}
				}
				fcPart := map[string]any{
					"functionCall": map[string]any{
						"name": tc.Name,
						"args": args,
					},
				}
				sig := tc.ThoughtSignature
				if sig == "" && i == 0 {
					sig = msg.ThoughtSignature
				}
				if sig == "" && i == 0 {
					// Official Google Gemini API sentinel fallback for missing thought_signature
					sig = "skip_thought_signature_validator"
				}
				if sig != "" {
					fcPart["thoughtSignature"] = sig
				}
				parts = append(parts, fcPart)
			}
			if len(parts) > 0 {
				contents = append(contents, map[string]any{
					"role":  "model",
					"parts": parts,
				})
			}

		case "tool":
			toolName := msg.ToolName
			if toolName == "" {
				toolName = "tool"
			}
			frPart := map[string]any{
				"functionResponse": map[string]any{
					"name": toolName,
					"response": map[string]any{
						"result": msg.Content,
					},
				},
			}
			// Merge consecutive tool responses into a single "user" turn so parallel function calls match 1:1
			if len(contents) > 0 && contents[len(contents)-1]["role"] == "user" {
				if existingParts, ok := contents[len(contents)-1]["parts"].([]map[string]any); ok && len(existingParts) > 0 {
					if _, isFR := existingParts[0]["functionResponse"]; isFR {
						contents[len(contents)-1]["parts"] = append(existingParts, frPart)
						continue
					}
				}
			}
			contents = append(contents, map[string]any{
				"role":  "user",
				"parts": []map[string]any{frPart},
			})

		default: // "user"
			var parts []map[string]any
			for _, img := range msg.Images {
				mime := img.MimeType
				if mime == "" {
					mime = "image/png"
				}
				parts = append(parts, map[string]any{
					"inlineData": map[string]any{
						"mimeType": mime,
						"data":     img.Data,
					},
				})
			}
			if msg.Content != "" {
				parts = append(parts, map[string]any{"text": msg.Content})
			}
			if len(parts) > 0 {
				contents = append(contents, map[string]any{
					"role":  "user",
					"parts": parts,
				})
			}
		}
	}

	// Merge consecutive items with the same role
	var mergedContents []map[string]any
	for _, c := range contents {
		if len(mergedContents) == 0 {
			mergedContents = append(mergedContents, c)
			continue
		}
		last := mergedContents[len(mergedContents)-1]
		if last["role"] == c["role"] {
			var lastParts []map[string]any
			if lp, ok := last["parts"].([]map[string]any); ok {
				lastParts = lp
			} else if lpRaw, ok := last["parts"].([]any); ok {
				for _, p := range lpRaw {
					if pm, ok := p.(map[string]any); ok {
						lastParts = append(lastParts, pm)
					}
				}
			}
			var cParts []map[string]any
			if cp, ok := c["parts"].([]map[string]any); ok {
				cParts = cp
			} else if cpRaw, ok := c["parts"].([]any); ok {
				for _, p := range cpRaw {
					if pm, ok := p.(map[string]any); ok {
						cParts = append(cParts, pm)
					}
				}
			}
			last["parts"] = append(lastParts, cParts...)
		} else {
			mergedContents = append(mergedContents, c)
		}
	}

	// Ensure the first content item is "user"
	if len(mergedContents) > 0 && mergedContents[0]["role"] == "model" {
		mergedContents = append([]map[string]any{{
			"role": "user",
			"parts": []map[string]any{
				{"text": "[Context from prior session]"},
			},
		}}, mergedContents...)
	}

	return mergedContents
}

func prepareGeminiTools(tools []map[string]any) []any {
	var decls []map[string]any
	for _, tool := range tools {
		name, _ := tool["name"].(string)
		desc, _ := tool["description"].(string)
		params := tool["parameters"]
		if params == nil {
			params = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		cleanParams := sanitizeSchema(params)
		decls = append(decls, map[string]any{
			"name":        name,
			"description": desc,
			"parameters":  cleanParams,
		})
	}

	return []any{
		map[string]any{
			"functionDeclarations": decls,
		},
	}
}

func streamGemini(ctx context.Context, opts ChatOptions, cb StreamCallbacks) (*ChatResult, error) {
	baseURL := getBaseURL(opts, "google", "https://generativelanguage.googleapis.com/v1beta")
	apiKey := getAPIKey(opts, "google", "GEMINI_API_KEY", "GOOGLE_API_KEY")

	endpoint := fmt.Sprintf("%s/models/%s:streamGenerateContent?alt=sse", strings.TrimRight(baseURL, "/"), opts.Model)

	payload := map[string]any{
		"contents": prepareGeminiContents(opts.Messages),
	}

	if opts.SystemPrompt != "" {
		payload["systemInstruction"] = map[string]any{
			"parts": []map[string]any{
				{"text": opts.SystemPrompt},
			},
		}
	}

	if len(opts.Tools) > 0 {
		payload["tools"] = prepareGeminiTools(opts.Tools)
	}

	genCfg := map[string]any{}
	if opts.MaxTokens > 0 {
		maxOut := opts.MaxTokens
		if maxOut > 65536 {
			maxOut = 65536
		}
		genCfg["maxOutputTokens"] = maxOut
	}

	lowerModel := strings.ToLower(opts.Model)
	effort := NormalizeEffort(opts.ReasoningEffort)
	supportsThinking := strings.Contains(lowerModel, "gemini-4") ||
		strings.Contains(lowerModel, "gemini-3") ||
		strings.Contains(lowerModel, "gemini-2.5") ||
		strings.Contains(lowerModel, "antigravity") ||
		strings.Contains(lowerModel, "deep-research") ||
		strings.Contains(lowerModel, "thinking") ||
		strings.Contains(lowerModel, "pro") ||
		strings.Contains(lowerModel, "flash")

	supportsThinkingLevel := strings.Contains(lowerModel, "gemini-4") ||
		strings.Contains(lowerModel, "gemini-3") ||
		strings.Contains(lowerModel, "antigravity") ||
		strings.Contains(lowerModel, "deep-research")

	sendGeminiReq := func(mode string) (*http.Response, error) {
		reqPayload := make(map[string]any, len(payload)+1)
		for k, v := range payload {
			reqPayload[k] = v
		}
		cfgCopy := make(map[string]any, len(genCfg)+1)
		for k, v := range genCfg {
			cfgCopy[k] = v
		}
		if mode == "with_level" {
			tc := map[string]any{
				"includeThoughts": effort != "none",
			}
			switch effort {
			case "none":
				tc["thinkingLevel"] = "LOW"
			case "low":
				tc["thinkingLevel"] = "LOW"
			case "medium":
				tc["thinkingLevel"] = "MEDIUM"
			case "high", "max", "auto":
				tc["thinkingLevel"] = "HIGH"
			}
			cfgCopy["thinkingConfig"] = tc
		} else if mode == "thoughts_only" && effort != "none" {
			cfgCopy["thinkingConfig"] = map[string]any{
				"includeThoughts": true,
			}
		}
		if len(cfgCopy) > 0 {
			reqPayload["generationConfig"] = cfgCopy
		}

		bodyBytes, err := json.Marshal(reqPayload)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal gemini payload: %w", err)
		}

		req, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(bodyBytes))
		if err != nil {
			return nil, fmt.Errorf("failed to create gemini request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "Unjess-Daemon/1.0")
		if apiKey != "" {
			req.Header.Set("x-goog-api-key", apiKey)
		}
		return http.DefaultClient.Do(req)
	}

	initialMode := "plain"
	if supportsThinking {
		if supportsThinkingLevel {
			initialMode = "with_level"
		} else if effort != "none" {
			initialMode = "thoughts_only"
		}
	}

	resp, err := sendGeminiReq(initialMode)
	if err != nil {
		return nil, fmt.Errorf("cannot connect to Gemini at %s: %w", endpoint, err)
	}
	if resp.StatusCode == 429 || resp.StatusCode == 500 || resp.StatusCode == 502 || resp.StatusCode == 503 || resp.StatusCode == 504 {
		_ = resp.Body.Close()
		select {
		case <-time.After(1500 * time.Millisecond):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		resp, err = sendGeminiReq(initialMode)
		if err != nil {
			return nil, fmt.Errorf("cannot connect to Gemini at %s: %w", endpoint, err)
		}
	}
	if resp.StatusCode == http.StatusBadRequest && initialMode == "with_level" {
		_ = resp.Body.Close()
		if effort != "none" {
			resp, err = sendGeminiReq("thoughts_only")
			if err != nil {
				return nil, fmt.Errorf("cannot connect to Gemini at %s: %w", endpoint, err)
			}
			if resp.StatusCode == http.StatusBadRequest {
				_ = resp.Body.Close()
				resp, err = sendGeminiReq("plain")
				if err != nil {
					return nil, fmt.Errorf("cannot connect to Gemini at %s: %w", endpoint, err)
				}
			}
		} else {
			resp, err = sendGeminiReq("plain")
			if err != nil {
				return nil, fmt.Errorf("cannot connect to Gemini at %s: %w", endpoint, err)
			}
		}
	} else if resp.StatusCode == http.StatusBadRequest && initialMode == "thoughts_only" {
		_ = resp.Body.Close()
		resp, err = sendGeminiReq("plain")
		if err != nil {
			return nil, fmt.Errorf("cannot connect to Gemini at %s: %w", endpoint, err)
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("gemini API error (%d): %s", resp.StatusCode, string(errBytes))
	}

	parser := NewThinkStreamParser(cb)
	result := &ChatResult{}

	type geminiChunk struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text             string `json:"text"`
					Thought          bool   `json:"thought"`
					ThoughtSignature string `json:"thoughtSignature"`
					ThoughtSigSnake  string `json:"thought_signature"`
					FunctionCall     *struct {
						ID   string         `json:"id,omitempty"`
						Name string         `json:"name"`
						Args map[string]any `json:"args"`
					} `json:"functionCall"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
		UsageMetadata *struct {
			PromptTokenCount     int `json:"promptTokenCount"`
			CandidatesTokenCount int `json:"candidatesTokenCount"`
			ThoughtsTokenCount   int `json:"thoughtsTokenCount"`
		} `json:"usageMetadata"`
	}

	scanner := bufio.NewScanner(resp.Body)
	buf := make([]byte, 1024*1024)
	scanner.Buffer(buf, 10*1024*1024)

	callSeq := 0
	latestSig := ""

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}

		dataStr := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if dataStr == "" || dataStr == "[DONE]" {
			continue
		}

		var chunk geminiChunk
		if err := json.Unmarshal([]byte(dataStr), &chunk); err != nil {
			continue
		}

		if chunk.UsageMetadata != nil {
			result.InputTokens = chunk.UsageMetadata.PromptTokenCount
			result.OutputTokens = chunk.UsageMetadata.CandidatesTokenCount + chunk.UsageMetadata.ThoughtsTokenCount
		}

		for _, cand := range chunk.Candidates {
			for _, part := range cand.Content.Parts {
				sig := part.ThoughtSignature
				if sig == "" {
					sig = part.ThoughtSigSnake
				}
				if sig != "" {
					latestSig = sig
					result.ThoughtSignature = sig
					if len(result.ToolCalls) > 0 && result.ToolCalls[0].ThoughtSignature == "" {
						result.ToolCalls[0].ThoughtSignature = sig
					}
				}

				if part.Thought {
					parser.FeedThinking(part.Text)
				} else if part.Text != "" {
					parser.FeedContent(part.Text)
				}

				if part.FunctionCall != nil {
					callSeq++
					tcID := part.FunctionCall.ID
					if tcID == "" {
						tcID = fmt.Sprintf("call_%s_%d_%d", part.FunctionCall.Name, time.Now().UnixNano()%1000000, callSeq)
					}
					args := part.FunctionCall.Args
					if args == nil {
						args = map[string]any{}
					}
					tcSig := sig
					if tcSig == "" && len(result.ToolCalls) == 0 {
						tcSig = latestSig
					}
					result.ToolCalls = append(result.ToolCalls, ToolCall{
						ID:               tcID,
						Name:             part.FunctionCall.Name,
						Arguments:        args,
						ThoughtSignature: tcSig,
					})
					if cb.OnToolStream != nil {
						rawBytes, _ := json.Marshal(args)
						preview := string(rawBytes)
						if len(preview) > 600 {
							preview = preview[:240] + "\n...\n" + preview[len(preview)-320:]
						}
						cb.OnToolStream(part.FunctionCall.Name, len(rawBytes), preview)
					}
				}
			}
		}
	}

	if err := scanner.Err(); err != nil && err != io.EOF {
		return nil, fmt.Errorf("error reading gemini stream: %w", err)
	}

	content, thinking := parser.Result()
	result.Content = content
	result.Thinking = thinking

	// Ensure if any tool calls were emitted and none have a signature yet, attach latestSig or official sentinel
	if len(result.ToolCalls) > 0 && result.ToolCalls[0].ThoughtSignature == "" {
		if latestSig != "" {
			result.ToolCalls[0].ThoughtSignature = latestSig
		} else {
			result.ToolCalls[0].ThoughtSignature = "skip_thought_signature_validator"
		}
	}

	return result, nil
}
