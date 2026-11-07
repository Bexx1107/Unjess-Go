//! Event System & Streaming Protocol for Unjess Desktop
//!
//! Models the exact reactive UI states seen in the Unjess agentic interface:
//! Planning steps, file creations, deployments, streaming chunks, and active tools.

use chrono::{DateTime, Utc};
use serde::{Deserialize, Serialize};

/// Status of a planning step in an agent workflow.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum StepStatus {
    Pending,
    InProgress,
    Completed,
    Failed,
}

/// A specific planning step executed by the agent (e.g. "Analyzed project requirements").
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct PlanningStep {
    pub id: String,
    pub description: String,
    pub status: StepStatus,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub started_at: Option<DateTime<Utc>>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub completed_at: Option<DateTime<Utc>>,
}

/// An entry in the "Files created" section of the UI.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct CreatedFile {
    pub path: String,
    pub is_dir: bool,
    pub status: String, // e.g. "created", "modified", "deleted"
    pub size_bytes: Option<u64>,
}

/// A deployment output card (e.g. "Website is live at: https://client-website.vercel.app").
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct DeploymentInfo {
    pub provider: String, // e.g. "Vercel", "Netlify", "Cloudflare"
    pub url: String,
    pub status: String,
    pub deployed_at: DateTime<Utc>,
}

/// An active tool reported in the right inspector panel.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ToolDescriptor {
    pub id: String,
    pub name: String,
    pub description: String,
    pub icon: String,
    pub enabled: bool,
}

/// The high-level event emitted over WebSockets/SSE to drive the OpenClaw UI.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(tag = "type", content = "payload")]
#[serde(rename_all = "snake_case")]
pub enum AgentEvent {
    /// Incremental text output stream from the LLM.
    TextChunk { text: String },
    
    /// Agent internal reasoning / thinking process.
    Thinking { thought: String },
    
    /// Live planning step list update (renders the checklist).
    PlanningUpdate { steps: Vec<PlanningStep> },
    
    /// File created or modified in workspace (renders the tree).
    FilesCreated { files: Vec<CreatedFile> },
    
    /// Live deployment URL produced.
    Deployment { info: DeploymentInfo },
    
    /// Tool invocation started.
    ToolCall {
        call_id: String,
        name: String,
        arguments: serde_json::Value,
    },
    
    /// Tool invocation completed.
    ToolResult {
        call_id: String,
        name: String,
        output: String,
        success: bool,
    },
    
    /// Overall agent task status update.
    StatusChange {
        status: String, // "idle", "thinking", "planning", "executing", "completed"
        message: Option<String>,
    },
}
