package frontier

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestExecuteBrowserUse_Live(t *testing.T) {
	browserExe := findHeadlessBrowser()
	if browserExe == "" {
		t.Skip("Skipping live browser test: Chrome/Edge not found")
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(`<!DOCTYPE html>
<html>
<head><title>Unjess Test Page</title></head>
<body style="font-family: sans-serif; padding: 20px;">
    <h1>Unjess Browser-Use Automation</h1>
    <div>
        <a href="#section" id="link1">Documentation</a>
    </div>
    <div style="margin-top: 15px;">
        <input id="q" type="text" placeholder="Search docs..." />
        <button id="btn" onclick="document.getElementById('status').innerText = 'Query: ' + document.getElementById('q').value">Run Search</button>
    </div>
    <p id="status">Ready</p>
</body>
</html>`))
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()

	// 1. Navigate
	hl := true
	navRes, err := ExecuteBrowserUse(ctx, tmpDir, BrowserUseOptions{
		Action:   "navigate",
		URL:      server.URL,
		Headless: &hl,
	})
	if err != nil {
		t.Fatalf("navigate failed: %v", err)
	}
	if navRes.Title != "Unjess Test Page" {
		t.Errorf("expected Title 'Unjess Test Page', got %q", navRes.Title)
	}
	if len(navRes.Elements) < 3 {
		t.Errorf("expected at least 3 interactive elements, got %d", len(navRes.Elements))
	}
	if !strings.HasPrefix(navRes.VisionDataURI, "data:image/") {
		t.Errorf("expected VisionDataURI to start with 'data:image/', got %q", truncateStr(navRes.VisionDataURI, 30))
	}
	if !strings.Contains(navRes.FormattedState, "Browser-Use State") {
		t.Errorf("expected FormattedState to contain 'Browser-Use State', got %s", navRes.FormattedState)
	}

	// Identify input and button indices from indexed elements
	var inputIdx, btnIdx int
	for _, el := range navRes.Elements {
		if el.Tag == "input" {
			inputIdx = el.Index
		} else if el.Tag == "button" {
			btnIdx = el.Index
		}
	}
	if inputIdx == 0 || btnIdx == 0 {
		t.Fatalf("could not find input (%d) or button (%d) in elements: %+v", inputIdx, btnIdx, navRes.Elements)
	}

	// 2. Type into input
	typeRes, err := ExecuteBrowserUse(ctx, tmpDir, BrowserUseOptions{
		Action: "type",
		Index:  inputIdx,
		Text:   "Antigravity",
	})
	if err != nil {
		t.Fatalf("type failed: %v", err)
	}
	if !strings.Contains(typeRes.ActionSummary, "Antigravity") {
		t.Errorf("expected ActionSummary to contain 'Antigravity', got %q", typeRes.ActionSummary)
	}

	// 3. Click submit button
	clickRes, err := ExecuteBrowserUse(ctx, tmpDir, BrowserUseOptions{
		Action: "click",
		Index:  btnIdx,
	})
	if err != nil {
		t.Fatalf("click failed: %v", err)
	}
	if !strings.Contains(clickRes.PageText, "Query: Antigravity") {
		t.Errorf("expected PageText to contain 'Query: Antigravity', got %q", clickRes.PageText)
	}

	// 4. Evaluate expression
	evalRes, err := ExecuteBrowserUse(ctx, tmpDir, BrowserUseOptions{
		Action: "evaluate",
		Script: "document.getElementById('status').innerText",
	})
	if err != nil {
		t.Fatalf("evaluate failed: %v", err)
	}
	if !strings.Contains(evalRes.EvalOutput, "Query: Antigravity") {
		t.Errorf("expected EvalOutput to contain 'Query: Antigravity', got %q", evalRes.EvalOutput)
	}

	// 5. Scroll and press_key
	_, err = ExecuteBrowserUse(ctx, tmpDir, BrowserUseOptions{
		Action:    "scroll",
		Direction: "down",
		Amount:    100,
	})
	if err != nil {
		t.Errorf("scroll failed: %v", err)
	}

	_, err = ExecuteBrowserUse(ctx, tmpDir, BrowserUseOptions{
		Action: "press_key",
		Key:    "Tab",
	})
	if err != nil {
		t.Errorf("press_key failed: %v", err)
	}

	// 6. Close session
	closeRes, err := ExecuteBrowserUse(ctx, tmpDir, BrowserUseOptions{
		Action: "close",
	})
	if err != nil {
		t.Fatalf("close failed: %v", err)
	}
	if !strings.Contains(closeRes.ActionSummary, "closed") {
		t.Errorf("expected close summary, got %q", closeRes.ActionSummary)
	}
}

func TestRFC6455_FrameEncodingAndDecoding(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	cdp := &cdpConn{
		conn: clientConn,
		br:   bufio.NewReader(clientConn),
	}

	// Test 1: Write masked frame from client and verify server reads unmasked
	testPayload := []byte("hello rfc6455 websocket")
	errChan := make(chan error, 1)

	go func() {
		// Server reads frame from client
		br := bufio.NewReader(serverConn)
		b0, err := br.ReadByte()
		if err != nil {
			errChan <- err
			return
		}
		if b0 != 0x81 {
			t.Errorf("expected 0x81, got 0x%02x", b0)
		}
		b1, err := br.ReadByte()
		if err != nil {
			errChan <- err
			return
		}
		isMasked := (b1 & 0x80) != 0
		if !isMasked {
			t.Errorf("expected client frame to be masked")
		}
		len7 := int(b1 & 0x7F)
		if len7 != len(testPayload) {
			t.Errorf("expected len %d, got %d", len(testPayload), len7)
		}
		var maskKey [4]byte
		for i := 0; i < 4; i++ {
			maskKey[i], _ = br.ReadByte()
		}
		payload := make([]byte, len7)
		for i := 0; i < len7; i++ {
			b, _ := br.ReadByte()
			payload[i] = b ^ maskKey[i%4]
		}
		if string(payload) != string(testPayload) {
			t.Errorf("expected payload %q, got %q", string(testPayload), string(payload))
		}
		errChan <- nil
	}()

	if err := cdp.writeMessage(testPayload); err != nil {
		t.Fatalf("writeMessage failed: %v", err)
	}
	if err := <-errChan; err != nil {
		t.Fatalf("server read error: %v", err)
	}

	// Test 2: Server sends 16-bit length frame to client and client reads it
	largePayload := bytes.Repeat([]byte("A"), 1024)
	go func() {
		header := make([]byte, 4)
		header[0] = 0x81 // FIN + text
		header[1] = 126  // unmasked 16-bit
		binary.BigEndian.PutUint16(header[2:4], uint16(len(largePayload)))
		_, _ = serverConn.Write(header)
		_, _ = serverConn.Write(largePayload)
	}()

	msg, err := cdp.readMessage()
	if err != nil {
		t.Fatalf("readMessage failed: %v", err)
	}
	if len(msg) != len(largePayload) {
		t.Errorf("expected %d bytes, got %d", len(largePayload), len(msg))
	}
}
