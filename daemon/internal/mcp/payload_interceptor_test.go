package mcp

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInterceptAndSavePayloads_JSONAttachments(t *testing.T) {
	tmpDir := t.TempDir()

	// Fake PDF binary data (%PDF-1.4 ...)
	pdfBytes := append([]byte("%PDF-1.4\n%"), make([]byte, 1024)...)
	pdfB64 := base64.StdEncoding.EncodeToString(pdfBytes)

	inputJSON := map[string]any{
		"subject": "Monthly Invoices",
		"from":    "billing@example.com",
		"attachments": []any{
			map[string]any{
				"filename": "invoice_10.pdf",
				"mimeType": "application/pdf",
				"content":  pdfB64,
				"size":     len(pdfBytes),
			},
		},
	}
	rawBytes, _ := json.Marshal(inputJSON)

	out := InterceptAndSavePayloads(tmpDir, string(rawBytes))

	// Verify output is valid JSON
	var parsed map[string]any
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("Intercepted output is not valid JSON: %v", err)
	}

	atts, ok := parsed["attachments"].([]any)
	if !ok || len(atts) != 1 {
		t.Fatalf("Expected 1 attachment, got: %v", parsed["attachments"])
	}
	att := atts[0].(map[string]any)

	// Verify content string was replaced
	contentStr := att["content"].(string)
	if strings.Contains(contentStr, pdfB64[:30]) {
		t.Errorf("Base64 string was NOT replaced! Content: %s", contentStr)
	}
	if !strings.Contains(contentStr, "[Saved to disk:") {
		t.Errorf("Expected '[Saved to disk:', got: %s", contentStr)
	}

	// Verify status and path keys
	if att["status"] != "saved_to_disk" {
		t.Errorf("Expected status 'saved_to_disk', got: %v", att["status"])
	}
	savedPath, _ := att["filePath"].(string)
	if !strings.HasPrefix(savedPath, "downloads/invoice_10") {
		t.Errorf("Expected filePath starting with 'downloads/invoice_10', got: %s", savedPath)
	}

	// Verify file actually exists on disk with identical bytes
	diskFile := filepath.Join(tmpDir, filepath.FromSlash(savedPath))
	readOnDisk, err := os.ReadFile(diskFile)
	if err != nil {
		t.Fatalf("File does not exist on disk: %v", err)
	}
	if len(readOnDisk) != len(pdfBytes) {
		t.Errorf("File size mismatch: expected %d, got %d", len(pdfBytes), len(readOnDisk))
	}
}

func TestInterceptAndSavePayloads_DataURI(t *testing.T) {
	tmpDir := t.TempDir()

	pngBytes := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 256)...)
	pngB64 := base64.StdEncoding.EncodeToString(pngBytes)
	dataURI := "data:image/png;base64," + pngB64

	input := "Here is your screenshot: " + dataURI + " please review."
	out := InterceptAndSavePayloads(tmpDir, input)

	if strings.Contains(out, pngB64[:20]) {
		t.Errorf("Data URI Base64 was NOT replaced! Output: %s", out)
	}
	if !strings.Contains(out, "[Saved attachment to downloads/") {
		t.Errorf("Expected saved attachment note, got: %s", out)
	}

	// Check that a .png file was written in downloads/
	entries, err := os.ReadDir(filepath.Join(tmpDir, "downloads"))
	if err != nil || len(entries) == 0 {
		t.Fatalf("No file created in downloads directory: %v", err)
	}
	if !strings.HasSuffix(entries[0].Name(), ".png") {
		t.Errorf("Expected .png extension, got: %s", entries[0].Name())
	}
}

func TestInterceptAndSavePayloads_NormalTextUnaffected(t *testing.T) {
	tmpDir := t.TempDir()

	plainText := "Hello world! This is just normal text with code:\nfunction add(a, b) { return a + b; }\nNo base64 here."
	out := InterceptAndSavePayloads(tmpDir, plainText)

	if out != plainText {
		t.Errorf("Normal text was unexpectedly modified:\n%s", out)
	}

	normalJSON := `{"name":"Alice","message":"Hello there!","count":42}`
	outJSON := InterceptAndSavePayloads(tmpDir, normalJSON)
	if outJSON != normalJSON {
		t.Errorf("Normal JSON was unexpectedly modified:\n%s", outJSON)
	}
}

func TestManagerCallTool_EndToEndAttachmentIntercept(t *testing.T) {
	tmpDir := t.TempDir()

	pdfBytes := append([]byte("%PDF-1.4\n%header"), make([]byte, 512)...)
	pdfB64 := base64.StdEncoding.EncodeToString(pdfBytes)

	// Mock MCP Server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req RPCRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "initialize":
			_ = json.NewEncoder(w).Encode(RPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result:  json.RawMessage(`{"capabilities":{},"serverInfo":{"name":"mock","version":"1.0"}}`),
			})
		case "tools/list":
			_ = json.NewEncoder(w).Encode(RPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result:  json.RawMessage(`{"tools":[{"name":"get_email","inputSchema":{"type":"object"}}]}`),
			})
		case "tools/call":
			// Server returns content array with JSON text containing attachment
			emailResp := map[string]any{
				"subject": "Attached Contract",
				"attachments": []any{
					map[string]any{
						"filename": "contract.pdf",
						"mimeType": "application/pdf",
						"content":  pdfB64,
					},
				},
			}
			emailJSON, _ := json.Marshal(emailResp)
			_ = json.NewEncoder(w).Encode(RPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result:  json.RawMessage(fmt.Sprintf(`{"content":[{"type":"text","text":%q}]}`, string(emailJSON))),
			})
		}
	}))
	defer ts.Close()

	cfg := map[string]ServerConfig{
		"mock": {
			Name:    "mock",
			URL:     ts.URL,
			Enabled: true,
		},
	}

	mgr := NewManagerWithConfig(cfg, tmpDir)
	defer mgr.DisconnectAll()

	if err := mgr.Connect("mock"); err != nil {
		t.Fatalf("Connect failed: %v", err)
	}

	res, err := mgr.CallTool("mcp_mock_get_email", map[string]any{})
	if err != nil {
		t.Fatalf("CallTool failed: %v", err)
	}

	if strings.Contains(res, pdfB64[:20]) {
		t.Errorf("Result still contains raw Base64! Got: %s", res)
	}
	if !strings.Contains(res, "downloads/contract.pdf") {
		t.Errorf("Result does not contain disk path 'downloads/contract.pdf'! Got: %s", res)
	}

	savedFile := filepath.Join(tmpDir, "downloads", "contract.pdf")
	if _, err := os.Stat(savedFile); err != nil {
		t.Fatalf("File was not saved to disk at %s: %v", savedFile, err)
	}
}
