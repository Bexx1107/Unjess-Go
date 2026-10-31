package codeintel

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractSymbols_Go(t *testing.T) {
	tmpDir := t.TempDir()

	goSource := `package testpkg

// ServerConfig holds the server settings.
type ServerConfig struct {
	Host string
	Port int
}

// Router interface for routing requests.
type Router interface {
	Route(path string) error
}

const DefaultPort = 8080
var GlobalStatus = "ready"

// NewServer initializes a new server.
func NewServer(cfg ServerConfig) *ServerConfig {
	return &cfg
}

// Start launches the server instance.
func (s *ServerConfig) Start() error {
	return nil
}
`
	goFile := "server.go"
	if err := os.WriteFile(filepath.Join(tmpDir, goFile), []byte(goSource), 0644); err != nil {
		t.Fatal(err)
	}

	symbols, err := ExtractSymbols(tmpDir, goFile)
	if err != nil {
		t.Fatalf("ExtractSymbols failed: %v", err)
	}

	symMap := make(map[string]SymbolNode)
	for _, s := range symbols {
		symMap[s.Name] = s
	}

	// Verify ServerConfig struct
	sc, ok := symMap["ServerConfig"]
	if !ok {
		t.Fatalf("expected ServerConfig struct in symbols: %+v", symbols)
	}
	if sc.Kind != "struct" {
		t.Errorf("expected Kind struct, got %s", sc.Kind)
	}
	if !strings.Contains(sc.Doc, "holds the server settings") {
		t.Errorf("expected doc for ServerConfig, got %q", sc.Doc)
	}

	// Verify Router interface
	rt, ok := symMap["Router"]
	if !ok {
		t.Fatalf("expected Router interface in symbols")
	}
	if rt.Kind != "interface" {
		t.Errorf("expected Kind interface, got %s", rt.Kind)
	}

	// Verify NewServer func
	ns, ok := symMap["NewServer"]
	if !ok {
		t.Fatalf("expected NewServer func in symbols")
	}
	if ns.Kind != "function" {
		t.Errorf("expected Kind function, got %s", ns.Kind)
	}

	// Verify (*ServerConfig).Start method
	st, ok := symMap["(*ServerConfig).Start"]
	if !ok {
		t.Fatalf("expected (*ServerConfig).Start method in symbols, got keys: %+v", symMap)
	}
	if st.Kind != "method" {
		t.Errorf("expected Kind method, got %s", st.Kind)
	}
	if st.StartLine >= st.EndLine {
		t.Errorf("expected StartLine < EndLine, got %d and %d", st.StartLine, st.EndLine)
	}
}

func TestExtractSymbols_Python(t *testing.T) {
	tmpDir := t.TempDir()

	pySource := `"""Module documentation."""

class Calculator:
    """A simple calculator class."""

    def __init__(self, val=0):
        self.val = val

    def add(self, x):
        """Adds x to current val."""
        self.val += x
        return self.val

def helper_func():
    return 42
`
	pyFile := "calc.py"
	if err := os.WriteFile(filepath.Join(tmpDir, pyFile), []byte(pySource), 0644); err != nil {
		t.Fatal(err)
	}

	symbols, err := ExtractSymbols(tmpDir, pyFile)
	if err != nil {
		t.Fatalf("ExtractSymbols failed: %v", err)
	}

	symMap := make(map[string]SymbolNode)
	for _, s := range symbols {
		symMap[s.Name] = s
	}

	if calc, ok := symMap["Calculator"]; !ok || calc.Kind != "class" {
		t.Errorf("expected Calculator class symbol, got %+v", calc)
	}

	if add, ok := symMap["add"]; !ok || add.Kind != "method" {
		t.Errorf("expected add method symbol, got %+v", add)
	} else {
		if !strings.Contains(add.Doc, "Adds x") {
			t.Errorf("expected doc for add method, got %q", add.Doc)
		}
	}

	if helper, ok := symMap["helper_func"]; !ok || helper.Kind != "function" {
		t.Errorf("expected helper_func function symbol, got %+v", helper)
	}
}

func TestExtractSymbols_BraceLanguages(t *testing.T) {
	tmpDir := t.TempDir()

	jsSource := `
export class AuthService {
    login() {
        return true;
    }
}

export async function authenticateUser(token) {
    if (!token) {
        return false;
    }
    return true;
}

const logout = async () => {
    console.log("logout");
};
`
	jsFile := "auth.js"
	if err := os.WriteFile(filepath.Join(tmpDir, jsFile), []byte(jsSource), 0644); err != nil {
		t.Fatal(err)
	}

	symbols, err := ExtractSymbols(tmpDir, jsFile)
	if err != nil {
		t.Fatalf("ExtractSymbols failed: %v", err)
	}

	symMap := make(map[string]SymbolNode)
	for _, s := range symbols {
		symMap[s.Name] = s
	}

	if _, ok := symMap["AuthService"]; !ok {
		t.Errorf("expected AuthService class")
	}
	if fn, ok := symMap["authenticateUser"]; !ok || fn.Kind != "function" {
		t.Errorf("expected authenticateUser function, got %+v", fn)
	}
	if fn, ok := symMap["logout"]; !ok || fn.Kind != "function" {
		t.Errorf("expected logout function, got %+v", fn)
	}
}

func TestFormatFileOutline(t *testing.T) {
	tmpDir := t.TempDir()

	goSource := `package main

// Main entry
func main() {
}
`
	goFile := "main.go"
	_ = os.WriteFile(filepath.Join(tmpDir, goFile), []byte(goSource), 0644)

	outline, err := FormatFileOutline(tmpDir, goFile)
	if err != nil {
		t.Fatalf("FormatFileOutline error: %v", err)
	}
	if !strings.Contains(outline, "File Outline: main.go") || !strings.Contains(outline, "[function] main") {
		t.Errorf("unexpected outline output:\n%s", outline)
	}

	// Empty file test
	emptyFile := "empty.txt"
	_ = os.WriteFile(filepath.Join(tmpDir, emptyFile), []byte(""), 0644)
	outlineEmpty, err := FormatFileOutline(tmpDir, emptyFile)
	if err != nil {
		t.Fatalf("FormatFileOutline error on empty file: %v", err)
	}
	if !strings.Contains(outlineEmpty, "no symbols found") {
		t.Errorf("expected 'no symbols found' for empty file, got: %s", outlineEmpty)
	}
}

func TestFindSymbolCallers(t *testing.T) {
	tmpDir := t.TempDir()

	file1 := `package api

func ProcessPayment(amount int) bool {
	return amount > 0
}
`
	file2 := `package api

func HandleCheckout() {
	if ProcessPayment(100) {
		println("Paid")
	}
}
`
	_ = os.WriteFile(filepath.Join(tmpDir, "pay.go"), []byte(file1), 0644)
	_ = os.WriteFile(filepath.Join(tmpDir, "checkout.go"), []byte(file2), 0644)

	report, err := FindSymbolCallers(tmpDir, "ProcessPayment")
	if err != nil {
		t.Fatalf("FindSymbolCallers error: %v", err)
	}

	if !strings.Contains(report, "Definitions (1 found)") {
		t.Errorf("expected 1 definition in report, got:\n%s", report)
	}
	if !strings.Contains(report, "pay.go") {
		t.Errorf("expected pay.go in definitions:\n%s", report)
	}
	if !strings.Contains(report, "References (1 found)") || !strings.Contains(report, "checkout.go") {
		t.Errorf("expected checkout.go in references:\n%s", report)
	}
}

func TestEditSymbol(t *testing.T) {
	tmpDir := t.TempDir()

	initialGo := `package mathutil

func Add(a, b int) int {
	return a + b
}

func Sub(a, b int) int {
	return a - b
}
`
	_ = os.WriteFile(filepath.Join(tmpDir, "math.go"), []byte(initialGo), 0644)

	newCode := `func Add(a, b int) int {
	// Added debug log
	println("adding numbers")
	return a + b
}`

	oldContent, updatedContent, callersReport, err := EditSymbol(tmpDir, "math.go", "Add", newCode)
	if err != nil {
		t.Fatalf("EditSymbol failed: %v", err)
	}

	if !strings.Contains(oldContent, "return a + b") {
		t.Errorf("oldContent missing original return: %s", oldContent)
	}
	if !strings.Contains(updatedContent, "Added debug log") {
		t.Errorf("updatedContent missing new code: %s", updatedContent)
	}
	if !strings.Contains(updatedContent, "func Sub") {
		t.Errorf("updatedContent accidentally lost Sub func: %s", updatedContent)
	}

	// Verify file on disk
	diskData, err := os.ReadFile(filepath.Join(tmpDir, "math.go"))
	if err != nil {
		t.Fatal(err)
	}
	if string(diskData) != updatedContent {
		t.Errorf("disk content mismatch with updatedContent")
	}

	if callersReport == "" {
		t.Errorf("expected non-empty callers report")
	}

	// Test symbol not found error
	_, _, _, err = EditSymbol(tmpDir, "math.go", "MultiplyNonExistent", "func Multiply() {}")
	if err == nil {
		t.Errorf("expected error for non-existent symbol, got nil")
	}
}

func TestRenameSymbolAcrossRepo(t *testing.T) {
	tmpDir := t.TempDir()

	file1 := "def calculate_discount(price): return price * 0.9"
	file2 := "total = calculate_discount(100) + calculate_discount(50)"
	file3 := "calculate_discount_old = 5" // Should NOT be replaced due to word boundary

	_ = os.WriteFile(filepath.Join(tmpDir, "calc.py"), []byte(file1), 0644)
	_ = os.WriteFile(filepath.Join(tmpDir, "app.py"), []byte(file2), 0644)
	_ = os.WriteFile(filepath.Join(tmpDir, "notes.txt"), []byte(file3), 0644)

	modified, count, err := RenameSymbolAcrossRepo(tmpDir, "calculate_discount", "apply_discount")
	if err != nil {
		t.Fatalf("RenameSymbolAcrossRepo error: %v", err)
	}

	if count != 3 {
		t.Errorf("expected 3 total replacements, got %d", count)
	}
	if len(modified) != 2 {
		t.Errorf("expected 2 modified files, got %d: %v", len(modified), modified)
	}

	// Verify notes.txt was not modified
	notesData, _ := os.ReadFile(filepath.Join(tmpDir, "notes.txt"))
	if string(notesData) != file3 {
		t.Errorf("expected notes.txt untouched due to word boundary, got: %s", string(notesData))
	}
}

func TestRunPostEditCheck(t *testing.T) {
	tmpDir := t.TempDir()
	ctx := context.Background()

	// 1. Valid JSON check
	validJson := `{"status": "ok", "code": 200}`
	_ = os.WriteFile(filepath.Join(tmpDir, "valid.json"), []byte(validJson), 0644)
	res := RunPostEditCheck(ctx, tmpDir, "valid.json")
	if !strings.Contains(res, "PASSED") {
		t.Errorf("expected PASSED for valid JSON, got: %s", res)
	}

	// 2. Invalid JSON check
	invalidJson := `{"status": "ok", "code": }`
	_ = os.WriteFile(filepath.Join(tmpDir, "invalid.json"), []byte(invalidJson), 0644)
	resInvalid := RunPostEditCheck(ctx, tmpDir, "invalid.json")
	if !strings.Contains(resInvalid, "SELF-HEAL REQUIRED") {
		t.Errorf("expected SELF-HEAL REQUIRED for invalid JSON, got: %s", resInvalid)
	}

	// 3. Python syntax check
	if findPythonBin() != "" {
		validPy := "def greet():\n    return 'hello'\n"
		_ = os.WriteFile(filepath.Join(tmpDir, "valid.py"), []byte(validPy), 0644)
		pyRes := RunPostEditCheck(ctx, tmpDir, "valid.py")
		if !strings.Contains(pyRes, "PASSED") {
			t.Errorf("expected PASSED for valid Python, got: %s", pyRes)
		}

		invalidPy := "def greet(\n    return 'syntax error'\n"
		_ = os.WriteFile(filepath.Join(tmpDir, "invalid.py"), []byte(invalidPy), 0644)
		pyBadRes := RunPostEditCheck(ctx, tmpDir, "invalid.py")
		if !strings.Contains(pyBadRes, "SELF-HEAL REQUIRED") {
			t.Errorf("expected SELF-HEAL REQUIRED for invalid Python, got: %s", pyBadRes)
		}
	}

	// 4. Non-linted file type (.md) returns empty string
	_ = os.WriteFile(filepath.Join(tmpDir, "doc.md"), []byte("# Notes"), 0644)
	mdRes := RunPostEditCheck(ctx, tmpDir, "doc.md")
	if mdRes != "" {
		t.Errorf("expected empty string for .md file, got: %q", mdRes)
	}
}

func TestVerifyWorkspaceBuild(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	// Empty workspace returns true with explanation
	passed, msg := VerifyWorkspaceBuild(ctx, tmpDir)
	if !passed || !strings.Contains(msg, "no supported build configuration detected") {
		t.Errorf("expected true with no configuration, got passed=%v msg=%s", passed, msg)
	}

	// Go workspace with passing test
	goMod := `module testmod
go 1.20
`
	goTest := `package testmod
import "testing"
func TestSample(t *testing.T) {}
`
	_ = os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte(goMod), 0644)
	_ = os.WriteFile(filepath.Join(tmpDir, "sample_test.go"), []byte(goTest), 0644)

	passedGo, outGo := VerifyWorkspaceBuild(ctx, tmpDir)
	if !passedGo {
		t.Errorf("expected go test to pass in testmod, got output: %s", outGo)
	}
}

func TestGitArcheology(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	// Not a git repo
	reportNonGit, err := GitArcheology(ctx, tmpDir, "file.txt", "someSymbol", 1, 10)
	if err != nil {
		t.Fatalf("GitArcheology unexpected error: %v", err)
	}
	if !strings.Contains(reportNonGit, "not a git repository") {
		t.Errorf("expected 'not a git repository' report, got: %s", reportNonGit)
	}

	// Initialize git repo for archeology test
	if _, err := exec.LookPath("git"); err == nil {
		exec.Command("git", "-C", tmpDir, "init").Run()
		exec.Command("git", "-C", tmpDir, "config", "user.email", "test@example.com").Run()
		exec.Command("git", "-C", tmpDir, "config", "user.name", "Test User").Run()

		testFile := "feature.py"
		content1 := "def authenticate():\n    return True\n"
		_ = os.WriteFile(filepath.Join(tmpDir, testFile), []byte(content1), 0644)
		exec.Command("git", "-C", tmpDir, "add", ".").Run()
		exec.Command("git", "-C", tmpDir, "commit", "-m", "feat: add authenticate function").Run()

		content2 := "def authenticate():\n    # Security check\n    return True\n"
		_ = os.WriteFile(filepath.Join(tmpDir, testFile), []byte(content2), 0644)
		exec.Command("git", "-C", tmpDir, "add", ".").Run()
		exec.Command("git", "-C", tmpDir, "commit", "-m", "refactor: add security comment").Run()

		reportGit, err := GitArcheology(ctx, tmpDir, testFile, "authenticate", 1, 3)
		if err != nil {
			t.Fatalf("GitArcheology error on git repo: %v", err)
		}
		if !strings.Contains(reportGit, "<git_archeology_report>") {
			t.Errorf("missing <git_archeology_report> tag in report: %s", reportGit)
		}
		if !strings.Contains(reportGit, "authenticate") {
			t.Errorf("expected symbol in archeology report: %s", reportGit)
		}
	}
}

func TestDebugTraceCommand(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	pyBin := findPythonBin()
	if pyBin == "" {
		t.Skip("python not available for DebugTraceCommand test")
	}

	scriptContent := `def divide(a, b):
    x = 100
    y = b
    return x / y

def main():
    divide(10, 0)

if __name__ == "__main__":
    main()
`
	scriptFile := "crash_script.py"
	_ = os.WriteFile(filepath.Join(tmpDir, scriptFile), []byte(scriptContent), 0644)

	cmdStr := pyBin + " crash_script.py"
	report, err := DebugTraceCommand(ctx, tmpDir, cmdStr)
	if err != nil {
		t.Fatalf("DebugTraceCommand returned error: %v", err)
	}

	if !strings.Contains(report, "=== POST-MORTEM CRASH TRACE & VARIABLE INSPECTOR ===") {
		t.Errorf("missing header in report:\n%s", report)
	}
	if !strings.Contains(report, "ZeroDivisionError") {
		t.Errorf("expected ZeroDivisionError in process output:\n%s", report)
	}
	if !strings.Contains(report, "crash_script.py") {
		t.Errorf("expected crash_script.py in stack frames:\n%s", report)
	}
	if !strings.Contains(report, ">>>") {
		t.Errorf("expected '>>>' pointer on crash line:\n%s", report)
	}
}
