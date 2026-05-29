#!/bin/sh
# zvk installer (POSIX): ensure a Go toolchain, then build & install zvk from
# source with `go install`. The compiled binary carries no Mark-of-the-Web and
# is hash-verified through GOSUMDB.
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

# 1. Ensure a `go` toolchain. Prefer a system go; otherwise fetch the official
#    (signed, high-reputation) toolchain into a private bootstrap dir.
if command -v go >/dev/null 2>&1; then
    echo "[zvk] using existing $(go version)"
    GO=go
else
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
    gover=$(fetch "https://go.dev/VERSION?m=text" | head -n1)
    echo "[zvk] no system go found; installing $gover toolchain"
    bootstrap="$ROOT/.bootstrap-go"
    rm -rf "$bootstrap"
    mkdir -p "$bootstrap"
    fetch "https://go.dev/dl/${gover}.${goos}-${goarch}.tar.gz" | tar -xzf - -C "$bootstrap"
    GO="$bootstrap/go/bin/go"
fi

# 2. Build & install zvk straight into <root>/bin via GOBIN.
mkdir -p "$ROOT/bin"
echo "[zvk] go install ${MODULE}@${VERSION}"
GOBIN="$ROOT/bin" "$GO" install "${MODULE}@${VERSION}"

# 3. Wire up PATH and pull a Zig toolchain.
"$ROOT/bin/zvk" self-install
"$ROOT/bin/zvk" zig install
