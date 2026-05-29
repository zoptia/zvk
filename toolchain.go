package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// toolchain is the declarative driver for one managed toolchain (zig, go, node).
// channel.go's toolDirs provides the on-disk <root>/<name>/{channels,versions}
// layout; everything tool-specific is captured in the fields below so the
// install/use/uninstall/list/which/status pipeline can be written exactly once.
//
// Adding a new toolchain is filling in one of these structs plus its index
// parsing — not copying the whole command surface a third time.
type toolchain struct {
	name           string        // "zig" / "go" / "node"
	usage          string        // `zvk <name> help` text
	channels       []channelInfo // declared in display order
	defaultChannel string
	archiveStrip   int      // leading path components to drop on extract
	conflictCmds   []string // command names to warn about if shadowed on PATH
	dirs           toolDirs

	// normalizeVersion maps user input ("1.23.4") to the canonical form used for
	// version dirs and display ("go1.23.4"). nil means identity.
	normalizeVersion func(string) string

	// parseInstall turns `install` args into a (channel, requested) pair. zig
	// takes a channel and always wants the newest; go takes a version on its lone
	// channel. requested "" / "latest" mean newest.
	parseInstall func(args []string) (channel, requested string, err error)

	// parseUse turns `use` args into a (channel, version) pair.
	parseUse func(args []string) (channel, version string, err error)

	// resolve fetches the upstream index and selects the asset for (channel,
	// requested). requested is already normalized.
	resolve func(channel, requested string) (toolAsset, error)

	// isInstalled reports whether versionDir already holds a usable toolchain.
	isInstalled func(versionDir string) bool

	// bins lists the bin/ entries to wire up for a channel.
	bins func(channel string) []binSpec

	// listRemote prints available upstream versions for `list --remote`. nil
	// means the toolchain doesn't support remote listing.
	listRemote func(stdout io.Writer) error

	// postInstall runs after a successful install (channel activated, bins
	// linked, PATH set). It is for side artifacts — e.g. zig generates Claude
	// Code reference docs here. Best-effort: it must not fail the install. nil
	// means nothing extra runs.
	postInstall func(root, version, channel string, stdout io.Writer)
}

// channelInfo names a channel and its human label for status output.
type channelInfo struct {
	name  string
	label string // e.g. "(release, default)"
}

// toolAsset is the resolved download for a (channel, version).
type toolAsset struct {
	version  string
	url      string
	sha256   string // expected hex digest; "" if the index provides none
	filename string // drives archive-format dispatch (.zip/.tar.gz/.tar.xz)

	// verify runs an extra integrity check (e.g. zig minisign) after the sha256
	// match. It owns its own progress output. nil means no extra check.
	verify func(tarball []byte, stdout io.Writer) error
}

// binSpec is one bin/ entry. exe is the executable path relative to a version
// dir, including any platform-specific suffix/layout (e.g. node.exe sits at the
// archive root on Windows but under bin/ on POSIX).
type binSpec struct {
	link string        // bin/ entry base name (POSIX); ".cmd" appended on Windows
	exe  func() string // executable path relative to a version dir
}

// ----------------------------------------------------------------------------
// channel helpers
// ----------------------------------------------------------------------------

func (tc *toolchain) channelNames() []string {
	out := make([]string, len(tc.channels))
	for i, c := range tc.channels {
		out[i] = c.name
	}
	return out
}

func (tc *toolchain) channelValid(name string) bool {
	for _, c := range tc.channels {
		if c.name == name {
			return true
		}
	}
	return false
}

func (tc *toolchain) channelLabel(name string) string {
	for _, c := range tc.channels {
		if c.name == name {
			return c.label
		}
	}
	return ""
}

func (tc *toolchain) normalize(raw string) string {
	if tc.normalizeVersion == nil {
		return raw
	}
	return tc.normalizeVersion(raw)
}

// binEntryName is the on-disk bin/ filename for a link base (`.cmd` on Windows).
func binEntryName(link string) string {
	if isWindows() {
		return link + ".cmd"
	}
	return link
}

// primaryBin is the bin used to report a channel's command/path in status/which.
func (tc *toolchain) primaryBin(channel string) binSpec {
	bs := tc.bins(channel)
	return bs[0]
}

// ----------------------------------------------------------------------------
// dispatch
// ----------------------------------------------------------------------------

func runToolchain(tc *toolchain, args []string, stdout io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(stdout, tc.usage)
		return nil
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "install":
		channel, requested, err := tc.parseInstall(rest)
		if err != nil {
			return err
		}
		return toolchainInstall(tc, channel, requested, stdout)
	case "update":
		return toolchainUpdate(tc, rest, stdout)
	case "use":
		channel, version, err := tc.parseUse(rest)
		if err != nil {
			return err
		}
		return toolchainUse(tc, channel, version, stdout)
	case "uninstall", "remove", "rm":
		if len(rest) < 1 {
			return usageErrorf("usage: zvk %s uninstall <version>", tc.name)
		}
		return toolchainUninstall(tc, rest[0], stdout)
	case "list", "ls":
		return toolchainList(tc, rest, stdout)
	case "which":
		channel := tc.defaultChannel
		if len(rest) > 0 {
			channel = rest[0]
		}
		return toolchainWhich(tc, channel, stdout)
	case "status", "info":
		return toolchainStatusCmd(tc, rest, stdout)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, tc.usage)
		return nil
	default:
		return usageErrorf("%s: unknown subcommand '%s'\n\n%s", tc.name, sub, tc.usage)
	}
}

// ----------------------------------------------------------------------------
// install pipeline — fetch index → download → verify → extract → activate → link
// ----------------------------------------------------------------------------

func toolchainInstall(tc *toolchain, channel, requestedRaw string, stdout io.Writer) error {
	if !tc.channelValid(channel) {
		return usageErrorf("%s: unknown channel '%s' (expected %s)", tc.name, channel, strings.Join(tc.channelNames(), ", "))
	}
	requested := tc.normalize(requestedRaw)
	root, err := defaultRoot()
	if err != nil {
		return err
	}
	tag := "[zvk " + tc.name + "]"
	fmt.Fprintf(stdout, "%s channel: %s\n", tag, channel)
	fmt.Fprintf(stdout, "%s install root: %s\n", tag, root)

	asset, err := tc.resolve(channel, requested)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "%s version: %s\n", tag, asset.version)

	versionDir := tc.dirs.versionDir(root, asset.version)
	if tc.isInstalled(versionDir) {
		fmt.Fprintf(stdout, "%s %s already installed at %s\n", tag, asset.version, versionDir)
	} else {
		fmt.Fprintf(stdout, "%s downloading %s\n", tag, asset.url)
		tarball, err := downloadToMemory(asset.url)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "%s downloaded %d bytes\n", tag, len(tarball))

		if asset.sha256 != "" {
			actual := sha256Hex(tarball)
			if actual != asset.sha256 {
				return fmt.Errorf("sha256 mismatch\n  expected: %s\n  actual:   %s", asset.sha256, actual)
			}
			fmt.Fprintf(stdout, "%s sha256 verified\n", tag)
		}
		if asset.verify != nil {
			if err := asset.verify(tarball, stdout); err != nil {
				return err
			}
		}

		fmt.Fprintf(stdout, "%s extracting to %s\n", tag, versionDir)
		if err := extractArchive(tarball, versionDir, tc.archiveStrip, asset.filename); err != nil {
			return err
		}
	}

	if err := tc.dirs.setActive(root, channel, asset.version); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "%s channel '%s' -> %s\n", tag, channel, asset.version)

	if err := tc.installBin(root, channel); err != nil {
		return err
	}
	if err := setupPath(binDir(root), stdout); err != nil {
		return err
	}
	warnShadowedTools(tc, root, stdout)
	if tc.postInstall != nil {
		tc.postInstall(root, asset.version, channel, stdout)
	}
	fmt.Fprintf(stdout, "%s done\n", tag)
	return nil
}

// toolchainUpdate re-runs install at latest for one channel, the default
// channel (no args), or every channel (`all`).
func toolchainUpdate(tc *toolchain, args []string, stdout io.Writer) error {
	var targets []string
	switch {
	case len(args) == 0:
		targets = []string{tc.defaultChannel}
	case args[0] == "all":
		targets = tc.channelNames()
	default:
		if !tc.channelValid(args[0]) {
			return usageErrorf("%s: unknown channel '%s' (expected %s or 'all')", tc.name, args[0], strings.Join(tc.channelNames(), ", "))
		}
		targets = []string{args[0]}
	}
	for _, ch := range targets {
		if err := toolchainInstall(tc, ch, "latest", stdout); err != nil {
			return err
		}
	}
	return nil
}

func toolchainUse(tc *toolchain, channel, versionRaw string, stdout io.Writer) error {
	if !tc.channelValid(channel) {
		return usageErrorf("%s: unknown channel '%s' (expected %s)", tc.name, channel, strings.Join(tc.channelNames(), ", "))
	}
	version := tc.normalize(versionRaw)
	root, err := defaultRoot()
	if err != nil {
		return err
	}
	if !tc.isInstalled(tc.dirs.versionDir(root, version)) {
		return fmt.Errorf("%s: version '%s' is not installed (run: zvk %s install %s)", tc.name, version, tc.name, version)
	}
	if err := tc.dirs.setActive(root, channel, version); err != nil {
		return err
	}
	if err := tc.installBin(root, channel); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "channel '%s' -> %s\n", channel, version)
	return nil
}

func toolchainUninstall(tc *toolchain, versionRaw string, stdout io.Writer) error {
	version := tc.normalize(versionRaw)
	root, err := defaultRoot()
	if err != nil {
		return err
	}
	versionDir := tc.dirs.versionDir(root, version)
	if !tc.isInstalled(versionDir) {
		return fmt.Errorf("%s: version '%s' is not installed", tc.name, version)
	}
	for _, ch := range tc.channelNames() {
		active, err := tc.dirs.readActive(root, ch)
		if err != nil {
			return err
		}
		if active == version {
			return fmt.Errorf("%s: '%s' is the active version for channel '%s'; switch first with `zvk %s use ...`", tc.name, version, ch, tc.name)
		}
	}
	if err := os.RemoveAll(versionDir); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "removed %s\n", version)
	return nil
}

func toolchainList(tc *toolchain, args []string, stdout io.Writer) error {
	for _, a := range args {
		if a == "--remote" || a == "-r" {
			if tc.listRemote == nil {
				return usageErrorf("%s: list --remote is not supported", tc.name)
			}
			return tc.listRemote(stdout)
		}
	}
	root, err := defaultRoot()
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, "Channels:")
	for _, ch := range tc.channelNames() {
		ver, err := tc.dirs.readActive(root, ch)
		if err != nil {
			return err
		}
		printChannelMapping(stdout, ch, ver)
	}
	fmt.Fprintln(stdout)
	vs, err := listSubdirs(tc.dirs.versionsDir(root))
	if err != nil {
		return err
	}
	printInstalledVersions(stdout, vs, false)
	return nil
}

func toolchainWhich(tc *toolchain, channel string, stdout io.Writer) error {
	if !tc.channelValid(channel) {
		return usageErrorf("%s: unknown channel '%s' (expected %s)", tc.name, channel, strings.Join(tc.channelNames(), ", "))
	}
	root, err := defaultRoot()
	if err != nil {
		return err
	}
	ver, err := tc.dirs.readActive(root, channel)
	if err != nil {
		return err
	}
	if ver == "" {
		fmt.Fprintf(stdout, "%s: (not installed)\n", channel)
		return nil
	}
	bp := filepath.Join(binDir(root), binEntryName(tc.primaryBin(channel).link))
	fmt.Fprintf(stdout, "%s: %s\n  %s\n", channel, ver, bp)
	return nil
}

// ----------------------------------------------------------------------------
// bin wiring — POSIX symlink chain through the channel; Windows .cmd shim
// ----------------------------------------------------------------------------

func (tc *toolchain) installBin(root, channel string) error {
	bd := binDir(root)
	if err := os.MkdirAll(bd, 0o755); err != nil {
		return err
	}
	var active string
	if isWindows() {
		var err error
		active, err = tc.dirs.readActive(root, channel)
		if err != nil {
			return err
		}
		if active == "" {
			return fmt.Errorf("%s channel %q is not set", tc.name, channel)
		}
	}
	for _, spec := range tc.bins(channel) {
		linkPath := filepath.Join(bd, binEntryName(spec.link))
		if isWindows() {
			exeAbs := filepath.Join(tc.dirs.versionDir(root, active), spec.exe())
			if err := writeWindowsShim(linkPath, exeAbs); err != nil {
				return err
			}
			continue
		}
		// bin/<link> -> ../<name>/channels/<ch>/<exe>
		target := filepath.Join("..", tc.name, "channels", channel, spec.exe())
		if err := replaceSymlink(target, linkPath); err != nil {
			return err
		}
	}
	return nil
}

// ----------------------------------------------------------------------------
// status — one JSON shape across all toolchains
// ----------------------------------------------------------------------------

type toolChannelEntry struct {
	Version    string `json:"version"`
	BinCommand string `json:"command"`
	BinPath    string `json:"path"`
}

type toolStatus struct {
	Name        string                       `json:"name"`
	InstallRoot string                       `json:"install_root"`
	BinDir      string                       `json:"bin_dir"`
	Channels    map[string]*toolChannelEntry `json:"channels"`
	Installed   []string                     `json:"installed_versions"`
}

func collectToolStatus(tc *toolchain) (*toolStatus, error) {
	root, err := defaultRoot()
	if err != nil {
		return nil, err
	}
	s := &toolStatus{
		Name:        tc.name,
		InstallRoot: root,
		BinDir:      binDir(root),
		Channels:    map[string]*toolChannelEntry{},
	}
	for _, ch := range tc.channelNames() {
		s.Channels[ch] = nil
		ver, err := tc.dirs.readActive(root, ch)
		if err != nil {
			return nil, err
		}
		if ver == "" {
			continue
		}
		cmd := binEntryName(tc.primaryBin(ch).link)
		s.Channels[ch] = &toolChannelEntry{
			Version:    ver,
			BinCommand: cmd,
			BinPath:    filepath.Join(binDir(root), cmd),
		}
	}
	vs, err := listSubdirs(tc.dirs.versionsDir(root))
	if err != nil {
		return nil, err
	}
	s.Installed = vs
	return s, nil
}

func toolchainStatusCmd(tc *toolchain, args []string, stdout io.Writer) error {
	jsonMode := len(args) > 0 && (args[0] == "--json" || args[0] == "-j")
	s, err := collectToolStatus(tc)
	if err != nil {
		return err
	}
	if jsonMode {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(s)
	}
	renderToolStatus(tc, s, stdout)
	return nil
}

func renderToolStatus(tc *toolchain, s *toolStatus, stdout io.Writer) {
	fmt.Fprintf(stdout, "Active %s versions:\n", tc.name)
	for _, ch := range tc.channelNames() {
		label := tc.channelLabel(ch)
		if e := s.Channels[ch]; e != nil {
			fmt.Fprintf(stdout, "  %-13s  %-32s  %s\n", e.BinCommand, e.Version, label)
		} else {
			defCmd := binEntryName(tc.primaryBin(ch).link)
			fmt.Fprintf(stdout, "  %-13s  %-32s  %s\n", defCmd, "(not installed)", label)
		}
	}
	fmt.Fprintf(stdout, "\nInstall root: %s\n", s.InstallRoot)
	printInstalledVersions(stdout, s.Installed, true)
}

// ----------------------------------------------------------------------------
// shadow detection — surface a non-zvk copy of a managed command on PATH
// ----------------------------------------------------------------------------

// warnShadowedTools prints an advisory (never fatal) when a command zvk just
// linked is also provided elsewhere on PATH. zvk prepends its own bin, so the
// managed copy wins in fresh login shells, but a system copy can still surface
// in IDEs or non-login shells — so we make the overlap explicit and hand the
// user concrete cleanup commands. zvk never touches the system install itself.
func warnShadowedTools(tc *toolchain, root string, stdout io.Writer) {
	bd := binDir(root)
	for _, name := range tc.conflictCmds {
		other := lookPathExcluding(name, bd)
		if other == "" {
			continue
		}
		fmt.Fprintf(stdout, "[zvk] note: a non-zvk `%s` is also on PATH:\n", name)
		fmt.Fprintf(stdout, "        %s\n", other)
		fmt.Fprintf(stdout, "      zvk's bin is prepended so `%s` resolves to the managed copy in new shells,\n", name)
		fmt.Fprintln(stdout, "      but the system copy may still surface in IDEs or non-login shells. To remove it:")
		for _, line := range cleanupHints(name, other) {
			fmt.Fprintf(stdout, "        %s\n", line)
		}
	}
}

// lookPathExcluding finds `name` on PATH, skipping the directory `exclude`
// (zvk's own bin). Returns the absolute path of the first hit, or "".
func lookPathExcluding(name, exclude string) string {
	exe := name
	if isWindows() {
		exe = name + ".exe"
	}
	excludeClean := filepath.Clean(exclude)
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir == "" || filepath.Clean(dir) == excludeClean {
			continue
		}
		cand := filepath.Join(dir, exe)
		if fileExists(cand) {
			return cand
		}
	}
	return ""
}

// cleanupHints suggests how to remove a shadowing install, inferred from its
// path. The suggestions are advisory; zvk runs none of them.
func cleanupHints(name, otherPath string) []string {
	switch {
	case strings.Contains(otherPath, "/Cellar/") || strings.Contains(otherPath, "/homebrew/"):
		return []string{"brew uninstall " + name}
	case name == "go" && strings.HasPrefix(otherPath, "/usr/local/go/"):
		return []string{"sudo rm -rf /usr/local/go   # official tarball install"}
	case strings.HasPrefix(otherPath, "/usr/bin/") || strings.HasPrefix(otherPath, "/usr/lib/"):
		return []string{
			"sudo apt remove " + aptPkg(name) + "    # Debian/Ubuntu",
			"sudo dnf remove " + name + "    # Fedora/RHEL",
		}
	default:
		return []string{
			"remove " + otherPath + " (or its package),",
			"or drop the line that adds " + filepath.Dir(otherPath) + " to PATH from your shell rc.",
		}
	}
}

func aptPkg(name string) string {
	if name == "go" {
		return "golang-go"
	}
	return name
}
