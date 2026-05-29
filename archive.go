package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/ulikunitz/xz"
)

// extractArchive dispatches on the source URL extension. All formats are
// decoded in-process: zip via stdlib `archive/zip`, tar via stdlib `archive/tar`
// stacked on `compress/gzip` (.tar.gz) or `github.com/ulikunitz/xz` (.tar.xz).
func extractArchive(archive []byte, dest string, strip int, sourceURL string) error {
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	switch {
	case strings.HasSuffix(sourceURL, ".zip"):
		return extractZip(archive, dest, strip)
	case strings.HasSuffix(sourceURL, ".tar.xz"):
		xr, err := xz.NewReader(bytes.NewReader(archive))
		if err != nil {
			return fmt.Errorf("xz decode: %w", err)
		}
		return extractTar(xr, dest, strip)
	case strings.HasSuffix(sourceURL, ".tar.gz"), strings.HasSuffix(sourceURL, ".tgz"):
		gr, err := gzip.NewReader(bytes.NewReader(archive))
		if err != nil {
			return fmt.Errorf("gzip decode: %w", err)
		}
		defer gr.Close()
		return extractTar(gr, dest, strip)
	}
	return fmt.Errorf("unknown archive format: %s", sourceURL)
}

// extractTar extracts a tar stream to `dest`, emulating `tar --strip-components`.
// Handles regular files, directories, symlinks, and hardlinks; other entry types
// (devices, fifos, xattr metadata) are skipped silently — the toolchains we
// install never contain them.
func extractTar(r io.Reader, dest string, strip int) error {
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		name := stripComponents(hdr.Name, strip)
		if name == "" {
			continue
		}
		if hasParentSegment(name) {
			return fmt.Errorf("refusing tar entry with `..` segment: %q", hdr.Name)
		}
		target := filepath.Join(dest, name)
		mode := os.FileMode(hdr.Mode) & 0o777

		switch hdr.Typeflag {
		case tar.TypeDir:
			// Force u+rwx so we can still write children even if the archived
			// directory was read-only.
			if err := os.MkdirAll(target, mode|0o700); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if mode == 0 {
				mode = 0o644
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			if err := f.Close(); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			_ = os.Remove(target)
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return err
			}
		case tar.TypeLink:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			// Hardlink targets are archive-relative, so they need the same
			// strip applied.
			linkTarget := filepath.Join(dest, stripComponents(hdr.Linkname, strip))
			_ = os.Remove(target)
			if err := os.Link(linkTarget, target); err != nil {
				return err
			}
		}
	}
	return nil
}

// extractZip extracts a zip archive in pure Go, emulating tar's strip_components.
func extractZip(data []byte, dest string, strip int) error {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return err
	}
	for _, f := range zr.File {
		name := stripComponents(f.Name, strip)
		if name == "" {
			continue
		}
		if hasParentSegment(name) {
			return fmt.Errorf("refusing zip entry with `..` segment: %q", f.Name)
		}
		target := filepath.Join(dest, name)
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		mode := f.Mode().Perm()
		if mode == 0 {
			mode = 0o644
		}
		dst, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
		if err != nil {
			rc.Close()
			return err
		}
		if _, err := io.Copy(dst, rc); err != nil {
			rc.Close()
			dst.Close()
			return err
		}
		rc.Close()
		if err := dst.Close(); err != nil {
			return err
		}
	}
	return nil
}

// stripComponents removes the first `n` slash-separated path segments.
// Archive entries always use forward slashes regardless of platform.
func stripComponents(name string, n int) string {
	if n <= 0 {
		return name
	}
	name = strings.TrimPrefix(name, "./")
	parts := strings.Split(name, "/")
	if len(parts) <= n {
		return ""
	}
	return strings.Join(parts[n:], "/")
}

// hasParentSegment reports whether `name` contains a `..` path segment,
// which would let an archive entry escape its destination directory.
// A substring check would falsely reject legitimate names like "foo..bar".
func hasParentSegment(name string) bool {
	for _, seg := range strings.Split(name, "/") {
		if seg == ".." {
			return true
		}
	}
	return false
}
