package main

import (
	"errors"
	"fmt"
	"os"
)

const usage = `zvk - multi-toolchain version manager (zig, go, node) + ssh key manager

Usage:
  zvk zig  <cmd> [args...]      Zig toolchain management
  zvk go   <cmd> [args...]      Go toolchain management
  zvk node <cmd> [args...]      Node.js toolchain management
  zvk ssh  <cmd> [args...]      SSH key management
  zvk app  <cmd> [args...]      Install assorted tools/apps (homebrew, claude-code, winget, scoop)

  zvk fetch [opts] <url>        HTTP request impersonating the latest Chrome's TLS fingerprint

  zvk status [--json]           Combined status (zig + go)
  zvk self-install              Copy zvk to <root>/bin/ + setup PATH
  zvk self-update [--dry-run] [--version=<v>]
                                   Rebuild & install zvk from source via ` + "`go install`" + `
  zvk version                   Print zvk version
  zvk help                      Show this help

Run ` + "`zvk <tool> help`" + ` for subcommand details. Install root defaults to
~/.zvk/ (override with ZVK_ROOT).
`

func main() {
	stdout := os.Stdout

	if len(os.Args) < 2 {
		fmt.Fprint(stdout, usage)
		return
	}

	cmd := os.Args[1]
	args := os.Args[2:]

	var err error
	switch cmd {
	case "zig":
		err = runToolchain(zigTC, args, stdout)
	case "go":
		err = runToolchain(goTC, args, stdout)
	case "node":
		err = runToolchain(nodeTC, args, stdout)
	case "ssh":
		err = runSSH(args, stdout)
	case "app":
		err = runApp(args, stdout)
	case "fetch":
		err = runFetch(args, stdout)
	case "status", "info":
		err = runCombinedStatus(args, stdout)
	case "self-install":
		err = runSelfInstall(stdout)
	case "self-update":
		err = runSelfUpdate(args, stdout)
	case "version", "--version", "-v":
		fmt.Fprintf(stdout, "zvk %s\n", zvkVersion)
	case "help", "--help", "-h":
		fmt.Fprint(stdout, usage)
	default:
		err = usageErrorf("unknown command '%s'\n\n%s", cmd, usage)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "zvk: %v\n", err)
		var uerr *usageError
		if errors.As(err, &uerr) {
			os.Exit(2)
		}
		os.Exit(1)
	}
}
