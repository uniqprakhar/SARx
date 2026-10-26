# Installs sarx on Windows (PowerShell 5.1 or later).
#   irm https://raw.githubusercontent.com/uniqprakhar/sarx/main/scripts/install.ps1 | iex
# Set $env:SARX_REPO to your fork/owner, $env:SARX_VERSION to pin a release, $env:SARX_BIN_DIR to choose the target.
$ErrorActionPreference = 'Stop'
$repo    = if ($env:SARX_REPO) { $env:SARX_REPO } else { 'uniqprakhar/sarx' }
$version = if ($env:SARX_VERSION) { $env:SARX_VERSION.TrimStart('v') } else { 'latest' }
$binDir  = if ($env:SARX_BIN_DIR) { $env:SARX_BIN_DIR } else { Join-Path $env:LOCALAPPDATA 'sarx\bin' }

$arch = if ($env:PROCESSOR_ARCHITECTURE -eq 'ARM64') { 'arm64' } else { 'amd64' }
$file = "sarx_windows_$arch.zip"
$base = if ($version -eq 'latest') { "https://github.com/$repo/releases/latest/download" } else { "https://github.com/$repo/releases/download/v$version" }

$tmp = Join-Path ([IO.Path]::GetTempPath()) ("sarx-" + [guid]::NewGuid())
New-Item -ItemType Directory -Path $tmp | Out-Null
try {
    Write-Host "downloading $base/$file"
    Invoke-WebRequest "$base/$file" -OutFile (Join-Path $tmp $file) -UseBasicParsing
    Invoke-WebRequest "$base/checksums.txt" -OutFile (Join-Path $tmp 'checksums.txt') -UseBasicParsing
    $want = (Get-Content (Join-Path $tmp 'checksums.txt') | Where-Object { $_ -match [regex]::Escape($file) + '$' } | Select-Object -First 1) -split '\s+' | Select-Object -First 1
    $got = (Get-FileHash (Join-Path $tmp $file) -Algorithm SHA256).Hash.ToLower()
    if (-not $want -or $want.ToLower() -ne $got) { throw 'checksum mismatch, not installing' }
    Expand-Archive (Join-Path $tmp $file) -DestinationPath $tmp -Force
    New-Item -ItemType Directory -Path $binDir -Force | Out-Null
    Copy-Item (Join-Path $tmp 'sarx.exe') (Join-Path $binDir 'sarx.exe') -Force
    Write-Host "installed $binDir\sarx.exe"
    $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
    if (($userPath -split ';') -notcontains $binDir) {
        [Environment]::SetEnvironmentVariable('Path', "$userPath;$binDir", 'User')
        Write-Host "added $binDir to your PATH; open a new terminal to use it"
    }
    Write-Host 'next: cd your-project; sarx init'
} finally {
    Remove-Item $tmp -Recurse -Force -ErrorAction SilentlyContinue
}
