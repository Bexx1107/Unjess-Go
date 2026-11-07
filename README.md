# Unjess — Native Go Daemon + Rust Desktop Harness

![Unjess Banner](assets/logo_banner.png)

> **Status:** Open-source personal project created by a solo builder with AI pair-programming. Built for local-first workflows and shared under the MIT license for anyone seeking a lightweight coding assistant without mandatory subscription walls or heavy web runtimes.

**Unjess** is a lightweight, native AI coding harness consisting of a **~8.6 MB Go backend daemon (`unjessd`)**, a **~1.3 MB native Rust Wry/WebView2 desktop application (`Unjess.exe`)**, and an interactive terminal CLI (`unjess`).

---

## Why Unjess?

- **Zero Heavy Runtimes:** Built in compiled Go and Rust — no Electron runtime, no Python virtualenvs, minimal idle memory consumption.
- **Provider Freedom:** Use local offline models via **Ollama**, or connect your own API keys to **DeepSeek**, **Anthropic**, **OpenAI**, **Google Gemini**, or any OpenAI-compatible endpoint.
- **Privacy & Offline First:** Can run 100% locally with local models — no code leaves your machine when configured for offline use.
- **Desktop & CLI Parity:** Use the native desktop window with embedded monochrome UI, or run directly in your terminal via `unjess`.
- **Resilient Agent Execution:** Built-in tool-call error recovery (fuzzy JSON argument repair, tool name aliasing, token overflow handling).

---

## Architecture

```
unjess/
├── daemon/                         # Go Backend Server & CLI (Go 1.22+)
│   ├── cmd/
│   │   ├── unjessd/                # Local HTTP + SSE Daemon Server (127.0.0.1:8765)
│   │   ├── unjess-cli/             # Terminal REPL & Command CLI (unjess)
│   │   ├── installer/              # Windows Setup Wizard (Unjess-Setup.exe)
│   │   └── uninstaller/            # Windows Uninstaller (Unjess-Uninstall.exe)
│   └── internal/
│       ├── agent/                  # Autonomous ReAct Loop & Context Compactor
│       ├── api/                    # REST + SSE Streaming API & Embedded Web UI
│       ├── cli/                    # Terminal REPL & Diff Viewer
│       ├── codeintel/              # AST Symbol & Caller Exploration
│       ├── config/                 # Settings & Multi-Provider Registry
│       ├── llm/                    # Streaming LLM Adapters (Ollama, Anthropic, OpenAI, etc.)
│       ├── mcp/                    # Model Context Protocol (MCP) Client
│       ├── memory/                 # Persistent Workspace Memory Store
│       ├── skills/                 # Custom Skill Runner (SKILL.md)
│       ├── tools/                  # Workspace Tools (read, edit, bash, search)
│       └── updater/                # In-App SHA-256 Release Updater
├── crates/                         # Rust Workspace
│   ├── unjess-desktop/             # Native Wry/Tao (WebView2) Desktop Shell (Unjess.exe)
│   └── unjess-core/                # Rust Shared Workspace Primitives
├── src/unjess/
│   └── desktop/index.html          # Embedded Monochrome UI
├── build-all.ps1                   # Unified Cross-Compilation Build Script
└── version.json                    # Release Metadata & Changelog
```

---

## Getting Started

### Pre-built Binaries (Windows & Linux)
Download the latest pre-compiled binaries and installers from [GitHub Releases](https://github.com/Bexx1107/Unjess-Go/releases/latest).

- **Windows:** Run `Unjess-Setup.exe` (or launch `Unjess.exe` directly).
- **Linux:** Run `./unjessd-linux-amd64` to start the daemon, and `./unjess-linux-amd64` for the CLI.

---

## Building from Source

### Prerequisites
- **Go 1.22+**
- **Rust (stable toolchain via `rustup`)**
- **Windows 10/11** (WebView2 Runtime installed) or **Linux (`amd64` / `arm64`)**

### Windows (Full Pipeline)
Run the PowerShell build script from the repository root:
```powershell
.\build-all.ps1
```

To build only the Go components:
```powershell
.\build-all.ps1 -SkipRust
```

### Manual Compilation

**Go Daemon & CLI (`daemon/`):**
```bash
cd daemon
go test ./internal/...
go build -trimpath -ldflags="-s -w" -o ../bin/unjessd ./cmd/unjessd
go build -trimpath -ldflags="-s -w" -o ../bin/unjess ./cmd/unjess-cli
```

**Rust Desktop Shell (`crates/unjess-desktop`):**
```bash
cd crates/unjess-desktop
cargo build --release
```

---

## Usage

### Desktop Application
Launch `Unjess.exe`. It automatically manages the local `unjessd` background daemon on `127.0.0.1:8765` and opens the native desktop window.

### Terminal CLI (`unjess`)
```bash
# Interactive REPL in your current workspace
unjess

# Single-shot task execution
unjess "Refactor the authentication middleware and run tests"

# Specify prompt directly
unjess --prompt "Analyze the git status"
```

### Common Commands
- `/plan` — Toggle read-only planning mode
- `/compact` — Trigger context compaction
- `/context` — Inspect token usage and context breakdown
- `/mcp` — Inspect connected Model Context Protocol servers
- `/help` — View available commands

---

## Roadmap & Known Limitations

- **Early Development:** This project is actively being developed and refined. Breaking changes may occur between minor versions.
- **UI in Progress:** The desktop user interface is continually being iterated on.
- **Community:** Feedback, bug reports, and contributions are welcome via GitHub Issues.

---

## License

Distributed under the [MIT License](LICENSE).
