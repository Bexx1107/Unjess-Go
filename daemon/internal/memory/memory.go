package memory

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

var (
	memoryMu sync.RWMutex
)

// MemoryEntry represents a single persistent cross-session memory.
type MemoryEntry struct {
	ID        string   `json:"id"`
	Key       string   `json:"key"`
	Category  string   `json:"category"`
	Content   string   `json:"content"`
	Workspace string   `json:"workspace,omitempty"`
	Tags      []string `json:"tags,omitempty"`
	CreatedAt string   `json:"created_at"`
	UpdatedAt string   `json:"updated_at"`
}

// MemoryFilePath returns the absolute path to ~/.unjess/memory/memories.json,
// or the override specified in UNJESS_MEMORY_PATH (useful for unit tests).
func MemoryFilePath() string {
	if override := strings.TrimSpace(os.Getenv("UNJESS_MEMORY_PATH")); override != "" {
		return override
	}

	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = "."
	}
	return filepath.Join(home, ".unjess", "memory", "memories.json")
}

func generateID(length int) string {
	b := make([]byte, length/2+1)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)[:length]
}

// normalizeWorkspaceKey canonicalizes a workspace identifier or path across
// Windows/POSIX separators so exact workspace comparisons avoid false misses
// or partial substring collisions.
func normalizeWorkspaceKey(ws string) string {
	clean := strings.TrimSpace(ws)
	if clean == "" {
		return ""
	}
	if strings.EqualFold(clean, "global") {
		return "global"
	}
	clean = strings.ReplaceAll(clean, "\\", "/")
	for strings.Contains(clean, "//") {
		clean = strings.ReplaceAll(clean, "//", "/")
	}
	for len(clean) > 1 && strings.HasSuffix(clean, "/") {
		if len(clean) == 3 && clean[1] == ':' {
			break
		}
		clean = strings.TrimSuffix(clean, "/")
	}
	return strings.ToLower(clean)
}

// LoadMemories reads and parses all saved cross-session memories from disk.
// Returns an empty slice if the file does not yet exist.
func LoadMemories() ([]MemoryEntry, error) {
	memoryMu.RLock()
	defer memoryMu.RUnlock()

	return loadMemoriesLocked()
}

func loadMemoriesLocked() ([]MemoryEntry, error) {
	filePath := MemoryFilePath()
	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return make([]MemoryEntry, 0), nil
		}
		return nil, fmt.Errorf("failed reading memories file: %w", err)
	}

	if len(strings.TrimSpace(string(data))) == 0 {
		return make([]MemoryEntry, 0), nil
	}

	var entries []MemoryEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("failed unmarshaling memories: %w", err)
	}

	return entries, nil
}

// SaveMemory saves or updates a cross-session memory entry.
// If an entry with the same Key exists (case-insensitive), it is updated in-place.
// Otherwise, a new memory entry is appended.
func SaveMemory(key, category, content, workspace string, tags []string) (MemoryEntry, error) {
	memoryMu.Lock()
	defer memoryMu.Unlock()

	cleanKey := strings.TrimSpace(key)
	if cleanKey == "" {
		return MemoryEntry{}, fmt.Errorf("memory key cannot be empty")
	}
	cleanContent := strings.TrimSpace(content)
	if cleanContent == "" {
		return MemoryEntry{}, fmt.Errorf("memory content cannot be empty")
	}

	cleanCategory := strings.ToLower(strings.TrimSpace(category))
	if cleanCategory == "" {
		cleanCategory = "fact"
	}
	switch cleanCategory {
	case "preference", "project", "fact", "decision", "architecture":
	default:
		// Accept custom categories or normalize
		if cleanCategory == "" {
			cleanCategory = "fact"
		}
	}

	cleanWS := strings.TrimSpace(workspace)
	if cleanWS == "" || strings.EqualFold(cleanWS, "global") {
		cleanWS = "global"
	}

	now := time.Now().UTC().Format(time.RFC3339)

	entries, err := loadMemoriesLocked()
	if err != nil {
		entries = make([]MemoryEntry, 0)
	}

	targetScope := normalizeWorkspaceKey(cleanWS)
	var targetIndex = -1
	for i, entry := range entries {
		entryScope := normalizeWorkspaceKey(entry.Workspace)
		if entryScope == "" {
			entryScope = "global"
		}
		if strings.EqualFold(entry.Key, cleanKey) && entryScope == targetScope {
			targetIndex = i
			break
		}
	}

	var savedEntry MemoryEntry
	if targetIndex >= 0 {
		// Update existing
		existing := entries[targetIndex]
		existing.Category = cleanCategory
		existing.Content = cleanContent
		if cleanWS != "" {
			existing.Workspace = cleanWS
		}
		if tags != nil {
			existing.Tags = tags
		}
		existing.UpdatedAt = now
		entries[targetIndex] = existing
		savedEntry = existing
	} else {
		// Create new
		savedEntry = MemoryEntry{
			ID:        "mem_" + generateID(8),
			Key:       cleanKey,
			Category:  cleanCategory,
			Content:   cleanContent,
			Workspace: cleanWS,
			Tags:      tags,
			CreatedAt: now,
			UpdatedAt: now,
		}
		entries = append(entries, savedEntry)
	}

	if err := saveMemoriesLocked(entries); err != nil {
		return MemoryEntry{}, err
	}

	return savedEntry, nil
}

func saveMemoriesLocked(entries []MemoryEntry) error {
	filePath := MemoryFilePath()
	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed creating memory directory: %w", err)
	}

	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return fmt.Errorf("failed marshaling memories: %w", err)
	}

	// Write atomically via temporary file
	tmpPath := fmt.Sprintf("%s.tmp.%d", filePath, time.Now().UnixNano())
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return fmt.Errorf("failed writing tmp memory file: %w", err)
	}

	if err := os.Rename(tmpPath, filePath); err != nil {
		// Fallback to direct write if rename fails (e.g. cross-device on Windows)
		_ = os.Remove(tmpPath)
		if writeErr := os.WriteFile(filePath, data, 0644); writeErr != nil {
			return fmt.Errorf("failed writing memories file: %w", writeErr)
		}
	}

	return nil
}

// SearchMemories searches and ranks memories by query, category, and workspace.
// If query is empty, all memories matching category and workspace filters are returned.
func SearchMemories(query, category, workspace string) []MemoryEntry {
	memoryMu.RLock()
	defer memoryMu.RUnlock()

	entries, err := loadMemoriesLocked()
	if err != nil || len(entries) == 0 {
		return make([]MemoryEntry, 0)
	}

	cleanQuery := strings.ToLower(strings.TrimSpace(query))
	cleanCat := strings.ToLower(strings.TrimSpace(category))
	cleanWS := strings.TrimSpace(workspace)
	targetWS := normalizeWorkspaceKey(cleanWS)

	type scoredEntry struct {
		entry MemoryEntry
		score int
	}

	var scored []scoredEntry
	for _, entry := range entries {
		// Category filter
		if cleanCat != "" && !strings.EqualFold(entry.Category, cleanCat) {
			continue
		}

		// Workspace filter: when an active workspace is supplied, strictly skip
		// entries tied to other workspaces (keep only global or matching workspace).
		entryWS := normalizeWorkspaceKey(entry.Workspace)
		if targetWS != "" && entryWS != "" && entryWS != "global" && entryWS != targetWS {
			continue
		}

		score := 0
		if cleanQuery == "" {
			// No query: include all matching entries, score by recency
			score = 10
		} else {
			entryKeyLower := strings.ToLower(entry.Key)
			entryContentLower := strings.ToLower(entry.Content)
			entryCatLower := strings.ToLower(entry.Category)

			if entryKeyLower == cleanQuery {
				score += 100
			} else if strings.Contains(entryKeyLower, cleanQuery) {
				score += 50
			}

			if strings.Contains(entryContentLower, cleanQuery) {
				score += 25
			}

			if strings.Contains(entryCatLower, cleanQuery) {
				score += 15
			}

			for _, tag := range entry.Tags {
				tagLower := strings.ToLower(tag)
				if tagLower == cleanQuery {
					score += 40
				} else if strings.Contains(tagLower, cleanQuery) {
					score += 20
				}
			}

			// Boost if relevant to active workspace (only when the query actually matched)
			if score > 0 && targetWS != "" && (entryWS == "" || entryWS == "global" || entryWS == targetWS) {
				score += 5
			}
		}

		if score > 0 {
			scored = append(scored, scoredEntry{entry: entry, score: score})
		}
	}

	// Sort by score descending, then by UpdatedAt descending
	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].score != scored[j].score {
			return scored[i].score > scored[j].score
		}
		return scored[i].entry.UpdatedAt > scored[j].entry.UpdatedAt
	})

	results := make([]MemoryEntry, len(scored))
	for i, s := range scored {
		results[i] = s.entry
	}
	return results
}

// DeleteMemory deletes a memory entry by ID or by Key.
// Returns true if an entry was found and deleted, false otherwise.
func DeleteMemory(idOrKey string) (bool, error) {
	memoryMu.Lock()
	defer memoryMu.Unlock()

	cleanIDOrKey := strings.TrimSpace(idOrKey)
	if cleanIDOrKey == "" {
		return false, fmt.Errorf("id or key cannot be empty")
	}

	entries, err := loadMemoriesLocked()
	if err != nil {
		return false, err
	}

	newEntries := make([]MemoryEntry, 0, len(entries))
	deleted := false
	for _, entry := range entries {
		if entry.ID == cleanIDOrKey || strings.EqualFold(entry.Key, cleanIDOrKey) {
			deleted = true
			continue
		}
		newEntries = append(newEntries, entry)
	}

	if !deleted {
		return false, nil
	}

	if err := saveMemoriesLocked(newEntries); err != nil {
		return false, err
	}
	return true, nil
}

// FormatForSystemPrompt loads saved memories, prioritizes global memories
// and those matching activeWorkspace, and formats them into a clean prompt block.
// If 0 memories exist, it returns a 2-line block confirming persistent memory is ENABLED.
func FormatForSystemPrompt(activeWorkspace string, maxEntries int) string {
	if maxEntries <= 0 {
		maxEntries = 30
	}

	entries, err := LoadMemories()
	if err != nil || len(entries) == 0 {
		return "Cross-Session Memory: ENABLED (~/.unjess/memory/memories.json).\nYou HAVE persistent memory across sessions via the memory_save, memory_recall, and memory_delete tools.\n"
	}

	cleanWS := strings.TrimSpace(activeWorkspace)
	targetWS := normalizeWorkspaceKey(cleanWS)

	// Score memories to prioritize active workspace + global memories (skip unrelated workspaces)
	type scored struct {
		entry MemoryEntry
		prio  int
	}

	var list []scored
	for _, entry := range entries {
		ws := normalizeWorkspaceKey(entry.Workspace)
		if ws == "" || ws == "global" {
			list = append(list, scored{entry: entry, prio: 10})
		} else if targetWS != "" && ws == targetWS {
			list = append(list, scored{entry: entry, prio: 20})
		}
	}

	if len(list) == 0 {
		return "Cross-Session Memory: ENABLED (~/.unjess/memory/memories.json).\nYou HAVE persistent memory across sessions via the memory_save, memory_recall, and memory_delete tools.\n"
	}

	// Sort by priority desc, then by UpdatedAt desc
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].prio != list[j].prio {
			return list[i].prio > list[j].prio
		}
		return list[i].entry.UpdatedAt > list[j].entry.UpdatedAt
	})

	if maxEntries > 12 {
		maxEntries = 12
	}
	if len(list) > maxEntries {
		list = list[:maxEntries]
	}

	var sb strings.Builder
	sb.WriteString("<cross_session_memory>\n")
	sb.WriteString("Persistent memories saved across sessions (use memory_save to add/update, memory_recall to search full content):\n")
	totalBudgetChars := 2200
	usedChars := 0
	for _, item := range list {
		e := item.entry
		cat := e.Category
		if cat == "" {
			cat = "fact"
		}
		content := strings.TrimSpace(e.Content)
		if len(content) > 260 {
			content = content[:260] + "... [use memory_recall for full entry]"
		}
		line := fmt.Sprintf("- [%s] %s: %s\n", cat, e.Key, content)
		if usedChars+len(line) > totalBudgetChars && usedChars > 0 {
			sb.WriteString("- ... [additional memories available via memory_recall]\n")
			break
		}
		sb.WriteString(line)
		usedChars += len(line)
	}
	sb.WriteString("</cross_session_memory>\n")

	return sb.String()
}
