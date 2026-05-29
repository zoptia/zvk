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

// zigTC is the Zig driver. Release picks the latest semver from the index;
// nightly tracks `master`. Tarballs are verified by sha256 then minisign.
var zigTC = &toolchain{
	name:           "zig",
	usage:          zigUsage,
	defaultChannel: zigDefaultChannel,
	archiveStrip:   1,
	dirs:           toolDirs{name: "zig"},
	channels: []channelInfo{
		{name: "release", label: "(release, default)"},
		{name: "nightly", label: "(nightly, opt-in)"},
	},
	isInstalled: func(versionDir string) bool {
		return fileExists(filepath.Join(versionDir, zigExeName()))
	},
	// bin/zig for release, bin/zig-nightly for nightly; the executable sits at
	// the version-dir root (lib/ is adjacent, so Zig finds it via the symlink).
	bins: func(channel string) []binSpec {
		link := "zig"
		if channel == "nightly" {
			link = "zig-nightly"
		}
		return []binSpec{{link: link, exe: zigExeName}}
	},
	parseInstall: func(args []string) (string, string, error) {
		channel := zigDefaultChannel
		for _, a := range args {
			if strings.HasPrefix(a, "--") {
				return "", "", usageErrorf("zig: unknown flag '%s'", a)
			}
			channel = a
		}
		return channel, "latest", nil
	},
	parseUse: func(args []string) (string, string, error) {
		if len(args) < 2 {
			return "", "", usageErrorf("usage: zvk zig use <channel> <version>")
		}
		return args[0], args[1], nil
	},
	resolve: resolveZigAsset,
}

func zigExeName() string {
	if isWindows() {
		return "zig.exe"
	}
	return "zig"
}

// resolveZigAsset fetches the Zig index and builds the download for a channel.
// The `requested` arg is ignored — Zig channels always resolve to their newest
// (latest release semver, or `master` for nightly).
func resolveZigAsset(channel, _ string) (toolAsset, error) {
	target, err := currentTarget()
	if err != nil {
		return toolAsset{}, err
	}
	indexBytes, err := downloadToMemory(zigIndexURL)
	if err != nil {
		return toolAsset{}, err
	}
	entry, err := resolveZigEntry(indexBytes, channel, target)
	if err != nil {
		return toolAsset{}, err
	}
	tarballURL := entry.Tarball
	return toolAsset{
		version:  entry.Version,
		url:      tarballURL,
		sha256:   entry.Shasum,
		filename: tarballURL,
		verify: func(tarball []byte, w io.Writer) error {
			if os.Getenv("ZVK_NO_MINISIGN") != "" {
				fmt.Fprintln(w, "[zvk zig] minisign verification skipped (ZVK_NO_MINISIGN set)")
				return nil
			}
			sigURL := tarballURL + ".minisig"
			fmt.Fprintf(w, "[zvk zig] fetching %s\n", sigURL)
			sig, err := downloadToMemory(sigURL)
			if err != nil {
				return err
			}
			if err := verifyMinisign(tarball, sig, zigMinisignPubkey); err != nil {
				return fmt.Errorf("minisign verification failed: %w", err)
			}
			fmt.Fprintln(w, "[zvk zig] minisign verified")
			return nil
		},
	}, nil
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
