<#
.SYNOPSIS
    Unjess (unjess.si) Clean Go + Rust + Native Installer Build Script
.DESCRIPTION
    Builds the complete Unjess stack from source:
    1. Copies src\unjess\desktop\index.html to daemon\internal\api\embedded_index.html
    2. Compiles daemon\unjessd.exe (Go daemon) with -trimpath -ldflags="-s -w -buildid="
    2b. Compiles daemon\unjess-cli.exe (Go CLI -> bin\unjess.exe) with -trimpath -ldflags="-s -w -buildid="
    3. Compiles crates\unjess-desktop (Unjess.exe Rust Desktop Shell) with --remap-path-prefix
    4. Compiles native Unjess-Uninstall.exe & Unjess-Setup.exe with -trimpath -ldflags="-s -w -H=windowsgui -buildid="
    5. Calculates SHA-256 of Unjess.exe and updates version.json
.PARAMETER SkipRust
    Skip Rust desktop compilation if only updating daemon/installer.
#>

param(
    [Parameter(Mandatory = $false)]
    [switch]$SkipRust
)

$ErrorActionPreference = "Stop"

Write-Host "==========================================================" -ForegroundColor Cyan
Write-Host "  UNJESS NATIVE BUILD PIPELINE (Go + Rust + Installer)   " -ForegroundColor Green
Write-Host "==========================================================" -ForegroundColor Cyan

Get-Process -Name "Unjess*", "unjess*" -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
Start-Sleep -Milliseconds 400

$RepoRoot = $PSScriptRoot
$distDir = Join-Path $RepoRoot "dist"
$binDir = Join-Path $RepoRoot "bin"
$distBinDir = Join-Path $distDir "bin"
$daemonDir = Join-Path $RepoRoot "daemon"
$cratesDir = Join-Path $RepoRoot "crates\unjess-desktop"
$assetsDir = Join-Path $RepoRoot "assets"

if (-not (Test-Path $distDir)) {
    New-Item -ItemType Directory -Path $distDir -Force | Out-Null
}
if (-not (Test-Path $binDir)) {
    New-Item -ItemType Directory -Path $binDir -Force | Out-Null
}
if (-not (Test-Path $distBinDir)) {
    New-Item -ItemType Directory -Path $distBinDir -Force | Out-Null
}

# 1. Sync embedded index.html
Write-Host "`n[1/5] Syncing desktop index.html -> daemon\internal\api\embedded_index.html..." -ForegroundColor Yellow
$htmlSrc = Join-Path $RepoRoot "src\unjess\desktop\index.html"
$htmlDst = Join-Path $daemonDir "internal\api\embedded_index.html"
if (Test-Path $htmlSrc) {
    Copy-Item $htmlSrc $htmlDst -Force
    Write-Host "  -> Synced embedded_index.html ($([math]::Round((Get-Item $htmlDst).Length / 1KB, 1)) KB)" -ForegroundColor Green
} else {
    Write-Warning "Source index.html not found at $htmlSrc"
}

# 2. Build daemon\unjessd.exe
Write-Host "`n[2/5] Building Go daemon (unjessd.exe)..." -ForegroundColor Yellow
Push-Location $daemonDir
try {
    & go build -trimpath -ldflags="-s -w -H=windowsgui -buildid=" -o unjessd.exe ./cmd/unjessd
    if ($LASTEXITCODE -ne 0) { throw "go build unjessd failed with exit code $LASTEXITCODE" }
    $unjessdExe = Join-Path $daemonDir "unjessd.exe"
    Copy-Item $unjessdExe (Join-Path $distDir "unjessd.exe") -Force
    Copy-Item $unjessdExe (Join-Path $RepoRoot "unjessd.exe") -Force
    Write-Host "  -> Built daemon\unjessd.exe ($([math]::Round((Get-Item $unjessdExe).Length / 1MB, 2)) MB)" -ForegroundColor Green
} finally {
    Pop-Location
}

# 2b. Build Go CLI (bin\unjess.exe)
Write-Host "`n[2b/5] Building Go CLI (bin\unjess.exe)..." -ForegroundColor Yellow
Push-Location $daemonDir
try {
    & go build -trimpath -ldflags="-s -w -buildid=" -o unjess-cli.exe ./cmd/unjess-cli
    if ($LASTEXITCODE -ne 0) { throw "go build unjess-cli failed with exit code $LASTEXITCODE" }
    $cliExe = Join-Path $daemonDir "unjess-cli.exe"
    Copy-Item $cliExe (Join-Path $binDir "unjess.exe") -Force
    Copy-Item $cliExe (Join-Path $distBinDir "unjess.exe") -Force
    Copy-Item $cliExe (Join-Path $distDir "unjess-cli.exe") -Force

    $installerCmdDir = Join-Path $daemonDir "cmd\installer"
    if (-not (Test-Path $installerCmdDir)) {
        New-Item -ItemType Directory -Path $installerCmdDir -Force | Out-Null
    }
    Copy-Item $cliExe (Join-Path $installerCmdDir "unjess-cli.exe") -Force
    Write-Host "  -> Built bin\unjess.exe ($([math]::Round((Get-Item $cliExe).Length / 1MB, 2)) MB)" -ForegroundColor Green
} finally {
    Pop-Location
}

# 3. Build crates\unjess-desktop (Unjess.exe)
Write-Host "`n[3/5] Building Rust desktop shell (Unjess.exe)..." -ForegroundColor Yellow
if (-not $SkipRust) {
    Push-Location $cratesDir
    try {
        $userHome = $env:USERPROFILE
        $env:RUSTFLAGS = "--remap-path-prefix=$RepoRoot=/unjess --remap-path-prefix=$userHome=/builder"
        & cargo build --release
        if ($LASTEXITCODE -ne 0) { throw "cargo build unjess-desktop failed with exit code $LASTEXITCODE" }
        $rustTarget = Join-Path $cratesDir "target\release\unjess-desktop.exe"
        if (-not (Test-Path $rustTarget)) { throw "Expected binary not found at $rustTarget" }
        Copy-Item $rustTarget (Join-Path $RepoRoot "unjess-desktop.exe") -Force
        Copy-Item $rustTarget (Join-Path $distDir "Unjess.exe") -Force
        Write-Host "  -> Built Unjess.exe ($([math]::Round((Get-Item (Join-Path $distDir "Unjess.exe")).Length / 1MB, 2)) MB)" -ForegroundColor Green
    } finally {
        Pop-Location
    }
} else {
    Write-Host "  -> Skipping Rust compilation as requested, using existing Unjess.exe" -ForegroundColor Gray
}

# 4. Build native Uninstaller and Installer
Write-Host "`n[4/5] Building native Uninstaller & Installer..." -ForegroundColor Yellow
Push-Location $daemonDir
try {
    # Ensure staging folder exists
    $installerCmdDir = Join-Path $daemonDir "cmd\installer"
    
    # Copy icon into installer staging
    $iconSrc = Join-Path $assetsDir "unjess_icon.ico"
    if (Test-Path $iconSrc) {
        Copy-Item $iconSrc (Join-Path $installerCmdDir "unjess_icon.ico") -Force
    }

    # Build uninstaller directly into installer staging directory
    $uninstallerExe = Join-Path $installerCmdDir "Unjess-Uninstall.exe"
    & go build -trimpath -ldflags="-s -w -H=windowsgui -buildid=" -o $uninstallerExe ./cmd/uninstaller
    if ($LASTEXITCODE -ne 0) { throw "go build uninstaller failed with exit code $LASTEXITCODE" }
    Copy-Item $uninstallerExe (Join-Path $distDir "Unjess-Uninstall.exe") -Force
    Write-Host "  -> Built Unjess-Uninstall.exe ($([math]::Round((Get-Item $uninstallerExe).Length / 1MB, 2)) MB)" -ForegroundColor Green

    # Stage Unjess.exe for the installer embedding
    $unjessDesktopDist = Join-Path $distDir "Unjess.exe"
    if (-not (Test-Path $unjessDesktopDist)) {
        $candidate1 = Join-Path $RepoRoot "..\releases\Unjess.exe"
        if (Test-Path $candidate1) {
            Copy-Item $candidate1 $unjessDesktopDist -Force
        }
    }
    if (Test-Path $unjessDesktopDist) {
        Copy-Item $unjessDesktopDist (Join-Path $installerCmdDir "Unjess.exe") -Force
    } else {
        throw "Cannot build installer: $unjessDesktopDist not found. Run without -SkipRust to compile."
    }

    # Ensure unjess-cli.exe is staged for installer embedding
    $stagedCli = Join-Path $installerCmdDir "unjess-cli.exe"
    if (-not (Test-Path $stagedCli)) {
        $distCli = Join-Path $distDir "unjess-cli.exe"
        if (Test-Path $distCli) {
            Copy-Item $distCli $stagedCli -Force
        } elseif (Test-Path (Join-Path $binDir "unjess.exe")) {
            Copy-Item (Join-Path $binDir "unjess.exe") $stagedCli -Force
        }
    }

    # Ensure unjessd.exe is staged for installer embedding
    $stagedDaemon = Join-Path $installerCmdDir "unjessd.exe"
    if (-not (Test-Path $stagedDaemon)) {
        $distDaemon = Join-Path $distDir "unjessd.exe"
        if (Test-Path $distDaemon) {
            Copy-Item $distDaemon $stagedDaemon -Force
        } elseif (Test-Path (Join-Path $daemonDir "unjessd.exe")) {
            Copy-Item (Join-Path $daemonDir "unjessd.exe") $stagedDaemon -Force
        }
    }

    # Build installer
    $setupExe = Join-Path $distDir "Unjess-Setup.exe"
    & go build -trimpath -ldflags="-s -w -H=windowsgui -buildid=" -o $setupExe ./cmd/installer
    if ($LASTEXITCODE -ne 0) { throw "go build installer failed with exit code $LASTEXITCODE" }
    Write-Host "  -> Built Unjess-Setup.exe ($([math]::Round((Get-Item $setupExe).Length / 1MB, 2)) MB)" -ForegroundColor Green

    # Clean up staged installer binaries
    Remove-Item (Join-Path $installerCmdDir "unjess-cli.exe") -Force -ErrorAction SilentlyContinue
    Remove-Item (Join-Path $installerCmdDir "unjessd.exe") -Force -ErrorAction SilentlyContinue
} finally {
    Pop-Location
}

# 5. Deploy to ..\releases and update SHA-256 in version.json
Write-Host "`n[5/5] Checking release targets & updating SHA-256..." -ForegroundColor Yellow
$releasesDir = Join-Path $RepoRoot "..\releases"
$targetExe = Join-Path $distDir "Unjess.exe"
$sha256 = (Get-FileHash -Path $targetExe -Algorithm SHA256).Hash.ToLower()
Write-Host "  -> Unjess.exe SHA-256: $sha256" -ForegroundColor Cyan

# List of candidate release directories to update if they exist
$destinations = @(
    $releasesDir,
    $env:UNJESS_RELEASE_DIR
) | Where-Object { $_ -and (Test-Path $_) } | Select-Object -Unique

$repoManifestPath = Join-Path $RepoRoot "version.json"
if (Test-Path $repoManifestPath) {
    try {
        $repoRaw = [System.IO.File]::ReadAllText($repoManifestPath, [System.Text.Encoding]::UTF8)
        $repoManifest = $repoRaw | ConvertFrom-Json
        $repoManifest.sha256 = $sha256
        $repoOut = $repoManifest | ConvertTo-Json -Depth 10
        [System.IO.File]::WriteAllText($repoManifestPath, $repoOut, (New-Object System.Text.UTF8Encoding($false)))
    } catch {
        Write-Warning "Failed to update repo version.json : $_"
    }
}

foreach ($dest in $destinations) {
    Write-Host "  -> Syncing binaries to $dest..." -ForegroundColor Green
    Copy-Item (Join-Path $distDir "Unjess.exe") (Join-Path $dest "Unjess.exe") -Force
    Copy-Item (Join-Path $distDir "unjessd.exe") (Join-Path $dest "unjessd.exe") -Force
    Copy-Item (Join-Path $distDir "Unjess-Uninstall.exe") (Join-Path $dest "Unjess-Uninstall.exe") -Force
    Copy-Item (Join-Path $distDir "Unjess-Setup.exe") (Join-Path $dest "Unjess-Setup.exe") -Force
    if (Test-Path (Join-Path $distDir "unjess-cli.exe")) {
        Copy-Item (Join-Path $distDir "unjess-cli.exe") (Join-Path $dest "unjess-cli.exe") -Force
    }
    $destBin = Join-Path $dest "bin"
    if (Test-Path (Join-Path $distBinDir "unjess.exe")) {
        if (-not (Test-Path $destBin)) { New-Item -ItemType Directory -Path $destBin -Force | Out-Null }
        Copy-Item (Join-Path $distBinDir "unjess.exe") (Join-Path $destBin "unjess.exe") -Force
    }

    $manifestPath = Join-Path $dest "version.json"
    if (Test-Path $repoManifestPath) {
        Copy-Item $repoManifestPath $manifestPath -Force
        Write-Host "  -> Synced version.json (v$($repoManifest.version) + sha256) to $manifestPath" -ForegroundColor Green
    }
}

# Also update local installed locations (%LOCALAPPDATA%\Programs\Unjess and %LOCALAPPDATA%\Unjess) if present
$localInstallDirs = @(
    "$env:LOCALAPPDATA\Programs\Unjess",
    "$env:LOCALAPPDATA\Unjess"
) | Where-Object { $_ -and (Test-Path $_) } | Select-Object -Unique

foreach ($instDir in $localInstallDirs) {
    Write-Host "  -> Updating installed binaries in $instDir..." -ForegroundColor Green
    Copy-Item (Join-Path $distDir "Unjess.exe") (Join-Path $instDir "Unjess.exe") -Force -ErrorAction SilentlyContinue
    Copy-Item (Join-Path $distDir "Unjess.exe") (Join-Path $instDir "unjess-desktop.exe") -Force -ErrorAction SilentlyContinue
    Copy-Item (Join-Path $distDir "unjessd.exe") (Join-Path $instDir "unjessd.exe") -Force -ErrorAction SilentlyContinue
    Copy-Item (Join-Path $distDir "Unjess-Uninstall.exe") (Join-Path $instDir "Unjess-Uninstall.exe") -Force -ErrorAction SilentlyContinue
    if (Test-Path (Join-Path $distDir "unjess-cli.exe")) {
        Copy-Item (Join-Path $distDir "unjess-cli.exe") (Join-Path $instDir "unjess-cli.exe") -Force -ErrorAction SilentlyContinue
    }
    $instBin = Join-Path $instDir "bin"
    if (Test-Path (Join-Path $distBinDir "unjess.exe")) {
        if (-not (Test-Path $instBin)) { New-Item -ItemType Directory -Path $instBin -Force | Out-Null }
        Copy-Item (Join-Path $distBinDir "unjess.exe") (Join-Path $instBin "unjess.exe") -Force -ErrorAction SilentlyContinue
    }
}

Write-Host "`n==========================================================" -ForegroundColor Green
Write-Host "  BUILD SUCCESSFUL! All targets up to date." -ForegroundColor Green
Write-Host "==========================================================" -ForegroundColor Cyan
