package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultQuickWorkspace(t *testing.T) {
	ws, err := DefaultQuickWorkspace()
	if err != nil {
		t.Fatalf("DefaultQuickWorkspace returned error: %v", err)
	}
	if ws == "" {
		t.Fatalf("expected non-empty workspace path")
	}
	info, err := os.Stat(ws)
	if err != nil || !info.IsDir() {
		t.Fatalf("expected directory at %s, got err: %v", ws, err)
	}
	if !strings.Contains(filepath.ToSlash(ws), ".unjess/workspaces/quick-chat") {
		t.Errorf("expected path containing .unjess/workspaces/quick-chat, got: %s", ws)
	}
}

func TestIsInternalUnjessDir(t *testing.T) {
	tests := []struct {
		path     string
		expected bool
	}{
		{`C:\Users\User\.gemini\antigravity\scratch\unjess`, true},
		{`C:/Users/User/.gemini/antigravity/scratch/unjess`, true},
		{`C:\Unjess-Portable\engine`, true},
		{`C:\Users\User\AppData\Local\Unjess\engine\python.exe`, true},
		{`/opt/unjess-portable/engine`, true},
		{`C:\Users\User\my-react-app`, false},
		{`D:\projects\ecommerce`, false},
	}

	for _, tt := range tests {
		got := IsInternalUnjessDir(tt.path)
		if got != tt.expected {
			t.Errorf("IsInternalUnjessDir(%q) = %v; want %v", tt.path, got, tt.expected)
		}
	}
}

func TestResolveSafePath(t *testing.T) {
	tempDir := t.TempDir()

	// Normal valid relative path
	p, err := ResolveSafePath(tempDir, "src/main.go")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := filepath.Join(tempDir, "src", "main.go")
	if p != expected {
		t.Errorf("expected %s, got %s", expected, p)
	}

	// Normal valid absolute path inside
	absValid := filepath.Join(tempDir, "file.txt")
	p2, err := ResolveSafePath(tempDir, absValid)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p2 != absValid {
		t.Errorf("expected %s, got %s", absValid, p2)
	}

	// Traversal attempt escaping workspace
	_, err = ResolveSafePath(tempDir, "../secret.txt")
	if err == nil {
		t.Fatalf("expected error on escaping path traversal, got nil")
	}

	// Traversal attempt with deeply nested ..
	_, err = ResolveSafePath(tempDir, "a/../../outside.txt")
	if err == nil {
		t.Fatalf("expected error on escaping path traversal, got nil")
	}
}

func TestListWorkspaceFiles(t *testing.T) {
	tempDir := t.TempDir()

	// Create test file structure
	_ = os.MkdirAll(filepath.Join(tempDir, "src"), 0755)
	_ = os.WriteFile(filepath.Join(tempDir, "src", "main.go"), []byte("package main"), 0644)
	_ = os.WriteFile(filepath.Join(tempDir, "README.md"), []byte("# Hello"), 0644)

	// Ignored folder
	_ = os.MkdirAll(filepath.Join(tempDir, "node_modules", "pkg"), 0755)
	_ = os.WriteFile(filepath.Join(tempDir, "node_modules", "pkg", "index.js"), []byte("console.log()"), 0644)

	files := ListWorkspaceFiles(tempDir, 100)
	if len(files) != 2 {
		t.Fatalf("expected 2 files, got %d: %+v", len(files), files)
	}

	foundMain := false
	foundReadme := false
	for _, f := range files {
		if f.Path == "src/main.go" {
			foundMain = true
		}
		if f.Path == "README.md" {
			foundReadme = true
		}
	}

	if !foundMain || !foundReadme {
		t.Errorf("expected src/main.go and README.md, got %+v", files)
	}
}

func TestGetGitInfo(t *testing.T) {
	// Call on current scratch/unjess repository
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	repoRoot := filepath.Dir(filepath.Dir(wd)) // parent of daemon
	info := GetGitInfo(repoRoot)
	if info.Branch == "" {
		t.Logf("Git branch empty or not in git repo: %+v", info)
	} else {
		t.Logf("Detected git branch: %s, dirty: %v, changed: %d", info.Branch, info.IsDirty, info.ChangedCount)
	}
}
