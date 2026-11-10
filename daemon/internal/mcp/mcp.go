// Package mcp implements Model Context Protocol (MCP) transport, client, server management,
// and preset configurations for the Unjess daemon.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gopkg.in/yaml.v3"
)

// ---------------------------------------------------------------------------
// MCP Presets
// ---------------------------------------------------------------------------

// MCPPreset represents a 1-click built-in MCP server preset.
type MCPPreset struct {
	Name           string            `json:"name"`
	Title          string            `json:"title,omitempty"`
	URL            string            `json:"url,omitempty"`
	Command        string            `json:"command"`
	Args           []string          `json:"args"`
	Headers        map[string]string `json:"headers,omitempty"`
	Env            map[string]string `json:"env"`
	Description    string            `json:"description"`
	SiteURL        string            `json:"site_url,omitempty"`
	AuthRequired   bool              `json:"auth_required,omitempty"`
	AuthTokenField string            `json:"auth_token_field,omitempty"`
}

// MCPPresets defines the 4 built-in presets in exact order.
var MCPPresets = []MCPPreset{
	{
		Name:        "rea",
		Command:     "npx",
		Args:        []string{"-y", "rea-agents@latest", "mcp"},
		Env:         map[string]string{},
		Description: "Reverse-engineer any website, web app, or GitHub repo into structured architecture, design system & PRD docs.",
	},
	{
		Name:        "filesystem",
		Command:     "npx",
		Args:        []string{"-y", "@modelcontextprotocol/server-filesystem", "."},
		Env:         map[string]string{},
		Description: "Standard Model Context Protocol filesystem operations.",
	},
	{
		Name:        "github",
		Command:     "npx",
		Args:        []string{"-y", "@modelcontextprotocol/server-github"},
		Env:         map[string]string{"GITHUB_PERSONAL_ACCESS_TOKEN": ""},
		Description: "Query GitHub repositories, issues, PRs, and code search.",
	},
	{
		Name:        "fetch",
		Command:     "npx",
		Args:        []string{"-y", "@modelcontextprotocol/server-fetch"},
		Env:         map[string]string{},
		Description: "Fetch web pages and convert HTML to markdown for LLM consumption.",
	},
}

// ---------------------------------------------------------------------------
// Server Configuration & Storage
// ---------------------------------------------------------------------------

// ServerConfig holds the configuration for a single MCP server.
type ServerConfig struct {
	Name        string            `json:"name,omitempty"`
	Command     string            `json:"command,omitempty"`
	Args        []string          `json:"args"`
	Cwd         string            `json:"cwd,omitempty"`
	Env         map[string]string `json:"env"`
	EagerTools  []string          `json:"eager_tools,omitempty"`
	Enabled     bool              `json:"enabled"`
	URL         string            `json:"url,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
	Description string            `json:"description,omitempty"`
}

// UnmarshalJSON implements custom JSON unmarshaling to handle flexible args and defaults.
func (s *ServerConfig) UnmarshalJSON(data []byte) error {
	type Alias ServerConfig
	aux := struct {
		Alias
		RawArgs any `json:"args"`
	}{
		Alias: Alias{Enabled: true}, // default enabled to true
	}

	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}

	*s = ServerConfig(aux.Alias)
	if aux.RawArgs != nil {
		switch v := aux.RawArgs.(type) {
		case []any:
			s.Args = make([]string, 0, len(v))
			for _, item := range v {
				if str, ok := item.(string); ok {
					s.Args = append(s.Args, str)
				}
			}
		case []string:
			s.Args = v
		case string:
			s.Args = strings.Fields(v)
		}
	}
	if s.Args == nil {
		s.Args = []string{}
	}
	if s.Env == nil {
		s.Env = make(map[string]string)
	}
	if s.Headers == nil {
		s.Headers = make(map[string]string)
	}
	return nil
}

// DefaultMCPConfigPath returns the path to ~/.unjess/mcp.json.
func DefaultMCPConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".unjess", "mcp.json"), nil
}

// LoadMCPConfig reads and parses server configs from ~/.unjess/mcp.json.
func LoadMCPConfig() (map[string]ServerConfig, error) {
	cfgPath, err := DefaultMCPConfigPath()
	if err != nil {
		return nil, err
	}
	return LoadMCPConfigFromPath(cfgPath)
}

// LoadMCPConfigFromPath loads MCP configurations from a given JSON or YAML file path.
func LoadMCPConfigFromPath(cfgPath string) (map[string]ServerConfig, error) {
	result := make(map[string]ServerConfig)
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		if os.IsNotExist(err) {
			return result, nil
		}
		return nil, err
	}

	var raw map[string]any
	if strings.HasSuffix(cfgPath, ".yaml") || strings.HasSuffix(cfgPath, ".yml") {
		if err := yaml.Unmarshal(data, &raw); err != nil {
			return result, err
		}
	} else {
		if err := json.Unmarshal(data, &raw); err != nil {
			return result, err
		}
	}

	// Supports mcpServers, mcp_servers, servers, or root map
	var serversMap map[string]any
	if s, ok := raw["mcpServers"].(map[string]any); ok {
		serversMap = s
	} else if s, ok := raw["mcp_servers"].(map[string]any); ok {
		serversMap = s
	} else if s, ok := raw["servers"].(map[string]any); ok {
		serversMap = s
	} else {
		serversMap = raw
	}

	for name, val := range serversMap {
		sBytes, err := json.Marshal(val)
		if err != nil {
			continue
		}
		var cfg ServerConfig
		if err := json.Unmarshal(sBytes, &cfg); err == nil {
			cfg.Name = name
			if strings.HasPrefix(cfg.URL, "http") || strings.HasPrefix(cfg.Command, "http") {
				if cfg.Headers == nil {
					cfg.Headers = make(map[string]string)
				}
				authH := strings.TrimSpace(cfg.Headers["Authorization"])
				if authH == "" || authH == "Bearer <TOKEN>" {
					tok := ResolveMashinaToken(cfg)
					if tok != "" {
						cfg.Headers["Authorization"] = "Bearer " + tok
					}
				}
			}
			result[name] = cfg
		}
	}

	return result, nil
}

// SaveMCPServer writes or updates an MCP server entry in ~/.unjess/mcp.json.
func SaveMCPServer(cfg ServerConfig) error {
	cfgPath, err := DefaultMCPConfigPath()
	if err != nil {
		return err
	}
	return SaveMCPServerToPath(cfgPath, cfg)
}

// SaveMCPServerToPath writes or updates an MCP server entry in the specified file path.
func SaveMCPServerToPath(cfgPath string, cfg ServerConfig) error {
	if cfg.Name == "" {
		return fmt.Errorf("server name is required")
	}

	if err := os.MkdirAll(filepath.Dir(cfgPath), 0755); err != nil {
		return err
	}

	servers, err := LoadMCPConfigFromPath(cfgPath)
	if err != nil {
		servers = make(map[string]ServerConfig)
	}

	// Normalize command/url
	if cfg.Command == "" && cfg.URL != "" {
		cfg.Command = cfg.URL
	}
	if cfg.Args == nil {
		cfg.Args = []string{}
	}
	if cfg.Env == nil {
		cfg.Env = map[string]string{}
	}
	if cfg.Headers == nil {
		cfg.Headers = map[string]string{}
	}
	if strings.HasPrefix(cfg.URL, "http") || strings.HasPrefix(cfg.Command, "http") {
		authH := strings.TrimSpace(cfg.Headers["Authorization"])
		if authH == "" || authH == "Bearer <TOKEN>" {
			tok := ResolveMashinaToken(cfg)
			if tok != "" {
				cfg.Headers["Authorization"] = "Bearer " + tok
			}
		}
	}

	servers[cfg.Name] = cfg

	// Write root structure with mcpServers key
	root := map[string]any{
		"mcpServers": servers,
	}

	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(cfgPath, out, 0644)
}

// DeleteMCPServer removes a server from ~/.unjess/mcp.json.
func DeleteMCPServer(name string) error {
	cfgPath, err := DefaultMCPConfigPath()
	if err != nil {
		return err
	}
	return DeleteMCPServerFromPath(cfgPath, name)
}

// DeleteMCPServerFromPath removes a server from the specified file path.
func DeleteMCPServerFromPath(cfgPath string, name string) error {
	if name == "" {
		return fmt.Errorf("server name is required")
	}

	servers, err := LoadMCPConfigFromPath(cfgPath)
	if err != nil {
		return err
	}

	if _, ok := servers[name]; !ok {
		return nil
	}

	delete(servers, name)

	root := map[string]any{
		"mcpServers": servers,
	}

	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(cfgPath, out, 0644)
}

// ListMCPServers returns a slice of all configured servers from ~/.unjess/mcp.json.
func ListMCPServers() ([]ServerConfig, error) {
	cfg, err := LoadMCPConfig()
	if err != nil {
		return nil, err
	}
	var list []ServerConfig
	for _, s := range cfg {
		list = append(list, s)
	}
	return list, nil
}

// ResolveMashinaToken resolves the Mashina Bearer token for mashina-collab or mashina.si endpoints.
func ResolveMashinaToken(cfg ServerConfig) string {
	// 1. cfg.Headers["Authorization"]
	for k, v := range cfg.Headers {
		if strings.EqualFold(k, "Authorization") && strings.TrimSpace(v) != "" {
			val := strings.TrimSpace(v)
			if strings.HasPrefix(strings.ToLower(val), "bearer ") {
				tok := strings.TrimSpace(val[7:])
				if tok != "<TOKEN>" && tok != "" {
					return tok
				}
			} else if val != "<TOKEN>" && val != "" {
				return val
			}
		}
	}

	// 2. cfg.Env (Authorization, MASHINA_COLLAB_TOKEN, etc.)
	for _, envKey := range []string{"Authorization", "authorization", "MASHINA_COLLAB_TOKEN", "mashina_collab_token", "MASHINA_TOKEN", "mashina_token"} {
		if v, ok := cfg.Env[envKey]; ok && strings.TrimSpace(v) != "" {
			val := strings.TrimSpace(v)
			if strings.HasPrefix(strings.ToLower(val), "bearer ") {
				tok := strings.TrimSpace(val[7:])
				if tok != "<TOKEN>" && tok != "" {
					return tok
				}
			} else if val != "<TOKEN>" && val != "" {
				return val
			}
		}
	}
	for k, v := range cfg.Env {
		if (strings.EqualFold(k, "authorization") || strings.EqualFold(k, "mashina_collab_token") || strings.EqualFold(k, "mashina_token")) && strings.TrimSpace(v) != "" {
			val := strings.TrimSpace(v)
			if strings.HasPrefix(strings.ToLower(val), "bearer ") {
				tok := strings.TrimSpace(val[7:])
				if tok != "<TOKEN>" && tok != "" {
					return tok
				}
			} else if val != "<TOKEN>" && val != "" {
				return val
			}
		}
	}

	// 3. os.Getenv("MASHINA_COLLAB_TOKEN") or os.Getenv("MASHINA_TOKEN")
	if t := os.Getenv("MASHINA_COLLAB_TOKEN"); strings.TrimSpace(t) != "" {
		return strings.TrimSpace(t)
	}
	if t := os.Getenv("MASHINA_TOKEN"); strings.TrimSpace(t) != "" {
		return strings.TrimSpace(t)
	}

	// 4. ~/.unjess/config.yaml
	home, err := os.UserHomeDir()
	if err == nil {
		yamlPath := filepath.Join(home, ".unjess", "config.yaml")
		if data, err := os.ReadFile(yamlPath); err == nil {
			var raw map[string]any
			if err := yaml.Unmarshal(data, &raw); err == nil {
				if t, ok := raw["mashina_collab_token"].(string); ok && strings.TrimSpace(t) != "" {
					return strings.TrimSpace(t)
				}
				if apiKeys, ok := raw["api_keys"].(map[string]any); ok {
					if t, ok := apiKeys["mashina"].(string); ok && strings.TrimSpace(t) != "" {
						return strings.TrimSpace(t)
					}
					if t, ok := apiKeys["mashina-collab"].(string); ok && strings.TrimSpace(t) != "" {
						return strings.TrimSpace(t)
					}
				}
			}
		}
	}

	return ""
}

// ---------------------------------------------------------------------------
// JSON-RPC Models
// ---------------------------------------------------------------------------

// RPCRequest represents a JSON-RPC 2.0 request or notification.
type RPCRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      any    `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// RPCResponse represents a JSON-RPC 2.0 response.
type RPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// RPCError represents a JSON-RPC error object.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// ToolInfo describes an available MCP tool.
type ToolInfo struct {
	Name         string         `json:"name"`
	PrefixedName string         `json:"prefixed_name"`
	Server       string         `json:"server"`
	Description  string         `json:"description"`
	Parameters   map[string]any `json:"parameters"`
}

// ---------------------------------------------------------------------------
// Transports (HTTP & Stdio)
// ---------------------------------------------------------------------------

// Transport defines the communication channel for an MCP server.
type Transport interface {
	Start() error
	Send(ctx context.Context, message string) (string, error)
	SendNotification(message string) error
	Close() error
	IsRunning() bool
	GetStderr() string
}

// HTTPTransport sends JSON-RPC 2.0 requests over HTTP POST and handles SSE responses.
type HTTPTransport struct {
	url       string
	headers   map[string]string
	timeout   time.Duration
	client    *http.Client
	mu        sync.Mutex
	running   bool
	lastError string
}

// NewHTTPTransport creates a new HTTP transport instance.
func NewHTTPTransport(url string, headers map[string]string) *HTTPTransport {
	hdrs := make(map[string]string)
	for k, v := range headers {
		hdrs[k] = v
	}
	return &HTTPTransport{
		url:     strings.TrimSpace(url),
		headers: hdrs,
		timeout: 60 * time.Second,
	}
}

// Start initializes the HTTP client.
func (t *HTTPTransport) Start() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.client = &http.Client{Timeout: t.timeout}
	t.running = true
	t.lastError = ""
	return nil
}

// Send sends a JSON-RPC message via HTTP POST and parses application/json or text/event-stream.
func (t *HTTPTransport) Send(ctx context.Context, message string) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if !t.running || t.client == nil {
		return "", fmt.Errorf("HTTP MCP transport is not running")
	}

	req, err := http.NewRequestWithContext(ctx, "POST", t.url, strings.NewReader(message))
	if err != nil {
		t.lastError = err.Error()
		return "", err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range t.headers {
		req.Header.Set(k, v)
	}

	resp, err := t.client.Do(req)
	if err != nil {
		t.lastError = err.Error()
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		t.lastError = fmt.Sprintf("HTTP %d: Unauthorized - please verify your token in Settings", resp.StatusCode)
		return fmt.Sprintf(`{"jsonrpc":"2.0","error":{"code":-32000,"message":"Authentication failed (%d): Valid token required. Configure in Settings -> API Keys / MCP."}}`, resp.StatusCode), nil
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		t.lastError = err.Error()
		return "", err
	}

	if resp.StatusCode >= 400 {
		t.lastError = fmt.Sprintf("HTTP %d: %s", resp.StatusCode, string(bodyBytes))
		return fmt.Sprintf(`{"jsonrpc":"2.0","error":{"code":-32603,"message":"HTTP transport error (%d): %s"}}`, resp.StatusCode, strings.TrimSpace(string(bodyBytes))), nil
	}

	text := strings.TrimSpace(string(bodyBytes))
	contentType := resp.Header.Get("Content-Type")

	// Parse Server-Sent Events (SSE) format if returned
	if strings.Contains(contentType, "text/event-stream") || strings.HasPrefix(text, "data:") {
		lines := strings.Split(text, "\n")
		for i := len(lines) - 1; i >= 0; i-- {
			line := strings.TrimSpace(lines[i])
			if strings.HasPrefix(line, "data:") {
				payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
				if payload != "" && payload != "[DONE]" {
					return payload, nil
				}
			}
		}
	}

	return text, nil
}

// SendNotification sends a fire-and-forget notification via HTTP POST.
func (t *HTTPTransport) SendNotification(message string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.running || t.client == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "POST", t.url, strings.NewReader(message))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range t.headers {
		req.Header.Set(k, v)
	}
	resp, err := t.client.Do(req)
	if err == nil && resp != nil {
		_ = resp.Body.Close()
	}
	return nil
}

// Close shuts down the HTTP transport.
func (t *HTTPTransport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.running = false
	t.client = nil
	return nil
}

// IsRunning reports whether the transport is active.
func (t *HTTPTransport) IsRunning() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.running
}

// GetStderr returns the last diagnostic error string.
func (t *HTTPTransport) GetStderr() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.lastError
}

// StdioTransport executes an MCP server as a subprocess and communicates via stdin/stdout.
type StdioTransport struct {
	command   string
	args      []string
	cwd       string
	env       map[string]string
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	stdout    *bufio.Reader
	mu        sync.Mutex
	running   bool
	stderrBuf strings.Builder
}

// NewStdioTransport creates a new StdioTransport instance.
func NewStdioTransport(command string, args []string, cwd string, env map[string]string) *StdioTransport {
	return &StdioTransport{
		command: command,
		args:    args,
		cwd:     cwd,
		env:     env,
	}
}

// resolveExecutable attempts to resolve command path, handling Windows extensions like .cmd or .bat.
func resolveExecutable(cmd string) string {
	if runtime.GOOS == "windows" {
		if lp, err := exec.LookPath(cmd); err == nil {
			return lp
		}
		if !strings.Contains(cmd, ".") {
			for _, ext := range []string{".cmd", ".exe", ".bat"} {
				if lp, err := exec.LookPath(cmd + ext); err == nil {
					return lp
				}
			}
		}
	} else {
		if lp, err := exec.LookPath(cmd); err == nil {
			return lp
		}
	}
	return cmd
}

// Start launches the MCP server subprocess.
func (s *StdioTransport) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	execPath := resolveExecutable(s.command)
	cmd := exec.Command(execPath, s.args...)
	if s.cwd != "" {
		cmd.Dir = s.cwd
	}

	cmd.SysProcAttr = sysProcAttr()

	// Merge system environment with custom env
	if len(s.env) > 0 {
		envMap := make(map[string]string)
		for _, e := range os.Environ() {
			if idx := strings.Index(e, "="); idx > 0 {
				envMap[e[:idx]] = e[idx+1:]
			}
		}
		for k, v := range s.env {
			envMap[k] = v
		}
		merged := make([]string, 0, len(envMap))
		for k, v := range envMap {
			merged = append(merged, fmt.Sprintf("%s=%s", k, v))
		}
		cmd.Env = merged
	}

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("failed creating stdin pipe: %w", err)
	}

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed creating stdout pipe: %w", err)
	}

	cmd.Stderr = &s.stderrBuf

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start MCP server (%s): %w", s.command, err)
	}

	s.cmd = cmd
	s.stdin = stdinPipe
	s.stdout = bufio.NewReader(stdoutPipe)
	s.running = true
	return nil
}

// Send sends a newline-delimited JSON-RPC string over stdin and reads a response line from stdout.
func (s *StdioTransport) Send(ctx context.Context, message string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.running || s.stdin == nil || s.stdout == nil {
		return "", fmt.Errorf("stdio MCP transport is not running")
	}

	// Write message + newline
	if _, err := io.WriteString(s.stdin, message+"\n"); err != nil {
		return "", fmt.Errorf("failed writing to MCP server stdin: %w", err)
	}

	type readResult struct {
		line string
		err  error
	}

	ch := make(chan readResult, 1)
	go func() {
		for {
			line, err := s.stdout.ReadString('\n')
			if err != nil {
				ch <- readResult{"", err}
				return
			}
			trimmed := strings.TrimSpace(line)
			if trimmed == "" {
				continue
			}
			// Skip logging lines that don't look like JSON
			if strings.HasPrefix(trimmed, "{") {
				ch <- readResult{trimmed, nil}
				return
			}
		}
	}()

	select {
	case res := <-ch:
		if res.err != nil {
			return "", fmt.Errorf("error reading from MCP server: %w", res.err)
		}
		return res.line, nil
	case <-time.After(45 * time.Second):
		return "", fmt.Errorf("timeout waiting for MCP server response after 45s")
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// SendNotification writes a notification to stdin without waiting for a response.
func (s *StdioTransport) SendNotification(message string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running || s.stdin == nil {
		return nil
	}
	_, err := io.WriteString(s.stdin, message+"\n")
	return err
}

// Close terminates the subprocess.
func (s *StdioTransport) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stdin != nil {
		_ = s.stdin.Close()
	}
	if s.cmd != nil && s.cmd.Process != nil {
		// Kill the whole tree so helpers such as a Chrome launched by the server do not linger.
		killProcessTree(s.cmd.Process.Pid)
		_ = s.cmd.Process.Kill()
		_ = s.cmd.Wait()
	}
	s.running = false
	s.cmd = nil
	s.stdin = nil
	s.stdout = nil
	return nil
}

// IsRunning reports whether the process is alive.
func (s *StdioTransport) IsRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running && s.cmd != nil && s.cmd.Process != nil
}

// GetStderr returns captured stderr.
func (s *StdioTransport) GetStderr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stderrBuf.String()
}

// ---------------------------------------------------------------------------
// MCP Protocol Client
// ---------------------------------------------------------------------------

// Client handles JSON-RPC 2.0 message framing over a Transport.
type Client struct {
	transport   Transport
	serverName  string
	initialized bool
	reqCounter  int64
}

// NewClient creates a new MCP client.
func NewClient(transport Transport, serverName string) *Client {
	return &Client{
		transport:  transport,
		serverName: serverName,
	}
}

func (c *Client) nextID() int64 {
	return atomic.AddInt64(&c.reqCounter, 1)
}

// Initialize performs the MCP handshake.
func (c *Client) Initialize(ctx context.Context) error {
	req := RPCRequest{
		JSONRPC: "2.0",
		ID:      c.nextID(),
		Method:  "initialize",
		Params: map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{},
			"clientInfo": map[string]any{
				"name":    "unjess",
				"version": "0.1.0",
			},
		},
	}

	reqBytes, err := json.Marshal(req)
	if err != nil {
		return err
	}

	rawResp, err := c.transport.Send(ctx, string(reqBytes))
	if err != nil {
		return err
	}

	var resp RPCResponse
	if err := json.Unmarshal([]byte(rawResp), &resp); err != nil {
		return fmt.Errorf("invalid MCP initialize response: %w", err)
	}

	if resp.Error != nil {
		return fmt.Errorf("MCP initialize error: %s (code %d)", resp.Error.Message, resp.Error.Code)
	}

	c.initialized = true

	// Send notifications/initialized notification
	notify := RPCRequest{
		JSONRPC: "2.0",
		Method:  "notifications/initialized",
	}
	if nBytes, err := json.Marshal(notify); err == nil {
		_ = c.transport.SendNotification(string(nBytes))
	}

	return nil
}

// ListTools queries tools/list on the server.
func (c *Client) ListTools(ctx context.Context) ([]ToolInfo, error) {
	req := RPCRequest{
		JSONRPC: "2.0",
		ID:      c.nextID(),
		Method:  "tools/list",
	}

	reqBytes, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	rawResp, err := c.transport.Send(ctx, string(reqBytes))
	if err != nil {
		return nil, err
	}

	var resp RPCResponse
	if err := json.Unmarshal([]byte(rawResp), &resp); err != nil {
		return nil, fmt.Errorf("invalid tools/list response: %w", err)
	}

	if resp.Error != nil {
		return nil, fmt.Errorf("tools/list error: %s", resp.Error.Message)
	}

	var listData struct {
		Tools []struct {
			Name        string         `json:"name"`
			Description string         `json:"description"`
			InputSchema map[string]any `json:"inputSchema"`
		} `json:"tools"`
	}

	if err := json.Unmarshal(resp.Result, &listData); err != nil {
		return nil, fmt.Errorf("failed parsing tools list: %w", err)
	}

	tools := make([]ToolInfo, 0, len(listData.Tools))
	for _, t := range listData.Tools {
		schema := t.InputSchema
		if schema == nil {
			schema = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		tools = append(tools, ToolInfo{
			Name:         t.Name,
			PrefixedName: fmt.Sprintf("mcp_%s_%s", c.serverName, t.Name),
			Server:       c.serverName,
			Description:  t.Description,
			Parameters:   schema,
		})
	}

	return tools, nil
}

// CallTool executes tools/call on the server.
func (c *Client) CallTool(ctx context.Context, name string, arguments map[string]any) (string, error) {
	req := RPCRequest{
		JSONRPC: "2.0",
		ID:      c.nextID(),
		Method:  "tools/call",
		Params: map[string]any{
			"name":      name,
			"arguments": arguments,
		},
	}

	reqBytes, err := json.Marshal(req)
	if err != nil {
		return "", err
	}

	rawResp, err := c.transport.Send(ctx, string(reqBytes))
	if err != nil {
		return "", err
	}

	var resp RPCResponse
	if err := json.Unmarshal([]byte(rawResp), &resp); err != nil {
		return "", fmt.Errorf("invalid tools/call response: %w", err)
	}

	if resp.Error != nil {
		return "", fmt.Errorf("MCP tool call error: %s", resp.Error.Message)
	}

	var callData struct {
		Content []struct {
			Type     string         `json:"type"`
			Text     string         `json:"text"`
			Data     string         `json:"data"`
			MimeType string         `json:"mimeType"`
			Resource map[string]any `json:"resource"`
		} `json:"content"`
	}

	if err := json.Unmarshal(resp.Result, &callData); err == nil && len(callData.Content) > 0 {
		var parts []string
		for _, item := range callData.Content {
			if item.Text != "" {
				parts = append(parts, item.Text)
			} else if item.Data != "" {
				mime := item.MimeType
				if mime == "" {
					mime = "application/octet-stream"
				}
				parts = append(parts, fmt.Sprintf("data:%s;base64,%s", mime, item.Data))
			} else if item.Resource != nil {
				if rBytes, err := json.Marshal(item.Resource); err == nil {
					parts = append(parts, string(rBytes))
				}
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, "\n"), nil
		}
	}

	return string(resp.Result), nil
}

// Close shuts down the client and underlying transport.
func (c *Client) Close() error {
	c.initialized = false
	return c.transport.Close()
}

// ---------------------------------------------------------------------------
// MCP Server Manager
// ---------------------------------------------------------------------------

// Manager manages MCP server discovery, connections, and tool execution.
type Manager struct {
	workspaceDir string
	mu           sync.RWMutex
	configs      map[string]ServerConfig
	clients      map[string]*Client
	allTools     map[string]ToolInfo
}

// NewManager creates a new server manager, loading configs from ~/.unjess/mcp.json.
func NewManager(workspaceDir string) *Manager {
	configs, _ := LoadMCPConfig()
	if configs == nil {
		configs = make(map[string]ServerConfig)
	}
	return NewManagerWithConfig(configs, workspaceDir)
}

// NewManagerWithConfig creates a new server manager with pre-loaded configs.
func NewManagerWithConfig(configs map[string]ServerConfig, workspaceDir string) *Manager {
	return &Manager{
		workspaceDir: workspaceDir,
		configs:      configs,
		clients:      make(map[string]*Client),
		allTools:     make(map[string]ToolInfo),
	}
}

// resolvePath substitutes workspace placeholders and resolves relative paths.
func (m *Manager) resolvePath(p string) string {
	if p == "" {
		return p
	}
	ws := m.workspaceDir
	if ws == "" {
		if cwd, err := os.Getwd(); err == nil {
			ws = cwd
		}
	}
	resolved := strings.ReplaceAll(p, "${workspaceRoot}", ws)
	resolved = strings.ReplaceAll(resolved, "${workspace}", ws)
	norm := strings.ReplaceAll(resolved, "\\", "/")
	if strings.HasPrefix(norm, "./") || strings.HasPrefix(norm, "../") {
		resolved = filepath.Join(ws, resolved)
	}
	return resolved
}

// Connect connects and initializes an MCP server by name.
func (m *Manager) Connect(serverName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	cfg, ok := m.configs[serverName]
	if !ok {
		return fmt.Errorf("unknown MCP server '%s'", serverName)
	}

	if !cfg.Enabled {
		return fmt.Errorf("MCP server '%s' is disabled", serverName)
	}

	if client, ok := m.clients[serverName]; ok {
		if client.initialized && client.transport.IsRunning() {
			return nil
		}
		// Stale client (server process died): tear it down and reconnect.
		_ = client.Close()
		delete(m.clients, serverName)
	}

	targetURL := strings.TrimSpace(cfg.URL)
	cmdStr := strings.TrimSpace(cfg.Command)
	if targetURL == "" && (strings.HasPrefix(cmdStr, "http://") || strings.HasPrefix(cmdStr, "https://")) {
		targetURL = cmdStr
	}

	var transport Transport
	if targetURL != "" {
		// Remote HTTP MCP server
		headers := make(map[string]string)
		for k, v := range cfg.Headers {
			headers[k] = v
		}

		// Auto-inject Mashina Bearer token if not explicitly present
		if serverName == "mashina" || serverName == "mashina-collab" || strings.Contains(targetURL, "mashina") {
			hasAuth := false
			for k := range headers {
				if strings.EqualFold(k, "Authorization") {
					hasAuth = true
					break
				}
			}
			if !hasAuth {
				token := ResolveMashinaToken(cfg)
				if token != "" {
					headers["Authorization"] = "Bearer " + token
				}
			}
		}

		transport = NewHTTPTransport(targetURL, headers)
	} else {
		// Subprocess stdio MCP server
		resolvedCmd := m.resolvePath(cfg.Command)
		resolvedArgs := make([]string, len(cfg.Args))
		for i, a := range cfg.Args {
			resolvedArgs[i] = m.resolvePath(a)
		}
		resolvedCwd := m.resolvePath(cfg.Cwd)

		transport = NewStdioTransport(resolvedCmd, resolvedArgs, resolvedCwd, cfg.Env)
	}

	if err := transport.Start(); err != nil {
		return fmt.Errorf("failed starting transport for '%s': %w", serverName, err)
	}

	client := NewClient(transport, serverName)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	if err := client.Initialize(ctx); err != nil {
		_ = transport.Close()
		return fmt.Errorf("failed initializing MCP server '%s': %w", serverName, err)
	}

	m.clients[serverName] = client

	// Discover tools
	tools, err := client.ListTools(ctx)
	if err == nil {
		for _, t := range tools {
			m.allTools[t.PrefixedName] = t
		}
	}

	return nil
}

// ConnectAll connects to all enabled servers and returns count of connected servers.
func (m *Manager) ConnectAll() int {
	count := 0
	for name, cfg := range m.configs {
		if cfg.Enabled {
			if err := m.Connect(name); err == nil {
				count++
			}
		}
	}
	return count
}

// Disconnect closes connection to a server and removes its tools.
func (m *Manager) Disconnect(serverName string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if client, ok := m.clients[serverName]; ok {
		_ = client.Close()
		delete(m.clients, serverName)
	}

	for k, t := range m.allTools {
		if t.Server == serverName {
			delete(m.allTools, k)
		}
	}
}

// DisconnectAll closes connections to all servers.
func (m *Manager) DisconnectAll() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for name, client := range m.clients {
		_ = client.Close()
		delete(m.clients, name)
	}
	m.allTools = make(map[string]ToolInfo)
}

// sharedManagers keeps one long-lived Manager per workspace so stateful MCP servers (for example
// chrome-devtools, which owns a Chrome instance and its open pages) survive between agent turns
// instead of being torn down every time a turn finishes.
var (
	sharedMu       sync.Mutex
	sharedManagers = make(map[string]*Manager)
)

// Shared returns the process-wide Manager for workspaceDir, creating it on first use. Server
// configs are reloaded on every call; servers that were removed, disabled, or reconfigured are
// disconnected so the next ConnectAll starts them with the new settings.
func Shared(workspaceDir string) *Manager {
	sharedMu.Lock()
	defer sharedMu.Unlock()

	m, ok := sharedManagers[workspaceDir]
	if !ok {
		m = NewManager(workspaceDir)
		sharedManagers[workspaceDir] = m
		return m
	}

	fresh, _ := LoadMCPConfig()
	if fresh == nil {
		fresh = make(map[string]ServerConfig)
	}
	m.mu.Lock()
	for name, client := range m.clients {
		nc, exists := fresh[name]
		if !exists || !nc.Enabled || !reflect.DeepEqual(nc, m.configs[name]) {
			_ = client.Close()
			delete(m.clients, name)
			for k, t := range m.allTools {
				if t.Server == name {
					delete(m.allTools, k)
				}
			}
		}
	}
	m.configs = fresh
	m.mu.Unlock()
	return m
}

// ShutdownShared closes every shared Manager and its MCP server processes. Call on daemon exit.
func ShutdownShared() {
	sharedMu.Lock()
	defer sharedMu.Unlock()
	for key, m := range sharedManagers {
		m.DisconnectAll()
		delete(sharedManagers, key)
	}
}

// AvailableTools returns all discovered tools across connected servers.
func (m *Manager) AvailableTools() []ToolInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]ToolInfo, 0, len(m.allTools))
	for _, t := range m.allTools {
		result = append(result, t)
	}
	return result
}

// CallTool executes a tool by its prefixed name (e.g. mcp_<server>_<tool>).
func (m *Manager) CallTool(prefixedName string, args map[string]any) (string, error) {
	m.mu.RLock()
	tool, ok := m.allTools[prefixedName]
	if !ok {
		m.mu.RUnlock()
		return "", fmt.Errorf("unknown MCP tool '%s'", prefixedName)
	}

	client, ok := m.clients[tool.Server]
	if !ok || !client.initialized {
		m.mu.RUnlock()
		return "", fmt.Errorf("MCP server '%s' is not connected", tool.Server)
	}
	wsRoot := m.workspaceDir
	m.mu.RUnlock()

	// Auto-hydrate file contents from workspace disk for collab_deploy when content is omitted, empty, or "@disk"
	if strings.Contains(tool.Name, "deploy") && strings.TrimSpace(wsRoot) != "" && args != nil {
		if rawFiles, ok := args["files"].([]any); ok {
			wsClean := filepath.Clean(wsRoot)
			for i, item := range rawFiles {
				if fMap, ok := item.(map[string]any); ok {
					relPath, _ := fMap["path"].(string)
					contentVal, _ := fMap["content"].(string)
					trimmedContent := strings.TrimSpace(contentVal)
					if relPath != "" && (trimmedContent == "" || trimmedContent == "@disk" || trimmedContent == "@file") {
						cand := filepath.Clean(filepath.Join(wsClean, filepath.FromSlash(relPath)))
						if strings.HasPrefix(strings.ToLower(cand), strings.ToLower(wsClean)) {
							if data, err := os.ReadFile(cand); err == nil {
								fMap["content"] = string(data)
								rawFiles[i] = fMap
							}
						}
					}
				}
			}
			args["files"] = rawFiles
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	rawRes, err := client.CallTool(ctx, tool.Name, args)
	if err != nil {
		return "", err
	}
	return InterceptAndSavePayloads(wsRoot, rawRes), nil
}

// RefreshTools re-discovers tools from all currently connected servers.
func (m *Manager) RefreshTools() map[string]int {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.allTools = make(map[string]ToolInfo)
	results := make(map[string]int)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	for sname, client := range m.clients {
		if client.initialized {
			tools, err := client.ListTools(ctx)
			if err == nil {
				for _, t := range tools {
					if strings.Contains(t.Name, "deploy") {
						t.Description += " [UNJESS AUTO-DISK TIP: To deploy workspace files without retyping their contents, pass files: [{\"path\": \"index.html\", \"content\": \"@disk\"}, ...] or leave \"content\" empty, and the Unjess daemon will automatically read the exact bytes from disk!]"
					}
					m.allTools[t.PrefixedName] = t
				}
				results[sname] = len(tools)
			} else {
				results[sname] = 0
			}
		}
	}

	return results
}

// RefreshMCPTools connects enabled MCP servers, discovers tools, and returns the response for POST /api/harness/mcp/refresh.
func (m *Manager) RefreshMCPTools() map[string]any {
	serverStatus := make(map[string]any)

	for name, cfg := range m.configs {
		if cfg.Enabled {
			if err := m.Connect(name); err != nil {
				serverStatus[name] = fmt.Sprintf("error: %v", err)
			} else {
				serverStatus[name] = "connected"
			}
		} else {
			serverStatus[name] = "disabled"
		}
	}

	tools := m.AvailableTools()
	toolsList := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		toolsList = append(toolsList, map[string]any{
			"name":          t.Name,
			"prefixed_name": t.PrefixedName,
			"server":        t.Server,
			"description":   t.Description,
			"parameters":    t.Parameters,
		})
	}

	return map[string]any{
		"status":        "ok",
		"tools":         toolsList,
		"server_status": serverStatus,
	}
}

// RefreshMCPTools is the package-level helper that loads config, connects servers, and produces the refresh result.
func RefreshMCPTools() map[string]any {
	cfg, err := LoadMCPConfig()
	if err != nil {
		return map[string]any{
			"status":        "ok",
			"tools":         []map[string]any{},
			"server_status": map[string]any{},
		}
	}

	manager := NewManagerWithConfig(cfg, "")
	defer manager.DisconnectAll()

	return manager.RefreshMCPTools()
}
