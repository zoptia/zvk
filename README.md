# zvk

Cross-platform multi-toolchain version manager and SSH key manager, written in
Go. Single binary, runs on macOS, Linux, and Windows. Everything lives under
`~/.zvk/` (override with `ZVK_ROOT`).

```
zvk zig install            # install the latest stable zig
zvk zig install nightly    # install the latest nightly zig
zvk go  install            # install the latest stable go
zvk ssh keygen             # generate an ed25519 keypair
```

## Install

zvk installs itself **from source**: the installer ensures a Go toolchain
(reusing a system `go`, or fetching the official one if absent), then runs
`go install github.com/zoptia/zvk@latest`. The resulting binary is compiled
locally — it carries no Mark-of-the-Web, so it avoids the SmartScreen/Gatekeeper
prompts that hit downloaded executables, and its sources are hash-verified
through `sum.golang.org` (GOSUMDB).

### macOS / Linux

```sh
curl -fsSL https://raw.githubusercontent.com/zoptia/zvk/main/install.sh | sh
```

### Windows (PowerShell)

```powershell
irm https://raw.githubusercontent.com/zoptia/zvk/main/install.ps1 | iex
```

The installer puts the `zvk` binary into `~/.zvk/bin/`, adds that directory to
PATH, and runs `zvk zig install` to pull the latest stable Zig.

> Behind a firewall or in mainland China, set a module proxy first:
> `export GOPROXY=https://goproxy.cn,direct` (PowerShell: `$env:GOPROXY=...`).

## Update

```sh
zvk self-update                 # rebuild & install the latest release
zvk self-update --version=v0.2.0
zvk self-update --dry-run       # show what it would do
```

`self-update` re-runs `go install github.com/zoptia/zvk@<version>` into an
isolated `GOBIN`, then atomically swaps the binary in `~/.zvk/bin/`. Building to
a temp dir first avoids clobbering the live binary on a failed build and
sidesteps the Windows "cannot overwrite a running .exe" lock. It requires a
`go` toolchain (the one zvk manages, or one on PATH).

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
│   └── gofmt        → ../go/channels/stable/bin/gofmt
├── zig/
│   ├── channels/{release,nightly}          # → ../versions/<ver>
│   └── versions/<ver>/
├── go/
│   ├── channels/stable                     # → ../versions/<ver>
│   └── versions/<ver>/                     # top level is the unpacked go/ contents
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

### Go

1. Fetch <https://go.dev/dl/?mode=json> for the version list and sha256.
2. Skip if already installed.
3. Otherwise download the `.tar.gz` → verify sha256 → extract in pure Go
   (stdlib `compress/gzip` + `archive/tar`, stripping the top-level `go/`
   directory).
4. Maintain `go/channels/stable` and the `bin/{go,gofmt}` symlinks.

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
| `ZVK_VERSION`        | Module version the installer / `self-update` installs   |
| `GOPROXY`            | Go module proxy used by the installer and `self-update` |

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
