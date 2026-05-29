package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	goIndexURLStable = "https://go.dev/dl/?mode=json"
	goIndexURLAll    = "https://go.dev/dl/?mode=json&include=all"
	goDLBase         = "https://go.dev/dl/"
	goChannel        = "stable"
)

const goUsage = `Usage:
  zvk go install [<version>|latest]    Install Go (default: latest stable)
  zvk go update                         Refresh stable channel to latest
  zvk go use <version>                  Point stable at an installed version
  zvk go uninstall <version>            Remove an installed version
  zvk go list [--remote]                List installed (or remote) versions
  zvk go which                          Show active version
  zvk go status [--json]                Print state (text or JSON)

<version> may be given as "go1.23.4" or "1.23.4".
`

func runGo(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(stdout, goUsage)
		return nil
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "install":
		return runGoInstall(rest, stdout)
	case "update":
		return runGoInstall([]string{"latest"}, stdout)
	case "use":
		return runGoUse(rest, stdout)
	case "uninstall", "remove", "rm":
		return runGoUninstall(rest, stdout)
	case "list", "ls":
		return runGoList(rest, stdout)
	case "which":
		return runGoWhich(stdout)
	case "status", "info":
		return runGoStatus(rest, stdout)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, goUsage)
		return nil
	default:
		return usageErrorf("go: unknown subcommand '%s'\n\n%s", sub, goUsage)
	}
}

// ============================================================================
// Paths
// ============================================================================

func goExeName() string {
	if isWindows() {
		return "go.exe"
	}
	return "go"
}

func gofmtExeName() string {
	if isWindows() {
		return "gofmt.exe"
	}
	return "gofmt"
}

func goBinCmd() string {
	if isWindows() {
		return "go.cmd"
	}
	return "go"
}

func gofmtBinCmd() string {
	if isWindows() {
		return "gofmt.cmd"
	}
	return "gofmt"
}

var goDirs = toolDirs{name: "go"}

func goIsInstalled(versionDir string) bool {
	return fileExists(filepath.Join(versionDir, "bin", goExeName()))
}

func goReadActive(root string) (string, error)    { return goDirs.readActive(root, goChannel) }
func goSetActive(root, version string) error      { return goDirs.setActive(root, goChannel, version) }
func goVersionDir(root, version string) string    { return goDirs.versionDir(root, version) }

// normalizeGoVersion accepts "1.23.4" / "go1.23.4" and returns "go1.23.4".
// "latest" / "stable" are passed through verbatim (resolved against the index).
func normalizeGoVersion(raw string) string {
	if raw == "" || raw == "latest" || raw == "stable" {
		return raw
	}
	if strings.HasPrefix(raw, "go") {
		return raw
	}
	return "go" + raw
}

// goInstallBins places both `bin/go` and `bin/gofmt`.
func goInstallBins(root string) error {
	bd := binDir(root)
	if err := os.MkdirAll(bd, 0o755); err != nil {
		return err
	}
	var active string
	if isWindows() {
		var err error
		active, err = goReadActive(root)
		if err != nil {
			return err
		}
		if active == "" {
			return fmt.Errorf("go channel is not set")
		}
	}
	for _, pair := range [][2]string{
		{goBinCmd(), goExeName()},
		{gofmtBinCmd(), gofmtExeName()},
	} {
		linkName, exeBase := pair[0], pair[1]
		linkPath := filepath.Join(bd, linkName)
		if isWindows() {
			exePath := filepath.Join(goVersionDir(root, active), "bin", exeBase)
			if err := writeWindowsShim(linkPath, exePath); err != nil {
				return err
			}
			continue
		}
		// bin/go -> ../go/channels/stable/bin/go
		target := filepath.Join("..", "go", "channels", goChannel, "bin", exeBase)
		if err := replaceSymlink(target, linkPath); err != nil {
			return err
		}
	}
	return nil
}

// ============================================================================
// Index
// ============================================================================

type goFile struct {
	Filename string `json:"filename"`
	OS       string `json:"os"`
	Arch     string `json:"arch"`
	Version  string `json:"version"`
	Sha256   string `json:"sha256"`
	Kind     string `json:"kind"`
}

type goRelease struct {
	Version string   `json:"version"`
	Stable  bool     `json:"stable"`
	Files   []goFile `json:"files"`
}

type goAsset struct {
	Version  string
	Filename string
	Sha256   string
}

func resolveGoAsset(indexJSON []byte, requested, osStr, arch string) (goAsset, error) {
	var releases []goRelease
	if err := json.Unmarshal(indexJSON, &releases); err != nil {
		return goAsset{}, err
	}
	wantLatest := requested == "latest" || requested == "stable"
	for _, rel := range releases {
		if wantLatest {
			if !rel.Stable {
				continue
			}
		} else if rel.Version != requested {
			continue
		}
		for _, f := range rel.Files {
			if f.Kind != "archive" || f.OS != osStr || f.Arch != arch {
				continue
			}
			return goAsset{Version: rel.Version, Filename: f.Filename, Sha256: f.Sha256}, nil
		}
		if !wantLatest {
			return goAsset{}, fmt.Errorf("no archive for %s on %s-%s", requested, osStr, arch)
		}
	}
	return goAsset{}, fmt.Errorf("release not found: %s", requested)
}

// ============================================================================
// install
// ============================================================================

func runGoInstall(args []string, stdout io.Writer) error {
	raw := "latest"
	for _, a := range args {
		if strings.HasPrefix(a, "--") {
			return usageErrorf("go: unknown flag '%s'", a)
		}
		raw = a
	}
	requested := normalizeGoVersion(raw)

	osStr := runtime.GOOS
	arch := runtime.GOARCH
	root, err := defaultRoot()
	if err != nil {
		return err
	}

	fmt.Fprintf(stdout, "[zvk go] target: %s-%s\n", osStr, arch)
	fmt.Fprintf(stdout, "[zvk go] install root: %s\n", root)

	url := goIndexURLStable
	if requested != "latest" && requested != "stable" {
		url = goIndexURLAll
	}
	fmt.Fprintf(stdout, "[zvk go] fetching index from %s\n", url)
	indexBytes, err := downloadToMemory(url)
	if err != nil {
		return err
	}
	asset, err := resolveGoAsset(indexBytes, requested, osStr, arch)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "[zvk go] version: %s\n", asset.Version)

	versionDir := goVersionDir(root, asset.Version)
	if goIsInstalled(versionDir) {
		fmt.Fprintf(stdout, "[zvk go] %s already installed at %s\n", asset.Version, versionDir)
	} else {
		fullURL := goDLBase + asset.Filename
		fmt.Fprintf(stdout, "[zvk go] tarball: %s\n", fullURL)
		fmt.Fprintln(stdout, "[zvk go] downloading...")
		tarball, err := downloadToMemory(fullURL)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "[zvk go] downloaded %d bytes\n", len(tarball))

		actual := sha256Hex(tarball)
		if actual != asset.Sha256 {
			return fmt.Errorf("sha256 mismatch\n  expected: %s\n  actual:   %s", asset.Sha256, actual)
		}
		fmt.Fprintln(stdout, "[zvk go] sha256 verified")

		fmt.Fprintf(stdout, "[zvk go] extracting to %s\n", versionDir)
		// Go archives wrap everything in a top-level `go/` directory.
		if err := extractArchive(tarball, versionDir, 1, asset.Filename); err != nil {
			return err
		}
	}

	if err := goSetActive(root, asset.Version); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "[zvk go] channel '%s' -> %s\n", goChannel, asset.Version)

	if err := goInstallBins(root); err != nil {
		return err
	}
	bd := binDir(root)
	fmt.Fprintf(stdout, "[zvk go] %s/%s ready\n", bd, goBinCmd())
	fmt.Fprintf(stdout, "[zvk go] %s/%s ready\n", bd, gofmtBinCmd())

	if err := setupPath(bd, stdout); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "[zvk go] done")
	return nil
}

func runGoUse(args []string, stdout io.Writer) error {
	if len(args) < 1 {
		return usageErrorf("usage: zvk go use <version>")
	}
	version := normalizeGoVersion(args[0])
	root, err := defaultRoot()
	if err != nil {
		return err
	}
	if !goIsInstalled(goVersionDir(root, version)) {
		return fmt.Errorf("go: version '%s' is not installed (run: zvk go install %s)", version, version)
	}
	if err := goSetActive(root, version); err != nil {
		return err
	}
	if err := goInstallBins(root); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "channel '%s' -> %s\n", goChannel, version)
	return nil
}

func runGoUninstall(args []string, stdout io.Writer) error {
	if len(args) < 1 {
		return usageErrorf("usage: zvk go uninstall <version>")
	}
	version := normalizeGoVersion(args[0])
	root, err := defaultRoot()
	if err != nil {
		return err
	}
	versionDir := goVersionDir(root, version)
	if !goIsInstalled(versionDir) {
		return fmt.Errorf("go: version '%s' is not installed", version)
	}
	active, err := goReadActive(root)
	if err != nil {
		return err
	}
	if active == version {
		return fmt.Errorf("go: '%s' is the active version for channel '%s'; switch first with `zvk go use <other>`", version, goChannel)
	}
	if err := os.RemoveAll(versionDir); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "removed %s\n", version)
	return nil
}

func runGoList(args []string, stdout io.Writer) error {
	remote := false
	for _, a := range args {
		if a == "--remote" || a == "-r" {
			remote = true
		}
	}
	if remote {
		fmt.Fprintln(stdout, "Fetching remote release index...")
		data, err := downloadToMemory(goIndexURLAll)
		if err != nil {
			return err
		}
		var releases []goRelease
		if err := json.Unmarshal(data, &releases); err != nil {
			return err
		}
		for _, r := range releases {
			tag := ""
			if r.Stable {
				tag = "  (stable)"
			}
			fmt.Fprintf(stdout, "  %s%s\n", r.Version, tag)
		}
		return nil
	}

	root, err := defaultRoot()
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, "Channel:")
	active, err := goReadActive(root)
	if err != nil {
		return err
	}
	printChannelMapping(stdout, goChannel, active)
	fmt.Fprintln(stdout)
	vs, err := listSubdirs(goDirs.versionsDir(root))
	if err != nil {
		return err
	}
	printInstalledVersions(stdout, vs, false)
	return nil
}

func runGoWhich(stdout io.Writer) error {
	root, err := defaultRoot()
	if err != nil {
		return err
	}
	active, err := goReadActive(root)
	if err != nil {
		return err
	}
	if active == "" {
		fmt.Fprintf(stdout, "%s: (not installed)\n", goChannel)
		return nil
	}
	bp := filepath.Join(root, "bin", goBinCmd())
	fmt.Fprintf(stdout, "%s: %s\n  %s\n", goChannel, active, bp)
	return nil
}

// ============================================================================
// status
// ============================================================================

type goChannelEntry struct {
	Version    string `json:"version"`
	BinCommand string `json:"command"`
	BinPath    string `json:"path"`
}

type goStatus struct {
	InstallRoot string                     `json:"install_root"`
	BinDir      string                     `json:"bin_dir"`
	Channels    map[string]*goChannelEntry `json:"channels"`
	Installed   []string                   `json:"installed_versions"`
}

func collectGoStatus() (*goStatus, error) {
	root, err := defaultRoot()
	if err != nil {
		return nil, err
	}
	s := &goStatus{
		InstallRoot: root,
		BinDir:      binDir(root),
		Channels:    map[string]*goChannelEntry{goChannel: nil},
	}
	if active, err := goReadActive(root); err == nil && active != "" {
		s.Channels[goChannel] = &goChannelEntry{
			Version:    active,
			BinCommand: goBinCmd(),
			BinPath:    filepath.Join(root, "bin", goBinCmd()),
		}
	} else if err != nil {
		return nil, err
	}
	vs, err := listSubdirs(goDirs.versionsDir(root))
	if err != nil {
		return nil, err
	}
	s.Installed = vs
	return s, nil
}

func runGoStatus(args []string, stdout io.Writer) error {
	jsonMode := len(args) > 0 && (args[0] == "--json" || args[0] == "-j")
	s, err := collectGoStatus()
	if err != nil {
		return err
	}
	if jsonMode {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(s)
	}
	fmt.Fprintln(stdout, "Active Go version:")
	if e := s.Channels[goChannel]; e != nil {
		fmt.Fprintf(stdout, "  %-13s  %-32s  (stable)\n", e.BinCommand, e.Version)
	} else {
		fmt.Fprintf(stdout, "  %-13s  (not installed)                   (stable)\n", "go")
	}
	fmt.Fprintf(stdout, "\nInstall root: %s\n", s.InstallRoot)
	printInstalledVersions(stdout, s.Installed, true)
	return nil
}
