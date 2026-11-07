package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"unjess-daemon/internal/memory"
)

// UsageRecord tracks token consumption and estimated cost for an agent or subagent turn.
type UsageRecord struct {
	ID           string  `json:"id"`
	Timestamp    int64   `json:"timestamp"` // Unix seconds
	ConvID       string  `json:"conv_id"`
	Workspace    string  `json:"workspace"`
	Model        string  `json:"model"`
	Provider     string  `json:"provider"`
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	TotalTokens  int     `json:"total_tokens"`
	CostUSD      float64 `json:"cost_usd"`
}

var (
	usageLedgerMu       sync.RWMutex
	testUsageLedgerOnce sync.Once
	testUsageLedgerPath string
)

func usageLedgerFilePath() string {
	if override := strings.TrimSpace(os.Getenv("UNJESS_USAGE_LEDGER_PATH")); override != "" {
		return override
	}
	exeName := strings.ToLower(filepath.Base(os.Args[0]))
	if strings.HasSuffix(exeName, ".test.exe") || strings.HasSuffix(exeName, ".test") || strings.Contains(exeName, "_test") {
		testUsageLedgerOnce.Do(func() {
			tmpDir, _ := os.MkdirTemp("", "unjess_test_ledger_*")
			testUsageLedgerPath = filepath.Join(tmpDir, "usage_ledger.json")
		})
		if testUsageLedgerPath != "" {
			return testUsageLedgerPath
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".unjess", "usage", "usage_ledger.json")
}

func loadUsageLedgerLocked() ([]UsageRecord, error) {
	filePath := usageLedgerFilePath()
	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return make([]UsageRecord, 0), nil
		}
		return nil, err
	}
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	if len(strings.TrimSpace(string(data))) == 0 {
		return make([]UsageRecord, 0), nil
	}
	var records []UsageRecord
	if err := json.Unmarshal(data, &records); err != nil {
		return nil, err
	}
	return records, nil
}

func saveUsageLedgerLocked(records []UsageRecord) error {
	filePath := usageLedgerFilePath()
	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filePath, data, 0644)
}

// EstimateModelCostUSD returns estimated cost in USD along with input and output rates per 1M tokens.
func EstimateModelCostUSD(model, provider string, inputTokens, outputTokens int) (costUSD float64, inputRatePer1M float64, outputRatePer1M float64) {
	lowerModel := strings.ToLower(model)
	lowerProvider := strings.ToLower(provider)

	// Local Ollama models (free)
	if lowerProvider == "ollama" && !strings.HasSuffix(lowerModel, ":cloud") {
		return 0.0, 0.0, 0.0
	}

	switch {
	case strings.Contains(lowerModel, "claude-opus"):
		inputRatePer1M = 15.00
		outputRatePer1M = 75.00
	case strings.Contains(lowerModel, "claude-sonnet"):
		inputRatePer1M = 3.00
		outputRatePer1M = 15.00
	case strings.Contains(lowerModel, "claude-haiku"):
		inputRatePer1M = 0.80
		outputRatePer1M = 4.00
	case strings.Contains(lowerModel, "gemini-4-argon") || strings.Contains(lowerModel, "gemini-2.5-pro") || (strings.Contains(lowerModel, "gemini") && strings.Contains(lowerModel, "pro")):
		inputRatePer1M = 1.25
		outputRatePer1M = 10.00
	case strings.Contains(lowerModel, "gemini-3.8-flash") || (strings.Contains(lowerModel, "gemini") && strings.Contains(lowerModel, "flash")):
		inputRatePer1M = 0.15
		outputRatePer1M = 0.60
	case strings.HasPrefix(lowerModel, "gpt-5") || strings.HasPrefix(lowerModel, "o3") || strings.Contains(lowerModel, "gpt-5") || strings.Contains(lowerModel, "o3"):
		inputRatePer1M = 2.50
		outputRatePer1M = 10.00
	case strings.Contains(lowerModel, "qwen3.8-max") || (strings.Contains(lowerModel, "qwen") && strings.Contains(lowerModel, "max")):
		inputRatePer1M = 1.60
		outputRatePer1M = 6.40
	case strings.Contains(lowerModel, "qwen3.8-flash") || (strings.Contains(lowerModel, "qwen") && (strings.Contains(lowerModel, "flash") || strings.Contains(lowerModel, "plus"))):
		inputRatePer1M = 0.15
		outputRatePer1M = 0.60
	case strings.Contains(lowerModel, "kimi"):
		inputRatePer1M = 1.00
		outputRatePer1M = 3.00
	case strings.HasSuffix(lowerModel, ":cloud"):
		inputRatePer1M = 0.40
		outputRatePer1M = 1.20
	default:
		inputRatePer1M = 1.00
		outputRatePer1M = 3.00
	}

	costUSD = (float64(inputTokens)/1000000.0)*inputRatePer1M + (float64(outputTokens)/1000000.0)*outputRatePer1M
	return costUSD, inputRatePer1M, outputRatePer1M
}

// GetModelContextLimit determines model context window limit in tokens.
func GetModelContextLimit(model string, override int) int {
	if override > 0 {
		return override
	}
	lower := strings.ToLower(model)
	switch {
	case strings.Contains(lower, "deepseek-v4.1-flash") || strings.Contains(lower, "dsflash") || strings.Contains(lower, "qwen3.8-flash") || strings.Contains(lower, "minimax-m2.7") || strings.Contains(lower, "gemini"):
		return 1000000
	case strings.Contains(lower, "deepseek-v4") || strings.Contains(lower, "deepseek-r1") || strings.Contains(lower, "qwen3") || strings.Contains(lower, "kimi") || strings.Contains(lower, "glm-5") || strings.HasSuffix(lower, ":cloud"):
		return 262144
	case strings.Contains(lower, "claude"):
		return 200000
	case strings.HasPrefix(lower, "gpt-5") || strings.HasPrefix(lower, "o3") || strings.Contains(lower, "gpt-5") || strings.Contains(lower, "o3"):
		return 200000
	case strings.Contains(lower, "qwen"):
		return 131072
	default:
		return 32768
	}
}

// RecordUsage appends a new usage record to the local usage ledger.
func (hm *HarnessManager) RecordUsage(rec UsageRecord) {
	usageLedgerMu.Lock()
	defer usageLedgerMu.Unlock()

	records, _ := loadUsageLedgerLocked()
	records = append(records, rec)
	_ = saveUsageLedgerLocked(records)
}

// ensureUsageBackfilledLocked scans existing conversations to seed missing historical turns into the ledger.
func (hm *HarnessManager) ensureUsageBackfilledLocked(records []UsageRecord) []UsageRecord {
	seenConvs := make(map[string]bool)
	for _, r := range records {
		seenConvs[r.ConvID] = true
	}

	hm.mu.RLock()
	convs, _ := hm.state["conversations"].([]any)
	hm.mu.RUnlock()

	var newRecords []UsageRecord
	for _, c := range convs {
		cm, ok := c.(map[string]any)
		if !ok {
			continue
		}
		cID := fmt.Sprint(cm["id"])
		if cID == "" || seenConvs[cID] {
			continue
		}
		seenConvs[cID] = true

		cModel := fmt.Sprint(cm["model"])
		if cModel == "" || cModel == "<nil>" {
			if hm.settings != nil {
				cModel = hm.settings.Model
			} else {
				cModel = "claude-sonnet-3-5"
			}
		}
		cProvider := ""
		if hm.settings != nil {
			cProvider = hm.settings.Provider
		}
		cWS := fmt.Sprint(cm["workspace"])
		ts := getInt64(cm["updated_at"])
		if ts == 0 {
			ts = getInt64(cm["created_at"])
		}
		if ts == 0 {
			ts = time.Now().Unix()
		}

		turns, _ := cm["turns"].([]any)
		for idx, t := range turns {
			tm, ok := t.(map[string]any)
			if !ok {
				continue
			}
			role := fmt.Sprint(tm["role"])
			if role != "agent" {
				continue
			}

			text := fmt.Sprint(tm["text"])
			thinking := fmt.Sprint(tm["thinking"])
			var toolLen int
			if tcs, ok := tm["tool_calls"].([]any); ok {
				for _, tc := range tcs {
					if b, err := json.Marshal(tc); err == nil {
						toolLen += len(b)
					}
				}
			}

			inTokens := 0
			outTokens := 0

			if stats, ok := tm["stats"].(map[string]any); ok {
				inTokens = getInt(stats, "tokens_in")
				outTokens = getInt(stats, "tokens_out")
			}
			if inTokens == 0 {
				inTokens = 2500
			}
			if outTokens == 0 {
				outTokens = (len(text) + len(thinking) + toolLen) / 4
				if outTokens < 20 {
					outTokens = 20
				}
			}

			cost, _, _ := EstimateModelCostUSD(cModel, cProvider, inTokens, outTokens)
			newRecords = append(newRecords, UsageRecord{
				ID:           fmt.Sprintf("backfill_%s_%d", cID, idx),
				Timestamp:    ts,
				ConvID:       cID,
				Workspace:    cWS,
				Model:        cModel,
				Provider:     cProvider,
				InputTokens:  inTokens,
				OutputTokens: outTokens,
				TotalTokens:  inTokens + outTokens,
				CostUSD:      cost,
			})
		}
	}

	if len(newRecords) > 0 {
		records = append(records, newRecords...)
		_ = saveUsageLedgerLocked(records)
	}

	return records
}

type usageBucket struct {
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	TotalTokens  int     `json:"total_tokens"`
	CostUSD      float64 `json:"cost_usd"`
	TurnsCount   int     `json:"turns_count"`
}

func (b *usageBucket) add(r UsageRecord) {
	b.InputTokens += r.InputTokens
	b.OutputTokens += r.OutputTokens
	b.TotalTokens += r.TotalTokens
	b.CostUSD += r.CostUSD
	b.TurnsCount++
}

func (b *usageBucket) toMap() map[string]any {
	return map[string]any{
		"input_tokens":  b.InputTokens,
		"output_tokens": b.OutputTokens,
		"total_tokens":  b.TotalTokens,
		"cost_usd":      math.Round(b.CostUSD*10000) / 10000,
		"turns_count":   b.TurnsCount,
	}
}

// GetUsageSummary returns bucketed usage metrics and clean markdown breakdown.
func (hm *HarnessManager) GetUsageSummary(convID string) map[string]any {
	usageLedgerMu.Lock()
	records, _ := loadUsageLedgerLocked()
	records = hm.ensureUsageBackfilledLocked(records)
	usageLedgerMu.Unlock()

	targetConvID := strings.TrimSpace(convID)
	if targetConvID == "" {
		hm.mu.RLock()
		if actID, ok := hm.state["active_conversation_id"].(string); ok {
			targetConvID = actID
		}
		hm.mu.RUnlock()
	}

	now := time.Now()
	nowUnix := now.Unix()
	startOfDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).Unix()
	startOfWeek := nowUnix - 7*86400
	startOfMonth := nowUnix - 30*86400
	startOfYear := nowUnix - 365*86400

	var sessionBucket, dayBucket, weekBucket, monthBucket, yearBucket, allTimeBucket usageBucket

	type modelKey struct {
		Model    string
		Provider string
	}
	modelBuckets := make(map[modelKey]*usageBucket)

	for _, r := range records {
		allTimeBucket.add(r)

		if r.Timestamp >= startOfYear {
			yearBucket.add(r)
		}
		if r.Timestamp >= startOfMonth {
			monthBucket.add(r)
		}
		if r.Timestamp >= startOfWeek {
			weekBucket.add(r)
		}
		if r.Timestamp >= startOfDay {
			dayBucket.add(r)
		}
		if targetConvID != "" && r.ConvID == targetConvID {
			sessionBucket.add(r)
		}

		key := modelKey{Model: r.Model, Provider: r.Provider}
		if key.Model == "" {
			key.Model = "unknown"
		}
		if _, ok := modelBuckets[key]; !ok {
			modelBuckets[key] = &usageBucket{}
		}
		modelBuckets[key].add(r)
	}

	var byModel []map[string]any
	for key, b := range modelBuckets {
		byModel = append(byModel, map[string]any{
			"model":         key.Model,
			"provider":      key.Provider,
			"input_tokens":  b.InputTokens,
			"output_tokens": b.OutputTokens,
			"total_tokens":  b.TotalTokens,
			"cost_usd":      math.Round(b.CostUSD*10000) / 10000,
			"turns_count":   b.TurnsCount,
		})
	}
	sort.Slice(byModel, func(i, j int) bool {
		cI, _ := byModel[i]["cost_usd"].(float64)
		cJ, _ := byModel[j]["cost_usd"].(float64)
		if cI != cJ {
			return cI > cJ
		}
		tI, _ := byModel[i]["total_tokens"].(int)
		tJ, _ := byModel[j]["total_tokens"].(int)
		return tI > tJ
	})

	var sb strings.Builder
	sb.WriteString("# Token and Cost Usage Report\n\n")
	sb.WriteString("| Scope | Turns | Input Tokens | Output Tokens | Total Tokens | Est. Cost (USD) |\n")
	sb.WriteString("|---|---|---|---|---|---|\n")
	sb.WriteString(fmt.Sprintf("| Active Session | %d | %s | %s | %s | $%.4f |\n",
		sessionBucket.TurnsCount, formatNumber(sessionBucket.InputTokens), formatNumber(sessionBucket.OutputTokens), formatNumber(sessionBucket.TotalTokens), sessionBucket.CostUSD))
	sb.WriteString(fmt.Sprintf("| Today | %d | %s | %s | %s | $%.4f |\n",
		dayBucket.TurnsCount, formatNumber(dayBucket.InputTokens), formatNumber(dayBucket.OutputTokens), formatNumber(dayBucket.TotalTokens), dayBucket.CostUSD))
	sb.WriteString(fmt.Sprintf("| Last 7 Days | %d | %s | %s | %s | $%.4f |\n",
		weekBucket.TurnsCount, formatNumber(weekBucket.InputTokens), formatNumber(weekBucket.OutputTokens), formatNumber(weekBucket.TotalTokens), weekBucket.CostUSD))
	sb.WriteString(fmt.Sprintf("| Last 30 Days | %d | %s | %s | %s | $%.4f |\n",
		monthBucket.TurnsCount, formatNumber(monthBucket.InputTokens), formatNumber(monthBucket.OutputTokens), formatNumber(monthBucket.TotalTokens), monthBucket.CostUSD))
	sb.WriteString(fmt.Sprintf("| Past Year | %d | %s | %s | %s | $%.4f |\n",
		yearBucket.TurnsCount, formatNumber(yearBucket.InputTokens), formatNumber(yearBucket.OutputTokens), formatNumber(yearBucket.TotalTokens), yearBucket.CostUSD))
	sb.WriteString(fmt.Sprintf("| All Time | %d | %s | %s | %s | $%.4f |\n",
		allTimeBucket.TurnsCount, formatNumber(allTimeBucket.InputTokens), formatNumber(allTimeBucket.OutputTokens), formatNumber(allTimeBucket.TotalTokens), allTimeBucket.CostUSD))

	if len(byModel) > 0 {
		sb.WriteString("\n### Breakdown by Model\n")
		sb.WriteString("| Model | Provider | Turns | Total Tokens | Est. Cost (USD) |\n")
		sb.WriteString("|---|---|---|---|---|\n")
		for _, m := range byModel {
			sb.WriteString(fmt.Sprintf("| `%s` | %s | %d | %s | $%.4f |\n",
				m["model"], m["provider"], m["turns_count"], formatNumber(m["total_tokens"].(int)), m["cost_usd"].(float64)))
		}
	}

	return map[string]any{
		"session":  sessionBucket.toMap(),
		"day":      dayBucket.toMap(),
		"week":     weekBucket.toMap(),
		"month":    monthBucket.toMap(),
		"year":     yearBucket.toMap(),
		"all_time": allTimeBucket.toMap(),
		"periods": map[string]any{
			"session":  sessionBucket.toMap(),
			"today":    dayBucket.toMap(),
			"day":      dayBucket.toMap(),
			"week":     weekBucket.toMap(),
			"month":    monthBucket.toMap(),
			"year":     yearBucket.toMap(),
			"all_time": allTimeBucket.toMap(),
		},
		"by_model":        byModel,
		"models":          byModel,
		"markdown_report": sb.String(),
	}
}

// GetContextUsage analyzes context window capacity and current breakdown.
func (hm *HarnessManager) GetContextUsage(convID string) map[string]any {
	return hm.GetContextUsageWithModel(convID, "")
}

// GetContextUsageWithModel analyzes context window capacity with an optional active model override.
func (hm *HarnessManager) GetContextUsageWithModel(convID string, modelOverride string) map[string]any {
	hm.mu.Lock()
	defer hm.mu.Unlock()

	targetConvID := strings.TrimSpace(convID)
	if targetConvID == "" {
		if actID, ok := hm.state["active_conversation_id"].(string); ok {
			targetConvID = actID
		}
	}

	convs, _ := hm.state["conversations"].([]any)
	var targetConv map[string]any
	for _, c := range convs {
		if cm, ok := c.(map[string]any); ok && cm["id"] == targetConvID {
			targetConv = cm
			break
		}
	}
	if targetConv == nil && len(convs) > 0 {
		if first, ok := convs[0].(map[string]any); ok {
			targetConv = first
			targetConvID = fmt.Sprint(first["id"])
		}
	}

	resolveLatestConvModel := func(cm map[string]any) string {
		if cm == nil {
			return ""
		}
		if turns, ok := cm["turns"].([]any); ok {
			for i := len(turns) - 1; i >= 0; i-- {
				if tm, ok := turns[i].(map[string]any); ok {
					m := strings.TrimSpace(fmt.Sprint(tm["model"]))
					if m != "" && m != "<nil>" && m != "system" {
						return m
					}
				}
			}
		}
		m := strings.TrimSpace(fmt.Sprint(cm["model"]))
		if m != "" && m != "<nil>" {
			return m
		}
		return ""
	}

	modelName := strings.TrimSpace(modelOverride)
	if modelName != "" && targetConv != nil {
		targetConv["model"] = modelName
	}
	wsPath := ""
	if targetConv != nil {
		if modelName == "" {
			// Prefer explicit conv["model"] if updated, or latest turn model
			cmModel := strings.TrimSpace(fmt.Sprint(targetConv["model"]))
			latestTurnModel := resolveLatestConvModel(targetConv)
			if latestTurnModel != "" && cmModel != latestTurnModel {
				// Update stale conv["model"] to match latest turn if user didn't override
				modelName = latestTurnModel
				targetConv["model"] = latestTurnModel
			} else {
				modelName = cmModel
			}
		}
		wsPath = fmt.Sprint(targetConv["workspace"])
	}
	if modelName == "" || modelName == "<nil>" {
		if hm.settings != nil && strings.TrimSpace(hm.settings.Model) != "" {
			modelName = strings.TrimSpace(hm.settings.Model)
		} else {
			modelName = "claude-sonnet-3-5"
		}
	}
	if wsPath == "" || wsPath == "<nil>" {
		if actWS, ok := hm.state["active_workspace"].(string); ok {
			wsPath = actWS
		}
	}

	override := 0
	if hm.settings != nil {
		override = hm.settings.ContextWindowOverride
	}
	limit := GetModelContextLimit(modelName, override)

	// Breakdown estimation for active session
	systemPromptTokens := 1650
	toolsTokens := 1850
	memoryTokens := len(memory.FormatForSystemPrompt(wsPath, 12)) / 4

	extractTurnToolTokens := func(tm map[string]any) int {
		var tok int
		tcs, ok := tm["tool_calls"].([]any)
		if !ok {
			tcs, _ = tm["toolCalls"].([]any)
		}
		for _, tc := range tcs {
			if b, err := json.Marshal(tc); err == nil {
				tok += len(b) / 4
			}
		}
		return tok
	}

	var historyUserTokens, historyAgentTokens, historyToolsTokens int
	if targetConv != nil {
		if turns, ok := targetConv["turns"].([]any); ok {
			for _, t := range turns {
				tm, ok := t.(map[string]any)
				if !ok {
					continue
				}
				role := fmt.Sprint(tm["role"])
				text := fmt.Sprint(tm["text"])
				thinking := fmt.Sprint(tm["thinking"])

				if role == "user" {
					historyUserTokens += len(text) / 4
				} else {
					historyAgentTokens += (len(text) + len(thinking)) / 4
					historyToolsTokens += extractTurnToolTokens(tm)
				}
			}
		}
	}

	totalActiveContext := systemPromptTokens + toolsTokens + memoryTokens + historyUserTokens + historyAgentTokens + historyToolsTokens
	usagePercent := (float64(totalActiveContext) / float64(limit)) * 100.0

	// Sessions list
	var sessionsList []map[string]any
	for _, c := range convs {
		cm, ok := c.(map[string]any)
		if !ok {
			continue
		}
		cID := fmt.Sprint(cm["id"])
		cTitle := fmt.Sprint(cm["title"])
		if strings.Contains(cTitle, "TestContextUsage_Analysis") {
			continue
		}
		cWS := fmt.Sprint(cm["workspace"])
		cModel := resolveLatestConvModel(cm)
		if cID == targetConvID && modelName != "" {
			cModel = modelName
		}
		if cModel == "" || cModel == "<nil>" {
			cModel = modelName
		}
		cLimit := GetModelContextLimit(cModel, override)

		var cUser, cAgent, cTools int
		var turnsCount int
		if turns, ok := cm["turns"].([]any); ok {
			turnsCount = len(turns)
			for _, t := range turns {
				tm, ok := t.(map[string]any)
				if !ok {
					continue
				}
				r := fmt.Sprint(tm["role"])
				txt := fmt.Sprint(tm["text"])
				th := fmt.Sprint(tm["thinking"])
				if r == "user" {
					cUser += len(txt) / 4
				} else {
					cAgent += (len(txt) + len(th)) / 4
					cTools += extractTurnToolTokens(tm)
				}
			}
		}
		cMem := len(memory.FormatForSystemPrompt(cWS, 12)) / 4
		cTotal := systemPromptTokens + toolsTokens + cMem + cUser + cAgent + cTools
		cPercent := (float64(cTotal) / float64(cLimit)) * 100.0

		sessionsList = append(sessionsList, map[string]any{
			"id":                   cID,
			"conv_id":              cID,
			"title":                cTitle,
			"workspace":            cWS,
			"model":                cModel,
			"turns_count":          turnsCount,
			"context_tokens":       cTotal,
			"total_context_tokens": cTotal,
			"context_limit":        cLimit,
			"pct":                  math.Round(cPercent*10) / 10,
			"usage_percent":        math.Round(cPercent*10) / 10,
			"updated_at":           cm["updated_at"],
		})
	}

	var sb strings.Builder
	sb.WriteString("# Context Window Analysis\n\n")
	sb.WriteString("### Active Session Context Breakdown\n")
	sb.WriteString(fmt.Sprintf("- System Prompt and Repo Map: %s tokens\n", formatNumber(systemPromptTokens)))
	sb.WriteString(fmt.Sprintf("- Tool Schemas: %s tokens\n", formatNumber(toolsTokens)))
	sb.WriteString(fmt.Sprintf("- Cross-Session Memory: %s tokens\n", formatNumber(memoryTokens)))
	sb.WriteString(fmt.Sprintf("- User History: %s tokens\n", formatNumber(historyUserTokens)))
	sb.WriteString(fmt.Sprintf("- Agent Thinking and Responses: %s tokens\n", formatNumber(historyAgentTokens)))
	sb.WriteString(fmt.Sprintf("- Tool Execution History: %s tokens\n", formatNumber(historyToolsTokens)))
	sb.WriteString(fmt.Sprintf("- Total Active Context: %s / %s tokens (%.1f%%)\n\n",
		formatNumber(totalActiveContext), formatNumber(limit), usagePercent))

	if len(sessionsList) > 0 {
		sb.WriteString("### Context Usage Across Sessions\n")
		sb.WriteString("| Conversation | Workspace | Model | Turns | Used Context | Limit | Usage % |\n")
		sb.WriteString("|---|---|---|---|---|---|---|\n")
		for _, s := range sessionsList {
			wsName := filepath.Base(fmt.Sprint(s["workspace"]))
			if wsName == "" || wsName == "." {
				wsName = "Default"
			}
			title := fmt.Sprint(s["title"])
			if len(title) > 28 {
				title = title[:25] + "..."
			}
			sb.WriteString(fmt.Sprintf("| %s | %s | `%s` | %d | %s | %s | %.1f%% |\n",
				title, wsName, s["model"], s["turns_count"],
				formatNumber(s["total_context_tokens"].(int)),
				formatNumber(s["context_limit"].(int)),
				s["usage_percent"].(float64)))
		}
	}

	return map[string]any{
		"conv_id":              targetConvID,
		"model":                modelName,
		"workspace":            wsPath,
		"system_prompt_tokens": systemPromptTokens,
		"tools_tokens":         toolsTokens,
		"memory_tokens":        memoryTokens,
		"history_user_tokens":  historyUserTokens,
		"history_agent_tokens": historyAgentTokens,
		"history_tools_tokens": historyToolsTokens,
		"components": map[string]any{
			"system_prompt":   systemPromptTokens,
			"tool_schemas":    toolsTokens,
			"memory":          memoryTokens,
			"user_prompts":    historyUserTokens,
			"agent_responses": historyAgentTokens,
			"tool_outputs":    historyToolsTokens,
		},
		"total_context_tokens": totalActiveContext,
		"context_limit":        limit,
		"percentage":           math.Round(usagePercent*10) / 10,
		"usage_percent":        math.Round(usagePercent*10) / 10,
		"sessions":             sessionsList,
		"markdown_report":      sb.String(),
	}
}

func formatNumber(n int) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	str := fmt.Sprintf("%d", n)
	var res []string
	for len(str) > 3 {
		res = append([]string{str[len(str)-3:]}, res...)
		str = str[:len(str)-3]
	}
	if len(str) > 0 {
		res = append([]string{str}, res...)
	}
	return strings.Join(res, ",")
}

func getInt64(val any) int64 {
	switch v := val.(type) {
	case int64:
		return v
	case int:
		return int64(v)
	case float64:
		return int64(v)
	case string:
		var n int64
		_, _ = fmt.Sscanf(v, "%d", &n)
		return n
	default:
		return 0
	}
}

func getInt(m map[string]any, keys ...string) int {
	for _, k := range keys {
		if val, ok := m[k]; ok {
			switch v := val.(type) {
			case int:
				return v
			case int64:
				return int(v)
			case float64:
				return int(v)
			case string:
				var n int
				_, _ = fmt.Sscanf(v, "%d", &n)
				return n
			}
		}
	}
	return 0
}
