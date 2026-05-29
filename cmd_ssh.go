package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"golang.org/x/crypto/ssh"
)

const sshUsage = `Usage:
  zvk ssh keygen [--name <name>] [--comment <comment>] [--force]
                                          Generate an ed25519 keypair
  zvk ssh list                          List managed keys
  zvk ssh show <name> [--private]       Print public (or private) key
  zvk ssh remove <name>                 Delete a managed key
  zvk ssh add <name>                    Add a key to ssh-agent (runs ssh-add)
  zvk ssh copy <name>                   Copy public key to system clipboard
  zvk ssh path [<name>]                 Print absolute path(s) of managed key(s)

Keys live under <root>/ssh/keys/. Default <name> is id_ed25519_<short-id>.
`

func runSSH(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(stdout, sshUsage)
		return nil
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "keygen", "gen", "new":
		return runSSHKeygen(rest, stdout)
	case "list", "ls":
		return runSSHList(stdout)
	case "show", "cat":
		return runSSHShow(rest, stdout)
	case "remove", "rm", "delete":
		return runSSHRemove(rest, stdout)
	case "add":
		return runSSHAdd(rest, stdout)
	case "copy", "cp":
		return runSSHCopy(rest, stdout)
	case "path":
		return runSSHPath(rest, stdout)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, sshUsage)
		return nil
	default:
		return usageErrorf("ssh: unknown subcommand '%s'\n\n%s", sub, sshUsage)
	}
}

// ============================================================================
// Paths
// ============================================================================

func sshKeysDir(root string) string { return filepath.Join(root, "ssh", "keys") }

func sshKeyPath(root, name string) string { return filepath.Join(root, "ssh", "keys", name) }

func sshPubKeyPath(root, name string) string {
	return filepath.Join(root, "ssh", "keys", name+".pub")
}

// ============================================================================
// keygen
// ============================================================================

func runSSHKeygen(args []string, stdout io.Writer) error {
	var (
		name    string
		comment string
		force   bool
	)
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--name", "-n":
			i++
			if i >= len(args) {
				return usageErrorf("ssh keygen: --name needs a value")
			}
			name = args[i]
		case "--comment", "-C":
			i++
			if i >= len(args) {
				return usageErrorf("ssh keygen: --comment needs a value")
			}
			comment = args[i]
		case "--force", "-f":
			force = true
		default:
			return usageErrorf("ssh keygen: unknown arg '%s'", a)
		}
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}

	if name == "" {
		// Disambiguate auto-generated names with first 4 bytes of pubkey.
		id := hex.EncodeToString(pub[:4])
		name = "id_ed25519_" + id
	}
	if comment == "" {
		user := os.Getenv("USER")
		if user == "" {
			user = os.Getenv("USERNAME")
		}
		if user == "" {
			user = "user"
		}
		host, err := os.Hostname()
		if err != nil || host == "" {
			host = "zvk"
		}
		comment = user + "@" + host
	}

	root, err := defaultRoot()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(sshKeysDir(root), 0o700); err != nil {
		return err
	}

	privPath := sshKeyPath(root, name)
	pubPath := sshPubKeyPath(root, name)

	if !force && pathExists(privPath) {
		return fmt.Errorf("ssh: '%s' already exists at %s (use --force to overwrite)", name, privPath)
	}

	// Public key line: "ssh-ed25519 <base64> <comment>\n".
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return err
	}
	pubBytes := ssh.MarshalAuthorizedKey(sshPub)
	pubBytes = bytes.TrimRight(pubBytes, "\n")
	pubLine := fmt.Sprintf("%s %s\n", string(pubBytes), comment)

	// Private key: OpenSSH `openssh-key-v1` PEM (unencrypted).
	pemBlock, err := ssh.MarshalPrivateKey(priv, comment)
	if err != nil {
		return err
	}
	privPEM := pem.EncodeToMemory(pemBlock)

	if err := writeFileAtomic(privPath, privPEM, 0o600); err != nil {
		return err
	}
	if err := writeFileAtomic(pubPath, []byte(pubLine), 0o644); err != nil {
		return err
	}

	fmt.Fprintf(stdout, "[zvk ssh] generated ed25519 keypair '%s'\n", name)
	fmt.Fprintf(stdout, "  private: %s\n", privPath)
	fmt.Fprintf(stdout, "  public:  %s\n\n", pubPath)
	fmt.Fprint(stdout, pubLine)
	return nil
}

// ============================================================================
// list / show / remove / path
// ============================================================================

func listSSHKeys(root string) ([]string, error) {
	files, err := listFiles(sshKeysDir(root))
	if err != nil {
		return nil, err
	}
	var out []string
	for _, f := range files {
		if filepath.Ext(f) == ".pub" {
			continue
		}
		out = append(out, f)
	}
	return out, nil
}

func runSSHList(stdout io.Writer) error {
	root, err := defaultRoot()
	if err != nil {
		return err
	}
	dir := sshKeysDir(root)
	keys, err := listSSHKeys(root)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "SSH keys (%s):\n", dir)
	if len(keys) == 0 {
		fmt.Fprintln(stdout, "  (none)")
		return nil
	}
	for _, k := range keys {
		fmt.Fprintf(stdout, "  %s\n", k)
	}
	return nil
}

func requireName(args []string, usage string) (string, error) {
	if len(args) < 1 {
		return "", usageErrorf("%s", usage)
	}
	return args[0], nil
}

func runSSHShow(args []string, stdout io.Writer) error {
	name, err := requireName(args, "usage: zvk ssh show <name> [--private]")
	if err != nil {
		return err
	}
	private := false
	for _, a := range args[1:] {
		if a == "--private" {
			private = true
		}
	}
	root, err := defaultRoot()
	if err != nil {
		return err
	}
	target := sshPubKeyPath(root, name)
	if private {
		target = sshKeyPath(root, name)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("ssh: no such key file: %s", target)
		}
		return err
	}
	if _, err := stdout.Write(data); err != nil {
		return err
	}
	if len(data) > 0 && data[len(data)-1] != '\n' {
		fmt.Fprintln(stdout)
	}
	return nil
}

func runSSHRemove(args []string, stdout io.Writer) error {
	name, err := requireName(args, "usage: zvk ssh remove <name>")
	if err != nil {
		return err
	}
	root, err := defaultRoot()
	if err != nil {
		return err
	}
	priv := sshKeyPath(root, name)
	pub := sshPubKeyPath(root, name)

	var found bool
	for _, p := range []string{priv, pub} {
		if pathExists(p) {
			found = true
			if err := os.Remove(p); err != nil {
				return err
			}
		}
	}
	if !found {
		return fmt.Errorf("ssh: no such key '%s'", name)
	}
	fmt.Fprintf(stdout, "removed key '%s'\n", name)
	return nil
}

func runSSHPath(args []string, stdout io.Writer) error {
	root, err := defaultRoot()
	if err != nil {
		return err
	}
	if len(args) >= 1 {
		fmt.Fprintln(stdout, sshKeyPath(root, args[0]))
		return nil
	}
	keys, err := listSSHKeys(root)
	if err != nil {
		return err
	}
	for _, k := range keys {
		fmt.Fprintln(stdout, sshKeyPath(root, k))
	}
	return nil
}

// ============================================================================
// add (subprocess: ssh-add)
// ============================================================================

func runSSHAdd(args []string, stdout io.Writer) error {
	name, err := requireName(args, "usage: zvk ssh add <name>")
	if err != nil {
		return err
	}
	root, err := defaultRoot()
	if err != nil {
		return err
	}
	priv := sshKeyPath(root, name)
	cmd := exec.Command("ssh-add", priv)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		// Propagate child exit code: ssh-add already printed to stderr,
		// so we exit directly rather than wrapping in an error.
		if exitErr, ok := err.(*exec.ExitError); ok {
			os.Exit(exitErr.ExitCode())
		}
		return err
	}
	fmt.Fprintf(stdout, "[zvk ssh] added %s\n", priv)
	return nil
}

// ============================================================================
// copy to clipboard
// ============================================================================

func runSSHCopy(args []string, stdout io.Writer) error {
	name, err := requireName(args, "usage: zvk ssh copy <name>")
	if err != nil {
		return err
	}
	root, err := defaultRoot()
	if err != nil {
		return err
	}
	pub := sshPubKeyPath(root, name)
	data, err := os.ReadFile(pub)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("ssh: no public key for '%s'", name)
		}
		return err
	}
	var argv []string
	switch {
	case isMacOS():
		argv = []string{"pbcopy"}
	case !isWindows():
		if os.Getenv("WAYLAND_DISPLAY") != "" {
			argv = []string{"wl-copy"}
		} else {
			argv = []string{"xclip", "-selection", "clipboard"}
		}
	default:
		return fmt.Errorf("ssh: copy not supported on this OS — pipe `zvk ssh show` manually")
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin = bytes.NewReader(data)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		// Propagate child exit code: the clipboard tool already printed to
		// stderr if it failed.
		if exitErr, ok := err.(*exec.ExitError); ok {
			os.Exit(exitErr.ExitCode())
		}
		return err
	}
	fmt.Fprintf(stdout, "[zvk ssh] copied public key '%s' to clipboard\n", name)
	return nil
}
