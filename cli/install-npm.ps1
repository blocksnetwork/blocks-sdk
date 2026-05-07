# Blocks CLI installer for Windows (PowerShell)
#
# Usage:
#   iex (irm https://config.blocks.ai/install.ps1)

$ErrorActionPreference = "Stop"

# Check for npm
if (-not (Get-Command npm -ErrorAction SilentlyContinue)) {
    Write-Error "npm is not installed. Please install Node.js from https://nodejs.org and try again."
    exit 1
}

Write-Host "Installing Blocks CLI..."
npm i -g @blocks-network/cli

if ($LASTEXITCODE -ne 0) {
    Write-Error "Failed to install @blocks-network/cli"
    exit 1
}

# Ensure ~/.blocks/bin is in the user PATH
$blocksDir = Join-Path $HOME ".blocks\bin"
if (Test-Path $blocksDir) {
    $currentPath = [Environment]::GetEnvironmentVariable("PATH", "User")
    if ($currentPath -notlike "*$blocksDir*") {
        [Environment]::SetEnvironmentVariable("PATH", "$blocksDir;$currentPath", "User")
        $env:PATH = "$blocksDir;$env:PATH"
    }
}

Write-Host ""
Write-Host "Done! Run 'blocks --help' to get started."
