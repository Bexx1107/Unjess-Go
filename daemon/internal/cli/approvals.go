package cli

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"unjess-daemon/internal/agent"
)

// PromptCommandApproval prompts the user for interactive approval when a command requires execution in Supervised mode.
func PromptCommandApproval(reader *bufio.Reader, cmdAppr *agent.PendingCommandApproval) (approved bool, switchToAutonomous bool) {
	if cmdAppr == nil {
		return true, false
	}

	commandStr := cmdAppr.Command
	if strings.TrimSpace(commandStr) == "" && len(cmdAppr.Arguments) > 0 {
		if b, err := json.MarshalIndent(cmdAppr.Arguments, "", "  "); err == nil {
			commandStr = string(b)
		} else {
			commandStr = fmt.Sprintf("%v", cmdAppr.Arguments)
		}
	}

	fmt.Println()
	fmt.Printf("%s┌────────────────────── Command Approval Required ──────────────────────┐%s\n", ColorYellow, ColorReset)
	fmt.Printf("%s│%s %-12s %s%s\n", ColorYellow, ColorBold, "Tool:", cmdAppr.ToolName, ColorReset)
	if cmdAppr.Workspace != "" {
		fmt.Printf("%s│%s %-12s %s%s\n", ColorYellow, ColorBold, "Workspace:", cmdAppr.Workspace, ColorReset)
	}
	if strings.Contains(commandStr, "\n") {
		fmt.Printf("%s│%s %-12s%s\n", ColorYellow, ColorBold, "Command:", ColorReset)
		for _, line := range strings.Split(commandStr, "\n") {
			fmt.Printf("%s│%s   %s%s%s\n", ColorYellow, ColorReset, ColorCyanBright, line, ColorReset)
		}
	} else {
		fmt.Printf("%s│%s %-12s %s%s%s\n", ColorYellow, ColorBold, "Command:", ColorCyanBright, commandStr, ColorReset)
	}
	fmt.Printf("%s└───────────────────────────────────────────────────────────────────────┘%s\n", ColorYellow, ColorReset)

	fmt.Printf("%sApprove command execution? [y]es / [n]o / [a]lways (switch to Autonomous): %s", ColorBold, ColorReset)

	input := strings.TrimSpace(readLine(reader))
	inputLower := strings.ToLower(input)
	switch inputLower {
	case "", "y", "yes":
		return true, false
	case "a", "auto", "always", "autonomous":
		return true, true
	case "n", "no":
		return false, false
	default:
		if strings.HasPrefix(inputLower, "y") {
			return true, false
		}
		if strings.HasPrefix(inputLower, "a") {
			return true, true
		}
		return false, false
	}
}

// PromptPlanApproval prompts the user to approve a proposed execution plan or submit feedback/rejection.
func PromptPlanApproval(reader *bufio.Reader, planAppr *agent.PendingPlanApproval) (approved bool, feedback string) {
	if planAppr == nil {
		return true, ""
	}

	fmt.Println()
	fmt.Printf("%s┌──────────────────────── Proposed Plan Approval ────────────────────────┐%s\n", ColorCyan, ColorReset)
	if planAppr.Title != "" {
		fmt.Printf("%s│%s %-12s %s%s%s\n", ColorCyan, ColorBold, "Title:", ColorCyanBright, planAppr.Title, ColorReset)
	}
	if planAppr.Summary != "" {
		fmt.Printf("%s│%s %-12s %s%s\n", ColorCyan, ColorBold, "Summary:", planAppr.Summary, ColorReset)
	}

	if len(planAppr.Steps) > 0 {
		fmt.Printf("%s│%s %-12s%s\n", ColorCyan, ColorBold, "Steps:", ColorReset)
		for idx, step := range planAppr.Steps {
			fmt.Printf("%s│%s   %s%d.%s %s\n", ColorCyan, ColorReset, ColorCyanBright, idx+1, ColorReset, step)
		}
	}

	if len(planAppr.OpenQuestions) > 0 {
		fmt.Printf("%s│%s %-12s%s\n", ColorCyan, ColorBold, "Questions:", ColorReset)
		for _, q := range planAppr.OpenQuestions {
			fmt.Printf("%s│%s   %s?%s %s\n", ColorCyan, ColorReset, ColorYellow, ColorReset, q)
		}
	}
	fmt.Printf("%s└───────────────────────────────────────────────────────────────────────┘%s\n", ColorCyan, ColorReset)

	fmt.Printf("%sApprove plan? [y]es / [n]o / [f]eedback (or type feedback directly): %s", ColorBold, ColorReset)

	input := strings.TrimSpace(readLine(reader))
	inputLower := strings.ToLower(input)
	switch inputLower {
	case "", "y", "yes":
		return true, ""
	case "n", "no":
		fmt.Printf("%sReason for rejection (optional, press Enter to skip): %s", ColorDim, ColorReset)
		reason := strings.TrimSpace(readLine(reader))
		return false, reason
	case "f", "feedback":
		fmt.Printf("%sEnter feedback: %s", ColorBold, ColorReset)
		fb := strings.TrimSpace(readLine(reader))
		return false, fb
	default:
		// User typed feedback directly
		return false, input
	}
}

// PromptQuestionApproval displays interactive questions and collects user selections or write-in answers.
func PromptQuestionApproval(reader *bufio.Reader, qAppr *agent.PendingQuestionApproval) (answers string, skipped bool) {
	if qAppr == nil || len(qAppr.Questions) == 0 {
		return "", true
	}

	var answeredParts []string
	allSkipped := true

	for idx, qMap := range qAppr.Questions {
		qText, _ := qMap["question"].(string)
		if qText == "" {
			qText = fmt.Sprintf("Question %d", idx+1)
		}

		var options []string
		if rawOpts, ok := qMap["options"].([]string); ok {
			options = rawOpts
		} else if rawOpts, ok := qMap["options"].([]any); ok {
			for _, opt := range rawOpts {
				if s, ok := opt.(string); ok {
					options = append(options, s)
				} else if sm, ok := opt.(map[string]any); ok {
					if lbl, ok := sm["label"].(string); ok {
						options = append(options, lbl)
					} else if txt, ok := sm["text"].(string); ok {
						options = append(options, txt)
					} else {
						options = append(options, fmt.Sprintf("%v", opt))
					}
				}
			}
		}

		fmt.Println()
		if len(qAppr.Questions) > 1 {
			fmt.Printf("%s[%d/%d] %s%s\n", ColorAccent, idx+1, len(qAppr.Questions), qText, ColorReset)
		} else {
			fmt.Printf("%s%s%s\n", ColorAccent, qText, ColorReset)
		}

		for optIdx, opt := range options {
			fmt.Printf("  %s[%d]%s %s\n", ColorBold, optIdx+1, ColorReset, opt)
		}
		fmt.Printf("  %s[0]%s Skip (or enter custom response)\n", ColorDim, ColorReset)
		fmt.Printf("%sYour choice: %s", ColorBold, ColorReset)

		ans := strings.TrimSpace(readLine(reader))
		if ans == "0" || strings.EqualFold(ans, "skip") {
			continue
		}
		if ans == "" {
			continue
		}

		allSkipped = false
		if num, err := strconv.Atoi(ans); err == nil && num >= 1 && num <= len(options) {
			answeredParts = append(answeredParts, options[num-1])
		} else {
			answeredParts = append(answeredParts, ans)
		}
	}

	if allSkipped || len(answeredParts) == 0 {
		return "", true
	}

	if len(answeredParts) == 1 {
		return answeredParts[0], false
	}
	return strings.Join(answeredParts, "\n"), false
}

// PromptSubagentApproval prompts the user to confirm spawning background subagents.
func PromptSubagentApproval(reader *bufio.Reader, subAppr *agent.PendingSubagentApproval) (specs []map[string]any, approved bool) {
	if subAppr == nil || len(subAppr.Specs) == 0 {
		return nil, false
	}

	fmt.Println()
	fmt.Printf("%s┌────────────────────── Subagent Spawn Request ──────────────────────┐%s\n", ColorMagenta, ColorReset)
	for idx, spec := range subAppr.Specs {
		role, _ := spec["role"].(string)
		typeName, _ := spec["type_name"].(string)
		model, _ := spec["model"].(string)
		prompt, _ := spec["prompt"].(string)

		if model == "" && subAppr.DefaultModel != "" {
			model = subAppr.DefaultModel
		}

		fmt.Printf("%s│%s #%d: %s%s%s [%s]\n", ColorMagenta, ColorReset, idx+1, ColorBold, role, ColorReset, typeName)
		if model != "" {
			fmt.Printf("%s│%s     Model:  %s%s\n", ColorMagenta, ColorReset, model, ColorReset)
		}
		if prompt != "" {
			preview := prompt
			if len(preview) > 65 {
				preview = preview[:62] + "..."
			}
			fmt.Printf("%s│%s     Prompt: %s%s\n", ColorMagenta, ColorReset, preview, ColorReset)
		}
		if idx < len(subAppr.Specs)-1 {
			fmt.Printf("%s│%s   ------------------------------------------------------------\n", ColorMagenta, ColorReset)
		}
	}
	fmt.Printf("%s└───────────────────────────────────────────────────────────────────┘%s\n", ColorMagenta, ColorReset)

	fmt.Printf("%sApprove spawning %d subagent(s)? [Y/n]: %s", ColorBold, len(subAppr.Specs), ColorReset)
	input := strings.TrimSpace(readLine(reader))
	inputLower := strings.ToLower(input)
	switch inputLower {
	case "", "y", "yes":
		return subAppr.Specs, true
	case "n", "no":
		return subAppr.Specs, false
	default:
		if strings.HasPrefix(inputLower, "y") {
			return subAppr.Specs, true
		}
		return subAppr.Specs, false
	}
}

// LoadImageAttachment loads an image from disk and formats it as a base64 Data URI map.
func LoadImageAttachment(filePath string) (map[string]any, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("read image file: %w", err)
	}

	ext := strings.ToLower(filepath.Ext(filePath))
	var mimeType string
	switch ext {
	case ".png":
		mimeType = "image/png"
	case ".jpg", ".jpeg":
		mimeType = "image/jpeg"
	case ".webp":
		mimeType = "image/webp"
	case ".gif":
		mimeType = "image/gif"
	case ".svg":
		mimeType = "image/svg+xml"
	default:
		mimeType = http.DetectContentType(data)
	}

	enc := base64.StdEncoding.EncodeToString(data)
	dataURI := fmt.Sprintf("data:%s;base64,%s", mimeType, enc)

	return map[string]any{
		"name":      filepath.Base(filePath),
		"data":      dataURI,
		"data_uri":  dataURI,
		"mime_type": mimeType,
	}, nil
}

// LoadFileAttachment loads a file from disk, detecting images to encode as base64 or reading text content.
func LoadFileAttachment(filePath string) (map[string]any, error) {
	ext := strings.ToLower(filepath.Ext(filePath))
	switch ext {
	case ".png", ".jpg", ".jpeg", ".webp", ".gif":
		return LoadImageAttachment(filePath)
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("read file: %w", err)
	}

	absPath, err := filepath.Abs(filePath)
	if err != nil {
		absPath = filePath
	}

	return map[string]any{
		"name":    filepath.Base(filePath),
		"path":    absPath,
		"content": string(data),
		"size":    len(data),
	}, nil
}

// readLine reads a single line of user input from reader, defaulting to os.Stdin if nil.
func readLine(r *bufio.Reader) string {
	if r == nil {
		r = bufio.NewReader(os.Stdin)
	}
	line, err := r.ReadString('\n')
	if err != nil && len(line) == 0 {
		return ""
	}
	return strings.TrimRight(line, "\r\n")
}

// parsePendingCommandApproval converts a live state pending command approval into typed pointer.
func parsePendingCommandApproval(val any) *agent.PendingCommandApproval {
	if val == nil {
		return nil
	}
	if pca, ok := val.(*agent.PendingCommandApproval); ok {
		return pca
	}
	if m, ok := val.(map[string]any); ok {
		id, _ := m["id"].(string)
		toolName, _ := m["tool_name"].(string)
		cmd, _ := m["command"].(string)
		ws, _ := m["workspace"].(string)
		var args map[string]any
		if a, ok := m["arguments"].(map[string]any); ok {
			args = a
		}
		var created int64
		if c, ok := m["created_at"].(float64); ok {
			created = int64(c)
		} else if c, ok := m["created_at"].(int64); ok {
			created = c
		}
		return &agent.PendingCommandApproval{
			ID:        id,
			ToolName:  toolName,
			Command:   cmd,
			Arguments: args,
			Workspace: ws,
			CreatedAt: created,
		}
	}
	return nil
}

// parsePendingPlanApproval converts a live state pending plan approval into typed pointer.
func parsePendingPlanApproval(val any) *agent.PendingPlanApproval {
	if val == nil {
		return nil
	}
	if ppa, ok := val.(*agent.PendingPlanApproval); ok {
		return ppa
	}
	if m, ok := val.(map[string]any); ok {
		id, _ := m["id"].(string)
		title, _ := m["title"].(string)
		summary, _ := m["summary"].(string)
		var steps []string
		if sSlice, ok := m["steps"].([]string); ok {
			steps = sSlice
		} else if sSlice, ok := m["steps"].([]any); ok {
			for _, item := range sSlice {
				if s, ok := item.(string); ok {
					steps = append(steps, s)
				}
			}
		}
		var openQuestions []string
		if oqSlice, ok := m["open_questions"].([]string); ok {
			openQuestions = oqSlice
		} else if oqSlice, ok := m["open_questions"].([]any); ok {
			for _, item := range oqSlice {
				if s, ok := item.(string); ok {
					openQuestions = append(openQuestions, s)
				}
			}
		}
		return &agent.PendingPlanApproval{
			ID:            id,
			Title:         title,
			Summary:       summary,
			Steps:         steps,
			OpenQuestions: openQuestions,
		}
	}
	return nil
}

// parsePendingQuestionApproval converts a live state pending question approval into typed pointer.
func parsePendingQuestionApproval(val any) *agent.PendingQuestionApproval {
	if val == nil {
		return nil
	}
	if pqa, ok := val.(*agent.PendingQuestionApproval); ok {
		return pqa
	}
	if m, ok := val.(map[string]any); ok {
		id, _ := m["id"].(string)
		var questions []map[string]any
		if qList, ok := m["questions"].([]map[string]any); ok {
			questions = qList
		} else if qList, ok := m["questions"].([]any); ok {
			for _, item := range qList {
				if qm, ok := item.(map[string]any); ok {
					questions = append(questions, qm)
				}
			}
		}
		return &agent.PendingQuestionApproval{
			ID:        id,
			Questions: questions,
		}
	}
	return nil
}

// parsePendingSubagentApproval converts a live state pending subagent approval into typed pointer.
func parsePendingSubagentApproval(val any) *agent.PendingSubagentApproval {
	if val == nil {
		return nil
	}
	if psa, ok := val.(*agent.PendingSubagentApproval); ok {
		return psa
	}
	if m, ok := val.(map[string]any); ok {
		id, _ := m["id"].(string)
		var specs []map[string]any
		if sList, ok := m["specs"].([]map[string]any); ok {
			specs = sList
		} else if sList, ok := m["specs"].([]any); ok {
			for _, item := range sList {
				if sm, ok := item.(map[string]any); ok {
					specs = append(specs, sm)
				}
			}
		}
		defModel, _ := m["default_model"].(string)
		return &agent.PendingSubagentApproval{
			ID:           id,
			Specs:        specs,
			DefaultModel: defModel,
		}
	}
	return nil
}
