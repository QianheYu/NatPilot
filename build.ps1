param(
    [switch]$All,
    [switch]$FrontendOnly,
    [switch]$NoFrontend,
    [switch]$Test,
    [switch]$Vet,
    [switch]$Electron,
    [string]$Version = $(if ($env:VERSION) { $env:VERSION } else { "0.1.0" })
)

$ErrorActionPreference = "Stop"

# NatPilot build helper for Windows (PowerShell equivalent of build.sh).
#
#   .\build.ps1                 frontend + native server binary
#   .\build.ps1 -All            ready-to-run bundles for all six Go targets
#   .\build.ps1 -Electron       package the tray-resident Electron desktop app
#   .\build.ps1 -FrontendOnly   rebuild the SPA only
#   .\build.ps1 -NoFrontend     binaries only (SPA already built)
#   .\build.ps1 -Test / -Vet    run tests / static checks
#
# There is no C dependency (the SQLite driver is pure Go), so every target
# cross-compiles from Windows without a toolchain.

$Root = Split-Path -Parent $MyInvocation.MyCommand.Path
Set-Location $Root

$commit = "unknown"
try {
    $c = git -C $Root rev-parse --short HEAD 2>$null
    if ($LASTEXITCODE -eq 0 -and $c) { $commit = $c.Trim() }
} catch { }

$buildDate = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ssZ")
$ldflags = "-s -w -X main.version=$Version -X main.commit=$commit -X main.buildDate=$buildDate"

$env:CGO_ENABLED = "0"

$targets = @(
    @{ OS = "linux";   Arch = "amd64"; Ext = "" },
    @{ OS = "linux";   Arch = "arm64"; Ext = "" },
    @{ OS = "darwin";  Arch = "amd64"; Ext = "" },
    @{ OS = "darwin";  Arch = "arm64"; Ext = "" },
    @{ OS = "windows"; Arch = "amd64"; Ext = ".exe" },
    @{ OS = "windows"; Arch = "arm64"; Ext = ".exe" }
)

function Build-Frontend {
    Write-Host "==> building frontend" -ForegroundColor Cyan
    Push-Location (Join-Path $Root "frontend")
    try {
        npm install --include=dev --no-audit --no-fund
        if ($LASTEXITCODE -ne 0) { throw "npm install failed" }
        npm run build
        if ($LASTEXITCODE -ne 0) { throw "npm run build failed" }
    } finally { Pop-Location }
}

function Build-Native {
    Write-Host "==> building native binaries" -ForegroundColor Cyan
    New-Item -ItemType Directory -Force -Path (Join-Path $Root "bin") | Out-Null
    go build -trimpath -ldflags $ldflags -o "bin/natpilot.exe" .
    if ($LASTEXITCODE -ne 0) { throw "go build (server) failed" }
    Get-ChildItem (Join-Path $Root "bin") | Format-Table Name, Length
}

function Build-All {
    Write-Host "==> cross building release bundles (no cross toolchain required)" -ForegroundColor Cyan
    $dist = Join-Path $Root "dist"
    if (Test-Path $dist) { Remove-Item -Recurse -Force $dist }
    foreach ($t in $targets) {
        $out = Join-Path $dist "$($t.OS)-$($t.Arch)"
        New-Item -ItemType Directory -Force -Path $out | Out-Null
        Write-Host ("    -> dist/{0}-{1}/" -f $t.OS, $t.Arch)
        $env:GOOS = $t.OS
        $env:GOARCH = $t.Arch
        go build -trimpath -ldflags $ldflags -o (Join-Path $out "natpilot$($t.Ext)") .
        if ($LASTEXITCODE -ne 0) { throw "go build failed for $($t.OS)/$($t.Arch)" }
    }
    Remove-Item Env:GOOS, Env:GOARCH -ErrorAction SilentlyContinue
    Write-Host ""
    Write-Host "==> bundles" -ForegroundColor Cyan
    Get-ChildItem $dist -Directory | ForEach-Object {
        "{0,-16} {1}" -f $_.Name, ((Get-ChildItem $_.FullName | ForEach-Object Name) -join " ")
    }
}

function Build-Electron {
    # Packages the tray-resident desktop edition. The Go server binary is bundled
    # from bin/ via extraResources, so it must exist first.
    Write-Host "==> packaging the Electron desktop edition" -ForegroundColor Cyan
    if (-not (Test-Path (Join-Path $Root "bin/natpilot.exe"))) { Build-Native }
    Push-Location (Join-Path $Root "desktop")
    try {
        npm install --no-audit --no-fund
        if ($LASTEXITCODE -ne 0) { throw "npm install failed" }
        npm run dist
        if ($LASTEXITCODE -ne 0) { throw "electron-builder failed" }
    } finally { Pop-Location }
}

function Run-Tests {
    Write-Host "==> go test ./..." -ForegroundColor Cyan
    go test ./... -count=1
    if ($LASTEXITCODE -ne 0) { throw "tests failed" }
}

function Run-Vet {
    Write-Host "==> go vet (host + darwin + linux)" -ForegroundColor Cyan
    go vet ./...
    if ($LASTEXITCODE -ne 0) { throw "go vet failed" }
    foreach ($os in @("darwin", "linux")) {
        $env:GOOS = $os
        go vet ./...
        if ($LASTEXITCODE -ne 0) { throw "go vet failed for $os" }
    }
    Remove-Item Env:GOOS -ErrorAction SilentlyContinue
}

if ($Test) { Run-Tests; exit 0 }
if ($Vet) { Run-Vet; exit 0 }
if ($FrontendOnly) { Build-Frontend; exit 0 }

if (-not $NoFrontend) { Build-Frontend }
if ($Electron) { Build-Native; Build-Electron }
elseif ($All) { Build-All }
else { Build-Native }

Write-Host "done" -ForegroundColor Green
