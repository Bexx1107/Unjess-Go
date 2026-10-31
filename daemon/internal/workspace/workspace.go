package workspace

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// DefaultQuickWorkspace ensures ~/.unjess/workspaces/quick-chat exists and returns its absolute path.
func DefaultQuickWorkspace() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get user home directory: %w", err)
	}
	quickDir := filepath.Join(home, ".unjess", "workspaces", "quick-chat")
	if err := os.MkdirAll(quickDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create quick workspace directory: %w", err)
	}
	abs, err := filepath.Abs(quickDir)
	if err != nil {
		return quickDir, nil
	}
	return abs, nil
}

// IsInternalUnjessDir returns true if path contains \scratch\unjess, Unjess-Portable,
// AppData\Local\Unjess\engine, or temporary test workspaces.
func IsInternalUnjessDir(p string) bool {
	if p == "" {
		return false
	}
	rawLower := strings.ToLower(p)
	slashLower := strings.ToLower(filepath.ToSlash(p))
	backLower := strings.ToLower(filepath.FromSlash(p))

	return strings.Contains(backLower, `\scratch\unjess`) ||
		strings.Contains(slashLower, "/scratch/unjess") ||
		strings.Contains(rawLower, "unjess-portable") ||
		strings.Contains(backLower, `appdata\local\unjess\engine`) ||
		strings.Contains(slashLower, "appdata/local/unjess/engine") ||
		strings.Contains(rawLower, "unjess_api_test") ||
		strings.Contains(slashLower, "appdata/local/temp/unjess") ||
		strings.Contains(backLower, `appdata\local\temp\unjess`)
}

// GitInfo represents repository branch and status summary.
type GitInfo struct {
	Branch       string `json:"branch"`
	IsDirty      bool   `json:"isDirty"`
	ChangedCount int    `json:"changedCount"`
}

// GetGitInfo runs `git rev-parse --abbrev-ref HEAD` and `git status --porcelain`
// (with hidden window on Windows via syscall.SysProcAttr{HideWindow: true}) and returns GitInfo.
func GetGitInfo(wsPath string) GitInfo {
	info := GitInfo{
		Branch:       "",
		IsDirty:      false,
		ChangedCount: 0,
	}
	if wsPath == "" {
		return info
	}

	// 1. Branch
	cmdBranch := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmdBranch.Dir = wsPath
	setHideWindow(cmdBranch)
	if outBranch, err := cmdBranch.Output(); err == nil {
		b := strings.TrimSpace(string(outBranch))
		if b != "" && b != "HEAD" {
			info.Branch = b
		} else {
			info.Branch = "main"
		}
	}

	// 2. Status
	cmdStatus := exec.Command("git", "status", "--porcelain")
	cmdStatus.Dir = wsPath
	setHideWindow(cmdStatus)
	if outStatus, err := cmdStatus.Output(); err == nil {
		raw := strings.TrimSpace(string(outStatus))
		if raw == "" {
			info.ChangedCount = 0
			info.IsDirty = false
		} else {
			lines := strings.Split(raw, "\n")
			count := 0
			for _, l := range lines {
				if strings.TrimSpace(l) != "" {
					count++
				}
			}
			info.ChangedCount = count
			info.IsDirty = count > 0
		}
	}

	return info
}

// FileEntry represents a file or directory entry in the workspace.
type FileEntry struct {
	Path  string `json:"path"`
	Name  string `json:"name"`
	Size  int64  `json:"size"`
	IsDir bool   `json:"isDir"`
}

// Ignored directory names for workspace scanning.
var ignoredDirNames = map[string]bool{
	".git":                true,
	"__pycache__":         true,
	"node_modules":        true,
	"target":              true,
	".venv":               true,
	"venv":                true,
	"env":                 true,
	".pytest_cache":       true,
	".mypy_cache":         true,
	".ruff_cache":         true,
	"dist":                true,
	"build":               true,
	"out":                 true,
	".next":               true,
	".nuxt":               true,
	".idea":               true,
	".vscode":             true,
	".vs":                 true,
	"engine":              true,
	"unjess-portable":     true,
	"standalone-test":     true,
	"vendor":              true,
	"coverage":            true,
	"bin":                 true,
	"obj":                 true,
	".gradle":             true,
	".cargo":              true,
	"appdata":             true,
	".unjess_attachments": true,
}

// ListWorkspaceFiles walks wsPath up to depth 4, skipping ignored dirs,
// and returns forward-slash relative paths (filepath.ToSlash(rel)).
func ListWorkspaceFiles(wsPath string, maxFiles int) []FileEntry {
	if maxFiles <= 0 {
		maxFiles = 400
	}

	absWs, err := filepath.Abs(wsPath)
	if err != nil {
		return nil
	}

	entries := make([]FileEntry, 0)
	errStop := errors.New("limit reached")

	_ = filepath.WalkDir(absWs, func(currentPath string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}

		rel, err := filepath.Rel(absWs, currentPath)
		if err != nil || rel == "." {
			return nil
		}

		slashRel := filepath.ToSlash(rel)
		parts := strings.Split(slashRel, "/")
		depth := len(parts)

		name := d.Name()
		nameLower := strings.ToLower(name)

		if d.IsDir() {
			// Skip ignored dirs or hidden dirs
			if ignoredDirNames[nameLower] || (strings.HasPrefix(name, ".") && name != ".github") {
				return filepath.SkipDir
			}
			if depth >= 4 {
				return filepath.SkipDir
			}
			return nil
		}

		// Skip dotfiles unless .gitignore or .env.example
		if strings.HasPrefix(name, ".") && name != ".gitignore" && name != ".env.example" {
			return nil
		}

		info, err := d.Info()
		var size int64
		if err == nil {
			size = info.Size()
		}

		entries = append(entries, FileEntry{
			Path:  slashRel,
			Name:  name,
			Size:  size,
			IsDir: false,
		})

		if len(entries) >= maxFiles {
			return errStop
		}

		return nil
	})

	// Sort entries deterministically by path
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Path < entries[j].Path
	})

	return entries
}

// ResolveSafePath resolves relPath against wsPath and verifies it does not escape wsPath (.. traversal check).
func ResolveSafePath(wsPath, relPath string) (string, error) {
	if wsPath == "" {
		return "", errors.New("workspace path cannot be empty")
	}

	absWs, err := filepath.Abs(wsPath)
	if err != nil {
		return "", fmt.Errorf("failed resolving workspace path: %w", err)
	}

	cleanRel := filepath.Clean(relPath)
	var target string
	if filepath.IsAbs(cleanRel) {
		target = cleanRel
	} else {
		// Strip leading slashes to prevent escaping root on Windows/Unix
		trimmed := strings.TrimLeft(relPath, `/\`)
		target = filepath.Join(absWs, filepath.Clean(trimmed))
	}

	absTarget, err := filepath.Abs(target)
	if err != nil {
		return "", fmt.Errorf("failed resolving target path: %w", err)
	}

	// Sandbox boundary check
	normWs := strings.ToLower(filepath.Clean(absWs))
	normTarget := strings.ToLower(filepath.Clean(absTarget))

	if normTarget != normWs && !strings.HasPrefix(normTarget, normWs+string(filepath.Separator)) {
		return "", fmt.Errorf("access denied: path '%s' escapes workspace sandbox '%s'", relPath, wsPath)
	}

	return absTarget, nil
}

// BrowseNativeFolder runs PowerShell to open System.Windows.Forms.FolderBrowserDialog with a TopMost = $true dummy form.
func BrowseNativeFolder() (string, error) {
	psScript := `[System.Reflection.Assembly]::LoadWithPartialName('System.windows.forms') | Out-Null; ` +
		`$top = New-Object System.Windows.Forms.Form; ` +
		`$top.TopMost = $true; ` +
		`$f = New-Object System.Windows.Forms.FolderBrowserDialog; ` +
		`$f.Description = 'Select Workspace Folder for Unjess'; ` +
		`$f.ShowNewFolderButton = $true; ` +
		`if ($f.ShowDialog($top) -eq [System.Windows.Forms.DialogResult]::OK) { Write-Output $f.SelectedPath }`

	cmd := exec.Command("powershell", "-NoProfile", "-Sta", "-WindowStyle", "Hidden", "-Command", psScript)
	setHideWindow(cmd)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("folder browser dialog error: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}
