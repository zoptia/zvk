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
	tools := []*toolchain{zigTC, goTC, nodeTC}
	jsonMode := len(args) > 0 && (args[0] == "--json" || args[0] == "-j")
	if jsonMode {
		combined := map[string]any{}
		for _, tc := range tools {
			s, err := collectToolStatus(tc)
			if err != nil {
				return err
			}
			combined[tc.name] = s
		}
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(combined)
	}
	for i, tc := range tools {
		if i > 0 {
			fmt.Fprintln(stdout)
		}
		fmt.Fprintf(stdout, "=== %s ===\n", tc.name)
		s, err := collectToolStatus(tc)
		if err != nil {
			return err
		}
		renderToolStatus(tc, s, stdout)
	}
	return nil
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

	// Teach the assistant about the non-toolchain features (best-effort; never
	// fails install). Each writes its doc and refreshes the aggregate CLAUDE.md.
	writeFetchDoc(root, stdout)
	writeServeDoc(root, stdout)

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
// self-update — re-run the canonical installer from source.
//
// Upgrading zvk is "re-run the installer"; self-update is a convenience that
// does exactly that: download install.sh / install.ps1 and execute it. The
// installer manages its own Go and `go install`s the module, so the rebuilt
// binary carries no Mark-of-the-Web (the usual SmartScreen trigger) and its
// sources are hash-verified through GOSUMDB. Keeping the real logic solely in
// the installer means there is one update path, not two that can drift.
// ============================================================================

func runSelfUpdate(args []string, stdout io.Writer) error {
	dryRun := false
	version := "" // empty → installer default (latest)
	for _, a := range args {
		switch {
		case a == "--dry-run":
			dryRun = true
		case a == "--force" || a == "-f":
			// Retained for compatibility; the installer always rebuilds.
		case strings.HasPrefix(a, "--version="):
			version = strings.TrimPrefix(a, "--version=")
		default:
			return usageErrorf("self-update: unknown argument '%s'", a)
		}
	}

	repo := strings.TrimPrefix(modulePath, "github.com/")
	scriptName := "install.sh"
	if isWindows() {
		scriptName = "install.ps1"
	}
	scriptURL := "https://raw.githubusercontent.com/" + repo + "/main/" + scriptName

	fmt.Fprintf(stdout, "[zvk] current %s; self-update re-runs the installer\n", zvkVersion)
	fmt.Fprintf(stdout, "[zvk] installer: %s\n", scriptURL)
	if version != "" {
		fmt.Fprintf(stdout, "[zvk] target version: %s\n", version)
	}
	if dryRun {
		fmt.Fprintf(stdout, "[zvk] dry-run: would download and run %s\n", scriptURL)
		return nil
	}

	script, err := downloadToMemory(scriptURL)
	if err != nil {
		return fmt.Errorf("fetch installer: %w", err)
	}
	tmp, err := os.MkdirTemp("", "zvk-selfupdate-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	scriptPath := filepath.Join(tmp, scriptName)
	if err := os.WriteFile(scriptPath, script, 0o755); err != nil {
		return err
	}

	var cmd *exec.Cmd
	if isWindows() {
		cmd = exec.Command("powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", scriptPath)
	} else {
		cmd = exec.Command("sh", scriptPath)
	}
	cmd.Env = os.Environ()
	if version != "" {
		cmd.Env = append(cmd.Env, "ZVK_VERSION="+version)
	}
	cmd.Stdout = stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("installer failed: %w", err)
	}

	// Echo the version actually installed. The running binary still reports the
	// OLD zvkVersion (it's baked in at build time), so query the freshly-built
	// binary instead. This makes a no-op upgrade visible — e.g. when GOPROXY
	// serves a cached @latest and the rebuild lands on the same version rather
	// than silently looking like it succeeded.
	root, rerr := defaultRoot()
	if rerr == nil {
		installed := filepath.Join(binDir(root), zvkBinaryName())
		if out, verr := exec.Command(installed, "version").Output(); verr == nil {
			now := strings.TrimSpace(strings.TrimPrefix(string(out), "zvk "))
			switch {
			case version != "" && now != version:
				fmt.Fprintf(stdout, "[zvk] installed %s (requested %s)\n", now, version)
			case now == zvkVersion:
				fmt.Fprintf(stdout, "[zvk] version unchanged: %s (already the newest, or GOPROXY served a cached @latest)\n", now)
			default:
				fmt.Fprintf(stdout, "[zvk] upgraded: %s -> %s\n", zvkVersion, now)
			}
		}
	}
	return nil
}
