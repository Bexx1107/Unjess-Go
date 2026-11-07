package cli

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"unjess-daemon/internal/tools"
)

var (
	reBold         = regexp.MustCompile(`\*\*(.+?)\*\*`)
	reItalic       = regexp.MustCompile(`\*([^\s*].*?)\*`)
	reInlineCode   = regexp.MustCompile("`([^`]+)`")
	reNumberedList = regexp.MustCompile(`^(\d+)\.\s+(.*)$`)
)

// RenderMarkdown formats a Markdown string for clear, colorized terminal display.
// Handles code blocks with box borders and cyan syntax highlighting, # headings in bold accent/cyan,
// **bold**, `inline code`, bullet lists with •, blockquotes with ▎, and horizontal rules.
func RenderMarkdown(md string) string {
	if strings.TrimSpace(md) == "" {
		return ""
	}

	lines := strings.Split(md, "\n")
	var result []string
	inCodeBlock := false
	codeBlockLang := ""

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Handle fenced code blocks
		if strings.HasPrefix(trimmed, "```") {
			if !inCodeBlock {
				inCodeBlock = true
				codeBlockLang = strings.TrimSpace(strings.TrimPrefix(trimmed, "```"))
				if codeBlockLang == "" {
					codeBlockLang = "code"
				}
				header := fmt.Sprintf("%s┌── [%s] %s%s",
					ColorDim, codeBlockLang, strings.Repeat("─", maxInt(10, 52-len(codeBlockLang))), ColorReset)
				result = append(result, header)
				continue
			} else {
				inCodeBlock = false
				codeBlockLang = ""
				footer := fmt.Sprintf("%s└──%s%s", ColorDim, strings.Repeat("─", 58), ColorReset)
				result = append(result, footer)
				continue
			}
		}

		// Inside code block: indent with vertical border and colorize
		if inCodeBlock {
			result = append(result, fmt.Sprintf("%s│%s   %s%s%s", ColorDim, ColorReset, ColorCyanBright, line, ColorReset))
			continue
		}

		// Horizontal rule
		if trimmed == "---" || trimmed == "***" || trimmed == "___" {
			result = append(result, fmt.Sprintf("%s%s%s", ColorDim, strings.Repeat("─", 60), ColorReset))
			continue
		}

		// Markdown Headings
		if strings.HasPrefix(line, "# ") {
			title := strings.TrimPrefix(line, "# ")
			formatted := formatInlineMarkdown(title)
			result = append(result, fmt.Sprintf("\n%s%s%s%s", ColorBold, ColorAccent, formatted, ColorReset))
			result = append(result, fmt.Sprintf("%s%s%s", ColorDim, strings.Repeat("━", maxInt(6, minInt(len(title)+2, 50))), ColorReset))
			continue
		}
		if strings.HasPrefix(line, "## ") {
			title := strings.TrimPrefix(line, "## ")
			formatted := formatInlineMarkdown(title)
			result = append(result, fmt.Sprintf("\n%s%s%s%s", ColorBold, ColorCyanBright, formatted, ColorReset))
			continue
		}
		if strings.HasPrefix(line, "### ") {
			title := strings.TrimPrefix(line, "### ")
			formatted := formatInlineMarkdown(title)
			result = append(result, fmt.Sprintf("\n%s%s%s%s", ColorBold, ColorWhite, formatted, ColorReset))
			continue
		}
		if strings.HasPrefix(line, "#### ") {
			title := strings.TrimPrefix(line, "#### ")
			formatted := formatInlineMarkdown(title)
			result = append(result, fmt.Sprintf("\n%s%s%s%s", ColorBold, ColorDim, formatted, ColorReset))
			continue
		}

		// Blockquotes
		if strings.HasPrefix(trimmed, "> ") {
			quote := strings.TrimPrefix(trimmed, "> ")
			formatted := formatInlineMarkdown(quote)
			result = append(result, fmt.Sprintf("  %s▎%s %s%s%s", ColorAccent, ColorReset, ColorItalic, formatted, ColorReset))
			continue
		}

		// Bullet lists
		if strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* ") {
			item := trimmed[2:]
			formatted := formatInlineMarkdown(item)
			indent := line[:len(line)-len(trimmed)]
			result = append(result, fmt.Sprintf("%s  %s•%s %s", indent, ColorAccent, ColorReset, formatted))
			continue
		}

		// Numbered lists
		if match := reNumberedList.FindStringSubmatch(trimmed); len(match) == 3 {
			numStr := match[1]
			item := match[2]
			formatted := formatInlineMarkdown(item)
			indent := line[:len(line)-len(trimmed)]
			result = append(result, fmt.Sprintf("%s  %s%s.%s %s", indent, ColorAccent, numStr, ColorReset, formatted))
			continue
		}

		// Tables: pipe-delimited lines
		if strings.HasPrefix(trimmed, "|") && strings.HasSuffix(trimmed, "|") {
			// Separator row: |---|---|
			if strings.Contains(trimmed, "---") {
				sep := strings.ReplaceAll(trimmed, "-", "─")
				sep = strings.ReplaceAll(sep, "|", "┼")
				result = append(result, fmt.Sprintf("%s%s%s", ColorDim, sep, ColorReset))
				continue
			}
			cells := strings.Split(trimmed, "|")
			var styledCells []string
			for _, cell := range cells {
				styledCells = append(styledCells, formatInlineMarkdown(cell))
			}
			result = append(result, strings.Join(styledCells, fmt.Sprintf("%s│%s", ColorDim, ColorReset)))
			continue
		}

		// Standard prose lines
		result = append(result, formatInlineMarkdown(line))
	}

	if inCodeBlock {
		result = append(result, fmt.Sprintf("%s└──%s%s", ColorDim, strings.Repeat("─", 58), ColorReset))
	}

	return strings.Join(result, "\n")
}

// formatInlineMarkdown replaces inline code `...`, bold **...**, and italic *...*.
func formatInlineMarkdown(s string) string {
	s = reInlineCode.ReplaceAllString(s, ColorCyan+"$1"+ColorReset)
	s = reBold.ReplaceAllString(s, ColorBold+"$1"+ColorReset)
	s = reItalic.ReplaceAllString(s, ColorItalic+"$1"+ColorReset)
	return s
}

// RenderDiff formats a tools.FileDiff with a file status badge and colorized diff lines.
// Additions are colored green, deletions red, hunk headers cyan, and context lines gray.
// Caps long diffs at 80 lines with a summary count.
func RenderDiff(diff tools.FileDiff) string {
	var sb strings.Builder

	actionColor := ColorYellow
	switch strings.ToLower(diff.Action) {
	case "created":
		actionColor = ColorGreenBright
	case "deleted":
		actionColor = ColorRedBright
	case "modified":
		actionColor = ColorCyanBright
	}

	header := fmt.Sprintf("📄 %s%s%s %s[%s]%s (%s+%d%s / %s-%d%s)\n",
		ColorBold, diff.Path, ColorReset,
		actionColor, diff.Action, ColorReset,
		ColorGreen, diff.Additions, ColorReset,
		ColorRed, diff.Deletions, ColorReset,
	)
	sb.WriteString(header)

	if strings.TrimSpace(diff.Diff) == "" {
		return sb.String()
	}

	lines := strings.Split(diff.Diff, "\n")
	const maxLines = 80
	totalLines := len(lines)
	displayLines := lines
	if totalLines > maxLines {
		displayLines = lines[:maxLines]
	}

	for _, line := range displayLines {
		if strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---") {
			sb.WriteString(fmt.Sprintf("%s%s%s\n", ColorDim, line, ColorReset))
		} else if strings.HasPrefix(line, "+") {
			sb.WriteString(fmt.Sprintf("%s%s%s\n", ColorGreen, line, ColorReset))
		} else if strings.HasPrefix(line, "-") {
			sb.WriteString(fmt.Sprintf("%s%s%s\n", ColorRed, line, ColorReset))
		} else if strings.HasPrefix(line, "@@") {
			sb.WriteString(fmt.Sprintf("%s%s%s\n", ColorCyanBright, line, ColorReset))
		} else {
			sb.WriteString(fmt.Sprintf("%s%s%s\n", ColorGray, line, ColorReset))
		}
	}

	if totalLines > maxLines {
		omitted := totalLines - maxLines
		sb.WriteString(fmt.Sprintf("%s... (%d more lines omitted, +%d / -%d total)%s\n",
			ColorDim, omitted, diff.Additions, diff.Deletions, ColorReset))
	}

	return sb.String()
}

// RenderToolCall formats an execution badge for tools.ToolCallRecord, including tool name,
// concise argument preview, status icon (✓ completed, ✗ error, ⟳ running), and duration.
func RenderToolCall(tc tools.ToolCallRecord) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("⚡ %s%s[%s]%s", ColorBold, ColorAccent, tc.Name, ColorReset))

	argPreview := formatToolArgsPreview(tc.Name, tc.Arguments)
	if argPreview != "" {
		sb.WriteString(fmt.Sprintf(" %s%s%s", ColorDim, argPreview, ColorReset))
	}

	var statusIcon string
	switch strings.ToLower(tc.Status) {
	case "completed":
		statusIcon = fmt.Sprintf(" %s✓%s", ColorGreenBright, ColorReset)
	case "error":
		statusIcon = fmt.Sprintf(" %s✗%s", ColorRedBright, ColorReset)
	case "running":
		statusIcon = fmt.Sprintf(" %s⟳%s", ColorYellow, ColorReset)
	default:
		if tc.Status != "" {
			statusIcon = fmt.Sprintf(" [%s]", tc.Status)
		}
	}
	sb.WriteString(statusIcon)

	if tc.DurationSec > 0 {
		sb.WriteString(fmt.Sprintf(" %s(%0.2fs)%s", ColorDim, tc.DurationSec, ColorReset))
	}

	if strings.ToLower(tc.Status) == "error" && tc.Result != "" {
		errPreview := strings.TrimSpace(tc.Result)
		if len(errPreview) > 120 {
			errPreview = errPreview[:117] + "..."
		}
		sb.WriteString(fmt.Sprintf("\n  %s└─ Error: %s%s", ColorRedBright, errPreview, ColorReset))
	}

	return sb.String()
}

// formatToolArgsPreview generates a concise string representation of tool parameters.
func formatToolArgsPreview(name string, args map[string]any) string {
	if len(args) == 0 {
		return ""
	}

	// Prioritize primary parameter names
	priorityKeys := []string{"path", "command", "pattern", "query", "url", "file_path", "symbol", "name"}
	for _, k := range priorityKeys {
		if val, ok := args[k]; ok {
			strVal := fmt.Sprint(val)
			if len(strVal) > 40 {
				strVal = strVal[:37] + "..."
			}
			return fmt.Sprintf("(%s=%q)", k, strVal)
		}
	}

	// Fallback to sorted key/value list up to 2 items
	var keys []string
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var parts []string
	for i, k := range keys {
		if i >= 2 {
			parts = append(parts, "...")
			break
		}
		v := fmt.Sprint(args[k])
		if len(v) > 20 {
			v = v[:17] + "..."
		}
		parts = append(parts, fmt.Sprintf("%s=%s", k, v))
	}
	return "(" + strings.Join(parts, ", ") + ")"
}

// RenderSubagentStep formats an individual subagent transcript step for real-time streaming output.
func RenderSubagentStep(role string, step map[string]any) string {
	var sb strings.Builder

	r := role
	if r == "" {
		r = fmt.Sprint(step["role"])
	}
	if r == "" || r == "<nil>" {
		r = "Subagent"
	}

	title := fmt.Sprint(step["title"])
	stepType := fmt.Sprint(step["type"])
	status := fmt.Sprint(step["status"])
	if title == "" || title == "<nil>" {
		if stepType != "" && stepType != "<nil>" {
			title = strings.Title(strings.ReplaceAll(stepType, "_", " "))
		} else {
			title = "Step"
		}
	}

	var statusBadge string
	switch strings.ToLower(status) {
	case "completed":
		statusBadge = fmt.Sprintf("%s✓%s", ColorGreenBright, ColorReset)
	case "error":
		statusBadge = fmt.Sprintf("%s✗%s", ColorRedBright, ColorReset)
	case "running":
		statusBadge = fmt.Sprintf("%s⟳%s", ColorYellow, ColorReset)
	default:
		if status != "" && status != "<nil>" {
			statusBadge = fmt.Sprintf("(%s)", status)
		}
	}

	sb.WriteString(fmt.Sprintf("🤖 %s%s[%s]%s %s%s%s", ColorBold, ColorMagenta, r, ColorReset, ColorBold, title, ColorReset))
	if statusBadge != "" {
		sb.WriteString(fmt.Sprintf(" %s", statusBadge))
	}

	if d, ok := step["duration_s"].(float64); ok && d > 0 {
		sb.WriteString(fmt.Sprintf(" %s(%0.1fs)%s", ColorDim, d, ColorReset))
	}

	switch stepType {
	case "tool_call":
		toolName := fmt.Sprint(step["tool_name"])
		if toolName != "" && toolName != "<nil>" {
			var args map[string]any
			if a, ok := step["arguments"].(map[string]any); ok {
				args = a
			}
			preview := formatToolArgsPreview(toolName, args)
			sb.WriteString(fmt.Sprintf("\n   %s⚡ %s%s%s", ColorDim, toolName, preview, ColorReset))
		}
	case "edit":
		filePath := fmt.Sprint(step["file_path"])
		if filePath != "" && filePath != "<nil>" {
			adds := toInt(step["additions"])
			dels := toInt(step["deletions"])
			sb.WriteString(fmt.Sprintf("\n   %s📄 %s (%s+%d%s / %s-%d%s)%s",
				ColorDim, filePath, ColorGreen, adds, ColorDim, ColorRed, dels, ColorDim, ColorReset))
		}
	case "thinking":
		content := fmt.Sprint(step["content"])
		if content != "" && content != "<nil>" {
			trimmed := strings.TrimSpace(content)
			if len(trimmed) > 80 {
				trimmed = trimmed[:77] + "..."
			}
			sb.WriteString(fmt.Sprintf("\n   %s💭 %s%s", ColorDim, trimmed, ColorReset))
		}
	default:
		content := fmt.Sprint(step["content"])
		if content != "" && content != "<nil>" {
			trimmed := strings.TrimSpace(content)
			if len(trimmed) > 80 {
				trimmed = trimmed[:77] + "..."
			}
			sb.WriteString(fmt.Sprintf("\n   %s%s%s", ColorDim, trimmed, ColorReset))
		}
	}

	return sb.String()
}

// RenderSubagentSummary formats the final or current state of a subagent run into a bordered card.
func RenderSubagentSummary(sub map[string]any) string {
	if sub == nil {
		return ""
	}

	var sb strings.Builder

	id := fmt.Sprint(sub["id"])
	role := fmt.Sprint(sub["role"])
	if role == "" || role == "<nil>" {
		role = "Subagent"
	}
	model := fmt.Sprint(sub["model"])
	status := fmt.Sprint(sub["status"])
	currentAction := fmt.Sprint(sub["current_action"])
	result := fmt.Sprint(sub["result"])

	var elapsed float64
	if e, ok := sub["elapsed_s"].(float64); ok {
		elapsed = e
	} else if e, ok := sub["elapsed_seconds"].(float64); ok {
		elapsed = e
	}

	statusColor := ColorYellow
	statusIcon := "⟳"
	switch strings.ToLower(status) {
	case "completed":
		statusColor = ColorGreenBright
		statusIcon = "✓"
	case "error":
		statusColor = ColorRedBright
		statusIcon = "✗"
	case "aborted":
		statusColor = ColorRed
		statusIcon = "⏹"
	}

	borderWidth := 58
	titleStr := fmt.Sprintf("🤖 %s (%s)", role, id)
	padLen := maxInt(2, borderWidth-len(titleStr)-6)

	sb.WriteString(fmt.Sprintf("%s┌── %s%s%s %s%s\n",
		ColorDim, ColorBold, ColorMagenta, titleStr, strings.Repeat("─", padLen), ColorReset))

	var metaParts []string
	metaParts = append(metaParts, fmt.Sprintf("Status: %s%s %s%s", statusColor, statusIcon, status, ColorReset))
	if model != "" && model != "<nil>" {
		metaParts = append(metaParts, fmt.Sprintf("Model: %s%s%s", ColorCyanBright, model, ColorReset))
	}
	if elapsed > 0 {
		metaParts = append(metaParts, fmt.Sprintf("Elapsed: %s%0.1fs%s", ColorWhite, elapsed, ColorReset))
	}
	sb.WriteString(fmt.Sprintf("%s│%s  %s\n", ColorDim, ColorReset, strings.Join(metaParts, "  •  ")))

	if currentAction != "" && currentAction != "<nil>" && strings.ToLower(status) != "completed" {
		sb.WriteString(fmt.Sprintf("%s│%s  Action: %s%s%s\n", ColorDim, ColorReset, ColorYellow, currentAction, ColorReset))
	}

	if result != "" && result != "<nil>" {
		resTrim := strings.TrimSpace(result)
		lines := strings.Split(resTrim, "\n")
		sb.WriteString(fmt.Sprintf("%s│%s  Result:\n", ColorDim, ColorReset))
		for i, l := range lines {
			if i >= 4 {
				sb.WriteString(fmt.Sprintf("%s│%s    %s... (%d more lines)%s\n", ColorDim, ColorReset, ColorDim, len(lines)-4, ColorReset))
				break
			}
			if len(l) > 85 {
				l = l[:82] + "..."
			}
			sb.WriteString(fmt.Sprintf("%s│%s    %s%s%s\n", ColorDim, ColorReset, ColorWhite, l, ColorReset))
		}
	}

	sb.WriteString(fmt.Sprintf("%s└%s%s\n", ColorDim, strings.Repeat("─", borderWidth), ColorReset))

	return sb.String()
}

// RenderUsageSummary formats token and cost analytics from hm.GetUsageSummary(convID)
// into a clean terminal table with breakdown by scope and model.
func RenderUsageSummary(summary map[string]any) string {
	if summary == nil {
		return "No usage data available.\n"
	}

	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("\n%s%s📊 Token & Cost Usage Summary%s\n", ColorBold, ColorAccent, ColorReset))

	sb.WriteString(fmt.Sprintf("%s┌─────────────────┬───────┬──────────────┬──────────────┬──────────────┬────────────┐%s\n", ColorDim, ColorReset))
	sb.WriteString(fmt.Sprintf("%s│%s %s%-15s%s %s│%s %s%5s%s %s│%s %s%12s%s %s│%s %s%12s%s %s│%s %s%12s%s %s│%s %s%10s%s %s│%s\n",
		ColorDim, ColorReset, ColorBold, "Scope", ColorReset,
		ColorDim, ColorReset, ColorBold, "Turns", ColorReset,
		ColorDim, ColorReset, ColorBold, "Input Tok.", ColorReset,
		ColorDim, ColorReset, ColorBold, "Output Tok.", ColorReset,
		ColorDim, ColorReset, ColorBold, "Total Tokens", ColorReset,
		ColorDim, ColorReset, ColorBold, "Cost (USD)", ColorReset,
		ColorDim, ColorReset,
	))
	sb.WriteString(fmt.Sprintf("%s├─────────────────┼───────┼──────────────┼──────────────┼──────────────┼────────────┤%s\n", ColorDim, ColorReset))

	extractBucket := func(key string) (turns, inTok, outTok, totTok int, cost float64, found bool) {
		b, ok := summary[key].(map[string]any)
		if !ok {
			return 0, 0, 0, 0, 0, false
		}
		turns = toInt(b["turns_count"])
		inTok = toInt(b["input_tokens"])
		outTok = toInt(b["output_tokens"])
		totTok = toInt(b["total_tokens"])
		cost = toFloat(b["cost_usd"])
		return turns, inTok, outTok, totTok, cost, true
	}

	scopes := []struct {
		name string
		key  string
	}{
		{"Active Session", "session"},
		{"Today", "day"},
		{"Last 7 Days", "week"},
		{"Last 30 Days", "month"},
		{"All Time", "all_time"},
	}

	for _, sc := range scopes {
		turns, inTok, outTok, totTok, cost, ok := extractBucket(sc.key)
		if !ok && sc.key != "session" && sc.key != "all_time" {
			continue
		}
		sb.WriteString(fmt.Sprintf("%s│%s %-15s %s│%s %5d %s│%s %12s %s│%s %12s %s│%s %12s %s│%s %s$%9.4f%s %s│%s\n",
			ColorDim, ColorReset, sc.name,
			ColorDim, ColorReset, turns,
			ColorDim, ColorReset, formatTokenNum(inTok),
			ColorDim, ColorReset, formatTokenNum(outTok),
			ColorDim, ColorReset, formatTokenNum(totTok),
			ColorDim, ColorReset, ColorGreen, cost, ColorReset,
			ColorDim, ColorReset,
		))
	}
	sb.WriteString(fmt.Sprintf("%s└─────────────────┴───────┴──────────────┴──────────────┴──────────────┴────────────┘%s\n", ColorDim, ColorReset))

	// By Model breakdown
	var byModelList []map[string]any
	if bm, ok := summary["by_model"].([]map[string]any); ok {
		byModelList = bm
	} else if bmA, ok := summary["by_model"].([]any); ok {
		for _, item := range bmA {
			if m, ok := item.(map[string]any); ok {
				byModelList = append(byModelList, m)
			}
		}
	}

	if len(byModelList) > 0 {
		sb.WriteString(fmt.Sprintf("\n%s%sBreakdown by Model:%s\n", ColorBold, ColorCyanBright, ColorReset))
		for _, m := range byModelList {
			modelName := fmt.Sprint(m["model"])
			provider := fmt.Sprint(m["provider"])
			totalTok := toInt(m["total_tokens"])
			turns := toInt(m["turns_count"])
			cost := toFloat(m["cost_usd"])

			pStr := ""
			if provider != "" && provider != "<nil>" {
				pStr = fmt.Sprintf(" (%s)", provider)
			}
			sb.WriteString(fmt.Sprintf("  %s•%s %s%s%s%s: %s%s%s tokens across %d turns (%s$%0.4f%s)\n",
				ColorAccent, ColorReset,
				ColorBold, modelName, pStr, ColorReset,
				ColorWhite, formatTokenNum(totalTok), ColorReset,
				turns,
				ColorGreen, cost, ColorReset,
			))
		}
	}

	return sb.String()
}

// RenderContextUsage formats context window capacity with a visual progress bar
// [████░░░░░░] XX% and detailed component token breakdown.
func RenderContextUsage(usage map[string]any) string {
	if usage == nil {
		return "No context usage data available.\n"
	}

	var sb strings.Builder

	model := fmt.Sprint(usage["model"])
	if model == "" || model == "<nil>" {
		model = "default"
	}

	totalTokens := toInt(usage["total_context_tokens"])
	if totalTokens == 0 {
		totalTokens = toInt(usage["context_tokens"])
	}
	limit := toInt(usage["context_limit"])
	if limit == 0 {
		limit = 128000
	}

	pct := toFloat(usage["percentage"])
	if pct == 0 {
		pct = toFloat(usage["usage_percent"])
	}
	if pct == 0 && limit > 0 {
		pct = (float64(totalTokens) / float64(limit)) * 100.0
	}

	const barWidth = 25
	filledLen := int(math.Round((pct / 100.0) * float64(barWidth)))
	if filledLen < 0 {
		filledLen = 0
	}
	if filledLen > barWidth {
		filledLen = barWidth
	}
	emptyLen := barWidth - filledLen

	barColor := ColorGreenBright
	if pct >= 85.0 {
		barColor = ColorRedBright
	} else if pct >= 60.0 {
		barColor = ColorYellow
	}

	progressBar := fmt.Sprintf("[%s%s%s%s%s]",
		barColor, strings.Repeat("█", filledLen),
		ColorGray, strings.Repeat("░", emptyLen),
		ColorReset,
	)

	sb.WriteString(fmt.Sprintf("\n%s%s🧠 Context Window Usage%s\n", ColorBold, ColorAccent, ColorReset))
	sb.WriteString(fmt.Sprintf("%s %s%.1f%%%s (%s / %s tokens) • %s%s%s\n",
		progressBar,
		ColorBold, pct, ColorReset,
		formatTokenNum(totalTokens), formatTokenNum(limit),
		ColorCyanBright, model, ColorReset,
	))

	sysTokens := toInt(usage["system_prompt_tokens"])
	toolsTokens := toInt(usage["tools_tokens"])
	memTokens := toInt(usage["memory_tokens"])
	userTokens := toInt(usage["history_user_tokens"])
	agentTokens := toInt(usage["history_agent_tokens"])
	histToolsTokens := toInt(usage["history_tools_tokens"])

	if comp, ok := usage["components"].(map[string]any); ok {
		if sysTokens == 0 {
			sysTokens = toInt(comp["system_prompt"])
		}
		if toolsTokens == 0 {
			toolsTokens = toInt(comp["tool_schemas"])
		}
		if memTokens == 0 {
			memTokens = toInt(comp["memory"])
		}
		if userTokens == 0 {
			userTokens = toInt(comp["user_prompts"])
		}
		if agentTokens == 0 {
			agentTokens = toInt(comp["agent_responses"])
		}
		if histToolsTokens == 0 {
			histToolsTokens = toInt(comp["tool_outputs"])
		}
	}

	if sysTokens > 0 || toolsTokens > 0 || memTokens > 0 || userTokens > 0 || agentTokens > 0 || histToolsTokens > 0 {
		sb.WriteString(fmt.Sprintf("%sBreakdown:%s\n", ColorDim, ColorReset))
		items := []struct {
			name   string
			tokens int
		}{
			{"System Prompt & Instructions", sysTokens},
			{"Tool Definitions & Schemas", toolsTokens},
			{"Cross-Session Memory", memTokens},
			{"User Prompts History", userTokens},
			{"Agent Thinking & Responses", agentTokens},
			{"Tool Execution History", histToolsTokens},
		}
		for i, it := range items {
			prefix := "├─"
			if i == len(items)-1 {
				prefix = "└─"
			}
			p := 0.0
			if limit > 0 {
				p = (float64(it.tokens) / float64(limit)) * 100.0
			}
			sb.WriteString(fmt.Sprintf("  %s%s %-30s %s%10s tokens%s (%s%4.1f%%%s)\n",
				ColorDim, prefix, it.name,
				ColorWhite, formatTokenNum(it.tokens), ColorReset,
				ColorDim, p, ColorReset,
			))
		}
	}

	return sb.String()
}

// toInt safely converts any numeric interface value to an integer.
func toInt(v any) int {
	if v == nil {
		return 0
	}
	switch val := v.(type) {
	case int:
		return val
	case int64:
		return int(val)
	case float64:
		return int(val)
	case float32:
		return int(val)
	case json.Number:
		i, _ := val.Int64()
		return int(i)
	}
	return 0
}

// toFloat safely converts any numeric interface value to a float64.
func toFloat(v any) float64 {
	if v == nil {
		return 0
	}
	switch val := v.(type) {
	case float64:
		return val
	case float32:
		return float64(val)
	case int:
		return float64(val)
	case int64:
		return float64(val)
	case json.Number:
		f, _ := val.Float64()
		return f
	}
	return 0
}

// formatTokenNum formats an integer with comma thousands separators.
func formatTokenNum(n int) string {
	if n < 0 {
		return "-" + formatTokenNum(-n)
	}
	if n < 1000 {
		return strconv.Itoa(n)
	}
	s := strconv.Itoa(n)
	var res []byte
	l := len(s)
	for i, c := range []byte(s) {
		if i > 0 && (l-i)%3 == 0 {
			res = append(res, ',')
		}
		res = append(res, c)
	}
	return string(res)
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
