# Copyright (c) 2026 Lateralus Labs, LLC.
# Use of this source code is governed by the Business Source License
# included in the LICENSE file.
#
# As of the Change Date listed in the LICENSE file, this software is
# released under the Apache License, Version 2.0.

# ztd Windows dev setup: install the toolchain, make build, and offer to add the
# repository root to PATH. The full contributor flow also builds
# evaluation-explorer, installs the contributor toolchain that `make ci` needs
# (Go dev tools, Node deps), and runs make dev-check; Node.js is only required
# for that full flow.
# Pass --build-only to stop after `make build` (Go only; no Node.js needed).
# Requires PowerShell 7+ (pwsh). See docs/architecture/scripts.md.

#Requires -Version 7.0

$ErrorActionPreference = "Stop"

$Script:RepoRoot = (Resolve-Path "$PSScriptRoot\..").Path
$Script:ExplorerDir = Join-Path $Script:RepoRoot "evaluation-explorer"
$Script:ExplorerDist = Join-Path $Script:ExplorerDir "dist\index.html"
$Script:NodeRequirement = "22.22.2+, 24.15+, or 26+"
$Script:AutoYes = $false
$Script:BuildOnly = $false

function Show-Usage {
    @"
Usage: pwsh scripts/windows-setup.ps1 [options]

Installs the toolchain needed to build ztd and, by default, to run 'make ci'
(Go dev tools and Node dependencies).

Options:
  -y, --yes       Install missing tooling without prompting
  --build-only    Only set up what 'make build' needs; skip the contributor toolchain
  -h, --help      Show this help
"@
}

foreach ($arg in $args) {
    if ($arg -in @("-y", "--yes")) {
        $Script:AutoYes = $true
    } elseif ($arg -eq "--build-only") {
        $Script:BuildOnly = $true
    } elseif ($arg -in @("-h", "--help")) {
        Show-Usage
        exit 0
    } else {
        Write-Host "Unknown option: $arg"
        Show-Usage
        exit 2
    }
}

function Confirm-Setup {
    param([string]$Prompt)
    if ($Script:AutoYes) { return $true }
    $response = Read-Host $Prompt
    return $response -match '^[Yy]$'
}

function Get-GoMinVersion {
    $line = Select-String -Path (Join-Path $Script:RepoRoot "go.mod") -Pattern '^go ' | Select-Object -First 1
    if (-not $line) {
        throw "could not read the required Go version from go.mod"
    }
    return $line.Line.Substring(3).Trim()
}

function Parse-GoVersion {
    param([string]$Version)
    if ($Version -match '(\d+)\.(\d+)(?:\.(\d+))?') {
        $patch = if ($Matches[3]) { $Matches[3] } else { "0" }
        return [Version]"$($Matches[1]).$($Matches[2]).$patch"
    }
    return $null
}

function Test-VersionAtLeast {
    param(
        [Version]$Have,
        [Version]$Need
    )
    return $Have -ge $Need
}

function Test-Git {
    if (Get-Command git -ErrorAction SilentlyContinue) {
        $version = (git --version) -replace '^git version ', ''
        Write-Host "  git: detected ($version)" -ForegroundColor Green
        return $true
    }
    Write-Host "  git: missing" -ForegroundColor Yellow
    return $false
}

function Test-Make {
    if (Get-Command make -ErrorAction SilentlyContinue) {
        Write-Host "  make: detected" -ForegroundColor Green
        return $true
    }
    Write-Host "  make: missing" -ForegroundColor Yellow
    return $false
}

function Test-Curl {
    if (Get-Command curl.exe -ErrorAction SilentlyContinue) {
        Write-Host "  curl: detected" -ForegroundColor Green
        return $true
    }
    Write-Host "  curl: missing (used to download Go and Node.js)" -ForegroundColor Yellow
    return $false
}

# GOTOOLCHAIN=auto lets `go version` report the go.mod toolchain while an
# explicit GOROOT still points the compiler at the older installed stdlib,
# which fails later with "package crypto/... is not in std".
function Assert-GoRootMatches {
    param([Version]$Have)
    if (-not $env:GOROOT) {
        return
    }
    $versionFile = Join-Path $env:GOROOT "VERSION"
    if (-not (Test-Path $versionFile)) {
        return
    }
    $rootVersion = Parse-GoVersion (Get-Content $versionFile -TotalCount 1)
    if ($rootVersion -and $rootVersion -ne $Have) {
        Write-Host "  go: GOROOT=$env:GOROOT is Go v$($rootVersion.ToString()) but the active toolchain is v$($Have.ToString())." -ForegroundColor Red
        Write-Host "      Builds will use the wrong standard library. Remove the GOROOT environment variable, for example:" -ForegroundColor Red
        Write-Host "      [Environment]::SetEnvironmentVariable('GOROOT', `$null, 'User')   # or 'Machine' from an elevated shell" -ForegroundColor Red
        Write-Host "      then open a new terminal and re-run this script." -ForegroundColor Red
        exit 1
    }
}

function Test-Go {
    param([Version]$Need)
    if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
        Write-Host "  go: missing (need >= $($Need.ToString()) from go.mod)" -ForegroundColor Yellow
        return $false
    }
    $goOutput = go version 2>$null
    $have = $null
    if ($goOutput -match 'go(\d+\.\d+(?:\.\d+)?)') {
        $have = Parse-GoVersion $Matches[1]
    }
    if (-not $have) {
        Write-Host "  go: detected but version unknown" -ForegroundColor Yellow
        return $false
    }
    if (Test-VersionAtLeast -Have $have -Need $Need) {
        Assert-GoRootMatches -Have $have
        Write-Host "  go: detected (v$($have.ToString()), need >= $($Need.ToString()))" -ForegroundColor Green
        return $true
    }
    Write-Host "  go: detected (v$($have.ToString())) but go.mod requires >= $($Need.ToString())" -ForegroundColor Yellow
    return $false
}

# Node.js lines the frontend toolchains accept. This mirrors the strictest
# frontend engines range (jsdom: ^22.22.2 || ^24.15.0 || >=26.0.0); keep it in
# step with console/package.json "engines".
function Test-NodeVersionSupported {
    param([Version]$Have)
    switch ($Have.Major) {
        22 { return $Have -ge [Version]"22.22.2" }
        24 { return $Have -ge [Version]"24.15.0" }
        default { return $Have.Major -ge 26 }
    }
}

function Test-Node {
    if (-not (Get-Command node -ErrorAction SilentlyContinue)) {
        Write-Host "  node: missing (need $Script:NodeRequirement for evaluation-explorer and console builds)" -ForegroundColor Yellow
        return $false
    }
    if (-not (Get-Command npm, npm.cmd -ErrorAction SilentlyContinue)) {
        Write-Host "  npm: missing" -ForegroundColor Yellow
        return $false
    }
    $have = $null
    if ((node --version) -match '(\d+)\.(\d+)\.(\d+)') {
        $have = [Version]"$($Matches[1]).$($Matches[2]).$($Matches[3])"
    }
    if (-not $have) {
        Write-Host "  node: detected but version unknown" -ForegroundColor Yellow
        return $false
    }
    if (Test-NodeVersionSupported -Have $have) {
        Write-Host "  node: detected (v$($have.ToString()))" -ForegroundColor Green
        $npmVer = (npm.cmd --version 2>$null)
        Write-Host "  npm: detected ($npmVer)" -ForegroundColor Green
        return $true
    }
    Write-Host "  node: detected (v$($have.ToString())) but need $Script:NodeRequirement" -ForegroundColor Yellow
    return $false
}

function Test-Ripgrep {
    if (Get-Command rg.exe, rg -ErrorAction SilentlyContinue) {
        Write-Host "  rg: detected" -ForegroundColor Green
        return $true
    }
    Write-Host "  rg: missing (ripgrep)" -ForegroundColor Yellow
    return $false
}

function Refresh-SessionPath {
    $env:Path = [System.Environment]::GetEnvironmentVariable("Path", "Machine") + ";" +
        [System.Environment]::GetEnvironmentVariable("Path", "User")
}

function Ensure-NpmBashShims {
    $localBin = Join-Path $env:USERPROFILE ".local\bin"
    if (-not (Test-Path $localBin)) {
        New-Item -ItemType Directory -Path $localBin -Force | Out-Null
    }
    $npmShim = Join-Path $localBin "npm"
    if (-not (Test-Path $npmShim)) {
        $content = "#!/bin/sh`nexec npm.cmd `"`$@`"`n"
        [System.IO.File]::WriteAllText($npmShim, $content, [System.Text.Encoding]::ASCII)
    }
    $npxShim = Join-Path $localBin "npx"
    if (-not (Test-Path $npxShim)) {
        $content = "#!/bin/sh`nexec npx.cmd `"`$@`"`n"
        [System.IO.File]::WriteAllText($npxShim, $content, [System.Text.Encoding]::ASCII)
    }
}

function Install-WindowsPackages {
    param([string[]]$Missing)

    $winget = Get-Command winget -ErrorAction SilentlyContinue
    $choco = Get-Command choco -ErrorAction SilentlyContinue
    if ($Missing.Count -gt 0 -and -not $winget -and -not $choco) {
        Write-Host "FATAL: install winget (App Installer from the Microsoft Store) or Chocolatey, then rerun this script." -ForegroundColor Red
        exit 1
    }

    $commands = @()
    if ($winget) {
        if ($Missing -contains "git") {
            $commands += "winget install --id Git.Git -e --accept-package-agreements --accept-source-agreements"
        }
        if ($Missing -contains "go") {
            $commands += "winget install --id Golang.Go -e --accept-package-agreements --accept-source-agreements"
        }
        if ($Missing -contains "node") {
            $commands += "winget install --id OpenJS.NodeJS.LTS -e --accept-package-agreements --accept-source-agreements"
        }
        if ($Missing -contains "make") {
            $commands += "winget install --id ezwinports.make -e --accept-package-agreements --accept-source-agreements"
        }
        if ($Missing -contains "rg") {
            $commands += "winget install --id BurntSushi.ripgrep.MSVC -e --accept-package-agreements --accept-source-agreements"
        }
    } elseif ($choco) {
        $packages = @()
        if ($Missing -contains "git") { $packages += "git" }
        if ($Missing -contains "go") { $packages += "golang" }
        if ($Missing -contains "node") { $packages += "nodejs-lts" }
        if ($Missing -contains "make") { $packages += "make" }
        if ($Missing -contains "rg") { $packages += "ripgrep" }
        if ($packages.Count -gt 0) {
            $commands += "choco install -y $($packages -join ' ')"
        }
    }

    if ($commands.Count -eq 0) {
        return
    }

    Write-Host "Suggested installs:" -ForegroundColor Yellow
    $commands | ForEach-Object { Write-Host "  $_" }
    if (Confirm-Setup "Install missing tooling now? [y/N] ") {
        foreach ($cmd in $commands) {
            Invoke-Expression $cmd
        }
        Refresh-SessionPath
        Ensure-NpmBashShims
    }
}

function Ensure-WritableGoPath {
    # A GOPATH pointing at a read-only location (e.g. C:\Program Files\Go\bin) makes
    # `go install` and the module checksum DB fail with "Access is denied".
    $gopath = (go env GOPATH 2>$null)
    $writable = $false
    if ($gopath) {
        try {
            New-Item -ItemType Directory -Path $gopath -Force -ErrorAction Stop | Out-Null
            $probe = Join-Path $gopath ".ztd-write-test"
            [System.IO.File]::WriteAllText($probe, "x")
            Remove-Item $probe -Force -ErrorAction SilentlyContinue
            $writable = $true
        } catch {
            $writable = $false
        }
    }
    $goRoot = (go env GOROOT 2>$null)
    $insideGoRoot = $goRoot -and $gopath -and $gopath.StartsWith($goRoot, [System.StringComparison]::OrdinalIgnoreCase)
    $parent = if ($gopath) { Split-Path $gopath -Leaf } else { "" }
    if ($writable -and -not $insideGoRoot -and $parent -ne "bin") {
        return
    }
    $newGoPath = Join-Path $env:USERPROFILE "go"
    Write-Host "  GOPATH '$gopath' is not usable (read-only or inside the Go install); using $newGoPath." -ForegroundColor Yellow
    $env:GOPATH = $newGoPath
    Remove-Item Env:\GOBIN -ErrorAction SilentlyContinue
    New-Item -ItemType Directory -Path $newGoPath -Force | Out-Null
    $userGoPath = [Environment]::GetEnvironmentVariable("GOPATH", "User")
    if ($userGoPath -and $userGoPath -ne $newGoPath) {
        [Environment]::SetEnvironmentVariable("GOPATH", $newGoPath, "User")
        Write-Host "  updated user GOPATH (was '$userGoPath') to $newGoPath." -ForegroundColor Yellow
    }
}

function Test-NodeDependencies {
    param([string]$Directory)
    $marker = Join-Path $Directory "node_modules\.package-lock.json"
    if (-not (Test-Path $marker) -or -not (Test-Path (Join-Path $Directory "package.json"))) {
        return $false
    }
    $installedAt = (Get-Item $marker).LastWriteTimeUtc
    foreach ($manifest in @("package.json", "package-lock.json")) {
        $path = Join-Path $Directory $manifest
        if ((Test-Path $path) -and (Get-Item $path).LastWriteTimeUtc -gt $installedAt) {
            return $false
        }
    }
    npm.cmd ls --prefix $Directory --depth=0 --offline *> $null
    return ($LASTEXITCODE -eq 0)
}

function Build-EvaluationExplorer {
    if (Test-Path $Script:ExplorerDist) {
        Write-Host "  evaluation-explorer dist already present — skipping frontend build" -ForegroundColor Green
        return
    }

    Write-Host "  building evaluation-explorer assets (required by make build)..." -ForegroundColor Cyan
    Push-Location $Script:ExplorerDir
    try {
        $installed = Test-NodeDependencies -Directory $Script:ExplorerDir
        if ($installed) {
            Write-Host "  evaluation-explorer dependencies are current — skipping npm install" -ForegroundColor Green
        }
        if (-not $installed -and (Test-Path "package-lock.json")) {
            npm.cmd ci
            $installed = ($LASTEXITCODE -eq 0)
            if (-not $installed) {
                Write-Host "  npm ci failed (lockfile out of sync, e.g. missing Windows optional deps); falling back to npm install without touching package-lock.json..." -ForegroundColor Yellow
            }
        }
        if (-not $installed) {
            npm.cmd install --no-package-lock
            if ($LASTEXITCODE -ne 0) {
                Write-Host "FATAL: npm install failed in evaluation-explorer." -ForegroundColor Red
                exit 1
            }
        }
        npm.cmd run build
        if ($LASTEXITCODE -ne 0) {
            Write-Host "FATAL: evaluation-explorer build failed." -ForegroundColor Red
            exit 1
        }
    } finally {
        Pop-Location
    }
}

function Test-ZTDRepositoryRoot {
    param([string]$Directory)

    if ([string]::IsNullOrWhiteSpace($Directory)) {
        return $false
    }
    try {
        $root = [System.IO.Path]::GetFullPath($Directory)
    } catch {
        return $false
    }
    $goMod = Join-Path $root "go.mod"
    $setupScript = Join-Path $root "scripts\windows-setup.ps1"
    if (-not (Test-Path -LiteralPath $goMod -PathType Leaf) -or
        -not (Test-Path -LiteralPath $setupScript -PathType Leaf)) {
        return $false
    }
    return [bool](Select-String -LiteralPath $goMod -Pattern '^module\s+github\.com/zerotrustdata/ztd\s*$' -Quiet)
}

function Get-ZTDCheckoutPathSegments {
    param(
        [string[]]$Segments,
        [string]$CurrentRoot
    )

    $current = [System.IO.Path]::GetFullPath($CurrentRoot).TrimEnd([char[]]'\/')
    $updated = [System.Collections.Generic.List[string]]::new()
    $updated.Add($current)
    foreach ($segment in $Segments) {
        if ([string]::IsNullOrWhiteSpace($segment)) {
            continue
        }
        try {
            $normalized = [System.IO.Path]::GetFullPath($segment).TrimEnd([char[]]'\/')
        } catch {
            $updated.Add($segment)
            continue
        }
        if ($normalized -ieq $current -or (Test-ZTDRepositoryRoot -Directory $normalized)) {
            continue
        }
        $updated.Add($segment)
    }
    return $updated.ToArray()
}

# Shows the planned user Path changes and writes them only after the user agrees
# (or passed --yes). A non-interactive run without --yes leaves the user Path
# unchanged rather than failing a setup that already built ztd.
function Configure-Path {
    param([bool]$IncludeDevTools = $false)
    $Script:PathConfigured = $false
    $Script:PathChanges = @()
    $userPath = [Environment]::GetEnvironmentVariable("Path", "User")
    $originalSegments = @()
    if ($userPath) {
        $originalSegments = $userPath.Split(';', [System.StringSplitOptions]::RemoveEmptyEntries)
    }
    $segments = @(Get-ZTDCheckoutPathSegments -Segments $originalSegments -CurrentRoot $Script:RepoRoot)
    $staleRoots = @($originalSegments | Where-Object {
        $_ -and $_ -ine $Script:RepoRoot -and (Test-ZTDRepositoryRoot -Directory $_)
    })

    $toAdd = @()
    if ($IncludeDevTools) {
        $goBin = if (Get-Command go -ErrorAction SilentlyContinue) {
            $bin = (go env GOBIN)
            if (-not $bin) { $bin = Join-Path (go env GOPATH) "bin" }
            $bin
        } else {
            Join-Path $env:USERPROFILE "go\bin"
        }
        if ($goBin -and (Test-Path $goBin)) {
            $toAdd += $goBin
        }
        $localBin = Join-Path $env:USERPROFILE ".local\bin"
        if (Test-Path $localBin) {
            $toAdd += $localBin
        }
    }

    $pathChanged = ($originalSegments -join ';') -cne ($segments -join ';')
    if ($pathChanged) {
        $Script:PathChanges += "put $($segments[0]) first"
    }
    foreach ($root in $staleRoots) {
        $Script:PathChanges += "remove stale ztd checkout $root"
    }
    foreach ($dir in $toAdd) {
        if ($segments -contains $dir) {
            Write-Host "  $dir already present in user PATH — skipping." -ForegroundColor Green
        } else {
            $segments += $dir
            $pathChanged = $true
            $Script:PathChanges += "add $dir"
        }
    }

    # Process-only Path for the remaining setup steps; it does not outlive this script.
    $processSegments = $env:Path.Split(';', [System.StringSplitOptions]::RemoveEmptyEntries)
    $processSegments = @(Get-ZTDCheckoutPathSegments -Segments $processSegments -CurrentRoot $Script:RepoRoot)
    foreach ($dir in $toAdd) {
        if ($processSegments -notcontains $dir) {
            $processSegments += $dir
        }
    }
    $env:Path = $processSegments -join ';'

    if (-not $pathChanged) {
        Write-Host "  user PATH already configured." -ForegroundColor Green
        $Script:PathConfigured = $true
        return
    }

    Write-Host "  Proposed user PATH changes:"
    foreach ($change in $Script:PathChanges) {
        Write-Host "    $change"
    }
    if (-not $Script:AutoYes -and [Console]::IsInputRedirected) {
        Write-Host "  stdin is redirected; leaving user PATH unchanged (pass --yes to apply)." -ForegroundColor Yellow
        return
    }
    if (-not (Confirm-Setup "Update your user PATH? [y/N] ")) {
        Write-Host "  Left user PATH unchanged." -ForegroundColor Yellow
        return
    }

    [Environment]::SetEnvironmentVariable("Path", ($segments -join ';'), "User")
    Write-Host "  updated user PATH." -ForegroundColor Green
    $Script:PathConfigured = $true
}

function Show-NextSteps {
    param([bool]$BuildOnly = $false)
    $binary = if (Test-Path (Join-Path $Script:RepoRoot "ztd.exe")) { ".\ztd.exe" } else { ".\ztd" }
    Write-Host ""
    Write-Host "[SETUP COMPLETE]" -ForegroundColor Green
    Write-Host "---------------------------------------------------------------"
    Write-Host "Binary: $binary"
    Write-Host ""
    Write-Host "Verify:"
    Write-Host "  $binary --version"
    Write-Host ""
    if (-not $BuildOnly) {
        Write-Host "  make dev-check        # confirms every tool 'make ci' needs is installed"
        Write-Host ""
        Write-Host "Contributor workflow:"
        Write-Host "  make ci               # full local CI (platform, protocol, console, website)"
        Write-Host "  make help             # all targets"
        Write-Host ""
    }
    Write-Host "Configure (optional): .env  # remote Ollama endpoint, Cloudflare token; ./ztd loads it at startup"
    Write-Host ""
    Write-Host "Next:"
    Write-Host "  make quickstart       # starts a local Gateway, signs you in, and runs your first governed action"
    Write-Host ""
    Write-Host "Docs: docs/guides/getting_started.md"
    if ($Script:PathConfigured) {
        Write-Host "Note: open a new terminal for PATH changes to take effect." -ForegroundColor Yellow
    } else {
        Write-Host "Note: user PATH was not changed. Run $binary from $($Script:RepoRoot), or rerun this script and accept the PATH update:" -ForegroundColor Yellow
        foreach ($change in $Script:PathChanges) {
            Write-Host "  $change" -ForegroundColor Yellow
        }
    }
}

if (-not (Test-Path (Join-Path $Script:RepoRoot "go.mod"))) {
    Write-Host "FATAL: run this script from a ztd repository clone (go.mod not found)." -ForegroundColor Red
    exit 1
}

$goMin = Parse-GoVersion (Get-GoMinVersion)

if ($Script:BuildOnly) {
    $totalSteps = 3
    $prereqsText = "git, make, curl, go >= $($goMin.ToString())"
} else {
    $totalSteps = 6
    $prereqsText = "git, make, curl, go >= $($goMin.ToString()), node ($Script:NodeRequirement), rg"
}

Write-Host "`n[SETUP] ztd Windows dev environment setup`n" -ForegroundColor Cyan
Write-Host "[STEP 1/$totalSteps] Checking prerequisites ($prereqsText)..." -ForegroundColor Yellow

$missing = @()
if (-not (Test-Git)) { $missing += "git" }
if (-not (Test-Make)) { $missing += "make" }
if (-not (Test-Curl)) { $missing += "curl" }
if (-not (Test-Go -Need $goMin)) { $missing += "go" }

if (-not $Script:BuildOnly) {
    if (-not (Test-Node)) { $missing += "node" }
    if (-not (Test-Ripgrep)) { $missing += "rg" }
}

if ($missing.Count -gt 0) {
    Write-Host ""
    Write-Host "Missing or outdated tooling: $($missing -join ', ')" -ForegroundColor Yellow
    Install-WindowsPackages -Missing $missing
    Refresh-SessionPath
    Write-Host ""
    Write-Host "Re-checking prerequisites..." -ForegroundColor Yellow
    $missing = @()
    if (-not (Test-Git)) { $missing += "git" }
    if (-not (Test-Make)) { $missing += "make" }
    if (-not (Test-Curl)) { $missing += "curl" }
    if (-not (Test-Go -Need $goMin)) { $missing += "go" }
    if (-not $Script:BuildOnly) {
        if (-not (Test-Node)) { $missing += "node" }
        if (-not (Test-Ripgrep)) { $missing += "rg" }
    }
    if ($missing.Count -gt 0) {
        Write-Host "FATAL: still missing: $($missing -join ', ')" -ForegroundColor Red
        Write-Host "Install the remaining tools, then rerun: pwsh scripts/windows-setup.ps1"
        exit 1
    }
}

Ensure-NpmBashShims
Ensure-WritableGoPath

function Invoke-Step {
    param([string]$Label, [scriptblock]$Command)
    & $Command
    if ($LASTEXITCODE -ne 0) {
        Write-Host "FATAL: $Label failed (exit code $LASTEXITCODE). Fix the error above and rerun: pwsh scripts/windows-setup.ps1" -ForegroundColor Red
        exit 1
    }
}

if (-not $Script:BuildOnly) {
    Write-Host "`n[STEP 2/$totalSteps] Building evaluation-explorer assets..." -ForegroundColor Yellow
    Build-EvaluationExplorer
}

$buildStep = if ($Script:BuildOnly) { 2 } else { 3 }
Write-Host "`n[STEP $buildStep/$totalSteps] Building ztd..." -ForegroundColor Yellow
Set-Location $Script:RepoRoot
Invoke-Step "make build" { make build }
Write-Host "Build successful." -ForegroundColor Green
# Every ./ztd command loads .env at startup; create it from the commented
# template and keep an existing one.
if (Test-Path (Join-Path $Script:RepoRoot ".env")) {
    Write-Host "Kept existing .env." -ForegroundColor Green
} else {
    Copy-Item (Join-Path $Script:RepoRoot ".env.example") (Join-Path $Script:RepoRoot ".env")
    Write-Host "Created .env from .env.example." -ForegroundColor Green
}

if ($Script:BuildOnly) {
    Write-Host "`n[STEP 3/$totalSteps] Offering to add the repository root to PATH..." -ForegroundColor Yellow
    Configure-Path -IncludeDevTools $false
    Show-NextSteps -BuildOnly $true
    exit 0
}

Write-Host "`n[STEP 4/$totalSteps] Installing the contributor toolchain (Go dev tools, Node deps)..." -ForegroundColor Yellow
Invoke-Step "make dev-setup" { make dev-setup }

Write-Host "`n[STEP 5/$totalSteps] Offering to add the repository root and dev tool directories to PATH..." -ForegroundColor Yellow
Configure-Path -IncludeDevTools $true

Write-Host "`n[STEP 6/$totalSteps] Verifying the toolchain (make dev-check)..." -ForegroundColor Yellow
Invoke-Step "make dev-check" { make dev-check }
Show-NextSteps -BuildOnly $false

