#!/bin/sh
# zvk installer (POSIX): download a Go toolchain that zvk manages itself, build &
# install zvk from source with it via `go install`, then wire up PATH and pull a
# Zig toolchain. The compiled binary carries no Mark-of-the-Web and module
# sources are hash-verified through GOSUMDB. Re-running this script repairs and
# upgrades an existing install (it is the canonical update path).
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/zoptia/zvk/main/install.sh | sh
#
# Env:
#   ZVK_VERSION   module version to install (default: latest)
#   ZVK_ROOT      install root (default: ~/.zvk)
#   GOPROXY       Go module proxy (e.g. CN users: https://goproxy.cn,direct)

set -eu

MODULE="github.com/zoptia/zvk"
VERSION="${ZVK_VERSION:-latest}"
ROOT="${ZVK_ROOT:-$HOME/.zvk}"
export GOPROXY="${GOPROXY:-https://proxy.golang.org,direct}"

fetch() {
    if command -v curl >/dev/null 2>&1; then
        curl -fsSL "$1"
    elif command -v wget >/dev/null 2>&1; then
        wget -qO- "$1"
    else
        echo "[zvk] need curl or wget" >&2
        exit 1
    fi
}

case "$(uname -s)" in
    Linux)  goos=linux ;;
    Darwin) goos=darwin ;;
    *) echo "[zvk] unsupported OS: $(uname -s)" >&2; exit 1 ;;
esac
case "$(uname -m)" in
    x86_64|amd64)  goarch=amd64 ;;
    aarch64|arm64) goarch=arm64 ;;
    *) echo "[zvk] unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac

# zvk always manages its own Go, regardless of any system install. Download the
# latest Go straight into the managed versions dir: this same copy both builds
# zvk and becomes zvk's default `go`. A non-zvk `go` already on PATH (if any) is
# reported afterwards by `zvk go install` — zvk never touches the system copy.
gover=$(fetch "https://go.dev/VERSION?m=text" | head -n1)
godir="$ROOT/go/versions/$gover"

# A managed Go is only trusted if the toolchain is actually complete. The
# bootstrap download below is a streamed, unverified curl|tar — if a prior run's
# stream was truncated it leaves bin/go present (it precedes src/ in the archive)
# but src/ incomplete, which then fails every build with "package ... is not in
# std". So validate src/ too, not just that bin/go is executable.
go_ok() {
    [ -x "$1/bin/go" ] && [ -d "$1/src/runtime" ] && "$1/bin/go" version >/dev/null 2>&1
}

if go_ok "$godir"; then
    echo "[zvk] managed Go $gover already present"
else
    echo "[zvk] installing managed Go $gover into $godir"
    # Extract into a sibling tmp dir and rename into place only once complete, so
    # a truncated download never leaves a half-written $godir that the check
    # above would later mistake for a good install (rename is atomic on the same
    # filesystem; both paths live under $ROOT/go/versions).
    rm -rf "$godir" "$godir.tmp"
    mkdir -p "$godir.tmp"
    # Go archives wrap everything in a top-level go/ — strip it.
    fetch "https://go.dev/dl/${gover}.${goos}-${goarch}.tar.gz" | tar -xzf - -C "$godir.tmp" --strip-components=1
    if ! go_ok "$godir.tmp"; then
        echo "[zvk] managed Go download looks incomplete (truncated stream?); aborting" >&2
        rm -rf "$godir.tmp"
        exit 1
    fi
    mv "$godir.tmp" "$godir"
fi
GO="$godir/bin/go"

# Build & install zvk into <root>/bin via GOBIN. GOTOOLCHAIN=local stops Go from
# re-downloading a toolchain just to satisfy go.mod — the managed one suffices.
mkdir -p "$ROOT/bin"

# Drop only this module's download cache before installing. Without this, a stale
# cached @latest resolution (or an already-downloaded version) can make
# `go install ...@latest` a silent no-op right after a new tag is pushed: Go
# reuses the cache instead of re-querying the proxy, so the rebuild lands on the
# old version while still reporting success. We scope the wipe to MODULE's @v
# subtree so other modules' caches (incl. the managed Go's build deps) are
# untouched. Cache files are read-only, hence chmod before rm.
modcache=$("$GO" env GOMODCACHE)
if [ -n "$modcache" ] && [ -d "$modcache/cache/download/$MODULE/@v" ]; then
    echo "[zvk] refreshing module cache for $MODULE"
    chmod -R u+w "$modcache/cache/download/$MODULE/@v" 2>/dev/null || true
    rm -rf "$modcache/cache/download/$MODULE/@v"
fi

echo "[zvk] go install ${MODULE}@${VERSION}"
GOTOOLCHAIN=local GOBIN="$ROOT/bin" "$GO" install "${MODULE}@${VERSION}"

# Wire up PATH, promote the just-extracted Go to the `stable` channel, then pull
# Zig. `go use` is a purely local re-link (no index fetch, no download): it
# pins stable at exactly the version that built zvk, rather than re-resolving
# "latest" — which could have moved on and pulled a different Go.
"$ROOT/bin/zvk" self-install
"$ROOT/bin/zvk" go use "$gover"
"$ROOT/bin/zvk" zig install
