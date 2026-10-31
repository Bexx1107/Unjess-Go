---
name: reverse-engineer-anything
description: Reverse engineer native, managed, Electron/JavaScript, packaged, firmware, and browser targets with REA (morluto/rea). Use shipped-artifact or requested runtime evidence to explain features, compare versions, decompile code, or guide a reconstruction.
triggers:
  - reverse engineer
  - rea
  - decompile
  - binary analysis
  - asar
  - inspect binary
  - reverse-engineer-anything
repo_url: https://github.com/morluto/rea
builtin: true
metadata:
  version: "42"
  author: morluto
  repository: https://github.com/morluto/rea
---

# REA: Reverse Engineer Anything

Use REA when a claim depends on a shipped binary or package, decompilation,
passive application runtime evidence, or comparison with behavior not established
by available source. For ordinary analysis of a complete source repository, use
normal repository tools and do not run REA readiness or provider commands.

## Connect only when needed

Installing this skill supplies instructions; it does not register the REA MCP
server, install analysis engines, or add tools to an already-running agent.

If REA tools are available and their registration is not known to be stale,
proceed directly to the target. Do not run diagnostics or setup before every
investigation. Use the connected server's actual tool list and input schemas;
a skill installed from repository main may describe capabilities absent from
an older npm release. Keep complete inline Evidence when that server does not
advertise retained references.

If tools are absent, a call is blocked by the client, or a required provider is
unavailable:
1. Diagnose without changing REA or client configuration. `npx` may download
   the package and write to npm's cache. Run:
   `npx -y rea-agents@latest doctor --json`
2. Distinguish the reason. Missing, malformed, or stale registration needs a
   scoped configuration repair. An aligned registration with no tools in the
   active session needs a restart/reconnection. Static JavaScript inspection
   needs neither Hopper nor Ghidra, and Android inspection has separate
   bring-your-own JADX/Java prerequisites.
3. A concrete CLI fallback for an operator-supplied JavaScript tree or ASAR is:
   ```bash
   npx -y rea-agents@latest analyze-javascript-application /absolute/path/to/app --json
   ```
   No MCP registration or native engine is required. Read the returned Evidence,
   graph, limitations, and unknowns with the same care as an MCP result.

### Other Direct CLI Workflows
- **Managed .NET Assemblies (PE/CLI):**
  ```bash
  npx -y rea-agents@latest inspect-managed-artifact /absolute/path/Assembly.dll --json
  npx -y rea-agents@latest inspect-managed-members /absolute/path/Assembly.dll --json
  ```
- **Go Binaries (Compiler & Module Metadata):**
  ```bash
  npx -y rea-agents@latest inspect-go-binary /absolute/path/to/program --json
  ```
- **Rust Binaries (Embedded Dependency Metadata):**
  ```bash
  npx -y rea-agents@latest inspect-rust-binary /absolute/path/to/program --json
  ```
- **SQLite Snapshots:**
  ```bash
  npx -y rea-agents@latest inspect-sqlite-database /absolute/path/to/db.sqlite --json
  ```
- **Native Binaries (with Ghidra / Hopper / IDA):**
  ```bash
  npx -y rea-agents@latest analyze /absolute/path/to/program --provider ghidra --json
  npx -y rea-agents@latest search /absolute/path/to/program "search" --provider ghidra --json
  npx -y rea-agents@latest function /absolute/path/to/program main --provider ghidra --json
  npx -y rea-agents@latest decompile /absolute/path/to/program 0x1000 --provider ghidra --json
  ```

## Route the target first

Choose the first tool from the target the user supplied. Use `open_binary` for
active-target native or archive workflows; target-free tools take their own
explicit path or endpoint and do not need it.

- **ASAR or extracted JavaScript/Electron tree:** `analyze_javascript_application`.
- **Archive/package member inventory (ZIP/APK/IPA/MSIX/AppX or DMG):**
  `open_binary` with the supplied local path; use `inspect_artifact` when its
  graph and findings help answer the question.
- **Android APK code, classes, methods, or incoming references:**
  `inspect_android_package`, then focused Android tools when advertised.
- **Managed PE/CLI assembly:** `inspect_managed_artifact`.
- **Firmware image:** `inspect_firmware_regions` when advertised. Use
  `extract_firmware` when extraction is requested, with a caller-selected new
  absolute output directory.
- **.NET NativeAOT PE/ELF:** native Ghidra analysis.
- **User-owned browser page already open:** `list_browser_targets`.
- **Browser action the agent must perform (click, type, navigate):**
  `capture_browser_scenario` with declared actions.
- **Retained HAR/SAZ/native mitmproxy/aioquic qlog capture:** `inspect_web_network_capture`.
- **User-owned Electron runtime already open:** `list_electron_targets`. Before an electron-main Inspector attempt, check packaged fuses with `inspect_electron_fuses`.
- **Explicit local WebAssembly artifact:** `inspect_wasm_artifact`.
- **Explicit local EVM bytecode carrier:** `inspect_evm_interface` with caller-selected `raw` or `hex` encoding.
- **Embedded Go compiler, module or build-setting metadata:** `inspect_go_binary` with the explicit local file.
- **Embedded cargo-auditable Rust dependency metadata:** `inspect_rust_binary` with the explicit local ELF, PE, or thin Mach-O file.
- **Explicit local SQLite database snapshot:** `inspect_sqlite_database`.
- **Explicit Linux ELF file for offline layout, symbols, relocations or static mitigation evidence:** `inspect_binary_layout`.
- **PE resource type/name/language trees and static icon group references:** `inspect_pe_resources` with an explicit path.
- **Native executable, library, or analysis database:** `open_binary`, then use focused analysis tools directly; call `binary_overview` when metadata or inventory context is useful.

## Work summary-first

Start with the default result and use its inline Evidence and graph context.
For a JavaScript application or ELF layout whose size is unknown or large,
call `analyze_javascript_application` or `inspect_binary_layout` with
`"detail": "summary"`, then inspect the retained `parent_evidence_id` with
`inspect_analysis_view`.

Every conclusion must distinguish observations, inferences, and unknowns. Cite
Evidence IDs, preserve limitations and incomplete coverage, and never imply
that static analysis observed execution.

## Plan broader investigations

For requests that span multiple features or subsystems, use a staged workflow:
1. Turn the request into a checklist of questions and the evidence each answer needs.
2. Inspect the current REA session, artifact identity, saved analysis database, bookmarks, and prior evidence.
3. Start with the smallest useful overview or inventory. Follow each question from its entry point through relevant data and state changes to its result.
4. Inspect relevant packaged resources and configuration alongside code when they affect the question.
5. Corroborate a conclusion with the evidence type it requires.
6. Decompose work into independent questions.
7. Keep a concise finding ledger linking each conclusion to Evidence IDs, confidence/evidence type, search boundary, and remaining unknowns.

## Finish the task

Explain findings in plain language and tie them to returned evidence. When the
user asks to build something, use normal coding tools and separate observed
behavior from design choices. Close an opened native session with
`close_binary` when the investigation is complete.
