// Package skills implements the Unjess Skills engine, discovery, frontmatter parsing,
// and GitHub skill installer.
package skills

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// FallbackBrowserUseSkillMD is the full built-in fallback definition of the browser-use skill.
const FallbackBrowserUseSkillMD = `---
name: browser-use
description: Interactive web browsing, DOM element indexing ([1], [2]...), form filling, clicking, scraping, and live Set-of-Mark visual verification using the browser_use tool
triggers:
  - auto
  - browser
  - web
  - navigate
  - som
  - click
builtin: true
metadata:
  version: "1.0"
  author: unjess
---

# Browser-Use: Interactive Web Automation & Set-of-Mark Vision

Use ` + "`browser_use`" + ` for autonomous, multi-step browser interactions, DOM element mapping, form filling, clicking, web scraping, and visual verification.

## Core Interaction Loop

1. **Navigate to the target website or local web app:**
   ` + "```json\n   {\n     \"action\": \"navigate\",\n     \"url\": \"https://example.com\"\n   }\n   ```" + `
   The engine automatically indexes all interactive elements ([1], [2], [3]...) and captures an annotated Set-of-Mark screenshot attached directly to your Vision input.

2. **Inspect the indexed elements list and visual badges:**
   Review the returned ` + "`[index]`" + ` elements (e.g. ` + "`[1] <input type=\"text\"> placeholder=\"Search\"`" + `, ` + "`[2] <button> \"Search\"`" + `).

3. **Interact with elements by their numbered index:**
   - **Type into an input:**
     ` + "```json\n     {\n       \"action\": \"type\",\n       \"index\": 1,\n       \"text\": \"my query\",\n       \"press_enter\": true\n     }\n     ```" + `
   - **Click a button or link:**
     ` + "```json\n     {\n       \"action\": \"click\",\n       \"index\": 2\n     }\n     ```" + `
   - **Scroll the page:**
     ` + "```json\n     {\n       \"action\": \"scroll\",\n       \"direction\": \"down\",\n       \"amount\": 550\n     }\n     ```" + `
   - **Press a keyboard key:**
     ` + "```json\n     {\n       \"action\": \"press_key\",\n       \"key\": \"Enter\"\n     }\n     ```" + `
   - **Evaluate custom JavaScript:**
     ` + "```json\n     {\n       \"action\": \"evaluate\",\n       \"script\": \"document.title\"\n     }\n     ```" + `

4. **Verify live visual results:**
   After every action, a new Set-of-Mark screenshot is captured and attached to Vision so you can visually verify layout changes, modal popups, error messages, and completed submissions.

5. **Close when finished:**
   ` + "```json\n   {\n     \"action\": \"close\"\n   }\n   ```" + `
`

// FallbackREASkillMD is the full built-in fallback definition of the reverse-engineer-anything skill.
const FallbackREASkillMD = `---
name: reverse-engineer-anything
description: Inspect and analyze compiled native, managed, Electron/JavaScript, packaged, firmware, and browser artifacts with REA (morluto/rea). Use shipped-artifact or runtime evidence to explain features, compare versions, inspect symbols, or guide a reconstruction.
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
1. Diagnose without changing REA or client configuration. ` + "`npx`" + ` may download
   the package and write to npm's cache. Run:
   ` + "`npx -y rea-agents@latest doctor --json`" + `
2. Distinguish the reason. Missing, malformed, or stale registration needs a
   scoped configuration repair. An aligned registration with no tools in the
   active session needs a restart/reconnection. Static JavaScript inspection
   needs neither Hopper nor Ghidra, and Android inspection has separate
   bring-your-own JADX/Java prerequisites.
3. A concrete CLI fallback for an operator-supplied JavaScript tree or ASAR is:
   ` + "```bash\n   npx -y rea-agents@latest analyze-javascript-application /absolute/path/to/app --json\n   ```" + `
   No MCP registration or native engine is required. Read the returned Evidence,
   graph, limitations, and unknowns with the same care as an MCP result.

### Other Direct CLI Workflows
- **Managed .NET Assemblies (PE/CLI):**
  ` + "```bash\n  npx -y rea-agents@latest inspect-managed-artifact /absolute/path/Assembly.dll --json\n  npx -y rea-agents@latest inspect-managed-members /absolute/path/Assembly.dll --json\n  ```" + `
- **Go Binaries (Compiler & Module Metadata):**
  ` + "```bash\n  npx -y rea-agents@latest inspect-go-binary /absolute/path/to/program --json\n  ```" + `
- **Rust Binaries (Embedded Dependency Metadata):**
  ` + "```bash\n  npx -y rea-agents@latest inspect-rust-binary /absolute/path/to/program --json\n  ```" + `
- **SQLite Snapshots:**
  ` + "```bash\n  npx -y rea-agents@latest inspect-sqlite-database /absolute/path/to/db.sqlite --json\n  ```" + `
- **Native Binaries (with Ghidra / Hopper / IDA):**
  ` + "```bash\n  npx -y rea-agents@latest analyze /absolute/path/to/program --provider ghidra --json\n  npx -y rea-agents@latest search /absolute/path/to/program \"search\" --provider ghidra --json\n  npx -y rea-agents@latest function /absolute/path/to/program main --provider ghidra --json\n  npx -y rea-agents@latest decompile /absolute/path/to/program 0x1000 --provider ghidra --json\n  ```" + `

## Route the target first

Choose the first tool from the target the user supplied. Use ` + "`open_binary`" + ` for
active-target native or archive workflows; target-free tools take their own
explicit path or endpoint and do not need it.

- **ASAR or extracted JavaScript/Electron tree:** ` + "`analyze_javascript_application`" + `.
- **Archive/package member inventory (ZIP/APK/IPA/MSIX/AppX or DMG):**
  ` + "`open_binary`" + ` with the supplied local path; use ` + "`inspect_artifact`" + ` when its
  graph and findings help answer the question.
- **Android APK code, classes, methods, or incoming references:**
  ` + "`inspect_android_package`" + `, then focused Android tools when advertised.
- **Managed PE/CLI assembly:** ` + "`inspect_managed_artifact`" + `.
- **Firmware image:** ` + "`inspect_firmware_regions`" + ` when advertised. Use
  ` + "`extract_firmware`" + ` when extraction is requested, with a caller-selected new
  absolute output directory.
- **.NET NativeAOT PE/ELF:** native Ghidra analysis.
- **User-owned browser page already open:** ` + "`list_browser_targets`" + `.
- **Browser action the agent must perform (click, type, navigate):**
  ` + "`capture_browser_scenario`" + ` with declared actions.
- **Retained HAR/SAZ/native mitmproxy/aioquic qlog capture:** ` + "`inspect_web_network_capture`" + `.
- **User-owned Electron runtime already open:** ` + "`list_electron_targets`" + `. Before an electron-main Inspector attempt, check packaged fuses with ` + "`inspect_electron_fuses`" + `.
- **Explicit local WebAssembly artifact:** ` + "`inspect_wasm_artifact`" + `.
- **Explicit local EVM bytecode carrier:** ` + "`inspect_evm_interface`" + ` with caller-selected ` + "`raw`" + ` or ` + "`hex`" + ` encoding.
- **Embedded Go compiler, module or build-setting metadata:** ` + "`inspect_go_binary`" + ` with the explicit local file.
- **Embedded cargo-auditable Rust dependency metadata:** ` + "`inspect_rust_binary`" + ` with the explicit local ELF, PE, or thin Mach-O file.
- **Explicit local SQLite database snapshot:** ` + "`inspect_sqlite_database`" + `.
- **Explicit Linux ELF file for offline layout, symbols, relocations or static mitigation evidence:** ` + "`inspect_binary_layout`" + `.
- **PE resource type/name/language trees and static icon group references:** ` + "`inspect_pe_resources`" + ` with an explicit path.
- **Native executable, library, or analysis database:** ` + "`open_binary`" + `, then use focused analysis tools directly; call ` + "`binary_overview`" + ` when metadata or inventory context is useful.

## Work summary-first

Start with the default result and use its inline Evidence and graph context.
For a JavaScript application or ELF layout whose size is unknown or large,
call ` + "`analyze_javascript_application`" + ` or ` + "`inspect_binary_layout`" + ` with
` + `"\"detail\": \"summary\""` + `, then inspect the retained ` + "`parent_evidence_id`" + ` with
` + "`inspect_analysis_view`" + `.

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
` + "`close_binary`" + ` when the investigation is complete.
`

// ---------------------------------------------------------------------------
// Skill Types
// ---------------------------------------------------------------------------

// SkillInfo contains the metadata and instructions of an available skill.
type SkillInfo struct {
	Name         string         `json:"name"`
	Description  string         `json:"description"`
	Triggers     []string       `json:"triggers"`
	Path         string         `json:"path"`
	Scope        string         `json:"scope"` // "preinstalled", "global", "workspace"
	Instructions string         `json:"instructions"`
	RepoURL      string         `json:"repo_url"`
	Metadata     map[string]any `json:"metadata,omitempty"`
}

// SkillSaveRequest holds the input parameters for creating or updating a skill.
type SkillSaveRequest struct {
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	Triggers     []string `json:"triggers"`
	Instructions string   `json:"instructions"`
	Scope        string   `json:"scope"` // "global" or "workspace"
	RepoURL      string   `json:"repo_url"`
	Workspace    string   `json:"workspace,omitempty"`
}

var nonAlphaRegex = regexp.MustCompile(`[^a-zA-Z0-9_\-]+`)

func toSlug(name string) string {
	slug := nonAlphaRegex.ReplaceAllString(strings.ToLower(name), "-")
	slug = strings.Trim(slug, "-")
	if slug == "" {
		slug = "skill"
	}
	return slug
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err = io.Copy(out, in); err != nil {
		return err
	}
	if info, errS := os.Stat(src); errS == nil {
		_ = os.Chmod(dst, info.Mode())
	}
	return nil
}

func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		return copyFile(path, target)
	})
}

// findBuiltinSkillsDir locates the builtin skills directory on disk if available.
func findBuiltinSkillsDir() string {
	candidates := []string{
		"src/unjess/skills/builtin",
		"../src/unjess/skills/builtin",
		"../../src/unjess/skills/builtin",
	}

	if envRoot := os.Getenv("UNJESS_ROOT"); envRoot != "" {
		candidates = append(candidates, filepath.Join(envRoot, "src", "unjess", "skills", "builtin"))
	}

	if exe, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(exeDir, "src", "unjess", "skills", "builtin"),
			filepath.Join(exeDir, "..", "src", "unjess", "skills", "builtin"),
		)
	}

	for _, c := range candidates {
		if fi, err := os.Stat(c); err == nil && fi.IsDir() {
			if abs, err := filepath.Abs(c); err == nil {
				return abs
			}
			return c
		}
	}
	return ""
}

// seedOtherBuiltins copies any other builtin skills into ~/.unjess/skills.
func seedOtherBuiltins(builtinDir, home string) {
	entries, err := os.ReadDir(builtinDir)
	if err != nil {
		return
	}
	userSkillsDir := filepath.Join(home, ".unjess", "skills")
	for _, entry := range entries {
		srcPath := filepath.Join(builtinDir, entry.Name())
		destPath := filepath.Join(userSkillsDir, entry.Name())
		if entry.IsDir() {
			if _, err := os.Stat(destPath); os.IsNotExist(err) {
				_ = copyDir(srcPath, destPath)
			}
		} else if strings.HasSuffix(entry.Name(), ".json") {
			if _, err := os.Stat(destPath); os.IsNotExist(err) {
				_ = copyFile(srcPath, destPath)
			}
		}
	}
}

// EnsureBuiltinSkillsSeeded verifies that ~/.unjess/skills/reverse-engineer-anything/SKILL.md
// is present, copying from builtin or writing the fallback.
func EnsureBuiltinSkillsSeeded() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}

	userSkillsDir := filepath.Join(home, ".unjess", "skills")
	targetDir := filepath.Join(userSkillsDir, "reverse-engineer-anything")
	targetSkillMD := filepath.Join(targetDir, "SKILL.md")

	builtinDir := findBuiltinSkillsDir()
	if _, err := os.Stat(targetSkillMD); os.IsNotExist(err) {
		if err := os.MkdirAll(targetDir, 0755); err == nil {
			seeded := false
			if builtinDir != "" {
				srcSkillMD := filepath.Join(builtinDir, "reverse-engineer-anything", "SKILL.md")
				if data, err := os.ReadFile(srcSkillMD); err == nil && len(data) > 0 {
					_ = os.WriteFile(targetSkillMD, data, 0644)
					seeded = true
				}
			}
			if !seeded {
				_ = os.WriteFile(targetSkillMD, []byte(FallbackREASkillMD), 0644)
			}
		}
	}

	// Ensure browser-use skill is seeded
	buTargetDir := filepath.Join(userSkillsDir, "browser-use")
	buTargetSkillMD := filepath.Join(buTargetDir, "SKILL.md")
	if _, err := os.Stat(buTargetSkillMD); os.IsNotExist(err) {
		if err := os.MkdirAll(buTargetDir, 0755); err == nil {
			_ = os.WriteFile(buTargetSkillMD, []byte(FallbackBrowserUseSkillMD), 0644)
		}
	}

	if builtinDir != "" {
		seedOtherBuiltins(builtinDir, home)
	}

	return nil
}

// parseYAMLFrontmatter parses YAML frontmatter enclosed in --- at the start of markdown.
func parseYAMLFrontmatter(text string) (map[string]any, string) {
	trimmed := strings.TrimLeft(text, "\r\n ")
	if !strings.HasPrefix(trimmed, "---") {
		return nil, text
	}

	rest := strings.TrimPrefix(trimmed, "---")
	rest = strings.TrimPrefix(rest, "\r\n")
	rest = strings.TrimPrefix(rest, "\n")

	endIdx := strings.Index(rest, "\n---")
	delimiterLen := 4
	if endIdx == -1 {
		endIdx = strings.Index(rest, "\r\n---")
		delimiterLen = 5
	}
	if endIdx == -1 {
		return nil, text
	}

	yamlPart := rest[:endIdx]
	after := rest[endIdx+delimiterLen:]
	after = strings.TrimLeft(after, "\r\n")

	var meta map[string]any
	if err := yaml.Unmarshal([]byte(yamlPart), &meta); err != nil {
		return nil, text
	}

	return meta, after
}

// extractTriggers extracts trigger strings from yaml frontmatter value (slice or comma-separated).
func extractTriggers(v any) []string {
	var triggers []string
	switch val := v.(type) {
	case []any:
		for _, item := range val {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				triggers = append(triggers, strings.TrimSpace(s))
			}
		}
	case []string:
		for _, s := range val {
			if strings.TrimSpace(s) != "" {
				triggers = append(triggers, strings.TrimSpace(s))
			}
		}
	case string:
		for _, s := range strings.Split(val, ",") {
			if strings.TrimSpace(s) != "" {
				triggers = append(triggers, strings.TrimSpace(s))
			}
		}
	}
	return triggers
}

func loadSkillMDSkill(dir, skillMDPath string) *SkillInfo {
	data, err := os.ReadFile(skillMDPath)
	if err != nil {
		return nil
	}
	meta, body := parseYAMLFrontmatter(string(data))
	name := filepath.Base(dir)
	description := ""
	var triggers []string
	repoURL := ""

	if meta != nil {
		if n, ok := meta["name"].(string); ok && strings.TrimSpace(n) != "" {
			name = strings.TrimSpace(n)
		}
		if d, ok := meta["description"].(string); ok {
			description = strings.TrimSpace(d)
		}
		if t, ok := meta["triggers"]; ok {
			triggers = extractTriggers(t)
		}
		if r, ok := meta["repo_url"].(string); ok {
			repoURL = strings.TrimSpace(r)
		} else if r, ok := meta["repository"].(string); ok {
			repoURL = strings.TrimSpace(r)
		}
	}

	return &SkillInfo{
		Name:         name,
		Description:  description,
		Triggers:     triggers,
		Path:         dir,
		Instructions: body,
		RepoURL:      repoURL,
		Metadata:     meta,
	}
}

func loadDirJSONSkill(dir, skillJSONPath string) *SkillInfo {
	data, err := os.ReadFile(skillJSONPath)
	if err != nil {
		return nil
	}
	var meta map[string]any
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil
	}

	name := filepath.Base(dir)
	if n, ok := meta["name"].(string); ok && strings.TrimSpace(n) != "" {
		name = strings.TrimSpace(n)
	}
	description := ""
	if d, ok := meta["description"].(string); ok {
		description = strings.TrimSpace(d)
	}
	triggers := extractTriggers(meta["triggers"])

	instructions := ""
	if instr, ok := meta["instructions"].(string); ok && instr != "" {
		instructions = instr
	} else {
		for _, cand := range []string{"instructions.md", "README.md"} {
			candPath := filepath.Join(dir, cand)
			if cData, err := os.ReadFile(candPath); err == nil {
				instructions = string(cData)
				break
			}
		}
	}

	repoURL := ""
	if r, ok := meta["repo_url"].(string); ok {
		repoURL = strings.TrimSpace(r)
	} else if r, ok := meta["repository"].(string); ok {
		repoURL = strings.TrimSpace(r)
	}

	return &SkillInfo{
		Name:         name,
		Description:  description,
		Triggers:     triggers,
		Path:         dir,
		Instructions: instructions,
		RepoURL:      repoURL,
		Metadata:     meta,
	}
}

func loadStandaloneJSONSkill(jsonPath string) *SkillInfo {
	data, err := os.ReadFile(jsonPath)
	if err != nil {
		return nil
	}
	var meta map[string]any
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil
	}

	stem := strings.TrimSuffix(filepath.Base(jsonPath), ".json")
	name := stem
	if n, ok := meta["name"].(string); ok && strings.TrimSpace(n) != "" {
		name = strings.TrimSpace(n)
	}
	description := ""
	if d, ok := meta["description"].(string); ok {
		description = strings.TrimSpace(d)
	}
	triggers := extractTriggers(meta["triggers"])
	instructions := ""
	if instr, ok := meta["instructions"].(string); ok {
		instructions = instr
	}
	repoURL := ""
	if r, ok := meta["repo_url"].(string); ok {
		repoURL = strings.TrimSpace(r)
	}

	return &SkillInfo{
		Name:         name,
		Description:  description,
		Triggers:     triggers,
		Path:         jsonPath,
		Instructions: instructions,
		RepoURL:      repoURL,
		Metadata:     meta,
	}
}

// DiscoverSkills scans builtin, global (~/.unjess/skills), and workspace (.agents/skills) roots,
// placing reverse-engineer-anything first with scope: "preinstalled" and repo_url: "https://github.com/morluto/rea".
func DiscoverSkills(workspacePath string) []SkillInfo {
	_ = EnsureBuiltinSkillsSeeded()

	home, _ := os.UserHomeDir()
	builtinDir := findBuiltinSkillsDir()

	var roots []string
	if builtinDir != "" {
		roots = append(roots, builtinDir)
	}
	if home != "" {
		globalDir := filepath.Join(home, ".unjess", "skills")
		roots = append(roots, globalDir)
	}
	var wsSkillsDir string
	if workspacePath != "" {
		wsSkillsDir = filepath.Join(workspacePath, ".agents", "skills")
		roots = append(roots, wsSkillsDir)
	}

	skillsByName := make(map[string]SkillInfo)

	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}

		for _, entry := range entries {
			fullPath := filepath.Join(root, entry.Name())
			var skill *SkillInfo

			if entry.IsDir() {
				skillMDPath := filepath.Join(fullPath, "SKILL.md")
				skillJSONPath := filepath.Join(fullPath, "skill.json")

				if _, err := os.Stat(skillMDPath); err == nil {
					skill = loadSkillMDSkill(fullPath, skillMDPath)
				} else if _, err := os.Stat(skillJSONPath); err == nil {
					skill = loadDirJSONSkill(fullPath, skillJSONPath)
				}
			} else if strings.HasSuffix(entry.Name(), ".json") {
				skill = loadStandaloneJSONSkill(fullPath)
			}

			if skill == nil {
				continue
			}

			pNorm := filepath.ToSlash(strings.ToLower(fullPath))
			bNorm := filepath.ToSlash(strings.ToLower(builtinDir))
			wsNorm := filepath.ToSlash(strings.ToLower(wsSkillsDir))

			if skill.Name == "reverse-engineer-anything" || (bNorm != "" && strings.HasPrefix(pNorm, bNorm)) {
				skill.Scope = "preinstalled"
				if skill.RepoURL == "" {
					skill.RepoURL = "https://github.com/morluto/rea"
				}
			} else if wsNorm != "" && strings.HasPrefix(pNorm, wsNorm) {
				skill.Scope = "workspace"
			} else {
				skill.Scope = "global"
			}

			skillsByName[skill.Name] = *skill
		}
	}

	// Ensure reverse-engineer-anything is always present
	if _, ok := skillsByName["reverse-engineer-anything"]; !ok {
		meta, body := parseYAMLFrontmatter(FallbackREASkillMD)
		skillsByName["reverse-engineer-anything"] = SkillInfo{
			Name:         "reverse-engineer-anything",
			Description:  "Inspect and analyze compiled native, managed, Electron/JavaScript, packaged, firmware, and browser artifacts with REA (morluto/rea).",
			Triggers:     []string{"reverse engineer", "rea", "decompile", "binary analysis"},
			Path:         filepath.Join(home, ".unjess", "skills", "reverse-engineer-anything"),
			Scope:        "preinstalled",
			Instructions: body,
			RepoURL:      "https://github.com/morluto/rea",
			Metadata:     meta,
		}
	}

	// Ensure browser-use is always present
	if _, ok := skillsByName["browser-use"]; !ok {
		meta, body := parseYAMLFrontmatter(FallbackBrowserUseSkillMD)
		skillsByName["browser-use"] = SkillInfo{
			Name:         "browser-use",
			Description:  "Interactive web browsing, DOM element indexing ([1], [2]...), form filling, clicking, scraping, and live Set-of-Mark visual verification using the browser_use tool",
			Triggers:     []string{"auto", "browser", "web", "navigate", "som", "click"},
			Path:         filepath.Join(home, ".unjess", "skills", "browser-use"),
			Scope:        "preinstalled",
			Instructions: body,
			Metadata:     meta,
		}
	}

	result := make([]SkillInfo, 0, len(skillsByName))
	for _, s := range skillsByName {
		result = append(result, s)
	}

	// Sort: reverse-engineer-anything first, then alphabetical by name lower
	sort.Slice(result, func(i, j int) bool {
		iName := result[i].Name
		jName := result[j].Name
		if iName == "reverse-engineer-anything" {
			return true
		}
		if jName == "reverse-engineer-anything" {
			return false
		}
		return strings.ToLower(iName) < strings.ToLower(jName)
	})

	return result
}

// SaveSkill creates or updates a skill file in SKILL.md format.
func SaveSkill(req SkillSaveRequest) (SkillInfo, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return SkillInfo{}, fmt.Errorf("name is required")
	}

	slug := toSlug(name)
	if slug == "" {
		slug = "skill"
	}

	scope := strings.ToLower(req.Scope)
	var targetDir string
	if scope == "workspace" && req.Workspace != "" {
		targetDir = filepath.Join(req.Workspace, ".agents", "skills", slug)
	} else {
		home, err := os.UserHomeDir()
		if err != nil {
			return SkillInfo{}, err
		}
		targetDir = filepath.Join(home, ".unjess", "skills", slug)
		scope = "global"
	}

	if err := os.MkdirAll(targetDir, 0755); err != nil {
		return SkillInfo{}, err
	}

	skillFile := filepath.Join(targetDir, "SKILL.md")

	var sb strings.Builder
	sb.WriteString("---\n")
	sb.WriteString(fmt.Sprintf("name: %s\n", name))
	if req.Description != "" {
		sb.WriteString(fmt.Sprintf("description: %s\n", req.Description))
	}
	if len(req.Triggers) > 0 {
		sb.WriteString("triggers:\n")
		for _, t := range req.Triggers {
			sb.WriteString(fmt.Sprintf("  - %s\n", t))
		}
	}
	if req.RepoURL != "" {
		sb.WriteString(fmt.Sprintf("repo_url: %s\n", req.RepoURL))
	}
	sb.WriteString("---\n\n")
	sb.WriteString(req.Instructions)

	if err := os.WriteFile(skillFile, []byte(sb.String()), 0644); err != nil {
		return SkillInfo{}, err
	}

	return SkillInfo{
		Name:         name,
		Description:  req.Description,
		Triggers:     req.Triggers,
		Path:         targetDir,
		Scope:        scope,
		Instructions: req.Instructions,
		RepoURL:      req.RepoURL,
	}, nil
}

// DeleteSkill deletes a skill by path or name from ~/.unjess/skills or workspace.
func DeleteSkill(name, pathParam, workspace string) ([]string, error) {
	var deleted []string

	if pathParam != "" {
		p := filepath.Clean(pathParam)
		if _, err := os.Stat(p); err == nil {
			_ = os.RemoveAll(p)
			deleted = append(deleted, p)
		}
	}

	if name != "" {
		home, _ := os.UserHomeDir()
		candidates := []string{
			filepath.Join(home, ".unjess", "skills", name),
			filepath.Join(home, ".unjess", "skills", name+".json"),
		}
		if workspace != "" {
			candidates = append(candidates,
				filepath.Join(workspace, ".agents", "skills", name),
				filepath.Join(workspace, ".agents", "skills", name+".json"),
			)
		}
		for _, cand := range candidates {
			if _, err := os.Stat(cand); err == nil {
				_ = os.RemoveAll(cand)
				deleted = append(deleted, cand)
			}
		}
	}

	return deleted, nil
}

// InstallFromGitHub inspects and installs a skill repo from GitHub into ~/.unjess/skills.
func InstallFromGitHub(repoURL string) (map[string]any, error) {
	rawURL := strings.TrimSpace(repoURL)
	if rawURL == "" {
		return nil, fmt.Errorf("url is required")
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	userSkillsDir := filepath.Join(home, ".unjess", "skills")
	if err := os.MkdirAll(userSkillsDir, 0755); err != nil {
		return nil, err
	}

	// Special fast-path for REA
	if strings.Contains(strings.ToLower(rawURL), "morluto/rea") {
		targetDir := filepath.Join(userSkillsDir, "reverse-engineer-anything")
		targetSkillMD := filepath.Join(targetDir, "SKILL.md")
		_ = os.MkdirAll(targetDir, 0755)

		var instr string
		builtinDir := findBuiltinSkillsDir()
		copied := false
		if builtinDir != "" {
			srcSkillMD := filepath.Join(builtinDir, "reverse-engineer-anything", "SKILL.md")
			if data, err := os.ReadFile(srcSkillMD); err == nil && len(data) > 0 {
				_ = os.WriteFile(targetSkillMD, data, 0644)
				instr = string(data)
				copied = true
			}
		}
		if !copied {
			_ = os.WriteFile(targetSkillMD, []byte(FallbackREASkillMD), 0644)
			instr = FallbackREASkillMD
		}

		return map[string]any{
			"status":    "ok",
			"success":   true,
			"installed": []string{"reverse-engineer-anything"},
			"skill": map[string]any{
				"name": "reverse-engineer-anything",
				"path": targetDir,
			},
			"skills": []map[string]any{
				{
					"name":         "reverse-engineer-anything",
					"description":  "Inspect and analyze compiled native, managed, Electron/JavaScript, packaged, firmware, and browser artifacts with REA (morluto/rea).",
					"triggers":     []string{"reverse engineer", "rea", "decompile", "binary analysis"},
					"path":         targetDir,
					"scope":        "preinstalled",
					"repo_url":     "https://github.com/morluto/rea",
					"instructions": instr,
				},
			},
		}, nil
	}

	// Parse owner/repo and optional /tree/<branch>/<subPath> or /blob/<branch>/<subPath>
	clean := rawURL
	clean = strings.TrimPrefix(clean, "https://")
	clean = strings.TrimPrefix(clean, "http://")
	clean = strings.TrimPrefix(clean, "github.com/")
	clean = strings.Trim(clean, "/")
	clean = strings.TrimSuffix(clean, ".git")

	parts := strings.Split(clean, "/")
	if len(parts) < 2 {
		return nil, fmt.Errorf("invalid GitHub repo format: %s", rawURL)
	}

	owner := parts[0]
	repo := parts[1]
	branchHint := ""
	subPath := ""
	if len(parts) >= 4 && (parts[2] == "tree" || parts[2] == "blob") {
		branchHint = parts[3]
		if len(parts) > 4 {
			subPath = strings.Join(parts[4:], "/")
			subPath = strings.TrimSuffix(subPath, "/SKILL.md")
			subPath = strings.TrimSuffix(subPath, "/skill.md")
			subPath = strings.Trim(subPath, "/")
		}
	}

	fetchText := func(url string, timeout time.Duration) (string, error) {
		client := &http.Client{Timeout: timeout}
		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			return "", err
		}
		req.Header.Set("User-Agent", "Unjess-Agent/2.1")
		resp, err := client.Do(req)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			return "", err
		}
		return string(b), nil
	}

	var results []map[string]any
	foundSkills := false

	// Full-Directory Multi-File Package Clone (preserving scripts/, reference/, data/, agents/)
	gitURL := fmt.Sprintf("https://github.com/%s/%s.git", owner, repo)
	tempCloneDir, errClone := os.MkdirTemp("", "unjess-skill-clone-*")
	if errClone == nil {
		defer os.RemoveAll(tempCloneDir)
		var cloneCmd *exec.Cmd
		if branchHint != "" {
			cloneCmd = exec.Command("git", "clone", "--depth", "1", "--branch", branchHint, gitURL, tempCloneDir)
		} else {
			cloneCmd = exec.Command("git", "clone", "--depth", "1", gitURL, tempCloneDir)
		}
		if cloneErr := cloneCmd.Run(); cloneErr == nil {
			skillSources := make(map[string]string)
			if subPath != "" {
				candDir := filepath.Join(tempCloneDir, filepath.FromSlash(subPath))
				if fi, errS := os.Stat(filepath.Join(candDir, "SKILL.md")); errS == nil && !fi.IsDir() {
					name := filepath.Base(candDir)
					skillSources[name] = candDir
				}
			} else if fi, errS := os.Stat(filepath.Join(tempCloneDir, "SKILL.md")); errS == nil && !fi.IsDir() {
				skillSources[repo] = tempCloneDir
			} else {
				_ = filepath.Walk(tempCloneDir, func(p string, info os.FileInfo, err error) error {
					if err == nil && !info.IsDir() && strings.EqualFold(info.Name(), "SKILL.md") {
						dir := filepath.Dir(p)
						if dir != tempCloneDir {
							name := filepath.Base(dir)
							skillSources[name] = dir
						}
					}
					return nil
				})
			}

			if len(skillSources) > 0 {
				for sName, sSrc := range skillSources {
					destDir := filepath.Join(userSkillsDir, sName)
					_ = os.RemoveAll(destDir)
					if errCopy := copyDir(sSrc, destDir); errCopy == nil {
						scriptsDir := filepath.Join(destDir, "scripts")
						if fi, errSc := os.Stat(scriptsDir); errSc == nil && fi.IsDir() {
							_ = filepath.Walk(scriptsDir, func(sp string, sinfo os.FileInfo, serr error) error {
								if serr == nil && !sinfo.IsDir() {
									_ = os.Chmod(sp, 0755)
								}
								return nil
							})
						}
						results = append(results, map[string]any{
							"name": sName,
							"path": destDir,
						})
						foundSkills = true
					}
				}
			}
		}
	}

	// 0. Fast-path when a specific subPath is provided (e.g. /tree/main/skills/apple-design)
	if subPath != "" {
		candBranches := []string{"HEAD", "main", "master"}
		if branchHint != "" {
			candBranches = append([]string{branchHint}, candBranches...)
		}
		for _, br := range candBranches {
			rawSkillURL := fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s/%s/SKILL.md", owner, repo, br, subPath)
			if rawContent, err := fetchText(rawSkillURL, 10*time.Second); err == nil && strings.TrimSpace(rawContent) != "" {
				subParts := strings.Split(subPath, "/")
				skillName := subParts[len(subParts)-1]
				targetDir := filepath.Join(userSkillsDir, skillName)
				_ = os.MkdirAll(targetDir, 0755)
				_ = os.WriteFile(filepath.Join(targetDir, "SKILL.md"), []byte(rawContent), 0644)
				results = append(results, map[string]any{
					"name": skillName,
					"path": targetDir,
				})
				foundSkills = true
				break
			}
		}
	}

	// 1. Try GitHub Tree API
	if !foundSkills {
		treeRef := "HEAD"
		if branchHint != "" {
			treeRef = branchHint
		}
		treeURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/git/trees/%s?recursive=1", owner, repo, treeRef)
		if treeData, err := fetchText(treeURL, 10*time.Second); err == nil {
			var treeResp struct {
				Tree []struct {
					Path string `json:"path"`
				} `json:"tree"`
			}
			if err := json.Unmarshal([]byte(treeData), &treeResp); err == nil {
				type candidateSkill struct {
					normPath  string
					skillName string
					isPrimary bool
				}
				var candidates []candidateSkill
				repoBase := strings.TrimSuffix(strings.ToLower(repo), "-skill")
				hasPrimary := false

				for _, item := range treeResp.Tree {
					normPath := filepath.ToSlash(item.Path)
					if subPath != "" && !strings.HasPrefix(strings.ToLower(normPath), strings.ToLower(subPath)) {
						continue
					}
					if strings.HasSuffix(strings.ToLower(normPath), "skill.md") {
						pParts := strings.Split(normPath, "/")
						skillName := repo
						if len(pParts) >= 2 && strings.EqualFold(pParts[len(pParts)-1], "skill.md") {
							skillName = pParts[len(pParts)-2]
							if skillName == "." || skillName == "skills" || skillName == ".claude" || skillName == ".cursor" {
								skillName = repo
							}
						}
						primary := strings.EqualFold(normPath, "SKILL.md") ||
							strings.EqualFold(skillName, repo) ||
							strings.EqualFold(skillName, repoBase)
						if primary {
							hasPrimary = true
						}
						candidates = append(candidates, candidateSkill{
							normPath:  normPath,
							skillName: skillName,
							isPrimary: primary,
						})
					}
				}

				seenSkillNames := make(map[string]bool)
				for _, cand := range candidates {
					if subPath == "" && hasPrimary && !cand.isPrimary {
						continue
					}
					lowerKey := strings.ToLower(cand.skillName)
					if seenSkillNames[lowerKey] {
						continue
					}
					rawContent, err := fetchText(fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s/%s", owner, repo, treeRef, cand.normPath), 10*time.Second)
					if err != nil || rawContent == "" {
						continue
					}
					seenSkillNames[lowerKey] = true
					targetDir := filepath.Join(userSkillsDir, cand.skillName)
					_ = os.MkdirAll(targetDir, 0755)
					_ = os.WriteFile(filepath.Join(targetDir, "SKILL.md"), []byte(rawContent), 0644)
					results = append(results, map[string]any{
						"name": cand.skillName,
						"path": targetDir,
					})
					foundSkills = true
				}
			}
		}
	}

	// 2. Fallback: check main and master for SKILL.md
	branches := []string{"main", "master"}
	if branchHint != "" {
		branches = append([]string{branchHint}, branches...)
	}
	if !foundSkills {
		var skillText string
		for _, br := range branches {
			text, err := fetchText(fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s/SKILL.md", owner, repo, br), 8*time.Second)
			if err == nil && text != "" {
				skillText = text
				break
			}
		}

		if skillText != "" {
			targetDir := filepath.Join(userSkillsDir, repo)
			_ = os.MkdirAll(targetDir, 0755)
			_ = os.WriteFile(filepath.Join(targetDir, "SKILL.md"), []byte(skillText), 0644)
			results = append(results, map[string]any{
				"name": repo,
				"path": targetDir,
			})
			foundSkills = true
		}
	}

	// 3. Fallback: check README.md / AGENTS.md or authenticated gh CLI (for private repos) and synthesize SKILL.md
	if !foundSkills {
		var readmeText string
		for _, br := range branches {
			text, err := fetchText(fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s/README.md", owner, repo, br), 8*time.Second)
			if err == nil && text != "" {
				readmeText = text
				break
			}
		}

		targetDir := filepath.Join(userSkillsDir, repo)
		_ = os.MkdirAll(targetDir, 0755)

		// If unauthenticated HTTP returned nothing (e.g. private GitHub repo), try authenticated gh CLI
		if strings.TrimSpace(readmeText) == "" {
			if ghBin, lookErr := exec.LookPath("gh"); lookErr == nil && ghBin != "" {
				tmpClone, tmpErr := os.MkdirTemp("", "unjess-gh-skill-*")
				if tmpErr == nil {
					defer os.RemoveAll(tmpClone)
					cloneCmd := exec.Command(ghBin, "repo", "clone", fmt.Sprintf("%s/%s", owner, repo), tmpClone, "--", "--depth", "1")
					if err := cloneCmd.Run(); err == nil {
						// Copy non-.git files into targetDir
						_ = filepath.Walk(tmpClone, func(p string, info os.FileInfo, walkErr error) error {
							if walkErr != nil {
								return nil
							}
							rel, relErr := filepath.Rel(tmpClone, p)
							if relErr != nil || rel == "." {
								return nil
							}
							if info.IsDir() {
								if info.Name() == ".git" {
									return filepath.SkipDir
								}
								return os.MkdirAll(filepath.Join(targetDir, rel), 0755)
							}
							return copyFile(p, filepath.Join(targetDir, rel))
						})
						// Gather primary instructions from SKILL.md, AGENTS.md, or README.md
						for _, docCand := range []string{"SKILL.md", "AGENTS.md", "README.md"} {
							if b, rErr := os.ReadFile(filepath.Join(tmpClone, docCand)); rErr == nil && len(strings.TrimSpace(string(b))) > 0 {
								readmeText = string(b)
								break
							}
						}
					}
				}
			}
		}

		// If SKILL.md was already copied directly from the repo with frontmatter, keep it; otherwise synthesize SKILL.md
		existingSkillMD := filepath.Join(targetDir, "SKILL.md")
		if b, rErr := os.ReadFile(existingSkillMD); rErr != nil || !strings.HasPrefix(strings.TrimSpace(string(b)), "---") {
			firstLine := ""
			for _, l := range strings.Split(readmeText, "\n") {
				trimmed := strings.TrimSpace(l)
				if trimmed != "" && !strings.HasPrefix(trimmed, "#") {
					if len(trimmed) > 120 {
						trimmed = trimmed[:120]
					}
					firstLine = trimmed
					break
				}
			}
			if firstLine == "" {
				firstLine = fmt.Sprintf("GitHub skill from %s/%s", owner, repo)
			}

			synthesized := fmt.Sprintf("---\nname: %s\ndescription: %s\ntriggers:\n  - %s\nrepo_url: https://github.com/%s/%s\n---\n\n%s",
				repo, firstLine, repo, owner, repo, readmeText)

			_ = os.WriteFile(existingSkillMD, []byte(synthesized), 0644)
		}

		results = append(results, map[string]any{
			"name": repo,
			"path": targetDir,
		})
	}

	installed := make([]string, 0, len(results))
	for _, r := range results {
		if n, ok := r["name"].(string); ok {
			installed = append(installed, n)
		}
	}

	var primarySkill map[string]any
	if len(results) > 0 {
		primarySkill = results[0]
	}

	return map[string]any{
		"status":    "ok",
		"success":   true,
		"installed": installed,
		"skill":     primarySkill,
		"skills":    results,
	}, nil
}

// Match finds skills that match user input, ordered by relevance.
func Match(skills []SkillInfo, userInput string) []SkillInfo {
	if strings.TrimSpace(userInput) == "" {
		return nil
	}

	inputLower := strings.ToLower(userInput)
	type scoredSkill struct {
		skill SkillInfo
		score int
	}
	var matches []scoredSkill

	for _, s := range skills {
		nameLower := strings.ToLower(s.Name)
		// Skip caveman / ponytail if present as skills to prevent duplicate prompt injection with prompt bar toggles
		if nameLower == "caveman" || nameLower == "ponytail" {
			continue
		}

		score := 0
		if len(nameLower) >= 3 && (strings.Contains(inputLower, nameLower) || strings.Contains(inputLower, strings.ReplaceAll(nameLower, "-", " "))) {
			score += 100
		}
		for _, pattern := range s.Triggers {
			patLower := strings.TrimSpace(strings.ToLower(pattern))
			if len(patLower) >= 3 && strings.Contains(inputLower, patLower) {
				score += 60
			}
		}

		if score >= 50 {
			matches = append(matches, scoredSkill{skill: s, score: score})
		}
	}

	sort.Slice(matches, func(i, j int) bool {
		return matches[i].score > matches[j].score
	})

	result := make([]SkillInfo, 0, len(matches))
	for _, m := range matches {
		result = append(result, m.skill)
		if len(result) >= 2 {
			break
		}
	}
	return result
}

// CatalogForPrompt formats available skills for injection into the system prompt.
func CatalogForPrompt(skills []SkillInfo) string {
	if len(skills) == 0 {
		return ""
	}
	var lines []string
	lines = append(lines, "Available skills:")
	for _, s := range skills {
		nameLower := strings.ToLower(s.Name)
		if nameLower == "caveman" || nameLower == "ponytail" {
			continue
		}
		desc := s.Description
		desc = strings.ReplaceAll(desc, "Reverse engineer", "Inspect binary/artifact")
		desc = strings.ReplaceAll(desc, "reverse engineer", "Inspect binary/artifact")
		if s.Path != "" {
			skillPath := s.Path
			if !strings.HasSuffix(strings.ToLower(skillPath), ".md") && !strings.HasSuffix(strings.ToLower(skillPath), ".json") {
				skillPath = filepath.Join(skillPath, "SKILL.md")
			}
			lines = append(lines, fmt.Sprintf("  - %s (%s): %s", s.Name, skillPath, desc))
		} else {
			lines = append(lines, fmt.Sprintf("  - %s: %s", s.Name, desc))
		}
	}
	if len(lines) <= 1 {
		return ""
	}
	return strings.Join(lines, "\n")
}
