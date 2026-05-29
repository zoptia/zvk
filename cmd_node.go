package main

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	nodeIndexURL = "https://nodejs.org/dist/index.json"
	nodeDistURL  = "https://nodejs.org/dist/"

	// Node has one active pointer (like go's `stable`), not parallel channels:
	// node/npm/npx are single commands, so two channels would fight over bin/node.
	// `lts` / `current` are install-time selectors, not persistent channels.
	nodeChannel = "default"
)

const nodeUsage = `Usage:
  zvk node install [lts|current|<version>]  Install Node.js (default: latest LTS)
  zvk node update                            Update active Node to latest current
  zvk node use <version>                     Point active at an installed version
  zvk node uninstall <version>              Remove an installed version
  zvk node list [--remote]                  List installed (or remote) versions
  zvk node which                            Show active version
  zvk node status [--json]                  Print state (text or JSON)

<version> may be given as "v20.11.0" or "20.11.0".
`

// nodeTC is the Node.js driver. A single active pointer holds the chosen
// version; `install lts` / `install current` select which line's newest to
// fetch. Tarballs are verified against the per-release SHASUMS256.txt. On
// Windows node.exe lives at the archive root, not under bin/.
var nodeTC = &toolchain{
	name:           "node",
	usage:          nodeUsage,
	defaultChannel: nodeChannel,
	archiveStrip:   1, // archives wrap everything in node-v<ver>-<os>-<arch>/.
	conflictCmds:   []string{"node"},
	dirs:           toolDirs{name: "node"},
	channels: []channelInfo{
		{name: nodeChannel, label: "(active)"},
	},
	normalizeVersion: normalizeNodeVersion,
	isInstalled: func(versionDir string) bool {
		return fileExists(filepath.Join(versionDir, nodeExeRel()))
	},
	bins: func(channel string) []binSpec {
		if isWindows() {
			return []binSpec{
				{link: "node", exe: func() string { return "node.exe" }},
				{link: "npm", exe: func() string { return "npm.cmd" }},
				{link: "npx", exe: func() string { return "npx.cmd" }},
			}
		}
		return []binSpec{
			{link: "node", exe: func() string { return filepath.Join("bin", "node") }},
			{link: "npm", exe: func() string { return filepath.Join("bin", "npm") }},
			{link: "npx", exe: func() string { return filepath.Join("bin", "npx") }},
		}
	},
	parseInstall: func(args []string) (string, string, error) {
		requested := "lts" // default: latest LTS
		for _, a := range args {
			if strings.HasPrefix(a, "--") {
				return "", "", usageErrorf("node: unknown flag '%s'", a)
			}
			requested = a // "lts" / "current" / a concrete version
		}
		return nodeChannel, requested, nil
	},
	parseUse: func(args []string) (string, string, error) {
		if len(args) < 1 {
			return "", "", usageErrorf("usage: zvk node use <version>")
		}
		return nodeChannel, args[0], nil
	},
	resolve:    resolveNodeAsset,
	listRemote: nodeListRemote,
}

// nodeExeRel is the node executable path relative to a version dir: under bin/
// on POSIX, at the archive root on Windows.
func nodeExeRel() string {
	if isWindows() {
		return "node.exe"
	}
	return filepath.Join("bin", "node")
}

// normalizeNodeVersion accepts "20.11.0" / "v20.11.0" and returns "v20.11.0".
// The selectors "lts"/"current"/"latest" pass through verbatim.
func normalizeNodeVersion(raw string) string {
	switch raw {
	case "", "latest", "lts", "current":
		return raw
	}
	if strings.HasPrefix(raw, "v") {
		return raw
	}
	return "v" + raw
}

// nodeOSArch maps the platform to Node's download os-arch token, e.g.
// "linux-x64", "darwin-arm64", "win-x64".
func nodeOSArch() (string, error) {
	var osPart string
	switch runtime.GOOS {
	case "linux":
		osPart = "linux"
	case "darwin":
		osPart = "darwin"
	case "windows":
		osPart = "win"
	default:
		return "", fmt.Errorf("unsupported os: %s", runtime.GOOS)
	}
	var arch string
	switch runtime.GOARCH {
	case "amd64":
		arch = "x64"
	case "arm64":
		arch = "arm64"
	default:
		return "", fmt.Errorf("unsupported arch: %s", runtime.GOARCH)
	}
	return osPart + "-" + arch, nil
}

// ============================================================================
// Index
// ============================================================================

type nodeRelease struct {
	Version string          `json:"version"` // "v20.11.0"
	LTS     json.RawMessage `json:"lts"`     // false, or a codename string
}

// isLTS reports whether this release carries an LTS codename (lts != false).
func (r nodeRelease) isLTS() bool {
	return len(r.LTS) > 0 && string(r.LTS) != "false"
}

func resolveNodeAsset(_, requested string) (toolAsset, error) {
	osArch, err := nodeOSArch()
	if err != nil {
		return toolAsset{}, err
	}
	data, err := downloadToMemory(nodeIndexURL)
	if err != nil {
		return toolAsset{}, err
	}
	var releases []nodeRelease
	if err := json.Unmarshal(data, &releases); err != nil {
		return toolAsset{}, err
	}

	version, err := pickNodeVersion(releases, requested)
	if err != nil {
		return toolAsset{}, err
	}

	ext := ".tar.gz"
	if isWindows() {
		ext = ".zip"
	}
	filename := "node-" + version + "-" + osArch + ext
	base := nodeDistURL + version + "/"

	shaText, err := downloadToMemory(base + "SHASUMS256.txt")
	if err != nil {
		return toolAsset{}, err
	}
	sha, err := nodeShaFor(shaText, filename)
	if err != nil {
		return toolAsset{}, err
	}
	return toolAsset{
		version:  version,
		url:      base + filename,
		sha256:   sha,
		filename: filename,
	}, nil
}

// pickNodeVersion resolves the concrete version string from a selector: "lts"
// is the newest LTS release, "current"/"latest"/"" the newest release overall
// (the index is newest-first), anything else an exact version match.
func pickNodeVersion(releases []nodeRelease, requested string) (string, error) {
	switch requested {
	case "lts":
		for _, r := range releases {
			if r.isLTS() {
				return r.Version, nil
			}
		}
		return "", fmt.Errorf("node: no LTS release found in index")
	case "current", "latest", "":
		if len(releases) == 0 {
			return "", fmt.Errorf("node: empty release index")
		}
		return releases[0].Version, nil
	default:
		for _, r := range releases {
			if r.Version == requested {
				return r.Version, nil
			}
		}
		return "", fmt.Errorf("node: version %q not found in index", requested)
	}
}

// nodeShaFor extracts the sha256 for `filename` from a SHASUMS256.txt body
// (lines of "<hex>  <filename>").
func nodeShaFor(shaText []byte, filename string) (string, error) {
	for _, line := range strings.Split(string(shaText), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == filename {
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("sha256 for %s not found in SHASUMS256.txt", filename)
}

func nodeListRemote(stdout io.Writer) error {
	fmt.Fprintln(stdout, "Fetching remote release index...")
	data, err := downloadToMemory(nodeIndexURL)
	if err != nil {
		return err
	}
	var releases []nodeRelease
	if err := json.Unmarshal(data, &releases); err != nil {
		return err
	}
	for _, r := range releases {
		tag := ""
		if r.isLTS() {
			tag = "  (lts)"
		}
		fmt.Fprintf(stdout, "  %s%s\n", r.Version, tag)
	}
	return nil
}
