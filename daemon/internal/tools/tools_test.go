package tools

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestComputeUnifiedDiff(t *testing.T) {
	oldContent := "line 1\nline 2\nline 3\n"
	newContent := "line 1\nline 2 modified\nline 3\n"

	diffStr, adds, dels := ComputeUnifiedDiff("src/test.txt", oldContent, newContent)
	if adds != 1 || dels != 1 {
		t.Fatalf("expected 1 add and 1 del, got adds=%d, dels=%d", adds, dels)
	}

	if !strings.Contains(diffStr, "--- a/src/test.txt") || !strings.Contains(diffStr, "+++ b/src/test.txt") {
		t.Errorf("missing diff headers in:\n%s", diffStr)
	}
	if !strings.Contains(diffStr, "-line 2") || !strings.Contains(diffStr, "+line 2 modified") {
		t.Errorf("missing diff changes in:\n%s", diffStr)
	}

	// Identical content returns empty diff
	identicalDiff, a, d := ComputeUnifiedDiff("file.txt", "same", "same")
	if identicalDiff != "" || a != 0 || d != 0 {
		t.Errorf("expected empty diff for identical content, got: %s", identicalDiff)
	}

	// New file
	newFileDiff, a2, d2 := ComputeUnifiedDiff("new.txt", "", "hello\nworld\n")
	if a2 != 2 || d2 != 0 {
		t.Errorf("expected 2 additions, got adds=%d dels=%d", a2, d2)
	}
	if !strings.Contains(newFileDiff, "--- /dev/null") {
		t.Errorf("expected /dev/null header in new file diff:\n%s", newFileDiff)
	}
}

func TestCoreToolDefinitions(t *testing.T) {
	defs := CoreToolDefinitions()
	if len(defs) < 32 {
		t.Fatalf("expected at least 32 core tool definitions, got %d", len(defs))
	}

	names := make(map[string]bool)
	for _, d := range defs {
		n, ok := d["name"].(string)
		if !ok {
			t.Fatalf("missing name in definition: %+v", d)
		}
		names[n] = true
	}

	required := []string{
		"read_file",
		"write_file",
		"edit_file",
		"list_dir",
		"grep_search",
		"find_by_name",
		"run_command",
		"git_status",
		"git_diff",
		"invoke_subagent",
		"memory_save",
		"memory_recall",
		"memory_delete",
		"web_search",
		"read_url",
		"mcp_status",
		"ask_question",
		"propose_plan",
		"ast_outline",
		"ast_find_callers",
		"ast_edit_symbol",
		"ast_rename_symbol",
		"git_archeology",
		"debug_trace",
		"synthesize_tool",
		"visual_qa_screenshot",
		"sec_attack_surface",
		"sec_taint_scan",
		"sec_smart_fuzz",
		"sec_supply_chain_audit",
		"run_security_duel",
		"speculative_execute",
		"manage_unjess",
	}

	for _, req := range required {
		if !names[req] {
			t.Errorf("missing required tool definition: %s", req)
		}
	}

	subDefs := SubagentToolDefinitions()
	if len(subDefs) < 28 {
		t.Fatalf("expected at least 28 subagent tool definitions, got %d", len(subDefs))
	}
	for _, sd := range subDefs {
		n := sd["name"]
		if n == "invoke_subagent" || n == "ask_question" || n == "propose_plan" || n == "speculative_execute" {
			t.Errorf("tool %s should not be in subagent tool definitions", n)
		}
	}

	roDefs := ReadOnlySubagentToolDefinitions()
	if len(roDefs) == 0 {
		t.Fatalf("expected read-only subagent tool definitions, got 0")
	}
	for _, rd := range roDefs {
		n, _ := rd["name"].(string)
		if n == "write_file" || n == "edit_file" || n == "run_command" ||
			n == "ast_edit_symbol" || n == "ast_rename_symbol" || n == "synthesize_tool" ||
			n == "debug_trace" || n == "sec_smart_fuzz" || n == "run_security_duel" ||
			n == "speculative_execute" || n == "invoke_subagent" {
			t.Errorf("write/exec tool %s found in read-only subagent tool definitions", n)
		}
	}
}

func TestToolExecutor_FileOperations(t *testing.T) {
	tempDir := t.TempDir()
	executor := NewToolExecutor(tempDir, 30)
	ctx := context.Background()

	// 1. write_file
	writeRes, ok := executor.Execute(ctx, "write_file", map[string]any{
		"path":    "hello.txt",
		"content": "Hello World\nSecond Line\nThird Line\n",
	})
	if !ok {
		t.Fatalf("write_file failed: %s", writeRes)
	}
	if len(executor.FilesTouched) != 1 || executor.FilesTouched[0].Action != "created" {
		t.Errorf("unexpected FilesTouched: %+v", executor.FilesTouched)
	}
	if len(executor.Diffs) != 1 {
		t.Errorf("unexpected Diffs: %+v", executor.Diffs)
	}

	// 2. read_file
	readRes, ok := executor.Execute(ctx, "read_file", map[string]any{
		"path":       "hello.txt",
		"start_line": 1,
		"end_line":   2,
	})
	if !ok {
		t.Fatalf("read_file failed: %s", readRes)
	}
	if !strings.Contains(readRes, "1: Hello World") || !strings.Contains(readRes, "2: Second Line") {
		t.Errorf("unexpected read_file output:\n%s", readRes)
	}

	// 3. edit_file
	editRes, ok := executor.Execute(ctx, "edit_file", map[string]any{
		"path":       "hello.txt",
		"old_string": "Second Line",
		"new_string": "Second Line Edited",
	})
	if !ok {
		t.Fatalf("edit_file failed: %s", editRes)
	}
	if len(executor.FilesTouched) != 1 || executor.FilesTouched[0].Action != "modified" {
		t.Errorf("expected modified FilesTouched, got: %+v", executor.FilesTouched)
	}

	// Read again to confirm edit
	readEdited, _ := executor.Execute(ctx, "read_file", map[string]any{
		"path": "hello.txt",
	})
	if !strings.Contains(readEdited, "Second Line Edited") {
		t.Errorf("expected edited content in file, got:\n%s", readEdited)
	}

	// 4. list_dir
	listRes, ok := executor.Execute(ctx, "list_dir", map[string]any{
		"path": ".",
	})
	if !ok {
		t.Fatalf("list_dir failed: %s", listRes)
	}
	if !strings.Contains(listRes, "hello.txt") {
		t.Errorf("expected hello.txt in list_dir output:\n%s", listRes)
	}

	// 5. find_by_name
	findRes, ok := executor.Execute(ctx, "find_by_name", map[string]any{
		"pattern": "*.txt",
	})
	if !ok {
		t.Fatalf("find_by_name failed: %s", findRes)
	}
	if !strings.Contains(findRes, "hello.txt") {
		t.Errorf("expected hello.txt in find_by_name output:\n%s", findRes)
	}

	// 6. grep_search
	grepRes, ok := executor.Execute(ctx, "grep_search", map[string]any{
		"pattern": "Edited",
	})
	if !ok {
		t.Fatalf("grep_search failed: %s", grepRes)
	}
	if !strings.Contains(grepRes, "Second Line Edited") {
		t.Errorf("expected match in grep_search output:\n%s", grepRes)
	}

	// 7. run_command
	testCmd := "echo 'PowerShell Test Success'"
	if runtime.GOOS == "windows" {
		testCmd = "Write-Output 'PowerShell Test Success'"
	}
	cmdRes, ok := executor.Execute(ctx, "run_command", map[string]any{
		"command": testCmd,
	})
	if !ok {
		t.Fatalf("run_command failed: %s", cmdRes)
	}
	if !strings.Contains(cmdRes, "PowerShell Test Success") {
		t.Errorf("unexpected run_command output:\n%s", cmdRes)
	}

	// Check ToolCalls count
	if len(executor.ToolCalls) != 8 {
		t.Errorf("expected 8 ToolCalls recorded, got %d", len(executor.ToolCalls))
	}
}

func TestToolExecutor_SandboxSafety(t *testing.T) {
	tempDir := t.TempDir()
	executor := NewToolExecutor(tempDir, 30)
	ctx := context.Background()

	// Outside path escape attempt
	res, ok := executor.Execute(ctx, "read_file", map[string]any{
		"path": "../../../sensitive.txt",
	})
	if ok {
		t.Fatalf("expected failure for path escaping workspace, got ok: %s", res)
	}
	if !strings.Contains(res, "escapes workspace sandbox") {
		t.Errorf("expected sandbox error, got: %s", res)
	}
}

func TestToolExecutor_GitCommands(t *testing.T) {
	// Execute git_status on actual workspace
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	repoRoot := filepath.Dir(filepath.Dir(wd))
	executor := NewToolExecutor(repoRoot, 10)
	ctx := context.Background()

	statusRes, ok := executor.Execute(ctx, "git_status", map[string]any{})
	if !ok {
		t.Logf("git_status returned not ok: %s", statusRes)
	} else {
		t.Logf("git_status output:\n%s", statusRes)
	}

	diffRes, ok := executor.Execute(ctx, "git_diff", map[string]any{})
	if !ok {
		t.Logf("git_diff returned not ok: %s", diffRes)
	} else {
		t.Logf("git_diff output length: %d", len(diffRes))
	}
}

func TestToolExecutor_MemoryOperations(t *testing.T) {
	tempMem := filepath.Join(t.TempDir(), "memories_tool_test.json")
	t.Setenv("UNJESS_MEMORY_PATH", tempMem)

	executor := NewToolExecutor(t.TempDir(), 10)
	ctx := context.Background()

	// 1. Save
	saveRes, ok := executor.Execute(ctx, "memory_save", map[string]any{
		"key":      "project_target",
		"content":  "Unjess Desktop AI coding agent",
		"category": "project",
		"scope":    "global",
		"tags":     []any{"desktop", "agent"},
	})
	if !ok {
		t.Fatalf("memory_save failed: %s", saveRes)
	}
	if !strings.Contains(saveRes, "Successfully saved") {
		t.Errorf("expected success message, got: %s", saveRes)
	}

	// 2. Recall
	recallRes, ok := executor.Execute(ctx, "memory_recall", map[string]any{
		"query": "coding agent",
	})
	if !ok {
		t.Fatalf("memory_recall failed: %s", recallRes)
	}
	if !strings.Contains(recallRes, "project_target") || !strings.Contains(recallRes, "Unjess Desktop") {
		t.Errorf("expected recalled memory, got: %s", recallRes)
	}

	// 3. Delete
	delRes, ok := executor.Execute(ctx, "memory_delete", map[string]any{
		"key": "project_target",
	})
	if !ok {
		t.Fatalf("memory_delete failed: %s", delRes)
	}

	// 4. Recall again
	recallRes2, ok := executor.Execute(ctx, "memory_recall", map[string]any{
		"query": "coding agent",
	})
	if !ok {
		t.Fatalf("memory_recall failed: %s", recallRes2)
	}
	if strings.Contains(recallRes2, "project_target") {
		t.Errorf("expected memory to be gone after delete: %s", recallRes2)
	}
}

func TestToolExecutor_ReadURL(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!DOCTYPE html>
<html>
<head>
    <title>Test Page</title>
    <style>body { background: #000; }</style>
    <script>alert("secret")</script>
</head>
<body>
    <h1>Documentation Title</h1>
    <p>This is the <b>main content</b> of the page &amp; documentation.</p>
    <div>Another section with helpful details.</div>
</body>
</html>`))
	}))
	defer ts.Close()

	executor := NewToolExecutor(t.TempDir(), 10)
	ctx := context.Background()

	res, ok := executor.Execute(ctx, "read_url", map[string]any{
		"url": ts.URL,
	})
	if !ok {
		t.Fatalf("read_url failed: %s", res)
	}

	if strings.Contains(res, "alert(\"secret\")") {
		t.Errorf("read_url failed to strip <script> tag: %s", res)
	}
	if strings.Contains(res, "body { background: #000; }") {
		t.Errorf("read_url failed to strip <style> tag: %s", res)
	}
	if !strings.Contains(res, "Documentation Title") {
		t.Errorf("read_url missing heading: %s", res)
	}
	if !strings.Contains(res, "main content of the page & documentation") {
		t.Errorf("read_url missing unescaped text: %s", res)
	}
}

func TestToolExecutor_ParseDuckDuckGoHTML(t *testing.T) {
	htmlData := `<div class="result results_links results_links_deep web-result">
  <div class="links_main links_deep result__body">
    <h2 class="result__title">
      <a rel="nofollow" class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fgolang.org%2Fpkg%2Ffmt%2F&amp;rut=123">fmt package - Go Documentation</a>
    </h2>
    <a class="result__snippet">Package fmt implements formatted I/O with functions analogous to C&#39;s printf and scanf.</a>
  </div>
</div>`

	results := parseDuckDuckGoHTML(htmlData, 5)
	if len(results) != 1 {
		t.Fatalf("expected 1 parsed result, got %d", len(results))
	}

	r := results[0]
	if r.URL != "https://golang.org/pkg/fmt/" {
		t.Errorf("expected decoded URL 'https://golang.org/pkg/fmt/', got '%s'", r.URL)
	}
	if r.Title != "fmt package - Go Documentation" {
		t.Errorf("expected title 'fmt package - Go Documentation', got '%s'", r.Title)
	}
	if !strings.Contains(r.Snippet, "analogous to C's printf") {
		t.Errorf("expected snippet with unescaped quote, got '%s'", r.Snippet)
	}
}

func TestToolExecutor_MCPStatus(t *testing.T) {
	tempDir := t.TempDir()
	executor := NewToolExecutor(tempDir, 30)
	ctx := context.Background()

	res, ok := executor.Execute(ctx, "mcp_status", map[string]any{})
	if !ok {
		t.Fatalf("mcp_status execution failed: %s", res)
	}
	if !strings.Contains(res, "MCP Servers Status") && !strings.Contains(res, "No MCP servers configured") {
		t.Errorf("unexpected mcp_status output: %s", res)
	}
}

func TestReadOnlySubagentTools(t *testing.T) {
	defs := ReadOnlySubagentToolDefinitions()
	for _, d := range defs {
		name := d["name"].(string)
		if name == "write_file" || name == "edit_file" || name == "run_command" || name == "invoke_subagent" {
			t.Errorf("read-only subagent tool definitions should not contain %s", name)
		}
	}

	tempDir := t.TempDir()
	exec := NewToolExecutor(tempDir, 30)
	exec.ReadOnly = true

	ctx := context.Background()
	for _, writeTool := range []string{"write_file", "edit_file", "run_command"} {
		res, ok := exec.ExecuteWithoutRecording(ctx, writeTool, map[string]any{})
		if ok {
			t.Errorf("expected %s to be blocked in read-only mode", writeTool)
		}
		if !strings.Contains(res, "disabled in read-only research subagent mode") {
			t.Errorf("unexpected error message for %s: %s", writeTool, res)
		}
	}
}

func TestCheckCommandSandbox(t *testing.T) {
	tempDir := t.TempDir()

	// 1. Should block sensitive Unjess secrets
	for _, cmd := range []string{
		"cat ~/.unjess/.session_secret",
		"type C:\\Users\\user\\.unjess\\keys.yaml",
		"Get-Content harness_state.json",
		"tail unjess.log",
	} {
		if err := checkCommandSandbox(cmd, tempDir); err == nil {
			t.Errorf("expected sandbox error for command '%s', got nil", cmd)
		}
	}

	// 2. Should block .unjess access outside workspaces
	for _, cmd := range []string{
		"ls ~/.unjess",
		"dir C:\\Users\\user\\.unjess\\config.yaml",
		"rm -rf ~/.unjess/skills",
	} {
		if err := checkCommandSandbox(cmd, tempDir); err == nil {
			t.Errorf("expected sandbox error for command '%s', got nil", cmd)
		}
	}

	// 3. Should block config.yaml traversal
	for _, cmd := range []string{
		"cat ../../config.yaml",
		"Get-Content ..\\config.yaml",
	} {
		if err := checkCommandSandbox(cmd, tempDir); err == nil {
			t.Errorf("expected sandbox error for command '%s', got nil", cmd)
		}
	}

	// 4. Should allow valid commands inside workspace, ~/.unjess/skills, and explicit external paths (e.g. Steam / Program Files / E: drive)
	for _, cmd := range []string{
		"go test ./...",
		"npm run build",
		"cat C:\\Users\\user\\.unjess\\workspaces\\testproject\\main.go",
		"ls ~/.unjess/workspaces/proj",
		"Get-Content C:\\Users\\user\\.unjess\\skills\\custom-skill\\SKILL.md",
		"Get-ChildItem \"C:\\Program Files (x86)\\Steam\\steamapps\\common\\Balatro\"",
		"Get-ChildItem \"E:\\Projects\\DesignAssets\"",
		"robocopy \"C:\\Program Files (x86)\\Steam\\steamapps\\common\\Balatro\" . /E",
	} {
		if err := checkCommandSandbox(cmd, tempDir); err != nil {
			t.Errorf("expected command '%s' to be allowed, got: %v", cmd, err)
		}
	}
}

func TestReadFileOnDirectoryAutoListsDir(t *testing.T) {
	tempDir := t.TempDir()
	subDir := filepath.Join(tempDir, "03_PROMPTS", "8")
	_ = os.MkdirAll(subDir, 0755)
	_ = os.WriteFile(filepath.Join(subDir, "scene_8.txt"), []byte("Prompt 8"), 0644)

	executor := NewToolExecutor(tempDir, 10)
	res, ok := executor.Execute(context.Background(), "read_file", map[string]any{
		"path": subDir,
	})
	if !ok {
		t.Fatalf("expected read_file on directory to auto-list directory, got error: %s", res)
	}
	if !strings.Contains(res, "scene_8.txt") {
		t.Errorf("expected directory listing to include scene_8.txt, got: %s", res)
	}
}

func TestGitTools_NonGitWorkspace(t *testing.T) {
	tempDir := t.TempDir()
	exec := NewToolExecutor(tempDir, 30)

	statusRes, ok1 := exec.executeGitStatus()
	if !ok1 {
		t.Fatalf("expected git_status to succeed on non-git workspace, got: %s", statusRes)
	}
	if !strings.Contains(statusRes, "Workspace is not a Git repository yet") {
		t.Errorf("unexpected git_status output: %s", statusRes)
	}

	diffRes, ok2 := exec.executeGitDiff(map[string]any{})
	if !ok2 {
		t.Fatalf("expected git_diff to succeed on non-git workspace, got: %s", diffRes)
	}
	if !strings.Contains(diffRes, "Workspace is not a Git repository yet") {
		t.Errorf("unexpected git_diff output: %s", diffRes)
	}
}

func TestAnalyzeSyntaxErrorAndLearn(t *testing.T) {
	tempHints := filepath.Join(t.TempDir(), "memory", "error_hints.json")
	t.Setenv("UNJESS_ERROR_HINTS_PATH", tempHints)

	// 1. && statement separator
	hint1 := analyzeSyntaxErrorAndLearn("npm test && echo done", "The token '&&' is not a valid statement separator in this version.")
	if !strings.Contains(hint1, "[SYNTAX AUTO-HINT]") || !strings.Contains(hint1, "PowerShell") {
		t.Errorf("expected PowerShell && auto-hint, got: %s", hint1)
	}

	// 2. curl parameter binding
	hint2 := analyzeSyntaxErrorAndLearn("curl -s http://localhost", "curl : Cannot bind parameter because parameter 's' is not recognized.")
	if !strings.Contains(hint2, "[SYNTAX AUTO-HINT]") || !strings.Contains(hint2, "curl.exe") {
		t.Errorf("expected curl auto-hint, got: %s", hint2)
	}

	// 3. Missing closing brace
	hint3 := analyzeSyntaxErrorAndLearn("if ($true) { Write-Host 'hi'", "MissingEndCurlyBrace: Missing closing '}' in statement block.")
	if !strings.Contains(hint3, "[SYNTAX AUTO-HINT]") || !strings.Contains(hint3, "closing curly brace") {
		t.Errorf("expected missing brace auto-hint, got: %s", hint3)
	}

	// 4. Unexpected token
	hint4 := analyzeSyntaxErrorAndLearn("git commit -m 'hello", "UnexpectedToken in expression or statement.")
	if !strings.Contains(hint4, "[SYNTAX AUTO-HINT]") || !strings.Contains(hint4, "unexpected token") {
		t.Errorf("expected unexpected token auto-hint, got: %s", hint4)
	}

	// Verify ZERO EMOJIS in hints
	for _, h := range []string{hint1, hint2, hint3, hint4} {
		for _, c := range h {
			if c > 127 && c != '-' && c != '\'' {
				t.Errorf("found non-ascii character in hint: %q", c)
			}
		}
	}

	// Allow background goroutines to finish writing before TempDir cleanup
	time.Sleep(50 * time.Millisecond)
}

func TestWriteFileWithRetry(t *testing.T) {
	tempFile := filepath.Join(t.TempDir(), "retry_test.txt")
	err := writeFileWithRetry(tempFile, []byte("hello world"), 0644)
	if err != nil {
		t.Fatalf("writeFileWithRetry failed: %v", err)
	}
	content, err := os.ReadFile(tempFile)
	if err != nil || string(content) != "hello world" {
		t.Fatalf("unexpected content: %s, err: %v", string(content), err)
	}
}

func TestExecuteRunCommand_EnvCI(t *testing.T) {
	tempDir := t.TempDir()
	executor := NewToolExecutor(tempDir, 10)
	ctx := context.Background()

	cmd := "echo CI=$env:CI"
	if runtime.GOOS != "windows" {
		cmd = "echo CI=$CI"
	}
	out, ok := executor.Execute(ctx, "run_command", map[string]any{
		"command": cmd,
	})
	if !ok {
		t.Fatalf("run_command failed: %s", out)
	}
	if !strings.Contains(out, "CI=true") {
		t.Errorf("expected CI=true in output, got: %s", out)
	}
}

func TestToolExecutor_ManageUnjessCustomProvider(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv("UNJESS_CONFIG_DIR", cfgDir)

	executor := NewToolExecutor(t.TempDir(), 10)
	ctx := context.Background()

	// 1. Add custom AI provider
	addRes, ok := executor.Execute(ctx, "manage_unjess", map[string]any{
		"action":   "add_provider",
		"id":       "deepseek",
		"name":     "DeepSeek API",
		"base_url": "https://api.deepseek.com/v1",
		"api_key":  "sk-test-deepseek",
		"models":   []any{"deepseek-chat", "deepseek-reasoner"},
	})
	if !ok {
		t.Fatalf("add_provider failed: %s", addRes)
	}
	if !strings.Contains(addRes, "DeepSeek API") {
		t.Errorf("unexpected add_provider output: %s", addRes)
	}

	// 2. List providers
	listRes, ok := executor.Execute(ctx, "manage_unjess", map[string]any{
		"action": "list_providers",
	})
	if !ok || !strings.Contains(listRes, "deepseek-reasoner") {
		t.Fatalf("expected deepseek-reasoner in list_providers, got: %s", listRes)
	}

	// 3. Remove provider
	remRes, ok := executor.Execute(ctx, "manage_unjess", map[string]any{
		"action": "remove_provider",
		"id":     "deepseek",
	})
	if !ok {
		t.Fatalf("remove_provider failed: %s", remRes)
	}
}

func TestToolExecutor_ContextTruncationAndDiskLog(t *testing.T) {
	tempDir := t.TempDir()
	executor := NewToolExecutor(tempDir, 30)
	ctx := context.Background()

	// Generate a command that outputs 360 lines (exceeding the 300-line inline budget)
	var cmdStr string
	if runtime.GOOS == "windows" {
		cmdStr = "1..360 | ForEach-Object { \"Line $_\"; }"
	} else {
		cmdStr = "for i in $(seq 1 360); do echo \"Line $i\"; done"
	}

	res, ok := executor.Execute(ctx, "run_command", map[string]any{
		"command": cmdStr,
	})
	if !ok {
		t.Fatalf("run_command failed: %s", res)
	}

	// Verify truncation banner is present
	if !strings.Contains(res, "Truncated") || !strings.Contains(res, "Full output saved to") {
		t.Fatalf("expected truncation banner in output, got:\n%s", res)
	}

	// Verify head contains Line 1 and Line 25
	if !strings.Contains(res, "Line 1") || !strings.Contains(res, "Line 25") {
		t.Errorf("expected head lines 1 and 25, got:\n%s", res)
	}

	// Verify tail contains Line 360
	if !strings.Contains(res, "Line 360") {
		t.Errorf("expected tail line 360, got:\n%s", res)
	}

	// Verify logs directory has the cached file
	logsDir := filepath.Join(tempDir, ".unjess", "logs")
	entries, err := os.ReadDir(logsDir)
	if err != nil || len(entries) == 0 {
		t.Fatalf("expected log files in %s, err: %v", logsDir, err)
	}

	// Read full log and verify it has all 360 lines
	logContent, err := os.ReadFile(filepath.Join(logsDir, entries[0].Name()))
	if err != nil {
		t.Fatalf("failed reading cached log: %v", err)
	}
	if !strings.Contains(string(logContent), "Line 180") || !strings.Contains(string(logContent), "Line 360") {
		t.Errorf("expected full log to contain intermediate line 180, got len %d", len(logContent))
	}
}

func TestToolExecutor_ErrorAwareLogTruncation(t *testing.T) {
	tempDir := t.TempDir()
	executor := NewToolExecutor(tempDir, 30)

	// Create 360 lines of test output with a failure located in the middle (line 150, outside 80 head + 100 tail)
	var sb strings.Builder
	for i := 1; i <= 360; i++ {
		if i == 150 {
			sb.WriteString("FAILED tests/test_solver.py::test_boundary - AssertionError: expected 1.0e-7 got 1.6e-5\n")
			sb.WriteString("    Traceback (most recent call last):\n")
			sb.WriteString("      File \"test_solver.py\", line 42, in test_boundary\n")
		} else {
			sb.WriteString(fmt.Sprintf("Line %d: passing test simulation\n", i))
		}
	}

	truncated := executor.truncateAndCacheOutput("pytest -v", sb.String())

	// Verify error context block was captured from the middle lines
	if !strings.Contains(truncated, "Error Context Detected: Lines") {
		t.Fatalf("expected Error Context Detected block, got:\n%s", truncated)
	}
	if !strings.Contains(truncated, "AssertionError: expected 1.0e-7 got 1.6e-5") {
		t.Fatalf("expected error message to be preserved in truncated output, got:\n%s", truncated)
	}
	if !strings.Contains(truncated, "Line 1: passing test simulation") {
		t.Errorf("expected head line 1, got:\n%s", truncated)
	}
	if !strings.Contains(truncated, "Line 360: passing test simulation") {
		t.Errorf("expected tail line 360, got:\n%s", truncated)
	}
}

func TestToolExecutor_BackgroundTasks(t *testing.T) {
	tempDir := t.TempDir()
	executor := NewToolExecutor(tempDir, 30)
	ctx := context.Background()

	var bgCmd string
	if runtime.GOOS == "windows" {
		bgCmd = "Start-Sleep -Milliseconds 400; Write-Output 'Async Task Done'"
	} else {
		bgCmd = "sleep 0.4 && echo 'Async Task Done'"
	}

	// 1. Start background task
	startRes, ok := executor.Execute(ctx, "start_task", map[string]any{
		"command": bgCmd,
	})
	if !ok {
		t.Fatalf("start_task failed: %s", startRes)
	}
	if !strings.Contains(startRes, "Started background task") {
		t.Fatalf("unexpected start_task output: %s", startRes)
	}

	// Extract task_id
	var taskID string
	for _, word := range strings.Fields(startRes) {
		trimmed := strings.Trim(word, "'\",.:")
		if strings.HasPrefix(trimmed, "task_") {
			taskID = trimmed
			break
		}
	}
	if taskID == "" {
		t.Fatalf("failed extracting task_id from: %s", startRes)
	}

	// 2. Poll immediately (should be running or just finished)
	pollRes, ok := executor.Execute(ctx, "poll_task", map[string]any{
		"task_id": taskID,
	})
	if !ok {
		t.Fatalf("poll_task failed: %s", pollRes)
	}

	// 3. Wait for task to finish
	time.Sleep(600 * time.Millisecond)

	pollRes2, ok := executor.Execute(ctx, "poll_task", map[string]any{
		"task_id": taskID,
	})
	if !ok {
		t.Fatalf("poll_task (final) failed: %s", pollRes2)
	}
	if !strings.Contains(pollRes2, "completed") && !strings.Contains(pollRes2, "Async Task Done") {
		t.Errorf("expected completed task with output, got: %s", pollRes2)
	}
}

func TestToolExecutor_SkillsSandboxWhitelist(t *testing.T) {
	tempDir := t.TempDir()
	executor := NewToolExecutor(tempDir, 30)
	ctx := context.Background()

	// Create a dummy global skill in user home or project
	userHome, err := os.UserHomeDir()
	if err == nil && userHome != "" {
		testSkillDir := filepath.Join(userHome, ".unjess", "skills", "test-sandbox-whitelist")
		_ = os.MkdirAll(testSkillDir, 0755)
		defer os.RemoveAll(testSkillDir)

		testSkillMD := filepath.Join(testSkillDir, "SKILL.md")
		_ = os.WriteFile(testSkillMD, []byte("# Whitelist Test Skill Content"), 0644)

		// Reading with ~/.unjess/skills/... should succeed even from tempDir workspace!
		readRes, ok := executor.Execute(ctx, "read_file", map[string]any{
			"path": filepath.Join("~", ".unjess", "skills", "test-sandbox-whitelist", "SKILL.md"),
		})
		if !ok || !strings.Contains(readRes, "Whitelist Test Skill Content") {
			t.Fatalf("expected successful read of global skill, got ok=%v, res=%s", ok, readRes)
		}
	}
}

func TestToolExecutor_ProtectedFiles(t *testing.T) {
	tempDir := t.TempDir()
	executor := NewToolExecutor(tempDir, 30)
	ctx := context.Background()

	protectedFile := filepath.Join(tempDir, "run_pretrain.sh")
	_ = os.WriteFile(protectedFile, []byte("#!/bin/bash\necho running"), 0755)

	executor.SetProtectedPaths([]string{"/app/run_pretrain.sh", "run_pretrain.sh"})

	// Attempt write_file
	res, ok := executor.Execute(ctx, "write_file", map[string]any{
		"path":    "run_pretrain.sh",
		"content": "echo hacked",
	})
	if ok || !strings.Contains(res, "PermissionDenied") {
		t.Fatalf("expected write_file on protected file to fail with PermissionDenied, got ok=%v, res=%s", ok, res)
	}

	// Attempt edit_file
	res, ok = executor.Execute(ctx, "edit_file", map[string]any{
		"path":       "run_pretrain.sh",
		"old_string": "echo running",
		"new_string": "echo modified",
	})
	if ok || !strings.Contains(res, "PermissionDenied") {
		t.Fatalf("expected edit_file on protected file to fail with PermissionDenied, got ok=%v, res=%s", ok, res)
	}

	// Attempt destructive run_command
	res, ok = executor.Execute(ctx, "run_command", map[string]any{
		"command": "echo modified > run_pretrain.sh",
	})
	if ok || !strings.Contains(res, "PermissionDenied") {
		t.Fatalf("expected run_command mutation on protected file to fail with PermissionDenied, got ok=%v, res=%s", ok, res)
	}

	// Reading protected file should still be allowed
	res, ok = executor.Execute(ctx, "read_file", map[string]any{
		"path": "run_pretrain.sh",
	})
	if !ok || !strings.Contains(res, "echo running") {
		t.Fatalf("expected read_file on protected file to succeed, got ok=%v, res=%s", ok, res)
	}
}

func TestReadFile_LineRangesAndStreaming(t *testing.T) {
	tmpDir := t.TempDir()
	executor := NewToolExecutor(tmpDir, 60)
	ctx := context.Background()

	// Create a file with 1500 lines to test both range and streaming (>1MB or multi-line)
	var sb strings.Builder
	for i := 1; i <= 1500; i++ {
		sb.WriteString(fmt.Sprintf("Line %d: some content here with repeated filler text to test line slicing\n", i))
	}
	samplePath := filepath.Join(tmpDir, "test_large.txt")
	if err := os.WriteFile(samplePath, []byte(sb.String()), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	// 1. Test standard start_line / end_line
	res, ok := executor.Execute(ctx, "read_file", map[string]any{
		"path":       "test_large.txt",
		"start_line": 50,
		"end_line":   55,
	})
	if !ok {
		t.Fatalf("read_file failed: %s", res)
	}
	if !strings.Contains(res, "50: Line 50:") || !strings.Contains(res, "55: Line 55:") {
		t.Errorf("expected lines 50-55 in output, got:\n%s", res)
	}
	if strings.Contains(res, "49: Line 49:") || strings.Contains(res, "56: Line 56:") {
		t.Errorf("unexpected lines outside 50-55 in output:\n%s", res)
	}

	// 2. Test aliases StartLine / EndLine and string numbers
	res2, ok2 := executor.Execute(ctx, "read_file", map[string]any{
		"path":      "test_large.txt",
		"StartLine": "100",
		"EndLine":   "102",
	})
	if !ok2 {
		t.Fatalf("read_file with string aliases failed: %s", res2)
	}
	if !strings.Contains(res2, "100: Line 100:") || !strings.Contains(res2, "102: Line 102:") {
		t.Errorf("expected lines 100-102 in output, got:\n%s", res2)
	}
}

func TestWindowsCommandEncoding(t *testing.T) {
	script := `python -c "print(r'\"escaped_quotes\"')"`
	encoded := encodePowerShell(script)
	if encoded == "" {
		t.Fatal("encodePowerShell returned empty string")
	}

	ctx := context.Background()
	cmd := buildWindowsCommand(ctx, script)
	if cmd == nil {
		t.Fatal("buildWindowsCommand returned nil")
	}
}

func TestEditFileHardening_LinePrefixesAndIndentation(t *testing.T) {
	orig := "class Solver:\n    def compute(self, x):\n        val = x + 1\n        return val\n"

	// 1. Model accidentally includes read_file "123: " line-number prefixes in old_string and new_string
	targetWithLineNums := "2:     def compute(self, x):\n3:         val = x + 1\n4:         return val"
	replacementWithLineNums := "2:     def compute(self, x):\n3:         val = x * 2\n4:         return val"
	updated, err := applySmartReplacementWithOptions(orig, targetWithLineNums, replacementWithLineNums, "solver.py", false)
	if err != nil {
		t.Fatalf("expected line-number prefix stripping to succeed, got err: %v", err)
	}
	if strings.Contains(updated, "2:") || !strings.Contains(updated, "        val = x * 2\n") {
		t.Errorf("unexpected updated content:\n%s", updated)
	}

	// 2. Fuzzy whitespace match preserves relative Python indentation
	unindentedTarget := "def compute(self, x):\n    val = x + 1\n    return val"
	unindentedReplacement := "def compute(self, x):\n    if x < 0:\n        return 0\n    return x + 1"
	updatedIndented, err := applySmartReplacementWithOptions(orig, unindentedTarget, unindentedReplacement, "solver.py", false)
	if err != nil {
		t.Fatalf("expected whitespace-tolerant match to succeed, got: %v", err)
	}
	if !strings.Contains(updatedIndented, "    def compute(self, x):\n        if x < 0:\n            return 0\n        return x + 1") {
		t.Errorf("expected relative indentation to be shifted by 4 spaces, got:\n%s", updatedIndented)
	}

	// 3. replace_all: true replaces multiple occurrences cleanly
	multiOrig := "foo = 1\nbar = foo + 2\nbaz = foo + 3\n"
	updatedAll, err := applySmartReplacementWithOptions(multiOrig, "foo", "alpha", "solver.py", true)
	if err != nil {
		t.Fatalf("expected replace_all to succeed, got: %v", err)
	}
	if strings.Count(updatedAll, "alpha") != 3 {
		t.Errorf("expected 3 occurrences of alpha, got:\n%s", updatedAll)
	}

	// 4. Failed match returns closest matching lines with 1-indexed line numbers
	_, failErr := applySmartReplacementWithOptions(orig, "    def compute(self, y):\n        val = y + 999\n", "pass", "solver.py", false)
	if failErr == nil {
		t.Fatal("expected non-matching target to return error")
	}
	if !strings.Contains(failErr.Error(), "Closest matching section") || !strings.Contains(failErr.Error(), "2:     def compute(self, x):") {
		t.Errorf("expected closest-match preview with line numbers, got:\n%v", failErr)
	}
}

func TestNormalizeToolCallAndTextExtraction(t *testing.T) {
	// 1. Alias bash -> run_command and cmd -> command
	name, args, err := NormalizeToolCall("bash", map[string]any{"cmd": "pytest -q"})
	if err != nil {
		t.Fatalf("NormalizeToolCall failed: %v", err)
	}
	if name != "run_command" || args["command"] != "pytest -q" {
		t.Errorf("expected run_command with command='pytest -q', got %s %v", name, args)
	}

	// 2. Alias str_replace_editor (view -> read_file, str_replace -> edit_file)
	name2, args2, err := NormalizeToolCall("str_replace_editor", map[string]any{
		"command":  "str_replace",
		"path":     "app.py",
		"old_str":  "a = 1",
		"new_str":  "a = 2",
		"replace_all": "true",
	})
	if err != nil {
		t.Fatalf("NormalizeToolCall str_replace_editor failed: %v", err)
	}
	if name2 != "edit_file" || args2["old_string"] != "a = 1" || args2["new_string"] != "a = 2" || args2["replace_all"] != true {
		t.Errorf("unexpected normalized str_replace_editor: %s %v", name2, args2)
	}

	// 3. Repair malformed _raw JSON with trailing comma
	name3, args3, err := NormalizeToolCall("read", map[string]any{
		"_raw": `{"file_path": "main.go", "start_line": "10",}`,
	})
	if err != nil {
		t.Fatalf("expected _raw JSON with trailing comma to be repaired, got err: %v", err)
	}
	if name3 != "read_file" || args3["path"] != "main.go" || args3["start_line"] != 10 {
		t.Errorf("unexpected repaired tool call: %s %v", name3, args3)
	}

	// 4. Extract tool call from <tool_call> tag in assistant text
	rawText := "I will inspect main.go now.\n<tool_call>\n{\"name\": \"read_file\", \"arguments\": {\"path\": \"main.go\"}}\n</tool_call>"
	extracted := ExtractToolCallsFromText(rawText)
	if len(extracted) != 1 || extracted[0].Name != "read_file" || extracted[0].Arguments["path"] != "main.go" {
		t.Fatalf("expected 1 extracted read_file tool call, got: %+v", extracted)
	}
}

func TestDeferredToolPlacementAndTodoWrite(t *testing.T) {
	tempDir := t.TempDir()
	executor := NewToolExecutor(tempDir, 30)
	executor.EnforceReadBeforeEdit = true
	ctx := context.Background()

	// 1. ListedToolDefinitions should return lean core set (~12 tools) instead of all 34+ tools
	listed := ListedToolDefinitions("Autonomous", "Fix the bug in solver.py", false, nil)
	if len(listed) > 15 || len(listed) < 10 {
		t.Errorf("expected ListedToolDefinitions to return ~12 core tools, got %d", len(listed))
	}

	// 2. tool_search unlocks deferred tools dynamically
	resSearch, ok := executor.Execute(ctx, "tool_search", map[string]any{"query": "security taint"})
	if !ok || !strings.Contains(resSearch, "sec_taint_scan") {
		t.Fatalf("expected tool_search to unlock sec_taint_scan, got: %s", resSearch)
	}
	if !executor.UnlockedDeferredTools["sec_taint_scan"] {
		t.Errorf("expected sec_taint_scan to be marked unlocked in executor.UnlockedDeferredTools")
	}
	listedAfter := ListedToolDefinitions("Autonomous", "Fix the bug in solver.py", false, executor.UnlockedDeferredTools)
	if len(listedAfter) <= len(listed) {
		t.Errorf("expected unlocked tools to expand listed tool definitions (%d -> %d)", len(listed), len(listedAfter))
	}

	// 3. todo_write updates checklist
	resTodo, ok := executor.Execute(ctx, "todo_write", map[string]any{
		"todos": []any{
			map[string]any{"id": "1", "content": "Inspect solver.py", "status": "completed"},
			map[string]any{"id": "2", "content": "Patch edge case", "status": "in_progress"},
		},
	})
	if !ok || !strings.Contains(resTodo, "1/2 completed") {
		t.Fatalf("unexpected todo_write output: %s", resTodo)
	}

	// 4. EnforceReadBeforeEdit rejects editing an existing unread file, then allows it after read_file
	samplePath := filepath.Join(tempDir, "unread.py")
	_ = os.WriteFile(samplePath, []byte("x = 1\n"), 0644)

	editRes, editOk := executor.Execute(ctx, "edit_file", map[string]any{
		"path":       "unread.py",
		"old_string": "x = 1",
		"new_string": "x = 2",
	})
	if editOk || !strings.Contains(editRes, "has not been read yet") {
		t.Fatalf("expected Read-Before-Edit check to block unread file edit, got ok=%v res=%s", editOk, editRes)
	}

	_, readOk := executor.Execute(ctx, "read_file", map[string]any{"path": "unread.py"})
	if !readOk {
		t.Fatal("read_file failed on unread.py")
	}

	editRes2, editOk2 := executor.Execute(ctx, "edit_file", map[string]any{
		"path":       "unread.py",
		"old_string": "x = 1",
		"new_string": "x = 2",
	})
	if !editOk2 {
		t.Fatalf("expected edit_file to succeed after read_file, got: %s", editRes2)
	}

	// 5. RevertAllSnapshots restores unread.py back to "x = 1\n"
	reverted, revErr := executor.RevertAllSnapshots()
	if revErr != nil || len(reverted) != 1 {
		t.Fatalf("expected 1 reverted file, got %v err=%v", reverted, revErr)
	}
	restoredBytes, _ := os.ReadFile(samplePath)
	if string(restoredBytes) != "x = 1\n" {
		t.Errorf("expected RevertAllSnapshots to restore 'x = 1\\n', got %q", string(restoredBytes))
	}
}




