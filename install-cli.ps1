# Unjess CLI Windows PowerShell One-Line Installer
$ErrorActionPreference = 'Stop'
Write-Host "══════════════════════════════════════════════════" -ForegroundColor Cyan
Write-Host "  UNJESS CLI — Universal Agentic Coding Harness v3.0.9 " -ForegroundColor White
Write-Host "══════════════════════════════════════════════════" -ForegroundColor Cyan
Write-Host ""
Write-Host "Downloading Unjess CLI from GitHub Releases..." -ForegroundColor Gray

$cliUrl = "https://github.com/Bexx1107/Unjess-Go/releases/download/v3.0.9/unjess-cli.exe"
$targetDir = Join-Path $env:USERPROFILE ".unjess\bin"
$expectedHash = "732111d3e036eccc17b5c6fbd085f6d9617e49971fb791987008e389aad9ac3f"

[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12 -bor [Net.SecurityProtocolType]::Tls13


if (!(Test-Path -Path $targetDir)) {
    New-Item -ItemType Directory -Path $targetDir -Force | Out-Null
}

$tempPath = Join-Path $env:TEMP "unjess-cli.exe"
(New-Object System.Net.WebClient).DownloadFile($cliUrl, $tempPath)

Write-Host "Verifying SHA-256 integrity..." -ForegroundColor Gray
$actualHash = (Get-FileHash -Path $tempPath -Algorithm SHA256).Hash.ToLower()

if ($actualHash -ne $expectedHash) {
    Write-Host "Security verification failed! Hash mismatch." -ForegroundColor Red
    Remove-Item -Path $tempPath -Force -ErrorAction SilentlyContinue
    exit 1
}

Write-Host "SHA-256 verified successfully." -ForegroundColor Green

# Install as unjess.exe and alias as njss.exe
$targetExe = Join-Path $targetDir "unjess.exe"
$aliasExe = Join-Path $targetDir "njss.exe"

Copy-Item -Path $tempPath -Destination $targetExe -Force
Copy-Item -Path $tempPath -Destination $aliasExe -Force
Remove-Item -Path $tempPath -Force -ErrorAction SilentlyContinue

# Ensure ~/.unjess/bin is in User Environment Path
$userPath = [Environment]::GetEnvironmentVariable("Path", "User")
$paths = if ($userPath) { $userPath -split ";" } else { @() }
if ($paths -notcontains $targetDir) {
    $newPath = if ([string]::IsNullOrWhiteSpace($userPath)) { $targetDir } else { "$userPath;$targetDir" }
    [Environment]::SetEnvironmentVariable("Path", $newPath, "User")
    Write-Host "Added $targetDir to User PATH." -ForegroundColor Gray
}

# Update current session PATH so user can run it immediately
if (($env:PATH -split ";") -notcontains $targetDir) {
    $env:PATH = "$env:PATH;$targetDir"
}

Write-Host ""
Write-Host "✓ Unjess CLI v3.0.9 installed successfully!" -ForegroundColor Green
Write-Host "  Location : $targetExe" -ForegroundColor Gray
Write-Host "  Commands : 'unjess' or 'njss'" -ForegroundColor Cyan
Write-Host ""
Write-Host "Run 'unjess' to launch the agentic coding environment." -ForegroundColor White
