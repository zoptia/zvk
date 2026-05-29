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

// goTC is the Go driver. Go has a single `stable` channel; `install <version>`
// selects a specific release. Go is also zvk's own build dependency, so a
// non-zvk `go` on PATH is flagged as a potential shadow.
var goTC = &toolchain{
	name:           "go",
	usage:          goUsage,
	defaultChannel: goChannel,
	archiveStrip:   1, // Go archives wrap everything in a top-level `go/`.
	conflictCmds:   []string{"go"},
	dirs:           toolDirs{name: "go"},
	channels: []channelInfo{
		{name: "stable", label: "(stable)"},
	},
	normalizeVersion: normalizeGoVersion,
	isInstalled: func(versionDir string) bool {
		return fileExists(filepath.Join(versionDir, "bin", goExeName()))
	},
	bins: func(channel string) []binSpec {
		return []binSpec{
			{link: "go", exe: func() string { return filepath.Join("bin", goExeName()) }},
			{link: "gofmt", exe: func() string { return filepath.Join("bin", gofmtExeName()) }},
		}
	},
	parseInstall: func(args []string) (string, string, error) {
		raw := "latest"
		for _, a := range args {
			if strings.HasPrefix(a, "--") {
				return "", "", usageErrorf("go: unknown flag '%s'", a)
			}
			raw = a
		}
		return goChannel, raw, nil
	},
	parseUse: func(args []string) (string, string, error) {
		if len(args) < 1 {
			return "", "", usageErrorf("usage: zvk go use <version>")
		}
		return goChannel, args[0], nil
	},
	resolve:    resolveGoAssetForInstall,
	listRemote: goListRemote,
}

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

func resolveGoAssetForInstall(channel, requested string) (toolAsset, error) {
	osStr := runtime.GOOS
	arch := runtime.GOARCH
	url := goIndexURLStable
	if requested != "latest" && requested != "stable" {
		url = goIndexURLAll
	}
	indexBytes, err := downloadToMemory(url)
	if err != nil {
		return toolAsset{}, err
	}
	asset, err := resolveGoAsset(indexBytes, requested, osStr, arch)
	if err != nil {
		return toolAsset{}, err
	}
	return toolAsset{
		version:  asset.Version,
		url:      goDLBase + asset.Filename,
		sha256:   asset.Sha256,
		filename: asset.Filename,
	}, nil
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

func goListRemote(stdout io.Writer) error {
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
