package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// ============================================================================
// extras — install assorted developer tools that are NOT version-managed
// toolchains.
//
// zig/go/node go through the toolchain driver (multi-version, channels, symlink
// switching). Things like Homebrew (a package manager) and Claude Code (an app)
// have no channels and no version switching — you install one and you're done.
// So they live here as "recipes": each knows how to detect itself on PATH and
// how to install itself (usually by running an official install script). extras
// does NOT track versions, manage channels, or wire bin symlinks.
//
// Adding an extra is appending one `extra` to `extraRecipes`.
// ============================================================================

const extrasUsage = `Usage:
  zvk extras list                 List available extras and install state
  zvk extras install <name>       Install an extra via its official method
  zvk extras uninstall <name>     Print how to uninstall an extra
  zvk extras status               Alias for list

Extras are assorted tools (package managers, apps) that aren't version-managed
the way zig/go/node are. Installation runs each tool's official script.
`

// extra is one installable recipe.
type extra struct {
	name        string
	description string
	osSupport   []string // GOOS values this supports; empty = all
	detectCmd   string   // command name to look for on PATH ("" = can't detect)
	install     func(stdout io.Writer) error
	uninstall   string // human instructions (extras never deletes for you)
	docURL      string
}

var extraRecipes = []*extra{
	{
		name:        "homebrew",
		description: "Homebrew package manager (brew)",
		osSupport:   []string{"darwin", "linux"},
		detectCmd:   "brew",
		install: func(stdout io.Writer) error {
			// Official one-liner, run non-interactively. brew may still prompt
			// for sudo, so stdin is passed through.
			return runShellScript(
				"https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh",
				"bash", []string{"NONINTERACTIVE=1"}, stdout)
		},
		uninstall: `/bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/uninstall.sh)"`,
		docURL:    "https://brew.sh",
	},
	{
		name:        "claude-code",
		description: "Claude Code CLI (claude)",
		osSupport:   []string{"darwin", "linux"},
		detectCmd:   "claude",
		install: func(stdout io.Writer) error {
			// Official native installer (no Node required).
			return runShellScript("https://claude.ai/install.sh", "bash", nil, stdout)
		},
		uninstall: "remove the `claude` binary the installer placed (typically ~/.local/bin/claude); see the docs",
		docURL:    "https://docs.claude.com/en/docs/claude-code",
	},
}

func findExtra(name string) *extra {
	for _, e := range extraRecipes {
		if e.name == name {
			return e
		}
	}
	return nil
}

func (e *extra) supportsThisOS() bool {
	if len(e.osSupport) == 0 {
		return true
	}
	for _, o := range e.osSupport {
		if o == runtime.GOOS {
			return true
		}
	}
	return false
}

// detect returns the install path and true if the extra's command is on PATH.
func (e *extra) detect() (string, bool) {
	if e.detectCmd == "" {
		return "", false
	}
	p, err := exec.LookPath(e.detectCmd)
	if err != nil {
		return "", false
	}
	return p, true
}

func runExtras(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return runExtrasList(stdout)
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "list", "ls", "status", "info":
		return runExtrasList(stdout)
	case "install", "add":
		if len(rest) < 1 {
			return usageErrorf("usage: zvk extras install <name>")
		}
		return runExtrasInstall(rest[0], stdout)
	case "uninstall", "remove", "rm":
		if len(rest) < 1 {
			return usageErrorf("usage: zvk extras uninstall <name>")
		}
		return runExtrasUninstall(rest[0], stdout)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, extrasUsage)
		return nil
	default:
		return usageErrorf("extras: unknown subcommand '%s'\n\n%s", sub, extrasUsage)
	}
}

func runExtrasList(stdout io.Writer) error {
	fmt.Fprintln(stdout, "Available extras:")
	for _, e := range extraRecipes {
		state := "not installed"
		if !e.supportsThisOS() {
			state = "unsupported on " + runtime.GOOS
		} else if p, ok := e.detect(); ok {
			state = "installed (" + p + ")"
		}
		fmt.Fprintf(stdout, "  %-12s %-30s %s\n", e.name, e.description, state)
	}
	return nil
}

func runExtrasInstall(name string, stdout io.Writer) error {
	e := findExtra(name)
	if e == nil {
		return usageErrorf("extras: unknown extra '%s' (see `zvk extras list`)", name)
	}
	if !e.supportsThisOS() {
		return fmt.Errorf("extras: '%s' is not supported on %s", name, runtime.GOOS)
	}
	if p, ok := e.detect(); ok {
		fmt.Fprintf(stdout, "[zvk extras] %s already installed at %s\n", name, p)
		return nil
	}
	fmt.Fprintf(stdout, "[zvk extras] installing %s (%s)\n", name, e.docURL)
	if err := e.install(stdout); err != nil {
		return fmt.Errorf("extras: installing %s: %w", name, err)
	}
	if p, ok := e.detect(); ok {
		fmt.Fprintf(stdout, "[zvk extras] %s ready: %s\n", name, p)
	} else {
		fmt.Fprintf(stdout, "[zvk extras] %s installed; you may need to restart your shell for it to appear on PATH\n", name)
	}
	return nil
}

func runExtrasUninstall(name string, stdout io.Writer) error {
	e := findExtra(name)
	if e == nil {
		return usageErrorf("extras: unknown extra '%s' (see `zvk extras list`)", name)
	}
	// extras installs system-level apps it didn't lay out itself, so it never
	// deletes them for you — it prints the official way instead.
	fmt.Fprintf(stdout, "[zvk extras] zvk does not remove %s automatically. To uninstall:\n", name)
	fmt.Fprintf(stdout, "  %s\n", e.uninstall)
	fmt.Fprintf(stdout, "  docs: %s\n", e.docURL)
	return nil
}

// runShellScript downloads a script and runs it through `shell`. stdin/stdout/
// stderr are passed through so the script can prompt (e.g. brew's sudo). This is
// the same trusted-official-script pattern self-update uses for the installer.
func runShellScript(url, shell string, extraEnv []string, stdout io.Writer) error {
	data, err := downloadToMemory(url)
	if err != nil {
		return fmt.Errorf("fetch %s: %w", url, err)
	}
	tmp, err := os.MkdirTemp("", "zvk-extra-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	scriptPath := filepath.Join(tmp, "install.sh")
	if err := os.WriteFile(scriptPath, data, 0o755); err != nil {
		return err
	}

	cmd := exec.Command(shell, scriptPath)
	cmd.Env = append(os.Environ(), extraEnv...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
