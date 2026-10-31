# Unjess 2.0 (`v2.3.8`) — Native Go Daemon + Rust Desktop Harness

![Unjess Banner](assets/logo_banner.png)

**Unjess 2.0** is a high-concurrency, native autonomous AI coding and security engineering harness built in **Go (`daemon/`)** and **Rust (`crates/unjess-desktop`, `crates/unjess-core`)**.

Unlike legacy Python/Electron wrappers, Unjess 2.0 compiles down to a lightweight **~8.6 MB Go backend daemon (`unjessd`)**, a **~1.3 MB native Rust Wry/Tao desktop shell (`Unjess.exe`)**, and a **full-parity native terminal CLI (`unjess-cli` / `unjess`)** with zero runtime dependencies.

---

## Architecture Overview

```
Unjess2.0/
├── daemon/                         # High-Concurrency Go Backend & CLI (Go 1.22+)
│   ├── cmd/
│   │   ├── unjessd/                # HTTP + SSE Daemon Server (127.0.0.1:8765)
│   │   ├── unjess-cli/             # Native Terminal REPL & Headless Benchmark CLI
│   │   ├── installer/              # Native Windows GUI Installer (Unjess-Setup.exe)
│   │   └── uninstaller/            # Native Windows Uninstaller (Unjess-Uninstall.exe)
│   └── internal/
│       ├── agent/                  # Autonomous ReAct Loop, Subagent Pool & Compaction Engine
│       ├── api/                    # REST + SSE Streaming API & Embedded Monochrome UI
│       ├── cli/                    # Interactive Terminal REPL, Diff Renderer & Trace Compaction
│       ├── codeintel/              # AST Symbol Outliner, Call-Graph & Surgical Symbol Editor
│       ├── config/                 # Multi-Provider Model Catalog & Dynamic Settings
│       ├── frontier/               # CDP Browser-Use Engine, Visual QA, Git Archeology & Tool Synthesis
│       ├── llm/                    # Unified Streaming Providers (Anthropic, OpenAI, Gemini, Ollama, etc.)
│       ├── mcp/                    # JSON-RPC 2.0 (Stdio + HTTP/SSE) MCP Client & Binary Payload Interceptor
│       ├── memory/                 # Persistent Cross-Session Memory Engine (~/.unjess/memory/)
│       ├── scheduler/              # Persistent Cron, Wall-Clock & Interval Task Scheduler
│       ├── security/               # Red Team vs. White Team Security Duel, Taint Scan & Smart Fuzzing
│       ├── skills/                 # Modular SKILL.md Engine & GitHub Skill Installer
│       ├── telemetry/              # Token/USD Cost Ledger & Emergency Process Killswitch
│       ├── tools/                  # Core & Deferred Tool Registry, Smart Fuzzy Edit & Async Task Manager
│       ├── updater/                # Atomic SHA-256 Auto-Updater & Detached Process Restart
│       ├── workspace/              # Workspace Sandboxing, Snapshot Undo Stack & Negative Path Fences
│       └── worktree/               # Shadow Git Worktrees & Speculative Multi-Branch Execution
├── crates/                         # Native Rust Desktop Shell & Core Workspace Crates
│   ├── unjess-desktop/             # Native Wry/Tao WebView2 Desktop Application (Unjess.exe)
│   └── unjess-core/                # Rust Core Event Bus, Tool Types & Workspace Primitives
├── src/unjess/
│   ├── desktop/index.html          # Monochrome Carbon Desktop UI (embedded into unjessd at build time)
│   └── skills/builtin/             # Preinstalled Skill (reverse-engineer-anything)
├── build-all.ps1                   # End-to-End Native Build Pipeline (Go + Rust + Installer + SHA-256)
└── version.json                    # Release Manifest & Full Version Changelog
```

---

## Key Capabilities (`v2.3.8`)

### 1. Autonomous ReAct Engine & 7-Point Harness Architecture
- **12-Tool Listed Core + Deferred Tool Search (`tool_search`)**: Keeps per-turn prompt schemas ultra-lean (`run_command`, `read_file`, `write_file`, `edit_file`, `grep_search`, `find_by_name`, `list_dir`, `todo_write`, `invoke_subagent`, `web_search`, `read_url`, `tool_search`) while dynamically unlocking 18+ specialized AST, Security, Worktree, Memory, and Browser tools on demand.
- **KV-Cache-Preserving Smart Micro-Compaction**: Replaces superseded file-read outputs in-place without mutating active recent context, preserving vLLM/Ollama prefix KV caches until the 140K-character auto-compaction threshold triggers a structured 9-section technical summary.
- **30 KB / 300-Line Error-Aware Terminal Windowing**: Preserves full pytest/compiler tracebacks inline (`120 head + error slice + 140 tail`) while caching multi-megabyte logs to `.unjess/logs/` for line-range inspection via `read_file`.
- **Hardened Multi-Pass `edit_file` Engine**: Enforces a Read-Before-Edit ledger, automatically strips accidental `123: ` line-number prefixes copied from `read_file`, preserves relative block indentation on fuzzy matches, supports `replace_all: true`, and returns closest-matching line previews on failure.
- **Tool Aliasing & Metered Recovery State Machine**: Automatically aliases hallucinated tool names (`bash` -> `run_command`, `str_replace_editor` -> `edit_file`, `glob` -> `find_by_name`), repairs malformed/double-escaped JSON arguments, and recovers cleanly from `max_tokens` truncation (`max_output_tokens_recovery`) and malformed tool calls (`malformed_tool_use_retry`).
- **Execution-Surface Verification & Trajectory Pivot Loop**: Blocks premature task completion if modified files have syntax errors or unverified test failures; on repeated verification failure, snapshots are reverted (`RevertAllSnapshots()`) and `<failed_attempt_memory>` is injected so the agent pivots cleanly within a single run.

### 2. High-Concurrency Subagents & Specialist Prompts
- **Parallel Goroutine Workers (`invoke_subagent`)**: Spawn up to 8 concurrent subagents (`max_turns` up to 300) with isolated context windows, real-time SSE transcript streaming, and fail-safe report synthesis.
- **Role-Specialized Subagent Prompts**: Dedicated prompts for `explore` / `research` (fast read-only reconnaissance with strict line-number citations), `plan` / `architect` (implementation blueprint & trade-off analysis), and `coder` / `verifier` workers.

### 3. Frontier Engineering & Red vs. White Security Arena
- **AST Code Intelligence (`daemon/internal/codeintel`)**: `ast_outline`, `ast_find_callers`, `ast_edit_symbol`, and `ast_rename_symbol` across Go, Rust, Python, TypeScript, and C/C++.
- **Shadow Git Worktrees (`daemon/internal/worktree`)**: `speculative_execute` runs competing implementation branches in isolated Git worktrees and merges the winning solution.
- **Red Team vs. White Team Security Duel (`daemon/internal/security`)**: `sec_attack_surface`, `sec_taint_scan`, `sec_smart_fuzz`, `sec_supply_chain_audit`, and automated offensive/defensive remediation via `run_security_duel`.
- **CDP Browser-Use & Visual QA (`daemon/internal/frontier`)**: Interactive DOM element overlays, automated browser navigation (`browser_use`), and multimodal UI screenshot auditing (`visual_qa_screenshot`).

### 4. Multi-Provider LLM Router & Native Reasoning Effort
- **12+ Cloud & Local Providers**: Native streaming support for **Ollama**, **Anthropic** (Claude Opus/Sonnet/Haiku), **OpenAI** (GPT-5, o3, o4-mini), **Google Gemini**, **DeepSeek**, **Qwen / DashScope**, **Moonshot / Kimi**, **Zhipu GLM**, **Groq**, **OpenRouter**, **Mistral**, and custom OpenAI-compatible endpoints.
- **Reasoning Effort Control**: Configurable `Auto`, `Low`, `Medium`, `High`, `Max`, and `Off` reasoning effort levels mapped to native provider thinking budgets without duplicate `<think>` tag conflicts.

---

## Building from Source

### Prerequisites
- **Go 1.22+**
- **Rust (stable toolchain via `rustup`)**
- **Windows 10/11** (WebView2 Runtime for desktop shell) or **Linux (`amd64` / `arm64`)**

### Full Windows Build (`Unjess.exe` + `unjessd.exe` + `unjess.exe` + `Unjess-Setup.exe`)
Run the unified PowerShell build pipeline from the repository root:

```powershell
.\build-all.ps1
```

Skip Rust compilation if you only modified Go daemon/CLI or UI code:

```powershell
.\build-all.ps1 -SkipRust
```

### Manual Component Builds

**1. Build & Test the Go Daemon and CLI (`daemon/`):**
```powershell
cd daemon
go test ./internal/...
go build -trimpath -ldflags="-s -w -buildid=" -o ../dist/unjessd.exe ./cmd/unjessd
go build -trimpath -ldflags="-s -w -buildid=" -o ../bin/unjess.exe ./cmd/unjess-cli
```

**2. Build the Rust Desktop Shell (`crates/unjess-desktop`):**
```powershell
cd crates/unjess-desktop
cargo build --release
```

---

## Usage

### Desktop App
Launch `Unjess.exe` (automatically starts `unjessd.exe` on `127.0.0.1:8765` and opens the native WebView2 window).

### Terminal CLI (`unjess`)
```powershell
# Interactive REPL in the current workspace
unjess

# Single-shot autonomous execution
unjess "Refactor the authentication middleware and run all unit tests"

# Headless benchmark / CI mode with custom model and turn budget
unjess --prompt "Fix the failing pytest suite" --model deepseek-v4.1-flash:cloud --max-turns 100
```

### Interactive Slash Commands
- `/plan` — Switch to read-only Plan Mode
- `/duel` — Launch Red Team vs. White Team Security Duel
- `/compact` — Summarize and compress conversation context
- `/budget` & `/context` — Inspect token/USD telemetry and per-layer context usage
- `/schedule` — Manage persistent background cron/interval tasks
- `/mcp` & `/skill` — Inspect connected MCP servers and installed `SKILL.md` modules
- `/caveman` & `/ponytail` — Toggle ultra-concise output or persona modes

---

## License

MIT
