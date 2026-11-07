package frontier

import (
	"bufio"
	"bytes"
	"context"
	cryptoRand "crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// BrowserUseOptions defines the input options for executing browser automation actions.
type BrowserUseOptions struct {
	Action     string `json:"action"`      // "navigate", "click", "type", "scroll", "press_key", "evaluate", "extract", "screenshot", "close", "cli"
	URL        string `json:"url"`         // for "navigate"
	Index      int    `json:"index"`       // 1-based [index] of interactive element from browser-use DOM map
	Selector   string `json:"selector"`    // optional CSS selector fallback for click/type
	Text       string `json:"text"`        // text to type (for "type") or CLI command args
	PressEnter bool   `json:"press_enter"` // whether to press Enter after typing
	Direction  string `json:"direction"`   // "down" or "up" (for "scroll")
	Amount     int    `json:"amount"`      // scroll pixels (default 550)
	Key        string `json:"key"`         // key name for "press_key" (e.g. "Enter", "Escape", "Tab", "ArrowDown")
	Script     string `json:"script"`      // JS expression for "evaluate"
	Headless   *bool  `json:"headless"`    // default true (--headless=new), can be set false to show visible browser window
}

// BrowserUseArgs is an alias for BrowserUseOptions.
type BrowserUseArgs = BrowserUseOptions

// InteractiveElement represents an indexed, clickable/typeable DOM node detected by the Set-of-Mark indexer.
type InteractiveElement struct {
	Index int    `json:"index"`
	Tag   string `json:"tag"`
	Type  string `json:"type,omitempty"`
	Role  string `json:"role,omitempty"`
	Text  string `json:"text,omitempty"`
	Href  string `json:"href,omitempty"`
	X     int    `json:"x"`
	Y     int    `json:"y"`
	W     int    `json:"w"`
	H     int    `json:"h"`
}

// BrowserUseResult encapsulates the output, indexed DOM map, and annotated vision screenshot of a browser step.
type BrowserUseResult struct {
	Title          string               `json:"title"`
	URL            string               `json:"url"`
	ActionSummary  string               `json:"action_summary"`
	Elements       []InteractiveElement `json:"elements"`
	PageText       string               `json:"page_text"`
	EvalOutput     string               `json:"eval_output,omitempty"`
	ScreenshotPath string               `json:"screenshot_path,omitempty"`
	VisionDataURI  string               `json:"vision_data_uri,omitempty"`
	VisionReport   string               `json:"vision_report,omitempty"`
	FormattedState string               `json:"formatted_state"`
}

// cdpConn manages a raw RFC-6455 WebSocket connection over net.Conn to a Chrome/Edge DevTools target.
type cdpConn struct {
	conn   net.Conn
	br     *bufio.Reader
	mu     sync.Mutex
	nextID int64
}

// dialCDP performs the HTTP WebSocket handshake and returns an active cdpConn.
func dialCDP(wsURL string, timeout time.Duration) (*cdpConn, error) {
	u, err := url.Parse(wsURL)
	if err != nil {
		return nil, fmt.Errorf("invalid websocket URL %q: %w", wsURL, err)
	}
	host := u.Host
	path := u.RequestURI()
	if path == "" {
		path = "/"
	}

	conn, err := net.DialTimeout("tcp", host, timeout)
	if err != nil {
		return nil, fmt.Errorf("failed to dial websocket %s: %w", host, err)
	}

	nonce := make([]byte, 16)
	if _, err := io.ReadFull(cryptoRand.Reader, nonce); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("failed to generate websocket key: %w", err)
	}
	secKey := base64.StdEncoding.EncodeToString(nonce)

	req := fmt.Sprintf("GET %s HTTP/1.1\r\n"+
		"Host: %s\r\n"+
		"Upgrade: websocket\r\n"+
		"Connection: Upgrade\r\n"+
		"Sec-WebSocket-Key: %s\r\n"+
		"Sec-WebSocket-Version: 13\r\n\r\n", path, host, secKey)

	if _, err := conn.Write([]byte(req)); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("failed to write websocket upgrade: %w", err)
	}

	br := bufio.NewReader(conn)
	statusLine, err := br.ReadString('\n')
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("failed to read websocket handshake response: %w", err)
	}
	if !strings.Contains(statusLine, "101") {
		_ = conn.Close()
		return nil, fmt.Errorf("unexpected websocket handshake status: %s", strings.TrimSpace(statusLine))
	}

	for {
		line, err := br.ReadString('\n')
		if err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("error reading handshake headers: %w", err)
		}
		if strings.TrimRight(line, "\r\n") == "" {
			break
		}
	}

	return &cdpConn{
		conn: conn,
		br:   br,
	}, nil
}

// writeMessage writes an RFC-6455 masked text message frame to the server.
func (c *cdpConn) writeMessage(payload []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	var header []byte
	length := len(payload)
	b0 := byte(0x81) // FIN (0x80) | text opcode (0x1)

	if length < 126 {
		header = []byte{b0, byte(0x80 | length)}
	} else if length <= 65535 {
		header = make([]byte, 4)
		header[0] = b0
		header[1] = byte(0x80 | 126)
		binary.BigEndian.PutUint16(header[2:4], uint16(length))
	} else {
		header = make([]byte, 10)
		header[0] = b0
		header[1] = byte(0x80 | 127)
		binary.BigEndian.PutUint64(header[2:10], uint64(length))
	}

	var mask [4]byte
	if _, err := io.ReadFull(cryptoRand.Reader, mask[:]); err != nil {
		return err
	}

	masked := make([]byte, length)
	for i := 0; i < length; i++ {
		masked[i] = payload[i] ^ mask[i%4]
	}

	if _, err := c.conn.Write(header); err != nil {
		return err
	}
	if _, err := c.conn.Write(mask[:]); err != nil {
		return err
	}
	if _, err := c.conn.Write(masked); err != nil {
		return err
	}
	return nil
}

// writePong responds to incoming Ping frames.
func (c *cdpConn) writePong(payload []byte) {
	length := len(payload)
	if length > 125 {
		length = 125
		payload = payload[:125]
	}
	var mask [4]byte
	_, _ = cryptoRand.Read(mask[:])
	header := []byte{0x8A, byte(0x80 | length)}
	masked := make([]byte, length)
	for i := 0; i < length; i++ {
		masked[i] = payload[i] ^ mask[i%4]
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	_, _ = c.conn.Write(header)
	_, _ = c.conn.Write(mask[:])
	_, _ = c.conn.Write(masked)
}

// readMessage reads an entire WebSocket message, supporting fragmented, 16-bit, and 64-bit frames.
func (c *cdpConn) readMessage() ([]byte, error) {
	var messageBuf bytes.Buffer

	for {
		b0, err := c.br.ReadByte()
		if err != nil {
			return nil, err
		}
		fin := (b0 & 0x80) != 0
		opcode := b0 & 0x0F

		if opcode == 0x8 { // Close frame
			return nil, io.EOF
		}

		b1, err := c.br.ReadByte()
		if err != nil {
			return nil, err
		}
		masked := (b1 & 0x80) != 0
		len7 := int(b1 & 0x7F)

		var payloadLen int64
		if len7 < 126 {
			payloadLen = int64(len7)
		} else if len7 == 126 {
			var b [2]byte
			if _, err := io.ReadFull(c.br, b[:]); err != nil {
				return nil, err
			}
			payloadLen = int64(binary.BigEndian.Uint16(b[:]))
		} else {
			var b [8]byte
			if _, err := io.ReadFull(c.br, b[:]); err != nil {
				return nil, err
			}
			payloadLen = int64(binary.BigEndian.Uint64(b[:]))
		}

		if payloadLen > 100*1024*1024 {
			return nil, fmt.Errorf("websocket frame exceeds 100MB limit: %d bytes", payloadLen)
		}

		var maskKey [4]byte
		if masked {
			if _, err := io.ReadFull(c.br, maskKey[:]); err != nil {
				return nil, err
			}
		}

		frameData := make([]byte, payloadLen)
		if _, err := io.ReadFull(c.br, frameData); err != nil {
			return nil, err
		}

		if masked {
			for i := range frameData {
				frameData[i] ^= maskKey[i%4]
			}
		}

		if opcode == 0x9 { // Ping
			c.writePong(frameData)
			continue
		}
		if opcode == 0xA { // Pong
			continue
		}

		messageBuf.Write(frameData)
		if fin {
			break
		}
	}

	return messageBuf.Bytes(), nil
}

// call sends a JSON-RPC request to CDP and synchronously waits for the response matching the request id.
func (c *cdpConn) call(method string, params map[string]any, timeout time.Duration) (map[string]any, error) {
	id := atomic.AddInt64(&c.nextID, 1)
	reqObj := map[string]any{
		"id":     id,
		"method": method,
	}
	if params != nil {
		reqObj["params"] = params
	}
	reqBytes, err := json.Marshal(reqObj)
	if err != nil {
		return nil, err
	}

	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	_ = c.conn.SetDeadline(time.Now().Add(timeout))
	defer func() {
		_ = c.conn.SetDeadline(time.Time{})
	}()

	if err := c.writeMessage(reqBytes); err != nil {
		return nil, fmt.Errorf("failed to send cdp message (%s): %w", method, err)
	}

	for {
		data, err := c.readMessage()
		if err != nil {
			return nil, fmt.Errorf("failed to read cdp response for %s (id %d): %w", method, id, err)
		}

		var resp struct {
			ID     int64          `json:"id"`
			Method string         `json:"method"`
			Result map[string]any `json:"result"`
			Error  *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}

		if err := json.Unmarshal(data, &resp); err != nil {
			continue
		}

		if resp.ID == id {
			if resp.Error != nil {
				return nil, fmt.Errorf("cdp error in %s: %s (code %d)", method, resp.Error.Message, resp.Error.Code)
			}
			if resp.Result == nil {
				resp.Result = make(map[string]any)
			}
			return resp.Result, nil
		}
	}
}

// BrowserUseSession holds an active browser process and CDP WebSocket connection.
type BrowserUseSession struct {
	cmd        *exec.Cmd
	port       int
	profileDir string
	wsURL      string
	conn       *cdpConn
	headless   bool
	closed     bool
	mu         sync.Mutex
}

var (
	activeBrowserSession *BrowserUseSession
	browserMu            sync.Mutex
)

// Close terminates the CDP connection, kills the browser process, and cleans up temp files.
func (s *BrowserUseSession) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if s.conn != nil && s.conn.conn != nil {
		_ = s.conn.conn.Close()
	}
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
		_ = s.cmd.Wait()
	}
	if s.profileDir != "" {
		_ = os.RemoveAll(s.profileDir)
	}
	return nil
}

// isAlive performs a quick HTTP healthcheck on the CDP version endpoint.
func (s *BrowserUseSession) isAlive() bool {
	if s == nil || s.closed || s.cmd == nil || s.cmd.Process == nil {
		return false
	}
	client := http.Client{Timeout: 600 * time.Millisecond}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/json/version", s.port))
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// call forwards a CDP command through the session's active connection.
func (s *BrowserUseSession) call(method string, params map[string]any, timeout time.Duration) (map[string]any, error) {
	if s.conn == nil {
		return nil, errors.New("cdp connection is closed")
	}
	return s.conn.call(method, params, timeout)
}

// evaluate runs a JavaScript expression via Runtime.evaluate and returns the resulting value.
func (s *BrowserUseSession) evaluate(expression string) (any, error) {
	res, err := s.call("Runtime.evaluate", map[string]any{
		"expression":    expression,
		"returnByValue": true,
		"awaitPromise":  true,
	}, 15*time.Second)
	if err != nil {
		return nil, err
	}
	if ex, ok := res["exceptionDetails"]; ok && ex != nil {
		return nil, fmt.Errorf("js exception: %v", ex)
	}
	if rObj, ok := res["result"].(map[string]any); ok {
		return rObj["value"], nil
	}
	return nil, nil
}

// CloseBrowserSession explicitly terminates the singleton browser session and resets state.
func CloseBrowserSession() error {
	browserMu.Lock()
	defer browserMu.Unlock()
	if activeBrowserSession != nil {
		err := activeBrowserSession.Close()
		activeBrowserSession = nil
		return err
	}
	return nil
}

// getOrCreateBrowserSession returns the running browser session or initializes a new one.
func getOrCreateBrowserSession(headless bool) (*BrowserUseSession, error) {
	browserMu.Lock()
	defer browserMu.Unlock()

	if activeBrowserSession != nil && !activeBrowserSession.closed {
		if activeBrowserSession.headless == headless && activeBrowserSession.isAlive() {
			return activeBrowserSession, nil
		}
		_ = activeBrowserSession.Close()
		activeBrowserSession = nil
	}

	session, err := startBrowserSession(headless)
	if err != nil {
		return nil, err
	}
	activeBrowserSession = session
	return session, nil
}

// startBrowserSession launches Chrome or Edge with remote debugging enabled.
func startBrowserSession(headless bool) (*BrowserUseSession, error) {
	exePath := findHeadlessBrowser()
	if exePath == "" {
		return nil, errors.New("headless browser (chrome or msedge) not found on system; please install Google Chrome or Microsoft Edge")
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("failed to allocate free port: %w", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	profileDir := filepath.Join(os.TempDir(), fmt.Sprintf("unjess_browser_use_%d", port))
	_ = os.RemoveAll(profileDir)
	if err := os.MkdirAll(profileDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create temp profile dir: %w", err)
	}

	args := []string{
		fmt.Sprintf("--remote-debugging-port=%d", port),
		"--remote-allow-origins=*",
		fmt.Sprintf("--user-data-dir=%s", profileDir),
		"--window-size=1280,850",
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-gpu",
		"--hide-scrollbars",
	}
	if headless {
		args = append(args, "--headless=new")
	}

	cmd := exec.Command(exePath, args...)
	if err := cmd.Start(); err != nil {
		_ = os.RemoveAll(profileDir)
		return nil, fmt.Errorf("failed to start browser process: %w", err)
	}

	deadline := time.Now().Add(6 * time.Second)
	var wsURL string
	httpClient := http.Client{Timeout: 600 * time.Millisecond}

	for time.Now().Before(deadline) {
		time.Sleep(150 * time.Millisecond)
		resp, err := httpClient.Get(fmt.Sprintf("http://127.0.0.1:%d/json/list", port))
		if err != nil {
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()

		var targets []struct {
			Type                 string `json:"type"`
			WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
		}
		if json.Unmarshal(body, &targets) == nil {
			for _, t := range targets {
				if t.Type == "page" && t.WebSocketDebuggerURL != "" {
					wsURL = t.WebSocketDebuggerURL
					break
				}
			}
			if wsURL != "" {
				break
			}
		}

		if wsURL == "" {
			req, rErr := http.NewRequest(http.MethodPut, fmt.Sprintf("http://127.0.0.1:%d/json/new?about:blank", port), nil)
			if rErr == nil {
				if putResp, putErr := httpClient.Do(req); putErr == nil {
					putBody, _ := io.ReadAll(putResp.Body)
					_ = putResp.Body.Close()
					var newTarget struct {
						WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
					}
					if json.Unmarshal(putBody, &newTarget) == nil && newTarget.WebSocketDebuggerURL != "" {
						wsURL = newTarget.WebSocketDebuggerURL
						break
					}
				}
			}
		}
	}

	if wsURL == "" {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
		_ = os.RemoveAll(profileDir)
		return nil, fmt.Errorf("browser launched on port %d but failed to return webSocketDebuggerUrl within 6s", port)
	}

	conn, err := dialCDP(wsURL, 5*time.Second)
	if err != nil {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
		_ = os.RemoveAll(profileDir)
		return nil, fmt.Errorf("failed to connect to cdp websocket (%s): %w", wsURL, err)
	}

	session := &BrowserUseSession{
		cmd:        cmd,
		port:       port,
		profileDir: profileDir,
		wsURL:      wsURL,
		conn:       conn,
		headless:   headless,
	}

	_, _ = session.call("Page.enable", nil, 5*time.Second)
	_, _ = session.call("Runtime.enable", nil, 5*time.Second)

	return session, nil
}

const somIndexerScript = `(() => {
	try {
		const old = document.getElementById('__bu_som_container');
		if (old) old.remove();

		const selectors = [
			'a[href]',
			'button',
			'input',
			'textarea',
			'select',
			'summary',
			'[role="button"]',
			'[role="link"]',
			'[role="checkbox"]',
			'[role="radio"]',
			'[role="tab"]',
			'[role="menuitem"]',
			'[onclick]',
			'[tabindex]:not([tabindex="-1"])'
		].join(',');

		const candidates = Array.from(document.querySelectorAll(selectors));
		const vw = window.innerWidth || document.documentElement.clientWidth || 1280;
		const vh = window.innerHeight || document.documentElement.clientHeight || 850;

		const visibleItems = [];
		for (let i = 0; i < candidates.length; i++) {
			const el = candidates[i];
			if (el.id === '__bu_som_container' || (el.closest && el.closest('#__bu_som_container'))) continue;
			if (el.disabled || el.getAttribute('aria-hidden') === 'true') continue;

			const rect = el.getBoundingClientRect();
			if (rect.width < 5 || rect.height < 5) continue;
			if (rect.bottom <= 0 || rect.top >= vh * 1.5 || rect.right <= 0 || rect.left >= vw * 1.5) continue;

			const style = window.getComputedStyle(el);
			if (!style || style.visibility === 'hidden' || style.display === 'none' || style.opacity === '0') continue;

			visibleItems.push({ el: el, rect: rect });
		}

		visibleItems.sort((a, b) => {
			if (Math.abs(a.rect.top - b.rect.top) > 12) {
				return a.rect.top - b.rect.top;
			}
			return a.rect.left - b.rect.left;
		});

		const limited = visibleItems.slice(0, 75);

		const container = document.createElement('div');
		container.id = '__bu_som_container';
		container.setAttribute('style', 'position:fixed;inset:0;pointer-events:none;z-index:2147483647;overflow:hidden;');

		const elementsData = [];

		for (let j = 0; j < limited.length; j++) {
			const idx = j + 1;
			const item = limited[j];
			const el = item.el;
			const rect = item.rect;

			el.setAttribute('data-bu-idx', String(idx));

			const box = document.createElement('div');
			box.setAttribute('style', 'position:absolute;box-sizing:border-box;pointer-events:none;' +
				'left:' + Math.max(0, rect.left) + 'px;' +
				'top:' + Math.max(0, rect.top) + 'px;' +
				'width:' + Math.max(0, rect.width) + 'px;' +
				'height:' + Math.max(0, rect.height) + 'px;' +
				'border:2px dashed #e11d48;');

			const badge = document.createElement('span');
			badge.innerText = '[' + idx + ']';
			const bTop = Math.max(0, rect.top - 14);
			badge.setAttribute('style', 'position:absolute;pointer-events:none;z-index:2147483647;' +
				'left:' + Math.max(0, rect.left) + 'px;' +
				'top:' + bTop + 'px;' +
				'background:#e11d48;color:#ffffff;font-size:10px;font-family:monospace;font-weight:bold;' +
				'padding:0px 3px;border-radius:2px;line-height:14px;box-shadow:0 1px 2px rgba(0,0,0,0.6);');

			container.appendChild(box);
			container.appendChild(badge);

			const tag = el.tagName.toLowerCase();
			let text = '';
			if (tag === 'input' || tag === 'textarea') {
				text = el.value || el.placeholder || el.getAttribute('aria-label') || '';
			} else if (tag === 'select') {
				const opt = el.options && el.selectedIndex >= 0 ? el.options[el.selectedIndex] : null;
				text = opt ? opt.text : (el.getAttribute('aria-label') || '');
			} else {
				text = el.innerText || el.getAttribute('aria-label') || el.getAttribute('title') || el.value || '';
			}
			text = text.trim().replace(/[\r\n\t]+/g, ' ').replace(/\s{2,}/g, ' ');
			if (text.length > 80) {
				text = text.substring(0, 77) + '...';
			}

			elementsData.push({
				index: idx,
				tag: tag,
				type: el.getAttribute('type') || '',
				role: el.getAttribute('role') || '',
				text: text,
				href: el.getAttribute('href') || '',
				x: Math.round(rect.left + rect.width / 2),
				y: Math.round(rect.top + rect.height / 2),
				w: Math.round(rect.width),
				h: Math.round(rect.height)
			});
		}

		(document.body || document.documentElement).appendChild(container);

		let pageText = '';
		if (document.body) {
			pageText = document.body.innerText || '';
			pageText = pageText.trim().replace(/\r\n/g, '\n').replace(/\n{3,}/g, '\n\n');
			if (pageText.length > 4000) {
				pageText = pageText.substring(0, 4000) + '... [truncated]';
			}
		}

		return {
			title: document.title || '',
			url: window.location.href || '',
			page_text: pageText,
			elements: elementsData
		};
	} catch (e) {
		return {
			title: document.title || '',
			url: window.location.href || '',
			page_text: document.body ? document.body.innerText : '',
			elements: [],
			error: String(e)
		};
	}
})()`

// navigate loads the target URL and waits for readyState complete.
func (s *BrowserUseSession) navigate(targetURL string) error {
	targetURL = strings.TrimSpace(targetURL)
	if targetURL == "" {
		return errors.New("url cannot be empty")
	}

	_, _ = s.call("Page.enable", nil, 5*time.Second)

	_, err := s.call("Page.navigate", map[string]any{"url": targetURL}, 15*time.Second)
	if err != nil {
		return fmt.Errorf("cdp navigation failed: %w", err)
	}

	time.Sleep(600 * time.Millisecond)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		val, err := s.evaluate("document.readyState")
		if err == nil {
			if str, ok := val.(string); ok && str == "complete" {
				break
			}
		}
		time.Sleep(150 * time.Millisecond)
	}

	time.Sleep(300 * time.Millisecond)
	return nil
}

// clickElement dispatches coordinate mouse clicks and JS click fallback on an indexed element or selector.
func (s *BrowserUseSession) clickElement(index int, selector string) (tag string, text string, err error) {
	var jsQuery string
	if index > 0 {
		jsQuery = fmt.Sprintf(`(() => {
			const el = document.querySelector('[data-bu-idx="%d"]');
			if (!el) return { found: false };
			el.scrollIntoView({ block: 'center', inline: 'center' });
			const rect = el.getBoundingClientRect();
			const tag = el.tagName.toLowerCase();
			let text = el.innerText || el.value || el.placeholder || el.getAttribute('aria-label') || '';
			text = text.trim().replace(/[\r\n\t]+/g, ' ').replace(/\s{2,}/g, ' ');
			if (text.length > 50) text = text.substring(0, 47) + '...';
			return {
				found: true,
				tag: tag,
				text: text,
				x: Math.round(rect.left + rect.width / 2),
				y: Math.round(rect.top + rect.height / 2)
			};
		})()`, index)
	} else {
		jsQuery = fmt.Sprintf(`(() => {
			const el = document.querySelector(%q);
			if (!el) return { found: false };
			el.scrollIntoView({ block: 'center', inline: 'center' });
			const rect = el.getBoundingClientRect();
			const tag = el.tagName.toLowerCase();
			let text = el.innerText || el.value || el.placeholder || el.getAttribute('aria-label') || '';
			text = text.trim().replace(/[\r\n\t]+/g, ' ').replace(/\s{2,}/g, ' ');
			if (text.length > 50) text = text.substring(0, 47) + '...';
			return {
				found: true,
				tag: tag,
				text: text,
				x: Math.round(rect.left + rect.width / 2),
				y: Math.round(rect.top + rect.height / 2)
			};
		})()`, selector)
	}

	val, err := s.evaluate(jsQuery)
	if err != nil {
		return "", "", fmt.Errorf("eval query failed: %w", err)
	}

	var pos struct {
		Found bool   `json:"found"`
		Tag   string `json:"tag"`
		Text  string `json:"text"`
		X     int    `json:"x"`
		Y     int    `json:"y"`
	}
	b, _ := json.Marshal(val)
	_ = json.Unmarshal(b, &pos)

	if !pos.Found {
		if index > 0 {
			return "", "", fmt.Errorf("element [%d] not found on page", index)
		}
		return "", "", fmt.Errorf("selector %q not found on page", selector)
	}

	_, _ = s.call("Input.dispatchMouseEvent", map[string]any{
		"type":       "mousePressed",
		"x":          pos.X,
		"y":          pos.Y,
		"button":     "left",
		"clickCount": 1,
	}, 5*time.Second)

	_, _ = s.call("Input.dispatchMouseEvent", map[string]any{
		"type":       "mouseReleased",
		"x":          pos.X,
		"y":          pos.Y,
		"button":     "left",
		"clickCount": 1,
	}, 5*time.Second)

	if index > 0 {
		_, _ = s.evaluate(fmt.Sprintf(`document.querySelector('[data-bu-idx="%d"]')?.click()`, index))
	} else {
		_, _ = s.evaluate(fmt.Sprintf(`document.querySelector(%q)?.click()`, selector))
	}

	return pos.Tag, pos.Text, nil
}

// typeElement focuses, sets value, triggers input/change events, and optionally submits form on Enter.
func (s *BrowserUseSession) typeElement(index int, selector string, text string, pressEnter bool) (tag string, err error) {
	textJSON, _ := json.Marshal(text)
	var jsQuery string
	if index > 0 {
		jsQuery = fmt.Sprintf(`(() => {
			const el = document.querySelector('[data-bu-idx="%d"]');
			if (!el) return { found: false };
			el.scrollIntoView({ block: 'center', inline: 'center' });
			el.focus();
			const tag = el.tagName.toLowerCase();
			if (tag === 'input' || tag === 'textarea') {
				el.value = %s;
			} else {
				el.innerText = %s;
			}
			el.dispatchEvent(new Event('input', { bubbles: true }));
			el.dispatchEvent(new Event('change', { bubbles: true }));
			if (%t) {
				const enterDown = new KeyboardEvent('keydown', { key: 'Enter', code: 'Enter', keyCode: 13, which: 13, bubbles: true });
				el.dispatchEvent(enterDown);
				const enterUp = new KeyboardEvent('keyup', { key: 'Enter', code: 'Enter', keyCode: 13, which: 13, bubbles: true });
				el.dispatchEvent(enterUp);
				if (el.form) {
					if (typeof el.form.requestSubmit === 'function') {
						el.form.requestSubmit();
					} else {
						el.form.submit();
					}
				}
			}
			return { found: true, tag: tag };
		})()`, index, string(textJSON), string(textJSON), pressEnter)
	} else {
		jsQuery = fmt.Sprintf(`(() => {
			const el = document.querySelector(%q);
			if (!el) return { found: false };
			el.scrollIntoView({ block: 'center', inline: 'center' });
			el.focus();
			const tag = el.tagName.toLowerCase();
			if (tag === 'input' || tag === 'textarea') {
				el.value = %s;
			} else {
				el.innerText = %s;
			}
			el.dispatchEvent(new Event('input', { bubbles: true }));
			el.dispatchEvent(new Event('change', { bubbles: true }));
			if (%t) {
				const enterDown = new KeyboardEvent('keydown', { key: 'Enter', code: 'Enter', keyCode: 13, which: 13, bubbles: true });
				el.dispatchEvent(enterDown);
				const enterUp = new KeyboardEvent('keyup', { key: 'Enter', code: 'Enter', keyCode: 13, which: 13, bubbles: true });
				el.dispatchEvent(enterUp);
				if (el.form) {
					if (typeof el.form.requestSubmit === 'function') {
						el.form.requestSubmit();
					} else {
						el.form.submit();
					}
				}
			}
			return { found: true, tag: tag };
		})()`, selector, string(textJSON), string(textJSON), pressEnter)
	}

	val, err := s.evaluate(jsQuery)
	if err != nil {
		return "", fmt.Errorf("eval type failed: %w", err)
	}

	var res struct {
		Found bool   `json:"found"`
		Tag   string `json:"tag"`
	}
	b, _ := json.Marshal(val)
	_ = json.Unmarshal(b, &res)

	if !res.Found {
		if index > 0 {
			return "", fmt.Errorf("element [%d] not found on page", index)
		}
		return "", fmt.Errorf("selector %q not found on page", selector)
	}

	if pressEnter {
		_ = s.dispatchKey("Enter")
	}

	return res.Tag, nil
}

// scroll scrolls the window by the given pixel amount.
func (s *BrowserUseSession) scroll(direction string, amount int) error {
	if amount <= 0 {
		amount = 550
	}
	dy := amount
	if strings.ToLower(strings.TrimSpace(direction)) == "up" {
		dy = -amount
	}
	js := fmt.Sprintf("window.scrollBy({ top: %d, behavior: 'instant' });", dy)
	_, err := s.evaluate(js)
	return err
}

// dispatchKey emits keyboard events via CDP and active element dispatch.
func (s *BrowserUseSession) dispatchKey(key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		key = "Enter"
	}

	var vKey int
	var code string
	var text string

	switch strings.ToLower(key) {
	case "enter", "return":
		key = "Enter"
		code = "Enter"
		vKey = 13
		text = "\r"
	case "escape", "esc":
		key = "Escape"
		code = "Escape"
		vKey = 27
	case "tab":
		key = "Tab"
		code = "Tab"
		vKey = 9
	case "backspace":
		key = "Backspace"
		code = "Backspace"
		vKey = 8
	case "arrowdown", "down":
		key = "ArrowDown"
		code = "ArrowDown"
		vKey = 40
	case "arrowup", "up":
		key = "ArrowUp"
		code = "ArrowUp"
		vKey = 38
	case "arrowleft", "left":
		key = "ArrowLeft"
		code = "ArrowLeft"
		vKey = 37
	case "arrowright", "right":
		key = "ArrowRight"
		code = "ArrowRight"
		vKey = 39
	case "space":
		key = " "
		code = "Space"
		vKey = 32
		text = " "
	default:
		code = key
		vKey = 0
		if len(key) == 1 {
			text = key
		}
	}

	downParams := map[string]any{
		"type":                  "rawKeyDown",
		"key":                   key,
		"code":                  code,
		"windowsVirtualKeyCode": vKey,
	}
	if text != "" {
		downParams["text"] = text
		downParams["unmodifiedText"] = text
	}
	_, _ = s.call("Input.dispatchKeyEvent", downParams, 5*time.Second)

	upParams := map[string]any{
		"type":                  "keyUp",
		"key":                   key,
		"code":                  code,
		"windowsVirtualKeyCode": vKey,
	}
	if text != "" {
		upParams["text"] = text
		upParams["unmodifiedText"] = text
	}
	_, _ = s.call("Input.dispatchKeyEvent", upParams, 5*time.Second)

	jsDispatch := fmt.Sprintf(`(() => {
		const target = document.activeElement || document.body;
		const down = new KeyboardEvent('keydown', { key: %q, code: %q, keyCode: %d, which: %d, bubbles: true });
		target.dispatchEvent(down);
		const up = new KeyboardEvent('keyup', { key: %q, code: %q, keyCode: %d, which: %d, bubbles: true });
		target.dispatchEvent(up);
	})()`, key, code, vKey, vKey, key, code, vKey, vKey)
	_, _ = s.evaluate(jsDispatch)
	return nil
}

// captureAndAnnotate indexes the DOM, draws numbered Set-of-Mark badges, captures a screenshot,
// optimizes the screenshot for Vision, and removes the badges.
func (s *BrowserUseSession) captureAndAnnotate(wsPath string, result *BrowserUseResult) error {
	val, err := s.evaluate(somIndexerScript)
	if err != nil {
		return fmt.Errorf("failed to evaluate DOM indexing script: %w", err)
	}

	var somData struct {
		Title    string               `json:"title"`
		URL      string               `json:"url"`
		PageText string               `json:"page_text"`
		Elements []InteractiveElement `json:"elements"`
		Error    string               `json:"error"`
	}
	dataBytes, err := json.Marshal(val)
	if err == nil {
		_ = json.Unmarshal(dataBytes, &somData)
	}

	result.Title = somData.Title
	result.URL = somData.URL
	result.PageText = somData.PageText
	result.Elements = somData.Elements

	ssRes, ssErr := s.call("Page.captureScreenshot", map[string]any{"format": "png"}, 15*time.Second)
	if ssErr == nil && ssRes != nil {
		if b64Str, ok := ssRes["data"].(string); ok && b64Str != "" {
			pngBytes, decErr := base64.StdEncoding.DecodeString(b64Str)
			if decErr == nil && len(pngBytes) > 0 {
				screenshotsDir := filepath.Join(wsPath, ".unjess", "screenshots")
				if wsPath == "" || strings.HasPrefix(wsPath, ".") {
					screenshotsDir = filepath.Join(os.TempDir(), "unjess_screenshots")
				}
				_ = os.MkdirAll(screenshotsDir, 0755)
				ts := time.Now().UnixNano()
				ssPath := filepath.Join(screenshotsDir, fmt.Sprintf("browser_use_%d.png", ts))
				if writeErr := os.WriteFile(ssPath, pngBytes, 0644); writeErr == nil {
					result.ScreenshotPath = ssPath
					dataURI, _, imgW, imgH, _, optSz, optErr := LoadAndOptimizeImageForVision(ssPath, 1568)
					if optErr == nil && dataURI != "" {
						result.VisionDataURI = dataURI
						result.VisionReport = fmt.Sprintf("Set-of-Mark screenshot (%dx%d, %d KB)", imgW, imgH, optSz/1024)
					}
				}
			}
		}
	}

	_, _ = s.evaluate("document.getElementById('__bu_som_container')?.remove();")
	return nil
}

// truncateStr shortens a string to maxLen with trailing ellipsis.
func truncateStr(s string, maxLen int) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}

// buildFormattedState creates a human-readable and model-friendly markdown report of the browser state.
func buildFormattedState(res *BrowserUseResult) {
	var sb strings.Builder
	sb.WriteString("### [BROWSER] Browser-Use State\n")
	sb.WriteString(fmt.Sprintf("- **Action**: %s\n", res.ActionSummary))
	sb.WriteString(fmt.Sprintf("- **Page Title**: %s\n", res.Title))
	sb.WriteString(fmt.Sprintf("- **URL**: %s\n", res.URL))
	if res.ScreenshotPath != "" {
		badgeMsg := fmt.Sprintf("numbered [1..%d] Set-of-Mark badges", len(res.Elements))
		if len(res.Elements) == 0 {
			badgeMsg = "no interactive badges found"
		}
		sb.WriteString(fmt.Sprintf("- **Screenshot**: Attached to Vision (`%s` with %s)\n", filepath.Base(res.ScreenshotPath), badgeMsg))
	}
	if res.EvalOutput != "" {
		sb.WriteString(fmt.Sprintf("\n#### Evaluation Output:\n```\n%s\n```\n", res.EvalOutput))
	}

	sb.WriteString("\n#### Interactive Elements (`[index]` -> use with `action: \"click\"` or `action: \"type\"`):\n")
	if len(res.Elements) == 0 {
		sb.WriteString("(No interactive elements found in current viewport)\n")
	} else {
		for _, el := range res.Elements {
			extra := ""
			if el.Type != "" {
				extra += fmt.Sprintf(" type=%s", el.Type)
			}
			if el.Role != "" {
				extra += fmt.Sprintf(" role=%s", el.Role)
			}
			if el.Href != "" {
				extra += fmt.Sprintf(" href=%s", el.Href)
			}
			sb.WriteString(fmt.Sprintf("[%d] <%s%s> %q @(%d,%d)\n", el.Index, el.Tag, extra, el.Text, el.X, el.Y))
		}
	}

	sb.WriteString("\n#### Visible Page Text:\n")
	if strings.TrimSpace(res.PageText) == "" {
		sb.WriteString("(Empty page text)\n")
	} else {
		sb.WriteString(res.PageText + "\n")
	}
	res.FormattedState = sb.String()
}

// ExecuteBrowserUse executes an interactive browser-use command against a headless or visible Chrome/Edge browser.
func ExecuteBrowserUse(ctx context.Context, wsPath string, opts BrowserUseOptions) (*BrowserUseResult, error) {
	action := strings.ToLower(strings.TrimSpace(opts.Action))
	if action == "" {
		action = "screenshot"
	}

	// 1. CLI fallback
	if action == "cli" {
		if cliPath, err := exec.LookPath("browser-use"); err == nil && cliPath != "" {
			cmdArgs := strings.Fields(opts.Text)
			cmd := exec.CommandContext(ctx, cliPath, cmdArgs...)
			setupProcessTreeKill(cmd)
			out, runErr := cmd.CombinedOutput()
			outStr := string(out)
			if runErr != nil {
				outStr += fmt.Sprintf("\n(exit error: %v)", runErr)
			}
			return &BrowserUseResult{
				ActionSummary:  fmt.Sprintf("Executed browser-use CLI (%s)", opts.Text),
				EvalOutput:     outStr,
				FormattedState: fmt.Sprintf("### [BROWSER] Browser-Use CLI Output\n```\n%s\n```\n", outStr),
			}, nil
		}
		return &BrowserUseResult{
			ActionSummary:  "browser-use CLI not found",
			FormattedState: "browser-use CLI is not installed in PATH. Use native actions (\"navigate\", \"click\", \"type\", \"scroll\", \"press_key\", \"evaluate\", \"screenshot\") with Unjess's native Go CDP browser automation engine.",
		}, nil
	}

	// 2. Close session
	if action == "close" {
		_ = CloseBrowserSession()
		return &BrowserUseResult{
			ActionSummary:  "Browser session closed and cleaned up successfully",
			FormattedState: "### [BROWSER] Browser-Use State\n- **Status**: Browser session closed and temporary files cleaned up.\n",
		}, nil
	}

	headless := true
	if opts.Headless != nil {
		headless = *opts.Headless
	}

	session, err := getOrCreateBrowserSession(headless)
	if err != nil {
		return nil, fmt.Errorf("failed to get or launch browser session: %w", err)
	}

	session.mu.Lock()
	defer session.mu.Unlock()

	var actionSummary string

	switch action {
	case "navigate", "goto", "open":
		targetURL := opts.URL
		if targetURL == "" {
			return nil, errors.New("url is required for navigate action")
		}
		if !strings.HasPrefix(targetURL, "http://") && !strings.HasPrefix(targetURL, "https://") && !strings.HasPrefix(targetURL, "file://") && !strings.HasPrefix(targetURL, "about:") {
			var candidate string
			if filepath.IsAbs(targetURL) {
				candidate = targetURL
			} else if wsPath != "" {
				candidate = filepath.Join(wsPath, targetURL)
			}
			if candidate != "" {
				if _, statErr := os.Stat(candidate); statErr == nil {
					absPath, _ := filepath.Abs(candidate)
					targetURL = "file:///" + filepath.ToSlash(absPath)
				}
			}
			if !strings.HasPrefix(targetURL, "file:///") {
				if strings.Contains(targetURL, ".") && !strings.Contains(targetURL, " ") {
					targetURL = "https://" + targetURL
				}
			}
		}
		if err := session.navigate(targetURL); err != nil {
			return nil, err
		}
		actionSummary = fmt.Sprintf("Navigated to %s", targetURL)

	case "click":
		if opts.Index <= 0 && strings.TrimSpace(opts.Selector) == "" {
			return nil, errors.New("index or selector is required for click action")
		}
		tag, text, err := session.clickElement(opts.Index, opts.Selector)
		if err != nil {
			return nil, err
		}
		time.Sleep(800 * time.Millisecond)
		if opts.Index > 0 {
			actionSummary = fmt.Sprintf("Clicked element [%d] <%s>%s</%s>", opts.Index, tag, text, tag)
		} else {
			actionSummary = fmt.Sprintf("Clicked element matching selector %q", opts.Selector)
		}

	case "type", "input", "fill":
		if opts.Index <= 0 && strings.TrimSpace(opts.Selector) == "" {
			return nil, errors.New("index or selector is required for type action")
		}
		tag, err := session.typeElement(opts.Index, opts.Selector, opts.Text, opts.PressEnter)
		if err != nil {
			return nil, err
		}
		time.Sleep(800 * time.Millisecond)
		enterMsg := ""
		if opts.PressEnter {
			enterMsg = " (pressed Enter)"
		}
		if opts.Index > 0 {
			actionSummary = fmt.Sprintf("Typed %q into element [%d] <%s>%s", opts.Text, opts.Index, tag, enterMsg)
		} else {
			actionSummary = fmt.Sprintf("Typed %q into element %q%s", opts.Text, opts.Selector, enterMsg)
		}

	case "scroll":
		dir := strings.ToLower(strings.TrimSpace(opts.Direction))
		if dir != "up" {
			dir = "down"
		}
		amt := opts.Amount
		if amt <= 0 {
			amt = 550
		}
		if err := session.scroll(dir, amt); err != nil {
			return nil, fmt.Errorf("scroll failed: %w", err)
		}
		time.Sleep(400 * time.Millisecond)
		actionSummary = fmt.Sprintf("Scrolled %s by %dpx", dir, amt)

	case "press_key":
		key := strings.TrimSpace(opts.Key)
		if key == "" {
			key = "Enter"
		}
		if err := session.dispatchKey(key); err != nil {
			return nil, fmt.Errorf("key press failed: %w", err)
		}
		time.Sleep(400 * time.Millisecond)
		actionSummary = fmt.Sprintf("Pressed key %q", key)

	case "evaluate":
		if strings.TrimSpace(opts.Script) == "" {
			return nil, errors.New("script is required for evaluate action")
		}
		evalVal, evalErr := session.evaluate(opts.Script)
		actionSummary = fmt.Sprintf("Evaluated JavaScript: %s", truncateStr(opts.Script, 50))
		result := &BrowserUseResult{
			ActionSummary: actionSummary,
		}
		if evalErr != nil {
			result.EvalOutput = fmt.Sprintf("Error: %v", evalErr)
		} else {
			b, _ := json.MarshalIndent(evalVal, "", "  ")
			result.EvalOutput = string(b)
		}
		_ = session.captureAndAnnotate(wsPath, result)
		buildFormattedState(result)
		return result, nil

	case "extract", "screenshot", "state":
		actionSummary = "Captured live DOM state and Set-of-Mark annotated screenshot"

	default:
		return nil, fmt.Errorf("unsupported browser_use action %q", opts.Action)
	}

	result := &BrowserUseResult{
		ActionSummary: actionSummary,
	}

	if err := session.captureAndAnnotate(wsPath, result); err != nil {
		return nil, fmt.Errorf("failed to capture DOM state/screenshot: %w", err)
	}

	buildFormattedState(result)
	return result, nil
}
