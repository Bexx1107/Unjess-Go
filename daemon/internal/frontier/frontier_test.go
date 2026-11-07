package frontier

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestCustomToolLifecycle(t *testing.T) {
	// Set isolated temporary custom tools directory
	tempDir := t.TempDir()
	t.Setenv("UNJESS_CUSTOM_TOOLS_DIR", tempDir)

	// 1. Synthesize Python tool
	pyCode := `import os, json
args = json.loads(os.environ.get("UNJESS_TOOL_ARGS", "{}"))
print("PY_RESULT:" + args.get("msg", "none"))
`
	pyTool, err := SynthesizeTool("calc_hash", "Calculates hash value", "python", pyCode, map[string]any{
		"type": "object",
		"properties": map[string]any{
			"msg": map[string]any{"type": "string"},
		},
	})
	if err != nil {
		t.Fatalf("SynthesizeTool(python) failed: %v", err)
	}

	if pyTool.Name != "custom_calc_hash" {
		t.Errorf("Expected sanitized name custom_calc_hash, got %s", pyTool.Name)
	}
	if pyTool.Language != "python" {
		t.Errorf("Expected language python, got %s", pyTool.Language)
	}

	// 2. LoadCustomTools check
	tools, err := LoadCustomTools()
	if err != nil {
		t.Fatalf("LoadCustomTools failed: %v", err)
	}
	if len(tools) != 1 {
		t.Fatalf("Expected 1 tool loaded, got %d", len(tools))
	}
	if tools[0].Name != "custom_calc_hash" {
		t.Errorf("Expected custom_calc_hash, got %s", tools[0].Name)
	}

	// 3. ExecuteCustomTool
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	out, found, err := ExecuteCustomTool(ctx, "", "custom_calc_hash", map[string]any{"msg": "hello_frontier"})
	if !found {
		t.Fatalf("Expected tool to be found")
	}
	if err != nil {
		t.Fatalf("ExecuteCustomTool returned error: %v (output: %s)", err, out)
	}
	if !strings.Contains(out, "PY_RESULT:hello_frontier") {
		t.Errorf("Output did not contain expected payload: %s", out)
	}

	// 4. CustomToolDefinitions
	defs := CustomToolDefinitions()
	if len(defs) != 1 {
		t.Fatalf("Expected 1 definition, got %d", len(defs))
	}
	fnMap, ok := defs[0]["function"].(map[string]any)
	if !ok || fnMap["name"] != "custom_calc_hash" {
		t.Errorf("Invalid tool definition: %+v", defs[0])
	}

	// 5. DeleteCustomTool
	if err := DeleteCustomTool("custom_calc_hash"); err != nil {
		t.Fatalf("DeleteCustomTool failed: %v", err)
	}
	toolsAfterDelete, err := LoadCustomTools()
	if err != nil {
		t.Fatalf("LoadCustomTools after delete failed: %v", err)
	}
	if len(toolsAfterDelete) != 0 {
		t.Errorf("Expected 0 tools after delete, got %d", len(toolsAfterDelete))
	}

	// Executing non-existent tool returns found=false
	_, foundMissing, _ := ExecuteCustomTool(ctx, "", "custom_calc_hash", nil)
	if foundMissing {
		t.Errorf("Expected found=false for deleted tool")
	}
}

func TestSynthesizeAndExecuteGoTool(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("UNJESS_CUSTOM_TOOLS_DIR", tempDir)

	goCode := `package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Println("GO_COMPILED_OK:" + os.Getenv("UNJESS_TOOL_ARGS"))
}
`
	tool, err := SynthesizeTool("native_worker", "High speed worker", "go", goCode, nil)
	if err != nil {
		t.Fatalf("SynthesizeTool(go) failed: %v", err)
	}

	if tool.Name != "custom_native_worker" {
		t.Errorf("Expected name custom_native_worker, got %s", tool.Name)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	out, found, err := ExecuteCustomTool(ctx, "", "custom_native_worker", map[string]any{"status": "active"})
	if !found {
		t.Fatalf("Expected tool to be found")
	}
	if err != nil {
		t.Fatalf("ExecuteCustomTool(go) returned error: %v, out: %s", err, out)
	}
	if !strings.Contains(out, "GO_COMPILED_OK:") || !strings.Contains(out, "status") {
		t.Errorf("Unexpected Go tool output: %s", out)
	}
}

func TestSynthesizeAndExecutePowerShellTool(t *testing.T) {
	if runtime.GOOS != "windows" {
		if _, err := exec.LookPath("pwsh"); err != nil {
			if _, err2 := exec.LookPath("powershell"); err2 != nil {
				t.Skip("skipping powershell execution test on non-windows system without pwsh/powershell")
			}
		}
	}
	tempDir := t.TempDir()
	t.Setenv("UNJESS_CUSTOM_TOOLS_DIR", tempDir)

	psCode := `$json = $env:UNJESS_TOOL_ARGS | ConvertFrom-Json
Write-Output "PS_OUTPUT:$($json.tag)"
`
	_, err := SynthesizeTool("ps_probe", "Powershell probe", "powershell", psCode, nil)
	if err != nil {
		t.Fatalf("SynthesizeTool(powershell) failed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	out, found, err := ExecuteCustomTool(ctx, "", "custom_ps_probe", map[string]any{"tag": "ps_success"})
	if !found {
		t.Fatalf("Expected tool to be found")
	}
	if err != nil {
		t.Fatalf("ExecuteCustomTool(powershell) returned error: %v, out: %s", err, out)
	}
	if !strings.Contains(out, "PS_OUTPUT:ps_success") {
		t.Errorf("Unexpected PowerShell tool output: %s", out)
	}
}

func TestContextCompactionAndPruning(t *testing.T) {
	// Test short output stays untouched
	shortOutput := "Simple short text"
	compactedShort := CompactOldToolOutput("run_command", shortOutput, 1200)
	if compactedShort != shortOutput {
		t.Errorf("Expected short output to remain unchanged, got: %s", compactedShort)
	}

	// Test long output gets compacted
	longOutput := strings.Repeat("A", 600) + "MIDDLE_CONTENT" + strings.Repeat("Z", 700)
	compactedLong := CompactOldToolOutput("read_file", longOutput, 1200)
	if len(compactedLong) >= len(longOutput) {
		t.Errorf("Expected compacted output to be shorter than original")
	}
	if !strings.Contains(compactedLong, "[Compacted earlier read_file output") {
		t.Errorf("Compacted output missing prefix header")
	}
	if !strings.Contains(compactedLong, "... [pruned to save context window] ...") {
		t.Errorf("Compacted output missing middle ellipsis marker")
	}

	// Test PruneIntraTurnToolOutputs
	messages := []map[string]any{
		{"role": "user", "content": "Start task"},
		{"role": "tool", "name": "grep_search", "content": strings.Repeat("M", 2000)},
		{"role": "tool", "name": "read_file", "content": strings.Repeat("N", 2500)},
		{"role": "tool", "name": "run_command", "content": "Recent short output 1"},
		{"role": "tool", "name": "run_command", "content": "Recent short output 2"},
	}

	// keepRecentTools = 2, maxToolOutputChars = 500
	pruned := PruneIntraTurnToolOutputs(0, 500, 2, messages)
	if len(pruned) != len(messages) {
		t.Fatalf("Expected %d messages, got %d", len(messages), len(pruned))
	}

	// First two tools should be compacted
	if !strings.Contains(pruned[1]["content"].(string), "[Compacted earlier grep_search output") {
		t.Errorf("Message 1 should have been compacted")
	}
	if !strings.Contains(pruned[2]["content"].(string), "[Compacted earlier read_file output") {
		t.Errorf("Message 2 should have been compacted")
	}

	// Last two tools should be untouched
	if pruned[3]["content"] != "Recent short output 1" {
		t.Errorf("Message 3 should remain untouched, got: %v", pruned[3]["content"])
	}
	if pruned[4]["content"] != "Recent short output 2" {
		t.Errorf("Message 4 should remain untouched, got: %v", pruned[4]["content"])
	}

	// Test BuildRollingCompactionPrompt
	prompt := BuildRollingCompactionPrompt("Step 1 done. Step 2 modified auth.go.")
	if !strings.Contains(prompt, "executive summary") || !strings.Contains(prompt, "Step 2 modified auth.go") {
		t.Errorf("BuildRollingCompactionPrompt did not format expected content: %s", prompt)
	}
}

func TestModelCascadeRouter(t *testing.T) {
	fastModel := "gemini-2.0-flash"
	primaryModel := "claude-3-5-sonnet"

	// 1. Explore task routes to fast model
	decExplore := SelectModelForTask("explore", "Find all router definitions", primaryModel, fastModel)
	if decExplore.Tier != "fast" || decExplore.Model != fastModel || decExplore.Provider != "google" {
		t.Errorf("Unexpected explore decision: %+v", decExplore)
	}

	// 2. Summarize task routes to fast model
	decSum := SelectModelForTask("summarize", "Summarize previous discussion", primaryModel, fastModel)
	if decSum.Tier != "fast" || decSum.Model != fastModel {
		t.Errorf("Unexpected summarize decision: %+v", decSum)
	}

	// 3. Architect task routes to frontier model
	decArch := SelectModelForTask("architect", "Design multi-tier caching architecture", primaryModel, fastModel)
	if decArch.Tier != "frontier" || decArch.Model != primaryModel || decArch.Provider != "anthropic" {
		t.Errorf("Unexpected architect decision: %+v", decArch)
	}

	// 4. Security duel routes to frontier model
	decSec := SelectModelForTask("security_duel", "Run duel exploit validation", primaryModel, fastModel)
	if decSec.Tier != "frontier" || decSec.Model != primaryModel {
		t.Errorf("Unexpected security_duel decision: %+v", decSec)
	}

	// 5. Inferred from prompt when taskKind is empty
	decInferred := RouteModelForStep("", "quick scan and list files in src/", primaryModel, fastModel)
	if decInferred.Tier != "fast" || decInferred.Model != fastModel {
		t.Errorf("Expected inferred fast tier, got: %+v", decInferred)
	}
}

func TestCaptureVisualQA(t *testing.T) {
	tempDir := t.TempDir()
	htmlFile := filepath.Join(tempDir, "index.html")
	htmlContent := `<!DOCTYPE html>
<html>
<head><title>Visual QA Test</title></head>
<body style="background-color: #101010; color: #00ff00;">
<h1>Visual QA Test Passed</h1>
</body>
</html>`
	if err := os.WriteFile(htmlFile, []byte(htmlContent), 0644); err != nil {
		t.Fatalf("Failed to write html file: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	dataURI, metaReport, err := CaptureVisualQA(ctx, tempDir, "index.html", 800, 600)
	if err != nil {
		t.Logf("CaptureVisualQA skipped or returned error (acceptable if headless browser unavailable in environment): %v", err)
		return
	}

	if !strings.HasPrefix(dataURI, "data:image/png;base64,") {
		t.Errorf("Expected dataURI to start with data:image/png;base64, got %s", dataURI[:min(len(dataURI), 50)])
	}
	if !strings.Contains(metaReport, "800x600") {
		t.Errorf("Meta report missing resolution: %s", metaReport)
	}
}

func TestCheckWorkspaceHealth(t *testing.T) {
	// Clean workspace test
	tempDir := t.TempDir()
	cleanPy := filepath.Join(tempDir, "valid.py")
	if err := os.WriteFile(cleanPy, []byte("x = 10\nprint(x)\n"), 0644); err != nil {
		t.Fatalf("Failed to write valid py: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	statusClean := CheckWorkspaceHealth(ctx, tempDir)
	if !statusClean.Healthy {
		t.Errorf("Expected clean workspace to be healthy, got issues: %+v", statusClean.Issues)
	}
	if statusClean.LastModifiedFile != "valid.py" {
		t.Errorf("Expected LastModifiedFile=valid.py, got %s", statusClean.LastModifiedFile)
	}

	// Broken workspace test with intentional syntax error
	brokenPy := filepath.Join(tempDir, "broken.py")
	if err := os.WriteFile(brokenPy, []byte("def invalid_syntax ::::\n"), 0644); err != nil {
		t.Fatalf("Failed to write broken py: %v", err)
	}

	statusBroken := CheckWorkspaceHealth(ctx, tempDir)
	if statusBroken.Healthy {
		t.Errorf("Expected broken workspace to be unhealthy")
	}
	if len(statusBroken.Issues) == 0 {
		t.Fatalf("Expected issues detected in broken workspace")
	}
	if !strings.Contains(statusBroken.SuggestedPrompt, "Fix the compiler/linter error") {
		t.Errorf("SuggestedPrompt missing fix directive: %s", statusBroken.SuggestedPrompt)
	}
}
