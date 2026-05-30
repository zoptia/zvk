package main

import (
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// ============================================================================
// app vminit — configure THIS host as a low-footprint VM node.
//
// Unlike the other `app` recipes (which install third-party tools), vminit
// reconfigures the local machine:
//   - hostname  → vm-<primary-ipv4>  (dots become dashes; hostnames can't have dots)
//   - no sleep/suspend
//   - no file indexing
//   - no graphical UI (Linux) / reduced animations (macOS)
//
// Every change is system-level and needs root, so root-requiring commands are
// run through sudo (stdin passed through for the password prompt). --dry-run
// prints the exact commands without running anything.
// ============================================================================

const vminitUsage = `Usage:
  zvk app vminit [--dry-run]

Configure this host as a low-footprint VM node:
  - set hostname to vm-<primary-ipv4>
  - disable sleep/suspend/hibernate
  - disable file indexing (Spotlight / mlocate)
  - disable the graphical UI (Linux) / reduce animations (macOS)

Changes are system-level and run via sudo. --dry-run prints the commands
without executing them.
`

// vmStep is one logical configuration step (one or more commands).
type vmStep struct {
	desc       string
	argvs      [][]string
	needsRoot  bool
	bestEffort bool // a failure is warned about, not fatal (e.g. unit may not exist)
}

func runAppVminit(args []string, stdout io.Writer) error {
	dryRun := false
	for _, a := range args {
		switch a {
		case "--dry-run", "-n":
			dryRun = true
		case "help", "-h", "--help":
			fmt.Fprint(stdout, vminitUsage)
			return nil
		default:
			return usageErrorf("app vminit: unknown argument '%s'", a)
		}
	}

	ip, err := primaryIPv4()
	if err != nil {
		return fmt.Errorf("vminit: could not determine primary IPv4: %w", err)
	}
	hostname := "vm-" + strings.ReplaceAll(ip, ".", "-")

	steps, err := vmSteps(hostname)
	if err != nil {
		return err
	}

	fmt.Fprintf(stdout, "[zvk vminit] platform: %s\n", runtime.GOOS)
	fmt.Fprintf(stdout, "[zvk vminit] primary IPv4: %s -> hostname: %s\n", ip, hostname)
	if dryRun {
		fmt.Fprintln(stdout, "[zvk vminit] dry-run: commands that WOULD run:")
	}

	for _, s := range steps {
		fmt.Fprintf(stdout, "[zvk vminit] %s\n", s.desc)
		for _, argv := range s.argvs {
			full := argv
			// Wrap root-requiring commands in sudo unless already root.
			if s.needsRoot && os.Geteuid() != 0 {
				full = append([]string{"sudo"}, argv...)
			}
			if dryRun {
				fmt.Fprintf(stdout, "    %s\n", strings.Join(full, " "))
				continue
			}
			cmd := exec.Command(full[0], full[1:]...)
			cmd.Stdin = os.Stdin
			cmd.Stdout = stdout
			cmd.Stderr = os.Stderr
			if err := cmd.Run(); err != nil {
				if s.bestEffort {
					fmt.Fprintf(stdout, "    note: `%s` failed (%v); continuing\n", strings.Join(full, " "), err)
					continue
				}
				return fmt.Errorf("vminit: `%s` failed: %w", strings.Join(full, " "), err)
			}
		}
	}
	fmt.Fprintln(stdout, "[zvk vminit] done")
	if runtime.GOOS == "linux" && !dryRun {
		fmt.Fprintln(stdout, "[zvk vminit] reboot for the graphical-UI change to take effect")
	}
	return nil
}

// primaryIPv4 returns the IP the kernel would use to reach the public internet —
// i.e. the address on the default-route interface. The UDP "connection" sends
// nothing; it only makes the kernel pick a route, so this works offline and
// cross-platform without parsing `ip`/`ifconfig`.
func primaryIPv4() (string, error) {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return "", err
	}
	defer conn.Close()
	addr, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok || addr.IP.To4() == nil {
		return "", fmt.Errorf("no IPv4 local address")
	}
	return addr.IP.To4().String(), nil
}

// vmSteps returns the platform-specific configuration steps.
func vmSteps(hostname string) ([]vmStep, error) {
	switch runtime.GOOS {
	case "linux":
		return []vmStep{
			{desc: "set hostname", needsRoot: true, argvs: [][]string{
				{"hostnamectl", "set-hostname", hostname}}},
			{desc: "disable sleep/suspend/hibernate", needsRoot: true, argvs: [][]string{
				{"systemctl", "mask", "sleep.target", "suspend.target", "hibernate.target", "hybrid-sleep.target"}}},
			{desc: "disable file indexing (mlocate/plocate)", needsRoot: true, bestEffort: true, argvs: [][]string{
				{"systemctl", "mask", "mlocate.timer", "plocate-updatedb.timer", "updatedb.timer"}}},
			{desc: "boot without the graphical UI", needsRoot: true, argvs: [][]string{
				{"systemctl", "set-default", "multi-user.target"}}},
		}, nil
	case "darwin":
		return []vmStep{
			{desc: "set hostname", needsRoot: true, argvs: [][]string{
				{"scutil", "--set", "HostName", hostname},
				{"scutil", "--set", "LocalHostName", hostname},
				{"scutil", "--set", "ComputerName", hostname}}},
			{desc: "disable sleep", needsRoot: true, argvs: [][]string{
				{"pmset", "-a", "sleep", "0"},
				{"pmset", "-a", "disablesleep", "1"}}},
			{desc: "disable Spotlight indexing", needsRoot: true, argvs: [][]string{
				{"mdutil", "-a", "-i", "off"}}},
			// Per-user defaults (no root); reduce window/dock animation.
			{desc: "reduce UI animations", needsRoot: false, bestEffort: true, argvs: [][]string{
				{"defaults", "write", "-g", "NSAutomaticWindowAnimationsEnabled", "-bool", "false"},
				{"defaults", "write", "com.apple.dock", "launchanim", "-bool", "false"}}},
		}, nil
	default:
		return nil, fmt.Errorf("vminit: unsupported on %s", runtime.GOOS)
	}
}
