# zvk installer (Windows): download a Go toolchain that zvk manages itself, build
# & install zvk from source with it via `go install`, then wire up PATH and pull
# a Zig toolchain. The compiled binary carries no Mark-of-the-Web, so it avoids
# the usual SmartScreen prompt that hits downloaded executables. Re-running this
# script repairs and upgrades an existing install (the canonical update path).
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

$arch = switch ($env:PROCESSOR_ARCHITECTURE) {
    'ARM64' { 'arm64' }
    'AMD64' { 'amd64' }
    default { 'amd64' }
}

# zvk always manages its own Go, regardless of any system install. Download the
# latest Go straight into the managed versions dir: this same copy both builds
# zvk and becomes zvk's default `go`. A non-zvk `go` already on PATH (if any) is
# reported afterwards by `zvk go install` — zvk never touches the system copy.
$gover = ((Invoke-RestMethod 'https://go.dev/VERSION?m=text') -split "`n")[0].Trim()
$godir = Join-Path $Root "go\versions\$gover"

# A managed Go is only trusted if the toolchain is actually complete. If a prior
# run's download/extract was interrupted it can leave bin\go.exe present (it
# precedes src\ in the archive) but src\ incomplete, which then fails every build
# with "package ... is not in std". So validate src\ too, not just that go.exe
# exists.
function Test-GoOk($dir) {
    $exe = Join-Path $dir 'bin\go.exe'
    if (-not (Test-Path $exe)) { return $false }
    if (-not (Test-Path (Join-Path $dir 'src\runtime'))) { return $false }
    try { & $exe version *> $null } catch { return $false }
    return $LASTEXITCODE -eq 0
}

if (Test-GoOk $godir) {
    Write-Host "[zvk] managed Go $gover already present"
} else {
    Write-Host "[zvk] installing managed Go $gover into $godir"
    # Extract into a sibling dir and rename into place only once complete, so a
    # truncated download/extract never leaves a half-written $godir that the
    # check above would later mistake for a good install (the final Move-Item is
    # an atomic rename — $extract and $godir share the same volume under $Root).
    $extract = "$godir.extract"
    if (Test-Path $godir)   { Remove-Item -Recurse -Force $godir }
    if (Test-Path $extract) { Remove-Item -Recurse -Force $extract }
    $zip = Join-Path $env:TEMP "$gover.windows-$arch.zip"
    Invoke-WebRequest "https://go.dev/dl/$gover.windows-$arch.zip" -OutFile $zip
    # Archives wrap everything in a top-level go\ — extract, validate, then lift
    # that go\ dir into place.
    Expand-Archive -Path $zip -DestinationPath $extract -Force
    Remove-Item $zip
    $staged = Join-Path $extract 'go'
    if (-not (Test-GoOk $staged)) {
        Remove-Item -Recurse -Force $extract
        throw "[zvk] managed Go download looks incomplete (truncated stream?); aborting"
    }
    Move-Item $staged $godir
    Remove-Item -Recurse -Force $extract
}
$goexe = Join-Path $godir 'bin\go.exe'

# Build & install zvk into <root>\bin via GOBIN. GOTOOLCHAIN=local stops Go from
# re-downloading a toolchain just to satisfy go.mod — the managed one suffices.
$bin = Join-Path $Root 'bin'
New-Item -ItemType Directory -Force -Path $bin | Out-Null

# Drop only this module's download cache before installing. Without this, a stale
# cached @latest resolution (or an already-downloaded version) can make
# `go install ...@latest` a silent no-op right after a new tag is pushed: Go
# reuses the cache instead of re-querying the proxy, so the rebuild lands on the
# old version while still reporting success. Scoped to this module's @v subtree
# so other modules' caches stay intact. Cache files are read-only.
$modcache  = (& $goexe env GOMODCACHE).Trim()
$modverDir = Join-Path $modcache "cache\download\$Module\@v"
if ($modcache -and (Test-Path $modverDir)) {
    Write-Host "[zvk] refreshing module cache for $Module"
    Get-ChildItem -Recurse -Force $modverDir | ForEach-Object { $_.Attributes = 'Normal' }
    Remove-Item -Recurse -Force $modverDir -ErrorAction SilentlyContinue
}

Write-Host "[zvk] go install $Module@$Version"
$env:GOTOOLCHAIN = 'local'
$env:GOBIN = $bin
& $goexe install "$Module@$Version"

# Wire up PATH, promote the just-extracted Go to the `stable` channel, then pull
# Zig. `go use` is a purely local re-link (no index fetch, no download): it pins
# stable at exactly the version that built zvk, rather than re-resolving
# "latest" — which could have moved on and pulled a different Go.
$zvk = Join-Path $bin 'zvk.exe'
& $zvk self-install
& $zvk go use $gover
& $zvk zig install
