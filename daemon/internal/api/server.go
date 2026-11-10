// Package api implements the HTTP REST server and routes for Unjess Desktop,
// Mobile Companion, and Agentic Coding Harness.
package api

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"unjess-daemon/internal/agent"
	"unjess-daemon/internal/config"
	"unjess-daemon/internal/frontier"
	"unjess-daemon/internal/mcp"
	"unjess-daemon/internal/memory"
	"unjess-daemon/internal/scheduler"
	"unjess-daemon/internal/security"
	"unjess-daemon/internal/skills"
	"unjess-daemon/internal/tools"
	"unjess-daemon/internal/updater"
	"unjess-daemon/internal/workspace"
	"unjess-daemon/internal/worktree"
)

//go:embed embedded_index.html
var embeddedDesktopHTML []byte

// Server encapsulates the HTTP API and static file server for Unjess Desktop & Mobile Companion.
type Server struct {
	Port       int
	Host       string
	Settings   *config.Settings
	Harness    *agent.HarnessManager
	Scheduler  *scheduler.Scheduler
	StartTime  time.Time
	httpServer *http.Server
}

// NewServer initializes a new Server instance.
func NewServer(port int, host string, settings *config.Settings, harness *agent.HarnessManager) *Server {
	if port <= 0 {
		port = 9757
	}
	if host == "" {
		host = "127.0.0.1"
	}
	s := &Server{
		Port:      port,
		Host:      host,
		Settings:  settings,
		Harness:   harness,
		StartTime: time.Now(),
	}
	s.Scheduler = scheduler.NewScheduler("", s)
	return s
}

// corsMiddleware provides Cross-Origin Resource Sharing headers for web, desktop, and mobile companion clients,
// while strictly rejecting untrusted external browser origins on localhost to prevent CSRF / DNS rebinding RCE.
func (s *Server) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		referer := r.Header.Get("Referer")

		// On local interfaces (127.0.0.1 / localhost), reject external web origins/referers
		if s.Host == "127.0.0.1" || s.Host == "localhost" {
			if origin != "" && !isLocalOrigin(origin) {
				http.Error(w, "Forbidden: Cross-origin access from untrusted external origin rejected", http.StatusForbidden)
				return
			}
			if referer != "" && !isLocalReferer(referer) {
				http.Error(w, "Forbidden: Cross-origin access from untrusted external referer rejected", http.StatusForbidden)
				return
			}
		}

		if origin != "" {
			if s.Host == "127.0.0.1" || s.Host == "localhost" {
				if isLocalOrigin(origin) {
					w.Header().Set("Access-Control-Allow-Origin", origin)
				}
			} else {
				w.Header().Set("Access-Control-Allow-Origin", "*")
			}
		} else {
			w.Header().Set("Access-Control-Allow-Origin", "*")
		}
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Pin, X-Session-Token")
		w.Header().Set("Access-Control-Max-Age", "86400")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isLocalOrigin(origin string) bool {
	if origin == "" {
		return true
	}
	if strings.HasPrefix(origin, "tauri://") || strings.HasPrefix(origin, "wry://") || strings.HasPrefix(origin, "vscode-webview://") {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	h := u.Hostname()
	return h == "127.0.0.1" || h == "localhost"
}

func isLocalReferer(referer string) bool {
	u, err := url.Parse(referer)
	if err != nil {
		return false
	}
	h := u.Hostname()
	return h == "127.0.0.1" || h == "localhost"
}

// authMiddleware enforces PIN authentication if configured, exempting public UI and iframe endpoints.
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		// Exempt UI, health, and iframe endpoints that cannot pass custom headers
		if path == "/" || path == "/desktop" || path == "/mobile" ||
			path == "/app" || path == "/openclaw" || path == "/studio" ||
			path == "/api/health" || path == "/api/status" ||
			path == "/api/harness/raw" || path == "/api/harness/web-proxy" {
			next.ServeHTTP(w, r)
			return
		}

		if s.Settings != nil && s.Settings.EnablePinAuth && s.Settings.MobilePin != "" {
			pin := r.Header.Get("X-Pin")
			if pin == "" {
				pin = r.URL.Query().Get("pin")
			}
			if pin != s.Settings.MobilePin {
				writeError(w, http.StatusUnauthorized, "Invalid or missing PIN")
				return
			}
		}

		next.ServeHTTP(w, r)
	})
}

// Routes constructs the main http.ServeMux routing table for all Unjess Desktop & Harness endpoints.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	// 1. Desktop UI & Mobile Companion HTML
	mux.HandleFunc("GET /", s.handleDesktopUI)
	mux.HandleFunc("GET /desktop", s.handleDesktopUI)
	mux.HandleFunc("GET /mobile", s.handleDesktopUI)
	mux.HandleFunc("GET /app", s.handleDesktopUI)
	mux.HandleFunc("GET /openclaw", s.handleDesktopUI)
	mux.HandleFunc("GET /studio", s.handleDesktopUI)

	// 2. Health & Status
	mux.HandleFunc("GET /api/health", s.handleHealth)
	mux.HandleFunc("GET /api/status", s.handleStatus)

	// 3. Settings & Models
	mux.HandleFunc("GET /api/studio/settings", s.handleGetSettings)
	mux.HandleFunc("POST /api/studio/settings", s.handleSaveSettings)
	mux.HandleFunc("GET /api/config", s.handleGetSettings)
	mux.HandleFunc("PUT /api/config", s.handleSaveSettings)
	mux.HandleFunc("GET /api/studio/models", s.handleModels)
	mux.HandleFunc("GET /api/harness/models", s.handleModels)
	mux.HandleFunc("GET /api/harness/network-info", s.handleNetworkInfo)
	mux.HandleFunc("GET /api/studio/network-info", s.handleNetworkInfo)

	// 4. State & Turn Control
	mux.HandleFunc("GET /api/harness/state", s.handleGetState)
	mux.HandleFunc("GET /api/harness/live", s.handleGetLive)
	mux.HandleFunc("GET /api/harness/usage", s.handleGetUsage)
	mux.HandleFunc("GET /api/harness/context", s.handleGetContext)
	mux.HandleFunc("POST /api/harness/abort", s.handleAbort)
	mux.HandleFunc("POST /api/harness/undo", s.handleUndoTurn)

	// 5. Conversations
	mux.HandleFunc("POST /api/harness/conversations", s.handleCreateConversation)
	mux.HandleFunc("DELETE /api/harness/conversations/{id}", s.handleDeleteConversation)
	mux.HandleFunc("POST /api/harness/conversations/{id}/clear", s.handleClearConversation)
	mux.HandleFunc("POST /api/harness/conversations/{id}/rename", s.handleRenameConversation)
	mux.HandleFunc("PATCH /api/harness/conversations/{id}", s.handleRenameConversation)
	mux.HandleFunc("DELETE /api/harness/conversations/", s.handleDeleteConversation)
	mux.HandleFunc("POST /api/harness/conversations/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/clear") {
			s.handleClearConversation(w, r)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/rename") {
			s.handleRenameConversation(w, r)
			return
		}
		s.handleCreateConversation(w, r)
	})

	// 6. Workspaces
	mux.HandleFunc("POST /api/harness/workspaces", s.handleAddWorkspace)
	mux.HandleFunc("POST /api/harness/workspaces/select", s.handleSelectWorkspace)
	mux.HandleFunc("PUT /api/harness/workspace", s.handleSelectWorkspace)
	mux.HandleFunc("POST /api/harness/workspaces/browse", s.handleBrowseWorkspace)

	// 7. Workspace Files & Raw Delivery
	mux.HandleFunc("GET /api/harness/files", s.handleListFiles)
	mux.HandleFunc("GET /api/harness/file", s.handleReadFile)
	mux.HandleFunc("POST /api/harness/file", s.handleWriteFile)
	mux.HandleFunc("DELETE /api/harness/file", s.handleDeleteFile)
	mux.HandleFunc("GET /api/harness/raw", s.handleRawFile)

	// 8. Command Execution & Chat Turns
	mux.HandleFunc("POST /api/harness/exec", s.handleExec)
	mux.HandleFunc("POST /api/harness/chat", s.handleChat)
	mux.HandleFunc("POST /api/chat", s.handleChat)
	mux.HandleFunc("POST /api/harness/mode", s.handleSetAgentMode)
	mux.HandleFunc("POST /api/harness/command/approve", s.handleApproveCommand)
	mux.HandleFunc("POST /api/harness/command/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/approve") {
			s.handleApproveCommand(w, r)
			return
		}
		http.NotFound(w, r)
	})
	mux.HandleFunc("POST /api/harness/question/answer", s.handleResolveQuestionApproval)
	mux.HandleFunc("POST /api/harness/question/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/answer") {
			s.handleResolveQuestionApproval(w, r)
			return
		}
		http.NotFound(w, r)
	})
	mux.HandleFunc("POST /api/harness/plan/approve", s.handleResolvePlanApproval)
	mux.HandleFunc("POST /api/harness/plan/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/approve") {
			s.handleResolvePlanApproval(w, r)
			return
		}
		http.NotFound(w, r)
	})

	// 9. Workflows CRUD
	mux.HandleFunc("GET /api/harness/workflows", s.handleListWorkflows)
	mux.HandleFunc("POST /api/harness/workflows", s.handleSaveWorkflow)
	mux.HandleFunc("DELETE /api/harness/workflows/{id}", s.handleDeleteWorkflow)
	mux.HandleFunc("DELETE /api/harness/workflows/", s.handleDeleteWorkflow)

	// 10. Autonomous Subagents CRUD & Run
	mux.HandleFunc("GET /api/harness/agents", s.handleListAgents)
	mux.HandleFunc("POST /api/harness/agents", s.handleSaveAgent)
	mux.HandleFunc("DELETE /api/harness/agents/{id}", s.handleDeleteAgent)
	mux.HandleFunc("POST /api/harness/agents/{id}/run", s.handleRunAgent)
	mux.HandleFunc("DELETE /api/harness/agents/", s.handleDeleteAgent)
	mux.HandleFunc("POST /api/harness/agents/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/run") {
			s.handleRunAgent(w, r)
			return
		}
		s.handleSaveAgent(w, r)
	})

	// 10b. Parallel Subagent Engine & Live Transcript API
	mux.HandleFunc("GET /api/harness/subagents", s.handleListSubagents)
	mux.HandleFunc("POST /api/harness/subagents/spawn", s.handleSpawnSubagents)
	mux.HandleFunc("POST /api/harness/subagents/approve", s.handleApproveSubagents)
	mux.HandleFunc("GET /api/harness/subagents/{id}", s.handleGetSubagent)
	mux.HandleFunc("POST /api/harness/subagents/{id}/message", s.handleMessageSubagent)
	mux.HandleFunc("POST /api/harness/subagents/{id}/abort", s.handleAbortSubagent)
	mux.HandleFunc("GET /api/harness/subagents/", s.handleListSubagents)
	mux.HandleFunc("POST /api/harness/subagents/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/spawn") {
			s.handleSpawnSubagents(w, r)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/approve") {
			s.handleApproveSubagents(w, r)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/message") {
			s.handleMessageSubagent(w, r)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/abort") {
			s.handleAbortSubagent(w, r)
			return
		}
		http.NotFound(w, r)
	})

	// 11. MCP Servers CRUD & Live Tool Discovery
	mux.HandleFunc("GET /api/harness/mcp", s.handleListMCP)
	mux.HandleFunc("POST /api/harness/mcp", s.handleSaveMCP)
	mux.HandleFunc("DELETE /api/harness/mcp/{name}", s.handleDeleteMCP)
	mux.HandleFunc("DELETE /api/harness/mcp/", s.handleDeleteMCP)
	mux.HandleFunc("POST /api/harness/mcp/refresh", s.handleRefreshMCP)

	// 12. Skills Library CRUD & GitHub Installer
	mux.HandleFunc("GET /api/harness/skills", s.handleListSkills)
	mux.HandleFunc("POST /api/harness/skills", s.handleSaveSkill)
	mux.HandleFunc("DELETE /api/harness/skills/{name}", s.handleDeleteSkills)
	mux.HandleFunc("DELETE /api/harness/skills", s.handleDeleteSkills)
	mux.HandleFunc("DELETE /api/harness/skills/", s.handleDeleteSkills)
	mux.HandleFunc("POST /api/harness/skills/install-github", s.handleInstallGitHubSkill)

	// 13. Auto-Updater & Release Changelog Engine
	mux.HandleFunc("GET /api/harness/updates", s.handleGetUpdates)
	mux.HandleFunc("GET /api/harness/updates/", s.handleGetUpdates)
	mux.HandleFunc("POST /api/harness/updates/check", s.handleCheckUpdates)
	mux.HandleFunc("POST /api/harness/updates/apply", s.handleApplyUpdate)
	mux.HandleFunc("POST /api/harness/updates/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/check") {
			s.handleCheckUpdates(w, r)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/apply") {
			s.handleApplyUpdate(w, r)
			return
		}
		http.NotFound(w, r)
	})

	// 14. Persistent Cross-Session Memory
	mux.HandleFunc("GET /api/harness/memories", s.handleListMemories)
	mux.HandleFunc("GET /api/harness/memories/", s.handleListMemories)
	mux.HandleFunc("POST /api/harness/memories", s.handleSaveMemory)
	mux.HandleFunc("POST /api/harness/memories/", s.handleSaveMemory)
	mux.HandleFunc("DELETE /api/harness/memories/{key}", s.handleDeleteMemory)
	mux.HandleFunc("DELETE /api/harness/memories", s.handleDeleteMemory)
	mux.HandleFunc("DELETE /api/harness/memories/", s.handleDeleteMemory)

	// 15. v2.1.0 Frontier, Worktree, Watchdog, Security Duel & Prompt Modifiers
	mux.HandleFunc("POST /api/harness/diff/revert-hunk", s.handleRevertHunk)
	mux.HandleFunc("GET /api/harness/watchdog", s.handleWatchdog)
	mux.HandleFunc("GET /api/harness/custom-tools", s.handleListCustomTools)
	mux.HandleFunc("GET /api/harness/custom-tools/", s.handleListCustomTools)
	mux.HandleFunc("DELETE /api/harness/custom-tools/{name}", s.handleDeleteCustomTool)
	mux.HandleFunc("DELETE /api/harness/custom-tools", s.handleDeleteCustomTool)
	mux.HandleFunc("DELETE /api/harness/custom-tools/", s.handleDeleteCustomTool)
	mux.HandleFunc("POST /api/harness/security/duel", s.handleSecurityDuel)
	mux.HandleFunc("POST /api/harness/compact", s.handleCompact)
	mux.HandleFunc("GET /api/harness/modifiers", s.handlePromptModifiers)
	mux.HandleFunc("POST /api/harness/modifiers", s.handlePromptModifiers)

	// 16. Web Proxy & Interactive CDP Browser
	mux.HandleFunc("GET /api/harness/web-proxy", s.handleWebProxy)
	mux.HandleFunc("POST /api/harness/browser/action", s.handleBrowserAction)

	// 17. Persistent Task Scheduler & Automation Engine (v2.3.0)
	mux.HandleFunc("GET /api/schedules", s.handleListSchedules)
	mux.HandleFunc("GET /api/schedules/", s.handleListSchedules)
	mux.HandleFunc("POST /api/schedules", s.handleCreateSchedule)
	mux.HandleFunc("PUT /api/schedules/{id}", s.handleUpdateSchedule)
	mux.HandleFunc("DELETE /api/schedules/{id}", s.handleDeleteSchedule)
	mux.HandleFunc("POST /api/schedules/{id}/run", s.handleRunScheduleNow)
	mux.HandleFunc("POST /api/schedules/{id}/toggle", s.handleToggleSchedule)

	mux.HandleFunc("GET /api/harness/schedules", s.handleListSchedules)
	mux.HandleFunc("GET /api/harness/schedules/", s.handleListSchedules)
	mux.HandleFunc("POST /api/harness/schedules", s.handleCreateSchedule)
	mux.HandleFunc("PUT /api/harness/schedules/{id}", s.handleUpdateSchedule)
	mux.HandleFunc("DELETE /api/harness/schedules/{id}", s.handleDeleteSchedule)
	mux.HandleFunc("POST /api/harness/schedules/{id}/run", s.handleRunScheduleNow)
	mux.HandleFunc("POST /api/harness/schedules/{id}/toggle", s.handleToggleSchedule)

	return mux
}

// Start begins listening on Host:Port and serves HTTP requests.
func (s *Server) Start() error {
	if s.Scheduler != nil {
		s.Scheduler.Start(context.Background())
	}
	addr := fmt.Sprintf("%s:%d", s.Host, s.Port)
	s.httpServer = &http.Server{
		Addr:              addr,
		Handler:           s.corsMiddleware(s.authMiddleware(s.Routes())),
		ReadHeaderTimeout: 30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	return s.httpServer.ListenAndServe()
}

// Shutdown gracefully stops the HTTP server.
func (s *Server) Shutdown(ctx context.Context) error {
	if s.Scheduler != nil {
		s.Scheduler.Stop()
	}
	if s.httpServer != nil {
		return s.httpServer.Shutdown(ctx)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Route Handlers
// ---------------------------------------------------------------------------

func (s *Server) handleDesktopUI(w http.ResponseWriter, r *http.Request) {
	// First check if index.html exists on disk (for live dev hot-reloading)
	path := findDesktopHTML()
	if path != "" {
		if data, err := os.ReadFile(path); err == nil {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(data)
			return
		}
	}

	// Fallback to embedded index.html when deployed standalone without external files
	if len(embeddedDesktopHTML) > 0 {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(embeddedDesktopHTML)
		return
	}

	http.Error(w, "Unjess Desktop UI static files not found.", http.StatusNotFound)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	uptime := time.Since(s.StartTime).Seconds()
	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"version": updater.CurrentVersion + "-go",
		"engine":  "go",
		"uptime":  uptime,
	})
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":   "idle",
		"model":    s.Settings.Model,
		"provider": s.Settings.Provider,
		"engine":   "go",
	})
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	dict := config.SerializeSettingsDict(s.Settings)
	resp := make(map[string]any, len(dict)+2)
	for k, v := range dict {
		resp[k] = v
	}
	resp["status"] = "ok"
	resp["settings"] = dict
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleSaveSettings(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}
	if err := config.ApplySettingsUpdate(s.Settings, body); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.Harness.UpdateSettings(s.Settings)
	dict := config.SerializeSettingsDict(s.Settings)
	resp := make(map[string]any, len(dict)+2)
	for k, v := range dict {
		resp[k] = v
	}
	resp["status"] = "ok"
	resp["settings"] = dict
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	ollamaURL := r.URL.Query().Get("ollama_url")
	ollamaKey := r.URL.Query().Get("ollama_key")
	llamacppURL := r.URL.Query().Get("llamacpp_url")
	lmstudioURL := r.URL.Query().Get("lmstudio_url")

	cfg := s.Settings
	if llamacppURL != "" || lmstudioURL != "" {
		copyCfg := *s.Settings
		if llamacppURL != "" {
			copyCfg.LlamaCppBaseURL = llamacppURL
		}
		if lmstudioURL != "" {
			copyCfg.LMStudioBaseURL = lmstudioURL
		}
		cfg = &copyCfg
	}

	modelsData := config.DiscoverModels(cfg, ollamaURL, ollamaKey)
	writeJSON(w, http.StatusOK, modelsData)
}

func (s *Server) handleNetworkInfo(w http.ResponseWriter, r *http.Request) {
	netInfo := config.GetNetworkInfo(s.Settings, s.Port)
	writeJSON(w, http.StatusOK, netInfo)
}

func (s *Server) handleGetState(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Harness.GetFullStatePayload())
}

func (s *Server) handleGetLive(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Harness.GetLiveTurnState())
}

func (s *Server) handleGetUsage(w http.ResponseWriter, r *http.Request) {
	convID := r.URL.Query().Get("conv_id")
	if convID == "" {
		convID = r.URL.Query().Get("conversation_id")
	}
	if s.Harness == nil {
		writeError(w, http.StatusInternalServerError, "Harness not initialized")
		return
	}
	summary := s.Harness.GetUsageSummary(convID)
	writeJSON(w, http.StatusOK, summary)
}

func (s *Server) handleGetContext(w http.ResponseWriter, r *http.Request) {
	convID := r.URL.Query().Get("conv_id")
	if convID == "" {
		convID = r.URL.Query().Get("conversation_id")
	}
	modelOverride := r.URL.Query().Get("model")
	if s.Harness == nil {
		writeError(w, http.StatusInternalServerError, "Harness not initialized")
		return
	}
	usage := s.Harness.GetContextUsageWithModel(convID, modelOverride)
	writeJSON(w, http.StatusOK, usage)
}

func (s *Server) handleAbort(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Harness.AbortTurn())
}

func (s *Server) handleUndoTurn(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}
	convID := fmt.Sprint(body["conv_id"])
	if convID == "<nil>" {
		convID = ""
	}
	turnID := fmt.Sprint(body["turn_id"])
	if turnID == "<nil>" {
		turnID = ""
	}
	turnIndex := -1
	if ti, ok := body["turn_index"]; ok && ti != nil {
		switch v := ti.(type) {
		case float64:
			turnIndex = int(v)
		case int:
			turnIndex = v
		case int64:
			turnIndex = int(v)
		case string:
			if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
				turnIndex = n
			}
		}
	}

	res, err := s.Harness.UndoToTurn(convID, turnID, turnIndex)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleCreateConversation(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)

	title, _ := body["title"].(string)
	ws, _ := body["workspace"].(string)
	mode, _ := body["mode"].(string)

	res := s.Harness.CreateConversation(title, ws, mode)
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleDeleteConversation(w http.ResponseWriter, r *http.Request) {
	convID := r.PathValue("id")
	if convID == "" {
		p := strings.TrimPrefix(r.URL.Path, "/api/harness/conversations/")
		convID = strings.Trim(p, "/")
	}
	if convID == "" {
		writeError(w, http.StatusBadRequest, "conversation_id is required")
		return
	}
	res := s.Harness.DeleteConversation(convID)
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleClearConversation(w http.ResponseWriter, r *http.Request) {
	convID := r.PathValue("id")
	if convID == "" {
		p := strings.TrimPrefix(r.URL.Path, "/api/harness/conversations/")
		p = strings.TrimSuffix(p, "/clear")
		convID = strings.Trim(p, "/")
	}
	if convID == "" {
		writeError(w, http.StatusBadRequest, "conversation_id is required")
		return
	}
	res := s.Harness.ClearConversation(convID)
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleRenameConversation(w http.ResponseWriter, r *http.Request) {
	convID := r.PathValue("id")
	if convID == "" {
		p := strings.TrimPrefix(r.URL.Path, "/api/harness/conversations/")
		p = strings.TrimSuffix(p, "/rename")
		convID = strings.Trim(p, "/")
	}
	if convID == "" {
		writeError(w, http.StatusBadRequest, "conversation_id is required")
		return
	}
	var body map[string]any
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	title, _ := body["title"].(string)
	res := s.Harness.RenameConversation(convID, title)
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleAddWorkspace(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}
	wsPath, _ := body["path"].(string)
	if wsPath == "" {
		wsPath, _ = body["name"].(string)
	}
	wsPath = strings.TrimSpace(wsPath)
	if wsPath == "" {
		writeError(w, http.StatusBadRequest, "path or name is required")
		return
	}
	name, _ := body["name"].(string)
	res, err := s.Harness.AddWorkspace(wsPath, name)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleSelectWorkspace(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}
	wsPath, _ := body["workspace"].(string)
	if wsPath == "" {
		wsPath, _ = body["path"].(string)
	}
	if wsPath == "" {
		wsPath, _ = body["name"].(string)
	}
	wsPath = strings.TrimSpace(wsPath)
	if wsPath == "" {
		writeError(w, http.StatusBadRequest, "workspace path is required")
		return
	}
	res, err := s.Harness.SelectWorkspace(wsPath)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleBrowseWorkspace(w http.ResponseWriter, r *http.Request) {
	chosen, err := workspace.BrowseNativeFolder()
	if err != nil || strings.TrimSpace(chosen) == "" {
		writeJSON(w, http.StatusOK, map[string]any{
			"status": "cancelled",
			"path":   "",
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok",
		"path":   chosen,
	})
}

func (s *Server) handleListFiles(w http.ResponseWriter, r *http.Request) {
	ws := r.URL.Query().Get("workspace")
	if strings.TrimSpace(ws) == "" {
		ws = s.Harness.ActiveWorkspace()
	}
	if strings.TrimSpace(ws) == "" {
		ws = "."
	}
	files := workspace.ListWorkspaceFiles(ws, 400)
	gitInfo := workspace.GetGitInfo(ws)
	writeJSON(w, http.StatusOK, map[string]any{
		"status":    "ok",
		"workspace": ws,
		"files":     files,
		"git_info":  gitInfo,
	})
}

func (s *Server) handleReadFile(w http.ResponseWriter, r *http.Request) {
	ws := r.URL.Query().Get("workspace")
	if strings.TrimSpace(ws) == "" {
		ws = s.Harness.ActiveWorkspace()
	}
	relPath := r.URL.Query().Get("path")
	if strings.TrimSpace(relPath) == "" {
		writeError(w, http.StatusBadRequest, "path is required")
		return
	}
	safePath, err := workspace.ResolveSafePath(ws, relPath)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	data, err := os.ReadFile(safePath)
	if err != nil {
		writeError(w, http.StatusNotFound, fmt.Sprintf("File not found: %s", relPath))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"path":    relPath,
		"content": string(data),
		"size":    len(data),
	})
}

func (s *Server) handleWriteFile(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}
	ws, _ := body["workspace"].(string)
	if strings.TrimSpace(ws) == "" {
		ws = s.Harness.ActiveWorkspace()
	}
	relPath, _ := body["path"].(string)
	relPath = strings.TrimSpace(relPath)
	if relPath == "" {
		writeError(w, http.StatusBadRequest, "path is required")
		return
	}
	content, _ := body["content"].(string)
	safePath, err := workspace.ResolveSafePath(ws, relPath)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := os.MkdirAll(filepath.Dir(safePath), 0755); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := os.WriteFile(safePath, []byte(content), 0644); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok",
		"path":   relPath,
		"size":   len(content),
	})
}

func (s *Server) handleDeleteFile(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)

	ws, _ := body["workspace"].(string)
	if strings.TrimSpace(ws) == "" {
		ws = r.URL.Query().Get("workspace")
	}
	if strings.TrimSpace(ws) == "" {
		ws = s.Harness.ActiveWorkspace()
	}
	relPath, _ := body["path"].(string)
	if strings.TrimSpace(relPath) == "" {
		relPath = r.URL.Query().Get("path")
	}
	relPath = strings.TrimSpace(relPath)
	if relPath == "" {
		writeError(w, http.StatusBadRequest, "path is required")
		return
	}
	safePath, err := workspace.ResolveSafePath(ws, relPath)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := os.RemoveAll(safePath); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "deleted",
		"path":   relPath,
	})
}

func (s *Server) handleRawFile(w http.ResponseWriter, r *http.Request) {
	ws := r.URL.Query().Get("workspace")
	if strings.TrimSpace(ws) == "" {
		ws = s.Harness.ActiveWorkspace()
	}
	relPath := r.URL.Query().Get("path")
	if strings.TrimSpace(relPath) == "" {
		relPath = "index.html"
	}
	safePath, err := workspace.ResolveSafePath(ws, relPath)
	if err != nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(fmt.Sprintf("<h3>Access denied: %v</h3>", err)))
		return
	}
	fi, err := os.Stat(safePath)
	if err != nil || fi.IsDir() {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(fmt.Sprintf("<h3>File not found in workspace: %s</h3>", relPath)))
		return
	}
	data, err := os.ReadFile(safePath)
	if err != nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(fmt.Sprintf("<h3>Error reading file: %v</h3>", err)))
		return
	}
	contentType := getMimeType(safePath, data)
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (s *Server) handleExec(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}
	cmd, _ := body["command"].(string)
	ws, _ := body["workspace"].(string)
	if strings.TrimSpace(ws) == "" {
		ws = s.Harness.ActiveWorkspace()
	}
	if strings.TrimSpace(ws) == "" {
		ws = "."
	}
	timeout := s.Settings.CommandTimeout
	if timeout <= 0 {
		timeout = 120
	}
	executor := tools.NewToolExecutor(ws, timeout)
	out, success := executor.Execute(r.Context(), "run_command", map[string]any{"command": cmd})
	rc := 0
	if !success {
		rc = 1
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":     "ok",
		"command":    cmd,
		"stdout":     out,
		"stderr":     "",
		"output":     out,
		"returncode": rc,
		"cwd":        ws,
	})
}

func (s *Server) handleSetAgentMode(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}
	mode := ""
	if m, ok := body["agent_mode"].(string); ok && m != "" {
		mode = m
	} else if m, ok := body["mode"].(string); ok {
		mode = m
	}
	res := s.Harness.SetAgentMode(mode)

	var cavemanPtr *bool
	if cv, ok := body["caveman_mode"].(bool); ok {
		cavemanPtr = &cv
	}
	var ponytailPtr *bool
	if pv, ok := body["ponytail_mode"].(bool); ok {
		ponytailPtr = &pv
	}
	if cavemanPtr != nil || ponytailPtr != nil {
		modRes := s.Harness.SetPromptModifiers(cavemanPtr, ponytailPtr)
		for k, v := range modRes {
			if k != "status" {
				res[k] = v
			}
		}
	} else {
		liveSnap := s.Harness.GetLiveTurnState()
		res["caveman_mode"] = liveSnap["caveman_mode"]
		res["ponytail_mode"] = liveSnap["ponytail_mode"]
	}

	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleApproveCommand(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}
	approved, _ := body["approved"].(bool)
	switchToAutonomous, _ := body["switch_to_autonomous"].(bool)
	if modeStr, ok := body["agent_mode"].(string); ok && strings.EqualFold(modeStr, "autonomous") {
		switchToAutonomous = true
	} else if m, ok := body["mode"].(string); ok && strings.EqualFold(m, "autonomous") {
		switchToAutonomous = true
	}
	res := s.Harness.ResolveCommandApprovalWithMode(approved, switchToAutonomous)
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleResolveQuestionApproval(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}
	skipped, _ := body["skipped"].(bool)
	var answersStr string
	if ans, ok := body["answers"].(string); ok {
		answersStr = ans
	} else if ansMap, ok := body["answers"].(map[string]any); ok {
		b, _ := json.Marshal(ansMap)
		answersStr = string(b)
	} else if ansList, ok := body["answers"].([]any); ok {
		var parts []string
		for _, a := range ansList {
			parts = append(parts, fmt.Sprint(a))
		}
		answersStr = strings.Join(parts, "\n")
	} else if a, ok := body["answer"].(string); ok {
		answersStr = a
	}
	res := s.Harness.ResolveQuestionApproval(answersStr, skipped)
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleResolvePlanApproval(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}
	approved, _ := body["approved"].(bool)
	feedback, _ := body["feedback"].(string)
	res := s.Harness.ResolvePlanApproval(approved, feedback)
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	// Max request body: 100MB for 4K images & attachments
	r.Body = http.MaxBytesReader(w, r.Body, 100<<20)

	var raw map[string]any
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}

	var req agent.ChatTurnRequest
	if p, ok := raw["prompt"].(string); ok && p != "" {
		req.Prompt = p
	} else if m, ok := raw["message"].(string); ok {
		req.Prompt = m
	}

	if c, ok := raw["conversation_id"].(string); ok {
		req.ConversationID = c
	}
	if m, ok := raw["model"].(string); ok {
		req.Model = m
	}
	if prv, ok := raw["provider"].(string); ok && prv != "" {
		req.Provider = prv
	}
	if ws, ok := raw["workspace"].(string); ok {
		req.Workspace = ws
	}
	if wf, ok := raw["workflow_id"].(string); ok {
		req.WorkflowID = wf
	}
	if a, ok := raw["agent_id"].(string); ok && a != "" {
		req.AgentID = a
	} else if sa, ok := raw["subagent_id"].(string); ok {
		req.AgentID = sa
	}
	if am, ok := raw["agent_mode"].(string); ok && am != "" {
		req.AgentMode = am
	} else if m, ok := raw["mode"].(string); ok {
		req.AgentMode = m
	}

	if re, ok := raw["reasoning_effort"].(string); ok && strings.TrimSpace(re) != "" {
		req.ReasoningEffort = strings.TrimSpace(re)
	} else if re, ok := raw["effort"].(string); ok && strings.TrimSpace(re) != "" {
		req.ReasoningEffort = strings.TrimSpace(re)
	}

	if cv, ok := raw["caveman_mode"].(bool); ok {
		req.CavemanMode = cv
		s.Harness.SetPromptModifiers(&cv, nil)
	}
	if pv, ok := raw["ponytail_mode"].(bool); ok {
		req.PonytailMode = pv
		s.Harness.SetPromptModifiers(nil, &pv)
	}

	if tMap, ok := raw["tools"].(map[string]any); ok {
		req.Tools = make(map[string]bool)
		for k, v := range tMap {
			if b, ok := v.(bool); ok {
				req.Tools[k] = b
			}
		}
	}

	if imgs, ok := raw["images"].([]any); ok {
		for _, it := range imgs {
			if m, ok := it.(map[string]any); ok {
				req.Images = append(req.Images, m)
			}
		}
	}
	if atts, ok := raw["attachments"].([]any); ok {
		for _, it := range atts {
			if m, ok := it.(map[string]any); ok {
				req.Attachments = append(req.Attachments, m)
			}
		}
	}

	req.Prompt = strings.TrimSpace(req.Prompt)
	if req.Prompt == "" && (len(req.Images) > 0 || len(req.Attachments) > 0) {
		req.Prompt = "Please analyze the attached file(s)/image(s)."
	}
	if req.Prompt == "" {
		writeError(w, http.StatusBadRequest, "prompt is required")
		return
	}

	// Use context.Background() as parent so an HTTP client/WebView2 socket timeout does not kill a long streaming LLM turn;
	// user cancellation is handled explicitly via POST /api/harness/abort (live.CancelFunc).
	res, err := s.Harness.RunAgentTurn(context.Background(), req)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleListWorkflows(w http.ResponseWriter, r *http.Request) {
	workflows := s.Harness.ListWorkflows()
	if workflows == nil {
		workflows = []map[string]any{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":    "ok",
		"workflows": workflows,
	})
}

func (s *Server) handleSaveWorkflow(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}
	res := s.Harness.SaveWorkflow(body)
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleDeleteWorkflow(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		p := strings.TrimPrefix(r.URL.Path, "/api/harness/workflows/")
		id = strings.Trim(p, "/")
	}
	if id == "" {
		writeError(w, http.StatusBadRequest, "workflow id is required")
		return
	}
	res := s.Harness.DeleteWorkflow(id)
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleListAgents(w http.ResponseWriter, r *http.Request) {
	agentsList := s.Harness.ListAgents()
	if agentsList == nil {
		agentsList = []map[string]any{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok",
		"agents": agentsList,
	})
}

func (s *Server) handleSaveAgent(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}
	res := s.Harness.SaveAgent(body)
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleDeleteAgent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		p := strings.TrimPrefix(r.URL.Path, "/api/harness/agents/")
		id = strings.Trim(p, "/")
	}
	if id == "" {
		writeError(w, http.StatusBadRequest, "agent id is required")
		return
	}
	res := s.Harness.DeleteAgent(id)
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleRunAgent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		p := strings.TrimPrefix(r.URL.Path, "/api/harness/agents/")
		p = strings.TrimSuffix(p, "/run")
		id = strings.Trim(p, "/")
	}
	if id == "" {
		writeError(w, http.StatusBadRequest, "agent id is required")
		return
	}

	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}
	prompt, _ := body["prompt"].(string)
	if prompt == "" {
		prompt, _ = body["task"].(string)
	}
	model, _ := body["model"].(string)
	ws, _ := body["workspace"].(string)
	if strings.TrimSpace(ws) == "" {
		ws = s.Harness.ActiveWorkspace()
	}

	res, err := s.Harness.RunSubagentTask(r.Context(), id, prompt, model, ws)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleListSubagents(w http.ResponseWriter, r *http.Request) {
	convID := r.URL.Query().Get("conv_id")
	subs := s.Harness.ListSubagentRuns(convID)
	if subs == nil {
		subs = []map[string]any{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":    "ok",
		"subagents": subs,
	})
}

func (s *Server) handleSpawnSubagents(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}
	convID := fmt.Sprint(body["conv_id"])
	if convID == "" || convID == "<nil>" {
		convID = s.Harness.ActiveConversationID()
	}
	ws := fmt.Sprint(body["workspace"])
	if ws == "" || ws == "<nil>" {
		ws = s.Harness.ActiveWorkspace()
	}

	var specs []map[string]any
	if rawSub, ok := body["subagents"].([]any); ok {
		for _, item := range rawSub {
			if m, ok := item.(map[string]any); ok {
				specs = append(specs, m)
			}
		}
	} else if rawSubMap, ok := body["subagents"].([]map[string]any); ok {
		specs = rawSubMap
	} else if prompt, ok := body["prompt"].(string); ok && strings.TrimSpace(prompt) != "" {
		specs = append(specs, body)
	}

	if len(specs) == 0 {
		writeError(w, http.StatusBadRequest, "No subagent specifications provided")
		return
	}

	runs := s.Harness.SpawnSubagents(context.Background(), convID, ws, specs, false)
	serialized := make([]map[string]any, 0, len(runs))
	for _, r := range runs {
		serialized = append(serialized, s.Harness.SerializeSubagentRun(r))
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":    "ok",
		"subagents": serialized,
	})
}

func (s *Server) handleGetSubagent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		p := strings.TrimPrefix(r.URL.Path, "/api/harness/subagents/")
		id = strings.Trim(p, "/")
	}
	if id == "" {
		writeError(w, http.StatusBadRequest, "subagent id is required")
		return
	}
	sub, found := s.Harness.GetSubagentRun(id)
	if !found {
		writeError(w, http.StatusNotFound, fmt.Sprintf("subagent '%s' not found", id))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":   "ok",
		"subagent": sub,
	})
}

func (s *Server) handleMessageSubagent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		p := strings.TrimPrefix(r.URL.Path, "/api/harness/subagents/")
		p = strings.TrimSuffix(p, "/message")
		id = strings.Trim(p, "/")
	}
	if id == "" {
		writeError(w, http.StatusBadRequest, "subagent id is required")
		return
	}

	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}
	message := strings.TrimSpace(fmt.Sprint(body["message"]))
	if message == "" || message == "<nil>" {
		message = strings.TrimSpace(fmt.Sprint(body["prompt"]))
	}
	if message == "" || message == "<nil>" {
		writeError(w, http.StatusBadRequest, "message or prompt is required")
		return
	}
	modelOverride := strings.TrimSpace(fmt.Sprint(body["model"]))
	if modelOverride == "<nil>" {
		modelOverride = ""
	}

	sub, err := s.Harness.SendSubagentMessage(r.Context(), id, message, modelOverride)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":   "ok",
		"subagent": sub,
	})
}

func (s *Server) handleAbortSubagent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		p := strings.TrimPrefix(r.URL.Path, "/api/harness/subagents/")
		p = strings.TrimSuffix(p, "/abort")
		id = strings.Trim(p, "/")
	}
	if id == "" {
		writeError(w, http.StatusBadRequest, "subagent id is required")
		return
	}
	res := s.Harness.AbortSubagentRun(id)
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleApproveSubagents(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}
	approved, _ := body["approved"].(bool)
	var specs []map[string]any
	if rawSpecs, ok := body["specs"].([]any); ok {
		for _, item := range rawSpecs {
			if m, ok := item.(map[string]any); ok {
				specs = append(specs, m)
			}
		}
	} else if rawSpecsMap, ok := body["specs"].([]map[string]any); ok {
		specs = rawSpecsMap
	}

	res := s.Harness.ResolveSubagentApproval(specs, approved)
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleListMCP(w http.ResponseWriter, r *http.Request) {
	servers, err := mcp.ListMCPServers()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if servers == nil {
		servers = []mcp.ServerConfig{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"servers": servers,
		"presets": mcp.MCPPresets,
	})
}

func (s *Server) handleSaveMCP(w http.ResponseWriter, r *http.Request) {
	var cfg mcp.ServerConfig
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}
	if strings.TrimSpace(cfg.Name) == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if err := mcp.SaveMCPServer(cfg); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok",
		"server": cfg,
	})
}

func (s *Server) handleDeleteMCP(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		p := strings.TrimPrefix(r.URL.Path, "/api/harness/mcp/")
		name = strings.Trim(p, "/")
	}
	if name == "" {
		name = r.URL.Query().Get("name")
	}
	if name == "" && r.Body != nil {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err == nil {
			name, _ = body["name"].(string)
		}
	}
	name = strings.TrimSpace(name)
	if name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if err := mcp.DeleteMCPServer(name); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"deleted": name,
	})
}

func (s *Server) handleRefreshMCP(w http.ResponseWriter, r *http.Request) {
	res := mcp.RefreshMCPTools()
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleListSkills(w http.ResponseWriter, r *http.Request) {
	_ = skills.EnsureBuiltinSkillsSeeded()
	ws := r.URL.Query().Get("workspace")
	if strings.TrimSpace(ws) == "" {
		ws = s.Harness.ActiveWorkspace()
	}
	skillList := skills.DiscoverSkills(ws)
	if skillList == nil {
		skillList = []skills.SkillInfo{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok",
		"skills": skillList,
	})
}

func (s *Server) handleSaveSkill(w http.ResponseWriter, r *http.Request) {
	var req skills.SkillSaveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}
	if req.Workspace == "" {
		req.Workspace = r.URL.Query().Get("workspace")
	}
	if req.Workspace == "" {
		req.Workspace = s.Harness.ActiveWorkspace()
	}
	saved, err := skills.SaveSkill(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok",
		"skill":  saved,
	})
}

func (s *Server) handleDeleteSkills(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		p := strings.TrimPrefix(r.URL.Path, "/api/harness/skills/")
		if p != r.URL.Path && !strings.Contains(p, "/") {
			name = strings.Trim(p, "/")
		}
	}
	if name == "" {
		name = r.URL.Query().Get("name")
	}
	pathParam := r.URL.Query().Get("path")
	ws := r.URL.Query().Get("workspace")
	if strings.TrimSpace(ws) == "" {
		ws = s.Harness.ActiveWorkspace()
	}

	if name == "" && pathParam == "" && r.Body != nil {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err == nil {
			if n, ok := body["name"].(string); ok {
				name = strings.TrimSpace(n)
			}
			if p, ok := body["path"].(string); ok {
				pathParam = strings.TrimSpace(p)
			}
		}
	}

	deleted, err := skills.DeleteSkill(name, pathParam, ws)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if deleted == nil {
		deleted = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"deleted": deleted,
	})
}

func (s *Server) handleInstallGitHubSkill(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}
	rawURL, _ := body["url"].(string)
	if strings.TrimSpace(rawURL) == "" {
		rawURL, _ = body["repo_url"].(string)
	}
	if strings.TrimSpace(rawURL) == "" {
		rawURL, _ = body["repo"].(string)
	}
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		writeError(w, http.StatusBadRequest, "url is required")
		return
	}
	res, err := skills.InstallFromGitHub(rawURL)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// ---------------------------------------------------------------------------
// Auto-Updater Handlers
// ---------------------------------------------------------------------------

func (s *Server) handleGetUpdates(w http.ResponseWriter, r *http.Request) {
	updateURL := ""
	autoUpdateEnabled := true
	if s.Settings != nil {
		updateURL = s.Settings.UpdateServerURL
		autoUpdateEnabled = s.Settings.AutoUpdateEnabled
	}
	res := updater.CheckForUpdates(updateURL, autoUpdateEnabled)
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleCheckUpdates(w http.ResponseWriter, r *http.Request) {
	updateURL := ""
	autoUpdateEnabled := true
	if s.Settings != nil {
		updateURL = s.Settings.UpdateServerURL
		autoUpdateEnabled = s.Settings.AutoUpdateEnabled
	}

	if r.Body != nil {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err == nil && body != nil {
			if customURL, ok := body["update_server_url"].(string); ok && strings.TrimSpace(customURL) != "" {
				updateURL = strings.TrimSpace(customURL)
			}
			if b, ok := body["auto_update_enabled"].(bool); ok {
				autoUpdateEnabled = b
			}
		}
	}

	res := updater.CheckForUpdates(updateURL, autoUpdateEnabled)
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleApplyUpdate(w http.ResponseWriter, r *http.Request) {
	updateURL := ""
	if s.Settings != nil {
		updateURL = s.Settings.UpdateServerURL
	}

	if r.Body != nil {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err == nil && body != nil {
			if customURL, ok := body["update_server_url"].(string); ok && strings.TrimSpace(customURL) != "" {
				updateURL = strings.TrimSpace(customURL)
			}
		}
	}

	res, err := updater.ApplyUpdate(updateURL)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to apply update: %v", err))
		return
	}
	writeJSON(w, http.StatusOK, res)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}

	if restarting, _ := res["restarting"].(bool); restarting {
		targetExe, _ := res["target_exe"].(string)
		if targetExe != "" {
			go func(tExe string) {
				time.Sleep(500 * time.Millisecond)
				_ = updater.RestartProcess(tExe)
			}(targetExe)
		}
	}
}

// ---------------------------------------------------------------------------
// Persistent Cross-Session Memory Handlers
// ---------------------------------------------------------------------------

func (s *Server) handleListMemories(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("q")
	if query == "" {
		query = r.URL.Query().Get("query")
	}
	category := r.URL.Query().Get("category")
	workspace := r.URL.Query().Get("workspace")
	if workspace == "" && s.Harness != nil {
		workspace = s.Harness.ActiveWorkspace()
	}

	entries := memory.SearchMemories(query, category, workspace)
	if entries == nil {
		entries = make([]memory.MemoryEntry, 0)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":   "ok",
		"count":    len(entries),
		"memories": entries,
	})
}

func (s *Server) handleSaveMemory(w http.ResponseWriter, r *http.Request) {
	if r.Body == nil {
		writeError(w, http.StatusBadRequest, "request body is required")
		return
	}

	var req struct {
		Key       string   `json:"key"`
		Category  string   `json:"category"`
		Content   string   `json:"content"`
		Workspace string   `json:"workspace"`
		Scope     string   `json:"scope"`
		Tags      []string `json:"tags"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid JSON body: %v", err))
		return
	}

	key := strings.TrimSpace(req.Key)
	if key == "" {
		writeError(w, http.StatusBadRequest, "key is required")
		return
	}
	content := strings.TrimSpace(req.Content)
	if content == "" {
		writeError(w, http.StatusBadRequest, "content is required")
		return
	}

	ws := strings.TrimSpace(req.Workspace)
	if ws == "" && strings.EqualFold(req.Scope, "workspace") && s.Harness != nil {
		ws = s.Harness.ActiveWorkspace()
	}

	entry, err := memory.SaveMemory(key, req.Category, content, ws, req.Tags)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed saving memory: %v", err))
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok",
		"memory": entry,
	})
}

func (s *Server) handleDeleteMemory(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if key == "" {
		key = r.URL.Query().Get("key")
	}
	if key == "" {
		key = r.URL.Query().Get("id")
	}
	if key == "" && strings.HasPrefix(r.URL.Path, "/api/harness/memories/") {
		key = strings.TrimPrefix(r.URL.Path, "/api/harness/memories/")
		key = strings.TrimSuffix(key, "/")
	}

	if key == "" && r.Body != nil {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err == nil && body != nil {
			if k, ok := body["key"].(string); ok {
				key = k
			} else if id, ok := body["id"].(string); ok {
				key = id
			}
		}
	}

	key = strings.TrimSpace(key)
	if key == "" {
		writeError(w, http.StatusBadRequest, "key or id is required")
		return
	}

	deleted, err := memory.DeleteMemory(key)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed deleting memory: %v", err))
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"deleted": deleted,
		"key":     key,
	})
}

// handleWebProxy proxies external websites for iframe rendering, stripping X-Frame-Options
// and CSP frame-ancestors, rewriting base href, and injecting a navigation bridge.
func (s *Server) handleWebProxy(w http.ResponseWriter, r *http.Request) {
	targetURL := strings.TrimSpace(r.URL.Query().Get("url"))
	if targetURL == "" {
		http.Error(w, "Missing 'url' query parameter", http.StatusBadRequest)
		return
	}
	if !strings.HasPrefix(targetURL, "http://") && !strings.HasPrefix(targetURL, "https://") {
		targetURL = "https://" + targetURL
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, targetURL, nil)
	if err != nil {
		http.Error(w, fmt.Sprintf("Invalid target URL: %v", err), http.StatusBadRequest)
		return
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8")

	client := &http.Client{
		Timeout: 20 * time.Second,
	}

	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to fetch target URL: %v", err), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	omittedHeaders := map[string]bool{
		"x-frame-options":                     true,
		"content-security-policy":             true,
		"content-security-policy-report-only": true,
		"cross-origin-opener-policy":          true,
		"cross-origin-embedder-policy":        true,
		"cross-origin-resource-policy":        true,
		"content-length":                      true,
		"content-encoding":                    true,
	}

	for k, vv := range resp.Header {
		if omittedHeaders[strings.ToLower(k)] {
			continue
		}
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.Header().Set("Access-Control-Allow-Origin", "*")

	contentType := resp.Header.Get("Content-Type")
	if strings.Contains(strings.ToLower(contentType), "text/html") {
		bodyBytes, err := io.ReadAll(resp.Body)
		if err != nil {
			http.Error(w, fmt.Sprintf("Failed to read response: %v", err), http.StatusBadGateway)
			return
		}

		finalURL := resp.Request.URL
		baseHref := finalURL.Scheme + "://" + finalURL.Host + "/"

		bridgeScript := fmt.Sprintf(`<script>
(function() {
	function notifyParent() {
		try {
			window.parent.postMessage({
				type: "unjess-browser-nav",
				url: %q,
				title: document.title || ""
			}, "*");
		} catch(e) {}
	}
	if (document.readyState === "loading") {
		document.addEventListener("DOMContentLoaded", notifyParent);
	} else {
		notifyParent();
	}

	document.addEventListener("click", function(e) {
		var anchor = e.target.closest("a");
		if (!anchor) return;
		var href = anchor.getAttribute("href");
		if (!href || href.startsWith("#") || href.startsWith("javascript:")) return;
		try {
			var resolved = new URL(href, document.baseURI).href;
			if (resolved.startsWith("http://") || resolved.startsWith("https://")) {
				e.preventDefault();
				window.location.href = "/api/harness/web-proxy?url=" + encodeURIComponent(resolved);
			}
		} catch(err) {}
	}, true);

	document.addEventListener("submit", function(e) {
		var form = e.target;
		var method = (form.getAttribute("method") || "GET").toUpperCase();
		if (method === "GET") {
			try {
				var action = form.getAttribute("action") || "";
				var resolved = new URL(action, document.baseURI);
				var formData = new FormData(form);
				formData.forEach(function(val, key) {
					resolved.searchParams.set(key, val);
				});
				e.preventDefault();
				window.location.href = "/api/harness/web-proxy?url=" + encodeURIComponent(resolved.href);
			} catch(err) {}
		}
	}, true);
})();
</script>`, finalURL.String())

		htmlStr := string(bodyBytes)
		injection := fmt.Sprintf("<base href=%q>\n%s\n", baseHref, bridgeScript)

		lower := strings.ToLower(htmlStr)
		if headIdx := strings.Index(lower, "<head"); headIdx != -1 {
			if closeIdx := strings.Index(htmlStr[headIdx:], ">"); closeIdx != -1 {
				pos := headIdx + closeIdx + 1
				htmlStr = htmlStr[:pos] + "\n" + injection + htmlStr[pos:]
			} else {
				htmlStr = injection + htmlStr
			}
		} else {
			htmlStr = injection + htmlStr
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write([]byte(htmlStr))
		return
	}

	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// handleBrowserAction executes interactive CDP browser automation actions.
func (s *Server) handleBrowserAction(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Workspace string `json:"workspace"`
		frontier.BrowserUseArgs
	}
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil && err != io.EOF {
			writeError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
			return
		}
	}

	wsPath := strings.TrimSpace(body.Workspace)
	if wsPath == "" && s.Harness != nil {
		wsPath = s.Harness.ActiveWorkspace()
	}

	ctx, cancel := context.WithTimeout(r.Context(), 35*time.Second)
	defer cancel()

	res, err := frontier.ExecuteBrowserUse(ctx, wsPath, body.BrowserUseArgs)
	errStr := ""
	if err != nil {
		errStr = err.Error()
	}

	visionDataURI := ""
	if res != nil {
		visionDataURI = res.VisionDataURI
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":          "ok",
		"result":          res,
		"vision_data_uri": visionDataURI,
		"error":           errStr,
	})
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": msg})
}

func findDesktopHTML() string {
	candidates := []string{
		"src/unjess/desktop/index.html",
		"../src/unjess/desktop/index.html",
		"../../src/unjess/desktop/index.html",
		"src/unjess/openclaw/index.html",
		"../src/unjess/openclaw/index.html",
	}

	if exe, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(exeDir, "src", "unjess", "desktop", "index.html"),
			filepath.Join(exeDir, "..", "src", "unjess", "desktop", "index.html"),
			filepath.Join(exeDir, "..", "..", "src", "unjess", "desktop", "index.html"),
			filepath.Join(exeDir, "src", "unjess", "openclaw", "index.html"),
			filepath.Join(exeDir, "..", "src", "unjess", "openclaw", "index.html"),
		)
	}

	for _, p := range candidates {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			if abs, err := filepath.Abs(p); err == nil {
				return abs
			}
			return p
		}
	}
	return ""
}

func getMimeType(filePath string, data []byte) string {
	ext := strings.ToLower(filepath.Ext(filePath))
	switch ext {
	case ".html", ".htm":
		return "text/html; charset=utf-8"
	case ".js", ".mjs":
		return "application/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".json":
		return "application/json; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".md", ".txt":
		return "text/plain; charset=utf-8"
	}
	if t := mime.TypeByExtension(ext); t != "" {
		return t
	}
	if len(data) > 0 {
		return http.DetectContentType(data)
	}
	return "application/octet-stream"
}

// -----------------------------------------------------------------------------
// v2.1.0 Handlers (Revert Hunk, Watchdog, Custom Tools, Security Duel, Compact)
// -----------------------------------------------------------------------------

func (s *Server) handleRevertHunk(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}
	relPath := getString(body, "path", "file_path", "file")
	if strings.TrimSpace(relPath) == "" {
		writeError(w, http.StatusBadRequest, "Missing required field: path")
		return
	}
	diffText := getString(body, "diff", "diff_text")
	hunkIndex := getInt(body, "hunk_index")

	ws := s.Harness.ActiveWorkspace()
	if ws == "" {
		ws = "."
	}

	updatedDiff, err := worktree.RevertHunkInFile(ws, relPath, hunkIndex, diffText)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("Failed to revert hunk: %v", err))
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":          "ok",
		"path":            relPath,
		"hunk_index":      hunkIndex,
		"updated_diff":    updatedDiff,
		"workspace_files": workspace.ListWorkspaceFiles(ws, 400),
		"git_info":        workspace.GetGitInfo(ws),
	})
}

func (s *Server) handleWatchdog(w http.ResponseWriter, r *http.Request) {
	ws := r.URL.Query().Get("workspace")
	if strings.TrimSpace(ws) == "" {
		ws = s.Harness.ActiveWorkspace()
	}
	if strings.TrimSpace(ws) == "" {
		ws = "."
	}
	status := frontier.CheckWorkspaceHealth(r.Context(), ws)
	writeJSON(w, http.StatusOK, map[string]any{
		"status":   "ok",
		"watchdog": status,
	})
}

func (s *Server) handleListCustomTools(w http.ResponseWriter, r *http.Request) {
	cTools, err := frontier.LoadCustomTools()
	if err != nil {
		cTools = []frontier.CustomTool{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":       "ok",
		"custom_tools": cTools,
	})
}

func (s *Server) handleDeleteCustomTool(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		name = r.URL.Query().Get("name")
	}
	if name == "" && strings.HasPrefix(r.URL.Path, "/api/harness/custom-tools/") {
		name = strings.TrimPrefix(r.URL.Path, "/api/harness/custom-tools/")
		name = strings.TrimSuffix(name, "/")
	}
	if name == "" && r.Body != nil {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		name = getString(body, "name", "tool_name")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		writeError(w, http.StatusBadRequest, "Missing required tool name")
		return
	}
	if err := frontier.DeleteCustomTool(name); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("Failed to delete custom tool: %v", err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"deleted": name,
	})
}

func (s *Server) handleSecurityDuel(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}

	targetURL := getString(body, "target_url", "url")
	ws := getString(body, "workspace")
	if strings.TrimSpace(ws) == "" {
		ws = s.Harness.ActiveWorkspace()
	}
	if strings.TrimSpace(ws) == "" {
		ws = "."
	}

	report, err := security.RunSecurityDuel(r.Context(), ws, targetURL)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("Security duel failed: %v", err))
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":           "ok",
		"report":           report,
		"summary_markdown": report.SummaryMarkdown,
	})
}

func (s *Server) handleCompact(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}

	convID := getString(body, "conv_id", "conversation_id", "id")
	conv, err := s.Harness.CompactConversation(convID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":       "ok",
		"conversation": conv,
	})
}

func (s *Server) handlePromptModifiers(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		res := s.Harness.SetPromptModifiers(nil, nil)
		writeJSON(w, http.StatusOK, res)
		return
	}
	var body map[string]any
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	var cPtr, pPtr *bool
	if v, ok := body["caveman_mode"].(bool); ok {
		c := v
		cPtr = &c
	}
	if v, ok := body["ponytail_mode"].(bool); ok {
		p := v
		pPtr = &p
	}
	res := s.Harness.SetPromptModifiers(cPtr, pPtr)
	writeJSON(w, http.StatusOK, res)
}

func getString(m map[string]any, keys ...string) string {
	if m == nil {
		return ""
	}
	for _, k := range keys {
		if v, ok := m[k]; ok && v != nil {
			s := strings.TrimSpace(fmt.Sprint(v))
			if s != "" && s != "<nil>" {
				return s
			}
		}
	}
	return ""
}

func getInt(m map[string]any, keys ...string) int {
	if m == nil {
		return 0
	}
	for _, k := range keys {
		if v, ok := m[k]; ok && v != nil {
			switch val := v.(type) {
			case int:
				return val
			case int64:
				return int(val)
			case float64:
				return int(val)
			case string:
				if n, err := strconv.Atoi(strings.TrimSpace(val)); err == nil {
					return n
				}
			}
		}
	}
	return 0
}

// -----------------------------------------------------------------------------
// Scheduler TaskExecutor & API Handlers (v2.3.0)
// -----------------------------------------------------------------------------

// ExecuteTask implements scheduler.TaskExecutor interface on Server.
func (s *Server) ExecuteTask(ctx context.Context, task *scheduler.ScheduledTask) (string, error) {
	ws := strings.TrimSpace(task.Workspace)
	if ws == "" {
		ws = s.Harness.ActiveWorkspace()
	}
	if ws == "" {
		ws = "."
	}

	switch strings.ToLower(task.Action) {
	case "watchdog":
		status := frontier.CheckWorkspaceHealth(ctx, ws)
		out := fmt.Sprintf("Watchdog run for %s: %s (healthy: %v, issues: %d)", ws, status.Summary, status.Healthy, len(status.Issues))
		if !status.Healthy && len(status.Issues) > 0 {
			return out, fmt.Errorf("watchdog found %d issues: %s", len(status.Issues), status.Summary)
		}
		return out, nil

	case "command":
		timeout := s.Settings.CommandTimeout
		if timeout <= 0 {
			timeout = 120
		}
		executor := tools.NewToolExecutor(ws, timeout)
		out, success := executor.Execute(ctx, "run_command", map[string]any{"command": task.Prompt})
		if !success {
			return out, fmt.Errorf("command execution failed: %s", out)
		}
		return out, nil

	default: // "prompt"
		req := agent.ChatTurnRequest{
			Prompt:          task.Prompt,
			Workspace:       ws,
			Model:           task.Model,
			ReasoningEffort: task.Effort,
		}
		res, err := s.Harness.RunAgentTurn(ctx, req)
		if err != nil {
			return "", err
		}
		if turn, ok := res["turn"].(map[string]any); ok {
			if txt, ok := turn["text"].(string); ok && txt != "" {
				return txt, nil
			}
		}
		return "Agent task completed successfully.", nil
	}
}

func (s *Server) handleListSchedules(w http.ResponseWriter, r *http.Request) {
	tasks := s.Scheduler.ListTasks()
	writeJSON(w, http.StatusOK, map[string]any{
		"status":    "ok",
		"schedules": tasks,
	})
}

func (s *Server) handleCreateSchedule(w http.ResponseWriter, r *http.Request) {
	var body scheduler.ScheduledTask
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}
	if strings.TrimSpace(body.Workspace) == "" {
		body.Workspace = s.Harness.ActiveWorkspace()
	}
	task, err := s.Scheduler.AddTask(body)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":   "ok",
		"schedule": task,
	})
}

func (s *Server) handleUpdateSchedule(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(parts) >= 3 {
			id = parts[2]
		}
	}
	var updates map[string]any
	if err := json.NewDecoder(r.Body).Decode(&updates); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}
	task, err := s.Scheduler.UpdateTask(id, updates)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":   "ok",
		"schedule": task,
	})
}

func (s *Server) handleDeleteSchedule(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(parts) >= 3 {
			id = parts[2]
		}
	}
	if !s.Scheduler.DeleteTask(id) {
		writeError(w, http.StatusNotFound, "schedule not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"deleted": true,
	})
}

func (s *Server) handleRunScheduleNow(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(parts) >= 4 && parts[len(parts)-1] == "run" {
			id = parts[len(parts)-2]
		}
	}
	task, err := s.Scheduler.RunTaskNow(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"status":   "error",
			"error":    err.Error(),
			"schedule": task,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":   "ok",
		"schedule": task,
	})
}

func (s *Server) handleToggleSchedule(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(parts) >= 4 && parts[len(parts)-1] == "toggle" {
			id = parts[len(parts)-2]
		}
	}
	task, err := s.Scheduler.ToggleTask(id)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":   "ok",
		"schedule": task,
	})
}
