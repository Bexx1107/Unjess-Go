package memory

import (
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func setupTestMemory(t *testing.T) string {
	t.Helper()
	tmpFile := filepath.Join(t.TempDir(), "test_memories.json")
	t.Setenv("UNJESS_MEMORY_PATH", tmpFile)
	return tmpFile
}

func TestMemoryEmptyState(t *testing.T) {
	setupTestMemory(t)

	memories, err := LoadMemories()
	if err != nil {
		t.Fatalf("unexpected error loading empty memories: %v", err)
	}
	if len(memories) != 0 {
		t.Fatalf("expected 0 memories, got %d", len(memories))
	}

	promptStr := FormatForSystemPrompt("", 10)
	if !strings.Contains(promptStr, "Cross-Session Memory: ENABLED") {
		t.Errorf("expected empty format to contain 'Cross-Session Memory: ENABLED', got: %s", promptStr)
	}
}

func TestSaveAndLoadMemory(t *testing.T) {
	setupTestMemory(t)

	// Save new entry
	entry, err := SaveMemory("user_stack_preference", "preference", "Prefers Go backend and React frontend", "global", []string{"stack", "frontend", "backend"})
	if err != nil {
		t.Fatalf("failed saving memory: %v", err)
	}

	if entry.Key != "user_stack_preference" {
		t.Errorf("expected key 'user_stack_preference', got '%s'", entry.Key)
	}
	if entry.Category != "preference" {
		t.Errorf("expected category 'preference', got '%s'", entry.Category)
	}
	if entry.ID == "" {
		t.Errorf("expected non-empty ID")
	}

	// Load entries
	memories, err := LoadMemories()
	if err != nil {
		t.Fatalf("failed loading memories: %v", err)
	}
	if len(memories) != 1 {
		t.Fatalf("expected 1 memory, got %d", len(memories))
	}
	if memories[0].Content != "Prefers Go backend and React frontend" {
		t.Errorf("unexpected content: %s", memories[0].Content)
	}

	// Update existing entry with case-insensitivity
	updated, err := SaveMemory("USER_STACK_PREFERENCE", "preference", "Prefers Go backend and Vue frontend", "global", []string{"stack", "vue"})
	if err != nil {
		t.Fatalf("failed updating memory: %v", err)
	}
	if updated.ID != entry.ID {
		t.Errorf("expected entry ID to remain %s, got %s", entry.ID, updated.ID)
	}

	memories2, err := LoadMemories()
	if err != nil {
		t.Fatalf("failed loading memories: %v", err)
	}
	if len(memories2) != 1 {
		t.Fatalf("expected 1 memory after update, got %d", len(memories2))
	}
	if memories2[0].Content != "Prefers Go backend and Vue frontend" {
		t.Errorf("expected updated content, got: %s", memories2[0].Content)
	}
}

func TestSearchMemories(t *testing.T) {
	setupTestMemory(t)

	_, _ = SaveMemory("mashina_db", "architecture", "Uses SQLite for relational state and JSON files for logs", "C:\\projects\\mashina", []string{"db", "sqlite"})
	_, _ = SaveMemory("coding_style", "preference", "Always use strict typing and no any types", "global", []string{"style", "typescript"})
	_, _ = SaveMemory("deploy_target", "fact", "Deployment server is AWS ECS running in eu-central-1", "global", []string{"deploy", "aws"})

	// Search empty query returns all
	all := SearchMemories("", "", "")
	if len(all) != 3 {
		t.Fatalf("expected 3 memories, got %d", len(all))
	}

	// Search with category filter
	prefOnly := SearchMemories("", "preference", "")
	if len(prefOnly) != 1 || prefOnly[0].Key != "coding_style" {
		t.Fatalf("expected 1 preference memory, got %d", len(prefOnly))
	}

	// Search with query matching key
	mashinaRes := SearchMemories("mashina", "", "")
	if len(mashinaRes) != 1 || mashinaRes[0].Key != "mashina_db" {
		t.Fatalf("expected mashina_db, got %+v", mashinaRes)
	}

	// Search with query matching content
	sqliteRes := SearchMemories("sqlite", "", "")
	if len(sqliteRes) != 1 || sqliteRes[0].Key != "mashina_db" {
		t.Fatalf("expected sqlite match on mashina_db, got %+v", sqliteRes)
	}

	// Search with query matching tag
	awsRes := SearchMemories("aws", "", "")
	if len(awsRes) != 1 || awsRes[0].Key != "deploy_target" {
		t.Fatalf("expected aws match on deploy_target, got %+v", awsRes)
	}
}

func TestDeleteMemory(t *testing.T) {
	setupTestMemory(t)

	_, _ = SaveMemory("note1", "fact", "content 1", "global", nil)
	entry2, _ := SaveMemory("note2", "fact", "content 2", "global", nil)

	// Delete by key
	deleted, err := DeleteMemory("NOTE1")
	if err != nil {
		t.Fatalf("delete error: %v", err)
	}
	if !deleted {
		t.Fatalf("expected deleted to be true")
	}

	// Delete by ID
	deleted2, err := DeleteMemory(entry2.ID)
	if err != nil {
		t.Fatalf("delete error: %v", err)
	}
	if !deleted2 {
		t.Fatalf("expected deleted2 to be true")
	}

	// Delete non-existent
	deleted3, err := DeleteMemory("not_found")
	if err != nil {
		t.Fatalf("delete error: %v", err)
	}
	if deleted3 {
		t.Fatalf("expected deleted3 to be false")
	}

	remaining, _ := LoadMemories()
	if len(remaining) != 0 {
		t.Fatalf("expected 0 remaining memories, got %d", len(remaining))
	}
}

func TestFormatForSystemPrompt(t *testing.T) {
	setupTestMemory(t)

	_, _ = SaveMemory("pref1", "preference", "user likes dark mode", "global", nil)
	_, _ = SaveMemory("proj_local", "project", "building an awesome desktop client", "C:\\my\\app", nil)
	_, _ = SaveMemory("proj_prefix_sibling", "project", "unrelated sibling project app2 secrets", "C:\\my\\app2", nil)
	_, _ = SaveMemory("proj_other", "project", "unrelated Project Y backend details", "C:\\other\\proj", nil)

	// Normalize trailing slash / forward-slash differences while strictly rejecting siblings and unrelated workspaces
	formatted := FormatForSystemPrompt("c:/my/app/", 10)
	if !strings.Contains(formatted, "<cross_session_memory>") {
		t.Errorf("expected <cross_session_memory> tag in:\n%s", formatted)
	}
	if !strings.Contains(formatted, "- [preference] pref1: user likes dark mode") {
		t.Errorf("expected pref1 in output:\n%s", formatted)
	}
	if !strings.Contains(formatted, "- [project] proj_local: building an awesome desktop client") {
		t.Errorf("expected proj_local in output:\n%s", formatted)
	}
	if strings.Contains(formatted, "proj_prefix_sibling") || strings.Contains(formatted, "app2") {
		t.Errorf("prefix sibling app2 leaked into C:\\my\\app prompt:\n%s", formatted)
	}
	if strings.Contains(formatted, "proj_other") || strings.Contains(formatted, "Project Y") {
		t.Errorf("unrelated workspace Project Y leaked into C:\\my\\app prompt:\n%s", formatted)
	}
}

func TestSearchAndSaveWorkspaceIsolation(t *testing.T) {
	setupTestMemory(t)

	// Same key in two workspaces must NOT overwrite each other
	_, err := SaveMemory("architecture", "architecture", "Project X uses Go + SQLite", "/work/proj-x", nil)
	if err != nil {
		t.Fatalf("SaveMemory proj-x failed: %v", err)
	}
	_, err = SaveMemory("architecture", "architecture", "Project Y uses Rust + Postgres", "/work/proj-y", nil)
	if err != nil {
		t.Fatalf("SaveMemory proj-y failed: %v", err)
	}
	_, err = SaveMemory("editor_theme", "preference", "Always monochrome dark", "global", nil)
	if err != nil {
		t.Fatalf("SaveMemory global failed: %v", err)
	}

	all, err := LoadMemories()
	if err != nil {
		t.Fatalf("LoadMemories failed: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("expected 3 isolated memories, got %d: %+v", len(all), all)
	}

	// Querying within proj-x must return proj-x architecture only (never proj-y)
	resX := SearchMemories("Postgres", "", "/work/proj-x")
	if len(resX) != 0 {
		t.Errorf("expected 0 results for Project Y content inside Project X, got %+v", resX)
	}
	resXValid := SearchMemories("architecture", "", "/work/proj-x/")
	if len(resXValid) != 1 || !strings.Contains(resXValid[0].Content, "Project X") {
		t.Errorf("expected only Project X architecture in /work/proj-x/, got %+v", resXValid)
	}
	// Prefix-similar sibling (/work/proj-x2) must NOT match /work/proj-x
	resX2 := SearchMemories("architecture", "", "/work/proj-x2")
	if len(resX2) != 0 {
		t.Errorf("expected 0 results for /work/proj-x inside /work/proj-x2, got %+v", resX2)
	}
}

func TestConcurrentAccess(t *testing.T) {
	setupTestMemory(t)

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			key := "key_" + string(rune('a'+(idx%5)))
			_, _ = SaveMemory(key, "fact", "concurrent content", "global", nil)
			_ = SearchMemories(key, "", "")
			_ = FormatForSystemPrompt("", 10)
		}(i)
	}
	wg.Wait()

	all, err := LoadMemories()
	if err != nil {
		t.Fatalf("failed loading memories after concurrent writes: %v", err)
	}
	if len(all) == 0 {
		t.Fatalf("expected memories to exist after concurrent writes")
	}
}
