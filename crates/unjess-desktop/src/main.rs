#![windows_subsystem = "windows"]

use std::error::Error;
use std::net::{SocketAddr, TcpStream};
use std::path::{Path, PathBuf};
use std::process::{Child, Command, Stdio};
use std::time::{Duration, Instant};
use tao::{
    dpi::LogicalSize,
    event::{Event, StartCause, WindowEvent},
    event_loop::{ControlFlow, EventLoop},
    window::{Icon, WindowBuilder},
};
use wry::{WebContext, WebViewBuilder};
#[cfg(target_os = "linux")]
use tao::platform::unix::WindowExtUnix;
#[cfg(target_os = "linux")]
use wry::WebViewBuilderExtUnix;

const ICON_RGBA_32: &[u8] = include_bytes!("../../../assets/unjess_icon_32.rgba");

const DAEMON_BIN_NAME: &str = if cfg!(windows) { "unjessd.exe" } else { "unjessd" };

fn is_server_online(addr: &str) -> bool {
    if let Ok(socket) = addr.parse::<SocketAddr>() {
        TcpStream::connect_timeout(&socket, Duration::from_millis(50)).is_ok()
    } else {
        false
    }
}

fn resolve_daemon_command() -> (String, Vec<String>) {
    let mut port_args = vec!["--port".to_string(), "9757".to_string()];
    if let Ok(exe) = std::env::current_exe() {
        port_args.push("--parent-exe".to_string());
        port_args.push(exe.to_string_lossy().to_string());
    }

    // Priority 1: Check for daemon binary right next to the current executable or in daemon/
    if let Ok(exe) = std::env::current_exe() {
        if let Some(parent) = exe.parent() {
            let direct = parent.join(DAEMON_BIN_NAME);
            if direct.is_file() {
                return (direct.to_string_lossy().to_string(), port_args);
            }
            let daemon_sub = parent.join("daemon").join(DAEMON_BIN_NAME);
            if daemon_sub.is_file() {
                return (daemon_sub.to_string_lossy().to_string(), port_args);
            }
        }
    }

    // Priority 2: Check up ancestor directories of current_exe()
    if let Ok(exe) = std::env::current_exe() {
        let mut curr = exe.parent();
        while let Some(dir) = curr {
            let direct = dir.join(DAEMON_BIN_NAME);
            if direct.is_file() {
                return (direct.to_string_lossy().to_string(), port_args);
            }
            let daemon_sub = dir.join("daemon").join(DAEMON_BIN_NAME);
            if daemon_sub.is_file() {
                return (daemon_sub.to_string_lossy().to_string(), port_args);
            }
            curr = dir.parent();
        }
    }

    // Priority 3: Check standard installation directories (Programs/Unjess on Windows, ~/.unjess/bin on Unix)
    #[cfg(windows)]
    {
        if let Ok(local_appdata) = std::env::var("LOCALAPPDATA") {
            let app_daemon = PathBuf::from(&local_appdata).join("Programs").join("Unjess").join("unjessd.exe");
            if app_daemon.is_file() {
                return (app_daemon.to_string_lossy().to_string(), port_args);
            }
            let old_daemon = PathBuf::from(&local_appdata).join("Unjess").join("unjessd.exe");
            if old_daemon.is_file() {
                return (old_daemon.to_string_lossy().to_string(), port_args);
            }
        }
    }
    #[cfg(not(windows))]
    {
        if let Ok(home) = std::env::var("HOME") {
            let bin_daemon = PathBuf::from(&home).join(".unjess").join("bin").join("unjessd");
            if bin_daemon.is_file() {
                return (bin_daemon.to_string_lossy().to_string(), port_args);
            }
        }
    }

    // Priority 4: Check current working directory for daemon binary
    if let Ok(cwd) = std::env::current_dir() {
        let direct = cwd.join(DAEMON_BIN_NAME);
        if direct.is_file() {
            return (direct.to_string_lossy().to_string(), port_args);
        }
        let daemon_sub = cwd.join("daemon").join(DAEMON_BIN_NAME);
        if daemon_sub.is_file() {
            return (daemon_sub.to_string_lossy().to_string(), port_args);
        }
    }

    // Fallback: system PATH
    (DAEMON_BIN_NAME.to_string(), port_args)
}

fn find_workspace_root(binary_path: &Path) -> PathBuf {
    // 1. Check ancestors of current_exe() for src/unjess/desktop/index.html
    if let Ok(exe) = std::env::current_exe() {
        let mut curr = exe.parent();
        while let Some(dir) = curr {
            if dir.join("src").join("unjess").join("desktop").join("index.html").is_file() {
                return dir.to_path_buf();
            }
            curr = dir.parent();
        }
    }

    // 2. Check ancestors of binary_path for src/unjess/desktop/index.html
    let abs_bin = if binary_path.is_absolute() {
        binary_path.to_path_buf()
    } else if let Ok(cwd) = std::env::current_dir() {
        cwd.join(binary_path)
    } else {
        binary_path.to_path_buf()
    };

    let mut curr = abs_bin.parent();
    while let Some(dir) = curr {
        if dir.join("src").join("unjess").join("desktop").join("index.html").is_file() {
            return dir.to_path_buf();
        }
        curr = dir.parent();
    }

    // 3. Check ancestors of current_dir() for src/unjess/desktop/index.html
    if let Ok(cwd) = std::env::current_dir() {
        let mut curr = Some(cwd.as_path());
        while let Some(dir) = curr {
            if dir.join("src").join("unjess").join("desktop").join("index.html").is_file() {
                return dir.to_path_buf();
            }
            curr = dir.parent();
        }
    }

    // 4. Check for daemon in ancestors of current_exe()
    if let Ok(exe) = std::env::current_exe() {
        let mut curr = exe.parent();
        while let Some(dir) = curr {
            if dir.join(DAEMON_BIN_NAME).is_file() {
                return dir.to_path_buf();
            }
            curr = dir.parent();
        }
    }

    // 5. If binary_path itself is daemon, use its directory (or its parent if inside 'daemon')
    if let Some(parent) = abs_bin.parent() {
        if parent.file_name().and_then(|n| n.to_str()) == Some("daemon") {
            if let Some(grandparent) = parent.parent() {
                return grandparent.to_path_buf();
            }
        }
        return parent.to_path_buf();
    }

    // 6. Default to current_dir or current_exe parent
    std::env::current_dir().unwrap_or_else(|_| {
        std::env::current_exe()
            .ok()
            .and_then(|e| e.parent().map(|p| p.to_path_buf()))
            .unwrap_or_else(|| PathBuf::from("."))
    })
}

fn try_spawn_server() -> Option<Child> {
    let (binary, args) = resolve_daemon_command();
    let bin_path = PathBuf::from(&binary);
    let work_dir = find_workspace_root(&bin_path);

    let mut cmd = Command::new(&binary);
    cmd.args(&args);
    cmd.current_dir(work_dir);
    cmd.stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::null());

    cmd.spawn().ok()
}

fn main() {
    if let Err(err) = run() {
        eprintln!("Unjess Desktop Error: {err}");
        let _ = std::fs::write("unjess-error.log", format!("Error: {err}\n"));
    }
}

fn run() -> Result<(), Box<dyn Error>> {
    let event_loop = EventLoop::new();
    let window_icon = Icon::from_rgba(ICON_RGBA_32.to_vec(), 32, 32).ok();
    let mut wb = WindowBuilder::new()
        .with_title("Unjess — Autonomous AI Coding Harness")
        .with_inner_size(LogicalSize::new(1360.0, 860.0))
        .with_min_inner_size(LogicalSize::new(480.0, 400.0));
    if let Some(icon) = window_icon {
        wb = wb.with_window_icon(Some(icon));
    }
    let window = wb.build(&event_loop)?;

    const HTML: &str = include_str!("../../../src/unjess/desktop/index.html");

    // Check if backend daemon is already active, otherwise spawn it
    let addr = "127.0.0.1:9757";
    let mut server_child: Option<Child> = None;

    if !is_server_online(addr) {
        server_child = try_spawn_server();
        let start = Instant::now();
        while start.elapsed() < Duration::from_millis(8000) {
            if is_server_online(addr) {
                println!("Unjess backend API server ready on {addr}.");
                break;
            }
            std::thread::sleep(Duration::from_millis(50));
        }
    } else {
        println!("Unjess backend API server already active on {addr}.");
    }

    let is_online = is_server_online(addr);

    // Standard user data directory in LOCALAPPDATA for WebView2
    #[cfg(windows)]
    let webview_data_dir: PathBuf = std::env::var("LOCALAPPDATA")
        .map(|p| PathBuf::from(p).join("Unjess").join("webview"))
        .unwrap_or_else(|_| PathBuf::from("webview_data"));
    #[cfg(windows)]
    let _ = std::fs::create_dir_all(&webview_data_dir);
    #[cfg(windows)]
    let mut web_context = WebContext::new(Some(webview_data_dir));
    #[cfg(not(windows))]
    let mut web_context = WebContext::default();

    let builder = WebViewBuilder::new_with_web_context(&mut web_context);
    #[cfg(target_os = "linux")]
    let vbox = window.default_vbox().expect("default_vbox should exist on tao unix window");
    #[cfg(target_os = "linux")]
    let _webview = if is_online {
        println!("Connecting WebView directly to live backend: http://{addr}/desktop");
        builder.with_url(format!("http://{addr}/desktop")).build_gtk(vbox)?
    } else {
        println!("Loading embedded autonomous desktop interface.");
        builder.with_html(HTML).build_gtk(vbox)?
    };

    #[cfg(not(target_os = "linux"))]
    let _webview = if is_online {
        println!("Connecting WebView directly to live backend: http://{addr}/desktop");
        builder.with_url(format!("http://{addr}/desktop")).build(&window)?
    } else {
        println!("Loading embedded autonomous desktop interface.");
        builder.with_html(HTML).build(&window)?
    };

    event_loop.run(move |event, _, control_flow| {
        *control_flow = ControlFlow::Wait;

        // Keep webview and web_context reference alive inside the event loop closure
        let _ = (&_webview, &web_context);

        match event {
            Event::NewEvents(StartCause::Init) => {
                println!("Unjess Native Desktop window initialized successfully.");
            }
            Event::WindowEvent {
                event: WindowEvent::CloseRequested,
                ..
            } => {
                if let Some(mut child) = server_child.take() {
                    let _ = child.kill();
                }
                *control_flow = ControlFlow::Exit;
            }
            _ => (),
        }
    });
}
