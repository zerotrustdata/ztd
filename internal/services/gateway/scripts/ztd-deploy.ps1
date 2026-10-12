# ztd Auto-Detect Deploy Script for Windows
# Detects architecture automatically and deploys the appropriate ztd binary.
# This script is embedded in the ztd gateway binary and served at:
#   http://<gateway-ip>:8080/ztd-deploy.ps1
# Run on remote hosts to download and deploy the ztd binary.

$ErrorActionPreference = "Stop"

$GatewayHost = if ($env:GATEWAY_HOST) { $env:GATEWAY_HOST } else { "{{.GatewayHost}}" }
$GatewayPort = if ($env:GATEWAY_PORT) { $env:GATEWAY_PORT } else { "{{.GatewayPort}}" }

Write-Host "Deploying ztd..." -ForegroundColor Green

# Clean up existing certificates
Write-Host "Cleaning up existing certificates..." -ForegroundColor Yellow
$PkiDir = Join-Path $env:USERPROFILE ".ztd\pki"
if (Test-Path $PkiDir) {
    Remove-Item -Recurse -Force $PkiDir -ErrorAction SilentlyContinue
}

# Detect architecture
$arch = $env:PROCESSOR_ARCHITECTURE
switch ($arch) {
    "AMD64" { $arch = "amd64" }
    "ARM64" { $arch = "arm64" }
    "x86"   { $arch = "386" }
    default {
        Write-Host "Unsupported architecture: $arch" -ForegroundColor Red
        Write-Host "Supported architectures: amd64, arm64, 386"
        exit 1
    }
}

$BinaryName = "ztd-windows-$arch.exe"
$BinaryUrl = "http://${GatewayHost}:${GatewayPort}/.well-known/ztd/bin/${BinaryName}"
$ChecksumUrl = "${BinaryUrl}.sha256"

Write-Host "Detected: Windows $arch" -ForegroundColor Yellow
Write-Host "Downloading from: $BinaryUrl" -ForegroundColor Yellow

$TempBinary = "ztd.download"
$TempChecksum = "ztd.download.sha256"

# Download the binary and checksum to temporary files before replacing the
# existing executable.
try {
    Invoke-RestMethod -Uri $BinaryUrl -OutFile $TempBinary
    Invoke-RestMethod -Uri $ChecksumUrl -OutFile $TempChecksum
    $ExpectedHash = (Get-Content -Raw $TempChecksum).Trim().Split(" ")[0].ToLowerInvariant()
    $ActualHash = (Get-FileHash -Algorithm SHA256 -Path $TempBinary).Hash.ToLowerInvariant()
    if ($ExpectedHash -ne $ActualHash) {
        throw "checksum verification failed"
    }
    Move-Item -Force $TempBinary "ztd.exe"
} catch {
    Remove-Item -Force $TempBinary, $TempChecksum -ErrorAction SilentlyContinue
    Write-Host "Failed to download or verify ztd: $_" -ForegroundColor Red
    exit 1
}
Remove-Item -Force $TempChecksum -ErrorAction SilentlyContinue

Write-Host "ztd deployed successfully!" -ForegroundColor Green

Write-Host "Starting ztd Operator to connect to Gateway at ${GatewayHost}..." -ForegroundColor Yellow
Write-Host "DEBUG: GatewayHost = ${GatewayHost}" -ForegroundColor Yellow
& .\ztd.exe operator start -e $GatewayHost
