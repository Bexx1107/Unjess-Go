@echo off
REM ═══════════════════════════════════════════════════════════════
REM  Unjess — Build Standalone Native Desktop App in Rust
REM ═══════════════════════════════════════════════════════════════
echo.
echo  Building Unjess Native Desktop App (Release)...
echo.
cargo build --release --manifest-path crates\unjess-desktop\Cargo.toml
if %ERRORLEVEL% EQU 0 (
    copy crates\unjess-desktop\target\release\unjess-desktop.exe .\unjess-desktop.exe >nul 2>&1
    echo.
    echo  ══════════════════════════════════════════════
    echo   BUILD SUCCESSFUL!
    echo   Executable: unjess-desktop.exe (under 1 MB)
    echo  ══════════════════════════════════════════════
    echo.
) else (
    echo.
    echo  Build failed. Check the error output above.
    echo.
)
pause
