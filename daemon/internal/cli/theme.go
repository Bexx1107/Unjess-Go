package cli

import (
	"fmt"
	"path/filepath"
	"strings"
)

// ANSI color and style escape sequences for terminal formatting.
const (
	ColorReset       = "\033[0m"
	ColorBold        = "\033[1m"
	ColorDim         = "\033[2m"
	ColorItalic      = "\033[3m"
	ColorUnderline   = "\033[4m"
	ColorRed         = "\033[31m"
	ColorGreen       = "\033[32m"
	ColorYellow      = "\033[33m"
	ColorBlue        = "\033[34m"
	ColorMagenta     = "\033[35m"
	ColorCyan        = "\033[36m"
	ColorWhite       = "\033[37m"
	ColorGray        = "\033[90m"
	ColorAccent      = "\033[38;5;214m" // Unjess Gold/Amber
	ColorCyanBright  = "\033[96m"
	ColorGreenBright = "\033[92m"
	ColorRedBright   = "\033[91m"
)

// PrintInfo prints an informational message prefixed with a cyan information icon.
func PrintInfo(msg string) {
	fmt.Printf("%s%sℹ%s %s%s%s\n", ColorBold, ColorCyan, ColorReset, ColorCyanBright, msg, ColorReset)
}

// PrintSuccess prints a success message prefixed with a green checkmark icon.
func PrintSuccess(msg string) {
	fmt.Printf("%s%s✓%s %s%s%s\n", ColorBold, ColorGreenBright, ColorReset, ColorGreen, msg, ColorReset)
}

// PrintWarn prints a warning message prefixed with a yellow warning icon.
func PrintWarn(msg string) {
	fmt.Printf("%s%s⚠%s %s%s%s\n", ColorBold, ColorYellow, ColorReset, ColorYellow, msg, ColorReset)
}

// PrintError prints an error message prefixed with a bright red cross icon.
func PrintError(msg string) {
	fmt.Printf("%s%s✗%s %s%s%s\n", ColorBold, ColorRedBright, ColorReset, ColorRedBright, msg, ColorReset)
}

// PrintBanner prints the Unjess Gold ASCII branding banner, version, model, and session badges.
func PrintBanner(version, model, provider, mode, effort, workspace, gitBranch string, caveman, ponytail bool) {
	fmt.Println()
	fmt.Printf("%s%s  _   _ _   _     _ _____ ____ ____  %s\n", ColorBold, ColorAccent, ColorReset)
	fmt.Printf("%s%s | | | | \\ | |   | | ____/ ___/ ___| %s\n", ColorBold, ColorAccent, ColorReset)
	fmt.Printf("%s%s | | | |  \\| |_  | |  _| \\___ \\___ \\ %s\n", ColorBold, ColorAccent, ColorReset)
	fmt.Printf("%s%s | |_| | |\\  | |_| | |___ ___) |__) |%s\n", ColorBold, ColorAccent, ColorReset)
	fmt.Printf("%s%s  \\___/|_| \\_|\\___/|_____|____/____/ %s\n", ColorBold, ColorAccent, ColorReset)

	verStr := strings.TrimSpace(version)
	if verStr == "" {
		verStr = "2.4.0"
	}
	if !strings.HasPrefix(verStr, "v") {
		verStr = "v" + verStr
	}

	fmt.Printf("  %s%sUnjess Go CLI%s %s%s%s\n\n", ColorBold, ColorWhite, ColorReset, ColorDim, verStr, ColorReset)

	var badges []string
	if model != "" {
		mStr := model
		if provider != "" {
			mStr += fmt.Sprintf(" (%s)", provider)
		}
		badges = append(badges, fmt.Sprintf("%s%s[%s]%s", ColorBold, ColorCyanBright, mStr, ColorReset))
	}
	if mode != "" {
		badges = append(badges, fmt.Sprintf("%s%s[%s]%s", ColorBold, ColorGreenBright, mode, ColorReset))
	}
	if effort != "" {
		badges = append(badges, fmt.Sprintf("%s%s[effort: %s]%s", ColorBold, ColorMagenta, effort, ColorReset))
	}
	if workspace != "" {
		wsShort := workspace
		if len(wsShort) > 35 {
			wsShort = "..." + wsShort[len(wsShort)-32:]
		}
		badges = append(badges, fmt.Sprintf("%s📂 %s%s", ColorDim, wsShort, ColorReset))
	}
	if gitBranch != "" {
		badges = append(badges, fmt.Sprintf("%s %s%s", ColorGreen, gitBranch, ColorReset))
	}
	if caveman {
		badges = append(badges, fmt.Sprintf("%s%s[🍖 caveman]%s", ColorBold, ColorYellow, ColorReset))
	}
	if ponytail {
		badges = append(badges, fmt.Sprintf("%s%s[👱 ponytail]%s", ColorBold, ColorMagenta, ColorReset))
	}

	if len(badges) > 0 {
		fmt.Printf("  %s\n\n", strings.Join(badges, " "))
	}
}

// PrintStatusBar prints a concise terminal status bar line displaying active model, mode, effort,
// workspace, caveman/ponytail toggles, and colored context window usage percentage.
func PrintStatusBar(model, mode, effort, workspace string, caveman, ponytail bool, contextPct float64) {
	var parts []string

	if model != "" {
		parts = append(parts, fmt.Sprintf("%s%s%s", ColorCyanBright, model, ColorReset))
	}
	if mode != "" {
		parts = append(parts, fmt.Sprintf("%s%s%s", ColorGreenBright, mode, ColorReset))
	}
	if effort != "" {
		parts = append(parts, fmt.Sprintf("%seffort:%s%s", ColorMagenta, effort, ColorReset))
	}
	if workspace != "" {
		wsName := filepath.Base(workspace)
		if wsName == "" || wsName == "." {
			wsName = workspace
		}
		parts = append(parts, fmt.Sprintf("%s📂 %s%s", ColorDim, wsName, ColorReset))
	}

	ctxColor := ColorGreenBright
	if contextPct >= 85.0 {
		ctxColor = ColorRedBright
	} else if contextPct >= 60.0 {
		ctxColor = ColorYellow
	}
	parts = append(parts, fmt.Sprintf("ctx: %s%.1f%%%s", ctxColor, contextPct, ColorReset))

	if caveman {
		parts = append(parts, fmt.Sprintf("%s🍖%s", ColorYellow, ColorReset))
	}
	if ponytail {
		parts = append(parts, fmt.Sprintf("%s👱%s", ColorMagenta, ColorReset))
	}

	barContent := strings.Join(parts, fmt.Sprintf(" %s•%s ", ColorDim, ColorReset))
	fmt.Printf("%s─[%s %s %s]──────────────────────────────────────────────%s\n",
		ColorDim, ColorReset, barContent, ColorDim, ColorReset)
}
