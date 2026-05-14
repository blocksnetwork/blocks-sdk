# Blocks CLI installer for Windows (PowerShell)
#
# Usage:
#   irm https://config.blocks.ai/install.ps1 | iex
#
# Or with a GitHub token (for pre-release builds):
#   $env:GITHUB_TOKEN = "ghp_..."
#   irm https://config.blocks.ai/install.ps1 | iex
#
# Environment variables:
#   GITHUB_TOKEN / GH_TOKEN — GitHub personal access token (for pre-release or draft builds)
#   BLOCKS_INSTALL_DIR      — Override install directory (default: ~\.blocks\bin)
#   BLOCKS_RELEASES_URL     — Override download base URL (flat directory with latest.json + archives)
#   BLOCKS_VERSION          — Pin to a specific version (default: latest)

$ErrorActionPreference = "Stop"
# Default to GitHub API download. Set BLOCKS_RELEASES_URL to use a flat-
# directory host (S3, etc.) instead.

$GitHubRepo = "blocksnetwork/blocks-sdk"
$GitHubApi = "https://api.github.com"
$InstallDir = if ($env:BLOCKS_INSTALL_DIR) { $env:BLOCKS_INSTALL_DIR } else { Join-Path $HOME ".blocks\bin" }
$AuthToken = if ($env:GITHUB_TOKEN) { $env:GITHUB_TOKEN } elseif ($env:GH_TOKEN) { $env:GH_TOKEN } else {
    try { $t = (gh auth token 2>$null); if ($t) { $t.Trim() } else { $null } } catch { $null }
}

# When no GitHub token and no explicit releases URL, default to the public
# releases endpoint so `irm https://config.blocks.ai/install.ps1 | iex` works
# without any authentication.
if (-not $env:BLOCKS_RELEASES_URL -and -not $AuthToken) {
    $env:BLOCKS_RELEASES_URL = "https://config.blocks.ai/releases/cli"
}

$AuthHeaders = if ($AuthToken) { @{ Authorization = "token $AuthToken" } } else { @{} }
$ApiHeaders = if ($AuthToken) { @{ Authorization = "token $AuthToken"; Accept = "application/vnd.github+json" } } else { @{ Accept = "application/vnd.github+json" } }
$AssetHeaders = if ($AuthToken) { @{ Authorization = "token $AuthToken"; Accept = "application/octet-stream" } } else { @{ Accept = "application/octet-stream" } }

# ── Detect architecture ─────────────────────────────────────────────
function Get-PlatformArch {
    $arch = [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture
    switch ($arch) {
        "X64" { return "amd64" }
        default {
            Write-Error "Unsupported architecture: $arch. Only x64 is supported."
            exit 1
        }
    }
}

# ── Get installed version ────────────────────────────────────────────
function Get-InstalledVersion {
    $binaryPath = Join-Path $InstallDir "blocks.exe"
    if (Test-Path $binaryPath) {
        try {
            $output = & $binaryPath version 2>$null
            return $output
        } catch {
            return $null
        }
    }
    return $null
}

# ── Find asset API URL from release JSON ─────────────────────────────
function Find-AssetApiUrl {
    param([object]$Release, [string]$AssetName)
    $asset = $Release.assets | Where-Object { $_.name -eq $AssetName } | Select-Object -First 1
    if ($asset) { return $asset.url }
    return $null
}

# ── Main ─────────────────────────────────────────────────────────────
function Install-BlocksCLI {
    $goarch = Get-PlatformArch

    Write-Host "Blocks CLI installer"
    Write-Host "  Platform: windows/$goarch"

    $installedVersion = Get-InstalledVersion

    $tmpDir = Join-Path $env:TEMP "blocks-install-$(Get-Random)"
    New-Item -ItemType Directory -Path $tmpDir -Force | Out-Null

    try {
        # ── Flat-directory download (S3 or custom URL) ──────────────────
        if ($env:BLOCKS_RELEASES_URL) {
            $downloadBase = $env:BLOCKS_RELEASES_URL

            if ($env:BLOCKS_VERSION) {
                $version = $env:BLOCKS_VERSION
            } else {
                Write-Host "  Fetching version manifest..."
                $manifest = Invoke-RestMethod -Uri "$downloadBase/latest.json" -Headers $AuthHeaders
                $version = $manifest.version
            }

            if (-not $version) {
                Write-Error "Could not determine version from manifest"
                exit 1
            }

            if ($installedVersion) {
                if ($installedVersion -eq $version) {
                    Write-Host "  Installed: v$installedVersion"
                    Write-Host ""
                    Write-Host "Blocks CLI v$version is already up to date."
                    return
                } else {
                    Write-Host "  Installed: v$installedVersion"
                    Write-Host "  Available: v$version"
                    Write-Host "  Upgrading..."
                }
            } else {
                Write-Host "  Version: v$version"
            }

            # GoReleaser archive naming
            $archiveName = "blocks_${version}_windows_${goarch}.zip"
            $archivePath = Join-Path $tmpDir $archiveName
            $checksumsPath = Join-Path $tmpDir "checksums.txt"

            Write-Host "  Downloading checksums..."
            Invoke-WebRequest -Uri "$downloadBase/checksums.txt" -OutFile $checksumsPath -UseBasicParsing -Headers $AuthHeaders

            Write-Host "  Downloading $archiveName..."
            Invoke-WebRequest -Uri "$downloadBase/$archiveName" -OutFile $archivePath -UseBasicParsing -Headers $AuthHeaders

        # ── GitHub API download (default) ───────────────────────────────
        } else {
            if (-not $AuthToken) {
                Write-Error "GITHUB_TOKEN or GH_TOKEN is required for private repo downloads.`nSet it with: `$env:GITHUB_TOKEN = `"ghp_...`"`nOr authenticate via: gh auth login"
                exit 1
            }

            if ($env:BLOCKS_VERSION) {
                $releaseUrl = "$GitHubApi/repos/$GitHubRepo/releases/tags/cli-v$($env:BLOCKS_VERSION)"
                Write-Host "  Fetching release info from GitHub API..."
                $release = Invoke-RestMethod -Uri $releaseUrl -Headers $ApiHeaders
            } else {
                Write-Host "  Fetching latest CLI release from GitHub API..."

                # Try /releases/latest first (single API call). If the repo's
                # "latest" release is already a CLI tag we're done; otherwise
                # fall back to scanning by published_at.
                $latestRelease = $null
                try {
                    $latestRelease = Invoke-RestMethod -Uri "$GitHubApi/repos/$GitHubRepo/releases/latest" -Headers $ApiHeaders
                } catch { }

                if ($latestRelease -and $latestRelease.tag_name -like "cli-v*") {
                    $release = $latestRelease
                } else {
                    $releases = Invoke-RestMethod -Uri "$GitHubApi/repos/$GitHubRepo/releases?per_page=30" -Headers $ApiHeaders
                    $cliRelease = $releases | Where-Object { $_.tag_name -like "cli-v*" } |
                        Sort-Object -Property published_at -Descending | Select-Object -First 1

                    if (-not $cliRelease) {
                        Write-Error "No CLI release (cli-v*) found"
                        exit 1
                    }

                    $release = $cliRelease
                }

                Write-Host "  Found release: $($release.tag_name)"
            }

            # Extract version from tag_name (strip "cli-v" prefix)
            $version = $release.tag_name -replace '^cli-v', '' -replace '^v', ''

            if (-not $version) {
                Write-Error "Could not determine version from release"
                exit 1
            }

            if ($installedVersion) {
                if ($installedVersion -eq $version) {
                    Write-Host "  Installed: v$installedVersion"
                    Write-Host ""
                    Write-Host "Blocks CLI v$version is already up to date."
                    return
                } else {
                    Write-Host "  Installed: v$installedVersion"
                    Write-Host "  Available: v$version"
                    Write-Host "  Upgrading..."
                }
            } else {
                Write-Host "  Version: v$version"
            }

            # Download checksums.txt
            $checksumsPath = Join-Path $tmpDir "checksums.txt"
            $checksumsApiUrl = Find-AssetApiUrl -Release $release -AssetName "checksums.txt"
            if ($checksumsApiUrl) {
                Write-Host "  Downloading checksums..."
                Invoke-WebRequest -Uri $checksumsApiUrl -OutFile $checksumsPath -UseBasicParsing -Headers $AssetHeaders
            }

            # Download the archive
            $archiveName = "blocks_${version}_windows_${goarch}.zip"
            $archiveApiUrl = Find-AssetApiUrl -Release $release -AssetName $archiveName
            if (-not $archiveApiUrl) {
                Write-Error "Could not find $archiveName asset in the release"
                exit 1
            }

            $archivePath = Join-Path $tmpDir $archiveName
            Write-Host "  Downloading $archiveName..."
            Invoke-WebRequest -Uri $archiveApiUrl -OutFile $archivePath -UseBasicParsing -Headers $AssetHeaders
        }

        # ── Common: verify, extract, install ─────────────────────────────
        if (Test-Path $checksumsPath) {
            Write-Host "  Verifying checksum..."
            $actualHash = (Get-FileHash -Path $archivePath -Algorithm SHA256).Hash.ToLower()
            $checksumLine = Get-Content $checksumsPath | Where-Object { $_ -match $archiveName } | Select-Object -First 1
            if ($checksumLine) {
                $expectedHash = ($checksumLine -split '\s+')[0]
                if ($actualHash -ne $expectedHash) {
                    Write-Error "Checksum mismatch!`n  Expected: $expectedHash`n  Actual:   $actualHash"
                    exit 1
                }
            } else {
                Write-Host "  Warning: No checksum found for $archiveName — skipping verification"
            }
        }

        Write-Host "  Installing to $InstallDir..."
        if (-not (Test-Path $InstallDir)) {
            New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
        }

        $extractDir = Join-Path $tmpDir "extracted"
        Expand-Archive -Path $archivePath -DestinationPath $extractDir -Force

        # GoReleaser extracts to a flat directory with the binary inside
        $extractedBinary = Get-ChildItem -Path $extractDir -Filter "blocks.exe" -Recurse | Select-Object -First 1
        if ($extractedBinary) {
            Copy-Item -Path $extractedBinary.FullName -Destination (Join-Path $InstallDir "blocks.exe") -Force
        } else {
            Write-Error "Could not find blocks.exe in archive"
            exit 1
        }

        # Add to user PATH
        $currentPath = [Environment]::GetEnvironmentVariable("PATH", "User")
        if ($currentPath -notlike "*$InstallDir*") {
            $newPath = "$InstallDir;$currentPath"
            [Environment]::SetEnvironmentVariable("PATH", $newPath, "User")
            Write-Host "  Added $InstallDir to user PATH"
        }

        # Also update current session PATH
        if ($env:PATH -notlike "*$InstallDir*") {
            $env:PATH = "$InstallDir;$env:PATH"
        }

        Write-Host ""
        if ($installedVersion) {
            Write-Host "Blocks CLI upgraded from v$installedVersion to v$version!"
        } else {
            Write-Host "Blocks CLI v$version installed successfully!"
        }
        Write-Host ""
        Write-Host "  Binary: $InstallDir\blocks.exe"
        Write-Host ""
        Write-Host "  You may need to restart your terminal for PATH changes to take effect."
        Write-Host ""
        Write-Host "  Verify with:"
        Write-Host "    blocks version"
    } finally {
        Remove-Item -Path $tmpDir -Recurse -Force -ErrorAction SilentlyContinue
    }
}

Install-BlocksCLI
