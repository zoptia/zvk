# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Language conventions

- All project documentation, code, comments, identifiers, and commit messages: **English**.


## Commands

```sh
go build -o zvk .          # build the binary
go vet ./...                  # static check
go mod tidy                   # sync dependencies
go install github.com/zoptia/zvk@latest   # build + install to $GOBIN
```

No test suite exists yet. To smoke-test install flows without touching the real
`~/.zvk/`, run with a sandbox root:

```sh
ZVK_ROOT=$(mktemp -d) ZVK_NO_MODIFY_PATH=1 ./zvk zig install nightly
ZVK_ROOT=$(mktemp -d) ZVK_NO_MODIFY_PATH=1 ./zvk go install latest
```

`ZVK_NO_MINISIGN=1` skips the Ed25519 verification of Zig tarballs (faster
iteration; never set in production).

## Distribution model (load-bearing)

zvk distributes itself **from source**, not as a prebuilt binary:

- `install.sh` / `install.ps1` are stage-0 shell scripts. They always download
  the latest Go into the managed `<root>/go/versions/<ver>/` (never reuse a
  system `go`), build zvk with it via `GOTOOLCHAIN=local GOBIN=<root>/bin go
  install github.com/zoptia/zvk@<version>`, then run `zvk self-install`, `zvk go
  install latest` (promotes that already-extracted Go to the `stable` channel —
  re-links, no re-download), and `zvk zig install`. That managed Go is both
  zvk's build dependency and its default `go`. Re-running the script
  repairs/upgrades in place.
- `self-update` (`cmd_self.go`) is a thin convenience: it downloads the
  canonical `install.sh`/`install.ps1` and executes it. The installer is the
  single update path — there are not two code paths that can drift.

Why source-install over a downloaded binary: a locally compiled binary carries
no Mark-of-the-Web, so it avoids the SmartScreen/Gatekeeper prompts that hit
downloaded executables; module sources are hash-verified via GOSUMDB; and
releasing is just `git tag`. The trade-off: a `go` toolchain is required, and
public `go install` implies the repo source is public.

`modulePath` (`util.go`) is the single source of truth for the install path.

## Architecture

`zvk` is a single-binary, cross-platform toolchain + key manager. Everything
lives under `~/.zvk/` (override with `ZVK_ROOT`).

### On-disk layout (load-bearing for the whole codebase)

```
<root>/
├── bin/                      # the only directory on user PATH
│   ├── zvk
│   ├── zig          → ../zig/channels/release/zig            (symlink chain)
│   ├── zig-nightly  → ../zig/channels/nightly/zig
│   ├── go           → ../go/channels/stable/bin/go
│   ├── gofmt        → ../go/channels/stable/bin/gofmt
│   ├── node         → ../node/channels/lts/bin/node
│   ├── npm          → ../node/channels/lts/bin/npm
│   └── npx          → ../node/channels/lts/bin/npx
├── zig/{channels,versions}/
├── go/{channels,versions}/
├── node/{channels,versions}/
└── ssh/keys/
```

Switching versions is just `replaceSymlink` on a single channel link — the
`bin/` entry is unchanged, the user's PATH is untouched. On Windows, where
unprivileged users cannot create symlinks, channels are recorded as
`channels/<ch>.txt` files and `bin/` entries are `.cmd` shims that exec the
real binary by absolute path (see `channel.go` and `toolchain.go`'s `installBin`).

### Module map

- `main.go` — top-level command dispatch only (a small registry mapping
  `zig`/`go`/`node` to their driver).
- `toolchain.go` — the declarative `toolchain` driver struct plus the single
  install/use/uninstall/list/which/status pipeline every managed toolchain runs
  through. Differences (index, target, verification, channels, bin layout) are
  driver fields, not duplicated command code. Also holds shadow-tool detection
  (`warnShadowedTools`): when a managed command (`go`, `node`) is also on PATH
  outside zvk, it prints an advisory + cleanup hints and never touches it.
- `cmd_{zig,go,node}.go` — one `toolchain` driver each: index parsing, target
  mapping, verification (zig minisign, go/node sha256, node SHASUMS256.txt),
  channel set, bin layout. Adding a toolchain = one more driver here.
- `cmd_{ssh,self}.go` — ssh key management; combined status, self-install, and
  `self-update` (downloads + re-runs the installer).
- `cmd_app.go` — the `app` command: install assorted tools/apps that are NOT
  version-managed toolchains (Homebrew, Claude Code, winget, scoop). These have
  no channels or version switching, so they live outside the toolchain driver as
  platform-specific "recipes" (`appRecipes`): each knows how to detect itself on
  PATH and install itself by running its official script (`runShellScript` for
  POSIX `bash`, `runPowerShellCommand` for Windows `iwr|iex`; stdin passed
  through for sudo prompts). A recipe with `install == nil` has no automated
  installer (e.g. winget ships with Windows' App Installer) — it just prints
  `installHint`. `app list` shows only recipes whose `osSupport` matches the
  current GOOS. This is deliberately not a package manager — no version tracking,
  no bin symlinks; `uninstall` just prints the official steps. Adding one =
  appending an `appRecipe`.
- `cmd_vminit.go` — `app vminit`: unlike the install recipes, this reconfigures
  the *local* host into a low-footprint VM node (hostname → `vm-<primary-ipv4>`,
  no sleep, no file indexing, no GUI on Linux / reduced animations on macOS).
  Steps are platform-specific (`vmSteps` per GOOS); root-requiring commands are
  wrapped in `sudo` (stdin passed through). `primaryIPv4` uses a UDP `net.Dial`
  to read the default-route interface IP cross-platform (no `ip`/`ifconfig`
  parsing). `--dry-run` prints commands without running them.
- `cmd_fetch.go` — the `fetch` command: a single-shot HTTP client built on
  `github.com/bogdanfinn/tls-client` that impersonates the latest Chrome's TLS +
  HTTP/2 fingerprint (ClientHello/JA3, HTTP/2 SETTINGS, header order), so
  anti-bot edges (Cloudflare, Akamai) return real content instead of a
  challenge. Default profile is the upstream-tracked "latest Chrome"
  (`profiles.DefaultClientProfile`); `--profile` selects any `MappedTLSClients`
  key. Sends a realistic Chrome header set; gzip/brotli are auto-decompressed so
  the body is readable text. Linked into the toolchain via `bogdanfinn/fhttp`
  (a net/http fork), not stdlib `net/http`. Companion `fetchdoc.go` writes its
  `<root>/fetch/CLAUDE.md` pointer.
- `cmd_serve.go` — the `serve` command: a stdlib `net/http` static file server
  to share a generated report over HTTP. Binds `0.0.0.0` by default (prints a
  localhost + a LAN URL via `primaryIPv4`); `--local` restricts to loopback.
  Single-file mode (`path` is a file) serves ONLY that file at every path, so
  siblings stay private; directory mode uses `FileServer`. `--once` exits after
  the first download. It blocks, so an assistant runs it in the background and
  reads the printed URL. Also writes its own `<root>/serve/CLAUDE.md` pointer.
- `claudemd.go` — aggregates each feature's `<root>/<feature>/CLAUDE.md` into a
  single `<root>/CLAUDE.md` and injects exactly ONE `@import` into the user's
  global `~/.claude/CLAUDE.md`, migrating away older per-feature imports. Every
  feature doc writer (`zigdoc`, `fetchdoc`, `cmd_serve`) calls
  `refreshZvkClaudeMd` after writing its pointer. Gated by `ZVK_NO_DOCS` /
  `ZVK_NO_CLAUDE_MD`.
- `zigdoc.go` — the zig driver's `postInstall` hook (`writeZigDocs`). After a
  zig install it deterministically stages the raw material an assistant needs to
  target *that* build instead of stale memory: `REFERENCE.<channel>.md`
  (version-pinned pointers + grep topic map), `STD_INDEX.md` (column-0 `pub`
  decls per top-level `lib/std` file — not the ~25k full tree), a local
  `release-notes.html` snapshot, an `ADAPTATION.prompt.md`, and `zig/CLAUDE.md`
  (surfaced to the assistant via the aggregate in `claudemd.go`). A dev/nightly
  build has no published `release-notes.html` (the URL 404s), so it skips that
  fetch and points the REFERENCE/ADAPTATION at the master std commit log
  instead. zvk never calls an LLM —
  it stages inputs + a prompt; the assistant writes `ADAPTATION.md` in-session.
  All best-effort: failures print advisories, never failing the install. Gated
  by `ZVK_NO_DOCS` / `ZVK_NO_CLAUDE_MD`.
- `channel.go` — abstract `readActiveVersion` / `setActiveVersion`. POSIX uses
  a directory symlink; Windows uses a `.txt` file. Every toolchain goes
  through this.
- `archive.go` — pure-Go decompression. `.zip` via `archive/zip`, `.tar.gz` via
  `compress/gzip`, `.tar.xz` via `github.com/ulikunitz/xz`, all stacked on
  `archive/tar`. No subprocess `tar`. Handles regular files, dirs, symlinks,
  hardlinks (with strip-component applied to hardlink targets); other entry
  types are skipped.
- `http.go` — shared `http.Client` (5-minute timeout) and `downloadToMemory`.
  Sets `User-Agent: zvk/<version>` so GitHub returns JSON, not HTML.
- `minisign.go` — Ed25519 verification supporting both raw (`Ed`) and
  BLAKE2b-512 prehashed (`ED`) signatures. Zig nightly uses `ED`.
- `pathenv.go` — idempotent shell-rc edit for bash/zsh/fish. Searches for
  `binDir` substring before appending to avoid duplicate writes.
- `util.go` — `defaultRoot`, `currentTarget`, `writeFileAtomic` (tmp + rename),
  `replaceSymlink` (rm + symlink), `sha256Hex`, platform predicates, the
  user-facing `zvkVersion` constant, and the `modulePath` install path.

### Toolchain install pipeline (one shape for Zig, Go, Node — see `toolchain.go`)

1. Fetch upstream index JSON (`ziglang.org/download/index.json` or
   `go.dev/dl/?mode=json`).
2. Resolve the entry for `(channel, target)` — for Zig release, the latest
   semver key wins; for nightly, `master`; for Go latest, the first `stable`
   release.
3. Skip if `versions/<ver>/` already has the executable.
4. Download tarball → verify sha256 → (Zig only) fetch `.minisig` and verify.
5. Extract via `extractArchive` (dispatched on URL suffix).
6. `setActiveVersion(channel, ver)` → `installBin(channel)` → `setupPath(bin)`.
7. Optional `postInstall(root, ver, channel)` for side artifacts — zig uses it
   for the Claude Code reference docs (`zigdoc.go`); go/node leave it nil.

The download is always to memory (no streaming). Tarballs cap around ~150 MB
in practice, which is fine for `[]byte`.

This is distinct from how **zvk itself** is installed/updated — see
"Distribution model" above (that path goes through `go install`, not this
download+extract pipeline).

## Conventions

- **Minimal dependencies.** Core toolchain logic uses only `golang.org/x/crypto`
  (ssh + blake2b) and `github.com/ulikunitz/xz`. The `fetch` command adds
  `github.com/bogdanfinn/tls-client` (+ its `fhttp`/`utls` forks) — the one
  deliberate exception, since real browser TLS fingerprinting can't be done with
  stdlib `crypto/tls`. Do not introduce more without a comparably strong reason.
- **No subprocess for decompression.** All archive handling is in-process. The
  `exec.Cmd` users are `ssh-add`, `pbcopy`/`wl-copy`/`xclip` (clipboard), the
  installer script invoked by `self-update` (`sh`/`powershell`), and the official
  tool scripts run by `app` (`bash` / `powershell`) — these pass through
  stdin/stdout/stderr.
- **Atomic file writes.** Use `writeFileAtomic` for any user-visible file
  (binaries, keys, config). It writes to a tmp file in the same dir and
  renames.
- **Symlink replacement.** Use `replaceSymlink` (rm + symlink) — never write
  through an existing symlink.
- **POSIX/Windows split.** Anything filesystem-shaped (channels, bin entries,
  PATH setup) branches on `isWindows()`. Keep the POSIX path the simple one.

## Environment variables

| Var | Effect |
|---|---|
| `ZVK_ROOT` | Override default `~/.zvk` install root |
| `ZVK_NO_MODIFY_PATH` | Skip writing PATH to shell rc |
| `ZVK_NO_MINISIGN` | Skip minisign verification of Zig tarballs (dev only) |
| `ZVK_NO_DOCS` | Skip generating the zig Claude Code reference docs (see `zigdoc.go`) |
| `ZVK_NO_CLAUDE_MD` | Generate the zig reference docs but don't write/inject `CLAUDE.md` |
| `ZVK_VERSION` | Module version the installer / `self-update` installs |
| `GOPROXY` | Go module proxy used by the installer and `self-update` |

## Release / version bumps

Releasing is `git tag vX.Y.Z && git push --tags`. `go install
github.com/zoptia/zvk@latest` then resolves that tag, so the installer and
`self-update` pick it up with no asset-building step.

`zvkVersion` in `util.go` is the user-facing version reported by `zvk version`
and used in the `User-Agent` header — bump it to match the tag you push.
