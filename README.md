# zvk

Cross-platform multi-toolchain version manager and SSH key manager, written in
Go. Single binary, runs on macOS, Linux, and Windows. Everything lives under
`~/.zvk/` (override with `ZVK_ROOT`).

```
zvk zig  install           # install the latest stable zig
zvk zig  install nightly   # install the latest nightly zig
zvk go   install           # install the latest stable go
zvk node install           # install the latest LTS node
zvk ssh  keygen            # generate an ed25519 keypair
```

## Install

zvk installs itself **from source**: the installer always downloads the latest
Go into the directory zvk manages (`~/.zvk/go/versions/<ver>/`) — never reusing
a system `go` — then builds zvk with it via `go install
github.com/zoptia/zvk@latest`. That same Go becomes zvk's default `go`. The
resulting binary is compiled locally — it carries no Mark-of-the-Web, so it
avoids the SmartScreen/Gatekeeper prompts that hit downloaded executables, and
its sources are hash-verified through `sum.golang.org` (GOSUMDB).

If a non-zvk `go` (or `node`) is already on your PATH, zvk leaves it untouched
and prints an advisory with the command to remove it — zvk's own `bin/` is
prepended to PATH, so the managed copy wins in new shells regardless.

### macOS / Linux

```sh
curl -fsSL https://raw.githubusercontent.com/zoptia/zvk/main/install.sh | sh
```

### Windows (PowerShell)

```powershell
irm https://raw.githubusercontent.com/zoptia/zvk/main/install.ps1 | iex
```

The installer puts the `zvk` binary into `~/.zvk/bin/`, adds that directory to
PATH, promotes the freshly downloaded Go to the `stable` channel, and runs `zvk
zig install` to pull the latest stable Zig.

> Behind a firewall or in mainland China, set a module proxy first:
> `export GOPROXY=https://goproxy.cn,direct` (PowerShell: `$env:GOPROXY=...`).

## Update

Updating zvk is just **re-running the installer**, and `self-update` is a
convenience that does exactly that — it downloads the canonical
`install.sh`/`install.ps1` and runs it. Keeping the real logic in one place
means there is a single update path that cannot drift from the install path.

```sh
zvk self-update                 # re-run the installer for the latest release
zvk self-update --version=v0.2.0
zvk self-update --dry-run       # show what it would do

# equivalently, just re-run the installer:
curl -fsSL https://raw.githubusercontent.com/zoptia/zvk/main/install.sh | sh
```

## Commands

```
zvk zig install [release|nightly]
zvk zig update  [release|nightly|all]
zvk zig use <channel> <version>
zvk zig uninstall <version>
zvk zig list
zvk zig which [channel]
zvk zig status [--json]

zvk go install [<version>|latest]
zvk go update
zvk go use <version>
zvk go uninstall <version>
zvk go list [--remote]
zvk go which
zvk go status [--json]

zvk node install [lts|current|<version>]
zvk node update
zvk node use <version>
zvk node uninstall <version>
zvk node list [--remote]
zvk node which
zvk node status [--json]

zvk ssh keygen [--name N] [--comment C] [--force]
zvk ssh list
zvk ssh show <name> [--private]
zvk ssh remove <name>
zvk ssh add <name>            # invoke ssh-add to load into ssh-agent
zvk ssh copy <name>           # copy the public key to the system clipboard
zvk ssh path [<name>]

zvk status [--json]
zvk self-install
zvk self-update [--dry-run] [--version=<v>]
zvk version
zvk help
```

## On-disk layout

```
~/.zvk/
├── bin/
│   ├── zvk                              # the manager itself
│   ├── zig          → ../zig/channels/release/zig
│   ├── zig-nightly  → ../zig/channels/nightly/zig
│   ├── go           → ../go/channels/stable/bin/go
│   ├── gofmt        → ../go/channels/stable/bin/gofmt
│   ├── node         → ../node/channels/default/bin/node
│   ├── npm          → ../node/channels/default/bin/npm
│   └── npx          → ../node/channels/default/bin/npx
├── zig/
│   ├── channels/{release,nightly}          # → ../versions/<ver>
│   └── versions/<ver>/
├── go/
│   ├── channels/stable                     # → ../versions/<ver>
│   └── versions/<ver>/                     # top level is the unpacked go/ contents
├── node/
│   ├── channels/default                    # → ../versions/<ver>
│   └── versions/<ver>/
└── ssh/
    └── keys/
        ├── id_ed25519_xxxxxxxx
        └── id_ed25519_xxxxxxxx.pub
```

## How it works

### Zig

1. Fetch <https://ziglang.org/download/index.json> for the latest version,
   tarball URL, and sha256.
2. Skip if already installed.
3. Otherwise download → verify sha256 → fetch `.minisig` and verify with the
   official Zig Ed25519 public key (using `crypto/ed25519` +
   `golang.org/x/crypto/blake2b`) → extract in pure Go
   (`github.com/ulikunitz/xz` + stdlib `archive/tar`) into
   `~/.zvk/zig/versions/<ver>/`.
4. Maintain the channel symlink and the bin symlink.
5. Generate **assistant reference docs** for that build (see below).

#### Assistant reference docs (Zig only)

Zig has no stability promise — stdlib and the build system are reorganized
across releases — and an AI assistant's training data lags the current compiler,
so it tends to write outdated Zig. After installing Zig, zvk deterministically
lays out the raw material an assistant needs to target *this* build instead of
stale memory (no LLM is involved — zvk only stages inputs):

- `zig/REFERENCE.<channel>.md` — version-pinned pointers (local `langref.html`,
  stdlib root) + a grep "topic map" (I/O, fs, http, Build, crypto…).
- `zig/versions/<ver>/STD_INDEX.md` — a lightweight "which file exports what"
  index (column-0 `pub` decls of each top-level `lib/std` file).
- `zig/versions/<ver>/release-notes.html` — the official breaking-change list,
  snapshotted locally.
- `zig/versions/<ver>/ADAPTATION.prompt.md` — a prompt you hand to Claude; it
  reads the notes + index and writes an `ADAPTATION.md` cheat sheet of "what my
  memory gets wrong about this version".
- `zig/CLAUDE.md` — a decision table (`zig` vs `zig-nightly` by a project's
  `minimum_zig_version`), idempotently `@import`ed into `~/.claude/CLAUDE.md` so
  Claude Code loads it automatically.

Disable with `ZVK_NO_DOCS` (skip entirely) or `ZVK_NO_CLAUDE_MD` (keep the docs
but don't touch any `CLAUDE.md`).

### Go

1. Fetch <https://go.dev/dl/?mode=json> for the version list and sha256.
2. Skip if already installed.
3. Otherwise download the `.tar.gz` → verify sha256 → extract in pure Go
   (stdlib `compress/gzip` + `archive/tar`, stripping the top-level `go/`
   directory).
4. Maintain `go/channels/stable` and the `bin/{go,gofmt}` symlinks.

### Node.js

1. Fetch <https://nodejs.org/dist/index.json> and resolve the version: `lts`
   picks the newest LTS release, `current` the newest overall, or an exact
   version like `v20.11.0`.
2. Skip if already installed.
3. Otherwise fetch that release's `SHASUMS256.txt`, look up the sha256 for this
   platform's archive, download → verify sha256 → extract in pure Go
   (`.tar.gz` on POSIX, `.zip` on Windows).
4. Maintain `node/channels/default` and the `bin/{node,npm,npx}` symlinks.
   Node has a single active pointer (the commands `node`/`npm`/`npx` can't be
   suffixed per channel the way `zig`/`zig-nightly` are), so `lts`/`current`
   are install-time selectors, not persistent channels.

### SSH

1. `keygen` calls `crypto/ed25519.GenerateKey` to produce a keypair.
2. `golang.org/x/crypto/ssh`'s `MarshalAuthorizedKey` and `MarshalPrivateKey`
   emit the standard OpenSSH public key line and `openssh-key-v1` private key
   (unencrypted).
3. The private key is written with 0600 permissions.
4. `add` invokes `ssh-add`; `copy` invokes `pbcopy` on macOS, `wl-copy` or
   `xclip` on Linux.

## Environment variables

| Variable                | Effect                                                  |
|-------------------------|---------------------------------------------------------|
| `ZVK_ROOT`           | Install root (defaults to `~/.zvk`)                  |
| `ZVK_NO_MODIFY_PATH` | Skip writing to shell rc                                |
| `ZVK_NO_MINISIGN`    | Skip minisign verification of Zig tarballs (not advised) |
| `ZVK_NO_DOCS`        | Skip generating the Zig assistant reference docs        |
| `ZVK_NO_CLAUDE_MD`   | Generate the docs but don't write/inject `CLAUDE.md`    |
| `ZVK_VERSION`        | Module version the installer builds (`self-update` forwards it) |
| `GOPROXY`            | Go module proxy used by the installer                   |

## Build from source

Requires Go 1.25 or newer.

```sh
go install github.com/zoptia/zvk@latest    # straight to $GOBIN
# or, from a clone:
git clone https://github.com/zoptia/zvk
cd zvk
go build -o zvk .
./zvk self-install
zvk zig install
zvk go install
```

## Third-party dependencies

Two, both de-facto standards:

- `golang.org/x/crypto` (the Go team's official extension module)
  - `crypto/ssh` — OpenSSH public/private key serialization (no stdlib equivalent)
  - `crypto/blake2b` — minisign `ED` prehashed signatures (stdlib has no blake2)
- `github.com/ulikunitz/xz` — pure-Go xz decompression (stdlib has no xz; Zig
  release tarballs are `.tar.xz`)

tar/gz/zip are handled entirely by the standard library (`archive/tar`,
`compress/gzip`, `archive/zip`); there is no dependency on the system `tar`
binary, so zvk runs in minimal environments where `tar` is absent.

## License

[Apache-2.0](LICENSE)
