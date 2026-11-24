package frontier

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func makeTinyPNG(t *testing.T) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 16, 16))
	img.Set(2, 2, color.RGBA{R: 225, G: 29, B: 72, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("png encode failed: %v", err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func TestNativeBrowserBridgeValidationAndExecution(t *testing.T) {
	defer func() {
		_ = RegisterNativeBrowserBridge("", "", 0, "", "")
	}()

	// 1. Reject non-loopback SSRF bridge URLs
	if err := RegisterNativeBrowserBridge("http://example.com:9999", "tok", 123, "", ""); err == nil {
		t.Fatalf("expected non-loopback bridge_url to be rejected")
	}

	pngB64 := makeTinyPNG(t)
	const secretToken = "test_wv2_token_310"

	// 2. Spin up mock loopback Desktop WebView2 bridge
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Bridge-Token") != secretToken {
			http.Error(w, `{"ok":false,"error":"forbidden"}`, http.StatusForbidden)
			return
		}
		if r.URL.Path == "/health" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":    true,
				"url":   "https://unjess.si/docs",
				"title": "Unjess Docs",
			})
			return
		}
		if r.URL.Path == "/action" {
			var opts BrowserUseOptions
			_ = json.NewDecoder(r.Body).Decode(&opts)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":             true,
				"title":          "Unjess Live Sidebar",
				"url":            opts.URL,
				"action_summary": "Navigated Sidebar Browser to " + opts.URL,
				"page_text":      "Welcome to native WebView2 sidebar browsing.",
				"elements": []InteractiveElement{
					{Index: 1, Tag: "button", Text: "Sign In", X: 120, Y: 48, W: 80, H: 32},
				},
				"screenshot_png_base64": pngB64,
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	if err := RegisterNativeBrowserBridge(srv.URL, secretToken, 4242, "about:blank", ""); err != nil {
		t.Fatalf("RegisterNativeBrowserBridge failed: %v", err)
	}

	st := GetNativeBrowserBridgeStatus()
	if st["connected"] != true {
		t.Fatalf("expected bridge connected=true, got %v", st)
	}
	if st["url"] != "https://unjess.si/docs" {
		t.Fatalf("unexpected health url: %v", st["url"])
	}

	// 3. Verify ExecuteBrowserUse routes to Native Sidebar WebView2 bridge and attaches Vision data URI
	res, err := ExecuteBrowserUse(context.Background(), t.TempDir(), BrowserUseOptions{
		Action: "navigate",
		URL:    "unjess.si/dashboard",
	})
	if err != nil {
		t.Fatalf("ExecuteBrowserUse via native bridge failed: %v", err)
	}
	if res.Engine != "native-webview2" {
		t.Fatalf("expected engine 'native-webview2', got %q", res.Engine)
	}
	if res.URL != "https://unjess.si/dashboard" {
		t.Fatalf("expected normalized https URL, got %q", res.URL)
	}
	if len(res.Elements) != 1 || res.Elements[0].Text != "Sign In" {
		t.Fatalf("unexpected elements: %+v", res.Elements)
	}
	if res.VisionDataURI == "" {
		t.Fatalf("expected VisionDataURI to be generated from native screenshot")
	}
	if !strings.Contains(res.FormattedState, "Native WebView2") {
		t.Fatalf("expected FormattedState to mention Native WebView2, got:\n%s", res.FormattedState)
	}
}
