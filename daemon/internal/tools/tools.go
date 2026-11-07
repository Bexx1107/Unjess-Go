package tools

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf16"

	"unjess-daemon/internal/codeintel"
	"unjess-daemon/internal/config"
	"unjess-daemon/internal/frontier"
	"unjess-daemon/internal/mcp"
	"unjess-daemon/internal/memory"
	"unjess-daemon/internal/security"
	"unjess-daemon/internal/skills"
	"unjess-daemon/internal/workspace"
	"unjess-daemon/internal/worktree"
)

var _ = worktree.ScoreCandidate

// ToolDefinition represents an LLM tool schema.
type ToolDefinition struct {
	Type     string                 `json:"type"`
	Name     string                 `json:"name"`
	Function ToolFunctionDefinition `json:"function"`
}

// ToolFunctionDefinition represents function metadata in an LLM tool definition.
type ToolFunctionDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

// ToolCallRecord tracks a single executed tool call and its outcome.
type ToolCallRecord struct {
	Name        string         `json:"name"`
	Arguments   map[string]any `json:"arguments"`
	Result      string         `json:"result"`
	Status      string         `json:"status"` // "running", "completed", or "error"
	StartedAt   int64          `json:"started_at,omitempty"`
	DurationSec float64        `json:"duration_s,omitempty"`
	startedTime time.Time
}

// FileTouch tracks a file created, modified, or deleted by a tool.
type FileTouch struct {
	Path   string `json:"path"`
	Action string `json:"action"` // "created", "modified", "deleted"
	IsDir  bool   `json:"isDir"`
}

// FileDiff records unified diffs and addition/deletion line counts.
type FileDiff struct {
	Path      string `json:"path"`
	Action    string `json:"action"` // "created", "modified", "deleted"
	Diff      string `json:"diff"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
}

// FileSnapshot tracks the pre-modification state of a file within a turn for clean undo/revert.
type FileSnapshot struct {
	Path          string `json:"path"`
	ExistedBefore bool   `json:"existed_before"`
	BeforeContent string `json:"before_content"`
}

// VisionImageItem represents a loaded or captured image queued for Vision LLM inspection.
type VisionImageItem struct {
	Name    string `json:"name"`
	DataURI string `json:"data_uri"`
}

// TodoItem represents a structured task checklist item managed via todo_write.
type TodoItem struct {
	ID      string `json:"id"`
	Content string `json:"content"`
	Status  string `json:"status"` // "pending", "in_progress", "completed"
}

// FileObservation tracks the observed on-disk state of a file when read or written by the agent (dsh-fs-observation-policy parity).
type FileObservation struct {
	ObservedAt      time.Time
	ModTimeUnixNano int64
	Size            int64
	ContentHash     uint64
	ConfirmedAbsent bool
}

func hashBytesFNV1a(data []byte) uint64 {
	var h uint64 = 14695981039346656037
	for _, b := range data {
		h ^= uint64(b)
		h *= 1099511628211
	}
	if h == 0 {
		return 1
	}
	return h
}

// ToolExecutor executes core agent tools safely within a workspace sandbox.
type ToolExecutor struct {
	WorkspacePath         string
	CommandTimeoutSec     int
	ReadOnly              bool
	EnforceReadBeforeEdit bool
	ProtectedPaths        []string
	ToolCalls             []ToolCallRecord
	FilesTouched          []FileTouch
	Diffs                 []FileDiff
	Snapshots             []FileSnapshot `json:"snapshots"`
	ReadFiles             map[string]time.Time
	FileObservations      map[string]FileObservation
	SessionCwd            string
	SessionEnv            map[string]string
	Todos                 []TodoItem
	UnlockedDeferredTools map[string]bool
	LatestVisualQAImage   string
	PendingVisionImages   []VisionImageItem
	mu                    sync.Mutex
}

// ResetShellSession resets the persistent shell working directory and environment variables (e.g. after a command timeout or abort).
func (t *ToolExecutor) ResetShellSession() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.SessionCwd = t.WorkspacePath
	t.SessionEnv = make(map[string]string)
}

// MarkFileRead records that a file has been read or written in this session (Read-Before-Edit & Version Freshness ledger).
func (t *ToolExecutor) MarkFileRead(relOrAbsPath string) {
	if t == nil || strings.TrimSpace(relOrAbsPath) == "" {
		return
	}
	now := time.Now()
	var obs FileObservation
	obs.ObservedAt = now

	diskPath := relOrAbsPath
	if !filepath.IsAbs(diskPath) && t.WorkspacePath != "" {
		diskPath = filepath.Join(t.WorkspacePath, filepath.FromSlash(relOrAbsPath))
	}
	if info, err := os.Stat(diskPath); err == nil && !info.IsDir() {
		obs.ModTimeUnixNano = info.ModTime().UnixNano()
		obs.Size = info.Size()
		if info.Size() <= 4*1024*1024 {
			if data, errR := os.ReadFile(diskPath); errR == nil {
				obs.ContentHash = hashBytesFNV1a(data)
			}
		}
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	if t.ReadFiles == nil {
		t.ReadFiles = make(map[string]time.Time)
	}
	if t.FileObservations == nil {
		t.FileObservations = make(map[string]FileObservation)
	}
	clean := filepath.ToSlash(filepath.Clean(relOrAbsPath))
	t.ReadFiles[clean] = now
	t.ReadFiles[strings.ToLower(clean)] = now
	t.ReadFiles[filepath.Base(clean)] = now
	if obs.ContentHash != 0 || obs.ModTimeUnixNano != 0 {
		t.FileObservations[clean] = obs
		t.FileObservations[strings.ToLower(clean)] = obs
		if diskClean := filepath.ToSlash(filepath.Clean(diskPath)); diskClean != "" {
			t.FileObservations[diskClean] = obs
			t.FileObservations[strings.ToLower(diskClean)] = obs
		}
	}
	if t.WorkspacePath != "" {
		if rel, err := filepath.Rel(t.WorkspacePath, diskPath); err == nil && !strings.HasPrefix(rel, "..") {
			relSlash := filepath.ToSlash(rel)
			t.ReadFiles[relSlash] = now
			t.ReadFiles[strings.ToLower(relSlash)] = now
			if obs.ContentHash != 0 || obs.ModTimeUnixNano != 0 {
				t.FileObservations[relSlash] = obs
				t.FileObservations[strings.ToLower(relSlash)] = obs
			}
		}
	}
}

// MarkFileAbsent records that read_file observed a file did not exist on disk (Confirmed Absence).
func (t *ToolExecutor) MarkFileAbsent(relOrAbsPath string) {
	if t == nil || strings.TrimSpace(relOrAbsPath) == "" {
		return
	}
	now := time.Now()
	obs := FileObservation{
		ObservedAt:      now,
		ConfirmedAbsent: true,
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.ReadFiles == nil {
		t.ReadFiles = make(map[string]time.Time)
	}
	if t.FileObservations == nil {
		t.FileObservations = make(map[string]FileObservation)
	}
	clean := filepath.ToSlash(filepath.Clean(relOrAbsPath))
	t.ReadFiles[clean] = now
	t.ReadFiles[strings.ToLower(clean)] = now
	t.FileObservations[clean] = obs
	t.FileObservations[strings.ToLower(clean)] = obs
	if t.WorkspacePath != "" {
		diskPath := relOrAbsPath
		if !filepath.IsAbs(diskPath) {
			diskPath = filepath.Join(t.WorkspacePath, filepath.FromSlash(relOrAbsPath))
		}
		if rel, err := filepath.Rel(t.WorkspacePath, diskPath); err == nil && !strings.HasPrefix(rel, "..") {
			relSlash := filepath.ToSlash(rel)
			t.ReadFiles[relSlash] = now
			t.ReadFiles[strings.ToLower(relSlash)] = now
			t.FileObservations[relSlash] = obs
			t.FileObservations[strings.ToLower(relSlash)] = obs
		}
	}
}

// CheckFileFreshness verifies that the on-disk file has not been modified externally since the agent's last read or write.
func (t *ToolExecutor) CheckFileFreshness(safePath, displayPath string, currentData []byte) error {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.FileObservations) == 0 {
		return nil
	}
	clean := filepath.ToSlash(filepath.Clean(safePath))
	obs, ok := t.FileObservations[clean]
	if !ok {
		obs, ok = t.FileObservations[strings.ToLower(clean)]
	}
	if !ok && displayPath != "" {
		dispClean := filepath.ToSlash(filepath.Clean(displayPath))
		obs, ok = t.FileObservations[dispClean]
		if !ok {
			obs, ok = t.FileObservations[strings.ToLower(dispClean)]
		}
	}
	if !ok {
		return nil
	}
	if obs.ConfirmedAbsent {
		return fmt.Errorf("Error [FS_STALE_VERSION]: File '%s' was previously observed absent by read_file, but now exists on disk (%d bytes). Re-read the file with read_file to inspect its current contents before modifying it.", displayPath, len(currentData))
	}
	if obs.ContentHash == 0 {
		return nil
	}
	currentHash := hashBytesFNV1a(currentData)
	if currentHash != obs.ContentHash {
		return fmt.Errorf("Error [FS_STALE_VERSION]: File '%s' has been modified on disk since you last read or edited it (observed %d bytes -> current %d bytes). Re-read the file with read_file to inspect the latest contents before editing so external changes are not overwritten.", displayPath, obs.Size, len(currentData))
	}
	return nil
}

// HasReadFile checks whether a file has been read or modified in this session.
func (t *ToolExecutor) HasReadFile(relOrAbsPath string) bool {
	if t == nil || strings.TrimSpace(relOrAbsPath) == "" {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	clean := filepath.ToSlash(filepath.Clean(relOrAbsPath))
	if t.ReadFiles != nil {
		if _, ok := t.ReadFiles[clean]; ok {
			return true
		}
		if _, ok := t.ReadFiles[strings.ToLower(clean)]; ok {
			return true
		}
		if _, ok := t.ReadFiles[filepath.Base(clean)]; ok {
			return true
		}
	}
	for _, ft := range t.FilesTouched {
		ftClean := filepath.ToSlash(filepath.Clean(ft.Path))
		if ftClean == clean || strings.EqualFold(ftClean, clean) || filepath.Base(ftClean) == filepath.Base(clean) {
			return true
		}
	}
	return false
}

// RevertAllSnapshots restores all modified/created files in the turn back to their pre-turn state.
func (t *ToolExecutor) RevertAllSnapshots() ([]string, error) {
	if t == nil {
		return nil, nil
	}
	t.mu.Lock()
	snaps := make([]FileSnapshot, len(t.Snapshots))
	copy(snaps, t.Snapshots)
	t.mu.Unlock()

	var reverted []string
	var lastErr error
	for _, snap := range snaps {
		targetPath := snap.Path
		if !filepath.IsAbs(targetPath) {
			targetPath = filepath.Join(t.WorkspacePath, filepath.FromSlash(snap.Path))
		}
		if snap.ExistedBefore {
			if err := writeFileWithRetry(targetPath, []byte(snap.BeforeContent), 0644); err != nil {
				lastErr = err
			} else {
				reverted = append(reverted, snap.Path)
				t.MarkFileRead(targetPath)
				t.MarkFileRead(snap.Path)
			}
		} else {
			if err := os.Remove(targetPath); err != nil && !os.IsNotExist(err) {
				lastErr = err
			} else {
				reverted = append(reverted, snap.Path+" (removed)")
				t.MarkFileAbsent(targetPath)
				t.MarkFileAbsent(snap.Path)
			}
		}
	}
	t.mu.Lock()
	t.FilesTouched = make([]FileTouch, 0)
	t.Diffs = make([]FileDiff, 0)
	t.mu.Unlock()
	return reverted, lastErr
}

// QueueVisionImage thread-safely queues an image dataURI for attachment to the Vision LLM input on the next step.
func (t *ToolExecutor) QueueVisionImage(name, dataURI string) {
	if strings.TrimSpace(dataURI) == "" {
		return
	}
	if strings.TrimSpace(name) == "" {
		name = "visual_qa.jpg"
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.LatestVisualQAImage = dataURI
	for _, existing := range t.PendingVisionImages {
		if existing.Name == name && existing.DataURI == dataURI {
			return
		}
	}
	t.PendingVisionImages = append(t.PendingVisionImages, VisionImageItem{
		Name:    name,
		DataURI: dataURI,
	})
}

// DrainVisionImages thread-safely drains and returns all queued Vision images for the current step.
func (t *ToolExecutor) DrainVisionImages() []VisionImageItem {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	items := t.PendingVisionImages
	if len(items) == 0 && t.LatestVisualQAImage != "" {
		items = []VisionImageItem{
			{
				Name:    "visual_qa.jpg",
				DataURI: t.LatestVisualQAImage,
			},
		}
	}
	t.PendingVisionImages = nil
	t.LatestVisualQAImage = ""
	return items
}

// TouchFile thread-safely records a file touch action.
func (t *ToolExecutor) TouchFile(relPath, action string, isDir bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.touchFile(relPath, action, isDir)
}

// NewToolExecutor creates a new ToolExecutor bound to the given workspace.
func NewToolExecutor(wsPath string, timeoutSec int) *ToolExecutor {
	if timeoutSec <= 0 {
		timeoutSec = 120
	}
	return &ToolExecutor{
		WorkspacePath:         wsPath,
		CommandTimeoutSec:     timeoutSec,
		ProtectedPaths:        make([]string, 0),
		ToolCalls:             make([]ToolCallRecord, 0),
		FilesTouched:          make([]FileTouch, 0),
		Diffs:                 make([]FileDiff, 0),
		Snapshots:             make([]FileSnapshot, 0),
		ReadFiles:             make(map[string]time.Time),
		FileObservations:      make(map[string]FileObservation),
		SessionCwd:            wsPath,
		SessionEnv:            make(map[string]string),
		Todos:                 make([]TodoItem, 0),
		UnlockedDeferredTools: make(map[string]bool),
		PendingVisionImages:   make([]VisionImageItem, 0),
	}
}

// SetProtectedPaths registers files/paths that cannot be modified or deleted.
func (t *ToolExecutor) SetProtectedPaths(paths []string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.ProtectedPaths = paths
}

// IsProtected checks whether a given file path is in the protected paths list.
func (t *ToolExecutor) IsProtected(relOrAbsPath string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.ProtectedPaths) == 0 || strings.TrimSpace(relOrAbsPath) == "" {
		return false
	}
	cleanInput := filepath.ToSlash(filepath.Clean(relOrAbsPath))
	cleanInputBase := filepath.Base(cleanInput)

	for _, p := range t.ProtectedPaths {
		cleanP := filepath.ToSlash(filepath.Clean(p))
		if cleanInput == cleanP || strings.HasSuffix(cleanInput, "/"+cleanP) || strings.HasSuffix(cleanP, "/"+cleanInput) {
			return true
		}
		if cleanInputBase == filepath.Base(cleanP) && (strings.Contains(cleanP, "/") || strings.Contains(cleanInput, "/")) {
			return true
		}
	}
	return false
}

// BeginToolCall thread-safely appends a tool call with status "running" and returns its index.
func (t *ToolExecutor) BeginToolCall(name string, args map[string]any) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now()
	idx := len(t.ToolCalls)
	t.ToolCalls = append(t.ToolCalls, ToolCallRecord{
		Name:        name,
		Arguments:   args,
		Result:      "Executing...",
		Status:      "running",
		StartedAt:   now.Unix(),
		startedTime: now,
	})
	return idx
}

// CompleteToolCall thread-safely updates the tool call at idx with its final result and status.
func (t *ToolExecutor) CompleteToolCall(idx int, result string, success bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	status := "completed"
	if !success {
		status = "error"
	}
	if idx >= 0 && idx < len(t.ToolCalls) {
		t.ToolCalls[idx].Result = result
		t.ToolCalls[idx].Status = status
		if !t.ToolCalls[idx].startedTime.IsZero() {
			t.ToolCalls[idx].DurationSec = time.Since(t.ToolCalls[idx].startedTime).Seconds()
		}
	} else {
		t.ToolCalls = append(t.ToolCalls, ToolCallRecord{
			Result: result,
			Status: status,
		})
	}
}

// Snapshot returns a thread-safe copy of ToolCalls, FilesTouched, and Diffs.
func (t *ToolExecutor) Snapshot() ([]ToolCallRecord, []FileTouch, []FileDiff) {
	if t == nil {
		return nil, nil, nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	tcCopy := make([]ToolCallRecord, len(t.ToolCalls))
	copy(tcCopy, t.ToolCalls)

	ftCopy := make([]FileTouch, len(t.FilesTouched))
	copy(ftCopy, t.FilesTouched)

	dfCopy := make([]FileDiff, len(t.Diffs))
	copy(dfCopy, t.Diffs)

	return tcCopy, ftCopy, dfCopy
}

// CoreToolDefinitions returns JSON Schema definitions for all core tools.
func CoreToolDefinitions() []map[string]any {
	defs := []map[string]any{
		{
			"type":        "function",
			"name":        "read_file",
			"description": "Read text file contents from the workspace sandbox with optional 1-indexed line ranges, or view an image file (.png, .jpg, .jpeg, .webp, .gif). Do NOT re-read a file you just edited with edit_file or write_file to verify — the edit tool would have returned an error if the change failed.",
			"function": map[string]any{
				"name":        "read_file",
				"description": "Read text file contents from the workspace sandbox with optional 1-indexed line ranges, or view an image file (.png, .jpg, .jpeg, .webp, .gif). Do NOT re-read a file you just edited with edit_file or write_file to verify — the edit tool would have returned an error if the change failed.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path": map[string]any{
							"type":        "string",
							"description": "Path to the file relative to the workspace root (or image file path).",
						},
						"start_line": map[string]any{
							"type":        "integer",
							"description": "Optional 1-indexed start line (inclusive).",
						},
						"end_line": map[string]any{
							"type":        "integer",
							"description": "Optional 1-indexed end line (inclusive).",
						},
					},
					"required": []string{"path"},
				},
			},
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Path to the file relative to the workspace root (or image file path).",
					},
					"start_line": map[string]any{
						"type":        "integer",
						"description": "Optional 1-indexed start line (inclusive).",
					},
					"end_line": map[string]any{
						"type":        "integer",
						"description": "Optional 1-indexed end line (inclusive).",
					},
				},
				"required": []string{"path"},
			},
		},
		{
			"type":        "function",
			"name":        "view_image",
			"description": "View and inspect one or more local image files (.jpg, .jpeg, .png, .webp, .gif, .bmp) on disk using your multimodal Vision input. Pass 'path' for a single image or 'paths' (array of file paths) to inspect up to 6 images in a single turn.",
			"function": map[string]any{
				"name":        "view_image",
				"description": "View and inspect one or more local image files (.jpg, .jpeg, .png, .webp, .gif, .bmp) on disk using your multimodal Vision input. Pass 'path' for a single image or 'paths' (array of file paths) to inspect up to 6 images in a single turn.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path": map[string]any{
							"type":        "string",
							"description": "Path to an image file (relative to workspace root or absolute path on disk).",
						},
						"paths": map[string]any{
							"type":        "array",
							"description": "Optional array of multiple image file paths to view and compare simultaneously (up to 6 images).",
							"items": map[string]any{
								"type": "string",
							},
						},
					},
				},
			},
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Path to an image file (relative to workspace root or absolute path on disk).",
					},
					"paths": map[string]any{
						"type":        "array",
						"description": "Optional array of multiple image file paths to view and compare simultaneously (up to 6 images).",
						"items": map[string]any{
							"type": "string",
						},
					},
				},
			},
		},
		{
			"type":        "function",
			"name":        "write_file",
			"description": "Create a new file or completely overwrite an existing file inside the workspace. ALWAYS prefer edit_file for modifying existing files. You must read an existing file with read_file before overwriting it.",
			"function": map[string]any{
				"name":        "write_file",
				"description": "Create a new file or completely overwrite an existing file inside the workspace. ALWAYS prefer edit_file for modifying existing files. You must read an existing file with read_file before overwriting it.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path": map[string]any{
							"type":        "string",
							"description": "Path to the file relative to the workspace root.",
						},
						"content": map[string]any{
							"type":        "string",
							"description": "Full UTF-8 content to write into the file.",
						},
					},
					"required": []string{"path", "content"},
				},
			},
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Path to the file relative to the workspace root.",
					},
					"content": map[string]any{
						"type":        "string",
						"description": "Full UTF-8 content to write into the file.",
					},
				},
				"required": []string{"path", "content"},
			},
		},
		{
			"type":        "function",
			"name":        "edit_file",
			"description": "Performs exact string replacement in a file. You must read the file with read_file in this conversation before editing. Keep old_string minimal (usually 1-4 lines, only enough surrounding context to be unique in the file). Never include the read_file line-number prefix (e.g. '12: ') in old_string or new_string. Use replace_all: true to replace every occurrence.",
			"function": map[string]any{
				"name":        "edit_file",
				"description": "Performs exact string replacement in a file. You must read the file with read_file in this conversation before editing. Keep old_string minimal (usually 1-4 lines, only enough surrounding context to be unique in the file). Never include the read_file line-number prefix (e.g. '12: ') in old_string or new_string. Use replace_all: true to replace every occurrence.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path": map[string]any{
							"type":        "string",
							"description": "Path to the file relative to the workspace root.",
						},
						"old_string": map[string]any{
							"type":        "string",
							"description": "Exact target string or code block to find and replace (without read_file line-number prefixes).",
						},
						"new_string": map[string]any{
							"type":        "string",
							"description": "Replacement string or code block (must be different from old_string).",
						},
						"replace_all": map[string]any{
							"type":        "boolean",
							"description": "Optional: replace all occurrences of old_string in the file (default false).",
						},
					},
					"required": []string{"path", "old_string", "new_string"},
				},
			},
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Path to the file relative to the workspace root.",
					},
					"old_string": map[string]any{
						"type":        "string",
						"description": "Exact target string or code block to find and replace (without read_file line-number prefixes).",
					},
					"new_string": map[string]any{
						"type":        "string",
						"description": "Replacement string or code block (must be different from old_string).",
					},
					"replace_all": map[string]any{
						"type":        "boolean",
						"description": "Optional: replace all occurrences of old_string in the file (default false).",
					},
				},
				"required": []string{"path", "old_string", "new_string"},
			},
		},
		{
			"type":        "function",
			"name":        "list_dir",
			"description": "List directory contents including file sizes and subdirectories.",
			"function": map[string]any{
				"name":        "list_dir",
				"description": "List directory contents including file sizes and subdirectories.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path": map[string]any{
							"type":        "string",
							"description": "Relative directory path (defaults to '.' for workspace root).",
						},
					},
				},
			},
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Relative directory path (defaults to '.' for workspace root).",
					},
				},
			},
		},
		{
			"type":        "function",
			"name":        "grep_search",
			"description": "Search for a text pattern or regex across workspace files.",
			"function": map[string]any{
				"name":        "grep_search",
				"description": "Search for a text pattern or regex across workspace files.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"pattern": map[string]any{
							"type":        "string",
							"description": "Search pattern or regex to search for.",
						},
						"path": map[string]any{
							"type":        "string",
							"description": "Optional subdirectory or file to restrict search to.",
						},
					},
					"required": []string{"pattern"},
				},
			},
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"pattern": map[string]any{
						"type":        "string",
						"description": "Search pattern or regex to search for.",
					},
					"path": map[string]any{
						"type":        "string",
						"description": "Optional subdirectory or file to restrict search to.",
					},
				},
				"required": []string{"pattern"},
			},
		},
		{
			"type":        "function",
			"name":        "find_by_name",
			"description": "Find files matching a glob pattern (e.g. '*.go', 'config.*') in the workspace.",
			"function": map[string]any{
				"name":        "find_by_name",
				"description": "Find files matching a glob pattern (e.g. '*.go', 'config.*') in the workspace.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"pattern": map[string]any{
							"type":        "string",
							"description": "File name pattern or glob to find.",
						},
					},
					"required": []string{"pattern"},
				},
			},
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"pattern": map[string]any{
						"type":        "string",
						"description": "File name pattern or glob to find.",
					},
				},
				"required": []string{"pattern"},
			},
		},
		{
			"type":        "function",
			"name":        "run_command",
			"description": "Execute a shell command inside the workspace directory. Commands are cheap to run and their errors are informative: run the straightforward command rather than perfecting it mentally first, and adjust from what it prints. Do not use run_command for file reading/editing/searching when read_file, edit_file, write_file, grep_search, or find_by_name can do the job. Do not sleep between commands or poll in a sleep loop — diagnose the root cause.",
			"function": map[string]any{
				"name":        "run_command",
				"description": "Execute a shell command inside the workspace directory. Commands are cheap to run and their errors are informative: run the straightforward command rather than perfecting it mentally first, and adjust from what it prints. Do not use run_command for file reading/editing/searching when read_file, edit_file, write_file, grep_search, or find_by_name can do the job. Do not sleep between commands or poll in a sleep loop — diagnose the root cause.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"command": map[string]any{
							"type":        "string",
							"description": "The command string to execute.",
						},
						"cwd": map[string]any{
							"type":        "string",
							"description": "Optional relative working directory within the workspace.",
						},
						"timeout_seconds": map[string]any{
							"type":        "integer",
							"description": "Optional timeout in seconds for long builds/tests (default 120s, up to 1800s).",
						},
						"is_background": map[string]any{
							"type":        "boolean",
							"description": "Optional. If true, runs asynchronously in background and returns task_id immediately.",
						},
					},
					"required": []string{"command"},
				},
			},
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"command": map[string]any{
						"type":        "string",
						"description": "The command string to execute.",
					},
					"cwd": map[string]any{
						"type":        "string",
						"description": "Optional relative working directory within the workspace.",
					},
					"timeout_seconds": map[string]any{
						"type":        "integer",
						"description": "Optional timeout in seconds for long builds/tests (default 120s, up to 1800s).",
					},
					"is_background": map[string]any{
						"type":        "boolean",
						"description": "Optional. If true, runs asynchronously in background and returns task_id immediately.",
					},
				},
				"required": []string{"command"},
			},
		},
		{
			"type":        "function",
			"name":        "start_task",
			"description": "Start a long-running process in the background (asynchronous execution) and return immediately with a task_id.",
			"function": map[string]any{
				"name":        "start_task",
				"description": "Start a long-running process in the background (asynchronous execution) and return immediately with a task_id.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"command": map[string]any{
							"type":        "string",
							"description": "The command string to execute in the background.",
						},
						"cwd": map[string]any{
							"type":        "string",
							"description": "Optional relative working directory within the workspace.",
						},
					},
					"required": []string{"command"},
				},
			},
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"command": map[string]any{
						"type":        "string",
						"description": "The command string to execute in the background.",
					},
					"cwd": map[string]any{
						"type":        "string",
						"description": "Optional relative working directory within the workspace.",
					},
				},
				"required": []string{"command"},
			},
		},
		{
			"type":        "function",
			"name":        "poll_task",
			"description": "Check the status, running state, exit code, and recent log output of a background task.",
			"function": map[string]any{
				"name":        "poll_task",
				"description": "Check the status, running state, exit code, and recent log output of a background task.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"task_id": map[string]any{
							"type":        "string",
							"description": "The task_id returned by start_task or run_command (with is_background: true).",
						},
					},
					"required": []string{"task_id"},
				},
			},
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"task_id": map[string]any{
						"type":        "string",
						"description": "The task_id returned by start_task or run_command (with is_background: true).",
					},
				},
				"required": []string{"task_id"},
			},
		},
		{
			"type":        "function",
			"name":        "cancel_task",
			"description": "Terminate a running background task process.",
			"function": map[string]any{
				"name":        "cancel_task",
				"description": "Terminate a running background task process.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"task_id": map[string]any{
							"type":        "string",
							"description": "The task_id to terminate.",
						},
					},
					"required": []string{"task_id"},
				},
			},
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"task_id": map[string]any{
						"type":        "string",
						"description": "The task_id to terminate.",
					},
				},
				"required": []string{"task_id"},
			},
		},
		{
			"type":        "function",
			"name":        "git_status",
			"description": "Check current git branch, dirty status, and changed files.",
			"function": map[string]any{
				"name":        "git_status",
				"description": "Check current git branch, dirty status, and changed files.",
				"parameters": map[string]any{
					"type":       "object",
					"properties": map[string]any{},
				},
			},
			"parameters": map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
		{
			"type":        "function",
			"name":        "git_diff",
			"description": "Show uncommitted git diff against HEAD in the workspace.",
			"function": map[string]any{
				"name":        "git_diff",
				"description": "Show uncommitted git diff against HEAD in the workspace.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path": map[string]any{
							"type":        "string",
							"description": "Optional specific file path to inspect.",
						},
					},
				},
			},
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Optional specific file path to inspect.",
					},
				},
			},
		},
		{
			"type":        "function",
			"name":        "invoke_subagent",
			"description": "Spawn one or more specialized subagents concurrently in parallel goroutines ('research' for fast read-only codebase exploration, 'coder' for targeted implementation, 'tester' for independent test/build verification). Subagents cannot see your conversation — make every subagent prompt self-contained with exact file paths, line numbers, and what 'done' looks like.",
			"function": map[string]any{
				"name":        "invoke_subagent",
				"description": "Spawn one or more specialized subagents concurrently in parallel goroutines ('research' for fast read-only codebase exploration, 'coder' for targeted implementation, 'tester' for independent test/build verification). Subagents cannot see your conversation — make every subagent prompt self-contained with exact file paths, line numbers, and what 'done' looks like.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"subagents": map[string]any{
							"type":        "array",
							"description": "List of specialized subagents to spawn concurrently.",
							"items": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"role": map[string]any{
										"type":        "string",
										"description": "A 2-5 word description of the subagent's role, e.g. 'Go HTTP API Server Engineer', 'Frontend UI Engineer', 'Database Debugger'.",
									},
									"prompt": map[string]any{
										"type":        "string",
										"description": "A clear, actionable task description for the subagent. Be specific about what files to inspect/edit and what to return.",
									},
									"model": map[string]any{
										"type":        "string",
										"description": "Optional model override or 'inherit' (default uses the active model).",
									},
									"type_name": map[string]any{
										"type":        "string",
										"description": "Subagent type ('self', 'research', 'coder', 'tester'). Defaults to 'self'.",
									},
									"max_turns": map[string]any{
										"type":        "integer",
										"description": "Optional maximum turns/iterations allocated to this subagent (defaults to 300). Set higher for complex building/refactoring tasks.",
									},
								},
								"required": []string{"role", "prompt"},
							},
						},
						"ask_user": map[string]any{
							"type":        "boolean",
							"description": "Set to true when the user asks to choose/approve the model(s) for the subagents before spawning, or when you want the user to pick models in the UI.",
						},
					},
					"required": []string{"subagents"},
				},
			},
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"subagents": map[string]any{
						"type":        "array",
						"description": "List of specialized subagents to spawn concurrently.",
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"role": map[string]any{
									"type":        "string",
									"description": "A 2-5 word description of the subagent's role, e.g. 'Go HTTP API Server Engineer', 'Frontend UI Engineer', 'Database Debugger'.",
								},
								"prompt": map[string]any{
									"type":        "string",
									"description": "A clear, actionable task description for the subagent. Be specific about what files to inspect/edit and what to return.",
								},
								"model": map[string]any{
									"type":        "string",
									"description": "Optional model override or 'inherit' (default uses the active model).",
								},
								"type_name": map[string]any{
									"type":        "string",
									"description": "Subagent type ('self', 'research', 'coder', 'tester'). Defaults to 'self'.",
								},
								"max_turns": map[string]any{
									"type":        "integer",
									"description": "Optional maximum turns/iterations allocated to this subagent (defaults to 300). Set higher for complex building/refactoring tasks.",
								},
							},
							"required": []string{"role", "prompt"},
						},
					},
					"ask_user": map[string]any{
						"type":        "boolean",
						"description": "Set to true when the user asks to choose/approve the model(s) for the subagents before spawning, or when you want the user to pick models in the UI.",
					},
				},
				"required": []string{"subagents"},
			},
		},
		{
			"type":        "function",
			"name":        "memory_save",
			"description": "Save a persistent cross-session memory (user preferences, project architecture, important facts, or decisions) to ~/.unjess/memory/memories.json so it is remembered across all future conversations and workspaces.",
			"function": map[string]any{
				"name":        "memory_save",
				"description": "Save a persistent cross-session memory (user preferences, project architecture, important facts, or decisions) to ~/.unjess/memory/memories.json so it is remembered across all future conversations and workspaces.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"key": map[string]any{
							"type":        "string",
							"description": "Short identifier or topic key for this memory (e.g. user_stack_preference, mashina_project_notes).",
						},
						"content": map[string]any{
							"type":        "string",
							"description": "Full information or fact to remember across sessions.",
						},
						"category": map[string]any{
							"type":        "string",
							"description": "Category for this memory: 'preference', 'project', 'fact', 'decision', 'architecture'.",
						},
						"scope": map[string]any{
							"type":        "string",
							"description": "Memory scope: 'global' (applies to all workspaces) or 'workspace' (tied to current workspace). Default is 'global'.",
						},
					},
					"required": []string{"key", "content"},
				},
			},
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"key": map[string]any{
						"type":        "string",
						"description": "Short identifier or topic key for this memory (e.g. user_stack_preference, mashina_project_notes).",
					},
					"content": map[string]any{
						"type":        "string",
						"description": "Full information or fact to remember across sessions.",
					},
					"category": map[string]any{
						"type":        "string",
						"description": "Category for this memory: 'preference', 'project', 'fact', 'decision', 'architecture'.",
					},
					"scope": map[string]any{
						"type":        "string",
						"description": "Memory scope: 'global' (applies to all workspaces) or 'workspace' (tied to current workspace). Default is 'global'.",
					},
				},
				"required": []string{"key", "content"},
			},
		},
		{
			"type":        "function",
			"name":        "memory_recall",
			"description": "Search and recall persistent cross-session memories from ~/.unjess/memory/memories.json across all past sessions.",
			"function": map[string]any{
				"name":        "memory_recall",
				"description": "Search and recall persistent cross-session memories from ~/.unjess/memory/memories.json across all past sessions.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"query": map[string]any{
							"type":        "string",
							"description": "Search query to filter memories by key, content, category, or tags. Leave empty to list all memories.",
						},
						"category": map[string]any{
							"type":        "string",
							"description": "Optional category filter: 'preference', 'project', 'fact', 'decision', 'architecture'.",
						},
					},
				},
			},
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{
						"type":        "string",
						"description": "Search query to filter memories by key, content, category, or tags. Leave empty to list all memories.",
					},
					"category": map[string]any{
						"type":        "string",
						"description": "Optional category filter: 'preference', 'project', 'fact', 'decision', 'architecture'.",
					},
				},
			},
		},
		{
			"type":        "function",
			"name":        "memory_delete",
			"description": "Delete a persistent memory entry by key or ID.",
			"function": map[string]any{
				"name":        "memory_delete",
				"description": "Delete a persistent memory entry by key or ID.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"key": map[string]any{
							"type":        "string",
							"description": "The key or ID of the memory entry to delete.",
						},
					},
					"required": []string{"key"},
				},
			},
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"key": map[string]any{
						"type":        "string",
						"description": "The key or ID of the memory entry to delete.",
					},
				},
				"required": []string{"key"},
			},
		},
		{
			"type":        "function",
			"name":        "web_search",
			"description": "Search the live web (via DuckDuckGo) for up-to-date information, documentation, libraries, or answers. Returns titles, URLs, and text snippets.",
			"function": map[string]any{
				"name":        "web_search",
				"description": "Search the live web (via DuckDuckGo) for up-to-date information, documentation, libraries, or answers. Returns titles, URLs, and text snippets.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"query": map[string]any{
							"type":        "string",
							"description": "The search query string.",
						},
						"max_results": map[string]any{
							"type":        "integer",
							"description": "Maximum number of search results to return (default: 6).",
						},
					},
					"required": []string{"query"},
				},
			},
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{
						"type":        "string",
						"description": "The search query string.",
					},
					"max_results": map[string]any{
						"type":        "integer",
						"description": "Maximum number of search results to return (default: 6).",
					},
				},
				"required": []string{"query"},
			},
		},
		{
			"type":        "function",
			"name":        "read_url",
			"description": "Fetch and read the text/markdown content of any public web URL (HTTP/HTTPS documentation, articles, APIs, GitHub pages).",
			"function": map[string]any{
				"name":        "read_url",
				"description": "Fetch and read the text/markdown content of any public web URL (HTTP/HTTPS documentation, articles, APIs, GitHub pages).",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"url": map[string]any{
							"type":        "string",
							"description": "The public HTTP or HTTPS web URL to fetch and parse.",
						},
					},
					"required": []string{"url"},
				},
			},
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"url": map[string]any{
						"type":        "string",
						"description": "The public HTTP or HTTPS web URL to fetch and parse.",
					},
				},
				"required": []string{"url"},
			},
		},
		{
			"type":        "function",
			"name":        "mcp_status",
			"description": "Check the live connection status, configured servers, and available tools of all Model Context Protocol (MCP) servers (e.g. mashina-collab, rea, filesystem, github, fetch).",
			"function": map[string]any{
				"name":        "mcp_status",
				"description": "Check the live connection status, configured servers, and available tools of all Model Context Protocol (MCP) servers (e.g. mashina-collab, rea, filesystem, github, fetch).",
				"parameters": map[string]any{
					"type":       "object",
					"properties": map[string]any{},
				},
			},
			"parameters": map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
		{
			"type":        "function",
			"name":        "ask_question",
			"description": "Ask the user one or more interactive multiple-choice questions with optional custom text input to clarify requirements, select implementation choices, or resolve ambiguities before taking action.",
			"function": map[string]any{
				"name":        "ask_question",
				"description": "Ask the user one or more interactive multiple-choice questions with optional custom text input to clarify requirements, select implementation choices, or resolve ambiguities before taking action.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"questions": map[string]any{
							"type":        "array",
							"description": "List of interactive questions to present to the user.",
							"items": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"question": map[string]any{
										"type":        "string",
										"description": "The question prompt to display to the user.",
									},
									"options": map[string]any{
										"type":        "array",
										"description": "Selectable choices for the user (at least 2 choices).",
										"items": map[string]any{
											"type": "string",
										},
									},
									"is_multi_select": map[string]any{
										"type":        "boolean",
										"description": "Set to true to allow multiple choices to be selected.",
									},
								},
								"required": []string{"question", "options"},
							},
						},
					},
					"required": []string{"questions"},
				},
			},
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"questions": map[string]any{
						"type":        "array",
						"description": "List of interactive questions to present to the user.",
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"question": map[string]any{
									"type":        "string",
									"description": "The question prompt to display to the user.",
								},
								"options": map[string]any{
									"type":        "array",
									"description": "Selectable choices for the user (at least 2 choices).",
									"items": map[string]any{
										"type": "string",
									},
								},
								"is_multi_select": map[string]any{
									"type":        "boolean",
									"description": "Set to true to allow multiple choices to be selected.",
								},
							},
							"required": []string{"question", "options"},
						},
					},
				},
				"required": []string{"questions"},
			},
		},
		{
			"type":        "function",
			"name":        "propose_plan",
			"description": "Propose an architectural or implementation plan to the user for formal approval before making code modifications or in Plan mode. Automatically generates or updates PLAN.md in the workspace and pauses until approved or given feedback.",
			"function": map[string]any{
				"name":        "propose_plan",
				"description": "Propose an architectural or implementation plan to the user for formal approval before making code modifications or in Plan mode. Automatically generates or updates PLAN.md in the workspace and pauses until approved or given feedback.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"title": map[string]any{
							"type":        "string",
							"description": "Descriptive title for the implementation plan.",
						},
						"summary": map[string]any{
							"type":        "string",
							"description": "Executive summary of the proposed solution and architecture.",
						},
						"steps": map[string]any{
							"type":        "array",
							"description": "Numbered or sequenced action steps to be executed.",
							"items": map[string]any{
								"type": "string",
							},
						},
						"open_questions": map[string]any{
							"type":        "array",
							"description": "Optional open questions or risks for the user's attention.",
							"items": map[string]any{
								"type": "string",
							},
						},
					},
					"required": []string{"title", "summary", "steps"},
				},
			},
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"title": map[string]any{
						"type":        "string",
						"description": "Descriptive title for the implementation plan.",
					},
					"summary": map[string]any{
						"type":        "string",
						"description": "Executive summary of the proposed solution and architecture.",
					},
					"steps": map[string]any{
						"type":        "array",
						"description": "Numbered or sequenced action steps to be executed.",
						"items": map[string]any{
							"type": "string",
						},
					},
					"open_questions": map[string]any{
						"type":        "array",
						"description": "Optional open questions or risks for the user's attention.",
						"items": map[string]any{
							"type": "string",
						},
					},
				},
				"required": []string{"title", "summary", "steps"},
			},
		},
		{
			"type":        "function",
			"name":        "ast_outline",
			"description": "Extract the structural AST outline (functions, methods, classes, structs, interfaces, and exact start-end line numbers) of a file without reading the entire file.",
			"function": map[string]any{
				"name":        "ast_outline",
				"description": "Extract the structural AST outline (functions, methods, classes, structs, interfaces, and exact start-end line numbers) of a file without reading the entire file.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path": map[string]any{
							"type":        "string",
							"description": "Path to the source file relative to workspace root.",
						},
					},
					"required": []string{"path"},
				},
			},
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Path to the source file relative to workspace root.",
					},
				},
				"required": []string{"path"},
			},
		},
		{
			"type":        "function",
			"name":        "ast_find_callers",
			"description": "Find all definitions and cross-repository call/reference sites of a function, method, struct, or symbol across the workspace.",
			"function": map[string]any{
				"name":        "ast_find_callers",
				"description": "Find all definitions and cross-repository call/reference sites of a function, method, struct, or symbol across the workspace.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"symbol": map[string]any{
							"type":        "string",
							"description": "Symbol name, function name, method name, or identifier to find across the workspace.",
						},
					},
					"required": []string{"symbol"},
				},
			},
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"symbol": map[string]any{
						"type":        "string",
						"description": "Symbol name, function name, method name, or identifier to find across the workspace.",
					},
				},
				"required": []string{"symbol"},
			},
		},
		{
			"type":        "function",
			"name":        "ast_edit_symbol",
			"description": "AST-native refactoring: replace an entire function, method, struct, or class by its symbol name (immune to whitespace or line-number shifts) and automatically check cross-repo callers and linter status.",
			"function": map[string]any{
				"name":        "ast_edit_symbol",
				"description": "AST-native refactoring: replace an entire function, method, struct, or class by its symbol name (immune to whitespace or line-number shifts) and automatically check cross-repo callers and linter status.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path": map[string]any{
							"type":        "string",
							"description": "Path to the source file relative to workspace root.",
						},
						"symbol": map[string]any{
							"type":        "string",
							"description": "Symbol name of the function, method, class, or struct to replace.",
						},
						"new_code": map[string]any{
							"type":        "string",
							"description": "New replacement code block for the target symbol.",
						},
					},
					"required": []string{"path", "symbol", "new_code"},
				},
			},
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Path to the source file relative to workspace root.",
					},
					"symbol": map[string]any{
						"type":        "string",
						"description": "Symbol name of the function, method, class, or struct to replace.",
					},
					"new_code": map[string]any{
						"type":        "string",
						"description": "New replacement code block for the target symbol.",
					},
				},
				"required": []string{"path", "symbol", "new_code"},
			},
		},
		{
			"type":        "function",
			"name":        "ast_rename_symbol",
			"description": "Rename a function, type, or identifier across all source files in the repository using word-boundary token matching.",
			"function": map[string]any{
				"name":        "ast_rename_symbol",
				"description": "Rename a function, type, or identifier across all source files in the repository using word-boundary token matching.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"old_name": map[string]any{
							"type":        "string",
							"description": "Current identifier/symbol name to replace.",
						},
						"new_name": map[string]any{
							"type":        "string",
							"description": "New identifier/symbol name.",
						},
					},
					"required": []string{"old_name", "new_name"},
				},
			},
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"old_name": map[string]any{
						"type":        "string",
						"description": "Current identifier/symbol name to replace.",
					},
					"new_name": map[string]any{
						"type":        "string",
						"description": "New identifier/symbol name.",
					},
				},
				"required": []string{"old_name", "new_name"},
			},
		},
		{
			"type":        "function",
			"name":        "git_archeology",
			"description": "GraphRAG & Git Archeology: inspect git blame and historical commit messages/diffs for a file line range or symbol to understand WHY code was written.",
			"function": map[string]any{
				"name":        "git_archeology",
				"description": "GraphRAG & Git Archeology: inspect git blame and historical commit messages/diffs for a file line range or symbol to understand WHY code was written.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path": map[string]any{
							"type":        "string",
							"description": "Path to file relative to workspace root.",
						},
						"query": map[string]any{
							"type":        "string",
							"description": "Optional symbol or commit message search query.",
						},
						"start_line": map[string]any{
							"type":        "integer",
							"description": "Optional start line number.",
						},
						"end_line": map[string]any{
							"type":        "integer",
							"description": "Optional end line number.",
						},
					},
				},
			},
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Path to file relative to workspace root.",
					},
					"query": map[string]any{
						"type":        "string",
						"description": "Optional symbol or commit message search query.",
					},
					"start_line": map[string]any{
						"type":        "integer",
						"description": "Optional start line number.",
					},
					"end_line": map[string]any{
						"type":        "integer",
						"description": "Optional end line number.",
					},
				},
			},
		},
		{
			"type":        "function",
			"name":        "debug_trace",
			"description": "Time-Travel / Post-Mortem Debugger: run a failing command or test and capture the full crash stack frames, 15-line source windows, and local variable state at the crash point.",
			"function": map[string]any{
				"name":        "debug_trace",
				"description": "Time-Travel / Post-Mortem Debugger: run a failing command or test and capture the full crash stack frames, 15-line source windows, and local variable state at the crash point.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"command": map[string]any{
							"type":        "string",
							"description": "Command line to execute and inspect for panics, tracebacks, or crashes.",
						},
					},
					"required": []string{"command"},
				},
			},
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"command": map[string]any{
						"type":        "string",
						"description": "Command line to execute and inspect for panics, tracebacks, or crashes.",
					},
				},
				"required": []string{"command"},
			},
		},
		{
			"type":        "function",
			"name":        "synthesize_tool",
			"description": "Self-Evolving Tool Synthesis: write, compile/validate, and dynamically register a new reusable tool (in python, powershell, node, or go) into ~/.unjess/custom_tools/ so you can call it immediately and in future sessions.",
			"function": map[string]any{
				"name":        "synthesize_tool",
				"description": "Self-Evolving Tool Synthesis: write, compile/validate, and dynamically register a new reusable tool (in python, powershell, node, or go) into ~/.unjess/custom_tools/ so you can call it immediately and in future sessions.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"name": map[string]any{
							"type":        "string",
							"description": "Tool identifier name (e.g. generate_proto, docker_health).",
						},
						"description": map[string]any{
							"type":        "string",
							"description": "Description explaining what the tool does and its parameters.",
						},
						"language": map[string]any{
							"type":        "string",
							"description": "Implementation language ('python', 'powershell', 'node', or 'go').",
						},
						"code": map[string]any{
							"type":        "string",
							"description": "Complete source code of the tool script.",
						},
						"parameters": map[string]any{
							"type":        "object",
							"description": "Optional JSON schema describing the tool parameters.",
						},
					},
					"required": []string{"name", "description", "language", "code"},
				},
			},
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name": map[string]any{
						"type":        "string",
						"description": "Tool identifier name (e.g. generate_proto, docker_health).",
					},
					"description": map[string]any{
						"type":        "string",
						"description": "Description explaining what the tool does and its parameters.",
					},
					"language": map[string]any{
						"type":        "string",
						"description": "Implementation language ('python', 'powershell', 'node', or 'go').",
					},
					"code": map[string]any{
						"type":        "string",
						"description": "Complete source code of the tool script.",
					},
					"parameters": map[string]any{
						"type":        "object",
						"description": "Optional JSON schema describing the tool parameters.",
					},
				},
				"required": []string{"name", "description", "language", "code"},
			},
		},
		{
			"type":        "function",
			"name":        "visual_qa_screenshot",
			"description": "Multimodal Visual QA & Image Inspection: render a local HTML file or live URL in headless Chrome/Edge and capture a screenshot, OR load a local image file (.jpg, .jpeg, .png, .webp, .gif) from disk, and attach it to your Vision input.",
			"function": map[string]any{
				"name":        "visual_qa_screenshot",
				"description": "Multimodal Visual QA & Image Inspection: render a local HTML file or live URL in headless Chrome/Edge and capture a screenshot, OR load a local image file (.jpg, .jpeg, .png, .webp, .gif) from disk, and attach it to your Vision input.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"target": map[string]any{
							"type":        "string",
							"description": "Local image file path (.jpg, .png, .webp, .gif), local HTML file path, or HTTP/HTTPS/file URL to render/inspect.",
						},
						"width": map[string]any{
							"type":        "integer",
							"description": "Browser viewport width in pixels (default: 1280).",
						},
						"height": map[string]any{
							"type":        "integer",
							"description": "Browser viewport height in pixels (default: 800).",
						},
					},
					"required": []string{"target"},
				},
			},
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"target": map[string]any{
						"type":        "string",
						"description": "Local image file path (.jpg, .png, .webp, .gif), local HTML file path, or HTTP/HTTPS/file URL to render/inspect.",
					},
					"width": map[string]any{
						"type":        "integer",
						"description": "Browser viewport width in pixels (default: 1280).",
					},
					"height": map[string]any{
						"type":        "integer",
						"description": "Browser viewport height in pixels (default: 800).",
					},
				},
				"required": []string{"target"},
			},
		},
		{
			"type":        "function",
			"name":        "sec_attack_surface",
			"description": "Shadow API & Route Discovery: scan the workspace for all HTTP routes, handlers, middleware, missing auth, CORS wildcards, and spoofable header bypasses.",
			"function": map[string]any{
				"name":        "sec_attack_surface",
				"description": "Shadow API & Route Discovery: scan the workspace for all HTTP routes, handlers, middleware, missing auth, CORS wildcards, and spoofable header bypasses.",
				"parameters": map[string]any{
					"type":       "object",
					"properties": map[string]any{},
				},
			},
			"parameters": map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
		{
			"type":        "function",
			"name":        "sec_taint_scan",
			"description": "AST & Data-Flow Taint Analysis: trace untrusted user inputs (req.body, query params, headers) to dangerous sinks (SQLi, Command Injection, Path Traversal, SSRF, XSS).",
			"function": map[string]any{
				"name":        "sec_taint_scan",
				"description": "AST & Data-Flow Taint Analysis: trace untrusted user inputs (req.body, query params, headers) to dangerous sinks (SQLi, Command Injection, Path Traversal, SSRF, XSS).",
				"parameters": map[string]any{
					"type":       "object",
					"properties": map[string]any{},
				},
			},
			"parameters": map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
		{
			"type":        "function",
			"name":        "sec_smart_fuzz",
			"description": "LLM-Guided Smart Fuzzer & Microsecond Concurrency Race Tester: fire synchronized microsecond request bursts and adversarial payloads against a target URL to detect race conditions (TOCTOU) and injection flaws.",
			"function": map[string]any{
				"name":        "sec_smart_fuzz",
				"description": "LLM-Guided Smart Fuzzer & Microsecond Concurrency Race Tester: fire synchronized microsecond request bursts and adversarial payloads against a target URL to detect race conditions (TOCTOU) and injection flaws.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"url": map[string]any{
							"type":        "string",
							"description": "Target HTTP/HTTPS URL endpoint to fuzz.",
						},
						"method": map[string]any{
							"type":        "string",
							"description": "HTTP method (e.g. GET, POST, PUT, DELETE). Default: GET.",
						},
						"concurrency": map[string]any{
							"type":        "integer",
							"description": "Number of concurrent synchronized requests (default: 25).",
						},
					},
					"required": []string{"url"},
				},
			},
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"url": map[string]any{
						"type":        "string",
						"description": "Target HTTP/HTTPS URL endpoint to fuzz.",
					},
					"method": map[string]any{
						"type":        "string",
						"description": "HTTP method (e.g. GET, POST, PUT, DELETE). Default: GET.",
					},
					"concurrency": map[string]any{
						"type":        "integer",
						"description": "Number of concurrent synchronized requests (default: 25).",
					},
				},
				"required": []string{"url"},
			},
		},
		{
			"type":        "function",
			"name":        "sec_supply_chain_audit",
			"description": "Supply-Chain & Malicious Dependency Hunter: scan package.json, build.rs, setup.py, go.mod, and scripts for obfuscated base64 payloads, suspicious outbound IPs, and env-var exfiltration.",
			"function": map[string]any{
				"name":        "sec_supply_chain_audit",
				"description": "Supply-Chain & Malicious Dependency Hunter: scan package.json, build.rs, setup.py, go.mod, and scripts for obfuscated base64 payloads, suspicious outbound IPs, and env-var exfiltration.",
				"parameters": map[string]any{
					"type":       "object",
					"properties": map[string]any{},
				},
			},
			"parameters": map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
		{
			"type":        "function",
			"name":        "run_security_duel",
			"description": "Execute 'The Duel' (Red Team vs. White Team Security Arena): run a full attack-surface, taint, supply-chain, Red Team PoC exploit & White Team patch/regression-test verification audit.",
			"function": map[string]any{
				"name":        "run_security_duel",
				"description": "Execute 'The Duel' (Red Team vs. White Team Security Arena): run a full attack-surface, taint, supply-chain, Red Team PoC exploit & White Team patch/regression-test verification audit.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"target_url": map[string]any{
							"type":        "string",
							"description": "Optional live target URL for active fuzzing and PoC exploit testing.",
						},
					},
				},
			},
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"target_url": map[string]any{
						"type":        "string",
						"description": "Optional live target URL for active fuzzing and PoC exploit testing.",
					},
				},
			},
		},
		{
			"type":        "function",
			"name":        "speculative_execute",
			"description": "Speculative Multi-Branch Execution: run 2 or more competing implementation strategies concurrently in isolated Shadow Git Worktrees, verify build/tests on each branch, automatically promote the winning branch to the workspace, and discard losing branches.",
			"function": map[string]any{
				"name":        "speculative_execute",
				"description": "Speculative Multi-Branch Execution: run 2 or more competing implementation strategies concurrently in isolated Shadow Git Worktrees, verify build/tests on each branch, automatically promote the winning branch to the workspace, and discard losing branches.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"strategies": map[string]any{
							"type":        "array",
							"description": "List of competing implementation strategies to run concurrently in isolated shadow worktrees.",
							"items": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"name": map[string]any{
										"type":        "string",
										"description": "Name of the strategy.",
									},
									"prompt": map[string]any{
										"type":        "string",
										"description": "Detailed prompt describing the implementation strategy for the shadow worker.",
									},
									"model": map[string]any{
										"type":        "string",
										"description": "Model ID to execute this strategy or 'inherit'.",
									},
								},
								"required": []string{"name", "prompt"},
							},
						},
					},
					"required": []string{"strategies"},
				},
			},
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"strategies": map[string]any{
						"type":        "array",
						"description": "List of competing implementation strategies to run concurrently in isolated shadow worktrees.",
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"name": map[string]any{
									"type":        "string",
									"description": "Name of the strategy.",
								},
								"prompt": map[string]any{
									"type":        "string",
									"description": "Detailed prompt describing the implementation strategy for the shadow worker.",
								},
								"model": map[string]any{
									"type":        "string",
									"description": "Model ID to execute this strategy or 'inherit'.",
								},
							},
							"required": []string{"name", "prompt"},
						},
					},
				},
				"required": []string{"strategies"},
			},
		},
		{
			"type":        "function",
			"name":        "browser_use",
			"description": "Interactive AI browser automation (Browser-Use / Set-of-Mark Vision). Navigate websites, inspect numbered interactive DOM elements ([1], [2], [3]...), click buttons/links by index, type into inputs/forms, scroll, execute JS, and automatically capture & attach annotated Vision screenshots of the live browser after every step.",
			"function": map[string]any{
				"name":        "browser_use",
				"description": "Interactive AI browser automation (Browser-Use / Set-of-Mark Vision). Navigate websites, inspect numbered interactive DOM elements ([1], [2], [3]...), click buttons/links by index, type into inputs/forms, scroll, execute JS, and automatically capture & attach annotated Vision screenshots of the live browser after every step.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"action": map[string]any{
							"type":        "string",
							"enum":        []string{"navigate", "click", "type", "scroll", "press_key", "evaluate", "extract", "screenshot", "close"},
							"description": "Browser action: navigate (load URL), click (click element by index or selector), type (fill text into element), scroll (scroll page), press_key (send keyboard key), evaluate (run JS), extract/screenshot (refresh DOM and capture annotated Set-of-Mark screenshot), close (terminate browser session).",
						},
						"url": map[string]any{
							"type":        "string",
							"description": "Target URL to open (supports https://..., http://localhost:..., or local file path) when action is 'navigate'.",
						},
						"index": map[string]any{
							"type":        "integer",
							"description": "1-based element number [1], [2], etc. from the Interactive Elements list and Set-of-Mark screenshot badges (for 'click' and 'type').",
						},
						"selector": map[string]any{
							"type":        "string",
							"description": "Optional CSS selector fallback (e.g. '#search-btn', 'button[type=submit]') if index is not used.",
						},
						"text": map[string]any{
							"type":        "string",
							"description": "Text to enter into the input/textarea when action is 'type', or CLI arguments for 'cli'.",
						},
						"press_enter": map[string]any{
							"type":        "boolean",
							"description": "Whether to press Enter/submit form after typing when action is 'type' (default: false).",
						},
						"direction": map[string]any{
							"type":        "string",
							"enum":        []string{"down", "up"},
							"description": "Scroll direction ('down' or 'up') when action is 'scroll' (default: 'down').",
						},
						"amount": map[string]any{
							"type":        "integer",
							"description": "Scroll distance in pixels when action is 'scroll' (default: 550).",
						},
						"key": map[string]any{
							"type":        "string",
							"description": "Key name to press when action is 'press_key' (e.g. 'Enter', 'Escape', 'Tab', 'ArrowDown').",
						},
						"script": map[string]any{
							"type":        "string",
							"description": "JavaScript code or expression to run when action is 'evaluate'.",
						},
						"headless": map[string]any{
							"type":        "boolean",
							"description": "Whether to run browser headless (default: true). Set false to display a visible Chrome/Edge browser window on screen.",
						},
					},
					"required": []string{"action"},
				},
			},
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"action": map[string]any{
						"type":        "string",
						"enum":        []string{"navigate", "click", "type", "scroll", "press_key", "evaluate", "extract", "screenshot", "close"},
						"description": "Browser action: navigate (load URL), click (click element by index or selector), type (fill text into element), scroll (scroll page), press_key (send keyboard key), evaluate (run JS), extract/screenshot (refresh DOM and capture annotated Set-of-Mark screenshot), close (terminate browser session).",
					},
					"url": map[string]any{
						"type":        "string",
						"description": "Target URL to open (supports https://..., http://localhost:..., or local file path) when action is 'navigate'.",
					},
					"index": map[string]any{
						"type":        "integer",
						"description": "1-based element number [1], [2], etc. from the Interactive Elements list and Set-of-Mark screenshot badges (for 'click' and 'type').",
					},
					"selector": map[string]any{
						"type":        "string",
						"description": "Optional CSS selector fallback (e.g. '#search-btn', 'button[type=submit]') if index is not used.",
					},
					"text": map[string]any{
						"type":        "string",
						"description": "Text to enter into the input/textarea when action is 'type', or CLI arguments for 'cli'.",
					},
					"press_enter": map[string]any{
						"type":        "boolean",
						"description": "Whether to press Enter/submit form after typing when action is 'type' (default: false).",
					},
					"direction": map[string]any{
						"type":        "string",
						"enum":        []string{"down", "up"},
						"description": "Scroll direction ('down' or 'up') when action is 'scroll' (default: 'down').",
					},
					"amount": map[string]any{
						"type":        "integer",
						"description": "Scroll distance in pixels when action is 'scroll' (default: 550).",
					},
					"key": map[string]any{
						"type":        "string",
						"description": "Key name to press when action is 'press_key' (e.g. 'Enter', 'Escape', 'Tab', 'ArrowDown').",
					},
					"script": map[string]any{
						"type":        "string",
						"description": "JavaScript code or expression to run when action is 'evaluate'.",
					},
					"headless": map[string]any{
						"type":        "boolean",
						"description": "Whether to run browser headless (default: true). Set false to display a visible Chrome/Edge browser window on screen.",
					},
				},
				"required": []string{"action"},
			},
		},
		{
			"type":        "function",
			"name":        "manage_unjess",
			"description": "Live Self-Customization of Unjess (Desktop & CLI) without recompiling: add/remove/list Custom AI API Providers (any OpenAI-compatible API endpoint with base_url, api_key, and models), add/remove MCP Servers, and create/install/delete Skills.",
			"function": map[string]any{
				"name":        "manage_unjess",
				"description": "Live Self-Customization of Unjess (Desktop & CLI) without recompiling: add/remove/list Custom AI API Providers (any OpenAI-compatible API endpoint with base_url, api_key, and models), add/remove MCP Servers, and create/install/delete Skills.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"action": map[string]any{
							"type":        "string",
							"enum":        []string{"add_provider", "remove_provider", "list_providers", "add_mcp", "remove_mcp", "save_skill", "install_skill_github", "delete_skill"},
							"description": "Self-customization action to execute.",
						},
						"id": map[string]any{
							"type":        "string",
							"description": "Provider slug ID (e.g. 'deepseek', 'together', 'fireworks', 'perplexity', 'sambanova') for add_provider / remove_provider.",
						},
						"name": map[string]any{
							"type":        "string",
							"description": "Display name for provider, MCP server name, or Skill name.",
						},
						"base_url": map[string]any{
							"type":        "string",
							"description": "OpenAI-compatible API base URL (e.g. 'https://api.deepseek.com/v1', 'https://api.together.xyz/v1') for add_provider.",
						},
						"api_key": map[string]any{
							"type":        "string",
							"description": "Optional API key for add_provider (persisted to ~/.unjess/custom_providers.json and ~/.unjess/keys.yaml).",
						},
						"models": map[string]any{
							"type":        "array",
							"items":       map[string]any{"type": "string"},
							"description": "List of model IDs served by the custom provider (e.g. ['deepseek-chat', 'deepseek-reasoner']).",
						},
						"command": map[string]any{
							"type":        "string",
							"description": "Executable command (e.g. 'npx', 'uvx', 'python', 'node') for add_mcp.",
						},
						"args": map[string]any{
							"type":        "array",
							"items":       map[string]any{"type": "string"},
							"description": "Command arguments array for add_mcp.",
						},
						"url": map[string]any{
							"type":        "string",
							"description": "Remote MCP HTTP/SSE URL (for add_mcp) or GitHub repository URL (for install_skill_github).",
						},
						"env": map[string]any{
							"type":        "object",
							"description": "Optional environment variables map for add_mcp.",
						},
						"description": map[string]any{
							"type":        "string",
							"description": "Description for add_mcp or save_skill.",
						},
						"triggers": map[string]any{
							"type":        "array",
							"items":       map[string]any{"type": "string"},
							"description": "Trigger keywords array for save_skill.",
						},
						"instructions": map[string]any{
							"type":        "string",
							"description": "Full Markdown instructions body for save_skill.",
						},
						"scope": map[string]any{
							"type":        "string",
							"enum":        []string{"global", "workspace"},
							"description": "Skill scope ('global' saves to ~/.unjess/skills, 'workspace' saves to .agents/skills). Default: 'global'.",
						},
					},
					"required": []string{"action"},
				},
			},
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"action": map[string]any{
						"type":        "string",
						"enum":        []string{"add_provider", "remove_provider", "list_providers", "add_mcp", "remove_mcp", "save_skill", "install_skill_github", "delete_skill"},
						"description": "Self-customization action to execute.",
					},
					"id": map[string]any{
						"type":        "string",
						"description": "Provider slug ID (e.g. 'deepseek', 'together', 'fireworks', 'perplexity', 'sambanova') for add_provider / remove_provider.",
					},
					"name": map[string]any{
						"type":        "string",
						"description": "Display name for provider, MCP server name, or Skill name.",
					},
					"base_url": map[string]any{
						"type":        "string",
						"description": "OpenAI-compatible API base URL (e.g. 'https://api.deepseek.com/v1', 'https://api.together.xyz/v1') for add_provider.",
					},
					"api_key": map[string]any{
						"type":        "string",
						"description": "Optional API key for add_provider (persisted to ~/.unjess/custom_providers.json and ~/.unjess/keys.yaml).",
					},
					"models": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "List of model IDs served by the custom provider (e.g. ['deepseek-chat', 'deepseek-reasoner']).",
					},
					"command": map[string]any{
						"type":        "string",
						"description": "Executable command (e.g. 'npx', 'uvx', 'python', 'node') for add_mcp.",
					},
					"args": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "Command arguments array for add_mcp.",
					},
					"url": map[string]any{
						"type":        "string",
						"description": "Remote MCP HTTP/SSE URL (for add_mcp) or GitHub repository URL (for install_skill_github).",
					},
					"env": map[string]any{
						"type":        "object",
						"description": "Optional environment variables map for add_mcp.",
					},
					"description": map[string]any{
						"type":        "string",
						"description": "Description for add_mcp or save_skill.",
					},
					"triggers": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "Trigger keywords array for save_skill.",
					},
					"instructions": map[string]any{
						"type":        "string",
						"description": "Full Markdown instructions body for save_skill.",
					},
					"scope": map[string]any{
						"type":        "string",
						"enum":        []string{"global", "workspace"},
						"description": "Skill scope ('global' saves to ~/.unjess/skills, 'workspace' saves to .agents/skills). Default: 'global'.",
					},
				},
				"required": []string{"action"},
			},
		},
		{
			"type":        "function",
			"name":        "todo_write",
			"description": "Create or update a structured task checklist for complex multi-step tasks (3+ steps). Skip for single straightforward tasks. Keep only ONE item in_progress at a time and mark items completed immediately when verified.",
			"function": map[string]any{
				"name":        "todo_write",
				"description": "Create or update a structured task checklist for complex multi-step tasks (3+ steps). Skip for single straightforward tasks. Keep only ONE item in_progress at a time and mark items completed immediately when verified.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"todos": map[string]any{
							"type":        "array",
							"description": "Updated list of todo checklist items.",
							"items": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"id": map[string]any{
										"type":        "string",
										"description": "Short identifier for the task item (e.g. '1', '2').",
									},
									"content": map[string]any{
										"type":        "string",
										"description": "Concise description of the step.",
									},
									"status": map[string]any{
										"type":        "string",
										"enum":        []string{"pending", "in_progress", "completed"},
										"description": "Current status of the task item.",
									},
								},
								"required": []string{"content", "status"},
							},
						},
					},
					"required": []string{"todos"},
				},
			},
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"todos": map[string]any{
						"type":        "array",
						"description": "Updated list of todo checklist items.",
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"id": map[string]any{
									"type":        "string",
									"description": "Short identifier for the task item (e.g. '1', '2').",
								},
								"content": map[string]any{
									"type":        "string",
									"description": "Concise description of the step.",
								},
								"status": map[string]any{
									"type":        "string",
									"enum":        []string{"pending", "in_progress", "completed"},
									"description": "Current status of the task item.",
								},
							},
							"required": []string{"content", "status"},
						},
					},
				},
				"required": []string{"todos"},
			},
		},
		{
			"type":        "function",
			"name":        "tool_search",
			"description": "Search for and unlock specialized deferred tools (e.g. AST symbol editing, security auditing/fuzzing, browser automation, web search, memory, custom tools) by keyword or name.",
			"function": map[string]any{
				"name":        "tool_search",
				"description": "Search for and unlock specialized deferred tools (e.g. AST symbol editing, security auditing/fuzzing, browser automation, web search, memory, custom tools) by keyword or name.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"query": map[string]any{
							"type":        "string",
							"description": "Keyword or tool name to search for (or 'all' to list all available specialized tools).",
						},
					},
					"required": []string{"query"},
				},
			},
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{
						"type":        "string",
						"description": "Keyword or tool name to search for (or 'all' to list all available specialized tools).",
					},
				},
				"required": []string{"query"},
			},
		},
	}

	customs := frontier.CustomToolDefinitions()
	if len(customs) > 0 {
		defs = append(defs, customs...)
	}
	return defs
}

// coreListedToolNames defines the lean default "listed" tool surface (Claude Code enginePlacement="listed" parity).
// Keeping the active tool surface compact (~12 core tools) prevents schema token bloat and tool-selection confusion on Flash models.
var coreListedToolNames = map[string]bool{
	"read_file":       true,
	"write_file":      true,
	"edit_file":       true,
	"list_dir":        true,
	"grep_search":     true,
	"find_by_name":    true,
	"run_command":     true,
	"git_status":      true,
	"git_diff":        true,
	"todo_write":      true,
	"invoke_subagent": true,
	"tool_search":     true,
}

// ListedToolDefinitions returns the lean active tool definitions for a turn, dynamically including deferred tools
// only when relevant to turnMode, prompt keywords, attached images, or explicitly unlocked via tool_search.
func ListedToolDefinitions(turnMode string, prompt string, hasImages bool, unlocked map[string]bool) []map[string]any {
	allDefs := CoreToolDefinitions()
	lowerPrompt := strings.ToLower(prompt)
	modeLower := strings.ToLower(strings.TrimSpace(turnMode))

	allow := make(map[string]bool, len(coreListedToolNames)+8)
	for k, v := range coreListedToolNames {
		allow[k] = v
	}
	for k, v := range unlocked {
		if v {
			allow[k] = true
		}
	}

	if modeLower == "plan" {
		allow["propose_plan"] = true
		allow["ask_question"] = true
	}
	if modeLower == "duel" || strings.Contains(lowerPrompt, "security") || strings.Contains(lowerPrompt, "vulnerab") ||
		strings.Contains(lowerPrompt, "pentest") || strings.Contains(lowerPrompt, "exploit") || strings.Contains(lowerPrompt, "fuzz") ||
		strings.Contains(lowerPrompt, "taint") || strings.Contains(lowerPrompt, "owasp") {
		allow["sec_attack_surface"] = true
		allow["sec_taint_scan"] = true
		allow["sec_smart_fuzz"] = true
		allow["sec_supply_chain_audit"] = true
		allow["run_security_duel"] = true
	}
	if hasImages || strings.Contains(lowerPrompt, ".png") || strings.Contains(lowerPrompt, ".jpg") ||
		strings.Contains(lowerPrompt, ".jpeg") || strings.Contains(lowerPrompt, ".webp") ||
		strings.Contains(lowerPrompt, "screenshot") || strings.Contains(lowerPrompt, "image") ||
		strings.Contains(lowerPrompt, "visual") {
		allow["view_image"] = true
		allow["visual_qa_screenshot"] = true
	}
	if strings.Contains(lowerPrompt, "http://") || strings.Contains(lowerPrompt, "https://") ||
		strings.Contains(lowerPrompt, "browser") || strings.Contains(lowerPrompt, "web search") ||
		strings.Contains(lowerPrompt, "search the web") || strings.Contains(lowerPrompt, "website") {
		allow["web_search"] = true
		allow["read_url"] = true
		allow["browser_use"] = true
	}
	if strings.Contains(lowerPrompt, "background") || strings.Contains(lowerPrompt, "daemon") || strings.Contains(lowerPrompt, "async task") {
		allow["start_task"] = true
		allow["poll_task"] = true
		allow["cancel_task"] = true
	}
	if strings.Contains(lowerPrompt, "memory") || strings.Contains(lowerPrompt, "remember") || strings.Contains(lowerPrompt, "recall") {
		allow["memory_save"] = true
		allow["memory_recall"] = true
		allow["memory_delete"] = true
	}
	if strings.Contains(lowerPrompt, "mcp") || strings.Contains(lowerPrompt, "provider") ||
		strings.Contains(lowerPrompt, "skill") || strings.Contains(lowerPrompt, "unjess") ||
		strings.Contains(lowerPrompt, "synthesize") || strings.Contains(lowerPrompt, "custom tool") {
		allow["manage_unjess"] = true
		allow["mcp_status"] = true
		allow["synthesize_tool"] = true
	}
	if strings.Contains(lowerPrompt, "ast_") || strings.Contains(lowerPrompt, "callers") || strings.Contains(lowerPrompt, "speculative") {
		allow["ast_outline"] = true
		allow["ast_find_callers"] = true
		allow["ast_edit_symbol"] = true
		allow["ast_rename_symbol"] = true
		allow["speculative_execute"] = true
	}

	listed := make([]map[string]any, 0, len(allow))
	for _, d := range allDefs {
		name, _ := d["name"].(string)
		if allow[name] || isCustomToolName(name) {
			listed = append(listed, d)
		}
	}
	return listed
}

// toolAliasMap maps common tool names from DeepSeek/OpenAI/Claude training corpora to Unjess canonical tool names.
var toolAliasMap = map[string]string{
	"bash":                        "run_command",
	"shell":                       "run_command",
	"sh":                          "run_command",
	"powershell":                  "run_command",
	"terminal":                    "run_command",
	"exec":                        "run_command",
	"execute":                     "run_command",
	"execute_command":             "run_command",
	"cmd":                         "run_command",
	"run_bash":                    "run_command",
	"read":                        "read_file",
	"file_read":                   "read_file",
	"view":                        "read_file",
	"cat":                         "read_file",
	"open_file":                   "read_file",
	"write":                       "write_file",
	"file_write":                  "write_file",
	"create_file":                 "write_file",
	"save_file":                   "write_file",
	"edit":                        "edit_file",
	"file_edit":                   "edit_file",
	"str_replace":                 "edit_file",
	"str_replace_based_edit_tool": "edit_file",
	"replace_in_file":             "edit_file",
	"patch_file":                  "edit_file",
	"multiedit":                   "edit_file",
	"multi_edit":                  "edit_file",
	"grep":                        "grep_search",
	"search":                      "grep_search",
	"ripgrep":                     "grep_search",
	"rg":                          "grep_search",
	"code_search":                 "grep_search",
	"glob":                        "find_by_name",
	"find":                        "find_by_name",
	"find_files":                  "find_by_name",
	"file_search":                 "find_by_name",
	"ls":                          "list_dir",
	"dir":                         "list_dir",
	"list_files":                  "list_dir",
	"list_directory":              "list_dir",
	"todowrite":                   "todo_write",
	"todo_read":                   "todo_write",
	"todos":                       "todo_write",
	"task_list":                   "todo_write",
	"task":                        "invoke_subagent",
	"agent":                       "invoke_subagent",
	"subagent":                    "invoke_subagent",
	"spawn_subagent":              "invoke_subagent",
	"toolsearch":                  "tool_search",
}

var (
	doubleEscapedUnicodeRe = regexp.MustCompile(`\\u([0-9a-fA-F]{4})`)
	windowsPathGuardRe     = regexp.MustCompile(`(?:^|[^A-Za-z])[A-Za-z]:[\\/]|(?:^|[\s"'=])\\\\[^\s\\/]+[\\/]`)
)

// repairDoubleEscapedUnicode converts accidental "\\uXXXX" sequences emitted by flash models into actual Unicode runes,
// while skipping Windows drive/UNC paths and ASCII control characters (matching Claude Code 2.1.314 qYe / tengu_repair_double_escaped_unicode).
func repairDoubleEscapedUnicode(s string) string {
	if !strings.Contains(s, `\u`) {
		return s
	}
	if windowsPathGuardRe.MatchString(s) {
		return s
	}
	return doubleEscapedUnicodeRe.ReplaceAllStringFunc(s, func(match string) string {
		hexStr := match[len(match)-4:]
		if code, err := strconv.ParseInt(hexStr, 16, 32); err == nil {
			if (code >= 0xD800 && code <= 0xDFFF) || code <= 31 || (code >= 127 && code <= 159) {
				return match
			}
			return string(rune(code))
		}
		return match
	})
}

// RepairAndParseMalformedJSON attempts to repair common LLM JSON syntax errors (code fences, trailing commas, unescaped newlines, truncated closing braces).
func RepairAndParseMalformedJSON(raw string) (map[string]any, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return map[string]any{}, nil
	}
	// Strip markdown fences if present
	if strings.HasPrefix(s, "```") {
		if nl := strings.Index(s, "\n"); nl != -1 {
			s = strings.TrimSpace(s[nl+1:])
		}
		if strings.HasSuffix(s, "```") {
			s = strings.TrimSpace(s[:len(s)-3])
		}
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(s), &out); err == nil && out != nil {
		return out, nil
	}

	// Remove trailing commas before } or ]
	trailingCommaRe := regexp.MustCompile(`,\s*([}\]])`)
	repaired := trailingCommaRe.ReplaceAllString(s, "$1")
	if err := json.Unmarshal([]byte(repaired), &out); err == nil && out != nil {
		return out, nil
	}

	// Extract first {...} object if surrounded by extra text
	firstBrace := strings.Index(repaired, "{")
	lastBrace := strings.LastIndex(repaired, "}")
	if firstBrace != -1 && lastBrace > firstBrace {
		sub := repaired[firstBrace : lastBrace+1]
		if err := json.Unmarshal([]byte(sub), &out); err == nil && out != nil {
			return out, nil
		}
	}

	// If truncated at the end, try closing quotes/braces
	if firstBrace != -1 && lastBrace < firstBrace {
		candidate := repaired[firstBrace:] + `"}`
		if err := json.Unmarshal([]byte(candidate), &out); err == nil && out != nil {
			return out, nil
		}
	}

	return nil, fmt.Errorf("malformed JSON arguments: %s", truncatePreview(raw, 240))
}

func truncatePreview(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

// NormalizeToolCall normalizes tool name aliases, repairs malformed _raw JSON arguments, and coerces parameter aliases.
func NormalizeToolCall(name string, args map[string]any) (string, map[string]any, error) {
	cleanName := strings.TrimSpace(name)
	lowerName := strings.ToLower(cleanName)
	lowerName = strings.ReplaceAll(lowerName, "-", "_")

	if args == nil {
		args = make(map[string]any)
	} else {
		cloned := make(map[string]any, len(args))
		for k, v := range args {
			if strVal, ok := v.(string); ok {
				cloned[k] = repairDoubleEscapedUnicode(strVal)
			} else {
				cloned[k] = v
			}
		}
		args = cloned
	}

	// Check for unparsed _raw JSON arguments
	if rawVal, hasRaw := args["_raw"]; hasRaw {
		rawStr := fmt.Sprint(rawVal)
		repairedMap, err := RepairAndParseMalformedJSON(rawStr)
		if err != nil {
			return cleanName, args, err
		}
		delete(args, "_raw")
		for k, v := range repairedMap {
			if strVal, ok := v.(string); ok {
				args[k] = repairDoubleEscapedUnicode(strVal)
			} else {
				args[k] = v
			}
		}
	}

	// Handle Anthropic/OpenHands str_replace_editor command multiplexer
	if lowerName == "str_replace_editor" || lowerName == "str_replace_based_edit_tool" {
		cmd := strings.ToLower(strings.TrimSpace(getString(args, "command")))
		switch cmd {
		case "view":
			cleanName = "read_file"
			if vr, ok := args["view_range"].([]any); ok && len(vr) >= 2 {
				args["start_line"] = vr[0]
				args["end_line"] = vr[1]
			}
		case "create":
			cleanName = "write_file"
			if ft := getString(args, "file_text"); ft != "" && getString(args, "content") == "" {
				args["content"] = ft
			}
		default:
			cleanName = "edit_file"
		}
	} else if mapped, exists := toolAliasMap[lowerName]; exists {
		cleanName = mapped
	}

	// Normalize common parameter aliases across tools
	if cleanName == "read_file" || cleanName == "write_file" || cleanName == "edit_file" || cleanName == "list_dir" {
		if getString(args, "path") == "" {
			if v := getString(args, "file_path", "filepath", "filename", "file", "target_file"); v != "" {
				args["path"] = v
			}
		}
	}
	if cleanName == "edit_file" {
		if getString(args, "old_string") == "" {
			if v := getString(args, "old_str", "oldText", "old_text", "search", "find"); v != "" {
				args["old_string"] = v
			}
		}
		if _, hasNew := args["new_string"]; !hasNew {
			for _, k := range []string{"new_str", "newText", "new_text", "replace"} {
				if v, ok := args[k]; ok {
					args["new_string"] = v
					break
				}
			}
		}
		if _, hasRepAll := args["replace_all"]; !hasRepAll {
			if rn, ok := args["replace_name"]; ok {
				args["replace_all"] = rn == true || fmt.Sprint(rn) == "true"
			}
		}
	} else if cleanName == "read_file" {
		// Support Claude Code Read(offset, limit, length) coercion (Zjr @ mod 330 L138506)
		if _, hasStart := args["start_line"]; !hasStart {
			if off, ok := args["offset"]; ok {
				if arr, isArr := off.([]any); isArr && len(arr) == 1 {
					off = arr[0]
				}
				if offInt, err := strconv.Atoi(strings.TrimSpace(fmt.Sprint(off))); err == nil && offInt > 0 {
					args["start_line"] = offInt
					if _, hasEnd := args["end_line"]; !hasEnd {
						limVal, hasLim := args["limit"]
						if !hasLim {
							limVal, hasLim = args["length"]
						}
						if hasLim {
							if arr, isArr := limVal.([]any); isArr && len(arr) == 1 {
								limVal = arr[0]
							}
							if limInt, err2 := strconv.Atoi(strings.TrimSpace(fmt.Sprint(limVal))); err2 == nil && limInt > 0 {
								args["end_line"] = offInt + limInt - 1
							}
						}
					}
				}
			}
		}
	} else if cleanName == "write_file" {
		if getString(args, "content") == "" {
			if v := getString(args, "file_text", "text", "body", "contents", "new_text"); v != "" {
				args["content"] = v
			}
		}
	} else if cleanName == "run_command" {
		if getString(args, "command") == "" {
			if v := getString(args, "cmd", "script", "bash_command", "shell_command", "command_line"); v != "" {
				args["command"] = v
			}
		}
		if _, hasTo := args["timeout_seconds"]; !hasTo {
			if tms, ok := args["timeout_ms"]; ok {
				if msInt, err := strconv.Atoi(strings.TrimSpace(fmt.Sprint(tms))); err == nil && msInt > 0 {
					args["timeout_seconds"] = (msInt + 999) / 1000
				}
			} else if toVal, ok := args["timeout"]; ok {
				if toInt, err := strconv.Atoi(strings.TrimSpace(fmt.Sprint(toVal))); err == nil && toInt > 0 {
					if toInt > 1800 {
						args["timeout_seconds"] = (toInt + 999) / 1000
					} else {
						args["timeout_seconds"] = toInt
					}
				}
			}
		}
	} else if cleanName == "invoke_subagent" {
		if _, hasSubs := args["subagents"]; !hasSubs {
			prompt := getString(args, "prompt", "description", "task")
			if prompt != "" {
				role := getString(args, "role", "subagent_type", "name")
				if role == "" {
					role = "Specialist Subagent"
				}
				args["subagents"] = []map[string]any{
					{
						"role":      role,
						"prompt":    prompt,
						"type_name": getString(args, "type_name", "subagent_type"),
						"model":     "inherit",
					},
				}
			}
		}
	}

	// Coerce stringified booleans and integers
	for _, bk := range []string{"replace_all", "replaceAll", "include_hidden", "case_sensitive", "ask_user", "headless", "keep_open"} {
		if sv, ok := args[bk].(string); ok {
			switch strings.ToLower(strings.TrimSpace(sv)) {
			case "true", "1", "yes":
				args[bk] = true
			case "false", "0", "no":
				args[bk] = false
			}
		}
	}
	for _, ik := range []string{"start_line", "end_line", "StartLine", "EndLine", "offset", "limit", "timeout", "port"} {
		if sv, ok := args[ik].(string); ok {
			if iv, err := strconv.Atoi(strings.TrimSpace(sv)); err == nil {
				args[ik] = iv
			}
		}
	}

	return cleanName, args, nil
}

var (
	xmlToolCallRe    = regexp.MustCompile(`(?s)<tool_call>\s*(\{.*?\})\s*</tool_call>`)
	fencedToolJSONRe = regexp.MustCompile("(?s)```(?:json|tool_call)?\\s*(\\{\\s*\"(?:name|tool|function)\"\\s*:.*?\\})\\s*```")
)

// ExtractedToolCall represents a tool call recovered from assistant text output.
type ExtractedToolCall struct {
	Name      string
	Arguments map[string]any
}

// ExtractToolCallsFromText recovers structured tool calls when a flash/local model emits them inside XML tags or JSON blocks instead of native tool_calls.
func ExtractToolCallsFromText(text string) []ExtractedToolCall {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return nil
	}
	var calls []ExtractedToolCall
	var rawBlocks []string

	for _, m := range xmlToolCallRe.FindAllStringSubmatch(trimmed, -1) {
		if len(m) > 1 {
			rawBlocks = append(rawBlocks, m[1])
		}
	}
	if len(rawBlocks) == 0 {
		for _, m := range fencedToolJSONRe.FindAllStringSubmatch(trimmed, -1) {
			if len(m) > 1 {
				rawBlocks = append(rawBlocks, m[1])
			}
		}
	}

	for _, block := range rawBlocks {
		parsed, err := RepairAndParseMalformedJSON(block)
		if err != nil || parsed == nil {
			continue
		}
		tName := getString(parsed, "name", "tool", "function")
		if tName == "" {
			continue
		}
		var tArgs map[string]any
		if aMap, ok := parsed["arguments"].(map[string]any); ok {
			tArgs = aMap
		} else if aMap, ok := parsed["parameters"].(map[string]any); ok {
			tArgs = aMap
		} else if aStr, ok := parsed["arguments"].(string); ok {
			tArgs, _ = RepairAndParseMalformedJSON(aStr)
		}
		normName, normArgs, normErr := NormalizeToolCall(tName, tArgs)
		if normErr == nil && normName != "" {
			calls = append(calls, ExtractedToolCall{
				Name:      normName,
				Arguments: normArgs,
			})
		}
	}
	return calls
}

// SubagentToolDefinitions returns JSON Schema definitions for tools available to subagents (excluding invoke_subagent, ask_question, propose_plan, speculative_execute).
func SubagentToolDefinitions() []map[string]any {
	defs := CoreToolDefinitions()
	subDefs := make([]map[string]any, 0, len(defs))
	for _, d := range defs {
		name, _ := d["name"].(string)
		if name != "invoke_subagent" && name != "ask_question" && name != "propose_plan" && name != "speculative_execute" {
			subDefs = append(subDefs, d)
		}
	}
	return subDefs
}

// ReadOnlySubagentToolDefinitions returns JSON Schema definitions for tools available to read-only research subagents
// (filtering out write/execution tools and synthesized tools).
func ReadOnlySubagentToolDefinitions() []map[string]any {
	defs := CoreToolDefinitions()
	subDefs := make([]map[string]any, 0, len(defs))
	for _, d := range defs {
		name, _ := d["name"].(string)
		if name != "invoke_subagent" && name != "ask_question" && name != "propose_plan" &&
			name != "write_file" && name != "edit_file" && name != "run_command" &&
			name != "ast_edit_symbol" && name != "ast_rename_symbol" && name != "synthesize_tool" &&
			name != "debug_trace" && name != "sec_smart_fuzz" && name != "run_security_duel" &&
			name != "speculative_execute" && name != "browser_use" && name != "manage_unjess" && !isCustomToolName(name) {
			subDefs = append(subDefs, d)
		}
	}
	return subDefs
}

func isCustomToolName(name string) bool {
	for _, ct := range frontier.CustomToolDefinitions() {
		if n, _ := ct["name"].(string); n == name {
			return true
		}
	}
	return false
}

// ExecuteWithoutRecording executes any of the core tools safely inside WorkspacePath without recording a ToolCallRecord.
func (t *ToolExecutor) ExecuteWithoutRecording(ctx context.Context, name string, args map[string]any) (string, bool) {
	normName, normArgs, normErr := NormalizeToolCall(name, args)
	if normErr != nil {
		return fmt.Sprintf("Error [malformed_tool_use]: %v. Please re-invoke tool '%s' with valid JSON object arguments.", normErr, name), false
	}
	name = normName
	args = normArgs

	if t.ReadOnly && (name == "write_file" || name == "edit_file" || name == "run_command" ||
		name == "start_task" || name == "cancel_task" ||
		name == "ast_edit_symbol" || name == "ast_rename_symbol" || name == "synthesize_tool" ||
		name == "debug_trace" || name == "sec_smart_fuzz" || name == "run_security_duel" ||
		name == "speculative_execute" || name == "invoke_subagent" || name == "manage_unjess" || isCustomToolName(name)) {
		return "Error: Tool '" + name + "' is disabled in read-only research subagent mode.", false
	}

	switch name {
	case "read_file":
		return t.executeReadFile(args)
	case "view_image":
		return t.executeViewImage(ctx, args)
	case "write_file":
		return t.executeWriteFile(ctx, args)
	case "edit_file":
		return t.executeEditFile(ctx, args)
	case "list_dir":
		return t.executeListDir(args)
	case "grep_search":
		return t.executeGrepSearch(ctx, args)
	case "find_by_name":
		return t.executeFindByName(args)
	case "run_command":
		return t.executeRunCommand(ctx, args)
	case "start_task":
		return t.executeStartTask(ctx, args)
	case "poll_task":
		return t.executePollTask(args)
	case "cancel_task":
		return t.executeCancelTask(args)
	case "git_status":
		return t.executeGitStatus()
	case "git_diff":
		return t.executeGitDiff(args)
	case "todo_write":
		return t.executeTodoWrite(args)
	case "tool_search":
		return t.executeToolSearch(args)
	case "memory_save":
		return t.executeMemorySave(args)
	case "memory_recall":
		return t.executeMemoryRecall(args)
	case "memory_delete":
		return t.executeMemoryDelete(args)
	case "web_search":
		return t.executeWebSearch(ctx, args)
	case "read_url":
		return t.executeReadURL(ctx, args)
	case "mcp_status":
		return t.executeMCPStatus(args)
	case "ask_question":
		return "Error: ask_question requires an interactive session with the user via HarnessManager.", false
	case "propose_plan":
		return "Error: propose_plan requires an interactive session with the user via HarnessManager.", false
	case "invoke_subagent":
		return "Error: invoke_subagent must be executed via HarnessManager.", false
	case "speculative_execute":
		return "Error: speculative_execute must be executed via HarnessManager/RunAgentTurn.", false
	case "ast_outline":
		return t.executeASTOutline(args)
	case "ast_find_callers":
		return t.executeASTFindCallers(args)
	case "ast_edit_symbol":
		return t.executeASTEditSymbol(ctx, args)
	case "ast_rename_symbol":
		return t.executeASTRenameSymbol(args)
	case "git_archeology":
		return t.executeGitArcheology(ctx, args)
	case "debug_trace":
		return t.executeDebugTrace(ctx, args)
	case "synthesize_tool":
		return t.executeSynthesizeTool(args)
	case "visual_qa_screenshot":
		return t.executeVisualQA(ctx, args)
	case "sec_attack_surface":
		return t.executeSecAttackSurface()
	case "sec_taint_scan":
		return t.executeSecTaintScan()
	case "sec_smart_fuzz":
		return t.executeSecSmartFuzz(ctx, args)
	case "sec_supply_chain_audit":
		return t.executeSecSupplyChainAudit()
	case "run_security_duel":
		return t.executeRunSecurityDuel(ctx, args)
	case "browser_use":
		return t.executeBrowserUse(ctx, args)
	case "manage_unjess":
		return t.executeManageUnjess(args)
	default:
		out, found, err := frontier.ExecuteCustomTool(ctx, t.WorkspacePath, name, args)
		if found {
			if err != nil {
				return fmt.Sprintf("Error executing custom tool '%s': %v\nOutput: %s", name, err, out), false
			}
			return out, true
		}
		return fmt.Sprintf("Error: unrecognized tool '%s'. Use tool_search(query='all') to list available tools.", name), false
	}
}

func (t *ToolExecutor) executeTodoWrite(args map[string]any) (string, bool) {
	rawList, ok := args["todos"].([]any)
	if !ok {
		if typedList, okTyped := args["todos"].([]map[string]any); okTyped {
			for _, m := range typedList {
				rawList = append(rawList, m)
			}
		} else {
			return "Error: 'todos' array is required.", false
		}
	}

	var items []TodoItem
	for idx, raw := range rawList {
		if m, ok := raw.(map[string]any); ok {
			id := strings.TrimSpace(getString(m, "id"))
			if id == "" {
				id = strconv.Itoa(idx + 1)
			}
			content := strings.TrimSpace(getString(m, "content", "task", "title", "text"))
			status := strings.ToLower(strings.TrimSpace(getString(m, "status", "state")))
			if status != "completed" && status != "in_progress" && status != "pending" {
				status = "pending"
			}
			if content != "" {
				items = append(items, TodoItem{
					ID:      id,
					Content: content,
					Status:  status,
				})
			}
		}
	}

	t.mu.Lock()
	t.Todos = items
	t.mu.Unlock()

	completedCount := 0
	for _, it := range items {
		if it.Status == "completed" {
			completedCount++
		}
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Todos updated (%d/%d completed):\n", completedCount, len(items)))
	for _, it := range items {
		marker := "[ ]"
		if it.Status == "completed" {
			marker = "[x]"
		} else if it.Status == "in_progress" {
			marker = "[~]"
		}
		sb.WriteString(fmt.Sprintf("  %s #%s: %s (%s)\n", marker, it.ID, it.Content, it.Status))
	}
	return strings.TrimRight(sb.String(), "\n"), true
}

func (t *ToolExecutor) executeToolSearch(args map[string]any) (string, bool) {
	query := strings.ToLower(strings.TrimSpace(getString(args, "query", "name", "keyword")))
	if query == "" {
		query = "all"
	}

	tokens := strings.FieldsFunc(query, func(r rune) bool {
		return r == ' ' || r == ',' || r == ';' || r == '/'
	})

	allDefs := CoreToolDefinitions()
	var matched []map[string]any
	for _, d := range allDefs {
		name, _ := d["name"].(string)
		desc, _ := d["description"].(string)
		lowerName := strings.ToLower(name)
		lowerDesc := strings.ToLower(desc)

		isMatch := false
		if query == "all" || query == "*" || strings.Contains(lowerName, query) || strings.Contains(lowerDesc, query) {
			isMatch = true
		} else if len(tokens) > 0 {
			for _, tok := range tokens {
				if len(tok) >= 2 && (strings.Contains(lowerName, tok) || strings.Contains(lowerDesc, tok)) {
					isMatch = true
					break
				}
			}
		}

		if isMatch {
			matched = append(matched, d)
			t.mu.Lock()
			if t.UnlockedDeferredTools == nil {
				t.UnlockedDeferredTools = make(map[string]bool)
			}
			t.UnlockedDeferredTools[name] = true
			t.mu.Unlock()
		}
	}

	if len(matched) == 0 {
		return fmt.Sprintf("No tools matched query '%s'. Try tool_search with query='all' to see all tools.", query), true
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Unlocked and matched %d tool(s) for '%s':\n", len(matched), query))
	for _, d := range matched {
		name, _ := d["name"].(string)
		desc, _ := d["description"].(string)
		sb.WriteString(fmt.Sprintf("- **%s**: %s\n", name, desc))
	}
	return strings.TrimRight(sb.String(), "\n"), true
}

// Execute executes any of the core tools safely inside WorkspacePath and records the tool call.
func (t *ToolExecutor) Execute(ctx context.Context, name string, args map[string]any) (string, bool) {
	idx := t.BeginToolCall(name, args)
	result, success := t.ExecuteWithoutRecording(ctx, name, args)
	t.CompleteToolCall(idx, result, success)
	return result, success
}

func expandHomeDir(p string) string {
	trimmed := strings.TrimSpace(p)
	if trimmed == "~" || strings.HasPrefix(trimmed, "~/") || strings.HasPrefix(trimmed, `~\`) {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			if trimmed == "~" {
				return home
			}
			return filepath.Join(home, trimmed[2:])
		}
	}
	return trimmed
}

func resolveReadOrInspectPath(wsPath, targetPath string) (string, error) {
	expanded := expandHomeDir(targetPath)
	cleanExpanded := filepath.Clean(expanded)

	// Whitelist 1: Check if path targets the user's global ~/.unjess/skills or ~/.unjess/logs
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		globalSkills := filepath.Clean(filepath.Join(home, ".unjess", "skills"))
		globalLogs := filepath.Clean(filepath.Join(home, ".unjess", "logs"))
		normTarget := strings.ToLower(cleanExpanded)
		if normTarget == strings.ToLower(globalSkills) || strings.HasPrefix(normTarget, strings.ToLower(globalSkills)+string(filepath.Separator)) {
			return cleanExpanded, nil
		}
		if normTarget == strings.ToLower(globalLogs) || strings.HasPrefix(normTarget, strings.ToLower(globalLogs)+string(filepath.Separator)) {
			return cleanExpanded, nil
		}
	}

	// Whitelist 2: Check relative references to skills or logs (.unjess/skills, skills, .skills, .unjess/logs)
	relTrimmed := strings.TrimLeft(targetPath, `/\`)
	relSlash := filepath.ToSlash(relTrimmed)
	if strings.HasPrefix(relSlash, ".unjess/skills") || strings.HasPrefix(relSlash, "skills") || strings.HasPrefix(relSlash, ".skills") || strings.HasPrefix(relSlash, ".unjess/logs") {
		// First check active workspace
		wsCand := filepath.Join(wsPath, filepath.Clean(relTrimmed))
		if _, err := os.Stat(wsCand); err == nil {
			return wsCand, nil
		}
		// If not in workspace, check user global ~/.unjess/skills or ~/.unjess/logs
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			if strings.HasPrefix(relSlash, ".unjess/skills") || strings.HasPrefix(relSlash, "skills") || strings.HasPrefix(relSlash, ".skills") {
				sub := strings.TrimPrefix(relSlash, ".unjess/skills")
				sub = strings.TrimPrefix(sub, ".skills")
				sub = strings.TrimPrefix(sub, "skills")
				sub = strings.TrimLeft(sub, "/")
				globalCand := filepath.Join(home, ".unjess", "skills", filepath.FromSlash(sub))
				if _, err := os.Stat(globalCand); err == nil {
					return globalCand, nil
				}
			} else if strings.HasPrefix(relSlash, ".unjess/logs") {
				sub := strings.TrimPrefix(relSlash, ".unjess/logs")
				sub = strings.TrimLeft(sub, "/")
				globalCand := filepath.Join(home, ".unjess", "logs", filepath.FromSlash(sub))
				if _, err := os.Stat(globalCand); err == nil {
					return globalCand, nil
				}
			}
		}
	}

	safePath, err := workspace.ResolveSafePath(wsPath, expanded)
	if err == nil {
		return safePath, nil
	}
	if filepath.IsAbs(expanded) && !strings.Contains(expanded, "..") {
		if sbErr := checkCommandSandbox(expanded, wsPath); sbErr == nil && !workspace.IsInternalUnjessDir(expanded) {
			return filepath.Clean(expanded), nil
		}
	}
	return "", err
}

func resolveWritePath(wsPath, targetPath string) (string, error) {
	expanded := expandHomeDir(targetPath)
	cleanExpanded := filepath.Clean(expanded)

	// Allow writes to ~/.unjess/skills and ~/.unjess/logs
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		globalSkills := filepath.Clean(filepath.Join(home, ".unjess", "skills"))
		globalLogs := filepath.Clean(filepath.Join(home, ".unjess", "logs"))
		normTarget := strings.ToLower(cleanExpanded)
		if strings.HasPrefix(normTarget, strings.ToLower(globalSkills)+string(filepath.Separator)) {
			return cleanExpanded, nil
		}
		if strings.HasPrefix(normTarget, strings.ToLower(globalLogs)+string(filepath.Separator)) {
			return cleanExpanded, nil
		}
	}

	safePath, err := workspace.ResolveSafePath(wsPath, expanded)
	if err == nil {
		return safePath, nil
	}
	if filepath.IsAbs(expanded) && !strings.Contains(expanded, "..") {
		if sbErr := checkCommandSandbox(expanded, wsPath); sbErr == nil && !workspace.IsInternalUnjessDir(expanded) {
			return filepath.Clean(expanded), nil
		}
	}
	return "", err
}

func (t *ToolExecutor) executeReadFile(args map[string]any) (string, bool) {
	relPath := getString(args, "path", "file_path", "target_file", "file")
	if strings.TrimSpace(relPath) == "" {
		return "Error: path is required", false
	}

	safePath, err := resolveReadOrInspectPath(t.WorkspacePath, relPath)
	if err != nil {
		return fmt.Sprintf("Error: %v", err), false
	}

	info, err := os.Stat(safePath)
	if err != nil {
		if os.IsNotExist(err) {
			t.MarkFileAbsent(safePath)
			t.MarkFileAbsent(relPath)
		}
		return fmt.Sprintf("Error: file not found: %s", relPath), false
	}
	if info.IsDir() {
		return t.executeListDir(map[string]any{"path": relPath})
	}

	// If the requested file is an image (.png, .jpg, .jpeg, .webp, .gif, .bmp), load & attach it to Vision input instead of reading binary bytes as text!
	if frontier.IsSupportedImageExtension(safePath) {
		dataURI, mimeType, imgW, imgH, origSz, optSz, loadErr := frontier.LoadAndOptimizeImageForVision(safePath, 1568)
		if loadErr != nil {
			return fmt.Sprintf("Error loading image %s: %v", relPath, loadErr), false
		}
		t.QueueVisionImage(filepath.Base(safePath), dataURI)
		dimInfo := ""
		if imgW > 0 && imgH > 0 {
			dimInfo = fmt.Sprintf(", %dx%d px", imgW, imgH)
		}
		return fmt.Sprintf("Image file loaded and attached to your Vision input (%s%s, %d KB -> %d KB optimized): %s — you can now see and inspect this image directly.",
			mimeType, dimInfo, origSz/1024, optSz/1024, filepath.ToSlash(relPath)), true
	}

	startLine := getInt(args, "start_line", "startLine", "StartLine", "offset", "from_line", "from")
	endLine := getInt(args, "end_line", "endLine", "EndLine", "limit", "to_line", "to")

	// If file is large (>1MB), stream line by line with bufio.Reader to avoid large allocations
	if info.Size() > 1024*1024 {
		file, err := os.Open(safePath)
		if err != nil {
			return fmt.Sprintf("Error reading file: %v", err), false
		}
		defer file.Close()

		reader := bufio.NewReader(file)
		lineNum := 0
		var sb strings.Builder

		s := 0
		if startLine > 0 {
			s = startLine - 1
		}
		e := -1
		if endLine > 0 {
			e = endLine
		}
		const maxLines = 1000
		if e <= 0 || (e-s) > maxLines {
			e = s + maxLines
		}

		for {
			lineStr, err := reader.ReadString('\n')
			if len(lineStr) > 0 {
				lineNum++
				if lineNum >= (s+1) && lineNum <= e {
					cleanLine := strings.TrimRight(lineStr, "\r\n")
					sb.WriteString(fmt.Sprintf("%d: %s\n", lineNum, cleanLine))
				}
			}
			if err != nil {
				break
			}
		}
		total := lineNum
		if s >= total && total > 0 {
			return fmt.Sprintf("Error: start_line %d exceeds file length (%d lines)", startLine, total), false
		}
		if e > total {
			e = total
		}
		t.MarkFileRead(safePath)
		t.MarkFileRead(relPath)
		header := fmt.Sprintf("File: %s (%d lines total, showing %d-%d)\n", filepath.ToSlash(relPath), total, s+1, e)
		return header + strings.TrimRight(sb.String(), "\n"), true
	}

	data, err := os.ReadFile(safePath)
	if err != nil {
		return fmt.Sprintf("Error reading file: %v", err), false
	}

	t.MarkFileRead(safePath)
	t.MarkFileRead(relPath)

	content := string(data)
	lines := splitLines(content)
	total := len(lines)

	s := 0
	if startLine > 0 {
		s = startLine - 1
	}
	e := total
	if endLine > 0 && endLine <= total {
		e = endLine
	}

	if s >= total && total > 0 {
		return fmt.Sprintf("Error: start_line %d exceeds file length (%d lines)", startLine, total), false
	}

	const maxLines = 1000
	if (e - s) > maxLines {
		e = s + maxLines
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("File: %s (%d lines total, showing %d-%d)\n", filepath.ToSlash(relPath), total, s+1, e))

	for i := s; i < e; i++ {
		sb.WriteString(fmt.Sprintf("%d: %s\n", i+1, lines[i]))
	}

	return strings.TrimRight(sb.String(), "\n"), true
}

// writeFileWithRetry writes data to a file, retrying up to 3 times on Windows in case of transient file locks.
func writeFileWithRetry(path string, data []byte, perm os.FileMode) error {
	var err error
	for attempt := 0; attempt < 4; attempt++ {
		err = os.WriteFile(path, data, perm)
		if err == nil {
			return nil
		}
		if runtime.GOOS == "windows" && attempt < 3 {
			time.Sleep(120 * time.Millisecond)
			continue
		}
		break
	}
	return err
}

func (t *ToolExecutor) executeWriteFile(ctx context.Context, args map[string]any) (string, bool) {
	relPath := getString(args, "path", "file_path", "target_file", "file")
	if strings.TrimSpace(relPath) == "" {
		return "Error: path is required", false
	}
	content := getString(args, "content", "code")

	safePath, err := resolveWritePath(t.WorkspacePath, relPath)
	if err != nil {
		return fmt.Sprintf("Error: %v", err), false
	}

	rel, err := filepath.Rel(t.WorkspacePath, safePath)
	if err != nil || strings.HasPrefix(rel, "..") {
		rel = safePath
	}
	relSlash := filepath.ToSlash(rel)

	if t.IsProtected(relPath) || t.IsProtected(safePath) || t.IsProtected(relSlash) {
		return fmt.Sprintf("PermissionDenied: '%s' is locked by task specification. Modifying or overwriting this file is strictly forbidden.", relSlash), false
	}

	var oldContent string
	existed := false
	action := "created"
	if existingBytes, err := os.ReadFile(safePath); err == nil {
		if t.EnforceReadBeforeEdit {
			if freshErr := t.CheckFileFreshness(safePath, relSlash, existingBytes); freshErr != nil {
				return freshErr.Error(), false
			}
		}
		oldContent = string(existingBytes)
		existed = true
		action = "modified"
	}

	// Record snapshot before modifying file
	t.mu.Lock()
	t.recordSnapshotLocked(relSlash, existed, oldContent)
	t.mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(safePath), 0755); err != nil {
		return fmt.Sprintf("Error creating directories: %v", err), false
	}

	if err := writeFileWithRetry(safePath, []byte(content), 0644); err != nil {
		return fmt.Sprintf("Error writing file: %v", err), false
	}

	diffStr, adds, dels := ComputeUnifiedDiff(relSlash, oldContent, content)

	t.mu.Lock()
	t.touchFile(relSlash, action, false)
	t.recordDiff(relSlash, action, diffStr, adds, dels)
	t.mu.Unlock()
	t.MarkFileRead(safePath)
	t.MarkFileRead(relSlash)

	linesCount := len(splitLines(content))
	res := fmt.Sprintf("Successfully wrote %d lines to %s", linesCount, relSlash)
	lintFeedback := codeintel.RunPostEditCheck(ctx, t.WorkspacePath, relPath)
	if lintFeedback != "" {
		res += "\n" + lintFeedback
	}
	return res, true
}

type editHunkSpec struct {
	oldStr     string
	newStr     string
	replaceAll bool
}

func extractEditHunks(args map[string]any) ([]editHunkSpec, error) {
	if rawEdits, hasEdits := args["edits"]; hasEdits && rawEdits != nil {
		var items []map[string]any
		switch typed := rawEdits.(type) {
		case []any:
			for _, item := range typed {
				if m, ok := item.(map[string]any); ok {
					items = append(items, m)
				}
			}
		case []map[string]any:
			items = append(items, typed...)
		}
		if len(items) > 0 {
			hunks := make([]editHunkSpec, 0, len(items))
			for idx, m := range items {
				oldS := getString(m, "old_string", "old_str", "oldText", "old_text", "target", "old_content", "search", "find")
				newS := getString(m, "new_string", "new_str", "newText", "new_text", "replacement", "new_content", "replace")
				if oldS == "" {
					return nil, fmt.Errorf("Error: edits[%d].old_string is required", idx)
				}
				repAll := getBool(m, "replace_all", "replaceAll")
				hunks = append(hunks, editHunkSpec{
					oldStr:     oldS,
					newStr:     newS,
					replaceAll: repAll,
				})
			}
			return hunks, nil
		}
	}
	oldStr := getString(args, "old_string", "target", "old_content")
	newStr := getString(args, "new_string", "replacement", "new_content")
	if oldStr == "" {
		return nil, fmt.Errorf("Error: old_string is required")
	}
	replaceAll := getBool(args, "replace_all", "replaceAll")
	return []editHunkSpec{
		{
			oldStr:     oldStr,
			newStr:     newStr,
			replaceAll: replaceAll,
		},
	}, nil
}

func (t *ToolExecutor) executeEditFile(ctx context.Context, args map[string]any) (string, bool) {
	relPath := getString(args, "path", "file_path", "target_file", "file")
	if strings.TrimSpace(relPath) == "" {
		return "Error: path is required", false
	}
	hunks, hunkErr := extractEditHunks(args)
	if hunkErr != nil {
		return hunkErr.Error(), false
	}

	safePath, err := resolveWritePath(t.WorkspacePath, relPath)
	if err != nil {
		return fmt.Sprintf("Error: %v", err), false
	}

	rel, err := filepath.Rel(t.WorkspacePath, safePath)
	if err != nil || strings.HasPrefix(rel, "..") {
		rel = safePath
	}
	relSlash := filepath.ToSlash(rel)

	if t.IsProtected(relPath) || t.IsProtected(safePath) || t.IsProtected(relSlash) {
		return fmt.Sprintf("PermissionDenied: '%s' is locked by task specification. Modifying or overwriting this file is strictly forbidden.", relSlash), false
	}

	if t.EnforceReadBeforeEdit && !t.HasReadFile(safePath) && !t.HasReadFile(relSlash) && !t.HasReadFile(relPath) {
		return fmt.Sprintf("Error: File '%s' has not been read yet in this session. Read it first with read_file before editing it so you have exact line contents and indentation.", relSlash), false
	}

	data, err := os.ReadFile(safePath)
	if err != nil {
		return fmt.Sprintf("Error reading file: %v", err), false
	}
	if t.EnforceReadBeforeEdit {
		if freshErr := t.CheckFileFreshness(safePath, relSlash, data); freshErr != nil {
			return freshErr.Error(), false
		}
	}
	oldContent := string(data)

	// In-memory preflight simulation across all hunks (atomic rollback if any hunk fails)
	simContent := oldContent
	for idx, h := range hunks {
		nextContent, applyErr := applySmartReplacementWithOptions(simContent, h.oldStr, h.newStr, relSlash, h.replaceAll)
		if applyErr != nil {
			if len(hunks) > 1 {
				return fmt.Sprintf("Error in edit hunk %d/%d (preflight aborted, 0 bytes modified on disk): %v", idx+1, len(hunks), applyErr), false
			}
			return applyErr.Error(), false
		}
		simContent = nextContent
	}
	newContent := simContent

	// Record snapshot before modifying file
	t.mu.Lock()
	t.recordSnapshotLocked(relSlash, true, oldContent)
	t.mu.Unlock()

	if err := writeFileWithRetry(safePath, []byte(newContent), 0644); err != nil {
		return fmt.Sprintf("Error writing modified file: %v", err), false
	}

	diffStr, adds, dels := ComputeUnifiedDiff(relSlash, oldContent, newContent)

	t.mu.Lock()
	t.touchFile(relSlash, "modified", false)
	t.recordDiff(relSlash, "modified", diffStr, adds, dels)
	t.mu.Unlock()
	t.MarkFileRead(safePath)
	t.MarkFileRead(relSlash)

	res := fmt.Sprintf("Successfully edited %s\n\n%s", relSlash, diffStr)
	lintFeedback := codeintel.RunPostEditCheck(ctx, t.WorkspacePath, relPath)
	if lintFeedback != "" {
		res += "\n" + lintFeedback
	}
	return res, true
}

func (t *ToolExecutor) executeListDir(args map[string]any) (string, bool) {
	p := getString(args, "path")
	if strings.TrimSpace(p) == "" {
		p = "."
	}

	safePath, err := resolveReadOrInspectPath(t.WorkspacePath, p)
	if err != nil {
		return fmt.Sprintf("Error: %v", err), false
	}

	entries, err := os.ReadDir(safePath)
	if err != nil {
		return fmt.Sprintf("Error reading directory: %v", err), false
	}

	var dirs []os.DirEntry
	var files []os.DirEntry

	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") && name != ".gitignore" && name != ".env.example" {
			continue
		}
		if entry.IsDir() {
			if isIgnoredDirName(name) {
				continue
			}
			dirs = append(dirs, entry)
		} else {
			files = append(files, entry)
		}
	}

	sort.Slice(dirs, func(i, j int) bool {
		return strings.ToLower(dirs[i].Name()) < strings.ToLower(dirs[j].Name())
	})
	sort.Slice(files, func(i, j int) bool {
		return strings.ToLower(files[i].Name()) < strings.ToLower(files[j].Name())
	})

	var sb strings.Builder
	totalItems := len(dirs) + len(files)
	cleanRel := filepath.ToSlash(p)
	if cleanRel == "" {
		cleanRel = "."
	}
	sb.WriteString(fmt.Sprintf("Directory: %s (%d items)\n", cleanRel, totalItems))

	for _, d := range dirs {
		sb.WriteString(fmt.Sprintf("  [DIR] %s/\n", d.Name()))
	}
	for _, f := range files {
		var sz int64
		if info, err := f.Info(); err == nil {
			sz = info.Size()
		}
		sb.WriteString(fmt.Sprintf("  [FILE] %s (%s)\n", f.Name(), formatSize(sz)))
	}

	if totalItems == 0 {
		sb.WriteString("  (empty directory)\n")
	}

	return strings.TrimRight(sb.String(), "\n"), true
}

func (t *ToolExecutor) executeGrepSearch(ctx context.Context, args map[string]any) (string, bool) {
	pattern := getString(args, "pattern", "query")
	if strings.TrimSpace(pattern) == "" {
		return "Error: pattern is required", false
	}

	targetRel := getString(args, "path")
	if strings.TrimSpace(targetRel) == "" {
		targetRel = "."
	}

	safePath, err := resolveReadOrInspectPath(t.WorkspacePath, targetRel)
	if err != nil {
		return fmt.Sprintf("Error: %v", err), false
	}

	// Try ripgrep if available
	if rgPath, err := exec.LookPath("rg"); err == nil {
		cmd := exec.CommandContext(ctx, rgPath, "--line-number", "--no-heading", "--color=never", "--max-count=50", pattern, safePath)
		cmd.Dir = t.WorkspacePath
		setHideWindow(cmd)
		out, _ := cmd.CombinedOutput()
		res := strings.TrimSpace(string(out))
		if res != "" {
			lines := strings.Split(res, "\n")
			return fmt.Sprintf("Found %d match(es) for '%s':\n\n%s", len(lines), pattern, res), true
		}
	}

	// Pure Go fallback grep search
	var re *regexp.Regexp
	re, err = regexp.Compile("(?i)" + regexp.QuoteMeta(pattern))
	if err != nil {
		return fmt.Sprintf("Error compiling search pattern: %v", err), false
	}

	var results []string
	const maxMatches = 50

	_ = filepath.WalkDir(safePath, func(curPath string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil || len(results) >= maxMatches {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if isIgnoredDirName(name) || (strings.HasPrefix(name, ".") && name != ".github") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(name, ".") && name != ".gitignore" {
			return nil
		}
		if isBinaryOrHeavyFile(name) {
			return nil
		}

		info, err := d.Info()
		if err != nil || info.Size() > 2*1024*1024 { // Skip files > 2MB
			return nil
		}

		data, err := os.ReadFile(curPath)
		if err != nil {
			return nil
		}

		rel, _ := filepath.Rel(t.WorkspacePath, curPath)
		relSlash := filepath.ToSlash(rel)
		lines := splitLines(string(data))

		for idx, line := range lines {
			if re.MatchString(line) {
				results = append(results, fmt.Sprintf("%s:%d: %s", relSlash, idx+1, strings.TrimRight(line, "\r")))
				if len(results) >= maxMatches {
					break
				}
			}
		}

		return nil
	})

	if len(results) == 0 {
		return fmt.Sprintf("No matches found for: %s", pattern), true
	}

	header := fmt.Sprintf("Found %d match(es) for '%s'", len(results), pattern)
	if len(results) >= maxMatches {
		header += fmt.Sprintf(" (capped at %d)", maxMatches)
	}
	return header + ":\n\n" + strings.Join(results, "\n"), true
}

func (t *ToolExecutor) executeFindByName(args map[string]any) (string, bool) {
	pattern := getString(args, "pattern")
	if strings.TrimSpace(pattern) == "" {
		return "Error: pattern is required", false
	}

	searchRoot := t.WorkspacePath
	if pathArg := getString(args, "path", "dir", "directory"); strings.TrimSpace(pathArg) != "" {
		resolved, err := resolveReadOrInspectPath(t.WorkspacePath, pathArg)
		if err != nil {
			return fmt.Sprintf("Error: %v", err), false
		}
		searchRoot = resolved
	}

	patLower := strings.ToLower(pattern)
	var matches []string
	const maxMatches = 100

	_ = filepath.WalkDir(searchRoot, func(curPath string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil || len(matches) >= maxMatches {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if isIgnoredDirName(name) || (strings.HasPrefix(name, ".") && name != ".github") {
				return filepath.SkipDir
			}
			return nil
		}

		matched, err := filepath.Match(patLower, strings.ToLower(name))
		if err == nil && matched {
			rel, err := filepath.Rel(searchRoot, curPath)
			if err == nil {
				matches = append(matches, filepath.ToSlash(rel))
			}
		}
		return nil
	})

	if len(matches) == 0 {
		return fmt.Sprintf("No files matching '%s' found.", pattern), true
	}

	sort.Strings(matches)
	return fmt.Sprintf("Found %d file(s):\n%s", len(matches), strings.Join(matches, "\n")), true
}

func checkCommandSandbox(command, workspaceRoot string) error {
	cmdLower := strings.ToLower(command)

	// Block direct references to sensitive Unjess harness files
	for _, sensitive := range []string{".session_secret", "keys.yaml", "harness_state.json", "unjess.log"} {
		if strings.Contains(cmdLower, sensitive) {
			return fmt.Errorf("access denied: shell command attempts to access protected Unjess secret/state file (%s) outside workspace sandbox", sensitive)
		}
	}

	// Block .unjess/mcp.json and .. traversal to mcp.json
	if strings.Contains(cmdLower, "mcp.json") {
		if strings.Contains(cmdLower, ".unjess/mcp.json") || strings.Contains(cmdLower, `.unjess\mcp.json`) || strings.Contains(cmdLower, "..") {
			return fmt.Errorf("access denied: shell command attempts to access protected Unjess mcp.json configuration outside workspace sandbox (%s)", workspaceRoot)
		}
		wsMcp := filepath.Join(workspaceRoot, "mcp.json")
		if _, err := os.Stat(wsMcp); os.IsNotExist(err) {
			return fmt.Errorf("access denied: shell command attempts to access protected Unjess mcp.json configuration outside workspace sandbox (%s)", workspaceRoot)
		}
	}

	// Block config.yaml if escaping into ~/.unjess or via directory traversal
	if strings.Contains(cmdLower, "config.yaml") {
		if strings.Contains(cmdLower, ".unjess/config.yaml") || strings.Contains(cmdLower, `.unjess\config.yaml`) || strings.Contains(cmdLower, "..") {
			return fmt.Errorf("access denied: shell command attempts to access protected Unjess harness configuration file (config.yaml)")
		}
	}

	// If cmdLower contains .unjess, allow references that explicitly point into .unjess/workspaces or .unjess/skills
	if strings.Contains(cmdLower, ".unjess") {
		if strings.Contains(cmdLower, "..") {
			return fmt.Errorf("access denied: shell command attempts to access protected Unjess harness directory (~/.unjess) outside the workspace sandbox (%s)", workspaceRoot)
		}

		idx := 0
		for {
			pos := strings.Index(cmdLower[idx:], ".unjess")
			if pos == -1 {
				break
			}
			actualPos := idx + pos
			rem := cmdLower[actualPos+len(".unjess"):]
			remSlash := strings.TrimLeft(rem, `/\`)
			allowedSubdir := strings.HasPrefix(remSlash, "workspaces") || strings.HasPrefix(remSlash, "skills") || strings.HasPrefix(remSlash, "logs")
			if !allowedSubdir {
				return fmt.Errorf("access denied: shell command attempts to access protected Unjess harness directory (~/.unjess) outside the workspace sandbox (%s)", workspaceRoot)
			}
			if strings.HasPrefix(remSlash, "skills") {
				if strings.Contains(cmdLower, "rm ") || strings.Contains(cmdLower, "remove-item") || strings.Contains(cmdLower, "rmdir") {
					return fmt.Errorf("access denied: destructive shell command against ~/.unjess/skills is prohibited")
				}
			}
			idx = actualPos + len(".unjess")
		}
	}

	return nil
}

// SyntaxErrorHint tracks a deterministic syntax or flag error pattern and its learned correction.
type SyntaxErrorHint struct {
	Pattern   string `json:"pattern"`
	ErrorText string `json:"error_text"`
	Hint      string `json:"hint"`
	Timestamp int64  `json:"timestamp"`
}

var syntaxHintsMu sync.Mutex

func getErrorHintsFilePath() string {
	if override := strings.TrimSpace(os.Getenv("UNJESS_ERROR_HINTS_PATH")); override != "" {
		return override
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".unjess", "memory", "error_hints.json")
}

func analyzeSyntaxErrorAndLearn(command, output string) string {
	var pattern, hintText string

	if strings.Contains(output, "The token '&&' is not a valid statement separator") || (strings.Contains(output, "ParserError") && strings.Contains(command, "&&")) {
		pattern = "&& statement separator"
		hintText = "Windows PowerShell does not support '&&' as a statement separator in older versions. Use ';' or 'if ($?) { ... }' to sequence commands."
	} else if strings.Contains(output, "curl : Cannot bind parameter") || (strings.Contains(output, "ParameterBindingException") && strings.Contains(command, "curl")) {
		pattern = "curl parameter binding"
		hintText = "In PowerShell, 'curl' is an alias for 'Invoke-WebRequest'. To invoke real cURL, run 'curl.exe' directly or use 'Invoke-RestMethod'."
	} else if strings.Contains(output, "MissingEndCurlyBrace") || strings.Contains(output, "Missing closing '}'") {
		pattern = "missing end curly brace"
		hintText = "PowerShell script block or expression is missing a closing curly brace '}'. Ensure all '{' braces are properly closed."
	} else if strings.Contains(output, "UnexpectedToken") {
		pattern = "unexpected token"
		hintText = "PowerShell encountered an unexpected token. Verify command syntax, quoting, and parameter flags (e.g. quote paths with spaces)."
	} else if strings.Contains(output, "ParameterBindingException") {
		pattern = "parameter binding exception"
		hintText = "PowerShell failed to bind parameter. Check parameter names and flags (e.g. use '-' instead of Unix '--' if calling PowerShell cmdlets, or invoke executable directly with .exe)."
	} else if strings.Contains(output, "ParserError") {
		pattern = "parser error"
		hintText = "PowerShell syntax parser error. Ensure paths with spaces are quoted and commands are separated by ';' or newlines."
	}

	if hintText == "" {
		return ""
	}

	go recordSyntaxErrorHint(pattern, output, hintText)
	return fmt.Sprintf("[SYNTAX AUTO-HINT] %s", hintText)
}

func recordSyntaxErrorHint(pattern, errorOutput, hintText string) {
	syntaxHintsMu.Lock()
	defer syntaxHintsMu.Unlock()

	filePath := getErrorHintsFilePath()
	var hints []SyntaxErrorHint

	if data, err := os.ReadFile(filePath); err == nil && len(data) > 0 {
		_ = json.Unmarshal(data, &hints)
	}

	sample := errorOutput
	if len(sample) > 200 {
		sample = sample[:200]
	}

	found := false
	for i, h := range hints {
		if strings.EqualFold(h.Pattern, pattern) {
			hints[i].Timestamp = time.Now().Unix()
			hints[i].ErrorText = sample
			hints[i].Hint = hintText
			found = true
			break
		}
	}
	if !found {
		hints = append(hints, SyntaxErrorHint{
			Pattern:   pattern,
			ErrorText: sample,
			Hint:      hintText,
			Timestamp: time.Now().Unix(),
		})
	}
	if len(hints) > 50 {
		hints = hints[len(hints)-50:]
	}

	_ = os.MkdirAll(filepath.Dir(filePath), 0755)
	if data, err := json.MarshalIndent(hints, "", "  "); err == nil {
		_ = os.WriteFile(filePath, data, 0644)
	}
}

// findGitBash looks for a usable Git Bash binary on Windows.
func findGitBash() string {
	if p, err := exec.LookPath("bash.exe"); err == nil {
		return p
	}
	if p, err := exec.LookPath("bash"); err == nil {
		return p
	}
	candidates := []string{
		`C:\Program Files\Git\bin\bash.exe`,
		`C:\Program Files\Git\usr\bin\bash.exe`,
		`C:\Program Files (x86)\Git\bin\bash.exe`,
		`C:\Program Files (x86)\Git\usr\bin\bash.exe`,
	}
	if localAppData := os.Getenv("LOCALAPPDATA"); localAppData != "" {
		candidates = append(candidates, filepath.Join(localAppData, "Programs", "Git", "bin", "bash.exe"))
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return ""
}

// encodePowerShell encodes a PowerShell scriptblock as Base64 UTF-16LE for -EncodedCommand.
func encodePowerShell(script string) string {
	utf16Runes := utf16.Encode([]rune(script))
	buf := make([]byte, len(utf16Runes)*2)
	for i, r := range utf16Runes {
		buf[i*2] = byte(r)
		buf[i*2+1] = byte(r >> 8)
	}
	return base64.StdEncoding.EncodeToString(buf)
}

// wrapPosixCommandWithStateSentinel wraps a POSIX sh/bash command with an EXIT trap
// that emits the post-command working directory and exported environment while preserving exit status.
func wrapPosixCommandWithStateSentinel(subCmd string) string {
	trapPrefix := `__unjess_exit() { __ec=$?; printf '\n__UNJESS_STATE_PWD__:%s:__END__\n__UNJESS_STATE_ENV_BEGIN__\n' "$(pwd -P 2>/dev/null || pwd)"; export -p 2>/dev/null || true; printf '\n__UNJESS_STATE_ENV_END__\n'; exit $__ec; }; trap __unjess_exit EXIT; `
	return trapPrefix + "\n" + subCmd
}

// buildWindowsCommand returns an exec.Cmd configured for Windows execution.
// It auto-detects Git Bash for POSIX shell scripts, and uses PowerShell with
// -EncodedCommand to prevent Windows CLI quote stripping/mangling.
func buildWindowsCommand(ctx context.Context, command string) *exec.Cmd {
	cmdStr := strings.TrimSpace(command)
	if strings.EqualFold(cmdStr, "cd") || strings.EqualFold(cmdStr, "cd.") || strings.EqualFold(cmdStr, "cd .") {
		cmdStr = "Get-Location"
	}

	lower := strings.ToLower(cmdStr)
	gitBash := findGitBash()

	// If Git Bash is available and the command explicitly invokes bash/sh or is a typical unix command
	if gitBash != "" {
		if strings.HasPrefix(lower, "bash ") || strings.HasPrefix(lower, "sh ") ||
			strings.HasPrefix(lower, "bash -c") || strings.HasPrefix(lower, "sh -c") {
			subCmd := cmdStr
			if strings.HasPrefix(lower, "bash -c ") {
				subCmd = strings.TrimPrefix(cmdStr[8:], " ")
			} else if strings.HasPrefix(lower, "sh -c ") {
				subCmd = strings.TrimPrefix(cmdStr[6:], " ")
			} else if strings.HasPrefix(lower, "bash ") {
				subCmd = strings.TrimPrefix(cmdStr[5:], " ")
			} else if strings.HasPrefix(lower, "sh ") {
				subCmd = strings.TrimPrefix(cmdStr[3:], " ")
			}
			if (strings.HasPrefix(subCmd, "\"") && strings.HasSuffix(subCmd, "\"")) ||
				(strings.HasPrefix(subCmd, "'") && strings.HasSuffix(subCmd, "'")) {
				if len(subCmd) >= 2 {
					subCmd = subCmd[1 : len(subCmd)-1]
				}
			}
			return exec.CommandContext(ctx, gitBash, "-c", wrapPosixCommandWithStateSentinel(subCmd))
		}
	}

	// PowerShell execution: Use -EncodedCommand so that internal quotes (single, double, escaped)
	// arrive 100% intact without PowerShell 5.1 / Windows CommandLineToArgvW stripping them.
	// Also snapshot initial env and emit post-command PWD + env delta sentinel while preserving exit code.
	utf8Prefix := "$ProgressPreference = 'SilentlyContinue'; [Console]::InputEncoding = [Console]::OutputEncoding = $OutputEncoding = [System.Text.UTF8Encoding]::new($false); $PSDefaultParameterValues['*:Encoding'] = 'utf8'; " +
		"$__u_env0 = @{}; foreach ($__e in (Get-ChildItem env:)) { $__u_env0[$__e.Name] = $__e.Value }; $global:LASTEXITCODE = $null;\n"
	stateSuffix := "\n$__u_ok = $?; $__u_ec = $LASTEXITCODE; " +
		"[Console]::Out.WriteLine(\"\"); " +
		"[Console]::Out.WriteLine(\"__UNJESS_STATE_PWD__:\" + (Get-Location).Path + \":__END__\"); " +
		"[Console]::Out.WriteLine(\"__UNJESS_STATE_ENV_BEGIN__\"); " +
		"foreach ($__e in (Get-ChildItem env:)) { if (-not $__u_env0.ContainsKey($__e.Name) -or $__u_env0[$__e.Name] -ne $__e.Value) { [Console]::Out.WriteLine($__e.Name + \"=\" + $__e.Value) } }; " +
		"foreach ($__k in $__u_env0.Keys) { if (-not (Test-Path (\"env:\" + $__k))) { [Console]::Out.WriteLine(\"__UNJESS_UNSET__=\" + $__k) } }; " +
		"[Console]::Out.WriteLine(\"__UNJESS_STATE_ENV_END__\"); " +
		"if ($null -ne $__u_ec -and $__u_ec -ne 0) { exit $__u_ec } elseif ($null -eq $__u_ec -and -not $__u_ok) { exit 1 }"
	fullScript := utf8Prefix + cmdStr + stateSuffix
	encoded := encodePowerShell(fullScript)

	psExe := "powershell.exe"
	if p, err := exec.LookPath("pwsh.exe"); err == nil {
		psExe = p
	} else if p, err := exec.LookPath("pwsh"); err == nil {
		psExe = p
	}

	return exec.CommandContext(ctx, psExe, "-NoProfile", "-NonInteractive", "-EncodedCommand", encoded)
}

var ignoredShellEnvVars = map[string]bool{
	"_":                           true,
	"PWD":                         true,
	"OLDPWD":                      true,
	"SHLVL":                       true,
	"RANDOM":                      true,
	"SECONDS":                     true,
	"LINENO":                      true,
	"EPOCHSECONDS":                true,
	"EPOCHREALTIME":               true,
	"BASH_EXECUTION_STRING":       true,
	"PSExecutionPolicyPreference": true,
	"LASTEXITCODE":                true,
}

func (t *ToolExecutor) buildCommandEnv() ([]string, map[string]string) {
	baseSlice := os.Environ()
	baseline := make(map[string]string, len(baseSlice)+4)
	merged := make(map[string]string, len(baseSlice)+8)
	keyCase := make(map[string]string, len(baseSlice)+8)

	normKey := func(k string) string {
		if runtime.GOOS == "windows" {
			return strings.ToUpper(k)
		}
		return k
	}

	for _, kv := range baseSlice {
		idx := strings.IndexByte(kv, '=')
		if idx <= 0 {
			continue
		}
		k := kv[:idx]
		v := kv[idx+1:]
		nk := normKey(k)
		baseline[nk] = v
		merged[nk] = v
		keyCase[nk] = k
	}

	for _, defKV := range [][2]string{{"CI", "true"}, {"FORCE_COLOR", "0"}} {
		nk := normKey(defKV[0])
		baseline[nk] = defKV[1]
		merged[nk] = defKV[1]
		if _, ok := keyCase[nk]; !ok {
			keyCase[nk] = defKV[0]
		}
	}

	if t != nil {
		t.mu.Lock()
		for k, v := range t.SessionEnv {
			nk := normKey(k)
			merged[nk] = v
			keyCase[nk] = k
		}
		t.mu.Unlock()
	}

	envOut := make([]string, 0, len(merged))
	for nk, v := range merged {
		origK := keyCase[nk]
		if origK == "" {
			origK = nk
		}
		envOut = append(envOut, origK+"="+v)
	}
	return envOut, baseline
}

func parsePosixExportOrEnvLine(line string) (string, string, bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return "", "", false
	}
	if strings.HasPrefix(trimmed, "declare -x ") {
		trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "declare -x "))
	} else if strings.HasPrefix(trimmed, "export ") {
		trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "export "))
	}
	eqIdx := strings.IndexByte(trimmed, '=')
	if eqIdx <= 0 {
		return "", "", false
	}
	key := strings.TrimSpace(trimmed[:eqIdx])
	val := trimmed[eqIdx+1:]
	if len(val) >= 2 {
		if (val[0] == '"' && val[len(val)-1] == '"') || (val[0] == '\'' && val[len(val)-1] == '\'') {
			q := val[0]
			val = val[1 : len(val)-1]
			if q == '"' {
				val = strings.ReplaceAll(val, `\"`, `"`)
				val = strings.ReplaceAll(val, `\\`, `\`)
				val = strings.ReplaceAll(val, `\$`, `$`)
			}
		}
	}
	return key, val, true
}

func normalizeMsysPathOnWindows(p string) string {
	if runtime.GOOS != "windows" {
		return p
	}
	// Convert Git Bash /c/Users/... to C:\Users\...
	if len(p) >= 3 && p[0] == '/' && ((p[1] >= 'a' && p[1] <= 'z') || (p[1] >= 'A' && p[1] <= 'Z')) && p[2] == '/' {
		drive := strings.ToUpper(string(p[1]))
		return filepath.Clean(drive + `:\` + filepath.FromSlash(p[3:]))
	}
	return filepath.Clean(p)
}

func (t *ToolExecutor) extractAndStripShellState(rawOutput string, baselineEnv map[string]string, explicitCwd bool, initialWorkDir string) string {
	const pwdStartTag = "__UNJESS_STATE_PWD__:"
	const pwdEndTag = ":__END__"
	const envBeginTag = "__UNJESS_STATE_ENV_BEGIN__"
	const envEndTag = "__UNJESS_STATE_ENV_END__"

	pwdIdx := strings.LastIndex(rawOutput, pwdStartTag)
	if pwdIdx == -1 {
		return rawOutput
	}

	rem := rawOutput[pwdIdx+len(pwdStartTag):]
	pwdEndIdx := strings.Index(rem, pwdEndTag)
	if pwdEndIdx == -1 {
		return rawOutput
	}
	capturedPwd := strings.TrimSpace(rem[:pwdEndIdx])

	afterPwd := rem[pwdEndIdx+len(pwdEndTag):]
	envBeginIdx := strings.Index(afterPwd, envBeginTag)
	envEndIdx := strings.Index(afterPwd, envEndTag)

	var envLines []string
	var suffixAfterSentinel string
	if envBeginIdx != -1 && envEndIdx != -1 && envEndIdx > envBeginIdx {
		envBlock := afterPwd[envBeginIdx+len(envBeginTag) : envEndIdx]
		envLines = strings.Split(strings.ReplaceAll(envBlock, "\r\n", "\n"), "\n")
		suffixAfterSentinel = afterPwd[envEndIdx+len(envEndTag):]
		suffixAfterSentinel = strings.TrimPrefix(suffixAfterSentinel, "\r\n")
		suffixAfterSentinel = strings.TrimPrefix(suffixAfterSentinel, "\n")
	} else {
		suffixAfterSentinel = strings.TrimPrefix(afterPwd, "\r\n")
		suffixAfterSentinel = strings.TrimPrefix(suffixAfterSentinel, "\n")
	}

	// Strip the leading newline right before __UNJESS_STATE_PWD__ that our sentinel emitter wrote
	cleanPrefix := rawOutput[:pwdIdx]
	if strings.HasSuffix(cleanPrefix, "\r\n") {
		cleanPrefix = cleanPrefix[:len(cleanPrefix)-2]
	} else if strings.HasSuffix(cleanPrefix, "\n") {
		cleanPrefix = cleanPrefix[:len(cleanPrefix)-1]
	}
	cleanedOutput := cleanPrefix + suffixAfterSentinel

	if t == nil {
		return cleanedOutput
	}

	// 1. Update SessionCwd if valid and within sandbox
	if capturedPwd != "" {
		normPwd := normalizeMsysPathOnWindows(capturedPwd)
		if info, err := os.Stat(normPwd); err == nil && info.IsDir() {
			if checkCommandSandbox(normPwd, t.WorkspacePath) == nil {
				if !explicitCwd || !strings.EqualFold(filepath.Clean(normPwd), filepath.Clean(initialWorkDir)) {
					t.mu.Lock()
					t.SessionCwd = filepath.Clean(normPwd)
					t.mu.Unlock()
				}
			}
		}
	}

	// 2. Update SessionEnv delta
	if len(envLines) > 0 {
		normKey := func(k string) string {
			if runtime.GOOS == "windows" {
				return strings.ToUpper(k)
			}
			return k
		}
		t.mu.Lock()
		if t.SessionEnv == nil {
			t.SessionEnv = make(map[string]string)
		}
		for _, line := range envLines {
			k, v, ok := parsePosixExportOrEnvLine(line)
			if !ok || k == "" {
				continue
			}
			if k == "__UNJESS_UNSET__" {
				unsetKey := strings.TrimSpace(v)
				delete(t.SessionEnv, unsetKey)
				if runtime.GOOS == "windows" {
					for existingK := range t.SessionEnv {
						if strings.EqualFold(existingK, unsetKey) {
							delete(t.SessionEnv, existingK)
						}
					}
				}
				continue
			}
			if ignoredShellEnvVars[k] {
				continue
			}
			nk := normKey(k)
			if baseVal, exists := baselineEnv[nk]; exists && baseVal == v {
				continue
			}
			if runtime.GOOS == "windows" {
				for existingK := range t.SessionEnv {
					if strings.EqualFold(existingK, k) && existingK != k {
						delete(t.SessionEnv, existingK)
					}
				}
			}
			t.SessionEnv[k] = v
		}
		t.mu.Unlock()
	}

	return cleanedOutput
}

func (t *ToolExecutor) executeRunCommand(ctx context.Context, args map[string]any) (string, bool) {
	command := getString(args, "command", "cmd")
	if strings.TrimSpace(command) == "" {
		return "Error: command is required", false
	}

	if err := checkCommandSandbox(command, t.WorkspacePath); err != nil {
		return err.Error(), false
	}

	for _, p := range t.ProtectedPaths {
		baseP := filepath.Base(p)
		if strings.Contains(command, p) || (len(baseP) > 3 && strings.Contains(command, baseP)) {
			isMutating := strings.Contains(command, ">") ||
				strings.Contains(command, "rm ") ||
				strings.Contains(command, "sed -i") ||
				strings.Contains(command, "mv ") ||
				strings.Contains(command, "truncate ") ||
				strings.Contains(command, "tee ")
			if isMutating {
				return fmt.Sprintf("PermissionDenied: Command attempts to modify or delete protected file '%s'. Modifying this file is strictly forbidden by task specification.", p), false
			}
		}
	}

	workDir := t.WorkspacePath
	t.mu.Lock()
	if t.SessionCwd != "" {
		if info, errS := os.Stat(t.SessionCwd); errS == nil && info.IsDir() {
			workDir = t.SessionCwd
		}
	}
	t.mu.Unlock()

	explicitCwd := false
	if cwdArg := getString(args, "cwd", "workdir"); strings.TrimSpace(cwdArg) != "" {
		resolved, err := resolveReadOrInspectPath(t.WorkspacePath, cwdArg)
		if err != nil {
			return fmt.Sprintf("Error resolving cwd: %v", err), false
		}
		workDir = resolved
		explicitCwd = true
	}

	timeoutSec := t.CommandTimeoutSec
	if argTimeout := getInt(args, "timeout"); argTimeout > 0 {
		timeoutSec = argTimeout
	} else if argTimeoutSec := getInt(args, "timeout_seconds"); argTimeoutSec > 0 {
		timeoutSec = argTimeoutSec
	} else if argTimeoutSec := getInt(args, "timeout_sec"); argTimeoutSec > 0 {
		timeoutSec = argTimeoutSec
	}
	if timeoutSec <= 0 {
		timeoutSec = 120
	} else if timeoutSec > 1800 {
		timeoutSec = 1800
	}

	// Dynamic Asynchronous Background Execution
	if getBool(args, "is_background", "background") {
		return t.executeStartTask(ctx, args)
	}

	cmdEnv, baselineEnv := t.buildCommandEnv()

	var outBytes []byte
	var err error
	var timedOut bool

	for attempt := 0; attempt < 2; attempt++ {
		cmdCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
		var cmd *exec.Cmd
		if runtime.GOOS == "windows" {
			cmd = buildWindowsCommand(cmdCtx, command)
		} else {
			shell := "bash"
			if _, err := exec.LookPath("bash"); err != nil {
				shell = "sh"
			}
			effectiveCmd := command
			if os.Geteuid() == 0 {
				_ = os.Chmod("/tmp", 0777|os.ModeSticky)
				// Ensure files/dirs created in /tmp by root are accessible to privilege-dropped workers (nobody/worker)
				effectiveCmd = "umask 0000; " + effectiveCmd
				if getBool(args, "as_worker", "drop_privileges") {
					effectiveCmd = fmt.Sprintf(`if id -u worker >/dev/null 2>&1; then su -s /bin/sh worker -c %s; elif id -u nobody >/dev/null 2>&1; then su -s /bin/sh nobody -c %s; else %s; fi`,
						shellQuoteSingle(command), shellQuoteSingle(command), command)
				}
			}
			cmd = exec.CommandContext(cmdCtx, shell, "-c", wrapPosixCommandWithStateSentinel(effectiveCmd))
		}

		cmd.Dir = workDir
		setHideWindow(cmd)
		cmd.WaitDelay = 1500 * time.Millisecond
		cmd.Cancel = func() error {
			if cmd.Process == nil {
				return nil
			}
			if runtime.GOOS == "windows" {
				_ = exec.Command("taskkill", "/F", "/T", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
			}
			return cmd.Process.Kill()
		}
		cmd.Env = cmdEnv

		startTime := time.Now()
		outBytes, err = cmd.CombinedOutput()
		if cmdCtx.Err() == context.DeadlineExceeded {
			timedOut = true
		}
		cancel()

		elapsed := time.Since(startTime)
		if err != nil && attempt == 0 && elapsed < 5*time.Second {
			outStr := string(outBytes)
			errStr := err.Error()
			if strings.Contains(outStr, "ECONNREFUSED") || strings.Contains(outStr, "ETIMEDOUT") || strings.Contains(outStr, "socket hang up") ||
				strings.Contains(errStr, "ECONNREFUSED") || strings.Contains(errStr, "ETIMEDOUT") || strings.Contains(errStr, "socket hang up") {
				time.Sleep(500 * time.Millisecond)
				continue
			}
		}
		break
	}

	output := t.extractAndStripShellState(string(outBytes), baselineEnv, explicitCwd, workDir)

	if timedOut {
		t.ResetShellSession()
	}

	// Context Truncation & Disk Log Caching (Head 25 + Tail 35 with .unjess/logs pointers)
	output = t.truncateAndCacheOutput(command, output)

	if timedOut {
		return fmt.Sprintf("Error: command timed out after %d seconds (shell session state reset to workspace root).\nOutput:\n%s", timeoutSec, output), false
	}

	if err != nil {
		hint := analyzeSyntaxErrorAndLearn(command, output)
		if hint != "" {
			if len(output) > 0 {
				output += "\n\n" + hint
			} else {
				output = hint
			}
		}
		if len(strings.TrimSpace(output)) > 0 {
			return output, false
		}
		return fmt.Sprintf("Error executing command: %v", err), false
	}

	if strings.TrimSpace(output) == "" {
		return "(Command finished with zero output)", true
	}

	return output, true
}

var cmdLogCounter uint64

func shellQuoteSingle(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// errorLineMarkers defines tokens indicative of critical errors, panics, or stack traces in terminal logs.
var errorLineMarkers = []string{
	"FAILED",
	"FAIL:",
	"[FAIL]",
	"Error:",
	"ERROR:",
	"AssertionError",
	"Traceback (most recent call last):",
	"panic:",
	"fatal error:",
	"SyntaxError",
	"TypeError",
	"ValueError",
	"NameError",
	"AttributeError",
	"NullPointerException",
	"undefined reference",
	"Segmentation fault",
	"BUILD FAILED",
	"Compilation error",
	"error[E",
	"Exception in thread",
	"ModuleNotFoundError",
	"ImportError",
	"undefined symbol",
	"privilege-dropped",
	"Relative MSE",
	"Expected:",
	"Received:",
}

func (t *ToolExecutor) truncateAndCacheOutput(command, output string) string {
	if output == "" {
		return output
	}

	lines := strings.Split(output, "\n")
	const maxLinesThreshold = 300
	const maxBytesThreshold = 30000

	// If within budget, return raw output directly (300 lines / 30 KB — Claude Code maxResultSizeChars=30000 parity)
	if len(lines) <= maxLinesThreshold && len(output) <= maxBytesThreshold {
		return output
	}

	// Prepare logs directory: prefer .unjess/logs in workspace root, fallback to ~/.unjess/logs
	logsDir := filepath.Join(t.WorkspacePath, ".unjess", "logs")
	if err := os.MkdirAll(logsDir, 0755); err != nil {
		if home, errH := os.UserHomeDir(); errH == nil && home != "" {
			logsDir = filepath.Join(home, ".unjess", "logs")
			_ = os.MkdirAll(logsDir, 0755)
		}
	}

	counter := atomic.AddUint64(&cmdLogCounter, 1)
	timestamp := time.Now().Format("20060102_150405")
	logFileName := fmt.Sprintf("cmd_%s_%d.log", timestamp, counter)
	fullLogPath := filepath.Join(logsDir, logFileName)

	header := fmt.Sprintf("# Command: %s\n# Executed at: %s\n# Output lines: %d, bytes: %d\n\n",
		command, time.Now().Format(time.RFC3339), len(lines), len(output))
	_ = os.WriteFile(fullLogPath, []byte(header+output), 0644)

	relLogPath := fullLogPath
	if rel, err := filepath.Rel(t.WorkspacePath, fullLogPath); err == nil && !strings.HasPrefix(rel, "..") {
		relLogPath = filepath.ToSlash(rel)
	}

	// Line-based head (80 lines) + pointer banner + tail (100 lines)
	if len(lines) > 180 {
		headCount := 80
		tailCount := 100
		if len(lines) < headCount+tailCount {
			headCount = len(lines) / 2
			tailCount = len(lines) - headCount
		}

		headLines := lines[:headCount]
		tailLines := lines[len(lines)-tailCount:]

		// Error-Aware Truncation (Claude Code Parity Standard):
		// Inspect omitted middle lines to detect if test failures, panics, or tracebacks are hidden.
		middleStart := headCount
		middleEnd := len(lines) - tailCount
		firstErrIdx := -1

		for idx := middleStart; idx < middleEnd; idx++ {
			lineUpper := strings.ToUpper(lines[idx])
			for _, m := range errorLineMarkers {
				if strings.Contains(lines[idx], m) || strings.Contains(lineUpper, strings.ToUpper(m)) {
					firstErrIdx = idx
					break
				}
			}
			if firstErrIdx != -1 {
				break
			}
		}

		// If an error is detected in the middle slice, extract an error context block
		if firstErrIdx != -1 {
			errContextStart := firstErrIdx - 5
			if errContextStart < middleStart {
				errContextStart = middleStart
			}
			errContextEnd := firstErrIdx + 40
			if errContextEnd > middleEnd {
				errContextEnd = middleEnd
			}

			omittedBefore := errContextStart - middleStart
			omittedAfter := middleEnd - errContextEnd

			var b strings.Builder
			b.WriteString(strings.Join(headLines, "\n"))
			if omittedBefore > 0 {
				b.WriteString(fmt.Sprintf("\n\n... [Truncated %d lines before error] ...\n", omittedBefore))
			} else {
				b.WriteString("\n")
			}
			b.WriteString(fmt.Sprintf("--- [Error Context Detected: Lines %d-%d] ---\n", errContextStart+1, errContextEnd))
			b.WriteString(strings.Join(lines[errContextStart:errContextEnd], "\n"))
			if omittedAfter > 0 {
				b.WriteString(fmt.Sprintf("\n... [Truncated %d lines after error. Full output saved to %s. Use grep or read_file to inspect] ...\n\n", omittedAfter, relLogPath))
			} else {
				b.WriteString("\n")
			}
			b.WriteString(strings.Join(tailLines, "\n"))
			return b.String()
		}

		omittedCount := len(lines) - (headCount + tailCount)
		banner := fmt.Sprintf("\n... [Truncated %d lines. Full output saved to %s. Use grep or read_file to inspect] ...\n",
			omittedCount, relLogPath)

		return strings.Join(headLines, "\n") + banner + strings.Join(tailLines, "\n")
	}

	// Byte-based fallback for few massive lines
	const headBytes = 10000
	const tailBytes = 15000
	if len(output) > headBytes+tailBytes {
		headPart := output[:headBytes]
		tailPart := output[len(output)-tailBytes:]
		omittedBytes := len(output) - (headBytes + tailBytes)
		banner := fmt.Sprintf("\n... [Truncated %d characters. Full output saved to %s. Use grep or read_file to inspect] ...\n",
			omittedBytes, relLogPath)
		return headPart + banner + tailPart
	}

	return output
}

func (t *ToolExecutor) executeStartTask(ctx context.Context, args map[string]any) (string, bool) {
	command := getString(args, "command", "cmd")
	if strings.TrimSpace(command) == "" {
		return "Error: command is required", false
	}
	cwd := getString(args, "cwd", "workdir")
	task, err := GetGlobalTaskManager().StartBackgroundTask(ctx, t.WorkspacePath, command, cwd)
	if err != nil {
		return fmt.Sprintf("Error starting background task: %v", err), false
	}
	relLog := task.LogPath
	if rel, errR := filepath.Rel(t.WorkspacePath, task.LogPath); errR == nil && !strings.HasPrefix(rel, "..") {
		relLog = filepath.ToSlash(rel)
	}
	return fmt.Sprintf("Started background task '%s' (PID %d).\nCommand: %s\nStreaming output to: %s\nUse poll_task with task_id '%s' to check status and logs.",
		task.ID, task.PID, task.Command, relLog, task.ID), true
}

func (t *ToolExecutor) executePollTask(args map[string]any) (string, bool) {
	taskID := getString(args, "task_id", "id")
	if strings.TrimSpace(taskID) == "" {
		return "Error: task_id is required", false
	}
	res, err := GetGlobalTaskManager().PollTask(taskID)
	if err != nil {
		return err.Error(), false
	}
	data, _ := json.MarshalIndent(res, "", "  ")
	return string(data), true
}

func (t *ToolExecutor) executeCancelTask(args map[string]any) (string, bool) {
	taskID := getString(args, "task_id", "id")
	if strings.TrimSpace(taskID) == "" {
		return "Error: task_id is required", false
	}
	res, err := GetGlobalTaskManager().CancelTask(taskID)
	if err != nil {
		return err.Error(), false
	}
	return res, true
}

func (t *ToolExecutor) executeGitStatus() (string, bool) {
	cmd := exec.Command("git", "status", "--porcelain=v1", "--branch")
	cmd.Dir = t.WorkspacePath
	setHideWindow(cmd)
	out, err := cmd.CombinedOutput()
	outStr := string(out)
	if err != nil {
		if strings.Contains(strings.ToLower(outStr), "not a git repository") {
			return "Workspace is not a Git repository yet (no .git directory). You can initialize one with 'git init' if needed.", true
		}
		return fmt.Sprintf("Error getting git status: %v\n%s", err, outStr), false
	}
	res := outStr
	if strings.TrimSpace(res) == "" {
		return "On branch main — working tree clean", true
	}
	return res, true
}

func (t *ToolExecutor) executeGitDiff(args map[string]any) (string, bool) {
	cmdArgs := []string{"diff", "HEAD"}
	if p := getString(args, "path"); p != "" {
		cmdArgs = append(cmdArgs, "--", p)
	}
	cmd := exec.Command("git", cmdArgs...)
	cmd.Dir = t.WorkspacePath
	setHideWindow(cmd)
	out, err := cmd.CombinedOutput()
	outStr := string(out)
	if err != nil {
		if strings.Contains(strings.ToLower(outStr), "not a git repository") {
			return "Workspace is not a Git repository yet (no .git directory). You can initialize one with 'git init' if needed.", true
		}
		return fmt.Sprintf("Error getting git diff: %v\n%s", err, outStr), false
	}
	res := outStr
	if strings.TrimSpace(res) == "" {
		return "(No uncommitted git changes)", true
	}
	return res, true
}

func (t *ToolExecutor) touchFile(relPath string, action string, isDir bool) {
	for i, f := range t.FilesTouched {
		if f.Path == relPath {
			t.FilesTouched[i].Action = action
			return
		}
	}
	t.FilesTouched = append(t.FilesTouched, FileTouch{
		Path:   relPath,
		Action: action,
		IsDir:  isDir,
	})
}

func (t *ToolExecutor) recordDiff(relPath, action, diffStr string, adds, dels int) {
	t.Diffs = append(t.Diffs, FileDiff{
		Path:      relPath,
		Action:    action,
		Diff:      diffStr,
		Additions: adds,
		Deletions: dels,
	})
}

// recordSnapshotLocked records the pre-turn snapshot of a file.
// If an entry for relPath already exists, it is NOT overwritten so we keep the earliest pre-turn state.
func (t *ToolExecutor) recordSnapshotLocked(relPath string, existedBefore bool, beforeContent string) {
	cleanRel := filepath.ToSlash(relPath)
	for _, snap := range t.Snapshots {
		if snap.Path == cleanRel {
			return
		}
	}
	t.Snapshots = append(t.Snapshots, FileSnapshot{
		Path:          cleanRel,
		ExistedBefore: existedBefore,
		BeforeContent: beforeContent,
	})
}

// MergeSnapshots merges snapshots from a subagent or another executor into t, preserving earliest state.
func (t *ToolExecutor) MergeSnapshots(snapshots []FileSnapshot) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, snap := range snapshots {
		t.recordSnapshotLocked(snap.Path, snap.ExistedBefore, snap.BeforeContent)
	}
}

// ComputeUnifiedDiff generates a clean unified diff and additions/deletions counts.
func ComputeUnifiedDiff(relPath, oldContent, newContent string) (string, int, int) {
	if oldContent == newContent {
		return "", 0, 0
	}

	relSlash := filepath.ToSlash(relPath)
	oldLines := splitLines(oldContent)
	newLines := splitLines(newContent)

	if len(oldLines) == 0 {
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("--- /dev/null\n+++ b/%s\n", relSlash))
		sb.WriteString(fmt.Sprintf("@@ -0,0 +1,%d @@\n", len(newLines)))
		for _, l := range newLines {
			sb.WriteString("+" + l + "\n")
		}
		return sb.String(), len(newLines), 0
	}

	if len(newLines) == 0 {
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("--- a/%s\n+++ /dev/null\n", relSlash))
		sb.WriteString(fmt.Sprintf("@@ -1,%d +0,0 @@\n", len(oldLines)))
		for _, l := range oldLines {
			sb.WriteString("-" + l + "\n")
		}
		return sb.String(), 0, len(oldLines)
	}

	edits := myersDiff(oldLines, newLines)
	hunks := buildHunks(edits, 3)
	if len(hunks) == 0 {
		return "", 0, 0
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("--- a/%s\n+++ b/%s\n", relSlash, relSlash))

	additions := 0
	deletions := 0

	for _, h := range hunks {
		sb.WriteString(fmt.Sprintf("@@ -%s +%s @@\n",
			formatRange(h.oldStart, h.oldCount),
			formatRange(h.newStart, h.newCount)))
		for _, line := range h.lines {
			sb.WriteString(line.prefix + line.text + "\n")
			if line.prefix == "+" {
				additions++
			} else if line.prefix == "-" {
				deletions++
			}
		}
	}

	return sb.String(), additions, deletions
}

type diffOp int

const (
	opEqual diffOp = iota
	opDelete
	opInsert
)

type diffEdit struct {
	op   diffOp
	text string
}

func myersDiff(a, b []string) []diffEdit {
	n := len(a)
	m := len(b)
	max := n + m
	if max == 0 {
		return nil
	}

	v := make([]int, 2*max+1)
	offset := max
	trace := make([][]int, 0, max+1)

	v[offset+1] = 0

	found := false
	for d := 0; d <= max; d++ {
		vCopy := make([]int, len(v))
		copy(vCopy, v)

		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || (k != d && v[offset+k-1] < v[offset+k+1]) {
				x = v[offset+k+1]
			} else {
				x = v[offset+k-1] + 1
			}
			y := x - k

			for x < n && y < m && a[x] == b[y] {
				x++
				y++
			}

			vCopy[offset+k] = x

			if x >= n && y >= m {
				trace = append(trace, vCopy)
				found = true
				break
			}
		}

		if found {
			break
		}
		trace = append(trace, vCopy)
		copy(v, vCopy)
	}

	// Backtrack
	edits := make([]diffEdit, 0, max)
	x := n
	y := m

	for d := len(trace) - 1; d > 0; d-- {
		k := x - y
		prevV := trace[d-1]

		var prevK int
		if k == -d || (k != d && prevV[offset+k-1] < prevV[offset+k+1]) {
			prevK = k + 1
		} else {
			prevK = k - 1
		}

		prevX := prevV[offset+prevK]
		prevY := prevX - prevK

		for x > prevX && y > prevY {
			edits = append(edits, diffEdit{op: opEqual, text: a[x-1]})
			x--
			y--
		}

		if d > 0 {
			if x == prevX {
				edits = append(edits, diffEdit{op: opInsert, text: b[prevY]})
				y--
			} else if y == prevY {
				edits = append(edits, diffEdit{op: opDelete, text: a[prevX]})
				x--
			}
		}
	}

	for x > 0 && y > 0 {
		edits = append(edits, diffEdit{op: opEqual, text: a[x-1]})
		x--
		y--
	}
	for x > 0 {
		edits = append(edits, diffEdit{op: opDelete, text: a[x-1]})
		x--
	}
	for y > 0 {
		edits = append(edits, diffEdit{op: opInsert, text: b[y-1]})
		y--
	}

	for i, j := 0, len(edits)-1; i < j; i, j = i+1, j-1 {
		edits[i], edits[j] = edits[j], edits[i]
	}

	return edits
}

type diffLine struct {
	prefix string
	text   string
}

type diffHunk struct {
	oldStart int
	oldCount int
	newStart int
	newCount int
	lines    []diffLine
}

func buildHunks(edits []diffEdit, contextLines int) []diffHunk {
	var changeIndices []int
	for idx, e := range edits {
		if e.op != opEqual {
			changeIndices = append(changeIndices, idx)
		}
	}

	if len(changeIndices) == 0 {
		return nil
	}

	type editRange struct {
		start int
		end   int
	}

	var ranges []editRange
	current := editRange{
		start: max(0, changeIndices[0]-contextLines),
		end:   min(len(edits)-1, changeIndices[0]+contextLines),
	}

	for _, idx := range changeIndices[1:] {
		rangeStart := max(0, idx-contextLines)
		rangeEnd := min(len(edits)-1, idx+contextLines)

		if rangeStart <= current.end+1 {
			current.end = rangeEnd
		} else {
			ranges = append(ranges, current)
			current = editRange{start: rangeStart, end: rangeEnd}
		}
	}
	ranges = append(ranges, current)

	var hunks []diffHunk

	for _, r := range ranges {
		oldStart := 1
		newStart := 1
		for i := 0; i < r.start; i++ {
			if edits[i].op == opEqual || edits[i].op == opDelete {
				oldStart++
			}
			if edits[i].op == opEqual || edits[i].op == opInsert {
				newStart++
			}
		}

		oldCount := 0
		newCount := 0
		var hLines []diffLine

		for i := r.start; i <= r.end; i++ {
			e := edits[i]
			switch e.op {
			case opEqual:
				oldCount++
				newCount++
				hLines = append(hLines, diffLine{prefix: " ", text: e.text})
			case opDelete:
				oldCount++
				hLines = append(hLines, diffLine{prefix: "-", text: e.text})
			case opInsert:
				newCount++
				hLines = append(hLines, diffLine{prefix: "+", text: e.text})
			}
		}

		hunks = append(hunks, diffHunk{
			oldStart: oldStart,
			oldCount: oldCount,
			newStart: newStart,
			newCount: newCount,
			lines:    hLines,
		})
	}

	return hunks
}

func formatRange(start, count int) string {
	if count == 0 {
		return fmt.Sprintf("%d,0", start)
	}
	if count == 1 {
		return fmt.Sprintf("%d", start)
	}
	return fmt.Sprintf("%d,%d", start, count)
}

var readFileLinePrefixRe = regexp.MustCompile(`^\s*\d+:\s?(.*)$`)

// stripReadFileLinePrefixes removes accidental `123: ` line number prefixes copied from read_file output.
// It only strips prefixes if every non-empty line in the block starts with a line number prefix.
func stripReadFileLinePrefixes(s string) (string, bool) {
	lines := strings.Split(s, "\n")
	nonEmpty := 0
	matched := 0
	stripped := make([]string, len(lines))
	for i, l := range lines {
		if strings.TrimSpace(l) == "" {
			stripped[i] = l
			continue
		}
		nonEmpty++
		if m := readFileLinePrefixRe.FindStringSubmatch(l); len(m) == 2 {
			matched++
			stripped[i] = m[1]
		} else {
			stripped[i] = l
		}
	}
	if nonEmpty > 0 && matched == nonEmpty {
		return strings.Join(stripped, "\n"), true
	}
	return s, false
}

func leadingWhitespace(s string) string {
	for i, r := range s {
		if r != ' ' && r != '\t' {
			return s[:i]
		}
	}
	return s
}

func shiftReplacementIndentation(repLines, targetLines []string, matchedOldFirstLine string) []string {
	var firstTargetLine string
	for _, l := range targetLines {
		if strings.TrimSpace(l) != "" {
			firstTargetLine = l
			break
		}
	}
	actualIndent := leadingWhitespace(matchedOldFirstLine)
	targetIndent := leadingWhitespace(firstTargetLine)
	if actualIndent == targetIndent {
		return repLines
	}

	out := make([]string, len(repLines))
	for i, l := range repLines {
		if strings.TrimSpace(l) == "" {
			out[i] = l
			continue
		}
		if targetIndent != "" && strings.HasPrefix(l, targetIndent) {
			out[i] = actualIndent + strings.TrimPrefix(l, targetIndent)
		} else if targetIndent == "" {
			out[i] = actualIndent + l
		} else {
			// Compute relative offset in spaces
			lIndent := leadingWhitespace(l)
			extra := ""
			if len(lIndent) > len(targetIndent) {
				extra = lIndent[len(targetIndent):]
			}
			out[i] = actualIndent + extra + strings.TrimLeft(l, " \t")
		}
	}
	return out
}

func findOccurrenceLineNumbers(content, target string) string {
	var lineNums []string
	offset := 0
	for {
		idx := strings.Index(content[offset:], target)
		if idx == -1 {
			break
		}
		absIdx := offset + idx
		lineNum := 1 + strings.Count(content[:absIdx], "\n")
		lineNums = append(lineNums, strconv.Itoa(lineNum))
		offset = absIdx + len(target)
		if len(lineNums) >= 8 || offset >= len(content) {
			break
		}
	}
	return strings.Join(lineNums, ", ")
}

func findClosestMatchPreview(oldLines, targetLines []string) string {
	if len(oldLines) == 0 {
		return "(file is empty)"
	}

	// Collect meaningful lines and identifier tokens from targetLines
	var anchors []string
	var identTokens []string
	seenTok := make(map[string]bool)
	for _, tl := range targetLines {
		tr := strings.TrimSpace(tl)
		if len(tr) >= 3 {
			anchors = append(anchors, tr)
		}
		for _, tok := range strings.FieldsFunc(tr, func(r rune) bool {
			return !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_')
		}) {
			if len(tok) >= 3 && !seenTok[tok] {
				seenTok[tok] = true
				identTokens = append(identTokens, tok)
			}
		}
	}

	bestLineIdx := 0
	bestScore := 0

	if len(anchors) > 0 || len(identTokens) > 0 {
		windowSize := len(targetLines)
		if windowSize < 3 {
			windowSize = 3
		}
		if windowSize > 30 {
			windowSize = 30
		}
		for i := 0; i < len(oldLines); i++ {
			score := 0
			end := i + windowSize
			if end > len(oldLines) {
				end = len(oldLines)
			}
			for k := i; k < end; k++ {
				olTrim := strings.TrimSpace(oldLines[k])
				if olTrim == "" {
					continue
				}
				for _, a := range anchors {
					if olTrim == a {
						score += 10
					} else if strings.Contains(olTrim, a) || strings.Contains(a, olTrim) {
						score += 4
					}
				}
				for _, tok := range identTokens {
					if strings.Contains(olTrim, tok) {
						score += 2
					}
				}
			}
			if score > bestScore {
				bestScore = score
				bestLineIdx = i
			}
		}
	}

	start := 0
	end := 25
	headerLabel := "File starts with:"
	if bestScore > 0 {
		start = bestLineIdx - 5
		if start < 0 {
			start = 0
		}
		end = bestLineIdx + len(targetLines) + 10
		headerLabel = fmt.Sprintf("Closest matching section in file (lines %d-%d):", start+1, min(end, len(oldLines)))
	}
	if end > len(oldLines) {
		end = len(oldLines)
	}

	var sb strings.Builder
	sb.WriteString(headerLabel + "\n")
	for i := start; i < end; i++ {
		sb.WriteString(fmt.Sprintf("%d: %s\n", i+1, oldLines[i]))
	}
	return strings.TrimRight(sb.String(), "\n")
}

func applySmartReplacement(oldContent, oldStr, newStr, relPath string) (string, error) {
	return applySmartReplacementWithOptions(oldContent, oldStr, newStr, relPath, false)
}

func applySmartReplacementWithOptions(oldContent, oldStr, newStr, relPath string, replaceAll bool) (string, error) {
	// Strategy 1: Exact substring match
	count := strings.Count(oldContent, oldStr)
	if count == 1 {
		return strings.Replace(oldContent, oldStr, newStr, 1), nil
	}
	if count > 1 {
		if replaceAll {
			return strings.ReplaceAll(oldContent, oldStr, newStr), nil
		}
		locs := findOccurrenceLineNumbers(oldContent, oldStr)
		return "", fmt.Errorf("Error: Target string found %d times in %s (at lines %s). Please provide a more specific target with surrounding context lines that matches exactly once, or set \"replace_all\": true.", count, relPath, locs)
	}

	// Strategy 2: Newline-normalized match (\r\n vs \n)
	normOld := normalizeNewlines(oldContent)
	normOldStr := normalizeNewlines(oldStr)
	normNewStr := normalizeNewlines(newStr)

	// Strategy 2.5: Strip accidental read_file line-number prefixes (e.g. "42: code") if raw match failed
	if strippedOld, wasStripped := stripReadFileLinePrefixes(normOldStr); wasStripped && strings.TrimSpace(strippedOld) != "" {
		normOldStr = strippedOld
		if strippedNew, newWasStripped := stripReadFileLinePrefixes(normNewStr); newWasStripped {
			normNewStr = strippedNew
		}
	}

	countNorm := strings.Count(normOld, normOldStr)
	if countNorm == 1 {
		replaced := strings.Replace(normOld, normOldStr, normNewStr, 1)
		if strings.Contains(oldContent, "\r\n") {
			return strings.ReplaceAll(replaced, "\n", "\r\n"), nil
		}
		return replaced, nil
	}
	if countNorm > 1 {
		if replaceAll {
			replaced := strings.ReplaceAll(normOld, normOldStr, normNewStr)
			if strings.Contains(oldContent, "\r\n") {
				return strings.ReplaceAll(replaced, "\n", "\r\n"), nil
			}
			return replaced, nil
		}
		locs := findOccurrenceLineNumbers(normOld, normOldStr)
		return "", fmt.Errorf("Error: Target string found %d times in %s (newline-normalized, at lines %s). Please provide a more specific target or set \"replace_all\": true.", countNorm, relPath, locs)
	}

	// Strategy 3: Whitespace-tolerant line block match with relative indentation preservation
	targetLines := splitLines(normOldStr)
	var trimmedTarget []string
	for _, l := range targetLines {
		if strings.TrimSpace(l) != "" {
			trimmedTarget = append(trimmedTarget, strings.TrimSpace(l))
		}
	}

	oldLines := splitLines(normOld)
	var nonZeroIdx []int
	var nonZeroLines []string
	for idx, l := range oldLines {
		if strings.TrimSpace(l) != "" {
			nonZeroIdx = append(nonZeroIdx, idx)
			nonZeroLines = append(nonZeroLines, strings.TrimSpace(l))
		}
	}

	tLen := len(trimmedTarget)
	if tLen > 0 && len(nonZeroLines) >= tLen {
		var matches [][2]int
		for i := 0; i <= len(nonZeroLines)-tLen; i++ {
			match := true
			for j := 0; j < tLen; j++ {
				if nonZeroLines[i+j] != trimmedTarget[j] {
					match = false
					break
				}
			}
			if match {
				startOldIdx := nonZeroIdx[i]
				endOldIdx := nonZeroIdx[i+tLen-1] + 1
				matches = append(matches, [2]int{startOldIdx, endOldIdx})
			}
		}

		if len(matches) == 1 {
			startI := matches[0][0]
			endI := matches[0][1]
			repLines := shiftReplacementIndentation(splitLines(normNewStr), targetLines, oldLines[startI])

			var replacedLines []string
			replacedLines = append(replacedLines, oldLines[:startI]...)
			replacedLines = append(replacedLines, repLines...)
			replacedLines = append(replacedLines, oldLines[endI:]...)

			joined := strings.Join(replacedLines, "\n")
			if strings.HasSuffix(normOld, "\n") {
				joined += "\n"
			}
			if strings.Contains(oldContent, "\r\n") {
				return strings.ReplaceAll(joined, "\n", "\r\n"), nil
			}
			return joined, nil
		}
		if len(matches) > 1 {
			if replaceAll {
				var replacedLines []string
				prevEnd := 0
				for _, m := range matches {
					if m[0] < prevEnd {
						continue
					}
					replacedLines = append(replacedLines, oldLines[prevEnd:m[0]]...)
					repLines := shiftReplacementIndentation(splitLines(normNewStr), targetLines, oldLines[m[0]])
					replacedLines = append(replacedLines, repLines...)
					prevEnd = m[1]
				}
				replacedLines = append(replacedLines, oldLines[prevEnd:]...)
				joined := strings.Join(replacedLines, "\n")
				if strings.HasSuffix(normOld, "\n") {
					joined += "\n"
				}
				if strings.Contains(oldContent, "\r\n") {
					return strings.ReplaceAll(joined, "\n", "\r\n"), nil
				}
				return joined, nil
			}
			return "", fmt.Errorf("Error: Target string matched %d locations in %s with fuzzy matching. Please provide a more specific target with surrounding context lines.", len(matches), relPath)
		}
	}

	// Strategy 4: Closest-match failure preview with line numbers
	closestPreview := findClosestMatchPreview(oldLines, targetLines)
	return "", fmt.Errorf("Error: Target string not found in %s.\n%s", relPath, closestPreview)
}

func normalizeNewlines(s string) string {
	s = strings.ReplaceAll(s, "\r\r\n", "\n")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

func splitLines(s string) []string {
	if len(s) == 0 {
		return nil
	}
	norm := normalizeNewlines(s)
	if strings.HasSuffix(norm, "\n") {
		norm = norm[:len(norm)-1]
	}
	if len(norm) == 0 {
		return []string{""}
	}
	return strings.Split(norm, "\n")
}

func getString(args map[string]any, keys ...string) string {
	for _, k := range keys {
		if val, ok := args[k]; ok && val != nil {
			if s, ok := val.(string); ok {
				return s
			}
			return fmt.Sprintf("%v", val)
		}
	}
	return ""
}

func getInt(args map[string]any, keys ...string) int {
	for _, key := range keys {
		if val, ok := args[key]; ok && val != nil {
			switch v := val.(type) {
			case int:
				return v
			case int64:
				return int(v)
			case float64:
				return int(v)
			case float32:
				return int(v)
			case string:
				if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
					return n
				}
			}
		}
	}
	return 0
}

func formatSize(sizeBytes int64) string {
	if sizeBytes < 1024 {
		return fmt.Sprintf("%d B", sizeBytes)
	}
	if sizeBytes < 1024*1024 {
		return fmt.Sprintf("%.1f KB", float64(sizeBytes)/1024.0)
	}
	return fmt.Sprintf("%.1f MB", float64(sizeBytes)/(1024.0*1024.0))
}

func isIgnoredDirName(name string) bool {
	lower := strings.ToLower(name)
	switch lower {
	case ".git", "__pycache__", "node_modules", "target", ".venv", "venv", "env",
		".pytest_cache", ".mypy_cache", ".ruff_cache", "dist", "build", "out",
		".next", ".nuxt", ".idea", ".vscode", ".vs", "engine", "unjess-portable",
		"standalone-test", "vendor", "coverage", "bin", "obj", ".gradle", ".cargo",
		"appdata", ".unjess_attachments":
		return true
	default:
		return false
	}
}

func isBinaryOrHeavyFile(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".exe", ".dll", ".so", ".dylib", ".bin", ".zip", ".tar", ".gz", ".7z",
		".iso", ".png", ".jpg", ".jpeg", ".webp", ".gif", ".ico", ".mp4", ".mp3",
		".wav", ".pyc", ".pyo", ".class", ".jar", ".woff", ".woff2", ".ttf":
		return true
	default:
		return false
	}
}

// executeMemorySave saves or updates a cross-session memory in ~/.unjess/memory/memories.json.
func (t *ToolExecutor) executeMemorySave(args map[string]any) (string, bool) {
	key := getString(args, "key", "name")
	if strings.TrimSpace(key) == "" {
		return "Error: key is required", false
	}
	content := getString(args, "content", "value")
	if strings.TrimSpace(content) == "" {
		return "Error: content is required", false
	}
	category := getString(args, "category", "type")
	scope := getString(args, "scope")

	ws := "global"
	if strings.EqualFold(scope, "workspace") {
		ws = t.WorkspacePath
	}

	var tags []string
	if rawTags, ok := args["tags"].([]any); ok {
		for _, rt := range rawTags {
			if s := strings.TrimSpace(fmt.Sprint(rt)); s != "" {
				tags = append(tags, s)
			}
		}
	} else if rawTagsStr, ok := args["tags"].([]string); ok {
		tags = rawTagsStr
	}

	entry, err := memory.SaveMemory(key, category, content, ws, tags)
	if err != nil {
		return fmt.Sprintf("Error saving memory: %v", err), false
	}

	return fmt.Sprintf("Successfully saved cross-session memory [%s] '%s' (scope: %s, ID: %s): %s", entry.Category, entry.Key, entry.Workspace, entry.ID, entry.Content), true
}

// executeMemoryRecall searches and recalls persistent memories across all sessions.
func (t *ToolExecutor) executeMemoryRecall(args map[string]any) (string, bool) {
	query := getString(args, "query", "q")
	category := getString(args, "category", "type")

	entries := memory.SearchMemories(query, category, t.WorkspacePath)
	if len(entries) == 0 {
		if query != "" {
			return fmt.Sprintf("No memories found matching query '%s'.", query), true
		}
		return "No persistent memories found in ~/.unjess/memory/memories.json.", true
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("### Recalled Memories (%d found)\n", len(entries)))
	for _, e := range entries {
		wsScope := e.Workspace
		if wsScope == "" {
			wsScope = "global"
		}
		tagsStr := ""
		if len(e.Tags) > 0 {
			tagsStr = fmt.Sprintf(" [tags: %s]", strings.Join(e.Tags, ", "))
		}
		sb.WriteString(fmt.Sprintf("- **[%s] %s** (scope: %s%s, id: `%s`):\n  %s\n", e.Category, e.Key, wsScope, tagsStr, e.ID, e.Content))
	}

	return strings.TrimSpace(sb.String()), true
}

// executeMemoryDelete deletes a persistent memory entry by key or ID.
func (t *ToolExecutor) executeMemoryDelete(args map[string]any) (string, bool) {
	key := getString(args, "key", "id", "name")
	if strings.TrimSpace(key) == "" {
		return "Error: key or id is required", false
	}

	deleted, err := memory.DeleteMemory(key)
	if err != nil {
		return fmt.Sprintf("Error deleting memory: %v", err), false
	}
	if !deleted {
		return fmt.Sprintf("No memory entry found with key or ID: '%s'", key), false
	}

	return fmt.Sprintf("Successfully deleted memory entry: '%s'", key), true
}

type webSearchResult struct {
	Title   string
	URL     string
	Snippet string
}

func parseDuckDuckGoHTML(htmlContent string, maxResults int) []webSearchResult {
	if maxResults <= 0 {
		maxResults = 6
	}

	var results []webSearchResult

	// Pattern 1: standard html.duckduckgo.com (<a class="result__a" href="..." >Title</a> and class="result__snippet")
	reBlock := regexp.MustCompile(`(?is)<div[^>]*class="[^"]*result results_links[^"]*"[^>]*>(.*?)</div>\s*</div>`)
	blocks := reBlock.FindAllStringSubmatch(htmlContent, -1)

	reLink := regexp.MustCompile(`(?is)<a[^>]*class="[^"]*result__a[^"]*"[^>]*href="([^"]+)"[^>]*>(.*?)</a>`)
	reSnippet := regexp.MustCompile(`(?is)<(?:a|div)[^>]*class="[^"]*result__snippet[^"]*"[^>]*>(.*?)</(?:a|div)>`)
	reStripTag := regexp.MustCompile(`<[^>]+>`)

	for _, b := range blocks {
		if len(results) >= maxResults {
			break
		}
		blockHTML := b[1]
		linkMatch := reLink.FindStringSubmatch(blockHTML)
		if len(linkMatch) < 3 {
			continue
		}

		rawURL := linkMatch[1]
		// Decode uddg= if present
		if idx := strings.Index(rawURL, "uddg="); idx != -1 {
			target := rawURL[idx+5:]
			if ampIdx := strings.Index(target, "&"); ampIdx != -1 {
				target = target[:ampIdx]
			}
			if unesc, err := url.QueryUnescape(target); err == nil && unesc != "" {
				rawURL = unesc
			}
		}
		if strings.HasPrefix(rawURL, "//") {
			rawURL = "https:" + rawURL
		}

		title := html.UnescapeString(strings.TrimSpace(reStripTag.ReplaceAllString(linkMatch[2], "")))
		snippet := ""
		if snipMatch := reSnippet.FindStringSubmatch(blockHTML); len(snipMatch) >= 2 {
			snippet = html.UnescapeString(strings.TrimSpace(reStripTag.ReplaceAllString(snipMatch[1], "")))
		}

		if rawURL != "" && title != "" {
			results = append(results, webSearchResult{
				Title:   title,
				URL:     rawURL,
				Snippet: snippet,
			})
		}
	}

	// Pattern 2: if pattern 1 did not find blocks (e.g. flatter HTML structure or lite.duckduckgo.com)
	if len(results) == 0 {
		// Try finding result-link and result-snippet from lite
		reLiteLink := regexp.MustCompile(`(?is)<a[^>]*class=['"]result-link['"][^>]*href=['"]([^'"]+)['"][^>]*>(.*?)</a>`)
		reLiteSnippet := regexp.MustCompile(`(?is)<td[^>]*class=['"]result-snippet['"][^>]*>(.*?)</td>`)

		linkMatches := reLiteLink.FindAllStringSubmatch(htmlContent, -1)
		snippetMatches := reLiteSnippet.FindAllStringSubmatch(htmlContent, -1)

		for i, lm := range linkMatches {
			if len(results) >= maxResults {
				break
			}
			rawURL := lm[1]
			if idx := strings.Index(rawURL, "uddg="); idx != -1 {
				target := rawURL[idx+5:]
				if ampIdx := strings.Index(target, "&"); ampIdx != -1 {
					target = target[:ampIdx]
				}
				if unesc, err := url.QueryUnescape(target); err == nil && unesc != "" {
					rawURL = unesc
				}
			}
			if strings.HasPrefix(rawURL, "//") {
				rawURL = "https:" + rawURL
			}
			title := html.UnescapeString(strings.TrimSpace(reStripTag.ReplaceAllString(lm[2], "")))
			snippet := ""
			if i < len(snippetMatches) && len(snippetMatches[i]) >= 2 {
				snippet = html.UnescapeString(strings.TrimSpace(reStripTag.ReplaceAllString(snippetMatches[i][1], "")))
			}

			if rawURL != "" && title != "" {
				results = append(results, webSearchResult{
					Title:   title,
					URL:     rawURL,
					Snippet: snippet,
				})
			}
		}
	}

	// Pattern 3: direct regex search for result__a if neither block pattern worked
	if len(results) == 0 {
		linkMatches := reLink.FindAllStringSubmatch(htmlContent, -1)
		for _, lm := range linkMatches {
			if len(results) >= maxResults {
				break
			}
			rawURL := lm[1]
			if idx := strings.Index(rawURL, "uddg="); idx != -1 {
				target := rawURL[idx+5:]
				if ampIdx := strings.Index(target, "&"); ampIdx != -1 {
					target = target[:ampIdx]
				}
				if unesc, err := url.QueryUnescape(target); err == nil && unesc != "" {
					rawURL = unesc
				}
			}
			if strings.HasPrefix(rawURL, "//") {
				rawURL = "https:" + rawURL
			}
			title := html.UnescapeString(strings.TrimSpace(reStripTag.ReplaceAllString(lm[2], "")))
			if rawURL != "" && title != "" {
				results = append(results, webSearchResult{
					Title: title,
					URL:   rawURL,
				})
			}
		}
	}

	return results
}

// executeWebSearch searches the web via DuckDuckGo.
func (t *ToolExecutor) executeWebSearch(ctx context.Context, args map[string]any) (string, bool) {
	query := getString(args, "query", "q")
	if strings.TrimSpace(query) == "" {
		return "Error: query is required", false
	}

	maxResults := getInt(args, "max_results")
	if maxResults <= 0 {
		maxResults = getInt(args, "limit")
	}
	if maxResults <= 0 {
		maxResults = 6
	}
	if maxResults > 20 {
		maxResults = 20
	}

	searchCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()

	client := &http.Client{
		Timeout: 12 * time.Second,
	}

	searchURL := "https://html.duckduckgo.com/html/?q=" + url.QueryEscape(query)
	req, err := http.NewRequestWithContext(searchCtx, "GET", searchURL, nil)
	if err != nil {
		return fmt.Sprintf("Error creating web search request: %v", err), false
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

	var bodyBytes []byte
	var results []webSearchResult

	for attempt := 0; attempt < 2; attempt++ {
		resp, err := client.Do(req)
		if err == nil && resp != nil {
			if resp.StatusCode == 429 || resp.StatusCode == 502 || resp.StatusCode == 503 || resp.StatusCode == 504 {
				_ = resp.Body.Close()
				if attempt == 0 {
					select {
					case <-searchCtx.Done():
					case <-time.After(600 * time.Millisecond):
						continue
					}
				}
				break
			}
			if resp.StatusCode == http.StatusOK {
				bodyBytes, _ = io.ReadAll(io.LimitReader(resp.Body, 512*1024))
				_ = resp.Body.Close()
				results = parseDuckDuckGoHTML(string(bodyBytes), maxResults)
			} else {
				_ = resp.Body.Close()
			}
			break
		} else {
			if attempt == 0 {
				select {
				case <-searchCtx.Done():
				case <-time.After(600 * time.Millisecond):
					continue
				}
			}
			break
		}
	}

	// Fallback to lite.duckduckgo.com if html.duckduckgo.com returned challenge or 0 results
	if len(results) == 0 && searchCtx.Err() == nil {
		liteURL := "https://lite.duckduckgo.com/lite/"
		postData := url.Values{"q": {query}}
		liteReq, liteErr := http.NewRequestWithContext(searchCtx, "POST", liteURL, strings.NewReader(postData.Encode()))
		if liteErr == nil {
			liteReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			liteReq.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36")
			liteReq.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")

			for attempt := 0; attempt < 2; attempt++ {
				liteResp, doErr := client.Do(liteReq)
				if doErr == nil && liteResp != nil {
					if liteResp.StatusCode == 429 || liteResp.StatusCode == 502 || liteResp.StatusCode == 503 || liteResp.StatusCode == 504 {
						_ = liteResp.Body.Close()
						if attempt == 0 {
							select {
							case <-searchCtx.Done():
							case <-time.After(600 * time.Millisecond):
								continue
							}
						}
						break
					}
					if liteResp.StatusCode == http.StatusOK {
						liteBody, _ := io.ReadAll(io.LimitReader(liteResp.Body, 512*1024))
						_ = liteResp.Body.Close()
						results = parseDuckDuckGoHTML(string(liteBody), maxResults)
					} else {
						_ = liteResp.Body.Close()
					}
					break
				} else {
					if attempt == 0 {
						select {
						case <-searchCtx.Done():
						case <-time.After(600 * time.Millisecond):
							continue
						}
					}
					break
				}
			}
		}
	}

	if len(results) == 0 {
		return fmt.Sprintf("No web search results found for query: '%s'", query), true
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("### Web Search Results for: %s\n\n", query))
	for i, r := range results {
		sb.WriteString(fmt.Sprintf("%d. [%s](%s)\n", i+1, r.Title, r.URL))
		if r.Snippet != "" {
			sb.WriteString(fmt.Sprintf("   %s\n\n", r.Snippet))
		} else {
			sb.WriteString("\n")
		}
	}

	return strings.TrimSpace(sb.String()), true
}

func cleanHTMLContent(htmlStr string, maxChars int) string {
	// Strip script, style, noscript, svg, iframe
	reTagBlocks := regexp.MustCompile(`(?is)<(script|style|noscript|svg|iframe)[^>]*>.*?</(script|style|noscript|svg|iframe)>`)
	htmlStr = reTagBlocks.ReplaceAllString(htmlStr, "")

	// Convert headings
	reHeadings := regexp.MustCompile(`(?i)<h([1-6])[^>]*>(.*?)</h[1-6]>`)
	htmlStr = reHeadings.ReplaceAllStringFunc(htmlStr, func(m string) string {
		parts := reHeadings.FindStringSubmatch(m)
		if len(parts) >= 3 {
			level := parts[1]
			content := strings.TrimSpace(parts[2])
			prefix := strings.Repeat("#", int(level[0]-'0'))
			return fmt.Sprintf("\n\n%s %s\n\n", prefix, content)
		}
		return "\n\n"
	})

	// Convert breaks
	reBreaks := regexp.MustCompile(`(?i)<br\s*/?>`)
	htmlStr = reBreaks.ReplaceAllString(htmlStr, "\n")

	// Convert blocks to newlines
	reBlocks := regexp.MustCompile(`(?i)</?(p|div|li|tr|blockquote|article|section|header|footer)[^>]*>`)
	htmlStr = reBlocks.ReplaceAllString(htmlStr, "\n")

	// Strip remaining HTML tags
	reAnyTag := regexp.MustCompile(`<[^>]+>`)
	text := reAnyTag.ReplaceAllString(htmlStr, "")

	// HTML unescape
	text = html.UnescapeString(text)

	// Clean up whitespace
	lines := strings.Split(text, "\n")
	var cleanLines []string
	consecutiveBlanks := 0
	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		if trimmed == "" {
			consecutiveBlanks++
			if consecutiveBlanks <= 2 {
				cleanLines = append(cleanLines, "")
			}
		} else {
			consecutiveBlanks = 0
			cleanLines = append(cleanLines, trimmed)
		}
	}

	result := strings.TrimSpace(strings.Join(cleanLines, "\n"))
	if maxChars <= 0 {
		maxChars = 25000
	}
	if len(result) > maxChars {
		result = result[:maxChars] + fmt.Sprintf("\n\n...[content truncated to %d characters]", maxChars)
	}
	return result
}

// executeReadURL fetches and cleans the readable text/markdown from a public web URL.
func (t *ToolExecutor) executeReadURL(ctx context.Context, args map[string]any) (string, bool) {
	targetURL := getString(args, "url", "target_url", "link")
	if strings.TrimSpace(targetURL) == "" {
		return "Error: url is required", false
	}

	parsed, err := url.Parse(targetURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return fmt.Sprintf("Error: invalid URL '%s'. Must start with http:// or https://", targetURL), false
	}

	readCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	client := &http.Client{
		Timeout: 15 * time.Second,
	}

	req, err := http.NewRequestWithContext(readCtx, "GET", targetURL, nil)
	if err != nil {
		return fmt.Sprintf("Error creating request: %v", err), false
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,text/plain;q=0.8,*/*;q=0.7")

	var resp *http.Response
	for attempt := 0; attempt < 2; attempt++ {
		resp, err = client.Do(req)
		if err == nil && resp != nil {
			if resp.StatusCode == 429 || resp.StatusCode == 502 || resp.StatusCode == 503 || resp.StatusCode == 504 {
				_ = resp.Body.Close()
				if attempt == 0 {
					select {
					case <-readCtx.Done():
						return fmt.Sprintf("HTTP %d returned when requesting %s (context timeout)", resp.StatusCode, targetURL), false
					case <-time.After(600 * time.Millisecond):
						continue
					}
				}
				return fmt.Sprintf("HTTP %d (%s) returned when requesting %s", resp.StatusCode, resp.Status, targetURL), false
			}
			break
		} else {
			if attempt == 0 {
				select {
				case <-readCtx.Done():
					return fmt.Sprintf("Error fetching URL: %v", err), false
				case <-time.After(600 * time.Millisecond):
					continue
				}
			}
			return fmt.Sprintf("Error fetching URL: %v", err), false
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		// If this is a GitHub URL returning 404 (e.g. a private repository) and the gh CLI is authenticated locally, fall back to gh CLI
		if resp.StatusCode == 404 && strings.EqualFold(parsed.Host, "github.com") {
			ghPath := strings.Trim(parsed.Path, "/")
			ghParts := strings.Split(ghPath, "/")
			if len(ghParts) >= 2 {
				ownerRepo := ghParts[0] + "/" + strings.TrimSuffix(ghParts[1], ".git")
				if ghBin, lookErr := exec.LookPath("gh"); lookErr == nil && ghBin != "" {
					ghCmd := exec.CommandContext(readCtx, ghBin, "repo", "view", ownerRepo)
					setHideWindow(ghCmd)
					if ghOut, ghErr := ghCmd.CombinedOutput(); ghErr == nil && len(strings.TrimSpace(string(ghOut))) > 0 {
						content := string(ghOut)
						for _, docName := range []string{"AGENTS.md", "SKILL.md", "VIDEO.md"} {
							apiCmd := exec.CommandContext(readCtx, ghBin, "api", fmt.Sprintf("repos/%s/contents/%s", ownerRepo, docName), "-H", "Accept: application/vnd.github.raw")
							setHideWindow(apiCmd)
							if docOut, docErr := apiCmd.Output(); docErr == nil && len(strings.TrimSpace(string(docOut))) > 0 {
								content += fmt.Sprintf("\n\n--- %s ---\n%s", docName, string(docOut))
							}
						}
						if len(content) > 25000 {
							content = content[:25000] + "\n\n...[content truncated to 25,000 characters]"
						}
						return fmt.Sprintf("### Content from %s (via authenticated gh CLI)\n\n%s", targetURL, content), true
					}
				}
			}
		}
		return fmt.Sprintf("HTTP %d (%s) returned when requesting %s", resp.StatusCode, resp.Status, targetURL), false
	}

	// Read up to 250 KB
	data, err := io.ReadAll(io.LimitReader(resp.Body, 250*1024))
	if err != nil {
		return fmt.Sprintf("Error reading URL response body: %v", err), false
	}

	contentType := resp.Header.Get("Content-Type")
	var cleaned string
	if strings.Contains(contentType, "text/plain") || strings.Contains(contentType, "application/json") {
		cleaned = string(data)
		if len(cleaned) > 25000 {
			cleaned = cleaned[:25000] + "\n\n...[content truncated to 25,000 characters]"
		}
	} else {
		cleaned = cleanHTMLContent(string(data), 25000)
	}

	return fmt.Sprintf("### Content from %s\n\n%s", targetURL, cleaned), true
}

func (t *ToolExecutor) executeMCPStatus(args map[string]any) (string, bool) {
	configs, err := mcp.LoadMCPConfig()
	if err != nil {
		return fmt.Sprintf("Error reading MCP configuration: %v", err), false
	}
	if len(configs) == 0 {
		return "No MCP servers configured in ~/.unjess/mcp.json.\nPresets available: mashina-collab, rea, filesystem, github, fetch.", true
	}

	mgr := mcp.NewManagerWithConfig(configs, t.WorkspacePath)
	defer mgr.DisconnectAll()
	refresh := mgr.RefreshMCPTools()

	serverStatus, _ := refresh["server_status"].(map[string]any)
	toolsList, _ := refresh["tools"].([]map[string]any)

	// Group tools by server
	serverTools := make(map[string][]string)
	for _, tl := range toolsList {
		srv, _ := tl["server"].(string)
		toolName, _ := tl["name"].(string)
		if srv != "" && toolName != "" {
			serverTools[srv] = append(serverTools[srv], toolName)
		}
	}

	var sb strings.Builder
	sb.WriteString("### MCP Servers Status\n\n")

	sNames := make([]string, 0, len(configs))
	for name := range configs {
		sNames = append(sNames, name)
	}
	sort.Strings(sNames)

	for _, sName := range sNames {
		sCfg := configs[sName]
		statusStr := "disabled"
		if sCfg.Enabled {
			if st, ok := serverStatus[sName]; ok {
				statusStr = fmt.Sprintf("%v", st)
			} else {
				statusStr = "enabled"
			}
		}

		sb.WriteString(fmt.Sprintf("- **%s** (Status: `%s`)\n", sName, statusStr))
		if sCfg.Enabled && strings.HasPrefix(sCfg.URL, "http") {
			sb.WriteString(fmt.Sprintf("  - Endpoint: `%s`\n", sCfg.URL))
		}
		if sCfg.Description != "" {
			sb.WriteString(fmt.Sprintf("  - Description: %s\n", sCfg.Description))
		}
		tools := serverTools[sName]
		if len(tools) > 0 {
			sb.WriteString(fmt.Sprintf("  - Available Tools (%d): `%s`\n", len(tools), strings.Join(tools, "`, `")))
			if sName == "mashina-collab" {
				sb.WriteString("  - Tip: When calling `mcp_mashina-collab_collab_deploy`, you can pass `files: [{\"path\": \"index.html\"}, ...]` with `content` omitted/empty (or `content: \"@disk\"`), and the Unjess daemon will automatically read the exact file bytes from your workspace disk without transcription.\n")
			}
		} else if sCfg.Enabled && statusStr == "connected" {
			sb.WriteString("  - Tools: None exported or discovery pending\n")
		} else if !sCfg.Enabled {
			sb.WriteString("  - Tools: (Server disabled)\n")
		} else {
			sb.WriteString(fmt.Sprintf("  - Tools: None available (Status: %s)\n", statusStr))
		}
		sb.WriteString("\n")
	}

	return strings.TrimSpace(sb.String()), true
}

// -----------------------------------------------------------------------------
// v2.1.0 Tool Execution Handlers (Code Intelligence, Frontier, Security)
// -----------------------------------------------------------------------------

func (t *ToolExecutor) executeASTOutline(args map[string]any) (string, bool) {
	relPath := getString(args, "path", "file_path", "target_file", "file")
	if strings.TrimSpace(relPath) == "" {
		return "Error: path is required", false
	}
	outline, err := codeintel.FormatFileOutline(t.WorkspacePath, relPath)
	if err != nil {
		return fmt.Sprintf("Error formatting AST outline: %v", err), false
	}
	return outline, true
}

func (t *ToolExecutor) executeASTFindCallers(args map[string]any) (string, bool) {
	sym := getString(args, "symbol", "name", "identifier")
	if strings.TrimSpace(sym) == "" {
		return "Error: symbol is required", false
	}
	report, err := codeintel.FindSymbolCallers(t.WorkspacePath, sym)
	if err != nil {
		return fmt.Sprintf("Error finding symbol callers: %v", err), false
	}
	return report, true
}

func (t *ToolExecutor) executeASTEditSymbol(ctx context.Context, args map[string]any) (string, bool) {
	relPath := getString(args, "path", "file_path", "target_file", "file")
	if strings.TrimSpace(relPath) == "" {
		return "Error: path is required", false
	}
	sym := getString(args, "symbol", "name")
	if strings.TrimSpace(sym) == "" {
		return "Error: symbol is required", false
	}
	newCode := getString(args, "new_code", "code", "replacement")

	safePath, err := workspace.ResolveSafePath(t.WorkspacePath, relPath)
	if err != nil {
		return fmt.Sprintf("Error: %v", err), false
	}
	rel, err := filepath.Rel(t.WorkspacePath, safePath)
	if err != nil {
		rel = relPath
	}
	relSlash := filepath.ToSlash(rel)

	var oldContent string
	if existingBytes, err := os.ReadFile(safePath); err == nil {
		oldContent = string(existingBytes)
	}

	t.mu.Lock()
	t.recordSnapshotLocked(relSlash, true, oldContent)
	t.mu.Unlock()

	oldContentRet, updatedContent, callersReport, err := codeintel.EditSymbol(t.WorkspacePath, relSlash, sym, newCode)
	if err != nil {
		return fmt.Sprintf("Error editing symbol %q in %s: %v", sym, relSlash, err), false
	}

	diffStr, adds, dels := ComputeUnifiedDiff(relSlash, oldContentRet, updatedContent)

	t.mu.Lock()
	t.touchFile(relSlash, "modified", false)
	t.recordDiff(relSlash, "modified", diffStr, adds, dels)
	t.mu.Unlock()

	lintFeedback := codeintel.RunPostEditCheck(ctx, t.WorkspacePath, relSlash)

	res := fmt.Sprintf("Successfully edited symbol %q in %s\n\n%s", sym, relSlash, diffStr)
	if callersReport != "" {
		res += "\n\nCross-Repo Callers & References:\n" + callersReport
	}
	if lintFeedback != "" {
		res += "\n\n" + lintFeedback
	}
	return res, true
}

func (t *ToolExecutor) executeASTRenameSymbol(args map[string]any) (string, bool) {
	oldName := getString(args, "old_name", "old")
	newName := getString(args, "new_name", "new")
	if oldName == "" || newName == "" {
		return "Error: old_name and new_name are required", false
	}
	modifiedFiles, count, err := codeintel.RenameSymbolAcrossRepo(t.WorkspacePath, oldName, newName)
	if err != nil {
		return fmt.Sprintf("Error renaming symbol %q to %q: %v", oldName, newName, err), false
	}
	for _, f := range modifiedFiles {
		t.mu.Lock()
		t.touchFile(f, "modified", false)
		t.mu.Unlock()
	}
	if len(modifiedFiles) == 0 {
		return fmt.Sprintf("No occurrences of symbol %q found across repository.", oldName), true
	}
	return fmt.Sprintf("Successfully renamed %q to %q across %d file(s) (%d total replacements):\n- %s",
		oldName, newName, len(modifiedFiles), count, strings.Join(modifiedFiles, "\n- ")), true
}

func (t *ToolExecutor) executeGitArcheology(ctx context.Context, args map[string]any) (string, bool) {
	relPath := getString(args, "path", "file_path", "file")
	query := getString(args, "query", "symbol", "search")
	startLine := getInt(args, "start_line")
	endLine := getInt(args, "end_line")
	res, err := codeintel.GitArcheology(ctx, t.WorkspacePath, relPath, query, startLine, endLine)
	if err != nil {
		return fmt.Sprintf("Error in git_archeology: %v", err), false
	}
	return res, true
}

func (t *ToolExecutor) executeDebugTrace(ctx context.Context, args map[string]any) (string, bool) {
	cmdStr := getString(args, "command", "cmd")
	if strings.TrimSpace(cmdStr) == "" {
		return "Error: command is required", false
	}
	res, err := codeintel.DebugTraceCommand(ctx, t.WorkspacePath, cmdStr)
	if err != nil {
		return fmt.Sprintf("Debug trace error: %v\nOutput: %s", err, res), false
	}
	return res, true
}

func (t *ToolExecutor) executeSynthesizeTool(args map[string]any) (string, bool) {
	name := getString(args, "name", "tool_name")
	desc := getString(args, "description", "desc")
	lang := getString(args, "language", "lang")
	code := getString(args, "code", "content")
	paramsObj, _ := args["parameters"].(map[string]any)

	if name == "" || desc == "" || lang == "" || code == "" {
		return "Error: name, description, language, and code are required", false
	}
	ct, err := frontier.SynthesizeTool(name, desc, lang, code, paramsObj)
	if err != nil {
		return fmt.Sprintf("Error synthesizing tool: %v", err), false
	}
	return fmt.Sprintf("Successfully synthesized and registered tool '%s' (%s) at %s.\nIt is now available for execution!", ct.Name, ct.Language, ct.ScriptPath), true
}

func (t *ToolExecutor) executeViewImage(ctx context.Context, args map[string]any) (string, bool) {
	var targets []string
	if single := getString(args, "path", "file_path", "target", "file", "image"); strings.TrimSpace(single) != "" {
		targets = append(targets, strings.TrimSpace(single))
	}
	for _, listKey := range []string{"paths", "files", "images", "targets"} {
		if rawList, ok := args[listKey].([]any); ok {
			for _, item := range rawList {
				if s := strings.TrimSpace(fmt.Sprint(item)); s != "" && s != "<nil>" {
					targets = append(targets, s)
				}
			}
		} else if strList, ok := args[listKey].([]string); ok {
			for _, s := range strList {
				if s = strings.TrimSpace(s); s != "" {
					targets = append(targets, s)
				}
			}
		}
	}

	if len(targets) == 0 {
		return "Error: 'path' or 'paths' parameter is required to view image file(s).", false
	}
	if len(targets) > 6 {
		targets = targets[:6]
	}

	var reports []string
	anySuccess := false
	for _, tgt := range targets {
		dataURI, report, err := frontier.CaptureVisualQA(ctx, t.WorkspacePath, tgt, 1280, 800)
		if err != nil {
			reports = append(reports, fmt.Sprintf("- %s: Error (%v)", tgt, err))
			continue
		}
		imgName := filepath.Base(tgt)
		if imgName == "" || imgName == "." || imgName == "/" {
			imgName = "image.jpg"
		}
		t.QueueVisionImage(imgName, dataURI)
		reports = append(reports, "- "+report)
		anySuccess = true
	}

	return strings.Join(reports, "\n"), anySuccess
}

func (t *ToolExecutor) executeVisualQA(ctx context.Context, args map[string]any) (string, bool) {
	target := getString(args, "target", "url", "path", "file", "image")
	if strings.TrimSpace(target) == "" {
		if _, hasPaths := args["paths"]; hasPaths {
			return t.executeViewImage(ctx, args)
		}
		return "Error: target is required", false
	}
	width := getInt(args, "width")
	if width <= 0 {
		width = 1280
	}
	height := getInt(args, "height")
	if height <= 0 {
		height = 800
	}
	dataURI, report, err := frontier.CaptureVisualQA(ctx, t.WorkspacePath, target, width, height)
	if err != nil {
		return fmt.Sprintf("Visual QA capture error: %v", err), false
	}
	imgName := filepath.Base(target)
	if imgName == "" || imgName == "." || imgName == "/" || strings.HasPrefix(strings.ToLower(target), "http") {
		imgName = "visual_qa.jpg"
	}
	t.QueueVisionImage(imgName, dataURI)
	return report, true
}

func (t *ToolExecutor) executeSecAttackSurface() (string, bool) {
	_, report, err := security.ScanAttackSurface(t.WorkspacePath)
	if err != nil {
		return fmt.Sprintf("Error scanning attack surface: %v", err), false
	}
	return report, true
}

func (t *ToolExecutor) executeSecTaintScan() (string, bool) {
	_, report, err := security.RunTaintAnalysis(t.WorkspacePath)
	if err != nil {
		return fmt.Sprintf("Error running taint analysis: %v", err), false
	}
	return report, true
}

func (t *ToolExecutor) executeSecSmartFuzz(ctx context.Context, args map[string]any) (string, bool) {
	url := getString(args, "url", "target_url", "target")
	if strings.TrimSpace(url) == "" {
		return "Error: url is required", false
	}
	method := getString(args, "method")
	if method == "" {
		method = "GET"
	}
	concurrency := getInt(args, "concurrency")
	if concurrency <= 0 {
		concurrency = 25
	}
	var payloads []string
	if rawPayloads, ok := args["payloads"].([]any); ok {
		for _, p := range rawPayloads {
			if s, ok := p.(string); ok && s != "" {
				payloads = append(payloads, s)
			}
		}
	}
	_, report, err := security.RunSmartFuzz(ctx, url, method, concurrency, payloads)
	if err != nil {
		return fmt.Sprintf("Error during smart fuzzing: %v", err), false
	}
	return report, true
}

func (t *ToolExecutor) executeSecSupplyChainAudit() (string, bool) {
	_, report, err := security.AuditSupplyChain(t.WorkspacePath)
	if err != nil {
		return fmt.Sprintf("Error auditing supply chain: %v", err), false
	}
	return report, true
}

func (t *ToolExecutor) executeRunSecurityDuel(ctx context.Context, args map[string]any) (string, bool) {
	targetURL := getString(args, "target_url", "url")
	rep, err := security.RunSecurityDuel(ctx, t.WorkspacePath, targetURL)
	if err != nil {
		return fmt.Sprintf("Error running security duel: %v", err), false
	}
	return rep.SummaryMarkdown, true
}

func getBool(args map[string]any, keys ...string) bool {
	for _, key := range keys {
		if val, ok := args[key]; ok && val != nil {
			switch v := val.(type) {
			case bool:
				return v
			case string:
				s := strings.ToLower(strings.TrimSpace(v))
				return s == "true" || s == "1" || s == "yes"
			case int:
				return v != 0
			case float64:
				return v != 0
			}
		}
	}
	return false
}

func (t *ToolExecutor) executeBrowserUse(ctx context.Context, args map[string]any) (string, bool) {
	action := getString(args, "action", "cmd", "op")
	if action == "" {
		action = "screenshot"
	}
	opts := frontier.BrowserUseOptions{
		Action:     action,
		URL:        getString(args, "url", "target", "link"),
		Index:      getInt(args, "index"),
		Selector:   getString(args, "selector", "css"),
		Text:       getString(args, "text", "value", "input"),
		PressEnter: getBool(args, "press_enter", "enter"),
		Direction:  getString(args, "direction", "dir"),
		Amount:     getInt(args, "amount"),
		Key:        getString(args, "key"),
		Script:     getString(args, "script", "js", "code"),
	}
	if opts.Index == 0 {
		opts.Index = getInt(args, "idx")
	}
	if hlVal, ok := args["headless"]; ok {
		switch h := hlVal.(type) {
		case bool:
			opts.Headless = &h
		case string:
			b := strings.ToLower(strings.TrimSpace(h)) == "true"
			opts.Headless = &b
		}
	}

	res, err := frontier.ExecuteBrowserUse(ctx, t.WorkspacePath, opts)
	if err != nil {
		return fmt.Sprintf("Browser-Use error: %v", err), false
	}
	if res.VisionDataURI != "" {
		imgName := "browser_use.jpg"
		if res.ScreenshotPath != "" {
			imgName = filepath.Base(res.ScreenshotPath)
		}
		t.QueueVisionImage(imgName, res.VisionDataURI)
	}
	return res.FormattedState, true
}

func getStringSlice(args map[string]any, keys ...string) []string {
	for _, k := range keys {
		val, ok := args[k]
		if !ok || val == nil {
			continue
		}
		switch v := val.(type) {
		case []string:
			var out []string
			for _, s := range v {
				if trimmed := strings.TrimSpace(s); trimmed != "" {
					out = append(out, trimmed)
				}
			}
			return out
		case []any:
			var out []string
			for _, item := range v {
				if trimmed := strings.TrimSpace(fmt.Sprint(item)); trimmed != "" && trimmed != "<nil>" {
					out = append(out, trimmed)
				}
			}
			return out
		case string:
			var out []string
			for _, part := range strings.Split(v, ",") {
				if trimmed := strings.TrimSpace(part); trimmed != "" {
					out = append(out, trimmed)
				}
			}
			return out
		}
	}
	return nil
}

func getStringMap(args map[string]any, key string) map[string]string {
	val, ok := args[key]
	if !ok || val == nil {
		return nil
	}
	out := make(map[string]string)
	switch v := val.(type) {
	case map[string]string:
		for mk, mv := range v {
			out[mk] = mv
		}
	case map[string]any:
		for mk, mv := range v {
			out[mk] = fmt.Sprint(mv)
		}
	}
	return out
}

func (t *ToolExecutor) executeManageUnjess(args map[string]any) (string, bool) {
	action := strings.ToLower(strings.TrimSpace(getString(args, "action", "op", "command_action")))
	switch action {
	case "add_provider":
		id := getString(args, "id", "provider", "slug")
		name := getString(args, "name", "title")
		if id == "" && name != "" {
			id = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(name), " ", "-"))
		}
		baseURL := getString(args, "base_url", "url", "endpoint")
		apiKey := getString(args, "api_key", "key", "token")
		models := getStringSlice(args, "models", "model")
		cp, err := config.SaveCustomProvider(config.CustomProvider{
			ID:      id,
			Name:    name,
			BaseURL: baseURL,
			APIKey:  apiKey,
			Models:  models,
		})
		if err != nil {
			return fmt.Sprintf("Error adding custom AI provider: %v", err), false
		}
		return fmt.Sprintf("Successfully registered Custom AI Provider '%s' (id: %s, base_url: %s) with %d model(s): %s.\nPersisted to ~/.unjess/custom_providers.json and ~/.unjess/keys.yaml — available immediately in both Unjess Desktop and CLI without recompiling.",
			cp.Name, cp.ID, cp.BaseURL, len(cp.Models), strings.Join(cp.Models, ", ")), true

	case "remove_provider":
		id := getString(args, "id", "provider", "name")
		if id == "" {
			return "Error: 'id' is required for remove_provider.", false
		}
		if _, err := config.DeleteCustomProvider(id); err != nil {
			return fmt.Sprintf("Error removing custom AI provider '%s': %v", id, err), false
		}
		return fmt.Sprintf("Removed custom AI provider '%s' from ~/.unjess/custom_providers.json.", id), true

	case "list_providers":
		var sb strings.Builder
		sb.WriteString("### Built-in AI Providers\n")
		sb.WriteString("- ollama (Local Ollama daemon)\n- ollama-api (Ollama Cloud API)\n- anthropic\n- openai\n- google\n- deepseek\n- xai\n- qwen\n- kimi\n- openrouter, groq, mistral, cerebras, lmstudio, llamacpp\n\n")
		customs := config.LoadCustomProviders()
		sb.WriteString(fmt.Sprintf("### Custom AI Providers (%d configured in ~/.unjess/custom_providers.json)\n", len(customs)))
		if len(customs) == 0 {
			sb.WriteString("(None configured yet. Use manage_unjess with action='add_provider' to add any OpenAI-compatible provider.)\n")
		} else {
			for _, cp := range customs {
				hasKey := cp.APIKey != ""
				sb.WriteString(fmt.Sprintf("- **%s** (`%s`): `%s` | Models: `%s` | API Key set: `%v`\n",
					cp.Name, cp.ID, cp.BaseURL, strings.Join(cp.Models, ", "), hasKey))
			}
		}
		return sb.String(), true

	case "add_mcp":
		name := getString(args, "name", "id")
		if name == "" {
			return "Error: 'name' is required for add_mcp.", false
		}
		cmd := getString(args, "command", "cmd")
		mcpURL := getString(args, "url", "endpoint")
		mcpArgs := getStringSlice(args, "args", "arguments")
		env := getStringMap(args, "env")
		headers := getStringMap(args, "headers")
		desc := getString(args, "description", "desc")
		cfg := mcp.ServerConfig{
			Name:        name,
			Command:     cmd,
			Args:        mcpArgs,
			URL:         mcpURL,
			Env:         env,
			Headers:     headers,
			Description: desc,
			Enabled:     true,
		}
		if err := mcp.SaveMCPServer(cfg); err != nil {
			return fmt.Sprintf("Error saving MCP server '%s': %v", name, err), false
		}
		return fmt.Sprintf("Successfully added/updated MCP server '%s' in ~/.unjess/mcp.json.", name), true

	case "remove_mcp":
		name := getString(args, "name", "id")
		if name == "" {
			return "Error: 'name' is required for remove_mcp.", false
		}
		if err := mcp.DeleteMCPServer(name); err != nil {
			return fmt.Sprintf("Error removing MCP server '%s': %v", name, err), false
		}
		return fmt.Sprintf("Removed MCP server '%s' from ~/.unjess/mcp.json.", name), true

	case "save_skill":
		name := getString(args, "name", "id", "title")
		desc := getString(args, "description", "desc")
		instructions := getString(args, "instructions", "content", "body", "prompt")
		triggers := getStringSlice(args, "triggers", "keywords")
		scope := getString(args, "scope")
		if scope == "" {
			scope = "global"
		}
		if name == "" || instructions == "" {
			return "Error: 'name' and 'instructions' are required for save_skill.", false
		}
		info, err := skills.SaveSkill(skills.SkillSaveRequest{
			Name:         name,
			Description:  desc,
			Triggers:     triggers,
			Instructions: instructions,
			Scope:        scope,
			Workspace:    t.WorkspacePath,
		})
		if err != nil {
			return fmt.Sprintf("Error saving skill '%s': %v", name, err), false
		}
		return fmt.Sprintf("Successfully saved Skill '%s' (%s scope) at %s/SKILL.md.", info.Name, info.Scope, info.Path), true

	case "install_skill_github":
		repoURL := getString(args, "url", "repo_url", "repo")
		if repoURL == "" {
			return "Error: 'url' (GitHub repository URL) is required for install_skill_github.", false
		}
		res, err := skills.InstallFromGitHub(repoURL)
		if err != nil {
			return fmt.Sprintf("Error installing skill from GitHub (%s): %v", repoURL, err), false
		}
		b, _ := json.MarshalIndent(res, "", "  ")
		return fmt.Sprintf("Installed skill(s) from GitHub (%s):\n%s", repoURL, string(b)), true

	case "delete_skill":
		name := getString(args, "name", "id")
		if name == "" {
			return "Error: 'name' is required for delete_skill.", false
		}
		deleted, err := skills.DeleteSkill(name, "", t.WorkspacePath)
		if err != nil {
			return fmt.Sprintf("Error deleting skill '%s': %v", name, err), false
		}
		return fmt.Sprintf("Deleted skill '%s' (%d path(s) removed: %s).", name, len(deleted), strings.Join(deleted, ", ")), true

	default:
		return fmt.Sprintf("Error: unknown manage_unjess action '%s'. Supported actions: add_provider, remove_provider, list_providers, add_mcp, remove_mcp, save_skill, install_skill_github, delete_skill.", action), false
	}
}
