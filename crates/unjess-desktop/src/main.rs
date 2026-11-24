#![windows_subsystem = "windows"]

use serde::{Deserialize, Serialize};
use std::error::Error;
use std::io::{BufRead, BufReader, Read, Write};
use std::net::{SocketAddr, TcpListener, TcpStream};
use std::path::{Path, PathBuf};
use std::process::{Child, Command, Stdio};
use std::sync::{Arc, Mutex, mpsc};
use std::time::{Duration, Instant, SystemTime, UNIX_EPOCH};
use tao::{
    dpi::LogicalSize,
    event::{Event, StartCause, WindowEvent},
    event_loop::{ControlFlow, EventLoopBuilder, EventLoopProxy},
    window::{Icon, WindowBuilder},
};
use wry::{
    NewWindowResponse, PageLoadEvent, Rect, WebContext, WebView, WebViewBuilder,
    dpi::{LogicalPosition, LogicalSize as WryLogicalSize},
};

#[cfg(target_os = "linux")]
use tao::platform::unix::WindowExtUnix;
#[cfg(target_os = "linux")]
use wry::WebViewBuilderExtUnix;
#[cfg(windows)]
use wry::{WebViewBuilderExtWindows, WebViewExtWindows};

const ICON_RGBA_32: &[u8] = include_bytes!("../../../assets/unjess_icon_32.rgba");

const DAEMON_BIN_NAME: &str = if cfg!(windows) { "unjessd.exe" } else { "unjessd" };

const SOM_INDEXER_SCRIPT: &str = r#"(() => {
    try {
        const old = document.getElementById('__bu_som_container');
        if (old) old.remove();

        const selectors = [
            'a[href]',
            'button',
            'input',
            'textarea',
            'select',
            'summary',
            '[role="button"]',
            '[role="link"]',
            '[role="checkbox"]',
            '[role="radio"]',
            '[role="tab"]',
            '[role="menuitem"]',
            '[onclick]',
            '[tabindex]:not([tabindex="-1"])'
        ].join(',');

        const candidates = Array.from(document.querySelectorAll(selectors));
        const vw = window.innerWidth || document.documentElement.clientWidth || 1024;
        const vh = window.innerHeight || document.documentElement.clientHeight || 768;

        const visibleItems = [];
        for (let i = 0; i < candidates.length; i++) {
            const el = candidates[i];
            if (el.id === '__bu_som_container' || (el.closest && el.closest('#__bu_som_container'))) continue;
            if (el.disabled || el.getAttribute('aria-hidden') === 'true') continue;

            const rect = el.getBoundingClientRect();
            if (rect.width < 5 || rect.height < 5) continue;
            if (rect.bottom <= 0 || rect.top >= vh * 1.5 || rect.right <= 0 || rect.left >= vw * 1.5) continue;

            const style = window.getComputedStyle(el);
            if (!style || style.visibility === 'hidden' || style.display === 'none' || style.opacity === '0') continue;

            visibleItems.push({ el: el, rect: rect });
        }

        visibleItems.sort((a, b) => {
            if (Math.abs(a.rect.top - b.rect.top) > 12) {
                return a.rect.top - b.rect.top;
            }
            return a.rect.left - b.rect.left;
        });

        const limited = visibleItems.slice(0, 75);

        const container = document.createElement('div');
        container.id = '__bu_som_container';
        container.setAttribute('style', 'position:fixed;inset:0;pointer-events:none;z-index:2147483647;overflow:hidden;');

        const elementsData = [];

        for (let j = 0; j < limited.length; j++) {
            const idx = j + 1;
            const item = limited[j];
            const el = item.el;
            const rect = item.rect;

            el.setAttribute('data-bu-idx', String(idx));

            const box = document.createElement('div');
            box.setAttribute('style', 'position:absolute;box-sizing:border-box;pointer-events:none;' +
                'left:' + Math.max(0, rect.left) + 'px;' +
                'top:' + Math.max(0, rect.top) + 'px;' +
                'width:' + Math.max(0, rect.width) + 'px;' +
                'height:' + Math.max(0, rect.height) + 'px;' +
                'border:2px dashed #e11d48;');

            const badge = document.createElement('span');
            badge.innerText = '[' + idx + ']';
            const bTop = Math.max(0, rect.top - 14);
            badge.setAttribute('style', 'position:absolute;pointer-events:none;z-index:2147483647;' +
                'left:' + Math.max(0, rect.left) + 'px;' +
                'top:' + bTop + 'px;' +
                'background:#e11d48;color:#ffffff;font-size:10px;font-family:monospace;font-weight:bold;' +
                'padding:0px 3px;border-radius:2px;line-height:14px;box-shadow:0 1px 2px rgba(0,0,0,0.6);');

            container.appendChild(box);
            container.appendChild(badge);

            const tag = el.tagName.toLowerCase();
            let text = '';
            if (tag === 'input' || tag === 'textarea') {
                text = el.value || el.placeholder || el.getAttribute('aria-label') || '';
            } else if (tag === 'select') {
                const opt = el.options && el.selectedIndex >= 0 ? el.options[el.selectedIndex] : null;
                text = opt ? opt.text : (el.getAttribute('aria-label') || '');
            } else {
                text = el.innerText || el.getAttribute('aria-label') || el.getAttribute('title') || el.value || '';
            }
            text = text.trim().replace(/[\r\n\t]+/g, ' ').replace(/\s{2,}/g, ' ');
            if (text.length > 80) {
                text = text.substring(0, 77) + '...';
            }

            elementsData.push({
                index: idx,
                tag: tag,
                type: el.getAttribute('type') || '',
                role: el.getAttribute('role') || '',
                text: text,
                href: el.getAttribute('href') || '',
                x: Math.round(rect.left + rect.width / 2),
                y: Math.round(rect.top + rect.height / 2),
                w: Math.round(rect.width),
                h: Math.round(rect.height)
            });
        }

        (document.body || document.documentElement).appendChild(container);

        let pageText = '';
        if (document.body) {
            pageText = document.body.innerText || '';
            pageText = pageText.trim().replace(/\r\n/g, '\n').replace(/\n{3,}/g, '\n\n');
            if (pageText.length > 4000) {
                pageText = pageText.substring(0, 4000) + '... [truncated]';
            }
        }

        return {
            title: document.title || '',
            url: window.location.href || '',
            page_text: pageText,
            elements: elementsData
        };
    } catch (e) {
        return {
            title: document.title || '',
            url: window.location.href || '',
            page_text: document.body ? document.body.innerText : '',
            elements: [],
            error: String(e)
        };
    }
})()"#;

#[derive(Default, Clone)]
struct SharedBrowserInfo {
    url: String,
    title: String,
    loading: bool,
}

#[derive(Deserialize)]
struct UiIpcMessage {
    #[serde(rename = "type")]
    msg_type: String,
    #[serde(default)]
    visible: bool,
    #[serde(default)]
    x: f64,
    #[serde(default)]
    y: f64,
    #[serde(default)]
    width: f64,
    #[serde(default)]
    height: f64,
    #[serde(default)]
    action: String,
    #[serde(default)]
    url: String,
}

#[derive(Deserialize, Default)]
struct BridgeActionRequest {
    #[serde(default)]
    action: String,
    #[serde(default)]
    url: String,
    #[serde(default)]
    index: i32,
    #[serde(default)]
    selector: String,
    #[serde(default)]
    text: String,
    #[serde(default)]
    press_enter: bool,
    #[serde(default)]
    direction: String,
    #[serde(default)]
    amount: i32,
    #[serde(default)]
    key: String,
    #[serde(default)]
    script: String,
}

#[derive(Serialize, Deserialize, Default, Clone)]
struct InteractiveElement {
    index: i32,
    tag: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    r#type: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    role: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    text: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    href: String,
    x: i32,
    y: i32,
    w: i32,
    h: i32,
}

#[derive(Serialize, Default)]
struct BridgeActionResponse {
    ok: bool,
    title: String,
    url: String,
    action_summary: String,
    elements: Vec<InteractiveElement>,
    page_text: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    eval_output: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    screenshot_png_base64: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    error: String,
}

enum UserEvent {
    UpdateBrowserBounds {
        visible: bool,
        x: f64,
        y: f64,
        width: f64,
        height: f64,
    },
    BrowserUiCommand {
        action: String,
        url: String,
    },
    BrowserPageEvent {
        url: Option<String>,
        title: Option<String>,
        loading: Option<bool>,
    },
    AgentNotifyPane {
        url: String,
        action: String,
    },
    EnsureCaptureReady {
        tx: mpsc::Sender<bool>,
    },
    RestoreHiddenAfterCapture,
    NavigateUrl {
        url: String,
        tx: mpsc::Sender<Result<(), String>>,
    },
    NavHistory {
        op: String,
        tx: mpsc::Sender<Result<(), String>>,
    },
    EvalJs {
        script: String,
        tx: mpsc::Sender<Result<String, String>>,
    },
    CdpCall {
        method: String,
        params_json: String,
        tx: mpsc::Sender<Result<String, String>>,
    },
}

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

    #[cfg(windows)]
    {
        if let Ok(local_appdata) = std::env::var("LOCALAPPDATA") {
            let app_daemon = PathBuf::from(&local_appdata)
                .join("Programs")
                .join("Unjess")
                .join("unjessd.exe");
            if app_daemon.is_file() {
                return (app_daemon.to_string_lossy().to_string(), port_args);
            }
            let old_daemon = PathBuf::from(&local_appdata)
                .join("Unjess")
                .join("unjessd.exe");
            if old_daemon.is_file() {
                return (old_daemon.to_string_lossy().to_string(), port_args);
            }
        }
    }
    #[cfg(not(windows))]
    {
        if let Ok(home) = std::env::var("HOME") {
            let bin_daemon = PathBuf::from(&home)
                .join(".unjess")
                .join("bin")
                .join("unjessd");
            if bin_daemon.is_file() {
                return (bin_daemon.to_string_lossy().to_string(), port_args);
            }
        }
    }

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

    (DAEMON_BIN_NAME.to_string(), port_args)
}

fn find_workspace_root(binary_path: &Path) -> PathBuf {
    if let Ok(exe) = std::env::current_exe() {
        let mut curr = exe.parent();
        while let Some(dir) = curr {
            if dir.join("daemon").join("internal").join("api").join("embedded_index.html").is_file() {
                return dir.to_path_buf();
            }
            curr = dir.parent();
        }
    }

    let abs_bin = if binary_path.is_absolute() {
        binary_path.to_path_buf()
    } else if let Ok(cwd) = std::env::current_dir() {
        cwd.join(binary_path)
    } else {
        binary_path.to_path_buf()
    };

    let mut curr = abs_bin.parent();
    while let Some(dir) = curr {
        if dir.join("daemon").join("internal").join("api").join("embedded_index.html").is_file() {
            return dir.to_path_buf();
        }
        curr = dir.parent();
    }

    if let Ok(cwd) = std::env::current_dir() {
        let mut curr = Some(cwd.as_path());
        while let Some(dir) = curr {
            if dir.join("daemon").join("internal").join("api").join("embedded_index.html").is_file() {
                return dir.to_path_buf();
            }
            curr = dir.parent();
        }
    }

    if let Ok(exe) = std::env::current_exe() {
        let mut curr = exe.parent();
        while let Some(dir) = curr {
            if dir.join(DAEMON_BIN_NAME).is_file() {
                return dir.to_path_buf();
            }
            curr = dir.parent();
        }
    }

    if let Some(parent) = abs_bin.parent() {
        if parent.file_name().and_then(|n| n.to_str()) == Some("daemon") {
            if let Some(grandparent) = parent.parent() {
                return grandparent.to_path_buf();
            }
        }
        return parent.to_path_buf();
    }

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

#[cfg(windows)]
fn show_windows_error_box(title: &str, message: &str) {
    use std::ffi::OsStr;
    use std::os::windows::ffi::OsStrExt;
    unsafe extern "system" {
        fn MessageBoxW(
            hwnd: *mut core::ffi::c_void,
            text: *const u16,
            caption: *const u16,
            u_type: u32,
        ) -> i32;
    }
    let to_wide = |s: &str| -> Vec<u16> {
        OsStr::new(s).encode_wide().chain(std::iter::once(0)).collect()
    };
    let text_w = to_wide(message);
    let title_w = to_wide(title);
    const MB_OK_ICONERROR: u32 = 0x0000_0010;
    unsafe {
        let _ = MessageBoxW(
            core::ptr::null_mut(),
            text_w.as_ptr(),
            title_w.as_ptr(),
            MB_OK_ICONERROR,
        );
    }
}

#[cfg(windows)]
fn call_webview2_cdp(
    webview: &WebView,
    method: &str,
    params_json: &str,
    tx: mpsc::Sender<Result<String, String>>,
) {
    use webview2_com::CallDevToolsProtocolMethodCompletedHandler;
    use windows_core::HSTRING;

    let core_wv = webview.webview();
    let method_h = HSTRING::from(method);
    let params_h = HSTRING::from(params_json);
    let tx_cb = tx.clone();
    let handler = CallDevToolsProtocolMethodCompletedHandler::create(Box::new(
        move |res, json_out| {
            match res {
                Ok(()) => {
                    let _ = tx_cb.send(Ok(json_out));
                }
                Err(e) => {
                    let _ = tx_cb.send(Err(e.to_string()));
                }
            }
            Ok(())
        },
    ));
    let call_res = unsafe { core_wv.CallDevToolsProtocolMethod(&method_h, &params_h, &handler) };
    if let Err(e) = call_res {
        let _ = tx.send(Err(e.to_string()));
    }
}

#[cfg(not(windows))]
fn call_webview2_cdp(
    _webview: &WebView,
    _method: &str,
    _params_json: &str,
    tx: mpsc::Sender<Result<String, String>>,
) {
    let _ = tx.send(Err("in-process CDP capture is Windows WebView2 specific".to_string()));
}

fn post_bridge_heartbeat(
    daemon_addr: &str,
    bridge_url: &str,
    token: &str,
    info: &Arc<Mutex<SharedBrowserInfo>>,
) {
    let Ok(socket) = daemon_addr.parse::<SocketAddr>() else {
        return;
    };
    let Ok(mut stream) = TcpStream::connect_timeout(&socket, Duration::from_millis(400)) else {
        return;
    };
    let _ = stream.set_write_timeout(Some(Duration::from_millis(500)));
    let _ = stream.set_read_timeout(Some(Duration::from_millis(500)));

    let snap = info.lock().map(|g| g.clone()).unwrap_or_default();
    let payload = serde_json::json!({
        "bridge_url": bridge_url,
        "token": token,
        "pid": std::process::id(),
        "url": snap.url,
        "title": snap.title,
    })
    .to_string();

    let req = format!(
        "POST /api/harness/browser/native-bridge HTTP/1.1\r\nHost: {daemon_addr}\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{}",
        payload.len(),
        payload
    );
    let _ = stream.write_all(req.as_bytes());
    let mut sink = [0u8; 256];
    let _ = stream.read(&mut sink);
}

fn proxy_eval_js(proxy: &EventLoopProxy<UserEvent>, script: String, timeout_ms: u64) -> Result<String, String> {
    let (tx, rx) = mpsc::channel();
    proxy
        .send_event(UserEvent::EvalJs { script, tx })
        .map_err(|_| "Desktop window closed".to_string())?;
    rx.recv_timeout(Duration::from_millis(timeout_ms))
        .map_err(|_| "Timed out evaluating JavaScript in Sidebar Browser".to_string())?
}

fn proxy_cdp_call(
    proxy: &EventLoopProxy<UserEvent>,
    method: &str,
    params: serde_json::Value,
    timeout_ms: u64,
) -> Result<String, String> {
    let (tx, rx) = mpsc::channel();
    proxy
        .send_event(UserEvent::CdpCall {
            method: method.to_string(),
            params_json: params.to_string(),
            tx,
        })
        .map_err(|_| "Desktop window closed".to_string())?;
    rx.recv_timeout(Duration::from_millis(timeout_ms))
        .map_err(|_| format!("Timed out executing {method} in Sidebar Browser"))?
}

fn dispatch_key_sequence(proxy: &EventLoopProxy<UserEvent>, raw_key: &str) {
    let k = raw_key.trim();
    let (key, code, vkey, text) = match k.to_lowercase().as_str() {
        "" | "enter" | "return" => ("Enter", "Enter", 13, "\r"),
        "escape" | "esc" => ("Escape", "Escape", 27, ""),
        "tab" => ("Tab", "Tab", 9, ""),
        "backspace" => ("Backspace", "Backspace", 8, ""),
        "arrowdown" | "down" => ("ArrowDown", "ArrowDown", 40, ""),
        "arrowup" | "up" => ("ArrowUp", "ArrowUp", 38, ""),
        "arrowleft" | "left" => ("ArrowLeft", "ArrowLeft", 37, ""),
        "arrowright" | "right" => ("ArrowRight", "ArrowRight", 39, ""),
        "space" => (" ", "Space", 32, " "),
        _ => (k, k, 0, if k.len() == 1 { k } else { "" }),
    };

    let mut down = serde_json::json!({
        "type": "rawKeyDown",
        "key": key,
        "code": code,
        "windowsVirtualKeyCode": vkey,
    });
    if !text.is_empty() {
        down["text"] = serde_json::Value::String(text.to_string());
        down["unmodifiedText"] = serde_json::Value::String(text.to_string());
    }
    let _ = proxy_cdp_call(proxy, "Input.dispatchKeyEvent", down, 2500);

    let mut up = serde_json::json!({
        "type": "keyUp",
        "key": key,
        "code": code,
        "windowsVirtualKeyCode": vkey,
    });
    if !text.is_empty() {
        up["text"] = serde_json::Value::String(text.to_string());
        up["unmodifiedText"] = serde_json::Value::String(text.to_string());
    }
    let _ = proxy_cdp_call(proxy, "Input.dispatchKeyEvent", up, 2500);

    let key_json = serde_json::to_string(key).unwrap_or_else(|_| "\"Enter\"".to_string());
    let code_json = serde_json::to_string(code).unwrap_or_else(|_| "\"Enter\"".to_string());
    let js = format!(
        r#"(() => {{
            const target = document.activeElement || document.body;
            if (!target) return;
            target.dispatchEvent(new KeyboardEvent('keydown', {{ key: {key_json}, code: {code_json}, keyCode: {vkey}, which: {vkey}, bubbles: true }}));
            target.dispatchEvent(new KeyboardEvent('keyup', {{ key: {key_json}, code: {code_json}, keyCode: {vkey}, which: {vkey}, bubbles: true }}));
        }})()"#
    );
    let _ = proxy_eval_js(proxy, js, 2500);
}

fn execute_bridge_action(
    proxy: &EventLoopProxy<UserEvent>,
    req: BridgeActionRequest,
) -> BridgeActionResponse {
    let action = if req.action.trim().is_empty() {
        "screenshot".to_string()
    } else {
        req.action.trim().to_lowercase()
    };

    let _ = proxy.send_event(UserEvent::AgentNotifyPane {
        url: req.url.clone(),
        action: action.clone(),
    });
    std::thread::sleep(Duration::from_millis(140));

    let mut eval_output = String::new();
    let action_summary: String;

    match action.as_str() {
        "navigate" | "goto" | "open" => {
            let target = req.url.trim().to_string();
            if target.is_empty() {
                return BridgeActionResponse {
                    ok: false,
                    error: "url is required for navigate action".to_string(),
                    ..Default::default()
                };
            }
            let (tx, rx) = mpsc::channel();
            if proxy
                .send_event(UserEvent::NavigateUrl {
                    url: target.clone(),
                    tx,
                })
                .is_err()
            {
                return BridgeActionResponse {
                    ok: false,
                    error: "Desktop window closed".to_string(),
                    ..Default::default()
                };
            }
            if let Ok(Err(e)) = rx.recv_timeout(Duration::from_secs(5)) {
                return BridgeActionResponse {
                    ok: false,
                    error: format!("navigation failed: {e}"),
                    ..Default::default()
                };
            }
            std::thread::sleep(Duration::from_millis(550));
            let deadline = Instant::now() + Duration::from_secs(6);
            while Instant::now() < deadline {
                if let Ok(state_raw) = proxy_eval_js(proxy, "document.readyState".to_string(), 1500) {
                    if state_raw.contains("complete") {
                        break;
                    }
                }
                std::thread::sleep(Duration::from_millis(150));
            }
            std::thread::sleep(Duration::from_millis(320));
            action_summary = format!("Navigated Sidebar Browser to {target}");
        }
        "back" | "forward" | "reload" | "clear_data" | "clear_cookies" => {
            let (tx, rx) = mpsc::channel();
            let _ = proxy.send_event(UserEvent::NavHistory {
                op: action.clone(),
                tx,
            });
            let _ = rx.recv_timeout(Duration::from_secs(4));
            std::thread::sleep(Duration::from_millis(500));
            action_summary = format!("Executed browser {action}");
        }
        "click" => {
            if req.index <= 0 && req.selector.trim().is_empty() {
                return BridgeActionResponse {
                    ok: false,
                    error: "index or selector is required for click action".to_string(),
                    ..Default::default()
                };
            }
            let query_js = if req.index > 0 {
                format!(
                    r#"(() => {{
                        const el = document.querySelector('[data-bu-idx="{}"]');
                        if (!el) return {{ found: false }};
                        el.scrollIntoView({{ block: 'center', inline: 'center' }});
                        const rect = el.getBoundingClientRect();
                        const tag = el.tagName.toLowerCase();
                        let text = el.innerText || el.value || el.placeholder || el.getAttribute('aria-label') || '';
                        text = text.trim().replace(/[\r\n\t]+/g, ' ').replace(/\s{{2,}}/g, ' ').slice(0, 50);
                        return {{
                            found: true,
                            tag,
                            text,
                            x: Math.round(rect.left + rect.width / 2),
                            y: Math.round(rect.top + rect.height / 2)
                        }};
                    }})()"#,
                    req.index
                )
            } else {
                let sel_json = serde_json::to_string(&req.selector).unwrap_or_else(|_| "\"\"".to_string());
                format!(
                    r#"(() => {{
                        const el = document.querySelector({sel_json});
                        if (!el) return {{ found: false }};
                        el.scrollIntoView({{ block: 'center', inline: 'center' }});
                        const rect = el.getBoundingClientRect();
                        const tag = el.tagName.toLowerCase();
                        let text = el.innerText || el.value || el.placeholder || el.getAttribute('aria-label') || '';
                        text = text.trim().replace(/[\r\n\t]+/g, ' ').replace(/\s{{2,}}/g, ' ').slice(0, 50);
                        return {{
                            found: true,
                            tag,
                            text,
                            x: Math.round(rect.left + rect.width / 2),
                            y: Math.round(rect.top + rect.height / 2)
                        }};
                    }})()"#
                )
            };

            let raw_pos = match proxy_eval_js(proxy, query_js, 5000) {
                Ok(v) => v,
                Err(e) => {
                    return BridgeActionResponse {
                        ok: false,
                        error: e,
                        ..Default::default()
                    };
                }
            };
            let parsed: serde_json::Value = serde_json::from_str(&raw_pos).unwrap_or_default();
            if !parsed.get("found").and_then(|v| v.as_bool()).unwrap_or(false) {
                let msg = if req.index > 0 {
                    format!("element [{}] not found on page", req.index)
                } else {
                    format!("selector {:?} not found on page", req.selector)
                };
                return BridgeActionResponse {
                    ok: false,
                    error: msg,
                    ..Default::default()
                };
            }
            let x = parsed.get("x").and_then(|v| v.as_i64()).unwrap_or(0);
            let y = parsed.get("y").and_then(|v| v.as_i64()).unwrap_or(0);
            let tag = parsed.get("tag").and_then(|v| v.as_str()).unwrap_or("element");
            let text = parsed.get("text").and_then(|v| v.as_str()).unwrap_or("");

            let cdp_clicked = proxy_cdp_call(
                proxy,
                "Input.dispatchMouseEvent",
                serde_json::json!({ "type": "mousePressed", "x": x, "y": y, "button": "left", "clickCount": 1 }),
                2500,
            )
            .is_ok();
            if cdp_clicked {
                let _ = proxy_cdp_call(
                    proxy,
                    "Input.dispatchMouseEvent",
                    serde_json::json!({ "type": "mouseReleased", "x": x, "y": y, "button": "left", "clickCount": 1 }),
                    2500,
                );
            } else {
                let fallback_click = if req.index > 0 {
                    format!(r#"document.querySelector('[data-bu-idx="{}"]')?.click()"#, req.index)
                } else {
                    let sel_json = serde_json::to_string(&req.selector).unwrap_or_else(|_| "\"\"".to_string());
                    format!(r#"document.querySelector({sel_json})?.click()"#)
                };
                let _ = proxy_eval_js(proxy, fallback_click, 2500);
            }

            std::thread::sleep(Duration::from_millis(650));
            action_summary = if req.index > 0 {
                format!("Clicked element [{}] <{}>{}</{}>", req.index, tag, text, tag)
            } else {
                format!("Clicked element matching selector {:?}", req.selector)
            };
        }
        "type" | "input" | "fill" => {
            if req.index <= 0 && req.selector.trim().is_empty() {
                return BridgeActionResponse {
                    ok: false,
                    error: "index or selector is required for type action".to_string(),
                    ..Default::default()
                };
            }
            let target_expr = if req.index > 0 {
                format!(r#"document.querySelector('[data-bu-idx="{}"]')"#, req.index)
            } else {
                let sel_json = serde_json::to_string(&req.selector).unwrap_or_else(|_| "\"\"".to_string());
                format!(r#"document.querySelector({sel_json})"#)
            };
            let text_json = serde_json::to_string(&req.text).unwrap_or_else(|_| "\"\"".to_string());
            let type_js = format!(
                r#"(() => {{
                    const el = {target_expr};
                    if (!el) return {{ found: false }};
                    el.scrollIntoView({{ block: 'center', inline: 'center' }});
                    el.focus();
                    const tag = el.tagName.toLowerCase();
                    if (tag === 'input') {{
                        const setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value')?.set;
                        if (setter) setter.call(el, {text_json}); else el.value = {text_json};
                    }} else if (tag === 'textarea') {{
                        const setter = Object.getOwnPropertyDescriptor(window.HTMLTextAreaElement.prototype, 'value')?.set;
                        if (setter) setter.call(el, {text_json}); else el.value = {text_json};
                    }} else if (el.isContentEditable) {{
                        el.innerText = {text_json};
                    }} else {{
                        el.value = {text_json};
                    }}
                    el.dispatchEvent(new Event('input', {{ bubbles: true }}));
                    el.dispatchEvent(new Event('change', {{ bubbles: true }}));
                    return {{ found: true, tag }};
                }})()"#
            );
            let raw_res = match proxy_eval_js(proxy, type_js, 5000) {
                Ok(v) => v,
                Err(e) => {
                    return BridgeActionResponse {
                        ok: false,
                        error: e,
                        ..Default::default()
                    };
                }
            };
            let parsed: serde_json::Value = serde_json::from_str(&raw_res).unwrap_or_default();
            if !parsed.get("found").and_then(|v| v.as_bool()).unwrap_or(false) {
                let msg = if req.index > 0 {
                    format!("element [{}] not found on page", req.index)
                } else {
                    format!("selector {:?} not found on page", req.selector)
                };
                return BridgeActionResponse {
                    ok: false,
                    error: msg,
                    ..Default::default()
                };
            }
            let tag = parsed.get("tag").and_then(|v| v.as_str()).unwrap_or("input");
            if req.press_enter {
                dispatch_key_sequence(proxy, "Enter");
                let submit_js = format!(
                    r#"(() => {{
                        const el = {target_expr};
                        if (el && el.form) {{
                            if (typeof el.form.requestSubmit === 'function') el.form.requestSubmit();
                            else el.form.submit();
                        }}
                    }})()"#
                );
                let _ = proxy_eval_js(proxy, submit_js, 2500);
            }
            std::thread::sleep(Duration::from_millis(650));
            let enter_note = if req.press_enter { " (pressed Enter)" } else { "" };
            action_summary = if req.index > 0 {
                format!("Typed {:?} into element [{}] <{}>{}", req.text, req.index, tag, enter_note)
            } else {
                format!("Typed {:?} into {:?}{}", req.text, req.selector, enter_note)
            };
        }
        "scroll" => {
            let amt = if req.amount > 0 { req.amount } else { 550 };
            let dy = if req.direction.trim().eq_ignore_ascii_case("up") {
                -amt
            } else {
                amt
            };
            let _ = proxy_eval_js(
                proxy,
                format!("window.scrollBy({{ top: {dy}, behavior: 'instant' }});"),
                3000,
            );
            std::thread::sleep(Duration::from_millis(350));
            let dir_label = if dy < 0 { "up" } else { "down" };
            action_summary = format!("Scrolled {dir_label} by {amt}px");
        }
        "press_key" => {
            let key = if req.key.trim().is_empty() {
                "Enter"
            } else {
                req.key.trim()
            };
            dispatch_key_sequence(proxy, key);
            std::thread::sleep(Duration::from_millis(350));
            action_summary = format!("Pressed key {key:?}");
        }
        "evaluate" => {
            if req.script.trim().is_empty() {
                return BridgeActionResponse {
                    ok: false,
                    error: "script is required for evaluate action".to_string(),
                    ..Default::default()
                };
            }
            let code_json = serde_json::to_string(&req.script).unwrap_or_else(|_| "\"\"".to_string());
            let wrapped = format!(
                r#"(() => {{
                    try {{
                        const out = (0, eval)({code_json});
                        if (out === undefined) return "undefined";
                        if (typeof out === "string") return out;
                        return JSON.stringify(out, null, 2);
                    }} catch (e) {{
                        return "JS Error: " + String(e);
                    }}
                }})()"#
            );
            if let Ok(raw) = proxy_eval_js(proxy, wrapped, 8000) {
                eval_output = serde_json::from_str::<String>(&raw).unwrap_or(raw);
            }
            action_summary = "Evaluated JavaScript in Sidebar Browser".to_string();
        }
        _ => {
            action_summary = "Captured live Sidebar Browser state and Set-of-Mark elements".to_string();
        }
    }

    // Ensure WebView2 surface is active for DOM bounding rects & PNG screenshot capture
    let (cap_tx, cap_rx) = mpsc::channel();
    let was_temporarily_shown = if proxy.send_event(UserEvent::EnsureCaptureReady { tx: cap_tx }).is_ok() {
        cap_rx.recv_timeout(Duration::from_secs(2)).unwrap_or(false)
    } else {
        false
    };
    if was_temporarily_shown {
        std::thread::sleep(Duration::from_millis(80));
    }

    let som_raw = proxy_eval_js(proxy, SOM_INDEXER_SCRIPT.to_string(), 6000).unwrap_or_default();
    let som_val: serde_json::Value = serde_json::from_str(&som_raw).unwrap_or_default();

    let title = som_val
        .get("title")
        .and_then(|v| v.as_str())
        .unwrap_or("")
        .to_string();
    let url = som_val
        .get("url")
        .and_then(|v| v.as_str())
        .unwrap_or("")
        .to_string();
    let page_text = som_val
        .get("page_text")
        .and_then(|v| v.as_str())
        .unwrap_or("")
        .to_string();
    let elements: Vec<InteractiveElement> = som_val
        .get("elements")
        .and_then(|v| serde_json::from_value(v.clone()).ok())
        .unwrap_or_default();

    let mut screenshot_png_base64 = String::new();
    if let Ok(ss_json) = proxy_cdp_call(
        proxy,
        "Page.captureScreenshot",
        serde_json::json!({ "format": "png" }),
        8000,
    ) {
        if let Ok(ss_val) = serde_json::from_str::<serde_json::Value>(&ss_json) {
            if let Some(b64) = ss_val.get("data").and_then(|v| v.as_str()) {
                screenshot_png_base64 = b64.to_string();
            }
        }
    }

    let _ = proxy_eval_js(
        proxy,
        "document.getElementById('__bu_som_container')?.remove();".to_string(),
        2000,
    );

    if was_temporarily_shown {
        let _ = proxy.send_event(UserEvent::RestoreHiddenAfterCapture);
    }

    BridgeActionResponse {
        ok: true,
        title,
        url,
        action_summary,
        elements,
        page_text,
        eval_output,
        screenshot_png_base64,
        error: String::new(),
    }
}

fn handle_bridge_client(
    mut stream: TcpStream,
    expected_token: &str,
    proxy: &EventLoopProxy<UserEvent>,
    info: &Arc<Mutex<SharedBrowserInfo>>,
    action_lock: &Arc<Mutex<()>>,
) {
    let _ = stream.set_read_timeout(Some(Duration::from_secs(10)));
    let _ = stream.set_write_timeout(Some(Duration::from_secs(15)));

    let Ok(cloned) = stream.try_clone() else {
        return;
    };
    let mut reader = BufReader::new(cloned);

    let mut req_line = String::new();
    if reader.read_line(&mut req_line).is_err() || req_line.is_empty() {
        return;
    }
    let parts: Vec<&str> = req_line.split_whitespace().collect();
    if parts.len() < 2 {
        return;
    }
    let method = parts[0];
    let path = parts[1];

    let mut content_length: usize = 0;
    let mut provided_token = String::new();

    loop {
        let mut line = String::new();
        if reader.read_line(&mut line).is_err() {
            return;
        }
        let trimmed = line.trim_end_matches(&['\r', '\n'][..]);
        if trimmed.is_empty() {
            break;
        }
        if let Some((k, v)) = trimmed.split_once(':') {
            let key_low = k.trim().to_ascii_lowercase();
            let val = v.trim();
            if key_low == "content-length" {
                content_length = val.parse::<usize>().unwrap_or(0);
            } else if key_low == "x-bridge-token" {
                provided_token = val.to_string();
            }
        }
    }

    if !expected_token.is_empty() && provided_token != expected_token {
        let body = r#"{"ok":false,"error":"unauthorized bridge token"}"#;
        let resp = format!(
            "HTTP/1.1 403 Forbidden\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{}",
            body.len(),
            body
        );
        let _ = stream.write_all(resp.as_bytes());
        return;
    }

    let mut body_buf = vec![0u8; content_length.min(4 * 1024 * 1024)];
    if content_length > 0 && reader.read_exact(&mut body_buf).is_err() {
        return;
    }

    if method == "GET" && path.starts_with("/health") {
        let snap = info.lock().map(|g| g.clone()).unwrap_or_default();
        let out = serde_json::json!({
            "ok": true,
            "url": snap.url,
            "title": snap.title,
            "loading": snap.loading,
        })
        .to_string();
        let resp = format!(
            "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{}",
            out.len(),
            out
        );
        let _ = stream.write_all(resp.as_bytes());
        return;
    }

    if method == "POST" && path.starts_with("/action") {
        let _guard = action_lock.lock();
        let req_obj: BridgeActionRequest = serde_json::from_slice(&body_buf).unwrap_or_default();
        let result = execute_bridge_action(proxy, req_obj);
        let out = serde_json::to_string(&result)
            .unwrap_or_else(|_| r#"{"ok":false,"error":"serialization failed"}"#.to_string());
        let resp = format!(
            "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{}",
            out.len(),
            out
        );
        let _ = stream.write_all(resp.as_bytes());
        return;
    }

    let not_found = r#"{"ok":false,"error":"not found"}"#;
    let resp = format!(
        "HTTP/1.1 404 Not Found\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{}",
        not_found.len(),
        not_found
    );
    let _ = stream.write_all(resp.as_bytes());
}

fn start_native_browser_bridge(
    daemon_addr: String,
    proxy: EventLoopProxy<UserEvent>,
    info: Arc<Mutex<SharedBrowserInfo>>,
) {
    let Ok(listener) = TcpListener::bind("127.0.0.1:0") else {
        return;
    };
    let Ok(local_addr) = listener.local_addr() else {
        return;
    };
    let bridge_url = format!("http://127.0.0.1:{}", local_addr.port());
    let nanos = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_nanos())
        .unwrap_or(0);
    let token = format!("unjess_wv2_{}_{:x}", std::process::id(), nanos);

    // Background heartbeat registration to unjessd
    let hb_daemon = daemon_addr.clone();
    let hb_url = bridge_url.clone();
    let hb_tok = token.clone();
    let hb_info = Arc::clone(&info);
    std::thread::spawn(move || {
        loop {
            post_bridge_heartbeat(&hb_daemon, &hb_url, &hb_tok, &hb_info);
            std::thread::sleep(Duration::from_secs(3));
        }
    });

    let action_lock = Arc::new(Mutex::new(()));
    std::thread::spawn(move || {
        for incoming in listener.incoming().flatten() {
            let tok = token.clone();
            let prx = proxy.clone();
            let inf = Arc::clone(&info);
            let lck = Arc::clone(&action_lock);
            std::thread::spawn(move || {
                handle_bridge_client(incoming, &tok, &prx, &inf, &lck);
            });
        }
    });
}

fn main() {
    if let Err(err) = run() {
        eprintln!("Unjess Desktop Error: {err}");
        let _ = std::fs::write("unjess-error.log", format!("Error: {err}\n"));
        #[cfg(windows)]
        {
            let msg = format!(
                "Unjess Desktop failed to start:\n\n{err}\n\nIf WebView2 initialization failed, ensure Microsoft Edge WebView2 Runtime is installed and WebView2Loader.dll is next to Unjess.exe."
            );
            show_windows_error_box("Unjess Desktop Error", &msg);
        }
    }
}

fn run() -> Result<(), Box<dyn Error>> {
    let event_loop = EventLoopBuilder::<UserEvent>::with_user_event().build();
    let proxy = event_loop.create_proxy();

    let window_icon = Icon::from_rgba(ICON_RGBA_32.to_vec(), 32, 32).ok();
    let mut wb = WindowBuilder::new()
        .with_title("Unjess — Autonomous AI Coding Harness")
        .with_inner_size(LogicalSize::new(1360.0, 860.0))
        .with_min_inner_size(LogicalSize::new(480.0, 400.0));
    if let Some(icon) = window_icon {
        wb = wb.with_window_icon(Some(icon));
    }
    let window = wb.build(&event_loop)?;

    const HTML: &str = include_str!("../../../daemon/internal/api/embedded_index.html");

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

    #[cfg(windows)]
    let ui_data_dir: PathBuf = std::env::var("LOCALAPPDATA")
        .map(|p| PathBuf::from(p).join("Unjess").join("webview"))
        .unwrap_or_else(|_| PathBuf::from("webview_data"));
    #[cfg(windows)]
    let _ = std::fs::create_dir_all(&ui_data_dir);
    #[cfg(windows)]
    let mut ui_context = WebContext::new(Some(ui_data_dir));
    #[cfg(not(windows))]
    let mut ui_context = WebContext::default();

    let ipc_proxy = proxy.clone();
    let ui_builder = WebViewBuilder::new_with_web_context(&mut ui_context)
        .with_clipboard(true)
        .with_ipc_handler(move |req| {
            let body = req.body();
            if let Ok(msg) = serde_json::from_str::<UiIpcMessage>(body) {
                match msg.msg_type.as_str() {
                    "browser_bounds" => {
                        let _ = ipc_proxy.send_event(UserEvent::UpdateBrowserBounds {
                            visible: msg.visible,
                            x: msg.x,
                            y: msg.y,
                            width: msg.width,
                            height: msg.height,
                        });
                    }
                    "browser_nav" => {
                        let _ = ipc_proxy.send_event(UserEvent::BrowserUiCommand {
                            action: msg.action,
                            url: msg.url,
                        });
                    }
                    _ => {}
                }
            }
        });

    #[cfg(target_os = "linux")]
    let vbox = window
        .default_vbox()
        .expect("default_vbox should exist on tao unix window");
    #[cfg(target_os = "linux")]
    let ui_webview = if is_online {
        ui_builder
            .with_url(format!("http://{addr}/desktop"))
            .build_gtk(vbox)?
    } else {
        ui_builder.with_html(HTML).build_gtk(vbox)?
    };

    #[cfg(not(target_os = "linux"))]
    let ui_webview = if is_online {
        ui_builder
            .with_url(format!("http://{addr}/desktop"))
            .build(&window)?
    } else {
        ui_builder.with_html(HTML).build(&window)?
    };

    // Persistent child WebView2 for the Sidebar / Tab Browser (preserves logins, cookies, and sessions)
    #[cfg(windows)]
    let browser_data_dir: PathBuf = std::env::var("LOCALAPPDATA")
        .map(|p| PathBuf::from(p).join("Unjess").join("browser_webview"))
        .unwrap_or_else(|_| PathBuf::from("browser_webview_data"));
    #[cfg(windows)]
    let _ = std::fs::create_dir_all(&browser_data_dir);
    #[cfg(windows)]
    let mut browser_context = WebContext::new(Some(browser_data_dir));
    #[cfg(not(windows))]
    let mut browser_context = WebContext::default();

    let load_proxy = proxy.clone();
    let title_proxy = proxy.clone();
    let popup_proxy = proxy.clone();

    #[allow(unused_mut)]
    let mut browser_builder = WebViewBuilder::new_with_web_context(&mut browser_context)
        .with_visible(false)
        .with_clipboard(true)
        .with_autoplay(true)
        .with_back_forward_navigation_gestures(true)
        .with_bounds(Rect {
            position: LogicalPosition::new(0.0, 0.0).into(),
            size: WryLogicalSize::new(10.0, 10.0).into(),
        })
        .with_url("about:blank")
        .with_on_page_load_handler(move |event, url| {
            let loading = matches!(event, PageLoadEvent::Started);
            let _ = load_proxy.send_event(UserEvent::BrowserPageEvent {
                url: Some(url),
                title: None,
                loading: Some(loading),
            });
        })
        .with_document_title_changed_handler(move |title| {
            let _ = title_proxy.send_event(UserEvent::BrowserPageEvent {
                url: None,
                title: Some(title),
                loading: None,
            });
        })
        .with_new_window_req_handler(move |url, _features| {
            if url.starts_with("http://") || url.starts_with("https://") {
                let _ = popup_proxy.send_event(UserEvent::BrowserUiCommand {
                    action: "navigate".to_string(),
                    url,
                });
            }
            NewWindowResponse::Deny
        });

    #[cfg(windows)]
    {
        browser_builder = browser_builder.with_additional_browser_args(
            "--disable-features=msWebOOUI,msPdfOOUI,msSmartScreenProtection --disable-blink-features=AutomationControlled",
        );
    }

    #[cfg(not(target_os = "linux"))]
    let browser_webview: Option<WebView> = browser_builder.build_as_child(&window).ok();
    #[cfg(target_os = "linux")]
    let browser_webview: Option<WebView> = browser_builder.build_as_child(&window).ok();

    let shared_info = Arc::new(Mutex::new(SharedBrowserInfo {
        url: "about:blank".to_string(),
        title: String::new(),
        loading: false,
    }));

    if browser_webview.is_some() {
        start_native_browser_bridge(addr.to_string(), proxy.clone(), Arc::clone(&shared_info));
    }

    let mut ui_requested_visible = false;
    let mut last_bounds = Rect {
        position: LogicalPosition::new(0.0, 0.0).into(),
        size: WryLogicalSize::new(420.0, 720.0).into(),
    };

    event_loop.run(move |event, _, control_flow| {
        *control_flow = ControlFlow::Wait;

        let _ = (&ui_webview, &ui_context, &browser_webview, &browser_context);

        match event {
            Event::NewEvents(StartCause::Init) => {
                println!("Unjess Native Desktop window initialized successfully.");
            }
            Event::UserEvent(user_ev) => match user_ev {
                UserEvent::UpdateBrowserBounds {
                    visible,
                    x,
                    y,
                    width,
                    height,
                } => {
                    ui_requested_visible = visible && width >= 20.0 && height >= 20.0;
                    if let Some(bwv) = &browser_webview {
                        if ui_requested_visible {
                            let rect = Rect {
                                position: LogicalPosition::new(x, y).into(),
                                size: WryLogicalSize::new(width.max(20.0), height.max(20.0)).into(),
                            };
                            last_bounds = rect;
                            let _ = bwv.set_bounds(rect);
                            let _ = bwv.set_visible(true);
                        } else {
                            let _ = bwv.set_visible(false);
                        }
                    }
                }
                UserEvent::BrowserUiCommand { action, url } => {
                    if let Some(bwv) = &browser_webview {
                        match action.as_str() {
                            "navigate" => {
                                if !url.trim().is_empty() {
                                    let _ = bwv.load_url(url.trim());
                                }
                            }
                            "back" => {
                                let _ = bwv.go_back();
                            }
                            "forward" => {
                                let _ = bwv.go_forward();
                            }
                            "reload" => {
                                let _ = bwv.reload();
                            }
                            "clear_data" | "clear_cookies" => {
                                let _ = bwv.clear_all_browsing_data();
                            }
                            _ => {}
                        }
                    }
                }
                UserEvent::BrowserPageEvent {
                    url,
                    title,
                    loading,
                } => {
                    let snap = if let Ok(mut guard) = shared_info.lock() {
                        if let Some(u) = url {
                            guard.url = u;
                        }
                        if let Some(t) = title {
                            guard.title = t;
                        }
                        if let Some(l) = loading {
                            guard.loading = l;
                        }
                        guard.clone()
                    } else {
                        SharedBrowserInfo::default()
                    };

                    let payload = serde_json::json!({
                        "url": snap.url,
                        "title": snap.title,
                        "loading": snap.loading,
                    });
                    let js = format!(
                        "window.__onNativeBrowserState && window.__onNativeBrowserState({payload});"
                    );
                    let _ = ui_webview.evaluate_script(&js);
                }
                UserEvent::AgentNotifyPane { url, action } => {
                    let payload = serde_json::json!({
                        "url": url,
                        "action": action,
                    });
                    let js = format!(
                        "window.__onAgentBrowserActivity && window.__onAgentBrowserActivity({payload});"
                    );
                    let _ = ui_webview.evaluate_script(&js);
                }
                UserEvent::EnsureCaptureReady { tx } => {
                    let mut temp_shown = false;
                    if let Some(bwv) = &browser_webview {
                        if !ui_requested_visible {
                            let offscreen = Rect {
                                position: LogicalPosition::new(-1800.0, 0.0).into(),
                                size: WryLogicalSize::new(1100.0, 780.0).into(),
                            };
                            let _ = bwv.set_bounds(offscreen);
                            let _ = bwv.set_visible(true);
                            temp_shown = true;
                        }
                    }
                    let _ = tx.send(temp_shown);
                }
                UserEvent::RestoreHiddenAfterCapture => {
                    if !ui_requested_visible {
                        if let Some(bwv) = &browser_webview {
                            let _ = bwv.set_visible(false);
                            let _ = bwv.set_bounds(last_bounds);
                        }
                    }
                }
                UserEvent::NavigateUrl { url, tx } => {
                    if let Some(bwv) = &browser_webview {
                        let res = bwv.load_url(&url).map_err(|e| e.to_string());
                        let _ = tx.send(res);
                    } else {
                        let _ = tx.send(Err("Browser WebView unavailable".to_string()));
                    }
                }
                UserEvent::NavHistory { op, tx } => {
                    if let Some(bwv) = &browser_webview {
                        let res = match op.as_str() {
                            "back" => bwv.go_back(),
                            "forward" => bwv.go_forward(),
                            "reload" => bwv.reload(),
                            "clear_data" | "clear_cookies" => bwv.clear_all_browsing_data(),
                            _ => Ok(()),
                        }
                        .map_err(|e| e.to_string());
                        let _ = tx.send(res);
                    } else {
                        let _ = tx.send(Err("Browser WebView unavailable".to_string()));
                    }
                }
                UserEvent::EvalJs { script, tx } => {
                    if let Some(bwv) = &browser_webview {
                        let tx_cb = tx.clone();
                        if let Err(e) = bwv.evaluate_script_with_callback(&script, move |out| {
                            let _ = tx_cb.send(Ok(out));
                        }) {
                            let _ = tx.send(Err(e.to_string()));
                        }
                    } else {
                        let _ = tx.send(Err("Browser WebView unavailable".to_string()));
                    }
                }
                UserEvent::CdpCall {
                    method,
                    params_json,
                    tx,
                } => {
                    if let Some(bwv) = &browser_webview {
                        call_webview2_cdp(bwv, &method, &params_json, tx);
                    } else {
                        let _ = tx.send(Err("Browser WebView unavailable".to_string()));
                    }
                }
            },
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
