//! Sandboxed Core Tools implemented in Rust
//!
//! Provides ultra-fast, memory-safe file operations strictly confined
//! to the workspace root.

use crate::workspace::{Workspace, WorkspaceError};
use serde::{Deserialize, Serialize};
use std::fs;
use thiserror::Error;

#[derive(Error, Debug)]
pub enum ToolError {
    #[error("Workspace error: {0}")]
    Workspace(#[from] WorkspaceError),
    #[error("IO error: {0}")]
    Io(#[from] std::io::Error),
    #[error("File edit failed: target string was not found in '{0}'")]
    TargetNotFound(String),
    #[error("File edit failed: target string matched multiple times ({0}) in '{1}'")]
    AmbiguousMatch(usize, String),
    #[error("File already exists: '{0}' (pass overwrite: true)")]
    AlreadyExists(String),
}

#[derive(Debug, Serialize, Deserialize)]
pub struct DirEntryInfo {
    pub name: String,
    pub path: String,
    pub is_dir: bool,
    pub size_bytes: u64,
}

pub struct ToolEngine {
    workspace: Workspace,
}

impl ToolEngine {
    pub fn new(workspace: Workspace) -> Self {
        Self { workspace }
    }

    pub fn workspace(&self) -> &Workspace {
        &self.workspace
    }

    /// Read a file within the workspace, optionally between start and end lines (1-indexed).
    pub fn read_file(
        &self,
        rel_path: &str,
        start_line: Option<usize>,
        end_line: Option<usize>,
    ) -> Result<String, ToolError> {
        let abs_path = self.workspace.resolve(rel_path)?;
        let content = fs::read_to_string(&abs_path)?;

        if start_line.is_none() && end_line.is_none() {
            return Ok(content);
        }

        let lines: Vec<&str> = content.lines().collect();
        let total = lines.len();

        let start = start_line.unwrap_or(1).saturating_sub(1).min(total);
        let end = end_line.unwrap_or(total).min(total);

        if start >= end {
            return Ok(String::new());
        }

        let sliced = lines[start..end].join("\n");
        Ok(sliced)
    }

    /// Write a file within the workspace. Creates parent directories if missing.
    pub fn write_file(
        &self,
        rel_path: &str,
        content: &str,
        overwrite: bool,
    ) -> Result<(), ToolError> {
        let abs_path = self.workspace.resolve(rel_path)?;

        if abs_path.exists() && !overwrite {
            return Err(ToolError::AlreadyExists(rel_path.to_string()));
        }

        if let Some(parent) = abs_path.parent() {
            fs::create_dir_all(parent)?;
        }

        fs::write(&abs_path, content)?;
        Ok(())
    }

    /// Edit a file by replacing an exact unique target occurrence with replacement text.
    pub fn edit_file(
        &self,
        rel_path: &str,
        target: &str,
        replacement: &str,
    ) -> Result<String, ToolError> {
        let abs_path = self.workspace.resolve(rel_path)?;
        let content = fs::read_to_string(&abs_path)?;

        let matches = content.matches(target).count();
        if matches == 0 {
            return Err(ToolError::TargetNotFound(rel_path.to_string()));
        }
        if matches > 1 {
            return Err(ToolError::AmbiguousMatch(matches, rel_path.to_string()));
        }

        let new_content = content.replacen(target, replacement, 1);
        fs::write(&abs_path, &new_content)?;

        Ok(format!("Successfully updated {}", rel_path))
    }

    /// List directory contents inside workspace.
    pub fn list_dir(&self, rel_path: &str) -> Result<Vec<DirEntryInfo>, ToolError> {
        let abs_path = self.workspace.resolve(rel_path)?;
        let mut entries = Vec::new();

        for entry in fs::read_dir(&abs_path)? {
            let entry = entry?;
            let metadata = entry.metadata()?;
            let file_name = entry.file_name().to_string_lossy().to_string();

            // Ignore standard VCS and build folders
            if file_name == ".git" || file_name == "node_modules" || file_name == "target" {
                continue;
            }

            let entry_path = entry.path();
            let relative = entry_path
                .strip_prefix(self.workspace.root())
                .unwrap_or(&entry_path)
                .to_string_lossy()
                .replace('\\', "/");

            entries.push(DirEntryInfo {
                name: file_name,
                path: relative,
                is_dir: metadata.is_dir(),
                size_bytes: metadata.len(),
            });
        }

        entries.sort_by(|a, b| {
            if a.is_dir == b.is_dir {
                a.name.cmp(&b.name)
            } else if a.is_dir {
                std::cmp::Ordering::Less
            } else {
                std::cmp::Ordering::Greater
            }
        });

        Ok(entries)
    }
}
