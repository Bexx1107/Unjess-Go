//! Sandboxed Workspace Management in Rust
//!
//! Enforces zero-cost security boundaries: canonical path resolution,
//! boundary confinement checks (preventing directory traversal attacks),
//! and file system operations within the workspace root.

use std::path::{Path, PathBuf};
use thiserror::Error;

#[derive(Error, Debug)]
pub enum WorkspaceError {
    #[error("Path '{0}' escapes workspace boundary '{1}'")]
    PathEscapesWorkspace(String, String),
    #[error("File or directory not found: '{0}'")]
    NotFound(String),
    #[error("IO error: {0}")]
    Io(#[from] std::io::Error),
    #[error("Invalid path: '{0}'")]
    InvalidPath(String),
}

/// A strictly sandboxed workspace confined to a specific root directory.
#[derive(Debug, Clone)]
pub struct Workspace {
    root: PathBuf,
}

impl Workspace {
    /// Create a new workspace at the given path.
    /// The path will be canonicalized to resolve symlinks and relative segments.
    pub fn new<P: AsRef<Path>>(path: P) -> Result<Self, WorkspaceError> {
        let path = path.as_ref();
        if !path.exists() {
            std::fs::create_dir_all(path)?;
        }
        let canonical_root = path.canonicalize()?;
        Ok(Self { root: canonical_root })
    }

    /// Access the canonical root path.
    pub fn root(&self) -> &Path {
        &self.root
    }

    /// Resolve a relative path inside the workspace.
    /// If the path attempts to escape the root via `..` or symlinks, returns `WorkspaceError::PathEscapesWorkspace`.
    pub fn resolve<P: AsRef<Path>>(&self, rel_path: P) -> Result<PathBuf, WorkspaceError> {
        let rel = rel_path.as_ref();
        
        // Strip leading slashes to treat as relative
        let clean_rel = if rel.is_absolute() {
            if let Ok(stripped) = rel.strip_prefix(&self.root) {
                stripped.to_path_buf()
            } else {
                return Err(WorkspaceError::PathEscapesWorkspace(
                    rel.display().to_string(),
                    self.root.display().to_string(),
                ));
            }
        } else {
            rel.to_path_buf()
        };

        let joined = self.root.join(&clean_rel);

        // For existing files, canonicalize and check prefix
        if joined.exists() {
            let canonical = joined.canonicalize()?;
            if !canonical.starts_with(&self.root) {
                return Err(WorkspaceError::PathEscapesWorkspace(
                    rel.display().to_string(),
                    self.root.display().to_string(),
                ));
            }
            Ok(canonical)
        } else {
            // For not-yet-existing files, verify parent directory
            if let Some(parent) = joined.parent() {
                if parent.exists() {
                    let canonical_parent = parent.canonicalize()?;
                    if !canonical_parent.starts_with(&self.root) {
                        return Err(WorkspaceError::PathEscapesWorkspace(
                            rel.display().to_string(),
                            self.root.display().to_string(),
                        ));
                    }
                }
            }
            Ok(joined)
        }
    }
}
