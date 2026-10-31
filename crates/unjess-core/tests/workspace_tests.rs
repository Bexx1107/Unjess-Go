use tempfile::tempdir;
use unjess_core::{AgentConfig, AgentSession, StepStatus, ToolEngine, Workspace};

#[test]
fn test_workspace_sandboxing() {
    let dir = tempdir().unwrap();
    let ws = Workspace::new(dir.path()).unwrap();

    // Resolving inside workspace should succeed
    let valid = ws.resolve("src/main.rs").unwrap();
    assert!(valid.starts_with(ws.root()));

    // Directory traversal attempt should be blocked
    let escape = ws.resolve("../../secret.txt");
    assert!(escape.is_err());
}

#[test]
fn test_tool_file_lifecycle() {
    let dir = tempdir().unwrap();
    let ws = Workspace::new(dir.path()).unwrap();
    let tools = ToolEngine::new(ws);

    // 1. Write file
    tools
        .write_file("hello.txt", "Hello Unjess from Rust!", false)
        .unwrap();

    // 2. Read file
    let content = tools.read_file("hello.txt", None, None).unwrap();
    assert_eq!(content, "Hello Unjess from Rust!");

    // 3. Edit file
    tools
        .edit_file("hello.txt", "Rust", "High-Speed Rust")
        .unwrap();
    let updated = tools.read_file("hello.txt", None, None).unwrap();
    assert_eq!(updated, "Hello Unjess from High-Speed Rust!");

    // 4. List directory
    let list = tools.list_dir(".").unwrap();
    assert_eq!(list.len(), 1);
    assert_eq!(list[0].name, "hello.txt");
    assert!(!list[0].is_dir);
}

#[test]
fn test_agent_session_workflow() {
    let dir = tempdir().unwrap();
    let ws = Workspace::new(dir.path()).unwrap();
    let mut session = AgentSession::new(AgentConfig::default(), ws);

    session.set_plan(vec![
        "Analyzed project requirements",
        "Created project structure",
        "Installed dependencies",
    ]);

    assert_eq!(session.planning_steps.len(), 3);
    assert_eq!(session.planning_steps[0].status, StepStatus::Pending);

    session.complete_step(0);
    assert_eq!(session.planning_steps[0].status, StepStatus::Completed);

    session.record_file("app/page.tsx", false, "created");
    assert_eq!(session.created_files.len(), 1);
    assert_eq!(session.created_files[0].path, "app/page.tsx");

    session.set_deployment("Vercel", "https://client-website.vercel.app");
    assert!(session.deployment.is_some());
    assert_eq!(
        session.deployment.as_ref().unwrap().url,
        "https://client-website.vercel.app"
    );
}
