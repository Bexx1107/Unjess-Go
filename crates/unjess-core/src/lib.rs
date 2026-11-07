//! Unjess Core — High-Performance Agent Runtime in Rust
//!
//! Provides memory-safe workspace sandboxing, blazing fast tool execution,
//! structured agent event streaming, and state tracking.

pub mod agent;
pub mod events;
pub mod tools;
pub mod workspace;

pub use agent::{AgentConfig, AgentSession};
pub use events::{AgentEvent, CreatedFile, DeploymentInfo, PlanningStep, StepStatus, ToolDescriptor};
pub use tools::{DirEntryInfo, ToolEngine, ToolError};
pub use workspace::{Workspace, WorkspaceError};
