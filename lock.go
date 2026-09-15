package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// lockToolchain serializes mutations across processes. The OS releases the lock
// on exit, including crashes. Keep the lock file so all contenders use one inode.
func lockToolchain(root, name string) (*os.File, error) {
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, ".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := lockFile(f); err != nil {
		f.Close()
		return nil, fmt.Errorf("%s: another operation may be in progress: %w", name, err)
	}
	return f, nil
}
