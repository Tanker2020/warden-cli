# install.ps1 - build and install the `warden` CLI on Windows.
#
#   powershell -ExecutionPolicy Bypass -File install.ps1
#   $env:WARDEN_INSTALL_DIR = 'C:\tools\warden'; powershell -ExecutionPolicy Bypass -File install.ps1
#
# What it does, in order (mirrors install.sh):
#   1. Finds a Go toolchain - or downloads one privately to
#      %USERPROFILE%\.warden\toolchain (per-user, no admin) if none is installed.
#   2. Downloads module dependencies.
#   3. Builds warden.exe and installs it to the install dir
#      (default: %LOCALAPPDATA%\Programs\warden).
#   4. Adds the install dir to your *user* PATH if it isn't already there.
#
# Requirements: Windows 10/11, PowerShell 5.1+ (built in). The build pulls all
# dependencies (including the sac-lang language front-end) from the network -
# no sibling repos required.

$ErrorActionPreference = 'Stop'

function Say($msg)  { Write-Host "==> $msg" -ForegroundColor Green }
function Warn($msg) { Write-Host "warning: $msg" -ForegroundColor Yellow }
function Die($msg)  { Write-Host "error: $msg" -ForegroundColor Red; exit 1 }

# ---------- locations ----------
$RepoDir      = $PSScriptRoot
$InstallDir   = if ($env:WARDEN_INSTALL_DIR) { $env:WARDEN_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA 'Programs\warden' }
$ToolchainDir = Join-Path $env:USERPROFILE '.warden\toolchain'
$GoVersion    = if ($env:WARDEN_GO_VERSION) { $env:WARDEN_GO_VERSION } else { '1.25.1' }  # used only when Go isn't installed
$MinGoMinor   = 24                                                                        # go.mod says go 1.24

# ---------- 1. find or fetch Go ----------
function Test-GoOk($goExe) {
    try {
        $v = & $goExe version 2>$null
        if ($v -match 'go1\.(\d+)') { return [int]$Matches[1] -ge $MinGoMinor }
    } catch { }
    return $false
}

$GoBin = $null
$systemGo = Get-Command go -ErrorAction SilentlyContinue
if ($systemGo -and (Test-GoOk $systemGo.Source)) {
    $GoBin = $systemGo.Source
    Say "using installed Go: $(& $GoBin version)"
}
elseif ((Test-Path (Join-Path $ToolchainDir 'go\bin\go.exe')) -and (Test-GoOk (Join-Path $ToolchainDir 'go\bin\go.exe'))) {
    $GoBin = Join-Path $ToolchainDir 'go\bin\go.exe'
    Say "using previously downloaded Go: $(& $GoBin version)"
}
else {
    $arch = switch ($env:PROCESSOR_ARCHITECTURE) {
        'AMD64' { 'amd64' }
        'ARM64' { 'arm64' }
        default { Die "unsupported architecture: $($env:PROCESSOR_ARCHITECTURE)" }
    }
    $zipName = "go$GoVersion.windows-$arch.zip"
    Say "Go not found - downloading go$GoVersion (windows/$arch) to $ToolchainDir (private to warden, no admin)"
    New-Item -ItemType Directory -Force -Path $ToolchainDir | Out-Null
    $tmpZip = Join-Path $env:TEMP $zipName
    # TLS 1.2 for Windows PowerShell 5.1 (PowerShell 7+ has it by default)
    [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
    Invoke-WebRequest -Uri "https://go.dev/dl/$zipName" -OutFile $tmpZip -UseBasicParsing
    $oldGo = Join-Path $ToolchainDir 'go'
    if (Test-Path $oldGo) { Remove-Item -Recurse -Force $oldGo }
    Expand-Archive -Path $tmpZip -DestinationPath $ToolchainDir -Force
    Remove-Item $tmpZip -ErrorAction SilentlyContinue
    $GoBin = Join-Path $ToolchainDir 'go\bin\go.exe'
    if (-not (Test-GoOk $GoBin)) { Die 'downloaded Go failed its version check' }
    Say "downloaded $(& $GoBin version)"
}

# ---------- 2. dependencies ----------
Say 'downloading module dependencies'
Push-Location $RepoDir
try {
    & $GoBin mod download
    if ($LASTEXITCODE -ne 0) { Die 'go mod download failed' }

    # ---------- 3. build + install ----------
    Say 'building warden'
    New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
    $exePath = Join-Path $InstallDir 'warden.exe'
    & $GoBin build -trimpath -o $exePath ./cmd/warden
    if ($LASTEXITCODE -ne 0) { Die 'build failed' }
    Say "installed $(& $exePath --version) -> $exePath"
}
finally {
    Pop-Location
}

# ---------- 4. PATH (user scope, persistent) ----------
$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
$onPath = ($userPath -split ';' | Where-Object { $_.TrimEnd('\') -ieq $InstallDir.TrimEnd('\') }).Count -gt 0
if ($onPath) {
    Say "$InstallDir is already on your PATH - you're done"
}
else {
    $newPath = if ([string]::IsNullOrEmpty($userPath)) { $InstallDir } else { "$userPath;$InstallDir" }
    [Environment]::SetEnvironmentVariable('Path', $newPath, 'User')
    $env:Path = "$env:Path;$InstallDir"   # current session too
    Say "added $InstallDir to your user PATH"
    Warn 'open a new terminal for other windows to pick it up'
}

Write-Host ''
Write-Host 'Next steps:'
Write-Host '  warden login       # authenticate (nyxtra.dev; --issuer http://localhost:5173 for local dev)'
Write-Host '  warden deploy --file <rule>.sac'
