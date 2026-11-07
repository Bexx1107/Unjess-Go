package cli

import (
	"bufio"
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"unjess-daemon/internal/agent"
	"unjess-daemon/internal/tools"
)

// RunTurnInteractive runs an interactive agent turn, streaming thinking, tool executions,
// subagent activities, interactive approval requests, and formatted output to the terminal.
func RunTurnInteractive(ctx context.Context, hm *agent.HarnessManager, req agent.ChatTurnRequest, reader *bufio.Reader, compactTrace ...bool) (map[string]any, error) {
	isCompact := false
	if len(compactTrace) > 0 && compactTrace[0] {
		isCompact = true
	}
	type turnResult struct {
		res map[string]any
		err error
	}

	resCh := make(chan turnResult, 1)
	go func() {
		r, e := hm.RunAgentTurn(ctx, req)
		resCh <- turnResult{res: r, err: e}
	}()

	ticker := time.NewTicker(80 * time.Millisecond)
	defer ticker.Stop()

	var (
		thinkingStarted    bool
		thinkingClosed     bool
		lastThinkingLen    int
		toolCallStatuses   = make(map[int]string)
		seenSubagents      = make(map[string]bool)
		subagentStepCounts = make(map[string]int)
		subagentCompleted  = make(map[string]bool)
		handledApprovals   = make(map[string]bool)
		lastLiveState      map[string]any
	)

	processLiveState := func(liveState map[string]any) {
		if liveState == nil {
			return
		}
		lastLiveState = liveState

		// 1. Stream thinking delta
		thinking, _ := liveState["thinking"].(string)
		if len(thinking) > lastThinkingLen {
			if !thinkingStarted {
				fmt.Printf("%s💭 Thinking...%s\n", ColorDim, ColorReset)
				thinkingStarted = true
			}
			delta := thinking[lastThinkingLen:]
			fmt.Print(ColorDim + ColorItalic + delta + ColorReset)
			lastThinkingLen = len(thinking)
		}

		// 2. Close thinking if tool calls or text begin
		toolCalls := extractToolCalls(liveState["tool_calls"])
		text, _ := liveState["text"].(string)
		if thinkingStarted && !thinkingClosed && (len(toolCalls) > 0 || len(strings.TrimSpace(text)) > 0) {
			fmt.Println()
			thinkingClosed = true
		}

		// 3. Render tool calls
		for i, tc := range toolCalls {
			prevStatus, seen := toolCallStatuses[i]
			if !seen || prevStatus != tc.Status {
				if thinkingStarted && !thinkingClosed {
					fmt.Println()
					thinkingClosed = true
				}
				if isCompact {
					fmt.Printf("\r\033[K%s⚡ [%d/%d] %s: %s...%s", ColorDim, i+1, len(toolCalls), tc.Name, formatToolArgsPreview(tc.Name, tc.Arguments), ColorReset)
				} else {
					if out := RenderToolCall(tc); out != "" {
						printLine(out)
					}
				}
				toolCallStatuses[i] = tc.Status
			}
		}

		// 4. Track subagents
		rawSubagents := extractSubagentMaps(liveState["subagents"])
		for _, sub := range rawSubagents {
			subID, _ := sub["id"].(string)
			role, _ := sub["role"].(string)
			typeName, _ := sub["type_name"].(string)
			status, _ := sub["status"].(string)

			if subID != "" && !seenSubagents[subID] {
				seenSubagents[subID] = true
				if thinkingStarted && !thinkingClosed {
					fmt.Println()
					thinkingClosed = true
				}
				PrintInfo(fmt.Sprintf("Spawning subagent: %s (%s)", role, typeName))
			}

			steps := extractSteps(sub["steps"])
			prevCount := subagentStepCounts[subID]
			if len(steps) > prevCount {
				for sIdx := prevCount; sIdx < len(steps); sIdx++ {
					stMap := stepToMap(steps[sIdx])
					if stMap != nil {
						if thinkingStarted && !thinkingClosed {
							fmt.Println()
							thinkingClosed = true
						}
						if out := RenderSubagentStep(role, stMap); out != "" {
							printLine(out)
						}
					}
				}
				subagentStepCounts[subID] = len(steps)
			}

			if (status == "completed" || status == "error" || status == "aborted") && !subagentCompleted[subID] {
				subagentCompleted[subID] = true
				if thinkingStarted && !thinkingClosed {
					fmt.Println()
					thinkingClosed = true
				}
				if out := RenderSubagentSummary(sub); out != "" {
					printLine(out)
				}
			}
		}

		// 5. Track pending approvals
		if cmdAppr := parsePendingCommandApproval(liveState["pending_command_approval"]); cmdAppr != nil {
			apprID := cmdAppr.ID
			if apprID == "" {
				apprID = fmt.Sprintf("cmd_%s_%s", cmdAppr.ToolName, cmdAppr.Command)
			}
			if !handledApprovals[apprID] {
				handledApprovals[apprID] = true
				if thinkingStarted && !thinkingClosed {
					fmt.Println()
					thinkingClosed = true
				}
				approved, auto := PromptCommandApproval(reader, cmdAppr)
				hm.ResolveCommandApprovalWithMode(approved, auto)
			}
		}

		if planAppr := parsePendingPlanApproval(liveState["pending_plan"]); planAppr != nil {
			apprID := planAppr.ID
			if apprID == "" {
				apprID = fmt.Sprintf("plan_%s", planAppr.Title)
			}
			if !handledApprovals[apprID] {
				handledApprovals[apprID] = true
				if thinkingStarted && !thinkingClosed {
					fmt.Println()
					thinkingClosed = true
				}
				approved, fb := PromptPlanApproval(reader, planAppr)
				hm.ResolvePlanApproval(approved, fb)
			}
		}

		if qAppr := parsePendingQuestionApproval(liveState["pending_question"]); qAppr != nil {
			apprID := qAppr.ID
			if apprID == "" {
				apprID = "question_approval"
			}
			if !handledApprovals[apprID] {
				handledApprovals[apprID] = true
				if thinkingStarted && !thinkingClosed {
					fmt.Println()
					thinkingClosed = true
				}
				answers, skipped := PromptQuestionApproval(reader, qAppr)
				hm.ResolveQuestionApproval(answers, skipped)
			}
		}

		if subAppr := parsePendingSubagentApproval(liveState["pending_subagent_approval"]); subAppr != nil {
			apprID := subAppr.ID
			if apprID == "" {
				apprID = "subagent_approval"
			}
			if !handledApprovals[apprID] {
				handledApprovals[apprID] = true
				if thinkingStarted && !thinkingClosed {
					fmt.Println()
					thinkingClosed = true
				}
				specs, approved := PromptSubagentApproval(reader, subAppr)
				hm.ResolveSubagentApproval(specs, approved)
			}
		}
	}

	var turnRes turnResult
	loopDone := false
	for !loopDone {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case tr := <-resCh:
			turnRes = tr
			loopDone = true
		case <-ticker.C:
			processLiveState(hm.GetLiveTurnState())
		}
	}

	if thinkingStarted && !thinkingClosed {
		fmt.Println()
		thinkingClosed = true
	}

	if turnRes.err != nil {
		return turnRes.res, turnRes.err
	}

	turn, _ := turnRes.res["turn"].(map[string]any)

	// Print tool calls and diffs (compact or expanded)
	var finalToolCalls []tools.ToolCallRecord
	if turn != nil {
		finalToolCalls = extractToolCalls(turn["tool_calls"])
	}
	if len(finalToolCalls) == 0 && lastLiveState != nil {
		finalToolCalls = extractToolCalls(lastLiveState["tool_calls"])
	}

	var diffs []tools.FileDiff
	if turn != nil {
		diffs = extractDiffs(turn["diffs"])
	}
	if len(diffs) == 0 && lastLiveState != nil {
		diffs = extractDiffs(lastLiveState["diffs"])
	}

	if isCompact {
		if len(finalToolCalls) > 0 || len(diffs) > 0 {
			fmt.Printf("\r\033[K%s● Execution Trace • %d tools • %d diffs (Ctrl+O or /trace to expand)%s\n", ColorDim, len(finalToolCalls), len(diffs), ColorReset)
		}
	} else {
		for i, tc := range finalToolCalls {
			prevStatus, seen := toolCallStatuses[i]
			if !seen || prevStatus != tc.Status {
				if out := RenderToolCall(tc); out != "" {
					printLine(out)
				}
				toolCallStatuses[i] = tc.Status
			}
		}

		for _, diff := range diffs {
			if dOut := RenderDiff(diff); dOut != "" {
				printLine(dOut)
			}
		}
	}

	// Print assistant response
	if turn != nil {
		if text, ok := turn["text"].(string); ok && strings.TrimSpace(text) != "" {
			fmt.Println()
			printLine(RenderMarkdown(text))
			fmt.Println()
		}
	}

	// Print subtle footer line with elapsed seconds and token usage
	if stats, ok := turnRes.res["stats"].(map[string]any); ok && stats != nil {
		printFooter(stats)
	}

	// Check for any remaining subagents needing summary
	if lastLiveState != nil {
		rawSubagents := extractSubagentMaps(lastLiveState["subagents"])
		for _, sub := range rawSubagents {
			subID, _ := sub["id"].(string)
			status, _ := sub["status"].(string)
			if (status == "completed" || status == "error" || status == "aborted") && !subagentCompleted[subID] {
				subagentCompleted[subID] = true
				if out := RenderSubagentSummary(sub); out != "" {
					printLine(out)
				}
			}
		}
	}

	return turnRes.res, nil
}

// printLine outputs a string ensuring it ends with a newline.
func printLine(s string) {
	if s == "" {
		return
	}
	if strings.HasSuffix(s, "\n") {
		fmt.Print(s)
	} else {
		fmt.Println(s)
	}
}

// printFooter formats and prints a subtle elapsed time and token consumption footer.
func printFooter(stats map[string]any) {
	tokensIn := getIntStat(stats, "tokens_in")
	tokensOut := getIntStat(stats, "tokens_out")
	totalTokens := getIntStat(stats, "total_tokens")
	if totalTokens == 0 && (tokensIn > 0 || tokensOut > 0) {
		totalTokens = tokensIn + tokensOut
	}
	elapsedS := getFloatStat(stats, "elapsed_s")

	fmt.Printf("%s⚡ %0.1fs • %s tokens (in: %s, out: %s)%s\n",
		ColorDim,
		elapsedS,
		formatNumber(totalTokens),
		formatNumber(tokensIn),
		formatNumber(tokensOut),
		ColorReset,
	)
}

// formatNumber formats an integer with thousands separator commas.
func formatNumber(n int64) string {
	if n < 0 {
		return "-" + formatNumber(-n)
	}
	if n < 1000 {
		return strconv.FormatInt(n, 10)
	}
	str := strconv.FormatInt(n, 10)
	var result []byte
	l := len(str)
	for i := 0; i < l; i++ {
		if i > 0 && (l-i)%3 == 0 {
			result = append(result, ',')
		}
		result = append(result, str[i])
	}
	return string(result)
}

// extractToolCalls extracts a slice of tools.ToolCallRecord from live state.
func extractToolCalls(val any) []tools.ToolCallRecord {
	if val == nil {
		return nil
	}
	if list, ok := val.([]tools.ToolCallRecord); ok {
		return list
	}
	if list, ok := val.([]*tools.ToolCallRecord); ok {
		res := make([]tools.ToolCallRecord, 0, len(list))
		for _, tc := range list {
			if tc != nil {
				res = append(res, *tc)
			}
		}
		return res
	}
	if list, ok := val.([]any); ok {
		res := make([]tools.ToolCallRecord, 0, len(list))
		for _, item := range list {
			if tc, ok := item.(tools.ToolCallRecord); ok {
				res = append(res, tc)
			} else if tc, ok := item.(*tools.ToolCallRecord); ok && tc != nil {
				res = append(res, *tc)
			} else if m, ok := item.(map[string]any); ok {
				name, _ := m["name"].(string)
				result, _ := m["result"].(string)
				status, _ := m["status"].(string)
				var args map[string]any
				if a, ok := m["arguments"].(map[string]any); ok {
					args = a
				}
				var dur float64
				if d, ok := m["duration_s"].(float64); ok {
					dur = d
				}
				res = append(res, tools.ToolCallRecord{
					Name:        name,
					Arguments:   args,
					Result:      result,
					Status:      status,
					DurationSec: dur,
				})
			}
		}
		return res
	}
	return nil
}

// extractDiffs extracts a slice of tools.FileDiff from turn or live state.
func extractDiffs(val any) []tools.FileDiff {
	if val == nil {
		return nil
	}
	if list, ok := val.([]tools.FileDiff); ok {
		return list
	}
	if list, ok := val.([]*tools.FileDiff); ok {
		res := make([]tools.FileDiff, 0, len(list))
		for _, d := range list {
			if d != nil {
				res = append(res, *d)
			}
		}
		return res
	}
	if list, ok := val.([]any); ok {
		var diffs []tools.FileDiff
		for _, item := range list {
			if fd, ok := item.(tools.FileDiff); ok {
				diffs = append(diffs, fd)
			} else if fd, ok := item.(*tools.FileDiff); ok && fd != nil {
				diffs = append(diffs, *fd)
			} else if m, ok := item.(map[string]any); ok {
				path, _ := m["path"].(string)
				action, _ := m["action"].(string)
				diffText, _ := m["diff"].(string)
				var adds, dels int
				if a, ok := m["additions"].(int); ok {
					adds = a
				} else if a, ok := m["additions"].(float64); ok {
					adds = int(a)
				}
				if d, ok := m["deletions"].(int); ok {
					dels = d
				} else if d, ok := m["deletions"].(float64); ok {
					dels = int(d)
				}
				diffs = append(diffs, tools.FileDiff{
					Path:      path,
					Action:    action,
					Diff:      diffText,
					Additions: adds,
					Deletions: dels,
				})
			}
		}
		return diffs
	}
	return nil
}

// extractSubagentMaps extracts []map[string]any from live state subagents list.
func extractSubagentMaps(val any) []map[string]any {
	if val == nil {
		return nil
	}
	if list, ok := val.([]map[string]any); ok {
		return list
	}
	if list, ok := val.([]any); ok {
		res := make([]map[string]any, 0, len(list))
		for _, item := range list {
			if m, ok := item.(map[string]any); ok {
				res = append(res, m)
			}
		}
		return res
	}
	return nil
}

// extractSteps extracts a slice of step items from a subagent's steps field.
func extractSteps(val any) []any {
	if val == nil {
		return nil
	}
	if list, ok := val.([]any); ok {
		return list
	}
	if list, ok := val.([]agent.SubagentStep); ok {
		res := make([]any, len(list))
		for i, s := range list {
			res[i] = s
		}
		return res
	}
	return nil
}

// stepToMap converts a subagent step (map, struct, or pointer) to map[string]any.
func stepToMap(step any) map[string]any {
	if m, ok := step.(map[string]any); ok {
		return m
	}
	if st, ok := step.(agent.SubagentStep); ok {
		return map[string]any{
			"id":         st.ID,
			"type":       st.Type,
			"title":      st.Title,
			"content":    st.Content,
			"tool_name":  st.ToolName,
			"arguments":  st.Arguments,
			"result":     st.Result,
			"file_path":  st.FilePath,
			"additions":  st.Additions,
			"deletions":  st.Deletions,
			"diff":       st.Diff,
			"duration_s": st.DurationSec,
			"status":     st.Status,
			"timestamp":  st.Timestamp,
		}
	}
	if st, ok := step.(*agent.SubagentStep); ok && st != nil {
		return map[string]any{
			"id":         st.ID,
			"type":       st.Type,
			"title":      st.Title,
			"content":    st.Content,
			"tool_name":  st.ToolName,
			"arguments":  st.Arguments,
			"result":     st.Result,
			"file_path":  st.FilePath,
			"additions":  st.Additions,
			"deletions":  st.Deletions,
			"diff":       st.Diff,
			"duration_s": st.DurationSec,
			"status":     st.Status,
			"timestamp":  st.Timestamp,
		}
	}
	return nil
}

// getIntStat retrieves an integer value from a stats map across numeric types.
func getIntStat(m map[string]any, key string) int64 {
	if v, ok := m[key]; ok {
		switch n := v.(type) {
		case int:
			return int64(n)
		case int64:
			return n
		case float64:
			return int64(n)
		case float32:
			return int64(n)
		}
	}
	return 0
}

// getFloatStat retrieves a float64 value from a stats map across numeric types.
func getFloatStat(m map[string]any, key string) float64 {
	if v, ok := m[key]; ok {
		switch n := v.(type) {
		case float64:
			return n
		case float32:
			return float64(n)
		case int:
			return float64(n)
		case int64:
			return float64(n)
		}
	}
	return 0
}
