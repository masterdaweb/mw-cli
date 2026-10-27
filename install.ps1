# Installs `mw`, the Master da Web CLI, on Windows:
#
#   irm https://raw.githubusercontent.com/masterdaweb/mw-cli/main/install.ps1 | iex
#
# Downloads the release for this computer, checks it against the release's
# checksums.txt, puts mw.exe in %LOCALAPPDATA%\Programs\mw and adds that folder
# to the user's PATH. $env:MW_VERSION = "1.2.3" picks a version;
# $env:MW_INSTALL_DIR a folder.
$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue"

$Repo = "masterdaweb/mw-cli"

function Fail([string]$msg) {
    Write-Error "mw install: $msg"
    exit 1
}

$arch = switch ($env:PROCESSOR_ARCHITECTURE) {
    "AMD64" { "amd64" }
    "ARM64" { "arm64" }
    default { Fail "no build for $($env:PROCESSOR_ARCHITECTURE)" }
}

$version = $env:MW_VERSION
if (-not $version) {
    try {
        $latest = Invoke-WebRequest -Uri "https://github.com/$Repo/releases/latest" -MaximumRedirection 0 -ErrorAction SilentlyContinue -UseBasicParsing
    } catch {
        $latest = $_.Exception.Response
    }
    $location = $null
    if ($latest -and $latest.Headers) { $location = $latest.Headers["Location"] }
    if (-not $location) {
        # PowerShell 7 follows the redirect itself: ask the API instead.
        $location = (Invoke-RestMethod -Uri "https://api.github.com/repos/$Repo/releases/latest" -UseBasicParsing).html_url
    }
    if (-not $location) { Fail "could not find the latest release" }
    $version = ($location -split "/tag/v")[-1]
}
$version = $version.TrimStart("v")

$archive = "mw_${version}_windows_${arch}.zip"
$base = "https://github.com/$Repo/releases/download/v$version"
$tmp = Join-Path ([System.IO.Path]::GetTempPath()) ("mw-" + [System.Guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Path $tmp | Out-Null

try {
    Write-Host "Downloading mw $version for windows/$arch..."
    try {
        Invoke-WebRequest -Uri "$base/$archive" -OutFile (Join-Path $tmp $archive) -UseBasicParsing
    } catch { Fail "no release $version for windows/$arch" }
    try {
        Invoke-WebRequest -Uri "$base/checksums.txt" -OutFile (Join-Path $tmp "checksums.txt") -UseBasicParsing
    } catch { Fail "the release has no checksums" }

    $line = Get-Content (Join-Path $tmp "checksums.txt") | Where-Object { $_ -match " $([regex]::Escape($archive))$" } | Select-Object -First 1
    if (-not $line) { Fail "$archive is not in checksums.txt" }
    $want = ($line -split "\s+")[0].ToLower()
    $got = (Get-FileHash -Algorithm SHA256 -Path (Join-Path $tmp $archive)).Hash.ToLower()
    if ($got -ne $want) { Fail "the download does not match its checksum (got $got, want $want)" }

    Expand-Archive -Path (Join-Path $tmp $archive) -DestinationPath $tmp -Force

    $dir = $env:MW_INSTALL_DIR
    if (-not $dir) { $dir = Join-Path $env:LOCALAPPDATA "Programs\mw" }
    New-Item -ItemType Directory -Path $dir -Force | Out-Null
    Copy-Item -Path (Join-Path $tmp "mw.exe") -Destination (Join-Path $dir "mw.exe") -Force

    $userPath = [Environment]::GetEnvironmentVariable("Path", "User")
    if (-not $userPath) { $userPath = "" }
    if (($userPath -split ";") -notcontains $dir) {
        [Environment]::SetEnvironmentVariable("Path", ($userPath.TrimEnd(";") + ";" + $dir).TrimStart(";"), "User")
        $env:Path = "$env:Path;$dir"
        Write-Host "Added $dir to your PATH (new terminals see it)."
    }
    $installed = & (Join-Path $dir "mw.exe") --version
    Write-Host "Installed $installed in $dir."
    Write-Host "Next: mw login"
} finally {
    Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
}
