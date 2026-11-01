# Unjess 2.0 Project Rules

## Project Context
This is **Unjess 2.0 (`v2.9.9`)**, a native autonomous AI coding and security engineering harness built in **Go (`daemon/`)** and **Rust (`crates/`)**.
- Desktop shell: `crates/unjess-desktop` (`Unjess.exe`, Rust Wry/Tao WebView2)
- Core Rust crate: `crates/unjess-core`
- Backend daemon: `daemon/cmd/unjessd` (`unjessd.exe`, Go 1.22+)
- Terminal CLI: `daemon/cmd/unjess-cli` (`unjess.exe` / `unjess`, Go 1.22+)
- Embedded Desktop UI: `src/unjess/desktop/index.html` (synced to `daemon/internal/api/embedded_index.html`)

## Build & Test Rules
- Build pipeline: `.\build-all.ps1` (compiles Go daemon, Go CLI, Rust desktop shell, native installer/uninstaller, and updates `version.json` SHA-256 hashes).
- Run `go test ./internal/...` inside `daemon/` to verify all Go unit tests pass before committing or building.
- Never leave stubs or incomplete implementations.
