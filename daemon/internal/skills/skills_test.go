package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFrontmatterParsing(t *testing.T) {
	md := `---
name: my-test-skill
description: A helpful test skill
triggers:
  - test
  - unit test
repo_url: https://github.com/test/my-test-skill
---

# Instructions

These are the instructions for my test skill.
`
	meta, body := parseYAMLFrontmatter(md)
	if meta == nil {
		t.Fatalf("expected non-nil meta")
	}
	if meta["name"] != "my-test-skill" {
		t.Errorf("expected name 'my-test-skill', got %v", meta["name"])
	}
	if meta["description"] != "A helpful test skill" {
		t.Errorf("expected description 'A helpful test skill', got %v", meta["description"])
	}
	triggers := extractTriggers(meta["triggers"])
	if len(triggers) != 2 || triggers[0] != "test" || triggers[1] != "unit test" {
		t.Errorf("expected 2 triggers ['test', 'unit test'], got %v", triggers)
	}
	if !strings.Contains(body, "# Instructions") {
		t.Errorf("expected body to contain '# Instructions', got %q", body)
	}
}

func TestEnsureBuiltinSkillsSeeded(t *testing.T) {
	if err := EnsureBuiltinSkillsSeeded(); err != nil {
		t.Fatalf("EnsureBuiltinSkillsSeeded failed: %v", err)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir failed: %v", err)
	}

	reaPath := filepath.Join(home, ".unjess", "skills", "reverse-engineer-anything", "SKILL.md")
	if _, err := os.Stat(reaPath); err != nil {
		t.Fatalf("expected seeded file at %s, got error: %v", reaPath, err)
	}
}

func TestDiscoverSkills(t *testing.T) {
	tmpWs := t.TempDir()
	wsSkillDir := filepath.Join(tmpWs, ".agents", "skills", "ws-skill")
	if err := os.MkdirAll(wsSkillDir, 0755); err != nil {
		t.Fatalf("failed creating ws dir: %v", err)
	}

	wsSkillContent := `---
name: ws-skill
description: Workspace-level custom skill
triggers:
  - workspace test
---

Workspace skill instructions.
`
	if err := os.WriteFile(filepath.Join(wsSkillDir, "SKILL.md"), []byte(wsSkillContent), 0644); err != nil {
		t.Fatalf("failed writing ws skill: %v", err)
	}

	skills := DiscoverSkills(tmpWs)
	if len(skills) == 0 {
		t.Fatalf("expected discovered skills, got 0")
	}

	// 1. First skill MUST be reverse-engineer-anything
	if skills[0].Name != "reverse-engineer-anything" {
		t.Errorf("expected first skill to be 'reverse-engineer-anything', got %q", skills[0].Name)
	}
	if skills[0].Scope != "preinstalled" {
		t.Errorf("expected REA scope to be 'preinstalled', got %q", skills[0].Scope)
	}
	if skills[0].RepoURL != "https://github.com/morluto/rea" {
		t.Errorf("expected REA repo_url to be 'https://github.com/morluto/rea', got %q", skills[0].RepoURL)
	}

	// 2. ws-skill should have scope "workspace"
	foundWs := false
	for _, s := range skills {
		if s.Name == "ws-skill" {
			foundWs = true
			if s.Scope != "workspace" {
				t.Errorf("expected ws-skill scope to be 'workspace', got %q", s.Scope)
			}
		}
	}
	if !foundWs {
		t.Errorf("expected ws-skill to be discovered in workspace %s", tmpWs)
	}
}

func TestSaveAndDeleteSkill(t *testing.T) {
	tmpWs := t.TempDir()

	req := SkillSaveRequest{
		Name:         "unit-test-skill",
		Description:  "Skill created by unit test",
		Triggers:     []string{"save test"},
		Instructions: "Instructions for unit test skill.",
		Scope:        "workspace",
		RepoURL:      "https://github.com/test/repo",
		Workspace:    tmpWs,
	}

	info, err := SaveSkill(req)
	if err != nil {
		t.Fatalf("SaveSkill failed: %v", err)
	}

	if info.Name != "unit-test-skill" {
		t.Errorf("expected name 'unit-test-skill', got %q", info.Name)
	}
	if info.Scope != "workspace" {
		t.Errorf("expected scope 'workspace', got %q", info.Scope)
	}

	// Verify file on disk
	skillPath := filepath.Join(info.Path, "SKILL.md")
	if _, err := os.Stat(skillPath); err != nil {
		t.Fatalf("expected SKILL.md to exist at %s", skillPath)
	}

	// Delete skill
	deleted, err := DeleteSkill("unit-test-skill", "", tmpWs)
	if err != nil {
		t.Fatalf("DeleteSkill failed: %v", err)
	}
	if len(deleted) == 0 {
		t.Errorf("expected at least 1 deleted path, got %d", len(deleted))
	}

	// Verify it's gone
	if _, err := os.Stat(info.Path); !os.IsNotExist(err) {
		t.Errorf("expected %s to be deleted", info.Path)
	}
}

func TestInstallFromGitHubREA(t *testing.T) {
	res, err := InstallFromGitHub("https://github.com/morluto/rea")
	if err != nil {
		t.Fatalf("InstallFromGitHub failed: %v", err)
	}

	if res["status"] != "ok" {
		t.Errorf("expected status 'ok', got %v", res["status"])
	}

	installed, ok := res["installed"].([]string)
	if !ok || len(installed) != 1 || installed[0] != "reverse-engineer-anything" {
		t.Errorf("expected installed ['reverse-engineer-anything'], got %v", res["installed"])
	}
}

func TestMatchAndCatalog(t *testing.T) {
	testSkills := []SkillInfo{
		{
			Name:        "reverse-engineer-anything",
			Description: "Reverse engineer native and managed binaries with REA",
			Triggers:    []string{"reverse engineer", "rea", "decompile"},
		},
		{
			Name:        "git-helper",
			Description: "Helps with git rebasing and merge conflicts and code maintenance",
			Triggers:    []string{"git rebase", "merge conflict"},
		},
		{
			Name:        "caveman",
			Description: "Caveman mode skill",
			Triggers:    []string{"caveman", "terse"},
		},
		{
			Name:        "ponytail",
			Description: "Ponytail mode skill",
			Triggers:    []string{"ponytail", "lazy dev"},
		},
	}

	matches := Match(testSkills, "Can you help me reverse engineer this binary?")
	if len(matches) == 0 || matches[0].Name != "reverse-engineer-anything" {
		t.Errorf("expected 'reverse-engineer-anything' to match, got %v", matches)
	}

	// Verify generic prompt with words like "code" does NOT match
	genericMatches := Match(testSkills, "Write a function in Go with clean code")
	if len(genericMatches) != 0 {
		t.Errorf("expected 0 matches for generic prompt, got %d: %v", len(genericMatches), genericMatches)
	}

	// Verify caveman and ponytail are filtered out from Match and Catalog
	cavemanMatches := Match(testSkills, "activate caveman and ponytail mode please")
	for _, m := range cavemanMatches {
		if m.Name == "caveman" || m.Name == "ponytail" {
			t.Errorf("expected caveman and ponytail to be skipped in Match, found %s", m.Name)
		}
	}

	cat := CatalogForPrompt(testSkills)
	if !strings.Contains(cat, "Available skills:") || !strings.Contains(cat, "reverse-engineer-anything") {
		t.Errorf("catalog missing expected skills: %s", cat)
	}
	if strings.Contains(cat, "caveman:") || strings.Contains(cat, "ponytail:") {
		t.Errorf("catalog should not contain caveman or ponytail: %s", cat)
	}
}

