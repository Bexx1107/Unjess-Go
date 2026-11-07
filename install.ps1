# Unjess Windows PowerShell One-Line Installer
$ErrorActionPreference = 'Stop'
Write-Host "══════════════════════════════════════════════════" -ForegroundColor Cyan
Write-Host "  UNJESS — Universal Agentic Coding Harness v3.0.0 " -ForegroundColor White
Write-Host "══════════════════════════════════════════════════" -ForegroundColor Cyan
Write-Host ""
Write-Host "Downloading Unjess Setup from GitHub Releases..." -ForegroundColor Gray

$installerUrl = "https://github.com/Bexx1107/Unjess-Go/releases/download/v3.0.0/Unjess-Setup.exe"
$tempPath = Join-Path $env:TEMP "Unjess-Setup.exe"
$expectedHash = "78762dc4fc87cbbbfb692b05a0c6bd0ace3886660d7373d97e4e5658c87a7b8e"

[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12 -bor [Net.SecurityProtocolType]::Tls13


(New-Object System.Net.WebClient).DownloadFile($installerUrl, $tempPath)

Write-Host "Verifying SHA-256 integrity..." -ForegroundColor Gray
$actualHash = (Get-FileHash -Path $tempPath -Algorithm SHA256).Hash.ToLower()

if ($actualHash -ne $expectedHash) {
    Write-Host "Security verification failed! Hash mismatch." -ForegroundColor Red
    Remove-Item -Path $tempPath -Force -ErrorAction SilentlyContinue
    exit 1
}

Write-Host "SHA-256 verified successfully." -ForegroundColor Green
Write-Host "Starting installation..." -ForegroundColor Green
Start-Process -FilePath $tempPath
Write-Host "Setup launched! Follow the prompts to complete installation." -ForegroundColor Cyan
