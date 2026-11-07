// Package main implements the Unjess CLI binary entrypoint (unjess-cli).
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"unjess-daemon/internal/agent"
	"unjess-daemon/internal/cli"
	"unjess-daemon/internal/config"
	"unjess-daemon/internal/skills"
	"unjess-daemon/internal/updater"
)

func main() {
	// 1. Enable Windows VT100 / Virtual Terminal Processing early
	cli.EnableVirtualTerminal()

	// 2. Define command-line flags
	var (
		promptFlag   string
		modelFlag    string
		providerFlag string
		yesFlag      bool
		verboseFlag  bool
		configFlag   string
		modeFlag     string
		effortFlag   string
		wsFlag       string
		convFlag     string
		cavemanFlag  bool
		ponytailFlag bool
		workflowFlag string
		agentFlag    string
		imageFlag    string
		fileFlag     string
		guiFlag      bool
		versionFlag  bool
		statusFlag   bool
		modelsFlag   bool
		updateFlag   bool
		duelFlag     bool
		watchdogFlag bool
	)

	flag.StringVar(&promptFlag, "prompt", "", "Run a single prompt non-interactively (one-shot mode) and exit")
	flag.StringVar(&modelFlag, "model", "", "Override active LLM model")
	flag.StringVar(&modelFlag, "m", "", "Override active LLM model (shorthand)")
	flag.StringVar(&providerFlag, "provider", "", "Model provider (e.g. ollama, openai)")
	flag.StringVar(&providerFlag, "p", "", "Model provider (shorthand)")
	flag.BoolVar(&yesFlag, "yes", false, "Auto-confirm all tool executions (autonomous mode)")
	flag.BoolVar(&yesFlag, "y", false, "Auto-confirm all tool executions (shorthand)")
	flag.BoolVar(&verboseFlag, "verbose", false, "Enable verbose output")
	flag.StringVar(&configFlag, "config", "", "Path to custom configuration file")

	flag.StringVar(&modeFlag, "mode", "", "Agent mode (autonomous, supervised, plan)")
	flag.StringVar(&effortFlag, "effort", "", "Reasoning effort (low, medium, high)")

	flag.StringVar(&wsFlag, "workspace", "", "Workspace directory path (defaults to current working directory)")
	flag.StringVar(&wsFlag, "w", "", "Workspace directory path (shorthand)")

	flag.StringVar(&convFlag, "conv", "", "Resume specific conversation ID")
	flag.StringVar(&convFlag, "c", "", "Resume specific conversation ID (shorthand)")

	flag.BoolVar(&cavemanFlag, "caveman", false, "Enable Caveman ultra-terse mode")
	flag.BoolVar(&ponytailFlag, "ponytail", false, "Enable Ponytail deep-focus mode")

	flag.StringVar(&workflowFlag, "workflow", "", "Run a specific workflow ID")
	flag.StringVar(&agentFlag, "agent", "", "Run with a specific custom agent ID")

	flag.StringVar(&imageFlag, "image", "", "Attach an image file path")
	flag.StringVar(&imageFlag, "i", "", "Attach an image file path (shorthand)")

	flag.StringVar(&fileFlag, "file", "", "Attach a file path")
	flag.StringVar(&fileFlag, "f", "", "Attach a file path (shorthand)")

	flag.BoolVar(&guiFlag, "gui", false, "Launch the Unjess Desktop GUI application")

	flag.BoolVar(&versionFlag, "version", false, "Print Unjess CLI version and exit")
	flag.BoolVar(&versionFlag, "v", false, "Print Unjess CLI version and exit (shorthand)")

	flag.BoolVar(&statusFlag, "status", false, "Print workspace & harness status and exit")
	flag.BoolVar(&modelsFlag, "models", false, "List available models and exit")
	flag.BoolVar(&updateFlag, "update", false, "Check for updates and exit")
	flag.BoolVar(&duelFlag, "duel", false, "Run Red-Team vs Blue-Team Security Duel on workspace and exit")
	flag.BoolVar(&watchdogFlag, "watchdog", false, "Run Workspace Watchdog health check and exit")

	flag.Parse()
	args := flag.Args()

	// 3. Handle version flag or positional `unjess version`
	if versionFlag || (len(args) > 0 && strings.EqualFold(args[0], "version")) {
		fmt.Printf("Unjess CLI v%s\n", updater.CurrentVersion)
		return
	}

	// 4. Resolve workspace directory
	wsPath := wsFlag
	if wsPath == "" {
		if wd, err := os.Getwd(); err == nil {
			wsPath = wd
		} else {
			wsPath = "."
		}
	}
	if abs, err := filepath.Abs(wsPath); err == nil {
		wsPath = abs
	}

	// 5. Handle GUI launch flag or positional `unjess gui` / `unjess desktop`
	if guiFlag || (len(args) > 0 && (strings.EqualFold(args[0], "gui") || strings.EqualFold(args[0], "desktop"))) {
		if err := cli.LaunchDesktopGUI(wsPath); err != nil {
			cli.PrintError(fmt.Sprintf("Failed to launch desktop GUI: %v", err))
			os.Exit(1)
		}
		return
	}

	// 6. Initialize core configuration and subsystem components
	settings := config.LoadConfig()
	_ = skills.EnsureBuiltinSkillsSeeded()
	hm := agent.NewHarnessManager(settings)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 7. Create command session
	sess := cli.NewCommandSession(hm, settings, wsPath)

	// Apply CLI flag overrides to session
	if modelFlag != "" {
		sess.Model = modelFlag
	}
	if modeFlag != "" {
		sess.AgentMode = modeFlag
	} else if yesFlag {
		sess.AgentMode = "Autonomous"
	}
	if effortFlag != "" {
		sess.ReasoningEffort = effortFlag
	}
	if convFlag != "" {
		sess.ConvID = convFlag
	}
	if cavemanFlag {
		sess.CavemanMode = true
	}
	if ponytailFlag {
		sess.PonytailMode = true
	}
	if workflowFlag != "" {
		sess.WorkflowID = workflowFlag
	}
	if agentFlag != "" {
		sess.AgentID = agentFlag
	}

	// 8. Load pending attachments if specified
	if imageFlag != "" {
		img, err := cli.LoadImageAttachment(imageFlag)
		if err != nil {
			cli.PrintError(fmt.Sprintf("Failed to load image '%s': %v", imageFlag, err))
			os.Exit(1)
		}
		sess.PendingImages = append(sess.PendingImages, img)
	}

	if fileFlag != "" {
		att, err := cli.LoadFileAttachment(fileFlag)
		if err != nil {
			cli.PrintError(fmt.Sprintf("Failed to load file attachment '%s': %v", fileFlag, err))
			os.Exit(1)
		}
		sess.PendingAttachments = append(sess.PendingAttachments, att)
	}

	// 9. Handle one-shot slash command flags or positional slash commands
	var slashCmd string
	if statusFlag {
		slashCmd = "/status"
	} else if modelsFlag {
		slashCmd = "/models"
	} else if updateFlag {
		slashCmd = "/update"
	} else if duelFlag {
		slashCmd = "/duel"
	} else if watchdogFlag {
		slashCmd = "/watchdog"
	} else if len(args) > 0 {
		first := strings.ToLower(args[0])
		switch first {
		case "help", "status", "models", "mcp", "skills", "update", "duel", "watchdog", "memory", "workflows", "agents", "subagents", "tools", "usage", "context", "settings", "git", "files":
			slashCmd = "/" + strings.Join(args, " ")
		default:
			if strings.HasPrefix(first, "/") {
				slashCmd = strings.Join(args, " ")
			}
		}
	}

	if slashCmd != "" {
		cli.DispatchSlashCommand(ctx, sess, slashCmd)
		return
	}

	// 10. Check for one-shot prompt (-p / --prompt or remaining positional text)
	prompt := promptFlag
	if prompt == "" {
		if providerFlag != "" {
			pfLower := strings.ToLower(providerFlag)
			if pfLower != "ollama" && pfLower != "openai" && pfLower != "gemini" && pfLower != "anthropic" && pfLower != "openrouter" && pfLower != "custom" {
				prompt = providerFlag
			}
		}
		if prompt == "" && len(args) > 0 {
			prompt = strings.Join(args, " ")
		}
	}

	if prompt != "" {
		req := agent.ChatTurnRequest{
			ConversationID:  sess.ConvID,
			Prompt:          prompt,
			Model:           sess.Model,
			ReasoningEffort: sess.ReasoningEffort,
			Workspace:       sess.Workspace,
			WorkflowID:      sess.WorkflowID,
			AgentID:         sess.AgentID,
			AgentMode:       sess.AgentMode,
			CavemanMode:     sess.CavemanMode,
			PonytailMode:    sess.PonytailMode,
			Images:          sess.PendingImages,
			Attachments:     sess.PendingAttachments,
		}

		sess.WorkflowID = ""
		sess.PendingImages = nil
		sess.PendingAttachments = nil

		_, err := cli.RunTurnInteractive(ctx, sess.HM, req, sess.Reader)
		if err != nil {
			cli.PrintError(fmt.Sprintf("Turn error: %v", err))
			os.Exit(1)
		}
		return
	}

	// 11. No one-shot execution requested: start interactive REPL
	if err := cli.StartREPL(ctx, sess); err != nil {
		cli.PrintError(fmt.Sprintf("CLI error: %v", err))
		os.Exit(1)
	}
}
