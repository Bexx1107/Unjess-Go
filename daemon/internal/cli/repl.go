package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"unjess-daemon/internal/agent"
	"unjess-daemon/internal/updater"
	"unjess-daemon/internal/workspace"
)

// StartREPL initiates the interactive Read-Eval-Print Loop for the Unjess CLI.
func StartREPL(ctx context.Context, sess *CommandSession) error {
	// 1. Enable Windows VT100 / ANSI escape sequences
	EnableVirtualTerminal()

	// 2. Ensure session reader is initialized
	if sess.Reader == nil {
		sess.Reader = bufio.NewReader(os.Stdin)
	}

	// 3. Ensure workspace directory is set
	if sess.Workspace == "" {
		if wd, err := os.Getwd(); err == nil {
			sess.Workspace = wd
		}
	}

	// 4. Fetch git branch info and display startup banner
	gitInfo := workspace.GetGitInfo(sess.Workspace)
	provider := ""
	if sess.Settings != nil {
		provider = sess.Settings.Provider
	}
	PrintBanner(updater.CurrentVersion, sess.Model, provider, sess.AgentMode, sess.ReasoningEffort, sess.Workspace, gitInfo.Branch, sess.CavemanMode, sess.PonytailMode)

	// 5. Signal handling for Ctrl+C (SIGINT)
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)
	defer signal.Stop(sigCh)

	var (
		intMu         sync.Mutex
		lastInterrupt time.Time
	)

	stopSig := make(chan struct{})
	defer close(stopSig)

	go func() {
		for {
			select {
			case <-stopSig:
				return
			case <-ctx.Done():
				return
			case <-sigCh:
				var isRunning bool
				if sess.HM != nil {
					live := sess.HM.GetLiveTurnState()
					isRunning, _ = live["running"].(bool)
				}

				if isRunning {
					sess.HM.AbortTurn()
					fmt.Printf("\n%s⚠ Aborting current turn...%s\n", ColorYellow, ColorReset)
				} else {
					intMu.Lock()
					now := time.Now()
					if !lastInterrupt.IsZero() && now.Sub(lastInterrupt) < 2*time.Second {
						intMu.Unlock()
						fmt.Printf("\n%sGoodbye!%s\n", ColorDim, ColorReset)
						os.Exit(0)
					}
					lastInterrupt = now
					intMu.Unlock()
					fmt.Printf("\n%s(Press Ctrl+C again or type /exit to quit)%s\n", ColorDim, ColorReset)
				}
			}
		}
	}()

	// 6. Main Interactive REPL loop
	var (
		multilineLines []string
		isFirstPrompt  = true
	)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if len(multilineLines) == 0 {
			fmt.Print(renderREPLPrompt(sess, isFirstPrompt))
			isFirstPrompt = false
		} else {
			fmt.Printf("%s  ... %s", ColorDim, ColorReset)
		}

		line, err := sess.Reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				if len(multilineLines) > 0 || strings.TrimSpace(line) != "" {
					multilineLines = append(multilineLines, strings.TrimRight(line, "\r\n"))
				} else {
					fmt.Println()
					return nil
				}
			} else {
				return err
			}
		}

		line = strings.TrimLeft(strings.TrimRight(line, "\r\n"), "\ufeff")

		// Handle trailing backslash for multiline continuation
		if strings.HasSuffix(line, "\\") {
			multilineLines = append(multilineLines, strings.TrimSuffix(line, "\\"))
			continue
		}

		multilineLines = append(multilineLines, line)
		fullInput := strings.TrimSpace(strings.TrimLeft(strings.Join(multilineLines, "\n"), "\ufeff"))
		multilineLines = nil

		if fullInput == "" {
			continue
		}

		// Handle Ctrl+O (0x0F) or /trace shortcut
		if strings.Contains(fullInput, "\x0f") || fullInput == "/trace" {
			DispatchSlashCommand(ctx, sess, "/trace")
			continue
		}

		// Handle shell execution escape shortcut: !command (e.g. !git status)
		if strings.HasPrefix(fullInput, "!") {
			shellCmd := strings.TrimSpace(strings.TrimPrefix(fullInput, "!"))
			_, shouldExit, _ := DispatchSlashCommand(ctx, sess, "/exec "+shellCmd)
			if shouldExit {
				return nil
			}
			continue
		}

		// Handle slash commands (/help, /model, /mode, /status, etc.)
		var prompt string
		if strings.HasPrefix(fullInput, "/") {
			handled, shouldExit, promptToRun := DispatchSlashCommand(ctx, sess, fullInput)
			if shouldExit {
				return nil
			}
			if handled && promptToRun == "" {
				continue
			}
			if promptToRun != "" {
				prompt = promptToRun
			} else {
				continue
			}
		} else {
			prompt = fullInput
		}

		// Build chat turn request payload
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

		// Clear one-shot items from session
		sess.WorkflowID = ""
		sess.PendingImages = nil
		sess.PendingAttachments = nil

		// Execute interactive agent turn with streaming terminal UI
		turnRes, turnErr := RunTurnInteractive(ctx, sess.HM, req, sess.Reader, sess.CompactTrace)
		if turnRes != nil {
			if t, ok := turnRes["turn"].(map[string]any); ok {
				sess.LastTurn = t
			}
		}
		if turnErr != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			PrintError(fmt.Sprintf("Turn error: %v", turnErr))
		}

		// Update active conversation ID from harness manager
		if sess.HM != nil {
			if activeID := sess.HM.ActiveConversationID(); activeID != "" {
				sess.ConvID = activeID
			}
		}
	}
}

// renderREPLPrompt builds the formatted prompt line:
// [workspaceName] (gitBranch) [Mode | Model] [+N attachments]
// ❯
func renderREPLPrompt(sess *CommandSession, isFirst bool) string {
	wsName := filepath.Base(sess.Workspace)
	if wsName == "" || wsName == "." || wsName == string(filepath.Separator) {
		if sess.Workspace != "" {
			wsName = sess.Workspace
		} else {
			wsName = "workspace"
		}
	}

	gitInfo := workspace.GetGitInfo(sess.Workspace)
	var gitPart string
	if gitInfo.Branch != "" {
		gitPart = fmt.Sprintf(" (%s%s%s)", ColorMagenta, gitInfo.Branch, ColorReset)
	}

	mode := sess.AgentMode
	if mode == "" {
		mode = "autonomous"
	}
	model := sess.Model
	if model == "" {
		model = "default"
	}

	var attachPart string
	attachCount := len(sess.PendingImages) + len(sess.PendingAttachments)
	if attachCount > 0 {
		attachPart = fmt.Sprintf(" %s[%d attachments]%s", ColorYellow, attachCount, ColorReset)
	}

	prefix := "\n"
	if isFirst {
		prefix = ""
	}

	return fmt.Sprintf("%s%s[%s]%s%s %s[%s | %s]%s%s\n%s❯ %s",
		prefix,
		ColorCyanBright, wsName, ColorReset,
		gitPart,
		ColorDim, mode, model, ColorReset,
		attachPart,
		ColorAccent, ColorReset,
	)
}
