//! Agent Execution Session and State Engine
//!
//! Maintains active conversation context, planning stages, tool tracking,
//! and event dispatching for the Unjess agent desktop UI.

use crate::events::{AgentEvent, CreatedFile, DeploymentInfo, PlanningStep, StepStatus, ToolDescriptor};
use crate::tools::ToolEngine;
use crate::workspace::Workspace;
use chrono::Utc;
use serde::{Deserialize, Serialize};
use tokio::sync::mpsc;

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct AgentConfig {
    pub model: String,
    pub autonomous: bool,
    pub workspace_path: String,
    pub context_mode: String, // e.g. "Auto"
}

impl Default for AgentConfig {
    fn default() -> Self {
        Self {
            model: "GPT-5".to_string(),
            autonomous: true,
            workspace_path: "client-website".to_string(),
            context_mode: "Auto".to_string(),
        }
    }
}

pub struct AgentSession {
    pub config: AgentConfig,
    pub tools: ToolEngine,
    pub planning_steps: Vec<PlanningStep>,
    pub created_files: Vec<CreatedFile>,
    pub deployment: Option<DeploymentInfo>,
    pub active_tools: Vec<ToolDescriptor>,
    event_tx: Option<mpsc::UnboundedSender<AgentEvent>>,
}

impl AgentSession {
    pub fn new(config: AgentConfig, workspace: Workspace) -> Self {
        let tools = ToolEngine::new(workspace);
        let active_tools = vec![
            ToolDescriptor {
                id: "filesystem".to_string(),
                name: "File system".to_string(),
                description: "Read, write, edit files".to_string(),
                icon: "folder".to_string(),
                enabled: true,
            },
            ToolDescriptor {
                id: "terminal".to_string(),
                name: "Terminal".to_string(),
                description: "Run shell commands".to_string(),
                icon: "terminal".to_string(),
                enabled: true,
            },
            ToolDescriptor {
                id: "browser".to_string(),
                name: "Browser".to_string(),
                description: "Search the web, view pages".to_string(),
                icon: "globe".to_string(),
                enabled: true,
            },
            ToolDescriptor {
                id: "git".to_string(),
                name: "Git".to_string(),
                description: "Manage version control".to_string(),
                icon: "git-branch".to_string(),
                enabled: true,
            },
            ToolDescriptor {
                id: "linter".to_string(),
                name: "Linter & Tests".to_string(),
                description: "Run tests, typecheck & format".to_string(),
                icon: "check-circle".to_string(),
                enabled: true,
            },
        ];

        Self {
            config,
            tools,
            planning_steps: Vec::new(),
            created_files: Vec::new(),
            deployment: None,
            active_tools,
            event_tx: None,
        }
    }

    pub fn set_event_sender(&mut self, tx: mpsc::UnboundedSender<AgentEvent>) {
        self.event_tx = Some(tx);
    }

    pub fn emit(&self, event: AgentEvent) {
        if let Some(ref tx) = self.event_tx {
            let _ = tx.send(event);
        }
    }

    /// Add a sequence of planned steps (e.g. from the planner LLM)
    pub fn set_plan(&mut self, descriptions: Vec<&str>) {
        self.planning_steps = descriptions
            .into_iter()
            .enumerate()
            .map(|(i, desc)| PlanningStep {
                id: format!("step-{}", i + 1),
                description: desc.to_string(),
                status: StepStatus::Pending,
                started_at: None,
                completed_at: None,
            })
            .collect();

        self.emit(AgentEvent::PlanningUpdate {
            steps: self.planning_steps.clone(),
        });
    }

    /// Mark a step as completed
    pub fn complete_step(&mut self, index: usize) {
        if let Some(step) = self.planning_steps.get_mut(index) {
            step.status = StepStatus::Completed;
            step.completed_at = Some(Utc::now());
        }
        self.emit(AgentEvent::PlanningUpdate {
            steps: self.planning_steps.clone(),
        });
    }

    /// Register a newly created or modified file
    pub fn record_file(&mut self, path: &str, is_dir: bool, status: &str) {
        let entry = CreatedFile {
            path: path.to_string(),
            is_dir,
            status: status.to_string(),
            size_bytes: None,
        };
        self.created_files.push(entry);
        self.emit(AgentEvent::FilesCreated {
            files: self.created_files.clone(),
        });
    }

    /// Set deployment result
    pub fn set_deployment(&mut self, provider: &str, url: &str) {
        let info = DeploymentInfo {
            provider: provider.to_string(),
            url: url.to_string(),
            status: "live".to_string(),
            deployed_at: Utc::now(),
        };
        self.deployment = Some(info.clone());
        self.emit(AgentEvent::Deployment { info });
    }
}
