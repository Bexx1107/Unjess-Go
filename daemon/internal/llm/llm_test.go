package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDetectProvider(t *testing.T) {
	tests := []struct {
		model              string
		configuredProvider string
		expected           string
	}{
		// Known prefixes
		{"ollama/llama3", "", "ollama"},
		{"ollama-api/deepseek-r1:cloud", "", "ollama-api"},
		{"openai/gpt-4o", "", "openai"},
		{"anthropic/claude-3-5-sonnet", "", "anthropic"},
		{"google/gemini-1.5-pro", "", "google"},
		{"groq/llama-3.3-70b-versatile", "", "groq"},
		{"mistral/codestral-latest", "", "mistral"},
		{"xai/grok-beta", "", "xai"},
		{"openrouter/meta-llama/llama-3-8b", "", "openrouter"},
		{"cerebras/llama3.1-70b", "", "cerebras"},
		{"kimi/moonshot-v1-8k", "", "kimi"},
		{"qwen/qwen-plus", "", "qwen"},
		{"lmstudio/local-model", "", "lmstudio"},
		{"llamacpp/local-model", "", "llamacpp"},

		// :cloud suffix
		{"deepseek-v4.1-flash:cloud", "", "ollama"},
		{"qwen2.5-coder:cloud", "openai", "ollama"},

		// gpt-, o1, o3 prefixes
		{"gpt-4o-mini", "", "openai"},
		{"gpt-4.5-preview", "anthropic", "openai"},
		{"o1-mini", "", "openai"},
		{"o1-preview", "", "openai"},
		{"o3-mini", "", "openai"},

		// claude- prefix
		{"claude-3-7-sonnet-20250219", "", "anthropic"},
		{"claude-instant-1.2", "", "anthropic"},

		// gemini- prefix
		{"gemini-2.0-flash", "", "google"},
		{"gemini-1.5-pro", "", "google"},

		// grok- prefix
		{"grok-2-1212", "", "xai"},

		// Colon tag models (Ollama convention)
		{"ornith-1.5:9b", "", "ollama"},
		{"qwen2.5-coder:7b", "", "ollama"},
		{"llama3.2:1b", "", "ollama"},

		// Local GGUF models & Windows absolute paths (must route to llamacpp/lmstudio, NOT ollama or cloud)
		{`E:\models\Qwen3.8-27B-OBLITERATED\Qwen3.8-27B-OBLITERATED-Q4_K_M.gguf`, "", "llamacpp"},
		{`E:\models\Qwen3.8-27B-OBLITERATED\Qwen3.8-27B-OBLITERATED-Q4_K_M.gguf`, "llamacpp", "llamacpp"},
		{`E:\models\Qwen3.8-27B-OBLITERATED\Qwen3.8-27B-OBLITERATED-Q4_K_M.gguf`, "llama.cpp", "llamacpp"},
		{`E:\models\Qwen3.8-27B-OBLITERATED\Qwen3.8-27B-OBLITERATED-Q4_K_M.gguf`, "lmstudio", "lmstudio"},
		{`/mnt/streamvault/models/model.gguf`, "", "llamacpp"},
		{"qwen3.8-27b.gguf", "", "llamacpp"},
		{"qwen3.8-27b", "llamacpp", "llamacpp"},
		{"mistral-7b", "llamacpp", "llamacpp"},

		// Configured provider fallback
		{"my-custom-fine-tune", "groq", "groq"},
		{"my-custom-fine-tune", "", "ollama"},
	}

	for _, tc := range tests {
		actual := DetectProvider(tc.model, tc.configuredProvider)
		if actual != tc.expected {
			t.Errorf("DetectProvider(%q, %q) = %q; want %q", tc.model, tc.configuredProvider, actual, tc.expected)
		}
	}
}

func TestNextPowerOf2(t *testing.T) {
	tests := []struct {
		input    int
		expected int
	}{
		{0, 1},
		{1, 1},
		{2, 2},
		{3, 4},
		{4, 4},
		{1000, 1024},
		{4096, 4096},
		{5000, 8192},
		{16384, 16384},
		{16385, 32768},
	}

	for _, tc := range tests {
		got := nextPowerOf2(tc.input)
		if got != tc.expected {
			t.Errorf("nextPowerOf2(%d) = %d; want %d", tc.input, got, tc.expected)
		}
	}
}

func TestComputeOllamaNumCtx(t *testing.T) {
	// 1. Minimum baseline is 16384
	optsSmall := ChatOptions{
		SystemPrompt: "Be helpful",
		Messages: []Message{
			{Role: "user", Content: "Hello world"},
		},
	}
	ctx1 := computeOllamaNumCtx(optsSmall)
	if ctx1 != 16384 {
		t.Errorf("expected 16384 for small prompt, got %d", ctx1)
	}

	// 2. Override respected
	optsOverride := ChatOptions{
		ContextWindowOverride: 32768,
	}
	ctx2 := computeOllamaNumCtx(optsOverride)
	if ctx2 != 32768 {
		t.Errorf("expected override 32768, got %d", ctx2)
	}

	// 3. Large input scales up
	largeContent := string(make([]byte, 60000)) // 60k chars -> ~20k tokens + 4096 = 24096 -> 32768
	optsLarge := ChatOptions{
		Messages: []Message{
			{Role: "user", Content: largeContent},
		},
	}
	ctx3 := computeOllamaNumCtx(optsLarge)
	if ctx3 != 32768 {
		t.Errorf("expected 32768 for ~20k tokens, got %d", ctx3)
	}

	// 4. Capped at 65536
	hugeContent := string(make([]byte, 300000)) // 300k chars -> ~100k tokens
	optsHuge := ChatOptions{
		Messages: []Message{
			{Role: "user", Content: hugeContent},
		},
	}
	ctx4 := computeOllamaNumCtx(optsHuge)
	if ctx4 != 65536 {
		t.Errorf("expected cap of 65536 for huge prompt, got %d", ctx4)
	}
}

func TestThinkStreamParser(t *testing.T) {
	var streamedText string
	var streamedThinking string

	cb := StreamCallbacks{
		OnText: func(chunk string) {
			streamedText += chunk
		},
		OnThinking: func(chunk string) {
			streamedThinking += chunk
		},
	}

	parser := NewThinkStreamParser(cb)

	// Feed split tags across chunks
	parser.FeedContent("Answer starts: ")
	parser.FeedContent("<thi")
	parser.FeedContent("nk>Deep reasoning here")
	parser.FeedContent("</thi")
	parser.FeedContent("nk> and final conclusion.")

	content, thinking := parser.Result()

	expectedContent := "Answer starts:  and final conclusion."
	expectedThinking := "Deep reasoning here"

	if content != expectedContent {
		t.Errorf("content = %q; want %q", content, expectedContent)
	}
	if thinking != expectedThinking {
		t.Errorf("thinking = %q; want %q", thinking, expectedThinking)
	}
	if streamedText != expectedContent {
		t.Errorf("streamedText = %q; want %q", streamedText, expectedContent)
	}
	if streamedThinking != expectedThinking {
		t.Errorf("streamedThinking = %q; want %q", streamedThinking, expectedThinking)
	}
}

func TestSanitizeSchema(t *testing.T) {
	schema := map[string]any{
		"$schema":              "http://json-schema.org/draft-07/schema#",
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"code": map[string]any{
				"type":        "string",
				"description": "code to execute",
				"$ref":        "#/definitions/code",
			},
		},
	}

	cleaned := sanitizeSchema(schema).(map[string]any)
	if _, exists := cleaned["$schema"]; exists {
		t.Error("expected $schema to be stripped")
	}
	if _, exists := cleaned["additionalProperties"]; exists {
		t.Error("expected additionalProperties to be stripped")
	}
	props := cleaned["properties"].(map[string]any)
	codeProp := props["code"].(map[string]any)
	if _, exists := codeProp["$ref"]; exists {
		t.Error("expected $ref to be stripped in nested property")
	}
	if codeProp["type"] != "string" {
		t.Error("expected valid properties to be preserved")
	}
}

func TestChatStreamOllama(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			http.NotFound(w, r)
			return
		}

		w.Header().Set("Content-Type", "application/x-ndjson")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("expected http.Flusher")
		}

		// NDJSON lines
		lines := []string{
			`{"message": {"role": "assistant", "thinking": "Let me see..."}}`,
			`{"message": {"role": "assistant", "content": "Hello "}}`,
			`{"message": {"role": "assistant", "content": "from Ollama!"}}`,
			`{"message": {"role": "assistant", "tool_calls": [{"id": "call_1", "function": {"name": "test_tool", "arguments": "{\"arg\": 123}"}}]}}`,
			`{"done": true, "prompt_eval_count": 25, "eval_count": 15}`,
		}

		for _, l := range lines {
			fmt.Fprintln(w, l)
			flusher.Flush()
		}
	}))
	defer ts.Close()

	var streamedText string
	var streamedThinking string

	opts := ChatOptions{
		Model:         "llama3.2:latest",
		Provider:      "ollama",
		OllamaBaseURL: ts.URL,
		Messages: []Message{
			{Role: "user", Content: "Hi"},
		},
	}

	res, err := ChatStream(context.Background(), opts, StreamCallbacks{
		OnText: func(chunk string) {
			streamedText += chunk
		},
		OnThinking: func(chunk string) {
			streamedThinking += chunk
		},
	})
	if err != nil {
		t.Fatalf("ChatStream Ollama error: %v", err)
	}

	if res.Content != "Hello from Ollama!" {
		t.Errorf("got content %q, want %q", res.Content, "Hello from Ollama!")
	}
	if res.Thinking != "Let me see..." {
		t.Errorf("got thinking %q, want %q", res.Thinking, "Let me see...")
	}
	if len(res.ToolCalls) != 1 || res.ToolCalls[0].Name != "test_tool" {
		t.Errorf("unexpected tool calls: %+v", res.ToolCalls)
	}
	if res.InputTokens != 25 || res.OutputTokens != 15 {
		t.Errorf("unexpected tokens: in=%d out=%d", res.InputTokens, res.OutputTokens)
	}
}

func TestChatStreamOpenAI(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)

		chunks := []string{
			`data: {"choices":[{"index":0,"delta":{"reasoning":"pondering..."}}]}`,
			`data: {"choices":[{"index":0,"delta":{"content":"OpenAI "}}]}`,
			`data: {"choices":[{"index":0,"delta":{"content":"response"}}]}`,
			`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_abc","function":{"name":"run_cmd","arguments":"{\"cmd\":"}}]}}]}`,
			`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"ls\"}"}}]}}]}`,
			`data: {"usage":{"prompt_tokens":10,"completion_tokens":20}}`,
			`data: [DONE]`,
		}

		for _, c := range chunks {
			fmt.Fprintf(w, "%s\n\n", c)
			flusher.Flush()
		}
	}))
	defer ts.Close()

	opts := ChatOptions{
		Model:    "openai/gpt-4o",
		BaseURLs: map[string]string{"openai": ts.URL},
		Messages: []Message{
			{Role: "user", Content: "Hello"},
		},
	}

	res, err := ChatStream(context.Background(), opts, StreamCallbacks{})
	if err != nil {
		t.Fatalf("ChatStream OpenAI error: %v", err)
	}

	if res.Content != "OpenAI response" {
		t.Errorf("got content %q, want %q", res.Content, "OpenAI response")
	}
	if res.Thinking != "pondering..." {
		t.Errorf("got thinking %q, want %q", res.Thinking, "pondering...")
	}
	if len(res.ToolCalls) != 1 || res.ToolCalls[0].Name != "run_cmd" {
		t.Fatalf("unexpected tool calls: %+v", res.ToolCalls)
	}
	if res.ToolCalls[0].Arguments["cmd"] != "ls" {
		t.Errorf("unexpected tool call arguments: %+v", res.ToolCalls[0].Arguments)
	}
	if res.InputTokens != 10 || res.OutputTokens != 20 {
		t.Errorf("unexpected tokens: in=%d out=%d", res.InputTokens, res.OutputTokens)
	}
}

func TestChatStreamAnthropic(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/messages" {
			http.NotFound(w, r)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)

		chunks := []string{
			"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":50}}}",
			"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\"}}",
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"Claude thinking...\"}}",
			"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}",
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hello from Claude\"}}",
			"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":2,\"content_block\":{\"type\":\"tool_use\",\"id\":\"call_ant\",\"name\":\"read_file\"}}",
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":2,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"path\\\": \\\"go.mod\\\"}\"}}",
			"event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":35}}",
			"event: message_stop\ndata: {\"type\":\"message_stop\"}",
		}

		for _, c := range chunks {
			fmt.Fprintf(w, "%s\n\n", c)
			flusher.Flush()
		}
	}))
	defer ts.Close()

	opts := ChatOptions{
		Model:    "anthropic/claude-3-7-sonnet-20250219",
		BaseURLs: map[string]string{"anthropic": ts.URL},
		Messages: []Message{
			{Role: "user", Content: "Test"},
		},
	}

	res, err := ChatStream(context.Background(), opts, StreamCallbacks{})
	if err != nil {
		t.Fatalf("ChatStream Anthropic error: %v", err)
	}

	if res.Content != "Hello from Claude" {
		t.Errorf("got content %q, want %q", res.Content, "Hello from Claude")
	}
	if res.Thinking != "Claude thinking..." {
		t.Errorf("got thinking %q, want %q", res.Thinking, "Claude thinking...")
	}
	if len(res.ToolCalls) != 1 || res.ToolCalls[0].Name != "read_file" {
		t.Fatalf("unexpected tool calls: %+v", res.ToolCalls)
	}
	if res.ToolCalls[0].Arguments["path"] != "go.mod" {
		t.Errorf("unexpected tool call arguments: %+v", res.ToolCalls[0].Arguments)
	}
	if res.InputTokens != 50 || res.OutputTokens != 35 {
		t.Errorf("unexpected tokens: in=%d out=%d", res.InputTokens, res.OutputTokens)
	}
}

func TestChatStreamGemini(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)

		chunks := []string{
			`data: {"candidates":[{"content":{"parts":[{"thought":true,"text":"Gemini thinking..."}]}}]}`,
			`data: {"candidates":[{"content":{"parts":[{"text":"Hello from Gemini!"}]}}]}`,
			`data: {"candidates":[{"content":{"parts":[{"functionCall":{"name":"list_dir","args":{"dir":"."}}}]}}]}`,
			`data: {"usageMetadata":{"promptTokenCount":30,"candidatesTokenCount":40}}`,
		}

		for _, c := range chunks {
			fmt.Fprintf(w, "%s\n\n", c)
			flusher.Flush()
		}
	}))
	defer ts.Close()

	opts := ChatOptions{
		Model:    "gemini-2.0-flash",
		BaseURLs: map[string]string{"google": ts.URL},
		Messages: []Message{
			{Role: "user", Content: "Test"},
		},
	}

	res, err := ChatStream(context.Background(), opts, StreamCallbacks{})
	if err != nil {
		t.Fatalf("ChatStream Gemini error: %v", err)
	}

	if res.Content != "Hello from Gemini!" {
		t.Errorf("got content %q, want %q", res.Content, "Hello from Gemini!")
	}
	if res.Thinking != "Gemini thinking..." {
		t.Errorf("got thinking %q, want %q", res.Thinking, "Gemini thinking...")
	}
	if len(res.ToolCalls) != 1 || res.ToolCalls[0].Name != "list_dir" {
		t.Fatalf("unexpected tool calls: %+v", res.ToolCalls)
	}
	if res.ToolCalls[0].Arguments["dir"] != "." {
		t.Errorf("unexpected tool call arguments: %+v", res.ToolCalls[0].Arguments)
	}
	if res.InputTokens != 30 || res.OutputTokens != 40 {
		t.Errorf("unexpected tokens: in=%d out=%d", res.InputTokens, res.OutputTokens)
	}
}

func TestPrepareOllamaMessagesThinkingAndTool(t *testing.T) {
	opts := ChatOptions{
		Messages: []Message{
			{Role: "user", Content: "Hello"},
			{Role: "assistant", Content: "I will use tool", Thinking: "Pondering the task..."},
			{Role: "tool", Content: "result of tool", ToolName: "read_file", ToolCallID: "call_123"},
		},
	}

	msgs := prepareOllamaMessages(opts)
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(msgs))
	}

	// Assistant message must have thinking
	if msgs[1]["thinking"] != "Pondering the task..." {
		t.Errorf("expected thinking in assistant message, got %v", msgs[1]["thinking"])
	}

	// Tool message must have tool_name and tool_call_id
	if msgs[2]["tool_name"] != "read_file" {
		t.Errorf("expected tool_name 'read_file', got %v", msgs[2]["tool_name"])
	}
	if msgs[2]["tool_call_id"] != "call_123" {
		t.Errorf("expected tool_call_id 'call_123', got %v", msgs[2]["tool_call_id"])
	}
}

func TestPrepareGeminiContentsAlternationAndMerge(t *testing.T) {
	// Consecutive model messages starting as first item
	messages := []Message{
		{Role: "assistant", Content: "Part A"},
		{Role: "assistant", Content: "Part B"},
		{Role: "user", Content: "User query 1"},
		{Role: "user", Content: "User query 2"},
	}

	contents := prepareGeminiContents(messages)
	// Must start with "user"
	if len(contents) == 0 || contents[0]["role"] != "user" {
		t.Fatalf("expected first item role 'user', got %+v", contents)
	}

	// Verify consecutive same-roles are merged (alternating user <-> model <-> user)
	for i := 1; i < len(contents); i++ {
		if contents[i]["role"] == contents[i-1]["role"] {
			t.Errorf("found consecutive unmerged items with same role %s at index %d", contents[i]["role"], i)
		}
	}
}

func TestPrepareAnthropicMessagesAlternationAndMerge(t *testing.T) {
	messages := []Message{
		{Role: "assistant", Content: "Assistant intro"},
		{Role: "assistant", Content: "Assistant details"},
		{Role: "user", Content: "User msg 1"},
		{Role: "user", Content: "User msg 2"},
	}

	out := prepareAnthropicMessages(messages)
	// Must start with user
	if len(out) == 0 || out[0]["role"] != "user" {
		t.Fatalf("expected first message to be user, got %+v", out)
	}

	// Must alternate
	for i := 1; i < len(out); i++ {
		if out[i]["role"] == out[i-1]["role"] {
			t.Errorf("found consecutive unmerged items with same role %s at index %d", out[i]["role"], i)
		}
	}
}

func TestOllamaCloudContextOptions(t *testing.T) {
	var capturedPayload map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&capturedPayload)
		w.Header().Set("Content-Type", "application/x-ndjson")
		fmt.Fprintln(w, `{"message": {"role": "assistant", "content": "Done"}, "done": true}`)
	}))
	defer ts.Close()

	// 1. Cloud model without override should NOT send num_ctx
	optsCloud := ChatOptions{
		Model:         "deepseek-v4.1-flash:cloud",
		Provider:      "ollama",
		OllamaBaseURL: ts.URL,
		Messages: []Message{
			{Role: "user", Content: "Hello"},
		},
	}
	_, err := ChatStream(context.Background(), optsCloud, StreamCallbacks{})
	if err != nil {
		t.Fatalf("ChatStream failed: %v", err)
	}
	if optsMap, ok := capturedPayload["options"].(map[string]any); ok {
		if _, hasNumCtx := optsMap["num_ctx"]; hasNumCtx {
			t.Errorf("expected no num_ctx for cloud model without override, got %v", optsMap["num_ctx"])
		}
	}

	// 2. Cloud model with override SHOULD send num_ctx = override
	optsCloudOverride := ChatOptions{
		Model:                 "deepseek-v4.1-flash:cloud",
		Provider:              "ollama",
		OllamaBaseURL:         ts.URL,
		ContextWindowOverride: 500000,
		Messages: []Message{
			{Role: "user", Content: "Hello"},
		},
	}
	_, err = ChatStream(context.Background(), optsCloudOverride, StreamCallbacks{})
	if err != nil {
		t.Fatalf("ChatStream failed: %v", err)
	}
	optsMap, ok := capturedPayload["options"].(map[string]any)
	if !ok || optsMap["num_ctx"] != float64(500000) {
		t.Errorf("expected num_ctx 500000 for cloud model with override, got %v", capturedPayload["options"])
	}

	// 3. Local model should send dynamic num_ctx (e.g. 16384)
	optsLocal := ChatOptions{
		Model:         "llama3.2:latest",
		Provider:      "ollama",
		OllamaBaseURL: ts.URL,
		Messages: []Message{
			{Role: "user", Content: "Hello"},
		},
	}
	_, err = ChatStream(context.Background(), optsLocal, StreamCallbacks{})
	if err != nil {
		t.Fatalf("ChatStream failed: %v", err)
	}
	optsMapLocal, ok := capturedPayload["options"].(map[string]any)
	if !ok || optsMapLocal["num_ctx"] == nil {
		t.Errorf("expected num_ctx present for local model, got %v", capturedPayload["options"])
	}
}

func TestOllamaVisionFallback(t *testing.T) {
	attempt := 0
	var finalMessages []map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt++
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)

		if attempt == 1 {
			// First attempt returns 400 image unsupported error
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error": "this model is missing data required for image input"}`))
			return
		}

		// Second attempt succeeds
		if msgs, ok := payload["messages"].([]any); ok {
			for _, m := range msgs {
				if mMap, ok := m.(map[string]any); ok {
					finalMessages = append(finalMessages, mMap)
				}
			}
		}

		w.Header().Set("Content-Type", "application/x-ndjson")
		fmt.Fprintln(w, `{"message": {"role": "assistant", "content": "Recovered without images!"}, "done": true}`)
	}))
	defer ts.Close()

	opts := ChatOptions{
		Model:         "deepseek-v4.1-flash:cloud",
		Provider:      "ollama",
		OllamaBaseURL: ts.URL,
		Messages: []Message{
			{
				Role:    "user",
				Content: "Look at this screenshot",
				Images: []ImageAttachment{
					{Data: "base64image"},
				},
			},
		},
	}

	res, err := ChatStream(context.Background(), opts, StreamCallbacks{})
	if err != nil {
		t.Fatalf("expected successful fallback, got error: %v", err)
	}
	if res.Content != "Recovered without images!" {
		t.Errorf("unexpected content: %s", res.Content)
	}
	if attempt != 2 {
		t.Errorf("expected 2 attempts (1 initial + 1 retry), got %d", attempt)
	}
	if len(finalMessages) > 0 {
		if _, hasImages := finalMessages[0]["images"]; hasImages {
			t.Errorf("expected images key to be stripped on retry, but found it")
		}
		content, _ := finalMessages[0]["content"].(string)
		if !strings.Contains(content, "[Note: Active model does not support raw image input") {
			t.Errorf("expected fallback note appended to message content, got: %s", content)
		}
	}
}
