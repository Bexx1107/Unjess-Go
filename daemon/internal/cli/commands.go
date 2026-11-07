package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"unjess-daemon/internal/agent"
	"unjess-daemon/internal/config"
	"unjess-daemon/internal/frontier"
	"unjess-daemon/internal/mcp"
	"unjess-daemon/internal/memory"
	"unjess-daemon/internal/scheduler"
	"unjess-daemon/internal/security"
	"unjess-daemon/internal/skills"
	"unjess-daemon/internal/tools"
	"unjess-daemon/internal/updater"
	"unjess-daemon/internal/workspace"
	"unjess-daemon/internal/worktree"
)

// CommandSession maintains the active CLI interactive state across turns.
type CommandSession struct {
	HM                 *agent.HarnessManager
	Settings           *config.Settings
	ConvID             string
	Workspace          string
	Model              string
	AgentMode          string
	ReasoningEffort    string
	CavemanMode        bool
	PonytailMode       bool
	WorkflowID         string
	AgentID            string
	PendingImages      []map[string]any
	PendingAttachments []map[string]any
	Reader             *bufio.Reader
	LastTurn           map[string]any
	CompactTrace       bool
}

// NewCommandSession initializes a new CommandSession with sensible defaults,
// resolving workspace directory, active conversation, model, and mode.
func NewCommandSession(hm *agent.HarnessManager, settings *config.Settings, ws string) *CommandSession {
	resolvedWS := strings.TrimSpace(ws)
	if resolvedWS == "" {
		if wd, err := os.Getwd(); err == nil {
			resolvedWS = wd
		}
	}
	if resolvedWS != "" {
		resolvedWS = filepath.Clean(resolvedWS)
	}

	if hm != nil && resolvedWS != "" {
		_, _ = hm.SelectWorkspace(resolvedWS)
	}

	model := ""
	if settings != nil {
		model = strings.TrimSpace(settings.Model)
	}
	if model == "" {
		model = "gemini-2.5-flash"
	}

	agentMode := "Autonomous"
	if settings != nil && settings.ConfirmCommands {
		agentMode = "Supervised"
	}

	convID := ""
	if hm != nil {
		// Always create a fresh, dedicated conversation for CLI sessions to prevent
		// context bleed-over from the Desktop GUI or previous runs. If the user explicitly
		// requested a specific session with --conv / -c, it overrides this in main.go.
		newConv := hm.CreateConversation("CLI Session", resolvedWS, agentMode)
		if newConv != nil {
			if id, ok := newConv["id"].(string); ok && id != "" {
				convID = id
			}
		}
	}

	return &CommandSession{
		HM:                 hm,
		Settings:           settings,
		ConvID:             convID,
		Workspace:          resolvedWS,
		Model:              model,
		AgentMode:          agentMode,
		ReasoningEffort:    "medium",
		CavemanMode:        false,
		PonytailMode:       false,
		WorkflowID:         "",
		AgentID:            "",
		PendingImages:      make([]map[string]any, 0),
		PendingAttachments: make([]map[string]any, 0),
		Reader:             bufio.NewReader(os.Stdin),
	}
}

// LaunchDesktopGUI locates and launches the detached Unjess Desktop application.
func LaunchDesktopGUI(ws string) error {
	var candidates []string
	selfExe, _ := os.Executable()
	if absSelf, err := filepath.Abs(selfExe); err == nil {
		selfExe = absSelf
	}

	// Check adjacent/parent to current CLI executable
	if selfExe != "" {
		dir := filepath.Dir(selfExe)
		candidates = append(candidates,
			filepath.Join(dir, "unjess-desktop.exe"),
			filepath.Join(dir, "..", "Unjess.exe"),
			filepath.Join(dir, "..", "unjess-desktop.exe"),
			filepath.Join(dir, "..", "dist", "Unjess.exe"),
			filepath.Join(dir, "dist", "Unjess.exe"),
		)
	}

	// Check standard local app data install directory
	if localAppData := os.Getenv("LOCALAPPDATA"); localAppData != "" {
		candidates = append(candidates,
			filepath.Join(localAppData, "Programs", "Unjess", "Unjess.exe"),
			filepath.Join(localAppData, "Programs", "Unjess", "unjess-desktop.exe"),
		)
	}

	// Check current directory and workspace
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates,
			filepath.Join(cwd, "unjess-desktop.exe"),
			filepath.Join(cwd, "dist", "Unjess.exe"),
			filepath.Join(cwd, "Unjess.exe"),
		)
	}
	if ws != "" {
		candidates = append(candidates,
			filepath.Join(ws, "unjess-desktop.exe"),
			filepath.Join(ws, "dist", "Unjess.exe"),
		)
	}

	var targetExe string
	for _, c := range candidates {
		if c == "" {
			continue
		}
		absC, err := filepath.Abs(c)
		if err != nil {
			absC = filepath.Clean(c)
		}
		// Never launch ourselves (the CLI binary in bin\unjess.exe)
		if selfExe != "" && strings.EqualFold(absC, selfExe) {
			continue
		}
		if strings.HasSuffix(strings.ToLower(absC), `\bin\unjess.exe`) {
			continue
		}
		if fi, err := os.Stat(absC); err == nil && !fi.IsDir() {
			targetExe = absC
			break
		}
	}

	if targetExe == "" {
		return fmt.Errorf("could not find Unjess.exe or unjess-desktop.exe (checked installer, local app data, and repo root)")
	}

	var cmd *exec.Cmd
	if ws != "" {
		cmd = exec.Command(targetExe, "--workspace", ws)
	} else {
		cmd = exec.Command(targetExe)
	}
	cmd.Dir = filepath.Dir(targetExe)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start desktop GUI (%s): %w", targetExe, err)
	}
	return nil
}

// DispatchSlashCommand parses and dispatches slash commands starting with "/",
// providing 100% feature parity with the GUI interface.
func DispatchSlashCommand(ctx context.Context, sess *CommandSession, line string) (handled bool, shouldExit bool, promptToRun string) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "/") {
		return false, false, ""
	}

	cmdLine := strings.TrimPrefix(trimmed, "/")
	var cmdName string
	var args string
	if idx := strings.IndexAny(cmdLine, " \t"); idx != -1 {
		cmdName = strings.ToLower(strings.TrimSpace(cmdLine[:idx]))
		args = strings.TrimSpace(cmdLine[idx+1:])
	} else {
		cmdName = strings.ToLower(strings.TrimSpace(cmdLine))
		args = ""
	}

	switch cmdName {
	case "help", "?":
		printHelp()
		return true, false, ""

	case "status":
		handleStatus(sess)
		return true, false, ""

	case "model":
		handleModels(sess, args)
		return true, false, ""

	case "models":
		handleModels(sess, "")
		return true, false, ""

	case "effort":
		handleEffort(sess, args)
		return true, false, ""

	case "mode":
		handleMode(sess, args)
		return true, false, ""

	case "caveman":
		handleCaveman(sess, args)
		return true, false, ""

	case "ponytail":
		handlePonytail(sess, args)
		return true, false, ""

	case "new":
		handleNewConv(sess, args)
		return true, false, ""

	case "conversations", "convs":
		handleConversations(sess)
		return true, false, ""

	case "switch":
		handleSwitchConv(sess, args)
		return true, false, ""

	case "clear":
		handleClearConv(sess)
		return true, false, ""

	case "delete":
		handleDeleteConv(sess, args)
		return true, false, ""

	case "compact":
		handleCompact(sess)
		return true, false, ""

	case "undo":
		handleUndo(sess, args)
		return true, false, ""

	case "revert":
		handleRevert(sess, args)
		return true, false, ""

	case "workspace", "ws":
		handleWorkspace(sess, args)
		return true, false, ""

	case "files":
		handleFiles(sess, args)
		return true, false, ""

	case "git":
		handleGit(sess)
		return true, false, ""

	case "workflows":
		handled, prompt := handleWorkflows(sess, "")
		return handled, false, prompt

	case "workflow":
		handled, prompt := handleWorkflows(sess, args)
		return handled, false, prompt

	case "agents":
		handleAgents(ctx, sess, "")
		return true, false, ""

	case "agent":
		handleAgents(ctx, sess, args)
		return true, false, ""

	case "subagents":
		handleSubagents(ctx, sess, "subagents", "")
		return true, false, ""

	case "spawn":
		handleSubagents(ctx, sess, "spawn", args)
		return true, false, ""

	case "subagent":
		handleSubagents(ctx, sess, "subagent", args)
		return true, false, ""

	case "mcp":
		handleMCP(sess, args)
		return true, false, ""

	case "skills":
		handleSkills(sess, args)
		return true, false, ""

	case "memory", "memories":
		handleMemory(sess, args)
		return true, false, ""

	case "security", "duel":
		handleSecurity(ctx, sess, args)
		return true, false, ""

	case "watchdog":
		handleWatchdog(ctx, sess)
		return true, false, ""

	case "tools":
		handleTools(sess, args)
		return true, false, ""

	case "browser":
		handleBrowser(ctx, sess, args)
		return true, false, ""

	case "usage":
		handleUsage(sess)
		return true, false, ""

	case "context":
		handleContext(sess)
		return true, false, ""

	case "settings":
		handleSettings(sess, "settings", "")
		return true, false, ""

	case "set":
		handleSettings(sess, "set", args)
		return true, false, ""

	case "update":
		handleUpdate(sess, args)
		return true, false, ""

	case "attach":
		handleAttach(sess, false, args)
		return true, false, ""

	case "image":
		handleAttach(sess, true, args)
		return true, false, ""

	case "exec":
		handleExec(ctx, sess, args)
		return true, false, ""

	case "schedule", "schedules":
		handleSchedule(ctx, sess, args)
		return true, false, ""

	case "trace":
		handleTrace(sess, args)
		return true, false, ""

	case "gui", "desktop":
		handleGUI(sess)
		return true, false, ""

	case "exit", "quit", "q":
		return true, true, ""

	default:
		PrintWarn(fmt.Sprintf("Unknown command: /%s. Type /help for available commands.", cmdName))
		return true, false, ""
	}
}

// ---------------------------------------------------------------------------
// Command Handlers
// ---------------------------------------------------------------------------

func printHelp() {
	fmt.Println()
	fmt.Printf("%s%sUnjess Go CLI - Slash Commands & Shortcuts%s\n", ColorBold, ColorAccent, ColorReset)

	printCat := func(title string) {
		fmt.Printf("\n%s%s%s%s\n", ColorBold, ColorWhite, title, ColorReset)
	}
	printCmd := func(cmd, desc string) {
		fmt.Printf("  %s%s%-30s%s %s\n", ColorBold, ColorCyanBright, cmd, ColorReset, desc)
	}

	printCat("Core & Session Management:")
	printCmd("/help, /?", "Show this categorized help guide")
	printCmd("/status", "Show active workspace, git branch, model, and context status")
	printCmd("/new [title]", "Create a new conversation session")
	printCmd("/conversations, /convs", "List all conversation sessions with turn counts")
	printCmd("/switch <conv_id>", "Switch to conversation by ID (or prefix)")
	printCmd("/clear", "Clear turns in current conversation")
	printCmd("/delete <conv_id>", "Delete a conversation session")
	printCmd("/compact", "Compact conversation context using rolling memory")
	printCmd("/undo [turn_index]", "Undo last turn (or turn index) and restore files")
	printCmd("/revert <file> [hunk]", "Revert a git diff hunk in file")
	printCmd("/exit, /quit, /q", "Exit the interactive CLI")

	printCat("Model & Execution Controls:")
	printCmd("/model [name]", "Display or switch active LLM model")
	printCmd("/models", "Discover all available cloud & local models")
	printCmd("/effort [low|med|high]", "Set reasoning effort level")
	printCmd("/mode [auto|super|plan]", "Set execution mode (Autonomous, Supervised, Plan)")
	printCmd("/caveman [on|off]", "Toggle ultra-terse caveman prompt modifier")
	printCmd("/ponytail [on|off]", "Toggle playful ponytail prompt modifier")

	printCat("Workspace & Environment:")
	printCmd("/workspace, /ws [path]", "Show or switch active workspace directory")
	printCmd("/files [filter]", "List files in workspace (with optional filter)")
	printCmd("/git", "Show detailed git status, branch, and porcelain diff")
	printCmd("/exec <command>", "Execute shell command in workspace (or use !<cmd>)")
	printCmd("/gui, /desktop", "Launch Unjess Desktop GUI application detached")

	printCat("Workflows & Automation:")
	printCmd("/workflows", "List saved automation workflows")
	printCmd("/workflow run <id>", "Execute a workflow prompt")
	printCmd("/workflow delete <id>", "Delete a saved workflow")
	printCmd("/schedule [list]", "List persistent scheduled tasks")
	printCmd("/schedule add <cron> <p>", "Add background scheduled task (cron/interval/time)")
	printCmd("/schedule toggle <id>", "Enable or disable a scheduled task")
	printCmd("/schedule run <id>", "Execute a scheduled task immediately")
	printCmd("/schedule delete <id>", "Delete a scheduled task")
	printCmd("/agents", "List custom autonomous agents")
	printCmd("/agent run <id> <task>", "Run custom autonomous agent on a task")
	printCmd("/agent delete <id>", "Delete a custom autonomous agent")
	printCmd("/subagents", "List parallel subagent runs for active conversation")
	printCmd("/spawn <role>: <prompt>", "Spawn a concurrent parallel subagent")
	printCmd("/subagent msg <id> <msg>", "Send a follow-up instruction to a subagent")
	printCmd("/subagent abort <id>", "Abort a running subagent worker")

	printCat("Integrations, Memory & Security:")
	printCmd("/mcp", "List configured MCP servers")
	printCmd("/mcp presets", "List built-in MCP server presets (rea, github, etc.)")
	printCmd("/mcp add <name> <cmd>", "Add a new Model Context Protocol server")
	printCmd("/mcp remove <name>", "Remove an MCP server")
	printCmd("/mcp refresh", "Refresh and reconnect all MCP tools")
	printCmd("/skills", "List preinstalled and workspace skills")
	printCmd("/skills install <url>", "Install skill package from GitHub")
	printCmd("/skills delete <name>", "Delete an installed skill")
	printCmd("/memory [query]", "Search persistent cross-session memories")
	printCmd("/memory save <key> <val>", "Save a cross-session persistent memory")
	printCmd("/memory delete <key>", "Delete a persistent memory by key")
	printCmd("/security [target_url]", "Run Red-Team vs White-Team Security Duel")
	printCmd("/watchdog", "Run workspace health checks and syntax diagnostics")
	printCmd("/tools [delete <name>]", "List or delete synthesized custom tools")
	printCmd("/browser <url>", "Navigate URL using headless Chrome & SOM vision")

	printCat("Diagnostics & System:")
	printCmd("/usage", "Display token ledger and estimated costs")
	printCmd("/context", "Display context window usage progress bar")
	printCmd("/trace [expand|toggle]", "Expand or toggle compact tool execution trace")
	printCmd("/settings", "Display all runtime configuration settings")
	printCmd("/set <key> <value>", "Update a runtime setting value")
	printCmd("/update [apply]", "Check for or apply daemon application updates")
	printCmd("/attach <filepath>", "Attach file to next agent turn")
	printCmd("/image <filepath>", "Attach image to next agent turn")

	printCat("Keyboard Shortcuts:")
	fmt.Printf("  %sEnter%s                        Send prompt turn to agent\n", ColorCyanBright, ColorReset)
	fmt.Printf("  %s\\ at line end%s                Continue typing multiline prompt\n", ColorCyanBright, ColorReset)
	fmt.Printf("  %s!<command>%s                   Fast shell command escape (e.g. !git diff)\n", ColorCyanBright, ColorReset)
	fmt.Printf("  %sCtrl+O%s                       Expand or toggle compact execution trace\n", ColorCyanBright, ColorReset)
	fmt.Printf("  %sCtrl+C%s                       Abort active turn / press twice to quit\n", ColorCyanBright, ColorReset)
	fmt.Println()
}

func handleStatus(sess *CommandSession) {
	fmt.Println()
	fmt.Printf("%s%s=== Unjess Session Status ===%s\n", ColorBold, ColorAccent, ColorReset)
	fmt.Printf("  %sWorkspace:%s        %s\n", ColorBold, ColorReset, sess.Workspace)

	gitInfo := workspace.GetGitInfo(sess.Workspace)
	dirtyStr := "clean"
	if gitInfo.IsDirty {
		dirtyStr = fmt.Sprintf("%d modified files", gitInfo.ChangedCount)
	}
	branch := gitInfo.Branch
	if branch == "" {
		branch = "(none)"
	}
	fmt.Printf("  %sGit Branch:%s       %s (%s)\n", ColorBold, ColorReset, branch, dirtyStr)

	provider := ""
	if sess.Settings != nil {
		provider = sess.Settings.Provider
	}
	if provider == "" {
		provider = "auto"
	}
	fmt.Printf("  %sActive Model:%s     %s%s%s (provider: %s)\n", ColorBold, ColorReset, ColorCyanBright, sess.Model, ColorReset, provider)
	fmt.Printf("  %sAgent Mode:%s       %s%s%s\n", ColorBold, ColorReset, ColorGreenBright, sess.AgentMode, ColorReset)
	fmt.Printf("  %sReasoning Effort:%s %s\n", ColorBold, ColorReset, sess.ReasoningEffort)
	fmt.Printf("  %sModifiers:%s        caveman=%t, ponytail=%t\n", ColorBold, ColorReset, sess.CavemanMode, sess.PonytailMode)
	fmt.Printf("  %sActive Conv ID:%s   %s\n", ColorBold, ColorReset, sess.ConvID)

	attachCount := len(sess.PendingAttachments) + len(sess.PendingImages)
	if attachCount > 0 {
		fmt.Printf("  %sPending Attach:%s   %d file(s)\n", ColorBold, ColorReset, attachCount)
	}

	if sess.HM != nil && sess.ConvID != "" {
		usage := sess.HM.GetContextUsageWithModel(sess.ConvID, sess.Model)
		fmt.Printf("  %sContext Window:%s   %s\n", ColorBold, ColorReset, RenderContextUsage(usage))
	}
	fmt.Println()
}

func handleModels(sess *CommandSession, args string) {
	modelArg := strings.TrimSpace(args)
	if modelArg != "" {
		sess.Model = modelArg
		if sess.Settings != nil {
			_ = config.ApplySettingsUpdate(sess.Settings, map[string]any{"model": modelArg})
			_ = config.SaveConfig(sess.Settings)
			if sess.HM != nil {
				sess.HM.UpdateSettings(sess.Settings)
			}
		}
		PrintSuccess(fmt.Sprintf("Active model switched to: %s", modelArg))
		return
	}

	PrintInfo("Discovering available models across cloud providers & local backends...")
	disc := config.DiscoverModels(sess.Settings, "", "")
	rawModels, _ := disc["models"].([]map[string]any)
	if len(rawModels) == 0 {
		if slice, ok := disc["models"].([]any); ok {
			for _, item := range slice {
				if m, ok := item.(map[string]any); ok {
					rawModels = append(rawModels, m)
				}
			}
		}
	}

	if len(rawModels) == 0 {
		PrintWarn("No models discovered or provider unreachable. Active model: " + sess.Model)
		return
	}

	fmt.Println()
	fmt.Printf("%s%sAvailable Models (%d discovered):%s\n", ColorBold, ColorAccent, len(rawModels), ColorReset)
	for _, m := range rawModels {
		id := fmt.Sprint(m["id"])
		name := fmt.Sprint(m["name"])
		prov := fmt.Sprint(m["provider"])
		desc := fmt.Sprint(m["desc"])

		var tags []string
		if t, _ := m["has_tools"].(bool); t {
			tags = append(tags, fmt.Sprintf("%s[tools]%s", ColorGreen, ColorReset))
		}
		if th, _ := m["has_thinking"].(bool); th {
			tags = append(tags, fmt.Sprintf("%s[thinking]%s", ColorMagenta, ColorReset))
		}
		if ef, _ := m["supports_effort"].(bool); ef {
			tags = append(tags, fmt.Sprintf("%s[effort]%s", ColorCyan, ColorReset))
		}

		activeMarker := "  "
		idColor := ColorCyanBright
		if id == sess.Model {
			activeMarker = "* "
			idColor = ColorGreenBright
		}

		tagStr := ""
		if len(tags) > 0 {
			tagStr = " " + strings.Join(tags, " ")
		}

		activeBadge := ""
		if id == sess.Model {
			activeBadge = fmt.Sprintf(" %s%s[ACTIVE]%s", ColorBold, ColorGreenBright, ColorReset)
		}

		fmt.Printf("%s%s%-32s%s %-12s %s%s%s\n", activeMarker, idColor, id, ColorReset, prov, name, tagStr, activeBadge)
		if desc != "" && desc != "<nil>" {
			fmt.Printf("     %s%s%s\n", ColorDim, desc, ColorReset)
		}
	}
	fmt.Println()
	PrintInfo("Use /model <model_id> to switch active model.")
}

func handleEffort(sess *CommandSession, args string) {
	val := strings.ToLower(strings.TrimSpace(args))
	if val == "" {
		PrintInfo(fmt.Sprintf("Current reasoning effort: %s", sess.ReasoningEffort))
		return
	}
	if val == "low" || val == "medium" || val == "med" || val == "high" {
		if val == "med" {
			val = "medium"
		}
		sess.ReasoningEffort = val
		PrintSuccess(fmt.Sprintf("Reasoning effort set to: %s", sess.ReasoningEffort))
	} else {
		PrintWarn("Usage: /effort [low|medium|high]")
	}
}

func handleMode(sess *CommandSession, args string) {
	val := strings.ToLower(strings.TrimSpace(args))
	if val == "" {
		PrintInfo(fmt.Sprintf("Current agent mode: %s", sess.AgentMode))
		return
	}

	var mode string
	switch val {
	case "autonomous", "auto":
		mode = "Autonomous"
	case "supervised", "super":
		mode = "Supervised"
	case "plan":
		mode = "Plan"
	default:
		PrintWarn("Usage: /mode [autonomous|supervised|plan]")
		return
	}

	if sess.HM != nil {
		sess.HM.SetAgentMode(mode)
	}
	sess.AgentMode = mode
	PrintSuccess(fmt.Sprintf("Agent mode set to: %s", mode))
}

func handleCaveman(sess *CommandSession, args string) {
	lower := strings.ToLower(strings.TrimSpace(args))
	if lower == "on" || lower == "true" || lower == "1" {
		sess.CavemanMode = true
	} else if lower == "off" || lower == "false" || lower == "0" {
		sess.CavemanMode = false
	} else if lower == "" {
		sess.CavemanMode = !sess.CavemanMode
	} else {
		PrintWarn("Usage: /caveman [on|off]")
		return
	}

	if sess.HM != nil {
		sess.HM.SetPromptModifiers(&sess.CavemanMode, nil)
	}
	if sess.CavemanMode {
		PrintSuccess("Caveman mode enabled (terse, direct output).")
	} else {
		PrintInfo("Caveman mode disabled.")
	}
}

func handlePonytail(sess *CommandSession, args string) {
	lower := strings.ToLower(strings.TrimSpace(args))
	if lower == "on" || lower == "true" || lower == "1" {
		sess.PonytailMode = true
	} else if lower == "off" || lower == "false" || lower == "0" {
		sess.PonytailMode = false
	} else if lower == "" {
		sess.PonytailMode = !sess.PonytailMode
	} else {
		PrintWarn("Usage: /ponytail [on|off]")
		return
	}

	if sess.HM != nil {
		sess.HM.SetPromptModifiers(nil, &sess.PonytailMode)
	}
	if sess.PonytailMode {
		PrintSuccess("Ponytail mode enabled.")
	} else {
		PrintInfo("Ponytail mode disabled.")
	}
}

func handleNewConv(sess *CommandSession, args string) {
	if sess.HM == nil {
		PrintWarn("HarnessManager is not initialized.")
		return
	}
	title := strings.TrimSpace(args)
	if title == "" {
		title = "New Conversation"
	}

	res := sess.HM.CreateConversation(title, sess.Workspace, sess.AgentMode)
	if res != nil {
		if id, ok := res["id"].(string); ok && id != "" {
			sess.ConvID = id
		}
	}
	PrintSuccess(fmt.Sprintf("Created new conversation: %s (%s)", title, sess.ConvID))
}

func handleConversations(sess *CommandSession) {
	if sess.HM == nil {
		PrintWarn("HarnessManager is not initialized.")
		return
	}
	state := sess.HM.GetFullStatePayload()
	convsRaw, _ := state["conversations"].([]any)
	if len(convsRaw) == 0 {
		PrintInfo("No conversations found.")
		return
	}

	fmt.Println()
	fmt.Printf("%s%sConversations (%d):%s\n", ColorBold, ColorAccent, len(convsRaw), ColorReset)
	for _, c := range convsRaw {
		cm, ok := c.(map[string]any)
		if !ok {
			continue
		}
		id := fmt.Sprint(cm["id"])
		title := fmt.Sprint(cm["title"])
		if title == "" || title == "<nil>" {
			title = "Untitled"
		}
		turnsCount := 0
		if turns, ok := cm["turns"].([]any); ok {
			turnsCount = len(turns)
		}

		activeMarker := "  "
		idColor := ColorCyanBright
		if id == sess.ConvID {
			activeMarker = "* "
			idColor = ColorGreenBright
		}

		activeBadge := ""
		if id == sess.ConvID {
			activeBadge = fmt.Sprintf(" %s%s[ACTIVE]%s", ColorBold, ColorGreenBright, ColorReset)
		}

		fmt.Printf("%s%s[%s]%s %s (%d turns)%s\n",
			activeMarker, idColor, id, ColorReset, title, turnsCount, activeBadge)
	}
	fmt.Println()
	PrintInfo("Switch conversation: /switch <conv_id>, New: /new [title]")
}

func handleSwitchConv(sess *CommandSession, args string) {
	targetID := strings.TrimSpace(args)
	if targetID == "" {
		PrintWarn("Usage: /switch <conv_id>")
		return
	}

	if sess.HM == nil {
		PrintWarn("HarnessManager is not initialized.")
		return
	}

	state := sess.HM.GetFullStatePayload()
	convsRaw, _ := state["conversations"].([]any)
	var foundID string
	var foundTitle string
	var foundWS string
	for _, c := range convsRaw {
		if cm, ok := c.(map[string]any); ok {
			id := fmt.Sprint(cm["id"])
			if id == targetID || strings.HasPrefix(id, targetID) {
				foundID = id
				foundTitle = fmt.Sprint(cm["title"])
				foundWS = fmt.Sprint(cm["workspace"])
				break
			}
		}
	}

	if foundID == "" {
		PrintError(fmt.Sprintf("No conversation matching '%s' found.", targetID))
		return
	}

	sess.ConvID = foundID
	if foundWS != "" && foundWS != "<nil>" {
		sess.Workspace = foundWS
		_, _ = sess.HM.SelectWorkspace(foundWS)
	}
	PrintSuccess(fmt.Sprintf("Switched to conversation: %s (%s)", foundTitle, foundID))
}

func handleClearConv(sess *CommandSession) {
	if sess.HM == nil {
		PrintWarn("HarnessManager is not initialized.")
		return
	}
	sess.HM.ClearConversation(sess.ConvID)
	PrintSuccess(fmt.Sprintf("Conversation '%s' turns cleared.", sess.ConvID))
}

func handleDeleteConv(sess *CommandSession, args string) {
	if sess.HM == nil {
		PrintWarn("HarnessManager is not initialized.")
		return
	}
	targetID := strings.TrimSpace(args)
	if targetID == "" {
		targetID = sess.ConvID
	} else {
		state := sess.HM.GetFullStatePayload()
		if convs, ok := state["conversations"].([]any); ok {
			for _, c := range convs {
				if cm, ok := c.(map[string]any); ok {
					id := fmt.Sprint(cm["id"])
					if id == targetID || strings.HasPrefix(id, targetID) {
						targetID = id
						break
					}
				}
			}
		}
	}

	sess.HM.DeleteConversation(targetID)
	PrintSuccess(fmt.Sprintf("Conversation '%s' deleted.", targetID))

	if targetID == sess.ConvID {
		sess.ConvID = sess.HM.ActiveConversationID()
		if sess.ConvID == "" {
			newConv := sess.HM.CreateConversation("New Conversation", sess.Workspace, sess.AgentMode)
			if newConv != nil {
				if id, ok := newConv["id"].(string); ok && id != "" {
					sess.ConvID = id
				}
			}
		}
		PrintInfo(fmt.Sprintf("Active conversation is now: %s", sess.ConvID))
	}
}

func handleCompact(sess *CommandSession) {
	if sess.HM == nil {
		PrintWarn("HarnessManager is not initialized.")
		return
	}
	PrintInfo("Compacting conversation context...")
	res, err := sess.HM.CompactConversation(sess.ConvID)
	if err != nil {
		PrintError(fmt.Sprintf("Context compaction failed: %v", err))
		return
	}
	msg := "Conversation context compacted successfully."
	if summary, ok := res["summary"].(string); ok && summary != "" {
		msg = fmt.Sprintf("Conversation context compacted. Summary:\n%s", summary)
	}
	PrintSuccess(msg)
}

func handleUndo(sess *CommandSession, args string) {
	if sess.HM == nil {
		PrintWarn("HarnessManager is not initialized.")
		return
	}

	turnIdx := -1
	if strings.TrimSpace(args) != "" {
		if idx, err := strconv.Atoi(strings.TrimSpace(args)); err == nil {
			turnIdx = idx
		} else {
			PrintWarn("Usage: /undo [turn_index]")
			return
		}
	} else {
		state := sess.HM.GetFullStatePayload()
		if convs, ok := state["conversations"].([]any); ok {
			for _, c := range convs {
				if cm, ok := c.(map[string]any); ok && fmt.Sprint(cm["id"]) == sess.ConvID {
					if turns, ok := cm["turns"].([]any); ok {
						if len(turns) > 0 {
							turnIdx = len(turns) - 1
						}
					}
					break
				}
			}
		}
		if turnIdx == -1 {
			PrintWarn("Conversation has no turns to undo.")
			return
		}
	}

	res, err := sess.HM.UndoToTurn(sess.ConvID, "", turnIdx)
	if err != nil {
		PrintError(fmt.Sprintf("Undo failed: %v", err))
		return
	}

	turnsRemoved := res["turns_removed"]
	PrintSuccess(fmt.Sprintf("Undo completed. Turns removed: %v", turnsRemoved))

	if restored, ok := res["restored_files"].([]string); ok && len(restored) > 0 {
		fmt.Printf("%sRestored files:%s\n", ColorGreen, ColorReset)
		for _, f := range restored {
			fmt.Printf("  + %s\n", f)
		}
	}
	if deleted, ok := res["deleted_files"].([]string); ok && len(deleted) > 0 {
		fmt.Printf("%sDeleted undo artifacts:%s\n", ColorRed, ColorReset)
		for _, f := range deleted {
			fmt.Printf("  - %s\n", f)
		}
	}
}

func handleRevert(sess *CommandSession, args string) {
	parts := strings.Fields(args)
	if len(parts) == 0 {
		PrintWarn("Usage: /revert <file_path> [hunk_index]")
		return
	}
	filePath := parts[0]
	hunkIdx := 0
	if len(parts) > 1 {
		if idx, err := strconv.Atoi(parts[1]); err == nil {
			hunkIdx = idx
		} else {
			PrintWarn("Invalid hunk index. Usage: /revert <file_path> [hunk_index]")
			return
		}
	}

	out, err := worktree.RevertHunkInFile(sess.Workspace, filePath, hunkIdx, "")
	if err != nil {
		PrintError(fmt.Sprintf("Revert failed: %v", err))
	} else {
		PrintSuccess(fmt.Sprintf("Reverted hunk %d in %s:\n%s", hunkIdx, filePath, out))
	}
}

func handleWorkspace(sess *CommandSession, args string) {
	target := strings.TrimSpace(args)
	if target == "" {
		PrintInfo(fmt.Sprintf("Active workspace: %s", sess.Workspace))
		if sess.HM != nil {
			workspaces := sess.HM.ListWorkspaces()
			if len(workspaces) > 0 {
				fmt.Println()
				fmt.Printf("%s%sRecent Workspaces (%d):%s\n", ColorBold, ColorAccent, len(workspaces), ColorReset)
				for _, w := range workspaces {
					wPath := fmt.Sprint(w["path"])
					wName := fmt.Sprint(w["name"])
					marker := "  "
					if wPath == sess.Workspace {
						marker = "* "
					}
					fmt.Printf("%s%-35s (%s)\n", marker, wPath, wName)
				}
				fmt.Println()
			}
		}
		PrintInfo("Switch workspace: /workspace <path>")
		return
	}

	cleanPath := filepath.Clean(target)
	if sess.HM != nil {
		res, err := sess.HM.SelectWorkspace(cleanPath)
		if err != nil {
			PrintError(fmt.Sprintf("Failed to select workspace '%s': %v", cleanPath, err))
			return
		}
		if actual, ok := res["workspace"].(string); ok && actual != "" {
			sess.Workspace = actual
		} else {
			sess.Workspace = cleanPath
		}
	} else {
		sess.Workspace = cleanPath
	}
	PrintSuccess(fmt.Sprintf("Workspace switched to: %s", sess.Workspace))
}

func handleFiles(sess *CommandSession, args string) {
	filter := strings.ToLower(strings.TrimSpace(args))
	files := workspace.ListWorkspaceFiles(sess.Workspace, 200)

	fmt.Println()
	fmt.Printf("%s%sWorkspace Files (%s):%s\n", ColorBold, ColorAccent, sess.Workspace, ColorReset)
	count := 0
	for _, f := range files {
		if filter != "" && !strings.Contains(strings.ToLower(f.Path), filter) && !strings.Contains(strings.ToLower(f.Name), filter) {
			continue
		}
		count++
		if f.IsDir {
			fmt.Printf("  %s%s%s/%s\n", ColorBold, ColorCyanBright, f.Path, ColorReset)
		} else {
			fmt.Printf("  %-45s %s(%d bytes)%s\n", f.Path, ColorDim, f.Size, ColorReset)
		}
	}
	if count == 0 {
		PrintInfo("No files matching filter found.")
	} else {
		fmt.Printf("\n%sTotal: %d file(s)%s\n", ColorDim, count, ColorReset)
	}
	fmt.Println()
}

func handleGit(sess *CommandSession) {
	gitInfo := workspace.GetGitInfo(sess.Workspace)
	fmt.Println()
	fmt.Printf("%s%sGit Status (%s):%s\n", ColorBold, ColorAccent, sess.Workspace, ColorReset)
	branch := gitInfo.Branch
	if branch == "" {
		branch = "(none / detached)"
	}
	fmt.Printf("  Branch:        %s%s%s\n", ColorCyanBright, branch, ColorReset)
	fmt.Printf("  Dirty:         %t\n", gitInfo.IsDirty)
	fmt.Printf("  Changed Files: %d\n", gitInfo.ChangedCount)

	cmd := exec.Command("git", "status", "-s")
	cmd.Dir = sess.Workspace
	if out, err := cmd.Output(); err == nil && len(out) > 0 {
		fmt.Printf("\n%sPorcelain Status:%s\n%s", ColorDim, ColorReset, string(out))
	}
	fmt.Println()
}

func handleWorkflows(sess *CommandSession, args string) (handled bool, prompt string) {
	if sess.HM == nil {
		PrintWarn("HarnessManager is not initialized.")
		return true, ""
	}

	parts := strings.Fields(args)
	sub := ""
	if len(parts) > 0 {
		sub = strings.ToLower(parts[0])
	}

	if sub == "" || sub == "list" {
		wfs := sess.HM.ListWorkflows()
		if len(wfs) == 0 {
			PrintInfo("No saved workflows found.")
			return true, ""
		}
		fmt.Println()
		fmt.Printf("%s%sSaved Workflows (%d):%s\n", ColorBold, ColorAccent, len(wfs), ColorReset)
		for _, wf := range wfs {
			id := fmt.Sprint(wf["id"])
			title := fmt.Sprint(wf["title"])
			if title == "" || title == "<nil>" {
				title = fmt.Sprint(wf["name"])
			}
			cat := fmt.Sprint(wf["category"])
			desc := fmt.Sprint(wf["description"])
			promptText := fmt.Sprint(wf["prompt"])

			fmt.Printf("  %s%s[%s]%s %s %s(%s)%s\n", ColorBold, ColorCyanBright, id, ColorReset, title, ColorDim, cat, ColorReset)
			if desc != "" && desc != "<nil>" {
				fmt.Printf("     %s\n", desc)
			}
			if promptText != "" && promptText != "<nil>" {
				preview := promptText
				if len(preview) > 100 {
					preview = preview[:97] + "..."
				}
				fmt.Printf("     %sPrompt: %s%s\n", ColorDim, preview, ColorReset)
			}
		}
		fmt.Println()
		PrintInfo("Run with: /workflow run <id>")
		return true, ""
	}

	if sub == "run" {
		if len(parts) < 2 {
			PrintWarn("Usage: /workflow run <id>")
			return true, ""
		}
		targetID := parts[1]
		wfs := sess.HM.ListWorkflows()
		for _, wf := range wfs {
			id := fmt.Sprint(wf["id"])
			if id == targetID || strings.HasPrefix(id, targetID) {
				promptText := fmt.Sprint(wf["prompt"])
				sess.WorkflowID = id
				title := fmt.Sprint(wf["title"])
				if title == "" || title == "<nil>" {
					title = fmt.Sprint(wf["name"])
				}
				PrintSuccess(fmt.Sprintf("Loaded workflow: %s (%s)", title, id))
				return true, promptText
			}
		}
		PrintError(fmt.Sprintf("Workflow '%s' not found.", targetID))
		return true, ""
	}

	if sub == "delete" || sub == "remove" {
		if len(parts) < 2 {
			PrintWarn("Usage: /workflow delete <id>")
			return true, ""
		}
		targetID := parts[1]
		sess.HM.DeleteWorkflow(targetID)
		PrintSuccess(fmt.Sprintf("Workflow '%s' deleted.", targetID))
		return true, ""
	}

	PrintWarn("Usage: /workflow [run <id>|delete <id>] or /workflows")
	return true, ""
}

func handleAgents(ctx context.Context, sess *CommandSession, args string) {
	if sess.HM == nil {
		PrintWarn("HarnessManager is not initialized.")
		return
	}

	parts := strings.Fields(args)
	sub := ""
	if len(parts) > 0 {
		sub = strings.ToLower(parts[0])
	}

	if sub == "" || sub == "list" {
		agents := sess.HM.ListAgents()
		if len(agents) == 0 {
			PrintInfo("No custom agents configured.")
			return
		}
		fmt.Println()
		fmt.Printf("%s%sCustom Autonomous Agents (%d):%s\n", ColorBold, ColorAccent, len(agents), ColorReset)
		for _, ag := range agents {
			id := fmt.Sprint(ag["id"])
			name := fmt.Sprint(ag["name"])
			role := fmt.Sprint(ag["role"])
			desc := fmt.Sprint(ag["description"])
			model := fmt.Sprint(ag["model"])
			toolsCount := 0
			if tls, ok := ag["tools"].([]any); ok {
				toolsCount = len(tls)
			}

			fmt.Printf("  %s%s[%s]%s %s %s(%s, tools: %d, model: %s)%s\n",
				ColorBold, ColorCyanBright, id, ColorReset, name, ColorDim, role, toolsCount, model, ColorReset)
			if desc != "" && desc != "<nil>" {
				fmt.Printf("     %s\n", desc)
			}
		}
		fmt.Println()
		PrintInfo("Run an agent: /agent run <id> <task prompt>")
		return
	}

	if sub == "run" {
		if len(parts) < 3 {
			PrintWarn("Usage: /agent run <id> <task prompt>")
			return
		}
		targetID := parts[1]
		taskPrompt := strings.TrimSpace(strings.Join(parts[2:], " "))
		PrintInfo(fmt.Sprintf("Executing custom agent '%s' on task...", targetID))

		res, err := sess.HM.RunSubagentTask(ctx, targetID, taskPrompt, sess.Model, sess.Workspace)
		if err != nil {
			PrintError(fmt.Sprintf("Agent execution failed: %v", err))
			return
		}
		PrintSuccess(fmt.Sprintf("Agent '%s' task completed successfully.", targetID))
		if text, ok := res["result"].(string); ok && text != "" {
			fmt.Println(RenderMarkdown(text))
		} else if out, ok := res["output"].(string); ok && out != "" {
			fmt.Println(RenderMarkdown(out))
		} else {
			fmt.Printf("%+v\n", res)
		}
		return
	}

	if sub == "delete" || sub == "remove" {
		if len(parts) < 2 {
			PrintWarn("Usage: /agent delete <id>")
			return
		}
		targetID := parts[1]
		sess.HM.DeleteAgent(targetID)
		PrintSuccess(fmt.Sprintf("Custom agent '%s' deleted.", targetID))
		return
	}

	PrintWarn("Usage: /agent [run <id> <task>|delete <id>] or /agents")
}

func handleSubagents(ctx context.Context, sess *CommandSession, cmdName, args string) {
	if sess.HM == nil {
		PrintWarn("HarnessManager is not initialized.")
		return
	}

	if cmdName == "spawn" {
		if strings.TrimSpace(args) == "" {
			PrintWarn("Usage: /spawn <role>: <prompt> (e.g. /spawn Architect: Review database schema)")
			return
		}
		role := "Generalist"
		promptText := args
		if idx := strings.IndexByte(args, ':'); idx != -1 {
			role = strings.TrimSpace(args[:idx])
			promptText = strings.TrimSpace(args[idx+1:])
		}
		specs := []map[string]any{
			{
				"role":   role,
				"prompt": promptText,
			},
		}
		runs := sess.HM.SpawnSubagents(ctx, sess.ConvID, sess.Workspace, specs, false)
		if len(runs) == 0 {
			PrintError("Failed to spawn subagent.")
			return
		}
		for _, r := range runs {
			PrintSuccess(fmt.Sprintf("Spawned parallel subagent [%s] (role: %s)", r.ID, r.Role))
		}
		return
	}

	parts := strings.Fields(args)
	sub := ""
	if len(parts) > 0 {
		sub = strings.ToLower(parts[0])
	}

	if sub == "" || sub == "list" {
		runs := sess.HM.ListSubagentRuns(sess.ConvID)
		if len(runs) == 0 {
			PrintInfo("No subagents running or recorded for this session.")
			return
		}
		fmt.Println()
		fmt.Printf("%s%sParallel Subagents (%d):%s\n", ColorBold, ColorAccent, len(runs), ColorReset)
		for _, r := range runs {
			id := fmt.Sprint(r["id"])
			role := fmt.Sprint(r["role"])
			status := fmt.Sprint(r["status"])
			turns := 0
			if t, ok := r["turns"].([]any); ok {
				turns = len(t)
			}
			task := fmt.Sprint(r["task"])
			if len(task) > 60 {
				task = task[:57] + "..."
			}

			statusColor := ColorCyan
			switch status {
			case "completed":
				statusColor = ColorGreenBright
			case "aborted":
				statusColor = ColorYellow
			case "error":
				statusColor = ColorRedBright
			case "running":
				statusColor = ColorAccent
			}

			fmt.Printf("  %s%s[%s]%s %s%-14s%s %s%s%-10s%s (%d turns) %s\n",
				ColorBold, ColorCyanBright, id, ColorReset,
				ColorBold, role, ColorReset,
				statusColor, ColorBold, status, ColorReset,
				turns, task,
			)
		}
		fmt.Println()
		PrintInfo("Subagent commands: /subagent msg <id> <msg>, /subagent abort <id>")
		return
	}

	if sub == "msg" || sub == "message" {
		if len(parts) < 3 {
			PrintWarn("Usage: /subagent msg <id> <message>")
			return
		}
		targetID := parts[1]
		msgText := strings.TrimSpace(strings.Join(parts[2:], " "))
		PrintInfo(fmt.Sprintf("Sending message to subagent '%s'...", targetID))
		res, err := sess.HM.SendSubagentMessage(ctx, targetID, msgText, sess.Model)
		if err != nil {
			PrintError(fmt.Sprintf("Message delivery failed: %v", err))
			return
		}
		PrintSuccess(fmt.Sprintf("Subagent responded: %v", res["status"]))
		if out, ok := res["output"].(string); ok && out != "" {
			fmt.Println(RenderMarkdown(out))
		}
		return
	}

	if sub == "abort" || sub == "stop" {
		if len(parts) < 2 {
			PrintWarn("Usage: /subagent abort <id>")
			return
		}
		targetID := parts[1]
		res := sess.HM.AbortSubagentRun(targetID)
		PrintSuccess(fmt.Sprintf("Subagent '%s' abort status: %v", targetID, res["status"]))
		return
	}

	PrintWarn("Usage: /subagents, /spawn <role>: <prompt>, /subagent msg <id> <msg>, or /subagent abort <id>")
}

func handleMCP(sess *CommandSession, args string) {
	parts := strings.Fields(args)
	sub := ""
	if len(parts) > 0 {
		sub = strings.ToLower(parts[0])
	}

	if sub == "" || sub == "list" {
		servers, err := mcp.ListMCPServers()
		if err != nil {
			PrintError(fmt.Sprintf("Failed to list MCP servers: %v", err))
			return
		}
		if len(servers) == 0 {
			PrintInfo("No MCP servers configured.")
			return
		}
		fmt.Println()
		fmt.Printf("%s%sConfigured MCP Servers (%d):%s\n", ColorBold, ColorAccent, len(servers), ColorReset)
		for _, s := range servers {
			statusStr := fmt.Sprintf("%s[disabled]%s", ColorDim, ColorReset)
			if s.Enabled {
				statusStr = fmt.Sprintf("%s[enabled]%s", ColorGreenBright, ColorReset)
			}
			cmdLine := s.Command + " " + strings.Join(s.Args, " ")
			fmt.Printf("  %s%s%-16s%s %s %s\n", ColorBold, ColorCyanBright, s.Name, ColorReset, statusStr, cmdLine)
			if s.Description != "" {
				fmt.Printf("     %s%s%s\n", ColorDim, s.Description, ColorReset)
			}
		}
		fmt.Println()
		PrintInfo("Commands: /mcp presets, /mcp add <name> <cmd> [args...], /mcp remove <name>, /mcp refresh")
		return
	}

	if sub == "presets" {
		fmt.Println()
		fmt.Printf("%s%sBuilt-in MCP Presets (%d):%s\n", ColorBold, ColorAccent, len(mcp.MCPPresets), ColorReset)
		for _, p := range mcp.MCPPresets {
			cmdLine := p.Command + " " + strings.Join(p.Args, " ")
			fmt.Printf("  %s%s%-14s%s %s\n", ColorBold, ColorCyanBright, p.Name, ColorReset, cmdLine)
			if p.Description != "" {
				fmt.Printf("     %s%s%s\n", ColorDim, p.Description, ColorReset)
			}
		}
		fmt.Println()
		return
	}

	if sub == "add" {
		if len(parts) < 3 {
			PrintWarn("Usage: /mcp add <name> <command> [args...]")
			return
		}
		name := parts[1]
		command := parts[2]
		cmdArgs := parts[3:]
		cfg := mcp.ServerConfig{
			Name:    name,
			Command: command,
			Args:    cmdArgs,
			Enabled: true,
			Env:     make(map[string]string),
		}
		if err := mcp.SaveMCPServer(cfg); err != nil {
			PrintError(fmt.Sprintf("Failed to save MCP server: %v", err))
		} else {
			PrintSuccess(fmt.Sprintf("MCP server '%s' added.", name))
		}
		return
	}

	if sub == "remove" || sub == "delete" {
		if len(parts) < 2 {
			PrintWarn("Usage: /mcp remove <name>")
			return
		}
		name := parts[1]
		if err := mcp.DeleteMCPServer(name); err != nil {
			PrintError(fmt.Sprintf("Failed to delete MCP server: %v", err))
		} else {
			PrintSuccess(fmt.Sprintf("MCP server '%s' removed.", name))
		}
		return
	}

	if sub == "refresh" {
		PrintInfo("Refreshing connected MCP servers and tools...")
		res := mcp.RefreshMCPTools()
		PrintSuccess(fmt.Sprintf("MCP refresh complete: %v", res["status"]))
		if toolsList, ok := res["tools"].([]any); ok {
			fmt.Printf("  Discovered %d tools from connected MCP servers.\n", len(toolsList))
		}
		return
	}

	PrintWarn("Usage: /mcp [list|presets|add <name> <command> [args...]|remove <name>|refresh]")
}

func handleSkills(sess *CommandSession, args string) {
	parts := strings.Fields(args)
	sub := ""
	if len(parts) > 0 {
		sub = strings.ToLower(parts[0])
	}

	if sub == "" || sub == "list" {
		sks := skills.DiscoverSkills(sess.Workspace)
		if len(sks) == 0 {
			PrintInfo("No skills found.")
			return
		}
		fmt.Println()
		fmt.Printf("%s%sDiscovered Skills (%d):%s\n", ColorBold, ColorAccent, len(sks), ColorReset)
		for _, s := range sks {
			triggers := strings.Join(s.Triggers, ", ")
			if triggers != "" {
				triggers = fmt.Sprintf(" [triggers: %s]", triggers)
			}
			fmt.Printf("  %s%s%-20s%s %s(%s)%s%s\n", ColorBold, ColorCyanBright, s.Name, ColorReset, ColorDim, s.Scope, ColorReset, triggers)
			if s.Description != "" {
				fmt.Printf("     %s%s%s\n", ColorDim, s.Description, ColorReset)
			}
		}
		fmt.Println()
		PrintInfo("Commands: /skills install <github_url>, /skills delete <name>")
		return
	}

	if sub == "install" {
		if len(parts) < 2 {
			PrintWarn("Usage: /skills install <github_url>")
			return
		}
		url := parts[1]
		PrintInfo(fmt.Sprintf("Installing skill from %s...", url))
		res, err := skills.InstallFromGitHub(url)
		if err != nil {
			PrintError(fmt.Sprintf("Skill installation failed: %v", err))
		} else {
			PrintSuccess(fmt.Sprintf("Skill installed successfully: %v", res["name"]))
		}
		return
	}

	if sub == "delete" || sub == "remove" {
		if len(parts) < 2 {
			PrintWarn("Usage: /skills delete <name>")
			return
		}
		name := parts[1]
		paths, err := skills.DeleteSkill(name, "", sess.Workspace)
		if err != nil {
			PrintError(fmt.Sprintf("Failed to delete skill '%s': %v", name, err))
		} else {
			PrintSuccess(fmt.Sprintf("Skill '%s' deleted (%d path(s) removed).", name, len(paths)))
		}
		return
	}

	PrintWarn("Usage: /skills [list|install <github_url>|delete <name>]")
}

func handleMemory(sess *CommandSession, args string) {
	trimmed := strings.TrimSpace(args)
	parts := strings.Fields(trimmed)
	sub := ""
	if len(parts) > 0 {
		sub = strings.ToLower(parts[0])
	}

	if sub == "save" {
		if len(parts) < 3 {
			PrintWarn("Usage: /memory save <key> <content>")
			return
		}
		key := parts[1]
		content := strings.TrimSpace(strings.Join(parts[2:], " "))
		entry, err := memory.SaveMemory(key, "general", content, sess.Workspace, nil)
		if err != nil {
			PrintError(fmt.Sprintf("Failed to save memory: %v", err))
		} else {
			PrintSuccess(fmt.Sprintf("Memory saved: [%s] (%s)", entry.Key, entry.ID))
		}
		return
	}

	if sub == "delete" || sub == "remove" {
		if len(parts) < 2 {
			PrintWarn("Usage: /memory delete <key>")
			return
		}
		key := parts[1]
		deleted, err := memory.DeleteMemory(key)
		if err != nil {
			PrintError(fmt.Sprintf("Failed to delete memory: %v", err))
		} else if deleted {
			PrintSuccess(fmt.Sprintf("Memory '%s' deleted.", key))
		} else {
			PrintWarn(fmt.Sprintf("Memory '%s' not found.", key))
		}
		return
	}

	var entries []memory.MemoryEntry
	if trimmed == "" || sub == "list" {
		all, err := memory.LoadMemories()
		if err != nil {
			PrintError(fmt.Sprintf("Failed to load memories: %v", err))
			return
		}
		entries = all
	} else {
		entries = memory.SearchMemories(trimmed, "", sess.Workspace)
	}

	if len(entries) == 0 {
		PrintInfo("No memories found matching criteria.")
		return
	}

	fmt.Println()
	fmt.Printf("%s%sPersistent Cross-Session Memories (%d):%s\n", ColorBold, ColorAccent, len(entries), ColorReset)
	for _, m := range entries {
		wsTag := ""
		if m.Workspace != "" {
			wsTag = fmt.Sprintf(" %s[ws: %s]%s", ColorDim, filepath.Base(m.Workspace), ColorReset)
		}
		fmt.Printf("  %s%s[%s]%s %s(%s)%s%s\n", ColorBold, ColorCyanBright, m.Key, ColorReset, ColorDim, m.Category, ColorReset, wsTag)
		preview := m.Content
		if len(preview) > 120 {
			preview = preview[:117] + "..."
		}
		fmt.Printf("     %s\n", preview)
	}
	fmt.Println()
	PrintInfo("Commands: /memory save <key> <content>, /memory delete <key>, /memory <query>")
}

func handleSecurity(ctx context.Context, sess *CommandSession, args string) {
	targetURL := strings.TrimSpace(args)
	PrintInfo("Initiating The Duel: Red-Team vs White-Team Security Arena...")
	if targetURL != "" {
		fmt.Printf("  Target URL: %s%s%s\n", ColorCyanBright, targetURL, ColorReset)
	}
	fmt.Printf("  Workspace:  %s%s%s\n\n", ColorCyanBright, sess.Workspace, ColorReset)

	report, err := security.RunSecurityDuel(ctx, sess.Workspace, targetURL)
	if err != nil {
		PrintError(fmt.Sprintf("Security Duel failed: %v", err))
		return
	}

	fmt.Println()
	fmt.Printf("%s%sSecurity Duel Results:%s\n", ColorBold, ColorAccent, ColorReset)
	fmt.Printf("  Attack Surfaces Found: %d\n", report.AttackSurfaceCount)
	fmt.Printf("  Taint Findings:        %d\n", len(report.TaintFindings))
	fmt.Printf("  Supply Chain Findings: %d\n", len(report.SupplyChainFindings))
	fmt.Printf("  Red Team PoC Exploits: %d\n", len(report.RedTeamExploits))
	fmt.Printf("  White Team Patches:    %d\n", len(report.WhiteTeamPatches))
	fmt.Printf("  Regression Tests:      %d\n", report.RegressionTestsAdded)
	fmt.Printf("  Verified Fixed:        %s%d%s\n\n", ColorGreenBright, report.VerifiedFixed, ColorReset)

	if report.SummaryMarkdown != "" {
		fmt.Println(RenderMarkdown(report.SummaryMarkdown))
	}
}

func handleWatchdog(ctx context.Context, sess *CommandSession) {
	PrintInfo(fmt.Sprintf("Running workspace health watchdog check on %s...", sess.Workspace))
	status := frontier.CheckWorkspaceHealth(ctx, sess.Workspace)

	fmt.Println()
	if status.Healthy {
		PrintSuccess(fmt.Sprintf("Workspace is HEALTHY (%s)", status.Workspace))
	} else {
		PrintWarn(fmt.Sprintf("Workspace health check detected %d issue(s)!", len(status.Issues)))
	}

	if status.Summary != "" {
		fmt.Printf("  %sSummary:%s %s\n", ColorBold, ColorReset, status.Summary)
	}

	if len(status.Issues) > 0 {
		fmt.Printf("\n%sDetected Issues:%s\n", ColorYellow, ColorReset)
		for _, issue := range status.Issues {
			sevColor := ColorYellow
			if strings.ToLower(issue.Severity) == "error" {
				sevColor = ColorRedBright
			}
			fmt.Printf("  [%s%s%s] %s: %s\n", sevColor, issue.Severity, ColorReset, issue.File, issue.Message)
		}
	}

	if status.SuggestedPrompt != "" {
		fmt.Printf("\n%sSuggested Fix Prompt:%s\n%s\n", ColorCyanBright, ColorReset, status.SuggestedPrompt)
	}
	fmt.Println()
}

func handleTools(sess *CommandSession, args string) {
	parts := strings.Fields(args)
	sub := ""
	if len(parts) > 0 {
		sub = strings.ToLower(parts[0])
	}

	if sub == "delete" || sub == "remove" {
		if len(parts) < 2 {
			PrintWarn("Usage: /tools delete <name>")
			return
		}
		name := parts[1]
		if err := frontier.DeleteCustomTool(name); err != nil {
			PrintError(fmt.Sprintf("Failed to delete custom tool '%s': %v", name, err))
		} else {
			PrintSuccess(fmt.Sprintf("Custom tool '%s' deleted.", name))
		}
		return
	}

	toolsList, err := frontier.LoadCustomTools()
	if err != nil {
		PrintError(fmt.Sprintf("Failed to load custom tools: %v", err))
		return
	}
	if len(toolsList) == 0 {
		PrintInfo("No synthesized custom tools found.")
		return
	}

	fmt.Println()
	fmt.Printf("%s%sSynthesized Custom Tools (%d):%s\n", ColorBold, ColorAccent, len(toolsList), ColorReset)
	for _, t := range toolsList {
		fmt.Printf("  %s%s%-20s%s %s(%s)%s\n", ColorBold, ColorCyanBright, t.Name, ColorReset, ColorDim, t.Language, ColorReset)
		if t.Description != "" {
			fmt.Printf("     %s%s%s\n", ColorDim, t.Description, ColorReset)
		}
		if t.ScriptPath != "" {
			fmt.Printf("     %sScript: %s%s\n", ColorDim, t.ScriptPath, ColorReset)
		}
	}
	fmt.Println()
	PrintInfo("Delete a tool: /tools delete <name>")
}

func handleBrowser(ctx context.Context, sess *CommandSession, args string) {
	targetURL := strings.TrimSpace(args)
	if targetURL == "" {
		PrintWarn("Usage: /browser <url>")
		return
	}

	PrintInfo(fmt.Sprintf("Launching headless CDP browser navigation to: %s...", targetURL))
	opts := frontier.BrowserUseOptions{
		Action: "navigate",
		URL:    targetURL,
	}

	res, err := frontier.ExecuteBrowserUse(ctx, sess.Workspace, opts)
	if err != nil {
		PrintError(fmt.Sprintf("Browser action failed: %v", err))
		return
	}

	PrintSuccess(fmt.Sprintf("Navigation succeeded: %s", res.Title))
	fmt.Printf("  URL:                 %s\n", res.URL)
	fmt.Printf("  Interactive Elements:%d\n", len(res.Elements))
	if res.ActionSummary != "" {
		fmt.Printf("  Action Summary:      %s\n", res.ActionSummary)
	}
	if res.ScreenshotPath != "" {
		fmt.Printf("  Screenshot:          %s\n", res.ScreenshotPath)
	}

	if len(res.Elements) > 0 {
		fmt.Printf("\n%sInteractive Elements (sample):%s\n", ColorBold, ColorReset)
		maxElem := 10
		if len(res.Elements) < maxElem {
			maxElem = len(res.Elements)
		}
		for i := 0; i < maxElem; i++ {
			el := res.Elements[i]
			textPreview := el.Text
			if len(textPreview) > 40 {
				textPreview = textPreview[:37] + "..."
			}
			fmt.Printf("  [%d] <%s> %s\n", el.Index, el.Tag, textPreview)
		}
	}

	if res.PageText != "" {
		snippet := res.PageText
		if len(snippet) > 300 {
			snippet = snippet[:297] + "..."
		}
		fmt.Printf("\n%sPage Text Preview:%s\n%s\n", ColorDim, ColorReset, snippet)
	}
	fmt.Println()
}

func handleUsage(sess *CommandSession) {
	if sess.HM == nil {
		PrintWarn("HarnessManager is not initialized.")
		return
	}
	summary := sess.HM.GetUsageSummary(sess.ConvID)
	fmt.Println(RenderUsageSummary(summary))
}

func handleContext(sess *CommandSession) {
	if sess.HM == nil {
		PrintWarn("HarnessManager is not initialized.")
		return
	}
	usage := sess.HM.GetContextUsageWithModel(sess.ConvID, sess.Model)
	fmt.Println(RenderContextUsage(usage))
}

func handleSettings(sess *CommandSession, cmdName, args string) {
	if cmdName == "set" {
		parts := strings.Fields(args)
		if len(parts) < 2 {
			PrintWarn("Usage: /set <key> <value> (e.g. /set model gpt-5, /set temperature 0.2)")
			return
		}
		key := parts[0]
		valStr := strings.TrimSpace(strings.Join(parts[1:], " "))

		var val any = valStr
		if lower := strings.ToLower(valStr); lower == "true" {
			val = true
		} else if lower == "false" {
			val = false
		} else if i, err := strconv.Atoi(valStr); err == nil {
			val = i
		} else if f, err := strconv.ParseFloat(valStr, 64); err == nil && strings.Contains(valStr, ".") {
			val = f
		}

		if sess.Settings == nil {
			sess.Settings = config.NewDefaultSettings()
		}

		if err := config.ApplySettingsUpdate(sess.Settings, map[string]any{key: val}); err != nil {
			PrintError(fmt.Sprintf("Failed to update setting '%s': %v", key, err))
			return
		}

		_ = config.SaveConfig(sess.Settings)
		if sess.HM != nil {
			sess.HM.UpdateSettings(sess.Settings)
		}
		if key == "model" {
			sess.Model = valStr
		}
		PrintSuccess(fmt.Sprintf("Setting '%s' updated to: %v", key, val))
		return
	}

	if sess.Settings == nil {
		sess.Settings = config.NewDefaultSettings()
	}

	dict := config.SerializeSettingsDict(sess.Settings)
	keys := make([]string, 0, len(dict))
	for k := range dict {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	fmt.Println()
	fmt.Printf("%s%sCurrent Settings Configuration:%s\n", ColorBold, ColorAccent, ColorReset)
	for _, k := range keys {
		v := dict[k]
		vStr := fmt.Sprintf("%v", v)
		if strings.Contains(k, "key") || strings.Contains(k, "pin") || strings.Contains(k, "token") {
			if vStr != "" && vStr != "<nil>" && vStr != "[]" && vStr != "map[]" {
				vStr = "********"
			}
		}
		fmt.Printf("  %s%-28s%s = %v\n", ColorCyanBright, k, ColorReset, vStr)
	}
	fmt.Println()
	PrintInfo("Update a setting: /set <key> <value>")
}

func handleUpdate(sess *CommandSession, args string) {
	sub := strings.ToLower(strings.TrimSpace(args))
	url := ""
	autoEnabled := false
	if sess.Settings != nil {
		url = sess.Settings.UpdateServerURL
		autoEnabled = sess.Settings.AutoUpdateEnabled
	}

	if sub == "apply" {
		PrintInfo("Downloading and applying update...")
		res, err := updater.ApplyUpdate(url)
		if err != nil {
			PrintError(fmt.Sprintf("Update failed: %v", err))
			return
		}
		PrintSuccess(fmt.Sprintf("Update applied successfully! Result: %v", res["status"]))
		PrintInfo("Please restart Unjess to complete the update.")
		return
	}

	PrintInfo("Checking for application updates...")
	info := updater.CheckForUpdates(url, autoEnabled)
	curr := fmt.Sprint(info["current_version"])
	latest := fmt.Sprint(info["latest_version"])
	avail, _ := info["update_available"].(bool)

	fmt.Println()
	fmt.Printf("%s%sVersion & Update Status:%s\n", ColorBold, ColorAccent, ColorReset)
	fmt.Printf("  Current Version:  %s\n", curr)
	fmt.Printf("  Latest Version:   %s\n", latest)
	if avail {
		fmt.Printf("  Status:           %s%sUPDATE AVAILABLE%s\n", ColorBold, ColorGreenBright, ColorReset)
		if msg := fmt.Sprint(info["message"]); msg != "" && msg != "<nil>" {
			fmt.Printf("  Changelog:        %s\n", msg)
		}
		fmt.Println()
		PrintInfo("Apply update now: /update apply")
	} else {
		fmt.Printf("  Status:           %sUnjess is up to date.%s\n", ColorGreen, ColorReset)
		fmt.Println()
	}
}

func handleAttach(sess *CommandSession, isImage bool, args string) {
	filePath := strings.TrimSpace(args)
	if filePath == "" {
		if isImage {
			PrintWarn("Usage: /image <filepath>")
		} else {
			PrintWarn("Usage: /attach <filepath>")
		}
		return
	}

	if !filepath.IsAbs(filePath) && sess.Workspace != "" {
		filePath = filepath.Join(sess.Workspace, filePath)
	}

	if _, err := os.Stat(filePath); err != nil {
		PrintError(fmt.Sprintf("File not found: %s", filePath))
		return
	}

	ext := strings.ToLower(filepath.Ext(filePath))
	isImg := isImage || ext == ".png" || ext == ".jpg" || ext == ".jpeg" || ext == ".gif" || ext == ".webp" || ext == ".bmp"

	if isImg {
		img, err := LoadImageAttachment(filePath)
		if err != nil {
			PrintError(fmt.Sprintf("Failed to load image '%s': %v", filePath, err))
			return
		}
		sess.PendingImages = append(sess.PendingImages, img)
		PrintSuccess(fmt.Sprintf("Image attached: %s (Total pending images: %d)", filepath.Base(filePath), len(sess.PendingImages)))
	} else {
		att, err := LoadFileAttachment(filePath)
		if err != nil {
			PrintError(fmt.Sprintf("Failed to load file attachment '%s': %v", filePath, err))
			return
		}
		sess.PendingAttachments = append(sess.PendingAttachments, att)
		PrintSuccess(fmt.Sprintf("File attached: %s (Total pending attachments: %d)", filepath.Base(filePath), len(sess.PendingAttachments)))
	}
}

func handleExec(ctx context.Context, sess *CommandSession, cmd string) {
	cmdStr := strings.TrimSpace(cmd)
	if cmdStr == "" {
		PrintWarn("Usage: /exec <shell command>")
		return
	}

	fmt.Printf("%s$ %s%s\n", ColorDim, cmdStr, ColorReset)
	te := tools.NewToolExecutor(sess.Workspace, 120)
	out, success := te.Execute(ctx, "run_command", map[string]any{"command": cmdStr})
	if success {
		fmt.Println(out)
	} else {
		PrintError(out)
	}
}

func handleGUI(sess *CommandSession) {
	PrintInfo("Launching Unjess Desktop GUI...")
	if err := LaunchDesktopGUI(sess.Workspace); err != nil {
		PrintError(fmt.Sprintf("Failed to launch desktop GUI: %v", err))
	} else {
		PrintSuccess("Unjess Desktop GUI launched detached.")
	}
}

func handleSchedule(ctx context.Context, sess *CommandSession, args string) {
	parts := strings.Fields(args)
	sub := ""
	if len(parts) > 0 {
		sub = strings.ToLower(parts[0])
	}

	sch := scheduler.NewScheduler("", nil)

	switch sub {
	case "", "list":
		tasks := sch.ListTasks()
		if len(tasks) == 0 {
			PrintInfo("No scheduled tasks found in ~/.unjess/schedules.json")
			fmt.Printf("Create one with: /schedule add \"<cron/interval/time>\" <prompt>\nExample: /schedule add \"0 22 * * *\" Verify build and test suite\n")
			return
		}
		fmt.Println()
		fmt.Printf("%s%sPersistent Scheduled Tasks (%d):%s\n", ColorBold, ColorAccent, len(tasks), ColorReset)
		for _, t := range tasks {
			statusStr := fmt.Sprintf("%s[disabled]%s", ColorDim, ColorReset)
			if t.Enabled {
				statusStr = fmt.Sprintf("%s[enabled]%s", ColorGreenBright, ColorReset)
			}
			lastRun := "never"
			if t.LastRunAt != nil {
				lastRun = t.LastRunAt.Format("2006-01-02 15:04")
			}
			nextRun := "disabled"
			if t.NextRunAt != nil && t.Enabled {
				nextRun = t.NextRunAt.Format("2006-01-02 15:04")
			}
			fmt.Printf("  %s%s%-12s%s %s %s%-18s%s %s\n", ColorBold, ColorCyanBright, t.ID, ColorReset, statusStr, ColorYellow, t.CronExpression, ColorReset, t.Title)
			fmt.Printf("     %sAction: %s | Next: %s | Last: %s (%s)%s\n", ColorDim, t.Action, nextRun, lastRun, t.LastStatus, ColorReset)
			if t.Prompt != "" {
				promptPrev := t.Prompt
				if len(promptPrev) > 80 {
					promptPrev = promptPrev[:77] + "..."
				}
				fmt.Printf("     %sPrompt: %s%s\n", ColorDim, promptPrev, ColorReset)
			}
		}
		fmt.Println()
		PrintInfo("Commands: /schedule add <cron> <prompt>, /schedule toggle <id>, /schedule run <id>, /schedule delete <id>")

	case "add":
		rest := strings.TrimSpace(strings.TrimPrefix(args, parts[0]))
		if rest == "" {
			PrintError("Usage: /schedule add \"<cron/interval/time>\" <prompt>")
			return
		}
		var cronSpec string
		var prompt string
		if strings.HasPrefix(rest, "\"") {
			endQuote := strings.Index(rest[1:], "\"")
			if endQuote != -1 {
				cronSpec = rest[1 : endQuote+1]
				prompt = strings.TrimSpace(rest[endQuote+2:])
			}
		}
		if cronSpec == "" {
			tokens := strings.Fields(rest)
			if len(tokens) >= 2 && strings.ToLower(tokens[0]) == "every" {
				cronSpec = tokens[0] + " " + tokens[1]
				prompt = strings.TrimSpace(strings.TrimPrefix(rest, cronSpec))
			} else if len(tokens) >= 1 {
				cronSpec = tokens[0]
				prompt = strings.TrimSpace(strings.TrimPrefix(rest, cronSpec))
			}
		}
		if cronSpec == "" || prompt == "" {
			PrintError("Usage: /schedule add \"<cron/interval/time>\" <prompt>\nExample: /schedule add \"0 22 * * *\" Clean logs and verify build")
			return
		}

		newTask := scheduler.ScheduledTask{
			Title:          prompt,
			CronExpression: cronSpec,
			Action:         "prompt",
			Prompt:         prompt,
			Workspace:      sess.Workspace,
			Model:          sess.Model,
			Enabled:        true,
			NotifyOn:       "on_error",
		}
		added, err := sch.AddTask(newTask)
		if err != nil {
			PrintError(fmt.Sprintf("Failed to add scheduled task: %v", err))
			return
		}
		nextStr := "pending"
		if added.NextRunAt != nil {
			nextStr = added.NextRunAt.Format("2006-01-02 15:04:05")
		}
		PrintSuccess(fmt.Sprintf("Scheduled task '%s' added (ID: %s, Next run: %s)", added.Title, added.ID, nextStr))

	case "toggle":
		if len(parts) < 2 {
			PrintError("Usage: /schedule toggle <id>")
			return
		}
		id := parts[1]
		task, err := sch.ToggleTask(id)
		if err != nil {
			PrintError(fmt.Sprintf("Failed to toggle schedule: %v", err))
			return
		}
		stateStr := "disabled"
		if task.Enabled {
			stateStr = "enabled"
		}
		PrintSuccess(fmt.Sprintf("Schedule '%s' (%s) is now %s.", task.Title, task.ID, stateStr))

	case "delete", "remove", "rm":
		if len(parts) < 2 {
			PrintError("Usage: /schedule delete <id>")
			return
		}
		id := parts[1]
		if sch.DeleteTask(id) {
			PrintSuccess(fmt.Sprintf("Schedule '%s' deleted.", id))
		} else {
			PrintError(fmt.Sprintf("Schedule '%s' not found.", id))
		}

	case "run":
		if len(parts) < 2 {
			PrintError("Usage: /schedule run <id>")
			return
		}
		id := parts[1]
		task, exists := sch.GetTask(id)
		if !exists {
			PrintError(fmt.Sprintf("Schedule '%s' not found.", id))
			return
		}
		PrintInfo(fmt.Sprintf("Executing scheduled task '%s' now...", task.Title))
		execSch := scheduler.NewScheduler("", scheduler.TaskExecutorFunc(func(execCtx context.Context, t *scheduler.ScheduledTask) (string, error) {
			req := agent.ChatTurnRequest{
				Prompt:          t.Prompt,
				Workspace:       sess.Workspace,
				Model:           t.Model,
				ReasoningEffort: t.Effort,
			}
			res, err := sess.HM.RunAgentTurn(execCtx, req)
			if err != nil {
				return "", err
			}
			if turn, ok := res["turn"].(map[string]any); ok {
				if txt, ok := turn["text"].(string); ok {
					return txt, nil
				}
			}
			return "Success", nil
		}))
		resTask, err := execSch.RunTaskNow(ctx, id)
		if err != nil {
			PrintError(fmt.Sprintf("Task execution failed: %v", err))
		} else {
			PrintSuccess(fmt.Sprintf("Task '%s' completed successfully.", resTask.Title))
			if resTask.LastOutput != "" {
				fmt.Println(RenderMarkdown(resTask.LastOutput))
			}
		}

	default:
		PrintError(fmt.Sprintf("Unknown /schedule subcommand: %s. Use list, add, toggle, run, or delete.", sub))
	}
}

func handleTrace(sess *CommandSession, args string) {
	sub := strings.ToLower(strings.TrimSpace(args))
	if sub == "toggle" {
		sess.CompactTrace = !sess.CompactTrace
		state := "expanded (verbose)"
		if sess.CompactTrace {
			state = "compact (single-line summary)"
		}
		PrintSuccess(fmt.Sprintf("CLI execution trace mode set to: %s", state))
		return
	}

	if sess.LastTurn == nil {
		PrintInfo("No previous turn execution trace recorded.")
		return
	}

	fmt.Println()
	fmt.Printf("%s%sLast Turn Execution Trace Details:%s\n", ColorBold, ColorAccent, ColorReset)

	toolCalls := extractToolCalls(sess.LastTurn["toolCalls"])
	if len(toolCalls) == 0 {
		toolCalls = extractToolCalls(sess.LastTurn["tool_calls"])
	}
	if len(toolCalls) > 0 {
		fmt.Printf("\n%sTool Invocations (%d):%s\n", ColorBold, len(toolCalls), ColorReset)
		for _, tc := range toolCalls {
			if out := RenderToolCall(tc); out != "" {
				printLine("  " + out)
			}
		}
	} else {
		PrintInfo("No tool calls recorded in last turn.")
	}

	diffs := extractDiffs(sess.LastTurn["diffs"])
	if len(diffs) > 0 {
		fmt.Printf("\n%sFile Modifications (%d):%s\n", ColorBold, len(diffs), ColorReset)
		for _, diff := range diffs {
			if dOut := RenderDiff(diff); dOut != "" {
				printLine(dOut)
			}
		}
	}

	thinking, _ := sess.LastTurn["thinking"].(string)
	if strings.TrimSpace(thinking) != "" {
		fmt.Printf("\n%sThinking / Reasoning Trace:%s\n", ColorBold, ColorReset)
		fmt.Println(ColorDim + ColorItalic + strings.TrimSpace(thinking) + ColorReset)
	}
	fmt.Println()
}
