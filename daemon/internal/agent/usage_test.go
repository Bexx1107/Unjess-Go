package agent

import (
	"path/filepath"
	"testing"
	"time"

	"unjess-daemon/internal/config"
)

func TestEstimateModelCostUSD(t *testing.T) {
	// 1. Local Ollama should be 0.0
	cost, inRate, outRate := EstimateModelCostUSD("qwen2.5-coder:7b", "ollama", 1000000, 1000000)
	if cost != 0.0 || inRate != 0.0 || outRate != 0.0 {
		t.Fatalf("expected 0.0 for local ollama, got cost=%v, in=%v, out=%v", cost, inRate, outRate)
	}

	// 2. Ollama Cloud
	costCloud, inCloud, outCloud := EstimateModelCostUSD("qwen2.5-coder:cloud", "ollama", 1000000, 1000000)
	if inCloud != 0.40 || outCloud != 1.20 || costCloud != 1.60 {
		t.Fatalf("expected cloud rates (0.40, 1.20), got cost=%v in=%v out=%v", costCloud, inCloud, outCloud)
	}

	// 3. Claude Opus
	_, inOpus, outOpus := EstimateModelCostUSD("claude-opus-4", "anthropic", 1000, 1000)
	if inOpus != 15.00 || outOpus != 75.00 {
		t.Fatalf("expected opus rates (15, 75), got in=%v out=%v", inOpus, outOpus)
	}

	// 4. Claude Sonnet
	_, inSonnet, outSonnet := EstimateModelCostUSD("claude-sonnet-3-5", "anthropic", 1000, 1000)
	if inSonnet != 3.00 || outSonnet != 15.00 {
		t.Fatalf("expected sonnet rates (3, 15), got in=%v out=%v", inSonnet, outSonnet)
	}

	// 5. Claude Haiku
	_, inHaiku, outHaiku := EstimateModelCostUSD("claude-haiku-3-5", "anthropic", 1000, 1000)
	if inHaiku != 0.80 || outHaiku != 4.00 {
		t.Fatalf("expected haiku rates (0.80, 4.00), got in=%v out=%v", inHaiku, outHaiku)
	}

	// 6. Gemini Pro / Argon
	_, inGem, outGem := EstimateModelCostUSD("gemini-4-argon", "gemini", 1000, 1000)
	if inGem != 1.25 || outGem != 10.00 {
		t.Fatalf("expected gemini pro rates (1.25, 10.00), got in=%v out=%v", inGem, outGem)
	}

	// 7. Gemini Flash
	_, inFlash, outFlash := EstimateModelCostUSD("gemini-3.8-flash", "gemini", 1000, 1000)
	if inFlash != 0.15 || outFlash != 0.60 {
		t.Fatalf("expected flash rates (0.15, 0.60), got in=%v out=%v", inFlash, outFlash)
	}

	// 8. GPT-5 / o3
	_, inGPT5, outGPT5 := EstimateModelCostUSD("gpt-5-preview", "openai", 1000, 1000)
	if inGPT5 != 2.50 || outGPT5 != 10.00 {
		t.Fatalf("expected gpt-5 rates (2.50, 10.00), got in=%v out=%v", inGPT5, outGPT5)
	}

	// 9. Qwen Max
	_, inQMax, outQMax := EstimateModelCostUSD("qwen3.8-max", "qwen", 1000, 1000)
	if inQMax != 1.60 || outQMax != 6.40 {
		t.Fatalf("expected qwen max rates (1.60, 6.40), got in=%v out=%v", inQMax, outQMax)
	}

	// 10. Qwen Flash
	_, inQFlash, outQFlash := EstimateModelCostUSD("qwen3.8-flash", "qwen", 1000, 1000)
	if inQFlash != 0.15 || outQFlash != 0.60 {
		t.Fatalf("expected qwen flash rates (0.15, 0.60), got in=%v out=%v", inQFlash, outQFlash)
	}

	// 11. Kimi
	_, inKimi, outKimi := EstimateModelCostUSD("kimi-k3", "kimi", 1000, 1000)
	if inKimi != 1.00 || outKimi != 3.00 {
		t.Fatalf("expected kimi rates (1.00, 3.00), got in=%v out=%v", inKimi, outKimi)
	}
}

func TestGetModelContextLimit(t *testing.T) {
	if GetModelContextLimit("gemini-2.5-pro", 0) != 1000000 {
		t.Errorf("expected 1000000 for gemini")
	}
	if GetModelContextLimit("deepseek-v4.1-flash:cloud", 0) != 1000000 {
		t.Errorf("expected 1000000 for deepseek-v4.1-flash:cloud")
	}
	if GetModelContextLimit("dsflash4.1", 0) != 1000000 {
		t.Errorf("expected 1000000 for dsflash4.1")
	}
	if GetModelContextLimit("qwen3.8-flash", 0) != 1000000 {
		t.Errorf("expected 1000000 for qwen3.8-flash")
	}
	if GetModelContextLimit("deepseek-v4", 0) != 262144 {
		t.Errorf("expected 262144 for deepseek-v4")
	}
	if GetModelContextLimit("deepseek-r1", 0) != 262144 {
		t.Errorf("expected 262144 for deepseek-r1")
	}
	if GetModelContextLimit("claude-sonnet-3-5", 0) != 200000 {
		t.Errorf("expected 200000 for claude")
	}
	if GetModelContextLimit("gpt-5", 0) != 200000 {
		t.Errorf("expected 200000 for gpt-5")
	}
	if GetModelContextLimit("kimi-k3", 0) != 262144 {
		t.Errorf("expected 262144 for kimi")
	}
	if GetModelContextLimit("qwen2.5-coder:cloud", 0) != 262144 {
		t.Errorf("expected 262144 for cloud")
	}
	if GetModelContextLimit("qwen2.5-coder", 0) != 131072 {
		t.Errorf("expected 131072 for local qwen")
	}
	if GetModelContextLimit("custom-model", 50000) != 50000 {
		t.Errorf("expected override 50000 to take precedence")
	}
}

func TestUsageLedger_RecordAndSummary(t *testing.T) {
	tempDir := t.TempDir()
	tempFile := filepath.Join(tempDir, "telemetry", "usage_ledger.json")
	t.Setenv("UNJESS_USAGE_LEDGER_PATH", tempFile)
	t.Setenv("UNJESS_STATE_PATH", filepath.Join(tempDir, "harness_state.json"))

	settings := &config.Settings{
		Model:    "claude-sonnet-3-5",
		Provider: "anthropic",
	}
	hm := NewHarnessManager(settings)

	now := time.Now().Unix()
	hm.RecordUsage(UsageRecord{
		ID:           "use_test_1",
		Timestamp:    now,
		ConvID:       "conv_test_1",
		Workspace:    t.TempDir(),
		Model:        "claude-sonnet-3-5",
		Provider:     "anthropic",
		InputTokens:  1000,
		OutputTokens: 200,
		TotalTokens:  1200,
		CostUSD:      0.006,
	})

	summary := hm.GetUsageSummary("conv_test_1")
	session, ok := summary["session"].(map[string]any)
	if !ok {
		t.Fatalf("expected session bucket in summary")
	}
	if session["turns_count"].(int) != 1 {
		t.Errorf("expected 1 turn in session, got %v", session["turns_count"])
	}
	if session["total_tokens"].(int) != 1200 {
		t.Errorf("expected 1200 tokens, got %v", session["total_tokens"])
	}

	report, ok := summary["markdown_report"].(string)
	if !ok || len(report) == 0 {
		t.Fatalf("expected non-empty markdown report")
	}

	// Verify ZERO EMOJIS in markdown report
	for _, c := range report {
		if c > 127 && c != '-' && c != '|' && c != '\n' && c != '\r' {
			t.Errorf("found non-ascii character in usage markdown report: %q", c)
		}
	}
}

func TestContextUsage_Analysis(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("UNJESS_STATE_PATH", filepath.Join(tempDir, "harness_state.json"))
	t.Setenv("UNJESS_USAGE_LEDGER_PATH", filepath.Join(tempDir, "usage_ledger.json"))
	settings := &config.Settings{
		Model:    "claude-sonnet-3-5",
		Provider: "anthropic",
	}
	hm := NewHarnessManager(settings)

	convRes := hm.CreateConversation(t.TempDir(), "claude-sonnet-3-5", "Test Session")
	convID := convRes["conversation"].(map[string]any)["id"].(string)

	ctxUsage := hm.GetContextUsage(convID)
	if ctxUsage["context_limit"].(int) != 200000 {
		t.Errorf("expected context limit 200000, got %v", ctxUsage["context_limit"])
	}

	totTokens, ok := ctxUsage["total_context_tokens"].(int)
	if !ok || totTokens <= 0 {
		t.Errorf("expected positive total context tokens, got %v", totTokens)
	}

	report, ok := ctxUsage["markdown_report"].(string)
	if !ok || len(report) == 0 {
		t.Fatalf("expected non-empty context markdown report")
	}

	// Verify ZERO EMOJIS in context markdown report
	for _, c := range report {
		if c > 127 && c != '-' && c != '|' && c != '\n' && c != '\r' && c != '%' {
			t.Errorf("found non-ascii character in context markdown report: %q", c)
		}
	}
}
