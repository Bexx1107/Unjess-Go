package agent

import (
	"context"
	"sync"
	"time"

	"unjess-daemon/internal/config"
	"unjess-daemon/internal/tools"
)

// ChatTurnRequest represents the incoming JSON payload for POST /api/harness/chat.
type ChatTurnRequest struct {
	ConversationID  string           `json:"conversation_id"`
	Prompt          string           `json:"prompt"`
	Model           string           `json:"model"`
	ReasoningEffort string           `json:"reasoning_effort,omitempty"`
	Workspace       string           `json:"workspace"`
	WorkflowID      string           `json:"workflow_id"`
	AgentID         string           `json:"agent_id"`
	AgentMode       string           `json:"agent_mode"`
	CavemanMode     bool             `json:"caveman_mode"`
	PonytailMode    bool             `json:"ponytail_mode"`
	Tools           map[string]bool  `json:"tools"`
	Images          []map[string]any `json:"images"`
	Attachments     []map[string]any `json:"attachments"`
}

// SubagentStep represents a single chronological activity item in a subagent's live transcript
// (e.g., "Message from Parent Agent", "Thought for 12s", "Check agent.go", "Edited server.go +596 -0").
type SubagentStep struct {
	ID          string         `json:"id"`
	Type        string         `json:"type"` // "parent_message", "user_message", "thinking", "tool_call", "edit", "reply", "status"
	Title       string         `json:"title"`
	Content     string         `json:"content,omitempty"`
	ToolName    string         `json:"tool_name,omitempty"`
	Arguments   map[string]any `json:"arguments,omitempty"`
	Result      string         `json:"result,omitempty"`
	FilePath    string         `json:"file_path,omitempty"`
	Additions   int            `json:"additions,omitempty"`
	Deletions   int            `json:"deletions,omitempty"`
	Diff        string         `json:"diff,omitempty"`
	DurationSec float64        `json:"duration_s,omitempty"`
	Status      string         `json:"status,omitempty"` // "running", "completed", "error"
	Timestamp   string         `json:"timestamp"`
}

// SubagentRun tracks the live and persisted state of a spawned subagent instance.
type SubagentRun struct {
	mu            sync.RWMutex           `json:"-"`
	ID            string                 `json:"id"`
	ConvID        string                 `json:"conv_id"`
	Role          string                 `json:"role"`
	TypeName      string                 `json:"type_name"`
	Model         string                 `json:"model"`
	Provider      string                 `json:"provider"`
	Workspace     string                 `json:"workspace"`
	Prompt        string                 `json:"prompt"`
	Status        string                 `json:"status"` // "running", "completed", "error", "aborted"
	CurrentAction string                 `json:"current_action"`
	MaxTurns      int                    `json:"max_turns,omitempty"`
	StartedAt     int64                  `json:"started_at"`
	ElapsedSec    float64                `json:"elapsed_s"`
	Thinking      string                 `json:"thinking"`
	Text          string                 `json:"text"`
	Result        string                 `json:"result"`
	Steps         []SubagentStep         `json:"steps"`
	ToolCalls     []tools.ToolCallRecord `json:"tool_calls"`
	FilesTouched  []tools.FileTouch      `json:"files_touched"`
	Diffs         []tools.FileDiff       `json:"diffs"`
	CancelFunc    context.CancelFunc     `json:"-"`
	Executor      *tools.ToolExecutor    `json:"-"`
	 startedTime   time.Time              `json:"-"`
}

// PendingSubagentApproval holds an in-flight subagent spawn request requiring user confirmation.
type PendingSubagentApproval struct {
	ID           string           `json:"id"`
	Specs        []map[string]any `json:"specs"`
	DefaultModel string           `json:"default_model"`
	CreatedAt    int64            `json:"created_at"`
}

// PendingCommandApproval holds an in-flight command execution request requiring user confirmation in Supervised mode.
type PendingCommandApproval struct {
	ID        string         `json:"id"`
	ToolName  string         `json:"tool_name"`
	Command   string         `json:"command"`
	Arguments map[string]any `json:"arguments"`
	Workspace string         `json:"workspace"`
	CreatedAt int64          `json:"created_at"`
}

// PendingQuestionApproval holds an in-flight interactive question requiring user choice/answer.
type PendingQuestionApproval struct {
	ID        string           `json:"id"`
	Questions []map[string]any `json:"questions"`
	CreatedAt int64            `json:"created_at"`
}

// PendingPlanApproval holds an in-flight proposed plan requiring user approval/feedback.
type PendingPlanApproval struct {
	ID            string   `json:"id"`
	Title         string   `json:"title"`
	Summary       string   `json:"summary"`
	Steps         []string `json:"steps"`
	OpenQuestions []string `json:"open_questions"`
	CreatedAt     int64    `json:"created_at"`
}

// LiveTurn tracks the real-time streaming state of an in-progress agent turn.
type LiveTurn struct {
	Running                 bool                     `json:"running"`
	ConvID                  string                   `json:"conv_id"`
	StartedAt               time.Time                `json:"-"`
	Thinking                string                   `json:"thinking"`
	Text                    string                   `json:"text"`
	PlanningSteps           []map[string]any         `json:"planning_steps"`
	ToolCalls               []tools.ToolCallRecord   `json:"tool_calls"`
	FilesTouched            []tools.FileTouch        `json:"files_touched"`
	Diffs                   []tools.FileDiff         `json:"diffs"`
	Subagents               []*SubagentRun           `json:"subagents,omitempty"`
	PendingSubagentApproval *PendingSubagentApproval `json:"pending_subagent_approval,omitempty"`
	SubagentApprovalCh      chan []map[string]any    `json:"-"`
	PendingCommandApproval  *PendingCommandApproval  `json:"pending_command_approval,omitempty"`
	CommandApprovalCh       chan bool                `json:"-"`
	PendingQuestion         *PendingQuestionApproval `json:"pending_question,omitempty"`
	QuestionAnswerCh        chan string              `json:"-"`
	PendingPlan             *PendingPlanApproval     `json:"pending_plan,omitempty"`
	PlanApprovalCh          chan map[string]any      `json:"-"`
	PlanApproved            bool                     `json:"plan_approved"`
	AgentMode               string                   `json:"agent_mode,omitempty"`
	ReasoningEffort         string                   `json:"reasoning_effort,omitempty"`
	CavemanMode             bool                     `json:"caveman_mode,omitempty"`
	PonytailMode            bool                     `json:"ponytail_mode,omitempty"`
	Aborted                 bool                     `json:"aborted"`
	CancelFunc              context.CancelFunc       `json:"-"`
	Executor                *tools.ToolExecutor      `json:"-"`
}

// HarnessManager manages persistent state (~/.unjess/harness_state.json),
// workspaces, conversations, workflows, custom subagents, and multi-turn agent execution.
type HarnessManager struct {
	mu           sync.RWMutex
	liveMu       sync.RWMutex
	subagentsMu  sync.RWMutex
	settings     *config.Settings
	agentMode    string
	cavemanMode  bool
	ponytailMode bool
	statePath    string
	state        map[string]any
	liveTurn     *LiveTurn
	subagentRuns []*SubagentRun
}
