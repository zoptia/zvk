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
// app — install assorted developer tools/apps that are NOT version-managed
// toolchains.
//
// zig/go/node go through the toolchain driver (multi-version, channels, symlink
// switching). Things like Homebrew (a package manager), Claude Code (an app),
// or a VM/container runtime have no channels and no version switching — you
// install one and you're done. So they live here as "recipes": each knows how
// to detect itself on PATH and how to install itself by running its official
// script. This is deliberately NOT a package manager — no version tracking, no
// bookkeeping, no bin symlinks. Each recipe is just its own bit of code.
//
// Recipes are platform-specific: `app list` shows only what's installable on
// the current OS (macOS/Linux → homebrew, claude-code; Windows → winget, scoop).
// A recipe whose `install` is nil has no automated installer (e.g. winget ships
// with Windows) and just prints `installHint`.
// Adding a tool is appending one `appRecipe`.
// ============================================================================

const appUsage = `Usage:
  zvk app list                    List apps installable on this platform
  zvk app install <name>          Install an app via its official script
  zvk app uninstall <name>        Print how to uninstall an app
  zvk app status                  Alias for list

Apps are assorted tools (package managers, runtimes, apps) that aren't
version-managed the way zig/go/node are. Installation runs each tool's official
script. This is not a package manager — zvk does no version tracking.
`

// appRecipe is one installable tool.
type appRecipe struct {
	name        string
	description string
	osSupport   []string                     // GOOS values this supports; empty = all
	detectCmd   string                       // command name to look for on PATH ("" = can't detect)
	install     func(stdout io.Writer) error // nil = no automated install; use installHint
	installHint string                       // how to install manually, shown when install is nil
	uninstall   string                       // human instructions (app never deletes for you)
	docURL      string
}

var appRecipes = []*appRecipe{
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
	{
		name:        "winget",
		description: "Windows Package Manager (winget)",
		osSupport:   []string{"windows"},
		detectCmd:   "winget",
		// winget ships as the App Installer AppX package — there's no official
		// install script, so detect it and point at the Store if it's missing.
		install: nil,
		installHint: "winget ships with the 'App Installer' package, preinstalled on current\n" +
			"  Windows 10/11. If it's missing, install 'App Installer' from the Microsoft\n" +
			"  Store (https://apps.microsoft.com/detail/9NBLGGH4NNS1) or grab a release from\n" +
			"  https://github.com/microsoft/winget-cli/releases",
		uninstall: "winget is a Windows system component; remove 'App Installer' from Settings > Apps if needed",
		docURL:    "https://learn.microsoft.com/windows/package-manager/",
	},
	{
		name:        "scoop",
		description: "Scoop package manager (scoop)",
		osSupport:   []string{"windows"},
		detectCmd:   "scoop",
		install: func(stdout io.Writer) error {
			// Official PowerShell one-liner.
			return runPowerShellCommand("iwr -useb get.scoop.sh | iex", stdout)
		},
		uninstall: "scoop uninstall scoop",
		docURL:    "https://scoop.sh",
	},
}

func findApp(name string) *appRecipe {
	for _, a := range appRecipes {
		if a.name == name {
			return a
		}
	}
	return nil
}

func (a *appRecipe) supportsThisOS() bool {
	if len(a.osSupport) == 0 {
		return true
	}
	for _, o := range a.osSupport {
		if o == runtime.GOOS {
			return true
		}
	}
	return false
}

// detect returns the install path and true if the app's command is on PATH.
func (a *appRecipe) detect() (string, bool) {
	if a.detectCmd == "" {
		return "", false
	}
	p, err := exec.LookPath(a.detectCmd)
	if err != nil {
		return "", false
	}
	return p, true
}

func runApp(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return runAppList(stdout)
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "list", "ls", "status", "info":
		return runAppList(stdout)
	case "install", "add":
		if len(rest) < 1 {
			return usageErrorf("usage: zvk app install <name>")
		}
		return runAppInstall(rest[0], stdout)
	case "uninstall", "remove", "rm":
		if len(rest) < 1 {
			return usageErrorf("usage: zvk app uninstall <name>")
		}
		return runAppUninstall(rest[0], stdout)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, appUsage)
		return nil
	default:
		return usageErrorf("app: unknown subcommand '%s'\n\n%s", sub, appUsage)
	}
}

// runAppList shows only the apps installable on the current platform.
func runAppList(stdout io.Writer) error {
	fmt.Fprintf(stdout, "Apps installable on %s:\n", runtime.GOOS)
	any := false
	for _, a := range appRecipes {
		if !a.supportsThisOS() {
			continue
		}
		any = true
		state := "not installed"
		if p, ok := a.detect(); ok {
			state = "installed (" + p + ")"
		}
		fmt.Fprintf(stdout, "  %-12s %-30s %s\n", a.name, a.description, state)
	}
	if !any {
		fmt.Fprintln(stdout, "  (none)")
	}
	return nil
}

func runAppInstall(name string, stdout io.Writer) error {
	a := findApp(name)
	if a == nil {
		return usageErrorf("app: unknown app '%s' (see `zvk app list`)", name)
	}
	if !a.supportsThisOS() {
		return fmt.Errorf("app: '%s' is not available on %s", name, runtime.GOOS)
	}
	if p, ok := a.detect(); ok {
		fmt.Fprintf(stdout, "[zvk app] %s already installed at %s\n", name, p)
		return nil
	}
	if a.install == nil {
		// No automated installer (e.g. winget): print the manual steps.
		fmt.Fprintf(stdout, "[zvk app] %s can't be installed automatically:\n", name)
		fmt.Fprintf(stdout, "  %s\n", a.installHint)
		fmt.Fprintf(stdout, "  docs: %s\n", a.docURL)
		return nil
	}
	fmt.Fprintf(stdout, "[zvk app] installing %s (%s)\n", name, a.docURL)
	if err := a.install(stdout); err != nil {
		return fmt.Errorf("app: installing %s: %w", name, err)
	}
	if p, ok := a.detect(); ok {
		fmt.Fprintf(stdout, "[zvk app] %s ready: %s\n", name, p)
	} else {
		fmt.Fprintf(stdout, "[zvk app] %s installed; you may need to restart your shell for it to appear on PATH\n", name)
	}
	return nil
}

func runAppUninstall(name string, stdout io.Writer) error {
	a := findApp(name)
	if a == nil {
		return usageErrorf("app: unknown app '%s' (see `zvk app list`)", name)
	}
	// app installs system-level tools it didn't lay out itself, so it never
	// deletes them for you — it prints the official way instead.
	fmt.Fprintf(stdout, "[zvk app] zvk does not remove %s automatically. To uninstall:\n", name)
	fmt.Fprintf(stdout, "  %s\n", a.uninstall)
	fmt.Fprintf(stdout, "  docs: %s\n", a.docURL)
	return nil
}

// runShellScript downloads a script and runs it through `shell` (POSIX recipes).
// stdin/stdout/stderr are passed through so the script can prompt (e.g. brew's
// sudo). Same trusted-official-script pattern self-update uses for the installer.
func runShellScript(url, shell string, extraEnv []string, stdout io.Writer) error {
	data, err := downloadToMemory(url)
	if err != nil {
		return fmt.Errorf("fetch %s: %w", url, err)
	}
	tmp, err := os.MkdirTemp("", "zvk-app-")
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

// runPowerShellCommand runs an inline PowerShell command (Windows recipes that
// install via `iwr ... | iex`). stdin/stdout/stderr are passed through.
func runPowerShellCommand(command string, stdout io.Writer) error {
	cmd := exec.Command("powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", command)
	cmd.Stdin = os.Stdin
	cmd.Stdout = stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
