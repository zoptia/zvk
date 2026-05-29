package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func runCombinedStatus(args []string, stdout io.Writer) error {
	jsonMode := len(args) > 0 && (args[0] == "--json" || args[0] == "-j")
	if jsonMode {
		zs, err := collectZigStatus()
		if err != nil {
			return err
		}
		gs, err := collectGoStatus()
		if err != nil {
			return err
		}
		combined := map[string]any{
			"zig": zs,
			"go":  gs,
		}
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(combined)
	}
	fmt.Fprintln(stdout, "=== zig ===")
	if err := runZigStatus(nil, stdout); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "\n=== go ===")
	return runGoStatus(nil, stdout)
}

// ============================================================================
// self-install
// ============================================================================

func runSelfInstall(stdout io.Writer) error {
	currentExe, err := os.Executable()
	if err != nil {
		return err
	}
	currentExe, err = filepath.Abs(currentExe)
	if err != nil {
		return err
	}
	root, err := defaultRoot()
	if err != nil {
		return err
	}
	bd := binDir(root)
	target := filepath.Join(bd, zvkBinaryName())

	if currentExe == target {
		fmt.Fprintf(stdout, "[zvk] already installed at %s\n", target)
	} else {
		if err := os.MkdirAll(bd, 0o755); err != nil {
			return err
		}
		if err := copyFile(currentExe, target, 0o755); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "[zvk] installed to %s\n", target)
	}

	if err := setupPath(bd, stdout); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "[zvk] done")
	return nil
}

func copyFile(src, dst string, mode os.FileMode) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return writeFileAtomic(dst, data, mode)
}

// ============================================================================
// self-update — rebuild from source via `go install <module>@<version>`.
//
// Unlike a download-and-replace updater, this compiles the binary locally, so
// on Windows it carries no Mark-of-the-Web (the usual SmartScreen trigger), and
// the module sources are hash-verified through GOSUMDB. It needs a `go`
// toolchain: the one zvk manages under <root>/bin is preferred, else PATH.
// ============================================================================

func runSelfUpdate(args []string, stdout io.Writer) error {
	dryRun := false
	version := "latest"
	for _, a := range args {
		switch {
		case a == "--dry-run":
			dryRun = true
		case a == "--force" || a == "-f":
			// Retained for compatibility; `go install` always rebuilds.
		case strings.HasPrefix(a, "--version="):
			version = strings.TrimPrefix(a, "--version=")
		default:
			return usageErrorf("self-update: unknown argument '%s'", a)
		}
	}

	root, err := defaultRoot()
	if err != nil {
		return err
	}
	bd := binDir(root)
	target := filepath.Join(bd, zvkBinaryName())

	goBin, err := findGoBinary(root)
	if err != nil {
		return err
	}
	spec := modulePath + "@" + version

	fmt.Fprintf(stdout, "[zvk] current %s; building %s with %s\n", zvkVersion, spec, goBin)
	if dryRun {
		fmt.Fprintf(stdout, "[zvk] dry-run: would `go install %s` then install to %s\n", spec, target)
		return nil
	}

	// Build into an isolated GOBIN, then atomically move into place. Building to
	// a temp dir first keeps a failed build from clobbering the live binary, and
	// sidesteps the Windows "cannot overwrite a running .exe" file lock.
	tmp, err := os.MkdirTemp("", "zvk-update-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	cmd := exec.Command(goBin, "install", spec)
	cmd.Env = append(os.Environ(), "GOBIN="+tmp)
	cmd.Stdout = stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("go install %s: %w", spec, err)
	}

	built := filepath.Join(tmp, zvkBinaryName())
	data, err := os.ReadFile(built)
	if err != nil {
		return fmt.Errorf("built binary not found under GOBIN: %w", err)
	}
	if err := os.MkdirAll(bd, 0o755); err != nil {
		return err
	}
	if err := writeFileAtomic(target, data, 0o755); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "[zvk] updated %s\n", target)
	return nil
}

// findGoBinary locates a `go` executable for self-update: the toolchain zvk
// manages under <root>/bin first, then PATH. On Windows the managed entry is a
// .cmd shim that is awkward to exec directly, so PATH is used there.
func findGoBinary(root string) (string, error) {
	if !isWindows() {
		managed := filepath.Join(binDir(root), "go")
		if _, err := os.Stat(managed); err == nil {
			return managed, nil
		}
	}
	if p, err := exec.LookPath("go"); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("no `go` toolchain found; install Go or run `zvk go install` first")
}
