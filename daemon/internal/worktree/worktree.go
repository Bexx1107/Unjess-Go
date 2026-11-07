package worktree

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Sandbox represents an isolated shadow workspace sandbox.
type Sandbox struct {
	ID          string            `json:"id"`
	Label       string            `json:"label"`
	OriginPath  string            `json:"origin_path"`
	ShadowPath  string            `json:"shadow_path"`
	EnvCacheDir string            `json:"env_cache_dir"`
	Mode        string            `json:"mode"` // "git_worktree" or "cow_mirror"
	CreatedAt   int64             `json:"created_at"`
	Status      string            `json:"status"` // "active", "promoted", "discarded"
	EnvVars     map[string]string `json:"env_vars"`
}

const (
	ModeGitWorktree = "git_worktree"
	ModeCowMirror   = "cow_mirror"

	StatusActive    = "active"
	StatusPromoted  = "promoted"
	StatusDiscarded = "discarded"

	MaxCopyFileSize = 25 * 1024 * 1024 // 25 MB
)

// defaultIgnoredDirs defines directory names skipped during shadow mirroring and diffs.
var defaultIgnoredDirs = map[string]bool{
	".git":         true,
	"node_modules": true,
	"target":       true,
	".unjess":      true,
	"__pycache__":  true,
	".venv":        true,
	"venv":         true,
}

// generateSandboxID returns a unique sandbox identifier.
func generateSandboxID() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("sb_%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("sb_%d_%s", time.Now().UnixNano(), hex.EncodeToString(b))
}

// isIgnoredRelPath checks whether any segment of a relative path belongs to defaultIgnoredDirs.
func isIgnoredRelPath(rel string) bool {
	slashRel := filepath.ToSlash(rel)
	parts := strings.Split(slashRel, "/")
	for _, p := range parts {
		if defaultIgnoredDirs[p] {
			return true
		}
	}
	return false
}

// copyFile copies a single file from src to dst, creating parent directories as needed.
func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}

	if fi, err := in.Stat(); err == nil {
		_ = out.Chmod(fi.Mode())
	}
	return nil
}

// filesEqual returns true if two files have identical size and content.
func filesEqual(path1, path2 string) (bool, error) {
	fi1, err := os.Stat(path1)
	if err != nil {
		return false, err
	}
	fi2, err := os.Stat(path2)
	if err != nil {
		return false, err
	}
	if fi1.Size() != fi2.Size() {
		return false, nil
	}
	b1, err := os.ReadFile(path1)
	if err != nil {
		return false, err
	}
	b2, err := os.ReadFile(path2)
	if err != nil {
		return false, err
	}
	return bytes.Equal(b1, b2), nil
}

// forceRemoveAll recursively deletes a directory tree, clearing read-only attributes on Windows.
func forceRemoveAll(dir string) error {
	if dir == "" {
		return nil
	}
	_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err == nil && info != nil {
			_ = os.Chmod(p, 0777)
		}
		return nil
	})
	return os.RemoveAll(dir)
}

// cowMirrorWorkspace performs a fast copy-on-write mirror of originPath into shadowPath.
func cowMirrorWorkspace(originPath, shadowPath string) error {
	if err := os.MkdirAll(shadowPath, 0755); err != nil {
		return err
	}

	return filepath.WalkDir(originPath, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		rel, err := filepath.Rel(originPath, path)
		if err != nil || rel == "." {
			return nil
		}

		if d.IsDir() {
			if defaultIgnoredDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}

		if isIgnoredRelPath(rel) {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return nil
		}
		if info.Size() > MaxCopyFileSize {
			return nil
		}

		dst := filepath.Join(shadowPath, rel)
		return copyFile(path, dst)
	})
}

// overlayDirtyFiles copies or removes uncommitted dirty files from wsPath to shadowPath.
func overlayDirtyFiles(wsPath, shadowPath string) {
	cmd := exec.Command("git", "status", "--porcelain=v1", "-uall")
	cmd.Dir = wsPath
	setHideWindow(cmd)
	out, err := cmd.Output()
	if err != nil {
		return
	}

	lines := strings.Split(string(out), "\n")
	for _, rawLine := range lines {
		line := strings.TrimRight(rawLine, "\r\n")
		if len(line) < 3 {
			continue
		}
		statusCode := line[:2]
		filePath := strings.TrimSpace(line[2:])

		// Handle renames e.g. "R  old -> new"
		if strings.Contains(filePath, " -> ") {
			parts := strings.Split(filePath, " -> ")
			if len(parts) == 2 {
				oldP := strings.Trim(parts[0], "\"")
				newP := strings.Trim(parts[1], "\"")
				if !isIgnoredRelPath(oldP) {
					_ = os.Remove(filepath.Join(shadowPath, oldP))
				}
				if !isIgnoredRelPath(newP) {
					srcP := filepath.Join(wsPath, newP)
					if fi, err := os.Stat(srcP); err == nil && !fi.IsDir() && fi.Size() <= MaxCopyFileSize {
						_ = copyFile(srcP, filepath.Join(shadowPath, newP))
					}
				}
			}
			continue
		}

		filePath = strings.Trim(filePath, "\"")
		if isIgnoredRelPath(filePath) {
			continue
		}

		dstPath := filepath.Join(shadowPath, filePath)
		srcPath := filepath.Join(wsPath, filePath)

		if strings.Contains(statusCode, "D") {
			_ = os.Remove(dstPath)
			continue
		}

		fi, err := os.Stat(srcPath)
		if err != nil || fi.IsDir() || fi.Size() > MaxCopyFileSize {
			continue
		}

		_ = copyFile(srcPath, dstPath)
	}
}

// CreateShadowSandbox creates an ephemeral, zero-host-pollution sandbox for wsPath.
// If wsPath has a git repository and git worktree succeeds, it creates a detached git worktree
// and overlays uncommitted dirty files. Otherwise, it falls back to a fast copy mirror.
func CreateShadowSandbox(wsPath, label string) (*Sandbox, error) {
	if wsPath == "" {
		return nil, errors.New("workspace path cannot be empty")
	}
	absWs, err := filepath.Abs(wsPath)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve workspace path: %w", err)
	}
	info, err := os.Stat(absWs)
	if err != nil {
		return nil, fmt.Errorf("workspace path does not exist: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("workspace path is not a directory: %s", absWs)
	}

	if label == "" {
		label = "sandbox"
	}

	id := generateSandboxID()
	baseDir := filepath.Join(os.TempDir(), "unjess_sandboxes")
	if err := os.MkdirAll(baseDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create sandbox base dir: %w", err)
	}

	shadowPath := filepath.Join(baseDir, id)
	envCacheDir := filepath.Join(baseDir, id+"_env")

	// Create isolated environment cache directories for zero host pollution
	tmpDir := filepath.Join(envCacheDir, "tmp")
	npmDir := filepath.Join(envCacheDir, "npm")
	pipDir := filepath.Join(envCacheDir, "pip")
	goDir := filepath.Join(envCacheDir, "gocache")

	if err := os.MkdirAll(tmpDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create sandbox tmp dir: %w", err)
	}
	_ = os.MkdirAll(npmDir, 0755)
	_ = os.MkdirAll(pipDir, 0755)
	_ = os.MkdirAll(goDir, 0755)

	envVars := map[string]string{
		"TMP":              tmpDir,
		"TEMP":             tmpDir,
		"NPM_CONFIG_CACHE": npmDir,
		"PIP_CACHE_DIR":    pipDir,
		"GOCACHE":          goDir,
	}

	mode := ModeCowMirror
	gitDir := filepath.Join(absWs, ".git")
	isGit := false
	if fi, err := os.Stat(gitDir); err == nil && (fi.IsDir() || !fi.IsDir()) {
		isGit = true
	}

	if isGit {
		cmd := exec.Command("git", "worktree", "add", "--detach", shadowPath, "HEAD")
		cmd.Dir = absWs
		setHideWindow(cmd)
		if _, err := cmd.CombinedOutput(); err == nil {
			mode = ModeGitWorktree
			overlayDirtyFiles(absWs, shadowPath)
		} else {
			// Worktree command failed (e.g. empty repo without commits)
			_ = forceRemoveAll(shadowPath)
			mode = ModeCowMirror
		}
	}

	if mode == ModeCowMirror {
		if err := cowMirrorWorkspace(absWs, shadowPath); err != nil {
			_ = forceRemoveAll(shadowPath)
			_ = forceRemoveAll(envCacheDir)
			return nil, fmt.Errorf("failed to create cow mirror sandbox: %w", err)
		}
	}

	sb := &Sandbox{
		ID:          id,
		Label:       label,
		OriginPath:  absWs,
		ShadowPath:  shadowPath,
		EnvCacheDir: envCacheDir,
		Mode:        mode,
		CreatedAt:   time.Now().Unix(),
		Status:      StatusActive,
		EnvVars:     envVars,
	}

	return sb, nil
}

// ComputeSandboxDiffs compares files in sb.ShadowPath against sb.OriginPath,
// returning relative paths of modified, created, and deleted files.
func ComputeSandboxDiffs(sb *Sandbox) (modified []string, created []string, deleted []string, err error) {
	if sb == nil {
		return nil, nil, nil, errors.New("sandbox cannot be nil")
	}

	originFiles := make(map[string]string)
	shadowFiles := make(map[string]string)

	// Collect files from OriginPath
	if _, err := os.Stat(sb.OriginPath); err == nil {
		_ = filepath.WalkDir(sb.OriginPath, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return nil
			}
			rel, err := filepath.Rel(sb.OriginPath, path)
			if err != nil || rel == "." {
				return nil
			}
			if d.IsDir() {
				if defaultIgnoredDirs[d.Name()] {
					return filepath.SkipDir
				}
				return nil
			}
			if isIgnoredRelPath(rel) {
				return nil
			}
			slashRel := filepath.ToSlash(rel)
			originFiles[slashRel] = path
			return nil
		})
	}

	// Collect files from ShadowPath
	if _, err := os.Stat(sb.ShadowPath); err == nil {
		_ = filepath.WalkDir(sb.ShadowPath, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return nil
			}
			rel, err := filepath.Rel(sb.ShadowPath, path)
			if err != nil || rel == "." {
				return nil
			}
			if d.IsDir() {
				if defaultIgnoredDirs[d.Name()] {
					return filepath.SkipDir
				}
				return nil
			}
			if isIgnoredRelPath(rel) {
				return nil
			}
			slashRel := filepath.ToSlash(rel)
			shadowFiles[slashRel] = path
			return nil
		})
	}

	// Determine created and modified
	for rel, sPath := range shadowFiles {
		oPath, exists := originFiles[rel]
		if !exists {
			created = append(created, rel)
		} else {
			equal, err := filesEqual(oPath, sPath)
			if err == nil && !equal {
				modified = append(modified, rel)
			}
		}
	}

	// Determine deleted
	for rel := range originFiles {
		if _, exists := shadowFiles[rel]; !exists {
			deleted = append(deleted, rel)
		}
	}

	sort.Strings(modified)
	sort.Strings(created)
	sort.Strings(deleted)

	return modified, created, deleted, nil
}

// PromoteSandbox copies all modified and created files from sb.ShadowPath to sb.OriginPath,
// removes deleted files from sb.OriginPath, and marks the sandbox status as "promoted".
func PromoteSandbox(sb *Sandbox) (appliedFiles []string, err error) {
	if sb == nil {
		return nil, errors.New("sandbox cannot be nil")
	}

	modified, created, deleted, err := ComputeSandboxDiffs(sb)
	if err != nil {
		return nil, fmt.Errorf("failed to compute sandbox diffs: %w", err)
	}

	for _, rel := range append(modified, created...) {
		src := filepath.Join(sb.ShadowPath, filepath.FromSlash(rel))
		dst := filepath.Join(sb.OriginPath, filepath.FromSlash(rel))
		if err := copyFile(src, dst); err != nil {
			return appliedFiles, fmt.Errorf("failed copying %s: %w", rel, err)
		}
		appliedFiles = append(appliedFiles, rel)
	}

	for _, rel := range deleted {
		dst := filepath.Join(sb.OriginPath, filepath.FromSlash(rel))
		_ = os.Remove(dst)
		appliedFiles = append(appliedFiles, rel)
	}

	sort.Strings(appliedFiles)
	sb.Status = StatusPromoted
	return appliedFiles, nil
}

// DiscardSandbox tears down the shadow worktree or mirror directory and env caches.
func DiscardSandbox(sb *Sandbox) error {
	if sb == nil {
		return nil
	}

	if sb.Mode == ModeGitWorktree {
		cmd := exec.Command("git", "-C", sb.OriginPath, "worktree", "remove", "--force", sb.ShadowPath)
		setHideWindow(cmd)
		_ = cmd.Run()
		cmdPrune := exec.Command("git", "-C", sb.OriginPath, "worktree", "prune")
		setHideWindow(cmdPrune)
		_ = cmdPrune.Run()
	}

	_ = forceRemoveAll(sb.ShadowPath)
	_ = forceRemoveAll(sb.EnvCacheDir)

	sb.Status = StatusDiscarded
	return nil
}

// SpeculativeCandidate represents a candidate branch in speculative multi-branch execution.
type SpeculativeCandidate struct {
	Index        int      `json:"index"`
	StrategyName string   `json:"strategy_name"`
	Prompt       string   `json:"prompt"`
	Model        string   `json:"model"`
	Sandbox      *Sandbox `json:"sandbox"`
	BuildPassed  bool     `json:"build_passed"`
	BuildOutput  string   `json:"build_output"`
	FilesChanged int      `json:"files_changed"`
	DurationSec  float64  `json:"duration_s"`
	ResultText   string   `json:"result_text"`
	Score        int      `json:"score"`
	Winner       bool     `json:"winner"`
}

// ScoreCandidate calculates a candidate's score:
// +1000 if BuildPassed == true && FilesChanged > 0,
// +200 if ResultText != "",
// minus int(DurationSec) as tie-breaker for speed.
func ScoreCandidate(c *SpeculativeCandidate) int {
	if c == nil {
		return 0
	}
	score := 0
	if c.BuildPassed && c.FilesChanged > 0 {
		score += 1000
	}
	if strings.TrimSpace(c.ResultText) != "" {
		score += 200
	}
	score -= int(c.DurationSec)
	c.Score = score
	return score
}

// PickSpeculativeWinner evaluates and scores all candidates, selecting the candidate with the highest score as Winner.
func PickSpeculativeWinner(candidates []*SpeculativeCandidate) *SpeculativeCandidate {
	if len(candidates) == 0 {
		return nil
	}

	var best *SpeculativeCandidate
	for _, c := range candidates {
		c.Winner = false
		score := ScoreCandidate(c)
		if best == nil || score > best.Score {
			best = c
		}
	}

	if best != nil {
		best.Winner = true
	}
	return best
}

// DiffHunk represents a single unified diff hunk.
type DiffHunk struct {
	Index     int      `json:"index"`
	Header    string   `json:"header"`
	OldStart  int      `json:"old_start"`
	OldLines  int      `json:"old_lines"`
	NewStart  int      `json:"new_start"`
	NewLines  int      `json:"new_lines"`
	Lines     []string `json:"lines"`
	Additions int      `json:"additions"`
	Deletions int      `json:"deletions"`
}

var hunkHeaderRegex = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@(.*)$`)

// ParseUnifiedDiffHunks parses unified diff text into a slice of DiffHunk objects.
func ParseUnifiedDiffHunks(diffText string) []DiffHunk {
	trimmed := strings.TrimSpace(strings.ReplaceAll(diffText, "\r\n", "\n"))
	if trimmed == "" {
		return nil
	}

	lines := strings.Split(trimmed, "\n")

	var hunks []DiffHunk
	var currentHunk *DiffHunk

	flushHunk := func() {
		if currentHunk != nil {
			hunks = append(hunks, *currentHunk)
			currentHunk = nil
		}
	}

	for _, line := range lines {
		if strings.HasPrefix(line, "@@") {
			matches := hunkHeaderRegex.FindStringSubmatch(line)
			if matches != nil {
				flushHunk()

				oldStart, _ := strconv.Atoi(matches[1])
				oldLines := 1
				if matches[2] != "" {
					oldLines, _ = strconv.Atoi(matches[2])
				} else if oldStart == 0 {
					oldLines = 0
				}

				newStart, _ := strconv.Atoi(matches[3])
				newLines := 1
				if matches[4] != "" {
					newLines, _ = strconv.Atoi(matches[4])
				} else if newStart == 0 {
					newLines = 0
				}

				currentHunk = &DiffHunk{
					Index:     len(hunks),
					Header:    line,
					OldStart:  oldStart,
					OldLines:  oldLines,
					NewStart:  newStart,
					NewLines:  newLines,
					Lines:     make([]string, 0),
					Additions: 0,
					Deletions: 0,
				}
				continue
			}
		}

		if currentHunk != nil {
			if strings.HasPrefix(line, "diff --git") || strings.HasPrefix(line, "--- ") || strings.HasPrefix(line, "+++ ") {
				flushHunk()
				continue
			}

			if strings.HasPrefix(line, "+") {
				currentHunk.Additions++
				currentHunk.Lines = append(currentHunk.Lines, line)
			} else if strings.HasPrefix(line, "-") {
				currentHunk.Deletions++
				currentHunk.Lines = append(currentHunk.Lines, line)
			} else if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\\") {
				currentHunk.Lines = append(currentHunk.Lines, line)
			} else {
				flushHunk()
			}
		}
	}

	flushHunk()
	return hunks
}

type hunkSpan struct {
	index     int
	startLine int
	endLine   int
}

type fileDiffSection struct {
	headerLines []string
	hunks       []hunkSpan
}

// removeHunkFromDiffText removes the hunk at targetHunkIndex from unified diffText.
func removeHunkFromDiffText(diffText string, targetHunkIndex int) string {
	trimmed := strings.TrimSpace(strings.ReplaceAll(diffText, "\r\n", "\n"))
	if trimmed == "" {
		return ""
	}
	diffLines := strings.Split(trimmed, "\n")

	var sections []*fileDiffSection
	var currentSection *fileDiffSection
	currentHunkIndex := 0

	var activeHunk *hunkSpan

	for i, line := range diffLines {
		if strings.HasPrefix(line, "diff --git") {
			if activeHunk != nil {
				activeHunk.endLine = i - 1
				if currentSection != nil {
					currentSection.hunks = append(currentSection.hunks, *activeHunk)
				}
				activeHunk = nil
			}
			currentSection = &fileDiffSection{
				headerLines: []string{line},
			}
			sections = append(sections, currentSection)
			continue
		}

		if strings.HasPrefix(line, "@@") && hunkHeaderRegex.MatchString(line) {
			if activeHunk != nil {
				activeHunk.endLine = i - 1
				if currentSection != nil {
					currentSection.hunks = append(currentSection.hunks, *activeHunk)
				}
				activeHunk = nil
			}
			if currentSection == nil {
				currentSection = &fileDiffSection{}
				sections = append(sections, currentSection)
			}
			activeHunk = &hunkSpan{
				index:     currentHunkIndex,
				startLine: i,
				endLine:   i,
			}
			currentHunkIndex++
			continue
		}

		if activeHunk != nil {
			if strings.HasPrefix(line, "--- ") || strings.HasPrefix(line, "+++ ") ||
				(!strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "-") && !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\\")) {
				activeHunk.endLine = i - 1
				if currentSection != nil {
					currentSection.hunks = append(currentSection.hunks, *activeHunk)
				}
				activeHunk = nil
				if currentSection != nil && (strings.HasPrefix(line, "--- ") || strings.HasPrefix(line, "+++ ")) {
					currentSection.headerLines = append(currentSection.headerLines, line)
				}
				continue
			}
			activeHunk.endLine = i
		} else if currentSection != nil {
			currentSection.headerLines = append(currentSection.headerLines, line)
		}
	}

	if activeHunk != nil && currentSection != nil {
		currentSection.hunks = append(currentSection.hunks, *activeHunk)
	}

	var outLines []string
	for _, sec := range sections {
		var remainingHunks []hunkSpan
		for _, h := range sec.hunks {
			if h.index != targetHunkIndex {
				remainingHunks = append(remainingHunks, h)
			}
		}

		if len(remainingHunks) > 0 {
			outLines = append(outLines, sec.headerLines...)
			for _, h := range remainingHunks {
				for lineIdx := h.startLine; lineIdx <= h.endLine; lineIdx++ {
					outLines = append(outLines, diffLines[lineIdx])
				}
			}
		}
	}

	result := strings.TrimSpace(strings.Join(outLines, "\n"))
	if result == "" {
		return ""
	}
	return result + "\n"
}

// RevertHunkInFile surgically reverts a single hunk in the specified file,
// and returns the remaining diff with the reverted hunk removed.
func RevertHunkInFile(wsPath, relPath string, hunkIndex int, diffText string) (string, error) {
	hunks := ParseUnifiedDiffHunks(diffText)
	if hunkIndex < 0 || hunkIndex >= len(hunks) {
		return "", fmt.Errorf("hunk index %d out of bounds (total hunks: %d)", hunkIndex, len(hunks))
	}

	targetHunk := hunks[hunkIndex]
	filePath := filepath.Join(wsPath, filepath.Clean(filepath.FromSlash(relPath)))

	rawBytes, err := os.ReadFile(filePath)
	if err != nil {
		return "", fmt.Errorf("failed to read file %s: %w", filePath, err)
	}

	hasCRLF := bytes.Contains(rawBytes, []byte("\r\n"))
	contentStr := strings.ReplaceAll(string(rawBytes), "\r\n", "\n")
	hasTrailingNewline := strings.HasSuffix(contentStr, "\n")
	if hasTrailingNewline {
		contentStr = strings.TrimSuffix(contentStr, "\n")
	}

	var fileLines []string
	if contentStr != "" {
		fileLines = strings.Split(contentStr, "\n")
	}

	// Construct afterEdit (current state in file) and beforeEdit (desired reverted state)
	var afterEdit []string
	var beforeEdit []string
	for _, l := range targetHunk.Lines {
		if strings.HasPrefix(l, "\\") {
			continue
		}
		if len(l) == 0 {
			afterEdit = append(afterEdit, "")
			beforeEdit = append(beforeEdit, "")
			continue
		}
		prefix := l[0]
		rest := l[1:]
		switch prefix {
		case ' ':
			afterEdit = append(afterEdit, rest)
			beforeEdit = append(beforeEdit, rest)
		case '+':
			afterEdit = append(afterEdit, rest)
		case '-':
			beforeEdit = append(beforeEdit, rest)
		default:
			afterEdit = append(afterEdit, l)
			beforeEdit = append(beforeEdit, l)
		}
	}

	matchIndex := -1
	targetStart := targetHunk.NewStart - 1
	if targetStart < 0 {
		targetStart = 0
	}

	if len(afterEdit) == 0 {
		// Pure deletion hunk (no context, no additions)
		matchIndex = targetStart
		if matchIndex > len(fileLines) {
			matchIndex = len(fileLines)
		}
	} else {
		// Search for afterEdit in fileLines
		matchesAt := func(idx int) bool {
			if idx < 0 || idx+len(afterEdit) > len(fileLines) {
				return false
			}
			for i := 0; i < len(afterEdit); i++ {
				if fileLines[idx+i] != afterEdit[i] {
					return false
				}
			}
			return true
		}

		if matchesAt(targetStart) {
			matchIndex = targetStart
		} else {
			// Find closest matching position
			bestDist := -1
			for i := 0; i <= len(fileLines)-len(afterEdit); i++ {
				if matchesAt(i) {
					dist := i - targetStart
					if dist < 0 {
						dist = -dist
					}
					if matchIndex == -1 || dist < bestDist {
						matchIndex = i
						bestDist = dist
					}
				}
			}

			// Fallback: match trimmed lines
			if matchIndex == -1 {
				matchesTrimmedAt := func(idx int) bool {
					if idx < 0 || idx+len(afterEdit) > len(fileLines) {
						return false
					}
					for i := 0; i < len(afterEdit); i++ {
						if strings.TrimRight(fileLines[idx+i], " \t") != strings.TrimRight(afterEdit[i], " \t") {
							return false
						}
					}
					return true
				}
				for i := 0; i <= len(fileLines)-len(afterEdit); i++ {
					if matchesTrimmedAt(i) {
						dist := i - targetStart
						if dist < 0 {
							dist = -dist
						}
						if matchIndex == -1 || dist < bestDist {
							matchIndex = i
							bestDist = dist
						}
					}
				}
			}
		}

		if matchIndex == -1 {
			return "", fmt.Errorf("could not locate hunk block in %s (NewStart=%d)", relPath, targetHunk.NewStart)
		}
	}

	// Apply replacement
	newLines := make([]string, 0, len(fileLines)-len(afterEdit)+len(beforeEdit))
	newLines = append(newLines, fileLines[:matchIndex]...)
	newLines = append(newLines, beforeEdit...)
	newLines = append(newLines, fileLines[matchIndex+len(afterEdit):]...)

	lineSep := "\n"
	if hasCRLF {
		lineSep = "\r\n"
	}

	updatedContent := strings.Join(newLines, lineSep)
	if hasTrailingNewline && len(newLines) > 0 {
		updatedContent += lineSep
	}

	if err := os.WriteFile(filePath, []byte(updatedContent), 0644); err != nil {
		return "", fmt.Errorf("failed to write updated file %s: %w", filePath, err)
	}

	// Remove hunk from diffText
	updatedDiff := removeHunkFromDiffText(diffText, hunkIndex)
	return updatedDiff, nil
}
