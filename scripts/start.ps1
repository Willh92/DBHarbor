$ErrorActionPreference = "Stop"
$RootDir = Split-Path -Parent $PSScriptRoot
Set-Location $RootDir

$Arch = if ($env:PROCESSOR_ARCHITECTURE -eq "ARM64") { "arm64" } else { "amd64" }
$BinPath = Join-Path $RootDir "dist\dbharbor-windows-$Arch.exe"

if (-not (Test-Path $BinPath)) {
    Write-Host "[start] Binary not found at $BinPath, building now..."
    node scripts/build.mjs
}

& $BinPath @args
