package updater

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		v1       string
		v2       string
		expected int
	}{
		{"2.0.0", "2.0.0", 0},
		{"v2.0.0", "2.0.0", 0},
		{"2.0.0", "v2.0.0", 0},
		{"V2.0.0", "v2.0.0", 0},
		{"2.0.1", "2.0.0", 1},
		{"2.0.0", "2.0.1", -1},
		{"2.1.0", "2.0.9", 1},
		{"2.0.9", "2.1.0", -1},
		{"1.8.0", "2.0.0", -1},
		{"2.0.0", "1.8.0", 1},
		{"1.10.0", "1.9.0", 1},
		{"1.9.0", "1.10.0", -1},
		{"3.0.0-beta", "2.9.9", 1},
	}

	for _, tt := range tests {
		got := CompareVersions(tt.v1, tt.v2)
		if got != tt.expected {
			t.Errorf("CompareVersions(%q, %q) = %d; want %d", tt.v1, tt.v2, got, tt.expected)
		}
	}
}

func TestBuiltinChangelog(t *testing.T) {
	cl := BuiltinChangelog()
	if len(cl) < 4 {
		t.Fatalf("expected at least 4 changelog entries, got %d", len(cl))
	}

	expectedVersions := []string{"2.9.8", "2.9.7", "2.9.6", "2.3.8", "2.3.7", "2.3.5", "2.3.4", "2.3.3", "2.3.2", "2.3.1", "2.3.0", "2.2.4", "2.2.3", "2.2.2", "2.2.1", "2.2.0", "2.1.3", "2.1.2", "2.1.1", "2.1.0", "2.0.3", "2.0.2", "2.0.1", "2.0.0", "1.8.0", "1.7.0", "1.5.0"}
	for i, ver := range expectedVersions {
		if cl[i].Version != ver {
			t.Errorf("entry %d: expected version %s, got %s", i, ver, cl[i].Version)
		}
		if cl[i].Title == "" {
			t.Errorf("entry %d: title is empty", i)
		}
		if cl[i].Tag == "" {
			t.Errorf("entry %d: tag is empty", i)
		}
		if len(cl[i].Changes) == 0 {
			t.Errorf("entry %d: changes list is empty", i)
		}
	}

	if cl[0].Tag != "LATEST" {
		t.Errorf("expected %s to have tag 'LATEST', got %s", cl[0].Version, cl[0].Tag)
	}
}

func TestCheckForUpdatesBuiltinFallback(t *testing.T) {
	// Point to unreachable port to force fallback to builtin
	res := CheckForUpdates("http://127.0.0.1:59999/version.json", true)
	if res["status"] != "ok" {
		t.Errorf("expected status 'ok', got %v", res["status"])
	}
	if res["current_version"] != CurrentVersion {
		t.Errorf("expected current_version %s, got %v", CurrentVersion, res["current_version"])
	}
	if res["latest_version"] != CurrentVersion {
		t.Errorf("expected latest_version %s, got %v", CurrentVersion, res["latest_version"])
	}
	if res["update_available"] != false {
		t.Errorf("expected update_available false, got %v", res["update_available"])
	}
	if res["source"] != "builtin" {
		t.Errorf("expected source 'builtin', got %v", res["source"])
	}

	cl, ok := res["changelog"].([]ChangelogEntry)
	if !ok || len(cl) == 0 {
		t.Fatalf("expected changelog list, got %v", res["changelog"])
	}
	if cl[0].Version != CurrentVersion {
		t.Errorf("expected first changelog version %s, got %s", CurrentVersion, cl[0].Version)
	}
}

func TestCheckForUpdatesRemote(t *testing.T) {
	// Spin up mock update server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		manifest := VersionManifest{
			Version:      "2.9.9",
			PublishedAt:  "2026-11-01",
			Channel:      "stable",
			DownloadURL:  "https://unjess.si/releases/Unjess-2.9.9.exe",
			InstallerURL: "https://unjess.si/releases/Unjess-Installer-2.9.9.exe",
			Notes:        "Added quantum memory indexing and lightning UI updates.",
			Changelog: []ChangelogEntry{
				{
					Version: "2.9.9",
					Date:    "2026-11-01",
					Title:   "Quantum Memory Indexing",
					Tag:     "FEATURE",
					Changes: []string{"Lightning-fast symbol graphs"},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(manifest)
	}))
	defer ts.Close()

	res := CheckForUpdates(ts.URL, true)
	if res["status"] != "ok" {
		t.Errorf("expected status 'ok', got %v", res["status"])
	}
	if res["source"] != "remote" {
		t.Errorf("expected source 'remote', got %v", res["source"])
	}
	if res["latest_version"] != "2.9.9" {
		t.Errorf("expected latest_version '2.9.9', got %v", res["latest_version"])
	}
	if res["update_available"] != true {
		t.Errorf("expected update_available true, got %v", res["update_available"])
	}
	if res["download_url"] != "https://unjess.si/releases/Unjess-2.9.9.exe" {
		t.Errorf("expected download_url to match, got %v", res["download_url"])
	}

	cl, ok := res["changelog"].([]ChangelogEntry)
	if !ok || len(cl) == 0 {
		t.Fatalf("expected changelog list, got %v", res["changelog"])
	}
	if cl[0].Version != "2.9.9" {
		t.Errorf("expected newest version 2.9.9 at index 0, got %s", cl[0].Version)
	}
	if cl[1].Version != CurrentVersion {
		t.Errorf("expected %s at index 1, got %s", CurrentVersion, cl[1].Version)
	}
}

func TestApplyUpdateToTarget(t *testing.T) {
	tmpDir := t.TempDir()
	targetExe := filepath.Join(tmpDir, "Unjess.exe")
	origContent := []byte("ORIGINAL_UNJESS_V2.0.0_BINARY")
	if err := os.WriteFile(targetExe, origContent, 0755); err != nil {
		t.Fatalf("failed to write original binary: %v", err)
	}

	newBinaryBytes := []byte("NEW_UNJESS_V2.1.0_BINARY_PAYLOAD")

	// Spin up mock server serving manifest + binary
	var ts *httptest.Server
	ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/download" {
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(newBinaryBytes)
			return
		}
		manifest := VersionManifest{
			Version:     CurrentVersion,
			DownloadURL: ts.URL + "/download",
			Notes:       "New release",
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(manifest)
	}))
	defer ts.Close()

	res, err := ApplyUpdateToTarget(ts.URL, targetExe)
	if err != nil {
		t.Fatalf("ApplyUpdateToTarget failed: %v", err)
	}

	if res["status"] != "ok" {
		t.Errorf("expected status 'ok', got %v", res["status"])
	}
	if res["updated_to"] != CurrentVersion {
		t.Errorf("expected updated_to '%s', got %v", CurrentVersion, res["updated_to"])
	}
	msg, _ := res["message"].(string)
	if !strings.Contains(msg, "Update downloaded and staged") {
		t.Errorf("expected message to contain 'Update downloaded and staged', got %s", msg)
	}

	// Verify targetExe has new binary content
	stagedBytes, err := os.ReadFile(targetExe)
	if err != nil {
		t.Fatalf("failed to read staged executable: %v", err)
	}
	if string(stagedBytes) != string(newBinaryBytes) {
		t.Errorf("staged binary content mismatch, got %s", string(stagedBytes))
	}

	// Verify .old file has old binary content
	oldExe := targetExe + ".old"
	oldBytes, err := os.ReadFile(oldExe)
	if err != nil {
		t.Fatalf("failed to read .old backup: %v", err)
	}
	if string(oldBytes) != string(origContent) {
		t.Errorf("old binary backup content mismatch, got %s", string(oldBytes))
	}
}

func TestCalculateSHA256(t *testing.T) {
	data := []byte("hello world")
	hash := CalculateSHA256(data)
	expected := "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9"
	if hash != expected {
		t.Errorf("CalculateSHA256 mismatch: got %s, want %s", hash, expected)
	}
}
