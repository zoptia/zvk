package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

func rejectVersionSymlink(dir string) error {
	info, err := os.Lstat(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("version path is not a real directory: %s", dir)
	}
	return nil
}

func (tc *toolchain) complete(dir, channel string) bool {
	if !tc.isInstalled(dir) {
		return false
	}
	for _, spec := range tc.bins(channel) {
		if !fileExists(filepath.Join(dir, spec.exe())) {
			return false
		}
	}
	return true
}

// installArchive publishes only a fully extracted and validated directory.
// Callers hold the toolchain lock. Old incomplete installs are retained until
// the replacement is ready and restored if publishing fails.
func (tc *toolchain) installArchive(root, channel string, asset toolAsset, data []byte) error {
	stage, err := os.MkdirTemp(filepath.Join(root, tc.name), ".install-*")
	if err != nil {
		return err
	}
	defer func() {
		if stage != "" {
			os.RemoveAll(stage)
		}
	}()
	payload := filepath.Join(stage, "payload")
	if err := extractArchive(data, payload, tc.archiveStrip, asset.filename); err != nil {
		return err
	}
	if !tc.complete(payload, channel) {
		return fmt.Errorf("%s: archive is missing required toolchain files", tc.name)
	}
	if err := os.MkdirAll(tc.dirs.versionsDir(root), 0o755); err != nil {
		return err
	}
	target := tc.dirs.versionDir(root, asset.version)
	backup := filepath.Join(stage, "previous")
	hadPrevious := pathExists(target)
	if hadPrevious {
		if err := os.Rename(target, backup); err != nil {
			return err
		}
	}
	if err := os.Rename(payload, target); err != nil {
		if hadPrevious {
			if restoreErr := os.Rename(backup, target); restoreErr != nil {
				// Keep the only surviving old copy for manual recovery.
				stage = ""
				return errors.Join(err, fmt.Errorf("restore failed; previous install remains at %s: %w", backup, restoreErr))
			}
		}
		return err
	}
	return nil
}

type entrySnapshot struct {
	path   string
	mode   os.FileMode
	data   []byte
	link   string
	exists bool
}

func snapshotEntry(path string) (entrySnapshot, error) {
	s := entrySnapshot{path: path}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	s.exists, s.mode = true, info.Mode()
	switch {
	case s.mode&os.ModeSymlink != 0:
		s.link, err = os.Readlink(path)
	case s.mode.IsRegular():
		s.data, err = os.ReadFile(path)
	default:
		err = fmt.Errorf("refusing to replace non-file entry: %s", path)
	}
	return s, err
}

func (s entrySnapshot) restore() error {
	if !s.exists {
		err := os.Remove(s.path)
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if s.mode&os.ModeSymlink != 0 {
		return replaceSymlink(s.link, s.path)
	}
	return writeFileAtomic(s.path, s.data, s.mode.Perm())
}

// activate preflights every affected entry and rolls back ordinary write errors.
// On POSIX the final channel rename is the single version-switch commit point.
// Windows shims are separate files; rollback preserves the previous mapping on
// errors, but the group is not crash-atomic.
func (tc *toolchain) activate(root, channel, version string) (err error) {
	channelPath := filepath.Join(tc.dirs.channelsDir(root), channel)
	if isWindows() {
		channelPath += ".txt"
	}
	paths := []string{channelPath}
	for _, spec := range tc.bins(channel) {
		paths = append(paths, filepath.Join(binDir(root), binEntryName(spec.link)))
	}
	var snapshots []entrySnapshot
	for _, path := range paths {
		s, e := snapshotEntry(path)
		if e != nil {
			return e
		}
		snapshots = append(snapshots, s)
	}
	defer func() {
		if err == nil {
			return
		}
		for i := len(snapshots) - 1; i >= 0; i-- {
			if e := snapshots[i].restore(); e != nil {
				err = errors.Join(err, fmt.Errorf("rollback %s: %w", snapshots[i].path, e))
			}
		}
	}()
	if isWindows() {
		if err = tc.dirs.setActive(root, channel, version); err != nil {
			return err
		}
		return tc.installBin(root, channel)
	}
	if err = tc.installBin(root, channel); err != nil {
		return err
	}
	return tc.dirs.setActive(root, channel, version)
}
