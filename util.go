package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// zvkVersion is the user-facing release version; bumped per GitHub Release.
const zvkVersion = "0.6.0"

// usageError signals a CLI misuse (unknown subcommand, missing arg, bad flag).
// `main` recognises it and exits with status 2; all other errors exit with 1.
type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

func usageErrorf(format string, args ...any) error {
	return &usageError{msg: fmt.Sprintf(format, args...)}
}

// modulePath is the Go module path. self-update rebuilds it from source via
// `go install <modulePath>@<version>` rather than downloading a release asset.
const modulePath = "github.com/zoptia/zvk"

// zvkBinaryName returns the OS-appropriate filename for the manager binary.
func zvkBinaryName() string {
	if runtime.GOOS == "windows" {
		return "zvk.exe"
	}
	return "zvk"
}

// defaultRoot resolves the install root. `ZVK_ROOT` wins; otherwise `~/.zvk/`.
func defaultRoot() (string, error) {
	if r := os.Getenv("ZVK_ROOT"); r != "" {
		return r, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("no home directory: %w", err)
	}
	return filepath.Join(home, ".zvk"), nil
}

// binDir is the shared `<root>/bin` directory used by all toolchains.
func binDir(root string) string {
	return filepath.Join(root, "bin")
}

// currentTarget returns the Zig-style target triple for this platform.
func currentTarget() (string, error) {
	var arch string
	switch runtime.GOARCH {
	case "amd64":
		arch = "x86_64"
	case "arm64":
		arch = "aarch64"
	default:
		return "", fmt.Errorf("unsupported arch: %s", runtime.GOARCH)
	}
	var osPart string
	switch runtime.GOOS {
	case "linux", "darwin", "windows":
		osPart = runtime.GOOS
		if osPart == "darwin" {
			osPart = "macos"
		}
	default:
		return "", fmt.Errorf("unsupported os: %s", runtime.GOOS)
	}
	return arch + "-" + osPart, nil
}

// pathExists is true iff `p` can be stat'd (file, directory, or dangling symlink).
func pathExists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// fileExists is true iff `p` exists AND is a regular file (follows symlinks).
func fileExists(p string) bool {
	info, err := os.Stat(p)
	if err != nil {
		return false
	}
	return info.Mode().IsRegular()
}

// listSubdirs returns directory entries inside `dir` (basename only, sorted).
// Missing directory → empty list, not an error.
func listSubdirs(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}

// listFiles returns regular file entries inside `dir` (basename only, sorted).
// Missing directory → empty list, not an error.
func listFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.Type().IsRegular() {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}

// replaceSymlink atomically replaces `link` with a symlink pointing at `target`.
// `target` is used verbatim (usually relative).
func replaceSymlink(target, link string) error {
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		return err
	}
	// RemoveAll handles both file/symlink and the rare case where `link` is a
	// non-empty directory left behind by an earlier broken install.
	if err := os.RemoveAll(link); err != nil {
		return err
	}
	return os.Symlink(target, link)
}

// writeFileAtomic writes `data` to `path` via a temp file + rename. Permissions
// are set on the temp file before rename so the final file is created atomically
// with the requested mode.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".zvk-tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op if rename succeeded

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// trimNewlines strips trailing CR/LF characters.
func trimNewlines(s string) string { return strings.TrimRight(s, "\r\n\t ") }

// isWindows reports whether the current platform is Windows.
func isWindows() bool { return runtime.GOOS == "windows" }

// isMacOS reports whether the current platform is macOS.
func isMacOS() bool { return runtime.GOOS == "darwin" }

// printChannelMapping writes one "  <channel>  -> <version>" row, or
// "  <channel>  (not set)" when the channel has never been pointed at a version.
func printChannelMapping(w io.Writer, channel, version string) {
	if version == "" {
		fmt.Fprintf(w, "  %-8s (not set)\n", channel)
	} else {
		fmt.Fprintf(w, "  %-8s -> %s\n", channel, version)
	}
}

// printInstalledVersions writes "Installed versions[: | (N):]" followed by one
// version per line, or "(none)" when empty. Set `withCount` to include the
// running total in the header (status output uses this; list output doesn't).
func printInstalledVersions(w io.Writer, versions []string, withCount bool) {
	if withCount {
		fmt.Fprintf(w, "Installed versions (%d):\n", len(versions))
	} else {
		fmt.Fprintln(w, "Installed versions:")
	}
	if len(versions) == 0 {
		fmt.Fprintln(w, "  (none)")
		return
	}
	for _, v := range versions {
		fmt.Fprintf(w, "  %s\n", v)
	}
}

// sha256Hex returns the lowercase hex digest of `data`.
func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
