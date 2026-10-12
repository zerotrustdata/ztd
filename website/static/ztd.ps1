$ErrorActionPreference = 'Stop'
$architecture = $Env:PROCESSOR_ARCHITECTURE.ToLower()
if ($architecture -notin @('amd64', 'arm64')) {
    throw "Unsupported architecture: $architecture"
}
if (Test-Path ./ztd.exe -PathType Container) {
    throw 'ztd.exe is a directory; refusing to replace it'
}
if ((Test-Path ./ztd.exe) -and $Env:ZTD_OVERWRITE -ne '1') {
    throw 'ztd.exe already exists; set ZTD_OVERWRITE=1 to replace it'
}
$baseUrl = 'https://zerotrustdata.io'
# Accepts X.Y.Z with an optional prerelease suffix. \z (end of string) is used instead of $ so a trailing newline cannot match.
$versionPattern = '^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?\z'
if ($Env:ZTD_VERSION) {
    $version = $Env:ZTD_VERSION
    if ($version -cnotmatch $versionPattern) { throw "Invalid ZTD_VERSION: $version (expected X.Y.Z or X.Y.Z-prerelease)" }
} else {
    $pointer = Invoke-RestMethod -Uri "$baseUrl/latest.json" -UseBasicParsing
    if ($pointer -is [string]) { $pointer = $pointer | ConvertFrom-Json }
    $version = [string]$pointer.version
    if ($version -cnotmatch $versionPattern) { throw 'latest.json does not name a valid release version; nothing was installed' }
}
Write-Host "Installing ztd $version"
$binary = "ztd-windows-$architecture.exe"
$binaryUrl = "$baseUrl/v/$version/$binary"
$temporaryBinary = New-TemporaryFile
$temporaryChecksum = New-TemporaryFile
try {
    Invoke-WebRequest -Uri $binaryUrl -OutFile $temporaryBinary -UseBasicParsing
    Invoke-WebRequest -Uri "$binaryUrl.sha256" -OutFile $temporaryChecksum -UseBasicParsing
    $checksumText = (Get-Content $temporaryChecksum -Raw)
    if (-not $checksumText -or -not $checksumText.Trim()) { throw "Malformed checksum file for $binary (empty)" }
    $checksumLines = @($checksumText.Trim() -split "`r?`n" | Where-Object { $_.Trim() -ne '' })
    if ($checksumLines.Count -ne 1) { throw "Malformed checksum file for $binary (expected one line)" }
    $expected = $checksumText.Trim().Split(' ', [System.StringSplitOptions]::RemoveEmptyEntries)[0].ToLower()
    if ($expected -notmatch '^[0-9a-f]{64}$') { throw "Malformed checksum file for $binary (expected 64 hex characters)" }
    $actual = (Get-FileHash $temporaryBinary -Algorithm SHA256).Hash.ToLower()
    if ($expected -ne $actual) { throw 'Checksum verification failed; nothing was installed' }
    if (Test-Path ./ztd.exe) {
        Write-Warning 'Replacing existing .\ztd.exe (ZTD_OVERWRITE=1); it was not checked as a previous ztd install'
    }
    Copy-Item $temporaryBinary ./ztd.exe -Force
    Write-Host 'Installed .\ztd.exe'
} finally {
    Remove-Item $temporaryBinary, $temporaryChecksum -Force -ErrorAction SilentlyContinue
}
