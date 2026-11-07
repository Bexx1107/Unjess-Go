@echo off
setlocal
set "DIR=%~dp0"

if exist "%DIR%bin\unjess.exe" (
    "%DIR%bin\unjess.exe" %*
    exit /b %ERRORLEVEL%
)

if exist "%DIR%dist\bin\unjess.exe" (
    "%DIR%dist\bin\unjess.exe" %*
    exit /b %ERRORLEVEL%
)

if exist "%DIR%daemon\unjess-cli.exe" (
    "%DIR%daemon\unjess-cli.exe" %*
    exit /b %ERRORLEVEL%
)

if exist "%DIR%dist\unjess-cli.exe" (
    "%DIR%dist\unjess-cli.exe" %*
    exit /b %ERRORLEVEL%
)

echo [unjess] Error: Unjess CLI binary not found. 1>&2
echo Checked locations: 1>&2
echo   - %DIR%bin\unjess.exe 1>&2
echo   - %DIR%dist\bin\unjess.exe 1>&2
echo   - %DIR%daemon\unjess-cli.exe 1>&2
echo Please run .\build-all.ps1 to build the Unjess CLI. 1>&2
exit /b 1
