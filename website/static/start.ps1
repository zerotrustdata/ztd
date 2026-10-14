# Copyright (c) 2026 Lateralus Labs, LLC.
# Use of this source code is governed by the Business Source License
# included in the LICENSE file.
#
# As of the Change Date listed in the LICENSE file, this software is
# released under the Apache License, Version 2.0.
#
# One-command ztd onboarding for Windows (PowerShell):
#
#   irm https://zerotrustdata.io/start.ps1 | iex
#
# 1. Installs ztd.exe into $Env:ZTD_DIR (default ~\ztd) with the checksum-verified installer, ztd.ps1.
# 2. Starts a Gateway there on https://localhost:8443 (doctrine posture, embedded data Operator, loopback only).
# 3. Signs the CLI in headless as the Gateway owner (ztd login).
# 4. Runs a first governed action through the Gateway and the Operator.
#
# Every step reuses what already exists (binary, running Gateway, CLI identity), so rerunning is safe. Runtime
# state lives in $Env:ZTD_DIR\.ztd\. Never requires Administrator.
#
# Environment:
#   ZTD_DIR              Install and runtime directory (default: ~\ztd).
#   ZTD_VERSION          Install this X.Y.Z release instead of the latest.
#   ZTD_OVERWRITE=1      Replace an existing ztd.exe (upgrade). A running Gateway keeps its version until it is
#                        restarted (.\ztd.exe gw restart).
#   ZTD_MASTER_KEY_FILE  Absolute path to a provisioned master key file. Passed to every command.
#
# Under `irm | iex` this script runs inside the caller's session, so it never calls exit (that would close the
# window), restores the caller's location and error preference, and keeps its state inside one script block.

& {
    $ErrorActionPreference = 'Stop'
    $baseUrl = 'https://zerotrustdata.io'
    $started = Get-Date

    function Step($n, $title) { Write-Host ''; Write-Host "[$n/4] " -ForegroundColor Cyan -NoNewline; Write-Host $title }
    function Ok($text) { Write-Host '  ' -NoNewline; Write-Host ([char]0x2713) -ForegroundColor Green -NoNewline; Write-Host " $text" }
    function Info($text) { Write-Host "  $text" -ForegroundColor DarkGray }
    function Fail($text) { throw "ztd start: $text" }
    function NextStep($command, $description) {
        if ($command.Length -gt 34) { Write-Host "  $command"; Write-Host "    $description" -ForegroundColor DarkGray }
        else { Write-Host ('  {0,-34} ' -f $command) -NoNewline; Write-Host $description -ForegroundColor DarkGray }
    }

    # Runs .\ztd.exe with the master key file when one is provisioned and returns its exit code and output.
    # Native stderr must not become a terminating error, so the error preference is relaxed for the call only.
    function Ztd([switch]$NoKey) {
        $arguments = @($args)
        if (-not $NoKey -and $Env:ZTD_MASTER_KEY_FILE) { $arguments += @('--master-key-file', $Env:ZTD_MASTER_KEY_FILE) }
        $previous = $ErrorActionPreference
        $ErrorActionPreference = 'Continue'
        try { $output = (& .\ztd.exe @arguments 2>&1 | ForEach-Object { "$_" }) -join "`n" }
        finally { $ErrorActionPreference = $previous }
        [pscustomobject]@{ Ok = ($LASTEXITCODE -eq 0); Output = $output }
    }
    function ShowLog($result) {
        if ($result -and $result.Output) {
            ($result.Output -split "`n" | Select-Object -Last 25) | ForEach-Object { Write-Host "    $_" -ForegroundColor DarkGray }
        }
    }
    function Field($result, $first, $second) {
        foreach ($line in ($result.Output -split "`n")) {
            $fields = $line.Trim() -split '\s+'
            if ($fields[0] -eq $first -and ($null -eq $second -or $fields[1] -eq $second)) { return $fields }
        }
    }

    $architecture = "$Env:PROCESSOR_ARCHITECTURE".ToLower()
    if ($architecture -notin @('amd64', 'arm64')) { Fail "unsupported architecture: $architecture" }
    if ($Env:ZTD_MASTER_KEY_FILE) {
        if (-not [System.IO.Path]::IsPathRooted($Env:ZTD_MASTER_KEY_FILE)) { Fail "ZTD_MASTER_KEY_FILE must be an absolute path: $Env:ZTD_MASTER_KEY_FILE" }
        if (-not (Test-Path $Env:ZTD_MASTER_KEY_FILE -PathType Leaf)) { Fail "ZTD_MASTER_KEY_FILE not found: $Env:ZTD_MASTER_KEY_FILE" }
    }
    $dir = if ($Env:ZTD_DIR) { $Env:ZTD_DIR } else { Join-Path $HOME 'ztd' }
    New-Item -ItemType Directory -Force -Path $dir | Out-Null

    Push-Location $dir
    try {
        $dir = (Get-Location).Path
        Write-Host 'ztd' -NoNewline; Write-Host " - zero-trust execution for AI agents. Installing into $dir"

        # 1. Binary ------------------------------------------------------------
        Step 1 'ztd binary'
        if ((Test-Path .\ztd.exe -PathType Leaf) -and $Env:ZTD_OVERWRITE -ne '1') {
            Ok "reusing $dir\ztd.exe (rerun with `$Env:ZTD_OVERWRITE='1' to upgrade)"
        } else {
            # ztd.ps1 installs into the current location; its own progress lines (information stream) are replaced by ours.
            try { & ([scriptblock]::Create((Invoke-RestMethod -Uri "$baseUrl/ztd.ps1" -UseBasicParsing))) 6>$null | Out-Null }
            catch { Fail "installing the ztd binary failed; nothing else was changed: $_" }
            Ok 'installed ztd.exe - SHA-256 verified'
        }
        # --version is a root flag and takes no --master-key-file.
        $result = Ztd -NoKey --version
        if (-not $result.Ok) { ShowLog $result; Fail "$dir\ztd.exe does not run on this host" }
        Info ($result.Output -split "`n")[0]

        # 2. Gateway -----------------------------------------------------------
        Step 2 'Gateway'
        $status = Ztd gw status
        if ($status.Ok -and (Field $status 'Gateway' 'online')) {
            Ok 'already running - reusing it'
        } else {
            Info 'starting on https://localhost:8443 (doctrine posture, embedded data Operator, loopback only)...'
            $result = Ztd gw start
            if (-not $result.Ok) {
                ShowLog $result
                if ($result.Output -match 'address already in use|only one usage of each socket') {
                    Write-Warning 'A port the Gateway needs is in use, usually by a Gateway started from another directory. Stop it from its own directory with .\ztd.exe gw stop, then rerun.'
                }
                Fail 'the Gateway did not start'
            }
            Ok 'Gateway online'
        }

        # 3. Sign in -----------------------------------------------------------
        Step 3 'Sign in'
        if ((Ztd auth context).Ok) {
            Ok 'CLI already signed in - reusing the identity'
        } else {
            $result = Ztd login
            if (-not $result.Ok) { ShowLog $result; Fail 'ztd login failed' }
            Ok 'signed in as the Gateway owner (headless CLI identity)'
        }

        # 4. First governed action ---------------------------------------------
        Step 4 'First governed action'
        Info ".\ztd.exe operator run --all-active --cmd 'echo hello from ztd'"
        $result = Ztd operator run --all-active --cmd 'echo hello from ztd'
        if (-not $result.Ok) { ShowLog $result; Fail 'the first governed action failed' }
        $result.Output -split "`n" | Where-Object { $_.Trim() } | ForEach-Object { Write-Host "    $_" }
        $session = (Field $result 'Operator' 'session') | Select-Object -Index 2
        Ok 'admitted by the Gateway, executed by the Operator, signed and recorded'

        $console = $null
        $status = Ztd gw status
        if ($status.Ok) { $console = (Field $status 'Console') | Select-Object -Index 1 }
        if (-not $console) { $console = 'https://localhost:8443/console/' }
        $key = if ($Env:ZTD_MASTER_KEY_FILE) { " --master-key-file $Env:ZTD_MASTER_KEY_FILE" } else { '' }
        if (-not $session) { $session = '<id>' }

        Write-Host ''
        Write-Host ("$([char]0x2713) ztd is running ({0}s)" -f [int]((Get-Date) - $started).TotalSeconds) -ForegroundColor Green
        Write-Host ''
        Write-Host ("  cd '{0}'" -f ($dir -replace "'", "''"))
        NextStep ".\ztd.exe tui$key" 'live governance console in the terminal'
        NextStep ".\ztd.exe login web$key" "add a passkey, then open $console"
        Write-Host "  .\ztd.exe gw data audit list --operator-session-id $session$key"
        NextStep ".\ztd.exe mcp agent run claude$key" 'put an AI agent behind the Gateway'
        NextStep ".\ztd.exe gw stop$key" 'stop the Gateway; state in .ztd\ is kept'
        Write-Host ''
        Write-Host '  Docs: https://github.com/zerotrustdata/ztd/blob/main/docs/guides/getting_started.md' -ForegroundColor DarkGray
    } finally {
        Pop-Location
    }
}
