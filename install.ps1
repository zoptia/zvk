# zvk installer (Windows): ensure a Go toolchain, then build & install zvk from
# source with `go install`. The compiled binary carries no Mark-of-the-Web, so
# it avoids the usual SmartScreen prompt that hits downloaded executables.
#
# Usage:
#   irm https://raw.githubusercontent.com/zoptia/zvk/main/install.ps1 | iex
#
# Env:
#   ZVK_VERSION   module version to install (default: latest)
#   ZVK_ROOT      install root (default: %USERPROFILE%\.zvk)
#   GOPROXY       Go module proxy

$ErrorActionPreference = 'Stop'

$Module  = 'github.com/zoptia/zvk'
$Version = if ($env:ZVK_VERSION) { $env:ZVK_VERSION } else { 'latest' }
$Root    = if ($env:ZVK_ROOT)    { $env:ZVK_ROOT }    else { Join-Path $HOME '.zvk' }
if (-not $env:GOPROXY) { $env:GOPROXY = 'https://proxy.golang.org,direct' }

# 1. Ensure a `go` toolchain.
$sysGo = Get-Command go -ErrorAction SilentlyContinue
if ($sysGo) {
    Write-Host "[zvk] using existing $(& go version)"
    $GoExe = 'go'
} else {
    $gover = ((Invoke-RestMethod 'https://go.dev/VERSION?m=text') -split "`n")[0].Trim()
    Write-Host "[zvk] no system go found; installing $gover toolchain"
    $arch = switch ($env:PROCESSOR_ARCHITECTURE) {
        'ARM64' { 'arm64' }
        'AMD64' { 'amd64' }
        default { 'amd64' }
    }
    $bootstrap = Join-Path $Root '.bootstrap-go'
    if (Test-Path $bootstrap) { Remove-Item -Recurse -Force $bootstrap }
    New-Item -ItemType Directory -Force -Path $bootstrap | Out-Null
    $zip = Join-Path $env:TEMP "$gover.windows-$arch.zip"
    Invoke-WebRequest "https://go.dev/dl/$gover.windows-$arch.zip" -OutFile $zip
    Expand-Archive -Path $zip -DestinationPath $bootstrap -Force
    Remove-Item $zip
    $GoExe = Join-Path $bootstrap 'go\bin\go.exe'
}

# 2. Build & install zvk straight into <root>\bin via GOBIN.
$bin = Join-Path $Root 'bin'
New-Item -ItemType Directory -Force -Path $bin | Out-Null
Write-Host "[zvk] go install $Module@$Version"
$env:GOBIN = $bin
& $GoExe install "$Module@$Version"

# 3. Wire up PATH and pull a Zig toolchain.
$zvk = Join-Path $bin 'zvk.exe'
& $zvk self-install
& $zvk zig install
