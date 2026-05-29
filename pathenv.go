package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
)

// setupPath idempotently appends `binDir` to PATH in the user's shell rc file.
// Skipped when `ZVK_NO_MODIFY_PATH` is set or on Windows (handled by install.ps1).
func setupPath(binDir string, stdout io.Writer) error {
	if os.Getenv("ZVK_NO_MODIFY_PATH") != "" {
		return nil
	}
	if isWindows() {
		fmt.Fprintln(stdout, "[zvk] on Windows; PATH should be configured by install.ps1")
		return nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}

	shell := os.Getenv("SHELL")
	if shell == "" {
		fmt.Fprintf(stdout, "[zvk] SHELL not set; add to PATH manually:\n  export PATH=\"%s:$PATH\"\n", binDir)
		return nil
	}
	shellName := filepath.Base(shell)

	var rcPath, line string
	switch shellName {
	case "fish":
		rcPath = filepath.Join(home, ".config", "fish", "conf.d", "zvk.fish")
		line = fmt.Sprintf("set -gx PATH \"%s\" $PATH\n", binDir)
	case "bash":
		filename := ".bashrc"
		if runtime.GOOS == "darwin" {
			filename = ".bash_profile"
		}
		rcPath = filepath.Join(home, filename)
		line = fmt.Sprintf("export PATH=\"%s:$PATH\"\n", binDir)
	case "zsh":
		rcPath = filepath.Join(home, ".zshrc")
		line = fmt.Sprintf("export PATH=\"%s:$PATH\"\n", binDir)
	default:
		fmt.Fprintf(stdout, "[zvk] shell '%s' not auto-configured; add to PATH manually:\n  export PATH=\"%s:$PATH\"\n", shellName, binDir)
		return nil
	}

	existing, err := os.ReadFile(rcPath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if bytes.Contains(existing, []byte(binDir)) {
		fmt.Fprintf(stdout, "[zvk] PATH already configured in %s\n", rcPath)
		return nil
	}

	var buf bytes.Buffer
	buf.Write(existing)
	if len(existing) > 0 && existing[len(existing)-1] != '\n' {
		buf.WriteByte('\n')
	}
	buf.WriteString("\n# Added by zvk\n")
	buf.WriteString(line)

	if err := os.MkdirAll(filepath.Dir(rcPath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(rcPath, buf.Bytes(), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "[zvk] added %s to PATH in %s\n", binDir, rcPath)
	fmt.Fprintln(stdout, "[zvk] restart your shell or run: exec $SHELL -l")
	return nil
}
