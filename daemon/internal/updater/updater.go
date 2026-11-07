// Package updater provides automatic update checks, manifest parsing, changelog merging,
// and safe binary staging for Unjess Desktop & Daemon.
package updater

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"unjess-daemon/internal/version"
)

const (
	CurrentVersion   = version.Current
	DefaultUpdateURL = "https://unjess.si/releases/version.json"
)

var (
	updaterMu        sync.RWMutex
	parentDesktopExe string
)

// SetParentExecutable records the known path to the parent Unjess desktop executable.
func SetParentExecutable(path string) {
	updaterMu.Lock()
	defer updaterMu.Unlock()
	parentDesktopExe = filepath.Clean(path)
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

func init() {
	// Clean up any leftover .old binary from a previous update on startup
	if exe, err := os.Executable(); err == nil {
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			oldExe := resolved + ".old"
			if _, err := os.Stat(oldExe); err == nil {
				_ = os.Remove(oldExe)
			}
		}
	}
}

// ChangelogEntry represents a single version entry in the release changelog.
type ChangelogEntry struct {
	Version string   `json:"version"`
	Date    string   `json:"date"`
	Title   string   `json:"title"`
	Tag     string   `json:"tag"`
	Changes []string `json:"changes"`
}

// VersionManifest defines the structure of version.json hosted on the update server or mirror.
type VersionManifest struct {
	Version      string           `json:"version"`
	PublishedAt  string           `json:"published_at"`
	Channel      string           `json:"channel"`
	DownloadURL  string           `json:"download_url"`
	InstallerURL string           `json:"installer_url"`
	DaemonURL    string           `json:"daemon_url"`
	SHA256       string           `json:"sha256,omitempty"`
	Notes        string           `json:"notes"`
	Changelog    []ChangelogEntry `json:"changelog"`
}

// BuiltinChangelog returns rich, detailed release notes for recent Unjess versions.
func BuiltinChangelog() []ChangelogEntry {
	return []ChangelogEntry{
		{
			Version: "3.0.2",
			Date:    "2026-11-07",
			Title:   "Unjess v3.0.2 - Native Lightweight Autonomous Coding Assistant",
			Tag:     "LATEST",
			Changes: []string{
				"Native Go daemon and Rust Wry desktop interface with minimal resource footprint",
				"Full provider support: local offline models (Ollama, LM Studio) and cloud APIs (OpenAI, Anthropic, Google Gemini, DeepSeek, xAI)",
				"Resilient agentic tool execution with automatic recovery and parameter repair",
				"Model Context Protocol (MCP) and custom skills integration",
				"Cross-session persistent memory and autonomous workspace management",
			},
		},
	}
}

// CompareVersions compares two semver version strings v1 and v2.
// It returns 1 if v1 > v2, -1 if v1 < v2, and 0 if equal.
// Leading 'v' or 'V' prefixes and surrounding whitespaces are stripped.
func CompareVersions(v1, v2 string) int {
	clean1 := strings.TrimSpace(v1)
	clean1 = strings.TrimPrefix(clean1, "v")
	clean1 = strings.TrimPrefix(clean1, "V")

	clean2 := strings.TrimSpace(v2)
	clean2 = strings.TrimPrefix(clean2, "v")
	clean2 = strings.TrimPrefix(clean2, "V")

	parts1 := strings.Split(clean1, ".")
	parts2 := strings.Split(clean2, ".")

	maxLen := len(parts1)
	if len(parts2) > maxLen {
		maxLen = len(parts2)
	}

	for i := 0; i < maxLen; i++ {
		var n1, n2 int
		if i < len(parts1) {
			numStr := strings.Split(parts1[i], "-")[0]
			n1, _ = strconv.Atoi(numStr)
		}
		if i < len(parts2) {
			numStr := strings.Split(parts2[i], "-")[0]
			n2, _ = strconv.Atoi(numStr)
		}
		if n1 > n2 {
			return 1
		}
		if n1 < n2 {
			return -1
		}
	}
	return 0
}

// CheckForUpdates queries the update server or local mirror for newer versions and manifests.
func CheckForUpdates(updateURL string, autoUpdateEnabled bool) map[string]any {
	if strings.TrimSpace(updateURL) == "" {
		updateURL = DefaultUpdateURL
	}

	var manifest VersionManifest
	source := "builtin"
	fetched := false

	// 1. Try remote HTTP GET with 4s timeout
	client := &http.Client{Timeout: 4 * time.Second}
	resp, err := client.Get(updateURL)
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			data, readErr := io.ReadAll(resp.Body)
			if readErr == nil {
				data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
				if json.Unmarshal(data, &manifest) == nil && manifest.Version != "" {
					source = "remote"
					fetched = true
				}
			}
		}
	}

	// 2. If remote fetch failed, check local mirror fallback if configured via UNJESS_UPDATE_MIRROR_DIR
	if !fetched {
		if mirrorDir := strings.TrimSpace(os.Getenv("UNJESS_UPDATE_MIRROR_DIR")); mirrorDir != "" {
			mirrorPath := filepath.Join(mirrorDir, "version.json")
			if data, readErr := os.ReadFile(mirrorPath); readErr == nil {
				data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
				if json.Unmarshal(data, &manifest) == nil && manifest.Version != "" {
					source = "local_mirror"
					fetched = true
				}
			}
		}
	}

	// If no manifest found, default latest version to CurrentVersion
	latestVersion := CurrentVersion
	if fetched && manifest.Version != "" {
		latestVersion = manifest.Version
	}
	if CompareVersions(CurrentVersion, latestVersion) > 0 {
		latestVersion = CurrentVersion
	}

	updateAvailable := CompareVersions(latestVersion, CurrentVersion) > 0

	manifest.Notes = sanitizeMojibake(manifest.Notes)

	// Merge changelog entries from manifest and builtin (deduplicating by version)
	seen := make(map[string]bool)
	var mergedChangelog []ChangelogEntry
	for _, entry := range manifest.Changelog {
		if entry.Version != "" && !seen[entry.Version] {
			seen[entry.Version] = true
			entry.Title = sanitizeMojibake(entry.Title)
			for ci := range entry.Changes {
				entry.Changes[ci] = sanitizeMojibake(entry.Changes[ci])
			}
			mergedChangelog = append(mergedChangelog, entry)
		}
	}
	for _, entry := range BuiltinChangelog() {
		if entry.Version != "" && !seen[entry.Version] {
			seen[entry.Version] = true
			entry.Title = sanitizeMojibake(entry.Title)
			for ci := range entry.Changes {
				entry.Changes[ci] = sanitizeMojibake(entry.Changes[ci])
			}
			mergedChangelog = append(mergedChangelog, entry)
		}
	}

	// Sort merged changelog strictly descending by version so the newest is always on top!
	sort.SliceStable(mergedChangelog, func(i, j int) bool {
		return CompareVersions(mergedChangelog[i].Version, mergedChangelog[j].Version) > 0
	})

	// Ensure the newest entry has Tag = "LATEST" and older entries do NOT retain "LATEST"
	for i := range mergedChangelog {
		if i == 0 {
			if mergedChangelog[i].Tag == "" || mergedChangelog[i].Tag == "UPDATE" {
				mergedChangelog[i].Tag = "LATEST"
			}
		} else {
			if mergedChangelog[i].Tag == "LATEST" {
				mergedChangelog[i].Tag = "UPDATE"
			}
		}
	}

	return map[string]any{
		"status":              "ok",
		"current_version":     CurrentVersion,
		"latest_version":      latestVersion,
		"update_available":    updateAvailable,
		"auto_update_enabled": autoUpdateEnabled,
		"update_server_url":   updateURL,
		"download_url":        manifest.DownloadURL,
		"installer_url":       manifest.InstallerURL,
		"daemon_url":          manifest.DaemonURL,
		"published_at":        manifest.PublishedAt,
		"notes":               manifest.Notes,
		"checked_at":          time.Now().Format("2006-01-02 15:04:05"),
		"source":              source,
		"changelog":           mergedChangelog,
	}
}

// sanitizeMojibake cleans up any double-encoded UTF-8 / Windows-1252 sequences (such as â€” or Ã¢â‚¬â€) in changelog text.
func sanitizeMojibake(s string) string {
	if s == "" {
		return s
	}
	replacer := strings.NewReplacer(
		"\xc3\x83\xc2\xa2\xc3\xa2\xc2\x82\xc2\xac\xc3\xa2\xc2\x80\xc2\x9d", "-",
		"\xc3\x83\xc2\xa2\xc3\xa2\xc2\x82\xc2\xac\xc3\xa2\xc2\x80\xc2\x9c", "-",
		"\xc3\xa2\xc2\x80\xc2\x94", "-",
		"\xc3\xa2\xc2\x80\xc2\x93", "-",
		"\xc3\xa2\xc2\x80\xc2\xa2", "-",
		"Ã¢â‚¬â€\"", "-",
		"Ã¢â‚¬â€", "-",
		"â€”", "-",
		"â€“", "-",
		"\ufffd?\"", "-",
		"\ufffd", "",
	)
	out := replacer.Replace(s)
	out = strings.ReplaceAll(out, "  -  ", " - ")
	return out
}

// stageBinary downloads or reads binary bytes and safely writes to targetPath via .old rename.
func stageBinary(sourceURL string, targetPath string, localMirrorFileName string) error {
	var newBytes []byte

	// 1. Check local mirror directory if configured via UNJESS_UPDATE_MIRROR_DIR
	if mirrorDir := strings.TrimSpace(os.Getenv("UNJESS_UPDATE_MIRROR_DIR")); mirrorDir != "" {
		mirrorPath := filepath.Join(mirrorDir, localMirrorFileName)
		if data, err := os.ReadFile(mirrorPath); err == nil && len(data) > 0 {
			newBytes = data
		}
	}

	// 2. Download from remote HTTP URL
	if len(newBytes) == 0 && (strings.HasPrefix(sourceURL, "http://") || strings.HasPrefix(sourceURL, "https://")) {
		client := &http.Client{Timeout: 90 * time.Second}
		resp, err := client.Get(sourceURL)
		if err != nil {
			return fmt.Errorf("failed downloading from %s: %w", sourceURL, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("download from %s returned HTTP %d", sourceURL, resp.StatusCode)
		}
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("reading download stream: %w", err)
		}
		newBytes = data
	} else if len(newBytes) == 0 && sourceURL != "" {
		// Local file path fallback
		if data, err := os.ReadFile(sourceURL); err == nil && len(data) > 0 {
			newBytes = data
		}
	}

	if len(newBytes) == 0 {
		return fmt.Errorf("no binary data received from %s", sourceURL)
	}

	// Ensure target directory exists
	if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
		return fmt.Errorf("creating directory for %s: %w", targetPath, err)
	}

	// Rename current target to .old
	oldExe := targetPath + ".old"
	hasOld := false
	if _, err := os.Stat(targetPath); err == nil {
		_ = os.Remove(oldExe) // Remove stale .old
		if err := os.Rename(targetPath, oldExe); err != nil {
			return fmt.Errorf("renaming %s to %s: %w", targetPath, oldExe, err)
		}
		hasOld = true
	}

	// Write new binary
	if err := os.WriteFile(targetPath, newBytes, 0755); err != nil {
		if hasOld {
			_ = os.Rename(oldExe, targetPath)
		}
		return fmt.Errorf("writing new binary to %s: %w", targetPath, err)
	}

	return nil
}

// ApplyUpdate downloads and stages the newest binary release for the current running executable.
func ApplyUpdate(updateURL string) (map[string]any, error) {
	info := CheckForUpdates(updateURL, true)
	latestVersion, _ := info["latest_version"].(string)
	if latestVersion == "" {
		latestVersion = CurrentVersion
	}

	downloadURL, _ := info["download_url"].(string)
	daemonURL, _ := info["daemon_url"].(string)
	installerURL, _ := info["installer_url"].(string)

	isAvailable, _ := info["update_available"].(bool)
	if !isAvailable && CompareVersions(latestVersion, CurrentVersion) <= 0 {
		return map[string]any{
			"status":          "ok",
			"current_version": CurrentVersion,
			"updated_to":      CurrentVersion,
			"restarting":      false,
			"message":         "Unjess is already up to date.",
		}, nil
	}

	desktopExe := FindDesktopExecutable()
	daemonExe, _ := os.Executable()
	if daemonExe != "" {
		if resolved, err := filepath.EvalSymlinks(daemonExe); err == nil {
			daemonExe = resolved
		}
	}

	var updatedAny bool

	// 1. Stage desktop executable if found
	if desktopExe != "" && downloadURL != "" {
		if err := stageBinary(downloadURL, desktopExe, "Unjess.exe"); err == nil {
			updatedAny = true
		}
	}

	// 2. Stage daemon executable
	if daemonExe != "" {
		targetURL := daemonURL
		mirrorName := "unjessd.exe"
		if targetURL == "" {
			targetURL = downloadURL
			mirrorName = "Unjess.exe"
		}
		if targetURL != "" {
			if err := stageBinary(targetURL, daemonExe, mirrorName); err == nil {
				updatedAny = true
			}
		}
	}

	// 3. Ensure sibling daemon & CLI are staged next to desktop executable
	// This permanently eliminates runtime dropper extraction for existing users upgrading via auto-update.
	if desktopExe != "" {
		desktopDir := filepath.Dir(desktopExe)
		if daemonURL != "" {
			siblingDaemon := filepath.Join(desktopDir, "unjessd.exe")
			if filepath.Clean(siblingDaemon) != filepath.Clean(daemonExe) {
				_ = stageBinary(daemonURL, siblingDaemon, "unjessd.exe")
			}
			siblingCli := filepath.Join(desktopDir, "unjess-cli.exe")
			if filepath.Clean(siblingCli) != filepath.Clean(daemonExe) {
				cliURL := strings.Replace(daemonURL, "unjessd.exe", "unjess-cli.exe", 1)
				if cliURL != daemonURL && cliURL != "" {
					_ = stageBinary(cliURL, siblingCli, "unjess-cli.exe")
				}
			}
		}
	}

	if !updatedAny {
		// Fallback: try staging downloadURL or installerURL into daemonExe
		fallbackURL := downloadURL
		if fallbackURL == "" {
			fallbackURL = installerURL
		}
		if daemonExe != "" && fallbackURL != "" {
			if err := stageBinary(fallbackURL, daemonExe, "Unjess.exe"); err != nil {
				return nil, fmt.Errorf("failed to stage update: %w", err)
			}
			updatedAny = true
		} else {
			return nil, fmt.Errorf("no update targets could be staged for version %s", latestVersion)
		}
	}

	restartExe := desktopExe
	if restartExe == "" {
		restartExe = daemonExe
	}

	return map[string]any{
		"status":          "ok",
		"current_version": CurrentVersion,
		"updated_to":      latestVersion,
		"restarting":      true,
		"target_exe":      restartExe,
		"message":         fmt.Sprintf("Update v%s applied successfully. Restarting Unjess...", latestVersion),
	}, nil
}

// ApplyUpdateToTarget stages an update into targetExe using safe Windows rename replacement.
func ApplyUpdateToTarget(updateURL string, targetExe string) (map[string]any, error) {
	info := CheckForUpdates(updateURL, true)
	latestVersion, _ := info["latest_version"].(string)
	if latestVersion == "" {
		latestVersion = CurrentVersion
	}

	downloadURL, _ := info["download_url"].(string)
	if downloadURL == "" {
		downloadURL, _ = info["daemon_url"].(string)
	}
	if downloadURL == "" {
		downloadURL, _ = info["installer_url"].(string)
	}

	if err := stageBinary(downloadURL, targetExe, "Unjess.exe"); err != nil {
		if !info["update_available"].(bool) {
			return map[string]any{
				"status":          "ok",
				"updated_to":      CurrentVersion,
				"message":         "Unjess is already up to date.",
				"current_version": CurrentVersion,
			}, nil
		}
		return nil, err
	}

	return map[string]any{
		"status":     "ok",
		"updated_to": latestVersion,
		"restarting": false,
		"target_exe": targetExe,
		"message":    "Update downloaded and staged. Restart Unjess to complete.",
	}, nil
}

// CalculateSHA256 returns hex-encoded SHA256 checksum of data.
func CalculateSHA256(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}
