// Package updater provides automatic update checks, manifest parsing, changelog merging,
// and safe binary staging for Unjess Desktop & Daemon.
package updater

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"unjess-daemon/internal/version"
)

const (
	CurrentVersion   = version.Current
	DefaultUpdateURL = "https://unjess.si/releases/version.json"
)

var (
	updaterMu         sync.RWMutex
	parentDesktopExe  string
)

// SetParentExecutable records the known path to the parent Unjess desktop executable.
func SetParentExecutable(path string) {
	updaterMu.Lock()
	defer updaterMu.Unlock()
	parentDesktopExe = filepath.Clean(path)
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

func init() {
	// Clean up any leftover .old binary from a previous update on startup
	if exe, err := os.Executable(); err == nil {
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			oldExe := resolved + ".old"
			if _, err := os.Stat(oldExe); err == nil {
				_ = os.Remove(oldExe)
			}
		}
	}
}

// ChangelogEntry represents a single version entry in the release changelog.
type ChangelogEntry struct {
	Version string   `json:"version"`
	Date    string   `json:"date"`
	Title   string   `json:"title"`
	Tag     string   `json:"tag"`
	Changes []string `json:"changes"`
}

// VersionManifest defines the structure of version.json hosted on the update server or mirror.
type VersionManifest struct {
	Version      string           `json:"version"`
	PublishedAt  string           `json:"published_at"`
	Channel      string           `json:"channel"`
	DownloadURL  string           `json:"download_url"`
	InstallerURL string           `json:"installer_url"`
	DaemonURL    string           `json:"daemon_url"`
	SHA256       string           `json:"sha256,omitempty"`
	Notes        string           `json:"notes"`
	Changelog    []ChangelogEntry `json:"changelog"`
}

// BuiltinChangelog returns rich, detailed release notes for recent Unjess versions.
func BuiltinChangelog() []ChangelogEntry {
	return []ChangelogEntry{
		{
			Version: "2.9.9",
			Date:    "2026-11-01",
			Title:   "Interactive Chat Session Renaming & Conversation Title REST API",
			Tag:     "LATEST",
			Changes: []string{
				"Interactive Chat Session Renaming: Added inline rename button (pencil icon) and double-click to rename conversations across both project workspace trees and quick chats",
				"Header Session Renaming: Clickable middle header session title with dedicated rename button for instant title updates",
				"Conversation Rename REST API: Added POST and PATCH /api/harness/conversations/{id}/rename with atomic title persistence in ~/.unjess/state.json",
			},
		},
		{
			Version: "2.9.8",
			Date:    "2026-11-01",
			Title:   "Silent Windows Daemon Execution (-H=windowsgui) & GGUF Local Provider Routing",
			Tag:     "UPDATE",
			Changes: []string{
				"Windows PE GUI Subsystem Enforcement: Built unjessd.exe with -H=windowsgui linker flag, ensuring daemon runs completely silent in background without spawning CMD window",
				"GGUF & Windows Path Provider Routing: Fixed provider detection so Windows absolute file paths (e.g. E:\\models\\...) and .gguf files route cleanly to llama.cpp / LM Studio rather than failing with Ollama 'invalid model name' error",
				"OpenAI /v1 Base URL Normalization: Automatically ensures llama.cpp and LM Studio endpoints call /v1/chat/completions even if user specifies root port URL",
				"Active Provider Synchronization: Synchronizes active provider in settings and chat execution payload when selecting models from local providers",
			},
		},
		{
			Version: "2.9.7",
			Date:    "2026-11-01",
			Title:   "Dynamic Model Discovery for llama.cpp & LM Studio, Test & Fetch Controls",
			Tag:     "UPDATE",
			Changes: []string{
				"Automated llama.cpp Model Discovery: Directly queries /v1/models on llama-server to populate active GGUF models into UI dropdowns",
				"LM Studio Discovery: Directly queries /v1/models on LM Studio server with automatic optgroup categorization",
				"Instant Settings Probing: Added dedicated 'Fetch' model discovery controls for llama.cpp and LM Studio endpoints",
				"Dynamic Runtime Versioning: Complete unification of runtime versioning across daemon, CLI, desktop shell, and system prompts",
			},
		},
		{
			Version: "2.9.6",
			Date:    "2026-11-01",
			Title:   "Terminal-Bench 4.0 Near-Miss Hardening & Dynamic Execution Parity",
			Tag:     "UPDATE",
			Changes: []string{
				"Terminal-Bench 4.0 Near-Miss Hardening: Pre-exit verifier guard and automated residual convergence checks",
				"Dynamic Runtime Versioning: Unified canonical version across Go daemon, CLI, settings API, and agent self-knowledge prompts",
				"Headless Execution Reliability: Streamlined subprocess monitoring and graceful container cleanup",
			},
		},
		{
			Version: "2.3.8",
			Date:    "2026-10-31",
			Title:   "Execution-Surface Verification Doctrine, Symbolic Adversarial Testing & Domain Rule Validation",
			Tag:     "UPDATE",
			Changes: []string{
				"Execution-Surface Verification Doctrine: Enforces active independent test execution on changed paths with zero tolerance for partial passes ('3 of 4 passed' is treated as failure until 100% clean)",
				"Symbolic & Mathematical Grader Hardening: Added explicit handling for symbolic edge cases including set unions, integration constants (+ C), piecewise expressions, units stripping, boxed LaTeX extraction, and tuple structures",
				"Domain Rule & Classification Alignment: Enforces strict evaluation of rule ordering, exclusion semantics in mutual exclusion pairs, strict vs non-strict inequalities, and ground truth validation",
				"Continuous Self-Correction Loop: Mandates immediate iterative root-cause fixes before concluding any execution turn",
			},
		},
		{
			Version: "2.3.7",
			Date:    "2026-10-31",
			Title:   "Harness Hardening: Negative File Constraints, Autonomous Continuation Invariants & Catalog Optimization",
			Tag:     "UPDATE",
			Changes: []string{
				"Negative File Constraints & Protected Paths: Automatically parses negative constraints from task prompts ('Do not modify /app/...') and locks write_file, edit_file, and mutating bash commands with PermissionDenied",
				"Autonomous Continuation Invariant: Detects intermediate narration commentary ('I will now test...', 'Let me inspect...') or empty reasoning turns, injecting immediate system guidance to keep execution active instead of exiting prematurely",
				"Subagent Model Catalog Optimization: Only injects the full 25-model subagent catalog when tasks explicitly reference parallel workers, delegation, or models, slashing system prompt token bloat",
				"Active Tool Surface Unification: Dynamic tool definition filtering respecting per-turn disabled tool sets and unified MCP tool registration",
			},
		},
		{
			Version: "2.3.5",
			Date:    "2026-10-31",
			Title:   "The 7 Core Harness Upgrades: KV-Cache Compaction, 30KB Output Budget & Resilient Tool Ladder",
			Tag:     "UPDATE",
			Changes: []string{
				"KV-Cache-Friendly Smart Context Compaction: Replaced destructive iter>=3 truncation with smart superseded file-read micro-compaction and autoCompactWindow token threshold, preserving critical code context and Ollama/vLLM KV caches",
				"300-Line / 30 KB Command Output Budget: Raised inline command execution output limits to 30,000 bytes with context-aware head/tail retention, preventing truncated compiler errors and test tracebacks",
				"Deferred Tool Placement & Tool Search: Sends lean core tool definitions (~12 tools) dynamically on turns, drastically slashing LLM schema token overhead while unlocking tools on demand",
				"Lean Autonomous System Prompt: Prevented duplicate reasoning prompts when native reasoning effort is enabled and trimmed non-essential protocols from initial prompts",
				"edit_file Hardening & Relative Indentation: Added automatic line-number prefix stripping, relative indentation preservation, and closest-match failure windows for ultra-reliable edits",
				"Named Recovery Ladder & Tool Aliasing: Added silent normalization for hallucinated tool names and malformed tool call JSON, rescuing execution flow automatically",
				"Single-Run Pass Gap Closing: Upgraded pre-exit verification to multi-attempt test/syntax validation with trajectory pivot recovery and leftover scratch file detection",
			},
		},
		{
			Version: "2.3.4",
			Date:    "2026-10-31",
			Title:   "Unified Antigravity-Style Activity Timeline, Lag-Free Typing & Crisp Tool Status Dots",
			Tag:     "UPDATE",
			Changes: []string{
				"Unified Antigravity-Style Activity Timeline: Replaced fragmented multi-box execution trace with a single unified chronological timeline list in Agent Activity — clean compact rows with friendly action labels, duration badges, and inline expandable details",
				"100% Lag-Free Typing: Eliminated UI thread DOM thrashing by caching structural signatures and diff calculations, ensuring instant keystroke response in the chat textarea even while the agent is actively executing",
				"Crisp, Solid Status Dots: Fixed status dot visibility across all tool calls and execution steps with dedicated solid core indicators and surrounding pulse animations that never clip or disappear",
			},
		},
		{
			Version: "2.3.3",
			Date:    "2026-10-31",
			Title:   "Strict Hidden-by-Default Activity Traces & Execution Details",
			Tag:     "UPDATE",
			Changes: []string{
				"Hidden-by-Default Activity Traces: Enforced strict collapsed-by-default behavior across Agent Activity trace, execution steps, tool call details, and reasoning tokens both during live streaming and upon completion",
				"Zero Visual Clutter: Live streaming displays only clean animated status row and streamed markdown text unless details are explicitly expanded by the user",
				"Persistent User Toggle State: Details only expand when user explicitly clicks [Show details], preserving state seamlessly across streaming updates",
			},
		},
		{
			Version: "2.3.2",
			Date:    "2026-10-31",
			Title:   "Changes & Code Diffs Inspector Panel & Clean Chat Stream",
			Tag:     "UPDATE",
			Changes: []string{
				"Code Changes & Diffs Inspector: Transformed right inspector sidebar into dedicated file diffs view with addition/deletion stats (+A -D), unified diffs, and individual hunk reversion",
				"Eliminated Duplicate Thinking: Fixed UI logic so reasoning tokens render strictly once in Model Thinking & Reasoning rather than duplicating in planning step details",
				"Ultra-Clean Chat Feed: Removed code diffs from the chat message feed completely, showing only assistant answers and collapsed Execution Trace pills with interactive Diff buttons",
			},
		},
		{
			Version: "2.3.1",
			Date:    "2026-10-31",
			Title:   "MCP Large Payload & Binary Attachment Interceptor",
			Tag:     "UPDATE",
			Changes: []string{
				"Automatic Binary Extraction: Automatically intercepts large Base64 / binary file payloads (PDFs, images, archives) returned by email, fetch, or file MCP tools and extracts them directly to workspace downloads/",
				"Context Window Protection: Replaces multi-megabyte Base64 payload blobs with clean disk file pointers, preventing context truncation and token exhaustion",
				"Full Binary Parity: Instant support for PDF, PNG, JPEG, WEBP, ZIP, DOCX, and raw binaries with clean metadata (filePath, status, size_bytes)",
			},
		},
		{
			Version: "2.3.0",
			Date:    "2026-10-30",
			Title:   "Persistent Task Scheduler, CLI Session Isolation, Quoting Fixes & Tooling Upgrades",
			Tag:     "UPDATE",
			Changes: []string{
				"Persistent Task Scheduler: Native cron, wall-clock, and interval scheduler engine running persistently in background daemon with REST API, CLI /schedule command, and Desktop GUI management tab",
				"CLI Session Isolation: Enforced clean, dedicated conversation sessions for CLI launches, eliminating cross-GUI context leakage",
				"Windows Execution Robustness: Git Bash auto-routing and PowerShell Base64 -EncodedCommand execution preventing quote stripping on Windows",
				"Large File Streaming: Added start_line and end_line range slicing with streaming reader in read_file primitive for massive files",
				"Plan Mode Scratchpad Allowance: Permitted write_file in scratch/ and .tmp/ directories during Plan Mode for non-destructive diagnostic exploration",
				"CLI Trace Compaction: Support for collapsible single-line tool execution traces with /trace and Ctrl+O toggles",
			},
		},
		{
			Version: "2.2.4",
			Date:    "2026-10-30",
			Title:   "Hotfix: Clean JavaScript Execution, Lucide Icon Restoration & UI Reactivity",
			Tag:     "UPDATE",
			Changes: []string{
				"JavaScript Engine Fix: Eliminated trailing syntax error in desktop interface ensuring 100% clean evaluation across all scripts",
				"Lucide Icon Restoration: Restored full vector icon rendering across sidebar tabs, model selector, and settings views",
				"Immediate Auto-Update Detection: Bumped release version so existing installations immediately detect update and refresh without manual reinstall",
			},
		},
		{
			Version: "2.2.3",
			Date:    "2026-10-30",
			Title:   "Header Minimalism, Window Min-Size Reduction & Draggable Sidepanels",
			Tag:     "UPDATE",
			Changes: []string{
				"Header Minimalism: Stripped redundant badges and engine pills for a clean, focused navigation bar",
				"Reduced Window Min-Size: Lowered desktop minimum window bounds to 480x400 for flexible screen tiling",
				"Draggable Resizable Sidepanels: Added interactive split dividers for sidebar and context inspector with width persistence",
				"Synchronized Go & Rust Architecture: Full parity across daemon, desktop runner, and distribution binaries",
			},
		},
		{
			Version: "2.2.2",
			Date:    "2026-10-30",
			Title:   "Collapsible Execution Trace, Noir Minimal UI & File Drag-and-Drop Fix",
			Tag:     "UPDATE",
			Changes: []string{
				"Collapsible Execution Trace: Pure monochrome noir activity drawer grouping tool calls, diffs, thinking streams, and file touches into an expandable row, collapsed by default for clean response reading",
				"Zero-Emoji Technical Minimalism: Eliminated all decorative emojis from activity summaries, headers, and UI status bars in favor of refined Lucide vector icons",
				"Image & Screenshot Drag-and-Drop Fix: Added runasoriginaluser to installer preventing Windows UIPI elevation drop-blocks and broadened image extension detection (.png, .jpg, .webp, .gif)",
				"Seamless In-App Update: Streamlined binary staging and auto-restart across Unjess Desktop and background daemon",
			},
		},
		{
			Version: "2.2.1",
			Date:    "2026-10-30",
			Title:   "Autonomous Loop Continuation Invariant, Read-Only Spec Fences & Pre-Exit Test Protocol",
			Tag:     "UPDATE",
			Changes: []string{
				"Autonomous Continuation Invariant: Intercepts intermediate narration or planned intent emitted without tool calls, injecting synthetic continuation nudges to prevent premature loop breaks and GUI stalls",
				"Read-Only & Spec File Fences: Automatically parses negative constraints from task prompts (e.g., 'Do not modify /app/run_pretrain.sh') and enforces hard PermissionDenied blocks across write_file, edit_file, and destructive shell commands",
				"Pre-Exit Test Verification Protocol: Actively verifies that executed regression and unit tests passed without errors before allowing task completion, requiring the model to resolve failures first",
			},
		},
		{
			Version: "2.2.0",
			Date:    "2026-10-29",
			Title:   "Harbor Scaffolding, Context Head/Tail Disk Caching, Multi-File Skills & Adversarial Verification",
			Tag:     "UPDATE",
			Changes: []string{
				"Harbor evaluation harness support with NVIDIA GPU passthrough and container orchestration",
				"Head/tail context truncation (25 head / 35 tail lines) with disk log caching in .unjess/logs/",
				"Dynamic timeout support and asynchronous background task manager",
				"Multi-file skills package loading and adversarial self-verification invariants",
			},
		},
		{
			Version: "2.1.3",
			Date:    "2026-10-27",
			Title:   "High-Capacity Subagents (300 Turns), Per-Worker Limits & Fail-Safe Report Synthesis",
			Tag:     "UPDATE",
			Changes: []string{
				"Increased default subagent maximum turn limit from 50 to 300 turns to support complex long-running autonomous building and refactoring workflows",
				"Added per-worker max_turns parameter in invoke_subagent allowing parent agents to assign custom iteration budgets per subagent",
				"Added fail-safe report synthesis in executeSubagentLoop: subagents that reach their turn limit or complete without text now return structured activity summaries instead of empty reports",
			},
		},
		{
			Version: "2.1.2",
			Date:    "2026-10-21",
			Title:   "Token Budget & Context Intelligence (/budget, /context), Browser-Use CDP Engine & Auto Error Learning",
			Tag:     "UPDATE",
			Changes: []string{
				"Added /budget (/price) and /context slash commands and interactive dashboards showing session, day, week, month, and year token & USD telemetry plus per-layer context window usage",
				"Upgraded Browser tab with dual-mode Web Proxy + Browser-Use CDP Engine (numbered interactive DOM element overlays, click/type/scroll/navigate actions)",
				"Added Harness2-inspired Auto Error Learning & Tool Self-Correction engine persisting verified tool fixes across sessions",
				"Streamlined Right Inspector and Project Explorer UI with clean green active workspace folder indicator and 1M/262K cloud model context window limits",
			},
		},
		{
			Version: "2.1.1",
			Date:    "2026-10-19",
			Title:   "Reasoning Effort Levels (Auto/Low/Med/High/Max), October 2026 Models & Live Thinking Stream",
			Tag:     "UPDATE",
			Changes: []string{
				"Added Reasoning Effort Selector (Auto, Low, Med, High, Max, Off) directly inside the Model Select dropdowns",
				"Added native API effort levels across Anthropic (Claude Opus 5.5, Sonnet 5.5, Fable 5.1, Haiku 5.5), Google Gemini (Gemini 4 Argon, Gemini 3.8 Flash), QwenCloud/DashScope (Qwen 3.8 Max, DeepSeek V4.1, GLM 5.3), Moonshot (Kimi K3, K2.7 Code), and OpenAI (GPT-5, o3, o4-mini)",
				"Fixed live Model Thinking & Reasoning stream auto-expansion during active turns across Gemini, Claude 5.5, Qwen, and Kimi",
				"Added Caveman & Ponytail prompt dock toggles and fixed Moonshot (.ai/.cn) and Qwen DashScope (Intl/CN/Coding) multi-region endpoint auto-fallback",
			},
		},
		{
			Version: "2.1.0",
			Date:    "2026-10-18",
			Title:   "Frontier v2.1.0 - AST Engine, Shadow Worktrees, Self-Healing, Visual QA & Red/White Security Duel",
			Tag:     "UPDATE",
			Changes: []string{
				"Added AST Code Intelligence Engine with ast_outline, ast_find_callers, ast_edit_symbol, and ast_rename_symbol",
				"Added Automatic Linter & Compiler Self-Healing Feedback Loop verifying files after write_file and edit_file",
				"Added Speculative Multi-Branch Execution (speculative_execute) with isolated shadow git worktrees",
				"Added Red vs. White Security Duel Mode with shadow API discovery, AST taint analysis, and microsecond smart fuzzing",
				"Added Multimodal Visual QA screenshot capture loop via headless Chromium / Microsoft Edge",
				"Added Self-Evolving Dynamic Tool Synthesis registering custom reusable tools into ~/.unjess/custom_tools/",
				"Added Intra-Turn Context Compaction and /compact & /duel slash commands",
			},
		},
		{
			Version: "2.0.3",
			Date:    "2026-10-17",
			Title:   "Seamless Windows Process Restart & Robust Update Pipeline",
			Tag:     "UPDATE",
			Changes: []string{
				"Added robust Windows detached process restart engine launching new Unjess.exe and cleanly closing old instances",
				"Added target executable resolution updating both desktop shell (Unjess.exe) and background daemon (unjessd.exe)",
				"Added frontend active health polling with automatic window reload when new version comes online",
				"Fixed update button state handling with reliable try/finally guards preventing indefinite spinning",
			},
		},
		{
			Version: "2.0.2",
			Date:    "2026-10-16",
			Title:   "Interactive Slash Commands & Instant MCP Awareness",
			Tag:     "UPDATE",
			Changes: []string{
				"Added interactive \"/\" Slash Commands Palette in prompt box with floating, searchable menu and arrow/tab navigation (/compact, /goal, /plan, /subagent, /skill, /web, /mcp, /memory, /clear, /help)",
				"Added native mcp_status tool for main agent and subagents to inspect all connected MCP servers and available tools in a single call",
				"Auto-injected <connected_mcp_servers> directly into system prompt for instantaneous 0-second MCP tool awareness",
			},
		},
		{
			Version: "2.0.1",
			Date:    "2026-10-16",
			Title:   "Mobile Viewport & Prompt Dock Fix",
			Tag:     "UPDATE",
			Changes: []string{
				"Added dynamic mobile viewport height tracking (100dvh + window.visualViewport.height) adapting automatically to mobile browser URL and toolbar heights",
				"Added min-h-0 across the flex chain and pinned #chat-input-dock with flex-shrink-0 above mobile browser toolbars and keyboard",
				"Added automatic viewport height synchronization on resize and scroll events",
			},
		},
		{
			Version: "2.0.0",
			Date:    "2026-10-15",
			Title:   "Go + Rust Native Architecture & Antigravity Parallel Subagents",
			Tag:     "MAJOR",
			Changes: []string{
				"Replaced 50 MB embedded Python runtime with native 8.6 MB Go backend daemon (unjessd) and 1.3 MB Rust desktop shell",
				"Added Antigravity-style Parallel Subagent Spawning (invoke_subagent) with concurrent Go worker goroutines",
				"Built Split-Screen Live Subagent Transcript Inspector with real-time thinking, file diffs, and direct subagent messaging",
				"Added Built-in Auto-Updater & Release Changelog Engine connected to unjess.si",
				"Standardized Windows command execution on hidden PowerShell and added dynamic Ollama context sizing (num_ctx >= 16384)",
			},
		},
		{
			Version: "1.8.0",
			Date:    "2026-10-15",
			Title:   "Mashina Collab Native MCP & Dynamic Model Discovery",
			Tag:     "FEATURE",
			Changes: []string{
				"Added native HTTP JSON-RPC 2.0 + SSE MCP transport with built-in Mashina Collab (mashina.si/api/collab/mcp) integration",
				"Dynamic model discovery across local Ollama and all 12 configured cloud API key providers",
				"Restored custom theme accent color engine with dynamic CSS variables",
			},
		},
		{
			Version: "1.7.0",
			Date:    "2026-10-14",
			Title:   "Monochrome Carbon Aesthetic, Multimodal Attachments & Native Folder Picker",
			Tag:     "UI",
			Changes: []string{
				"Redesigned interface in high-contrast monochrome carbon palette",
				"Added Ctrl+V clipboard image paste, drag-and-drop file attachments, and automatic 4K image optimization",
				"Added Quick Chat auto-isolated workspaces and native Windows Folder Picker dialog",
			},
		},
		{
			Version: "1.5.0",
			Date:    "2026-10-13",
			Title:   "Skills Library, REA Preinstalled Skill & MCP Control Center",
			Tag:     "FEATURE",
			Changes: []string{
				"Preinstalled Reverse Engineer Anything (morluto/rea) skill and 1-click MCP server presets",
				"Added GitHub Skill Repository installer and 6-tab Settings Control Center",
			},
		},
	}
}

// CompareVersions compares two semver version strings v1 and v2.
// It returns 1 if v1 > v2, -1 if v1 < v2, and 0 if equal.
// Leading 'v' or 'V' prefixes and surrounding whitespaces are stripped.
func CompareVersions(v1, v2 string) int {
	clean1 := strings.TrimSpace(v1)
	clean1 = strings.TrimPrefix(clean1, "v")
	clean1 = strings.TrimPrefix(clean1, "V")

	clean2 := strings.TrimSpace(v2)
	clean2 = strings.TrimPrefix(clean2, "v")
	clean2 = strings.TrimPrefix(clean2, "V")

	parts1 := strings.Split(clean1, ".")
	parts2 := strings.Split(clean2, ".")

	maxLen := len(parts1)
	if len(parts2) > maxLen {
		maxLen = len(parts2)
	}

	for i := 0; i < maxLen; i++ {
		var n1, n2 int
		if i < len(parts1) {
			numStr := strings.Split(parts1[i], "-")[0]
			n1, _ = strconv.Atoi(numStr)
		}
		if i < len(parts2) {
			numStr := strings.Split(parts2[i], "-")[0]
			n2, _ = strconv.Atoi(numStr)
		}
		if n1 > n2 {
			return 1
		}
		if n1 < n2 {
			return -1
		}
	}
	return 0
}

// CheckForUpdates queries the update server or local mirror for newer versions and manifests.
func CheckForUpdates(updateURL string, autoUpdateEnabled bool) map[string]any {
	if strings.TrimSpace(updateURL) == "" {
		updateURL = DefaultUpdateURL
	}

	var manifest VersionManifest
	source := "builtin"
	fetched := false

	// 1. Try remote HTTP GET with 4s timeout
	client := &http.Client{Timeout: 4 * time.Second}
	resp, err := client.Get(updateURL)
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			data, readErr := io.ReadAll(resp.Body)
			if readErr == nil {
				data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
				if json.Unmarshal(data, &manifest) == nil && manifest.Version != "" {
					source = "remote"
					fetched = true
				}
			}
		}
	}

	// 2. If remote fetch failed, check local mirror fallback if configured via UNJESS_UPDATE_MIRROR_DIR
	if !fetched {
		if mirrorDir := strings.TrimSpace(os.Getenv("UNJESS_UPDATE_MIRROR_DIR")); mirrorDir != "" {
			mirrorPath := filepath.Join(mirrorDir, "version.json")
			if data, readErr := os.ReadFile(mirrorPath); readErr == nil {
				data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
				if json.Unmarshal(data, &manifest) == nil && manifest.Version != "" {
					source = "local_mirror"
					fetched = true
				}
			}
		}
	}

	// If no manifest found, default latest version to CurrentVersion
	latestVersion := CurrentVersion
	if fetched && manifest.Version != "" {
		latestVersion = manifest.Version
	}
	if CompareVersions(CurrentVersion, latestVersion) > 0 {
		latestVersion = CurrentVersion
	}

	updateAvailable := CompareVersions(latestVersion, CurrentVersion) > 0

	manifest.Notes = sanitizeMojibake(manifest.Notes)

	// Merge changelog entries from manifest and builtin (deduplicating by version)
	seen := make(map[string]bool)
	var mergedChangelog []ChangelogEntry
	for _, entry := range manifest.Changelog {
		if entry.Version != "" && !seen[entry.Version] {
			seen[entry.Version] = true
			entry.Title = sanitizeMojibake(entry.Title)
			for ci := range entry.Changes {
				entry.Changes[ci] = sanitizeMojibake(entry.Changes[ci])
			}
			mergedChangelog = append(mergedChangelog, entry)
		}
	}
	for _, entry := range BuiltinChangelog() {
		if entry.Version != "" && !seen[entry.Version] {
			seen[entry.Version] = true
			entry.Title = sanitizeMojibake(entry.Title)
			for ci := range entry.Changes {
				entry.Changes[ci] = sanitizeMojibake(entry.Changes[ci])
			}
			mergedChangelog = append(mergedChangelog, entry)
		}
	}

	// Sort merged changelog strictly descending by version so the newest is always on top!
	sort.SliceStable(mergedChangelog, func(i, j int) bool {
		return CompareVersions(mergedChangelog[i].Version, mergedChangelog[j].Version) > 0
	})

	// Ensure the newest entry has Tag = "LATEST" and older entries do NOT retain "LATEST"
	for i := range mergedChangelog {
		if i == 0 {
			if mergedChangelog[i].Tag == "" || mergedChangelog[i].Tag == "UPDATE" {
				mergedChangelog[i].Tag = "LATEST"
			}
		} else {
			if mergedChangelog[i].Tag == "LATEST" {
				mergedChangelog[i].Tag = "UPDATE"
			}
		}
	}

	return map[string]any{
		"status":              "ok",
		"current_version":     CurrentVersion,
		"latest_version":      latestVersion,
		"update_available":    updateAvailable,
		"auto_update_enabled": autoUpdateEnabled,
		"update_server_url":   updateURL,
		"download_url":        manifest.DownloadURL,
		"installer_url":       manifest.InstallerURL,
		"daemon_url":          manifest.DaemonURL,
		"published_at":        manifest.PublishedAt,
		"notes":               manifest.Notes,
		"checked_at":          time.Now().Format("2006-01-02 15:04:05"),
		"source":              source,
		"changelog":           mergedChangelog,
	}
}

// sanitizeMojibake cleans up any double-encoded UTF-8 / Windows-1252 sequences (such as â€” or Ã¢â‚¬â€) in changelog text.
func sanitizeMojibake(s string) string {
	if s == "" {
		return s
	}
	replacer := strings.NewReplacer(
		"\xc3\x83\xc2\xa2\xc3\xa2\xc2\x82\xc2\xac\xc3\xa2\xc2\x80\xc2\x9d", "-",
		"\xc3\x83\xc2\xa2\xc3\xa2\xc2\x82\xc2\xac\xc3\xa2\xc2\x80\xc2\x9c", "-",
		"\xc3\xa2\xc2\x80\xc2\x94", "-",
		"\xc3\xa2\xc2\x80\xc2\x93", "-",
		"\xc3\xa2\xc2\x80\xc2\xa2", "-",
		"Ã¢â‚¬â€\"", "-",
		"Ã¢â‚¬â€", "-",
		"â€”", "-",
		"â€“", "-",
		"\ufffd?\"", "-",
		"\ufffd", "",
	)
	out := replacer.Replace(s)
	out = strings.ReplaceAll(out, "  -  ", " - ")
	return out
}

// stageBinary downloads or reads binary bytes and safely writes to targetPath via .old rename.
func stageBinary(sourceURL string, targetPath string, localMirrorFileName string) error {
	var newBytes []byte

	// 1. Check local mirror directory if configured via UNJESS_UPDATE_MIRROR_DIR
	if mirrorDir := strings.TrimSpace(os.Getenv("UNJESS_UPDATE_MIRROR_DIR")); mirrorDir != "" {
		mirrorPath := filepath.Join(mirrorDir, localMirrorFileName)
		if data, err := os.ReadFile(mirrorPath); err == nil && len(data) > 0 {
			newBytes = data
		}
	}

	// 2. Download from remote HTTP URL
	if len(newBytes) == 0 && (strings.HasPrefix(sourceURL, "http://") || strings.HasPrefix(sourceURL, "https://")) {
		client := &http.Client{Timeout: 90 * time.Second}
		resp, err := client.Get(sourceURL)
		if err != nil {
			return fmt.Errorf("failed downloading from %s: %w", sourceURL, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("download from %s returned HTTP %d", sourceURL, resp.StatusCode)
		}
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("reading download stream: %w", err)
		}
		newBytes = data
	} else if len(newBytes) == 0 && sourceURL != "" {
		// Local file path fallback
		if data, err := os.ReadFile(sourceURL); err == nil && len(data) > 0 {
			newBytes = data
		}
	}

	if len(newBytes) == 0 {
		return fmt.Errorf("no binary data received from %s", sourceURL)
	}

	// Ensure target directory exists
	if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
		return fmt.Errorf("creating directory for %s: %w", targetPath, err)
	}

	// Rename current target to .old
	oldExe := targetPath + ".old"
	hasOld := false
	if _, err := os.Stat(targetPath); err == nil {
		_ = os.Remove(oldExe) // Remove stale .old
		if err := os.Rename(targetPath, oldExe); err != nil {
			return fmt.Errorf("renaming %s to %s: %w", targetPath, oldExe, err)
		}
		hasOld = true
	}

	// Write new binary
	if err := os.WriteFile(targetPath, newBytes, 0755); err != nil {
		if hasOld {
			_ = os.Rename(oldExe, targetPath)
		}
		return fmt.Errorf("writing new binary to %s: %w", targetPath, err)
	}

	return nil
}

// ApplyUpdate downloads and stages the newest binary release for the current running executable.
func ApplyUpdate(updateURL string) (map[string]any, error) {
	info := CheckForUpdates(updateURL, true)
	latestVersion, _ := info["latest_version"].(string)
	if latestVersion == "" {
		latestVersion = CurrentVersion
	}

	downloadURL, _ := info["download_url"].(string)
	daemonURL, _ := info["daemon_url"].(string)
	installerURL, _ := info["installer_url"].(string)

	isAvailable, _ := info["update_available"].(bool)
	if !isAvailable && CompareVersions(latestVersion, CurrentVersion) <= 0 {
		return map[string]any{
			"status":          "ok",
			"current_version": CurrentVersion,
			"updated_to":      CurrentVersion,
			"restarting":      false,
			"message":         "Unjess is already up to date.",
		}, nil
	}

	desktopExe := FindDesktopExecutable()
	daemonExe, _ := os.Executable()
	if daemonExe != "" {
		if resolved, err := filepath.EvalSymlinks(daemonExe); err == nil {
			daemonExe = resolved
		}
	}

	var updatedAny bool

	// 1. Stage desktop executable if found
	if desktopExe != "" && downloadURL != "" {
		if err := stageBinary(downloadURL, desktopExe, "Unjess.exe"); err == nil {
			updatedAny = true
		}
	}

	// 2. Stage daemon executable
	if daemonExe != "" {
		targetURL := daemonURL
		mirrorName := "unjessd.exe"
		if targetURL == "" {
			targetURL = downloadURL
			mirrorName = "Unjess.exe"
		}
		if targetURL != "" {
			if err := stageBinary(targetURL, daemonExe, mirrorName); err == nil {
				updatedAny = true
			}
		}
	}

	// 3. Ensure sibling daemon & CLI are staged next to desktop executable
	// This permanently eliminates runtime dropper extraction for existing users upgrading via auto-update.
	if desktopExe != "" {
		desktopDir := filepath.Dir(desktopExe)
		if daemonURL != "" {
			siblingDaemon := filepath.Join(desktopDir, "unjessd.exe")
			if filepath.Clean(siblingDaemon) != filepath.Clean(daemonExe) {
				_ = stageBinary(daemonURL, siblingDaemon, "unjessd.exe")
			}
			siblingCli := filepath.Join(desktopDir, "unjess-cli.exe")
			if filepath.Clean(siblingCli) != filepath.Clean(daemonExe) {
				cliURL := strings.Replace(daemonURL, "unjessd.exe", "unjess-cli.exe", 1)
				if cliURL != daemonURL && cliURL != "" {
					_ = stageBinary(cliURL, siblingCli, "unjess-cli.exe")
				}
			}
		}
	}

	if !updatedAny {
		// Fallback: try staging downloadURL or installerURL into daemonExe
		fallbackURL := downloadURL
		if fallbackURL == "" {
			fallbackURL = installerURL
		}
		if daemonExe != "" && fallbackURL != "" {
			if err := stageBinary(fallbackURL, daemonExe, "Unjess.exe"); err != nil {
				return nil, fmt.Errorf("failed to stage update: %w", err)
			}
			updatedAny = true
		} else {
			return nil, fmt.Errorf("no update targets could be staged for version %s", latestVersion)
		}
	}

	restartExe := desktopExe
	if restartExe == "" {
		restartExe = daemonExe
	}

	return map[string]any{
		"status":          "ok",
		"current_version": CurrentVersion,
		"updated_to":      latestVersion,
		"restarting":      true,
		"target_exe":      restartExe,
		"message":         fmt.Sprintf("Update v%s applied successfully. Restarting Unjess...", latestVersion),
	}, nil
}

// ApplyUpdateToTarget stages an update into targetExe using safe Windows rename replacement.
func ApplyUpdateToTarget(updateURL string, targetExe string) (map[string]any, error) {
	info := CheckForUpdates(updateURL, true)
	latestVersion, _ := info["latest_version"].(string)
	if latestVersion == "" {
		latestVersion = CurrentVersion
	}

	downloadURL, _ := info["download_url"].(string)
	if downloadURL == "" {
		downloadURL, _ = info["daemon_url"].(string)
	}
	if downloadURL == "" {
		downloadURL, _ = info["installer_url"].(string)
	}

	if err := stageBinary(downloadURL, targetExe, "Unjess.exe"); err != nil {
		if !info["update_available"].(bool) {
			return map[string]any{
				"status":          "ok",
				"updated_to":      CurrentVersion,
				"message":         "Unjess is already up to date.",
				"current_version": CurrentVersion,
			}, nil
		}
		return nil, err
	}

	return map[string]any{
		"status":     "ok",
		"updated_to": latestVersion,
		"restarting": false,
		"target_exe": targetExe,
		"message":    "Update downloaded and staged. Restart Unjess to complete.",
	}, nil
}

// CalculateSHA256 returns hex-encoded SHA256 checksum of data.
func CalculateSHA256(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}
