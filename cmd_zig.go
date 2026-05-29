package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	zigIndexURL = "https://ziglang.org/download/index.json"

	// Zig project's official minisign public key. Source: https://ziglang.org/download/
	zigMinisignPubkey = "RWSGOq2NVecA2UPNdBUZykf1CCb147pkmdtYxgb3Ti+JO/wCYvhbAb/U"

	zigDefaultChannel = "release"
)

const zigUsage = `Usage:
  zvk zig install [release|nightly]    Install Zig (default: release)
  zvk zig update  [release|nightly|all] Re-run install for the given target(s)
  zvk zig use <channel> <version>      Point a channel at an installed version
  zvk zig uninstall <version>          Remove an installed version
  zvk zig list                         List installed versions and channel state
  zvk zig which [channel]              Show active version for a channel
  zvk zig status [--json]              Print state (text or JSON)
`

func runZig(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(stdout, zigUsage)
		return nil
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "install":
		return runZigInstall(rest, stdout)
	case "update":
		return runZigUpdate(rest, stdout)
	case "use":
		return runZigUse(rest, stdout)
	case "uninstall", "remove", "rm":
		return runZigUninstall(rest, stdout)
	case "list", "ls":
		return runZigList(stdout)
	case "which":
		return runZigWhich(rest, stdout)
	case "status", "info":
		return runZigStatus(rest, stdout)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, zigUsage)
		return nil
	default:
		return usageErrorf("zig: unknown subcommand '%s'\n\n%s", sub, zigUsage)
	}
}

// ============================================================================
// Channels
// ============================================================================

func zigChannelValid(s string) bool { return s == "release" || s == "nightly" }

// zigBinName returns the user-facing command name for a channel — `zig` for
// release, `zig-nightly` for nightly, with a `.cmd` suffix on Windows shims.
func zigBinName(channel string) string {
	name := "zig"
	if channel == "nightly" {
		name = "zig-nightly"
	}
	if isWindows() {
		name += ".cmd"
	}
	return name
}

func zigExeName() string {
	if isWindows() {
		return "zig.exe"
	}
	return "zig"
}

var zigDirs = toolDirs{name: "zig"}

func zigIsInstalled(versionDir string) bool {
	return fileExists(filepath.Join(versionDir, zigExeName()))
}

// zigInstallBin places `bin/zig` (or `bin/zig-nightly`) routed through the channel.
// On POSIX it's a symlink chain; on Windows it's a `.cmd` shim that exec's the
// real `zig.exe` so Zig can locate its adjacent `lib/`.
func zigInstallBin(root, channel string) error {
	bd := binDir(root)
	if err := os.MkdirAll(bd, 0o755); err != nil {
		return err
	}
	binPath := filepath.Join(bd, zigBinName(channel))

	if isWindows() {
		active, err := zigDirs.readActive(root,channel)
		if err != nil {
			return err
		}
		if active == "" {
			return fmt.Errorf("channel %s is not set", channel)
		}
		exePath := filepath.Join(zigDirs.versionDir(root,active), zigExeName())
		return writeWindowsShim(binPath, exePath)
	}
	// bin/zig -> ../zig/channels/<ch>/zig
	target := filepath.Join("..", "zig", "channels", channel, zigExeName())
	return replaceSymlink(target, binPath)
}

// ============================================================================
// Index parsing
// ============================================================================

type zigEntry struct {
	Version string
	Tarball string
	Shasum  string
}

// resolveZigEntry picks the tarball entry for `channel` + `target` from the
// release index. For `release`, the latest semver key (excluding `master`) wins.
func resolveZigEntry(indexJSON []byte, channel, target string) (zigEntry, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(indexJSON, &raw); err != nil {
		return zigEntry{}, err
	}

	var key string
	switch channel {
	case "nightly":
		key = "master"
	case "release":
		key = pickLatestRelease(raw)
		if key == "" {
			return zigEntry{}, fmt.Errorf("no release found in index")
		}
	default:
		return zigEntry{}, fmt.Errorf("unknown channel: %s", channel)
	}

	chRaw, ok := raw[key]
	if !ok {
		return zigEntry{}, fmt.Errorf("channel key %q not found", key)
	}
	var ch map[string]json.RawMessage
	if err := json.Unmarshal(chRaw, &ch); err != nil {
		return zigEntry{}, err
	}

	version := key
	if v, ok := ch["version"]; ok {
		_ = json.Unmarshal(v, &version)
	}

	tRaw, ok := ch[target]
	if !ok {
		return zigEntry{}, fmt.Errorf("unsupported target %q (channel %q)", target, channel)
	}
	var t struct {
		Tarball string `json:"tarball"`
		Shasum  string `json:"shasum"`
	}
	if err := json.Unmarshal(tRaw, &t); err != nil {
		return zigEntry{}, err
	}
	if t.Tarball == "" || t.Shasum == "" {
		return zigEntry{}, fmt.Errorf("missing tarball/shasum for %s/%s", channel, target)
	}
	return zigEntry{Version: version, Tarball: t.Tarball, Shasum: t.Shasum}, nil
}

func pickLatestRelease(m map[string]json.RawMessage) string {
	var best string
	var bestParts [3]int
	for k := range m {
		if k == "master" {
			continue
		}
		p, ok := parseSemver3(k)
		if !ok {
			continue
		}
		if best == "" || semverGreater(p, bestParts) {
			best = k
			bestParts = p
		}
	}
	return best
}

func parseSemver3(s string) ([3]int, bool) {
	var p [3]int
	parts := strings.SplitN(s, ".", 4)
	if len(parts) < 3 {
		return p, false
	}
	for i := 0; i < 3; i++ {
		v, err := strconv.Atoi(parts[i])
		if err != nil {
			return p, false
		}
		p[i] = v
	}
	return p, true
}

func semverGreater(a, b [3]int) bool {
	for i := 0; i < 3; i++ {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return false
}

// ============================================================================
// install / update
// ============================================================================

func runZigInstall(args []string, stdout io.Writer) error {
	channel := zigDefaultChannel
	for _, a := range args {
		if strings.HasPrefix(a, "--") {
			return usageErrorf("zig: unknown flag '%s'", a)
		}
		channel = a
	}
	if !zigChannelValid(channel) {
		return usageErrorf("zig: unknown channel '%s' (expected 'nightly' or 'release')", channel)
	}

	target, err := currentTarget()
	if err != nil {
		return err
	}
	root, err := defaultRoot()
	if err != nil {
		return err
	}

	fmt.Fprintf(stdout, "[zvk zig] target: %s\n", target)
	fmt.Fprintf(stdout, "[zvk zig] channel: %s\n", channel)
	fmt.Fprintf(stdout, "[zvk zig] install root: %s\n", root)
	fmt.Fprintf(stdout, "[zvk zig] fetching index from %s\n", zigIndexURL)

	indexBytes, err := downloadToMemory(zigIndexURL)
	if err != nil {
		return err
	}
	entry, err := resolveZigEntry(indexBytes, channel, target)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "[zvk zig] version: %s\n", entry.Version)

	versionDir := zigDirs.versionDir(root,entry.Version)
	if zigIsInstalled(versionDir) {
		fmt.Fprintf(stdout, "[zvk zig] %s already installed at %s\n", entry.Version, versionDir)
	} else {
		fmt.Fprintf(stdout, "[zvk zig] tarball: %s\n", entry.Tarball)
		fmt.Fprintln(stdout, "[zvk zig] downloading...")
		tarball, err := downloadToMemory(entry.Tarball)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "[zvk zig] downloaded %d bytes\n", len(tarball))

		actual := sha256Hex(tarball)
		if actual != entry.Shasum {
			return fmt.Errorf("sha256 mismatch\n  expected: %s\n  actual:   %s", entry.Shasum, actual)
		}
		fmt.Fprintln(stdout, "[zvk zig] sha256 verified")

		if os.Getenv("ZVK_NO_MINISIGN") != "" {
			fmt.Fprintln(stdout, "[zvk zig] minisign verification skipped (ZVK_NO_MINISIGN set)")
		} else {
			minisigURL := entry.Tarball + ".minisig"
			fmt.Fprintf(stdout, "[zvk zig] fetching %s\n", minisigURL)
			minisigText, err := downloadToMemory(minisigURL)
			if err != nil {
				return err
			}
			if err := verifyMinisign(tarball, minisigText, zigMinisignPubkey); err != nil {
				return fmt.Errorf("minisign verification failed: %w", err)
			}
			fmt.Fprintln(stdout, "[zvk zig] minisign verified")
		}

		fmt.Fprintf(stdout, "[zvk zig] extracting to %s\n", versionDir)
		if err := extractArchive(tarball, versionDir, 1, entry.Tarball); err != nil {
			return err
		}
	}

	if err := zigDirs.setActive(root,channel, entry.Version); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "[zvk zig] channel '%s' -> %s\n", channel, entry.Version)

	if err := zigInstallBin(root, channel); err != nil {
		return err
	}
	bd := binDir(root)
	fmt.Fprintf(stdout, "[zvk zig] %s/%s ready\n", bd, zigBinName(channel))

	if err := setupPath(bd, stdout); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "[zvk zig] done")
	return nil
}

func runZigUpdate(args []string, stdout io.Writer) error {
	// `update all` fans out across every channel; anything else is a thin
	// alias for `install` (which already idempotently no-ops when up to date).
	if len(args) > 0 && args[0] == "all" {
		if err := runZigInstall([]string{"release"}, stdout); err != nil {
			return err
		}
		return runZigInstall([]string{"nightly"}, stdout)
	}
	return runZigInstall(args, stdout)
}

func runZigUse(args []string, stdout io.Writer) error {
	if len(args) < 2 {
		return usageErrorf("usage: zvk zig use <channel> <version>")
	}
	channel, version := args[0], args[1]
	if !zigChannelValid(channel) {
		return usageErrorf("zig: unknown channel '%s' (expected 'nightly' or 'release')", channel)
	}
	root, err := defaultRoot()
	if err != nil {
		return err
	}
	if !zigIsInstalled(zigDirs.versionDir(root,version)) {
		return fmt.Errorf("zig: version '%s' is not installed (run: zvk zig install %s)", version, version)
	}
	if err := zigDirs.setActive(root,channel, version); err != nil {
		return err
	}
	if err := zigInstallBin(root, channel); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "channel '%s' -> %s\n", channel, version)
	return nil
}

func runZigUninstall(args []string, stdout io.Writer) error {
	if len(args) < 1 {
		return usageErrorf("usage: zvk zig uninstall <version>")
	}
	version := args[0]
	root, err := defaultRoot()
	if err != nil {
		return err
	}
	versionDir := zigDirs.versionDir(root,version)
	if !zigIsInstalled(versionDir) {
		return fmt.Errorf("zig: version '%s' is not installed", version)
	}
	for _, ch := range []string{"release", "nightly"} {
		active, err := zigDirs.readActive(root,ch)
		if err != nil {
			return err
		}
		if active == version {
			return fmt.Errorf("zig: '%s' is the active version for channel '%s'; switch first with `zvk zig use %s <other>`", version, ch, ch)
		}
	}
	if err := os.RemoveAll(versionDir); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "removed %s\n", version)
	return nil
}

func runZigList(stdout io.Writer) error {
	root, err := defaultRoot()
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, "Channels:")
	for _, ch := range []string{"nightly", "release"} {
		ver, err := zigDirs.readActive(root, ch)
		if err != nil {
			return err
		}
		printChannelMapping(stdout, ch, ver)
	}
	fmt.Fprintln(stdout)
	vs, err := listSubdirs(zigDirs.versionsDir(root))
	if err != nil {
		return err
	}
	printInstalledVersions(stdout, vs, false)
	return nil
}

func runZigWhich(args []string, stdout io.Writer) error {
	channel := zigDefaultChannel
	if len(args) > 0 {
		channel = args[0]
	}
	if !zigChannelValid(channel) {
		return usageErrorf("zig: unknown channel '%s' (expected 'nightly' or 'release')", channel)
	}
	root, err := defaultRoot()
	if err != nil {
		return err
	}
	ver, err := zigDirs.readActive(root,channel)
	if err != nil {
		return err
	}
	if ver == "" {
		fmt.Fprintf(stdout, "%s: (not installed)\n", channel)
		return nil
	}
	bp := filepath.Join(root, "bin", zigBinName(channel))
	fmt.Fprintf(stdout, "%s: %s\n  %s\n", channel, ver, bp)
	return nil
}

// ============================================================================
// status
// ============================================================================

type zigChannelEntry struct {
	Version    string `json:"version"`
	BinCommand string `json:"command"`
	BinPath    string `json:"path"`
}

type zigStatus struct {
	InstallRoot string                      `json:"install_root"`
	BinDir      string                      `json:"bin_dir"`
	Channels    map[string]*zigChannelEntry `json:"channels"`
	Installed   []string                    `json:"installed_versions"`
}

func collectZigStatus() (*zigStatus, error) {
	root, err := defaultRoot()
	if err != nil {
		return nil, err
	}
	s := &zigStatus{
		InstallRoot: root,
		BinDir:      binDir(root),
		Channels:    map[string]*zigChannelEntry{"release": nil, "nightly": nil},
	}
	for _, ch := range []string{"release", "nightly"} {
		ver, err := zigDirs.readActive(root,ch)
		if err != nil {
			return nil, err
		}
		if ver == "" {
			continue
		}
		s.Channels[ch] = &zigChannelEntry{
			Version:    ver,
			BinCommand: zigBinName(ch),
			BinPath:    filepath.Join(root, "bin", zigBinName(ch)),
		}
	}
	vs, err := listSubdirs(zigDirs.versionsDir(root))
	if err != nil {
		return nil, err
	}
	s.Installed = vs
	return s, nil
}

func runZigStatus(args []string, stdout io.Writer) error {
	jsonMode := len(args) > 0 && (args[0] == "--json" || args[0] == "-j")
	s, err := collectZigStatus()
	if err != nil {
		return err
	}
	if jsonMode {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(s)
	}
	fmt.Fprintln(stdout, "Active Zig versions:")
	for _, ch := range []string{"release", "nightly"} {
		label := "(release, default)"
		if ch == "nightly" {
			label = "(nightly, opt-in)"
		}
		e := s.Channels[ch]
		if e == nil {
			defName := "zig"
			if ch == "nightly" {
				defName = "zig-nightly"
			}
			fmt.Fprintf(stdout, "  %-13s  (not installed)                   %s\n", defName, label)
		} else {
			fmt.Fprintf(stdout, "  %-13s  %-32s  %s\n", e.BinCommand, e.Version, label)
		}
	}
	fmt.Fprintf(stdout, "\nInstall root: %s\n", s.InstallRoot)
	printInstalledVersions(stdout, s.Installed, true)
	return nil
}
