package worktree

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCowMirrorSandboxLifecycle(t *testing.T) {
	tempWs, err := os.MkdirTemp("", "unjess_worktree_cow_*")
	if err != nil {
		t.Fatalf("failed to create temp ws: %v", err)
	}
	defer os.RemoveAll(tempWs)

	// Create test files in origin
	file1Path := filepath.Join(tempWs, "file1.txt")
	file2Path := filepath.Join(tempWs, "file2.txt")
	if err := os.WriteFile(file1Path, []byte("hello file 1\n"), 0644); err != nil {
		t.Fatalf("failed to write file1: %v", err)
	}
	if err := os.WriteFile(file2Path, []byte("hello file 2\n"), 0644); err != nil {
		t.Fatalf("failed to write file2: %v", err)
	}

	// Create ignored directory and file
	nodeModulesDir := filepath.Join(tempWs, "node_modules", "some_pkg")
	if err := os.MkdirAll(nodeModulesDir, 0755); err != nil {
		t.Fatalf("failed to create node_modules: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nodeModulesDir, "index.js"), []byte("module.exports = {};"), 0644); err != nil {
		t.Fatalf("failed to write index.js: %v", err)
	}

	// 1. Create sandbox
	sb, err := CreateShadowSandbox(tempWs, "cow-test")
	if err != nil {
		t.Fatalf("CreateShadowSandbox failed: %v", err)
	}
	if sb.Mode != ModeCowMirror {
		t.Errorf("expected mode %s, got %s", ModeCowMirror, sb.Mode)
	}
	if sb.Status != StatusActive {
		t.Errorf("expected status %s, got %s", StatusActive, sb.Status)
	}
	if sb.Label != "cow-test" {
		t.Errorf("expected label cow-test, got %s", sb.Label)
	}

	// Verify isolated env vars exist on disk
	for key, path := range sb.EnvVars {
		if fi, err := os.Stat(path); err != nil || !fi.IsDir() {
			t.Errorf("expected isolated cache dir for %s at %s to exist as dir", key, path)
		}
	}

	// Verify ignored file was NOT copied
	ignoredShadow := filepath.Join(sb.ShadowPath, "node_modules", "some_pkg", "index.js")
	if _, err := os.Stat(ignoredShadow); !os.IsNotExist(err) {
		t.Errorf("expected ignored file %s to NOT exist in shadow", ignoredShadow)
	}

	// 2. Perform changes in shadow
	shadowFile1 := filepath.Join(sb.ShadowPath, "file1.txt")
	if err := os.WriteFile(shadowFile1, []byte("hello file 1 - modified in shadow\n"), 0644); err != nil {
		t.Fatalf("failed modifying shadow file1: %v", err)
	}

	shadowCreated := filepath.Join(sb.ShadowPath, "newfile.txt")
	if err := os.WriteFile(shadowCreated, []byte("created in shadow\n"), 0644); err != nil {
		t.Fatalf("failed creating shadow newfile: %v", err)
	}

	shadowFile2 := filepath.Join(sb.ShadowPath, "file2.txt")
	if err := os.Remove(shadowFile2); err != nil {
		t.Fatalf("failed deleting shadow file2: %v", err)
	}

	// Create file in ignored dir in shadow (should not be in diffs)
	_ = os.MkdirAll(filepath.Join(sb.ShadowPath, "node_modules"), 0755)
	_ = os.WriteFile(filepath.Join(sb.ShadowPath, "node_modules", "shadow.js"), []byte("ignored"), 0644)

	// 3. Compute Diffs
	modified, created, deleted, err := ComputeSandboxDiffs(sb)
	if err != nil {
		t.Fatalf("ComputeSandboxDiffs failed: %v", err)
	}

	if len(modified) != 1 || modified[0] != "file1.txt" {
		t.Errorf("expected modified=[file1.txt], got %v", modified)
	}
	if len(created) != 1 || created[0] != "newfile.txt" {
		t.Errorf("expected created=[newfile.txt], got %v", created)
	}
	if len(deleted) != 1 || deleted[0] != "file2.txt" {
		t.Errorf("expected deleted=[file2.txt], got %v", deleted)
	}

	// 4. Promote Sandbox
	applied, err := PromoteSandbox(sb)
	if err != nil {
		t.Fatalf("PromoteSandbox failed: %v", err)
	}
	if len(applied) != 3 {
		t.Errorf("expected 3 applied files, got %v", applied)
	}
	if sb.Status != StatusPromoted {
		t.Errorf("expected status %s, got %s", StatusPromoted, sb.Status)
	}

	// Verify changes applied to origin
	f1Content, err := os.ReadFile(file1Path)
	if err != nil || !strings.Contains(string(f1Content), "modified in shadow") {
		t.Errorf("origin file1 was not updated: %s", string(f1Content))
	}
	if _, err := os.Stat(filepath.Join(tempWs, "newfile.txt")); err != nil {
		t.Errorf("origin newfile.txt does not exist after promote: %v", err)
	}
	if _, err := os.Stat(file2Path); !os.IsNotExist(err) {
		t.Errorf("origin file2.txt was not deleted after promote")
	}

	// 5. Discard Sandbox
	shadowPath := sb.ShadowPath
	envPath := sb.EnvCacheDir
	if err := DiscardSandbox(sb); err != nil {
		t.Fatalf("DiscardSandbox failed: %v", err)
	}
	if sb.Status != StatusDiscarded {
		t.Errorf("expected status %s, got %s", StatusDiscarded, sb.Status)
	}
	if _, err := os.Stat(shadowPath); !os.IsNotExist(err) {
		t.Errorf("shadow path %s was not deleted on discard", shadowPath)
	}
	if _, err := os.Stat(envPath); !os.IsNotExist(err) {
		t.Errorf("env cache path %s was not deleted on discard", envPath)
	}
}

func TestGitWorktreeSandboxLifecycle(t *testing.T) {
	// Skip if git is not available
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed, skipping git worktree test")
	}

	tempWs, err := os.MkdirTemp("", "unjess_worktree_git_*")
	if err != nil {
		t.Fatalf("failed to create temp git ws: %v", err)
	}
	defer os.RemoveAll(tempWs)

	// Initialize git repo
	runCmd := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = tempWs
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git command %v failed: %v (%s)", args, err, string(out))
		}
	}

	runCmd("init")
	runCmd("config", "user.name", "Unjess Test")
	runCmd("config", "user.email", "test@unjess.dev")

	// Commit initial file
	trackedFile := filepath.Join(tempWs, "tracked.txt")
	if err := os.WriteFile(trackedFile, []byte("tracked initial\n"), 0644); err != nil {
		t.Fatalf("failed writing tracked.txt: %v", err)
	}
	runCmd("add", "tracked.txt")
	runCmd("commit", "-m", "initial commit")

	// Create dirty uncommitted files:
	// 1. modified tracked file
	if err := os.WriteFile(trackedFile, []byte("tracked dirty modification\n"), 0644); err != nil {
		t.Fatalf("failed modifying tracked.txt: %v", err)
	}
	// 2. untracked new file
	untrackedFile := filepath.Join(tempWs, "untracked.txt")
	if err := os.WriteFile(untrackedFile, []byte("untracked file content\n"), 0644); err != nil {
		t.Fatalf("failed writing untracked.txt: %v", err)
	}

	// Create shadow sandbox
	sb, err := CreateShadowSandbox(tempWs, "git-worktree-test")
	if err != nil {
		t.Fatalf("CreateShadowSandbox failed: %v", err)
	}
	if sb.Mode != ModeGitWorktree {
		t.Errorf("expected mode %s, got %s", ModeGitWorktree, sb.Mode)
	}

	// Verify that dirty overlay copied uncommitted changes into shadow worktree
	shadowTracked := filepath.Join(sb.ShadowPath, "tracked.txt")
	content, err := os.ReadFile(shadowTracked)
	if err != nil || !strings.Contains(string(content), "tracked dirty modification") {
		t.Errorf("dirty modification was not overlaid in shadow worktree: %s", string(content))
	}

	shadowUntracked := filepath.Join(sb.ShadowPath, "untracked.txt")
	uContent, err := os.ReadFile(shadowUntracked)
	if err != nil || !strings.Contains(string(uContent), "untracked file content") {
		t.Errorf("untracked file was not overlaid in shadow worktree: %s", string(uContent))
	}

	// Add a new file in shadow worktree
	shadowNew := filepath.Join(sb.ShadowPath, "feature.go")
	if err := os.WriteFile(shadowNew, []byte("package feature\n"), 0644); err != nil {
		t.Fatalf("failed writing feature.go in shadow: %v", err)
	}

	// Compute diffs
	modified, created, deleted, err := ComputeSandboxDiffs(sb)
	if err != nil {
		t.Fatalf("ComputeSandboxDiffs failed: %v", err)
	}
	if len(created) != 1 || created[0] != "feature.go" {
		t.Errorf("expected created=[feature.go], got %v", created)
	}
	if len(modified) != 0 {
		t.Errorf("expected 0 modified, got %v", modified)
	}
	if len(deleted) != 0 {
		t.Errorf("expected 0 deleted, got %v", deleted)
	}

	// Promote sandbox
	applied, err := PromoteSandbox(sb)
	if err != nil {
		t.Fatalf("PromoteSandbox failed: %v", err)
	}
	if len(applied) != 1 || applied[0] != "feature.go" {
		t.Errorf("expected applied=[feature.go], got %v", applied)
	}
	if _, err := os.Stat(filepath.Join(tempWs, "feature.go")); err != nil {
		t.Errorf("feature.go not found in origin after promote: %v", err)
	}

	// Discard sandbox
	shadowPath := sb.ShadowPath
	if err := DiscardSandbox(sb); err != nil {
		t.Fatalf("DiscardSandbox failed: %v", err)
	}
	if _, err := os.Stat(shadowPath); !os.IsNotExist(err) {
		t.Errorf("shadow path was not deleted after discard: %s", shadowPath)
	}
}

func TestSpeculativeCandidateAndWinner(t *testing.T) {
	// Candidate 1: Passed, 3 files changed, fast (2.0s)
	c1 := &SpeculativeCandidate{
		Index:        0,
		StrategyName: "fast_pass",
		BuildPassed:  true,
		FilesChanged: 3,
		DurationSec:  2.0,
		ResultText:   "Built successfully",
	}

	// Candidate 2: Passed, 5 files changed, slower (15.0s)
	c2 := &SpeculativeCandidate{
		Index:        1,
		StrategyName: "slow_pass",
		BuildPassed:  true,
		FilesChanged: 5,
		DurationSec:  15.0,
		ResultText:   "All tests green",
	}

	// Candidate 3: Failed build
	c3 := &SpeculativeCandidate{
		Index:        2,
		StrategyName: "fail_build",
		BuildPassed:  false,
		FilesChanged: 2,
		DurationSec:  1.0,
		ResultText:   "Syntax error",
	}

	ScoreCandidate(c1)
	ScoreCandidate(c2)
	ScoreCandidate(c3)

	// Score c1: 1000 + 200 - 2 = 1198
	if c1.Score != 1198 {
		t.Errorf("expected c1 score 1198, got %d", c1.Score)
	}
	// Score c2: 1000 + 200 - 15 = 1185
	if c2.Score != 1185 {
		t.Errorf("expected c2 score 1185, got %d", c2.Score)
	}
	// Score c3: 0 + 200 - 1 = 199
	if c3.Score != 199 {
		t.Errorf("expected c3 score 199, got %d", c3.Score)
	}

	candidates := []*SpeculativeCandidate{c3, c2, c1}
	winner := PickSpeculativeWinner(candidates)

	if winner == nil {
		t.Fatalf("expected non-nil winner")
	}
	if winner.StrategyName != "fast_pass" {
		t.Errorf("expected fast_pass to win, got %s", winner.StrategyName)
	}
	if !c1.Winner {
		t.Errorf("expected c1.Winner to be true")
	}
	if c2.Winner || c3.Winner {
		t.Errorf("expected losers to have Winner == false")
	}

	// Empty candidate slice
	if PickSpeculativeWinner(nil) != nil {
		t.Errorf("expected nil winner for nil slice")
	}
}

func TestParseUnifiedDiffHunks(t *testing.T) {
	diffText := `diff --git a/main.go b/main.go
index 1111111..2222222 100644
--- a/main.go
+++ b/main.go
@@ -10,5 +10,6 @@ func main() {
 	fmt.Println("start")
-	fmt.Println("old")
+	fmt.Println("new line 1")
+	fmt.Println("new line 2")
 	fmt.Println("end")
@@ -30,3 +31,3 @@ func other() {
 	var x = 1
-	var y = 2
+	var y = 20
 	var z = 3
`
	hunks := ParseUnifiedDiffHunks(diffText)
	if len(hunks) != 2 {
		t.Fatalf("expected 2 hunks, got %d", len(hunks))
	}

	// Verify Hunk 0
	h0 := hunks[0]
	if h0.Index != 0 {
		t.Errorf("expected index 0, got %d", h0.Index)
	}
	if h0.OldStart != 10 || h0.OldLines != 5 || h0.NewStart != 10 || h0.NewLines != 6 {
		t.Errorf("h0 line bounds mismatch: Old=%d,%d New=%d,%d", h0.OldStart, h0.OldLines, h0.NewStart, h0.NewLines)
	}
	if h0.Additions != 2 || h0.Deletions != 1 {
		t.Errorf("expected 2 additions and 1 deletion, got +%d -%d", h0.Additions, h0.Deletions)
	}

	// Verify Hunk 1
	h1 := hunks[1]
	if h1.Index != 1 {
		t.Errorf("expected index 1, got %d", h1.Index)
	}
	if h1.OldStart != 30 || h1.OldLines != 3 || h1.NewStart != 31 || h1.NewLines != 3 {
		t.Errorf("h1 line bounds mismatch: Old=%d,%d New=%d,%d", h1.OldStart, h1.OldLines, h1.NewStart, h1.NewLines)
	}
	if h1.Additions != 1 || h1.Deletions != 1 {
		t.Errorf("expected 1 addition and 1 deletion, got +%d -%d", h1.Additions, h1.Deletions)
	}
}

func TestRevertHunkInFile(t *testing.T) {
	tempWs, err := os.MkdirTemp("", "unjess_revert_test_*")
	if err != nil {
		t.Fatalf("failed to create temp ws: %v", err)
	}
	defer os.RemoveAll(tempWs)

	fileLines := []string{
		"line 1",
		"line 2",
		"modified line 3",
		"line 4",
		"line 5",
		"line 6",
		"line 7",
		"line 8",
		"modified line 9",
		"line 10",
	}

	filePath := filepath.Join(tempWs, "sample.txt")
	if err := os.WriteFile(filePath, []byte(strings.Join(fileLines, "\n")+"\n"), 0644); err != nil {
		t.Fatalf("failed writing sample.txt: %v", err)
	}

	diffText := `diff --git a/sample.txt b/sample.txt
--- a/sample.txt
+++ b/sample.txt
@@ -2,3 +2,3 @@
 line 2
-original line 3
+modified line 3
 line 4
@@ -8,3 +8,3 @@
 line 8
-original line 9
+modified line 9
 line 10
`

	// Revert first hunk (index 0)
	remDiff, err := RevertHunkInFile(tempWs, "sample.txt", 0, diffText)
	if err != nil {
		t.Fatalf("RevertHunkInFile failed: %v", err)
	}

	content, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("failed reading file: %v", err)
	}

	linesAfterRevert := strings.Split(strings.TrimRight(string(content), "\n"), "\n")
	if linesAfterRevert[2] != "original line 3" {
		t.Errorf("expected line 3 to be reverted to 'original line 3', got %q", linesAfterRevert[2])
	}
	if linesAfterRevert[8] != "modified line 9" {
		t.Errorf("expected line 9 to remain 'modified line 9', got %q", linesAfterRevert[8])
	}

	// Verify remaining diff only contains the second hunk
	remainingHunks := ParseUnifiedDiffHunks(remDiff)
	if len(remainingHunks) != 1 {
		t.Fatalf("expected 1 remaining hunk, got %d", len(remainingHunks))
	}
	if !strings.Contains(remDiff, "modified line 9") {
		t.Errorf("expected remDiff to contain second hunk, got:\n%s", remDiff)
	}
	if strings.Contains(remDiff, "modified line 3") {
		t.Errorf("expected remDiff to NOT contain first hunk, got:\n%s", remDiff)
	}

	// Revert the remaining hunk (index 0 in remDiff)
	finalDiff, err := RevertHunkInFile(tempWs, "sample.txt", 0, remDiff)
	if err != nil {
		t.Fatalf("RevertHunkInFile on remaining hunk failed: %v", err)
	}

	finalContent, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("failed reading final file: %v", err)
	}

	finalLines := strings.Split(strings.TrimRight(string(finalContent), "\n"), "\n")
	if finalLines[8] != "original line 9" {
		t.Errorf("expected line 9 to be reverted to 'original line 9', got %q", finalLines[8])
	}

	if strings.TrimSpace(finalDiff) != "" {
		t.Errorf("expected empty finalDiff after all hunks reverted, got %q", finalDiff)
	}
}

func TestRevertHunkInFile_ErrorCases(t *testing.T) {
	tempWs, err := os.MkdirTemp("", "unjess_revert_err_*")
	if err != nil {
		t.Fatalf("failed to create temp ws: %v", err)
	}
	defer os.RemoveAll(tempWs)

	diffText := `@@ -1,2 +1,2 @@
-foo
+bar
`
	// Index out of bounds
	_, err = RevertHunkInFile(tempWs, "foo.txt", 5, diffText)
	if err == nil {
		t.Errorf("expected error for out-of-bounds hunkIndex")
	}

	// File does not exist
	_, err = RevertHunkInFile(tempWs, "nonexistent.txt", 0, diffText)
	if err == nil {
		t.Errorf("expected error for nonexistent file")
	}
}

func TestRevertHunkInFile_MultiFile(t *testing.T) {
	tempWs, err := os.MkdirTemp("", "unjess_multifile_*")
	if err != nil {
		t.Fatalf("failed to create temp ws: %v", err)
	}
	defer os.RemoveAll(tempWs)

	fileA := filepath.Join(tempWs, "a.txt")
	fileB := filepath.Join(tempWs, "b.txt")
	_ = os.WriteFile(fileA, []byte("hello alpha\nmodified line A\n"), 0644)
	_ = os.WriteFile(fileB, []byte("hello beta\nmodified line B\n"), 0644)

	diffText := `diff --git a/a.txt b/a.txt
--- a/a.txt
+++ b/a.txt
@@ -1,2 +1,2 @@
 hello alpha
-orig line A
+modified line A
diff --git a/b.txt b/b.txt
--- a/b.txt
+++ b/b.txt
@@ -1,2 +1,2 @@
 hello beta
-orig line B
+modified line B
`

	// Revert hunk 0 (file A)
	updatedDiff, err := RevertHunkInFile(tempWs, "a.txt", 0, diffText)
	if err != nil {
		t.Fatalf("RevertHunkInFile on file A failed: %v", err)
	}

	contentA, _ := os.ReadFile(fileA)
	if !strings.Contains(string(contentA), "orig line A") {
		t.Errorf("file A was not reverted: %s", string(contentA))
	}

	// Verify file B was NOT changed
	contentB, _ := os.ReadFile(fileB)
	if !strings.Contains(string(contentB), "modified line B") {
		t.Errorf("file B should have remained modified: %s", string(contentB))
	}

	// Verify updatedDiff contains file B's hunk and headers, but not file A's
	if strings.Contains(updatedDiff, "a.txt") {
		t.Errorf("expected updatedDiff to omit file A, got:\n%s", updatedDiff)
	}
	if !strings.Contains(updatedDiff, "b.txt") {
		t.Errorf("expected updatedDiff to retain file B, got:\n%s", updatedDiff)
	}
}

func TestScoreCandidate_EdgeCases(t *testing.T) {
	if ScoreCandidate(nil) != 0 {
		t.Errorf("expected ScoreCandidate(nil) == 0")
	}

	// 0 files changed -> no 1000 points
	c := &SpeculativeCandidate{
		BuildPassed:  true,
		FilesChanged: 0,
		ResultText:   "No changes",
		DurationSec:  0,
	}
	ScoreCandidate(c)
	if c.Score != 200 {
		t.Errorf("expected score 200, got %d", c.Score)
	}
}

func TestDiscardSandbox_NilSafe(t *testing.T) {
	if err := DiscardSandbox(nil); err != nil {
		t.Errorf("expected DiscardSandbox(nil) to be nil, got: %v", err)
	}
}
