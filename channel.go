package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// toolDirs is the `<root>/<name>/{channels,versions}/...` layout shared by every
// managed toolchain. zig and go each instantiate one; everything tool-specific
// (bin shim layout, archive shape, channel set) stays in the cmd_*.go file.
type toolDirs struct{ name string }

func (d toolDirs) channelsDir(root string) string {
	return filepath.Join(root, d.name, "channels")
}

func (d toolDirs) versionsDir(root string) string {
	return filepath.Join(root, d.name, "versions")
}

func (d toolDirs) versionDir(root, version string) string {
	return filepath.Join(root, d.name, "versions", version)
}

func (d toolDirs) readActive(root, channel string) (string, error) {
	return readActiveVersion(d.channelsDir(root), channel)
}

func (d toolDirs) setActive(root, channel, version string) error {
	return setActiveVersion(d.channelsDir(root), channel, version)
}

// readActiveVersion returns the version a channel currently points at, or "" if unset.
// POSIX uses a directory symlink under `channelsDir/<channel>`; Windows uses
// `channelsDir/<channel>.txt` to avoid needing the symlink privilege.
func readActiveVersion(channelsDir, channel string) (string, error) {
	if isWindows() {
		path := filepath.Join(channelsDir, channel+".txt")
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				return "", nil
			}
			return "", err
		}
		return trimNewlines(string(data)), nil
	}
	link := filepath.Join(channelsDir, channel)
	target, err := os.Readlink(link)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return filepath.Base(target), nil
}

// setActiveVersion records `version` as the active version for `channel`.
// Callers must ensure `versions/<version>/` already exists alongside.
func setActiveVersion(channelsDir, channel, version string) error {
	if err := os.MkdirAll(channelsDir, 0o755); err != nil {
		return err
	}
	if isWindows() {
		path := filepath.Join(channelsDir, channel+".txt")
		return os.WriteFile(path, []byte(version), 0o644)
	}
	link := filepath.Join(channelsDir, channel)
	// `channels/<ch>` -> `../versions/<ver>` (a sibling under the same tool root).
	target := filepath.Join("..", "versions", version)
	return replaceSymlink(target, link)
}

// writeWindowsShim writes a `.cmd` wrapper at `linkPath` that exec's `exePath`
// with all forwarded args. Used on Windows where unprivileged users can't make
// symlinks, so `bin/<cmd>.cmd` redirects to the channel's actual binary.
func writeWindowsShim(linkPath, exePath string) error {
	wrapper := fmt.Sprintf("@\"%s\" %%*\r\n", exePath)
	_ = os.Remove(linkPath)
	return os.WriteFile(linkPath, []byte(wrapper), 0o644)
}
