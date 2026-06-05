package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
)

// ============================================================================
// serve — share a file or directory over HTTP.
//
// The intended workflow: an assistant generates an HTML report, then runs
// `zvk serve report.html` so it opens in a browser — the user's, or a
// colleague's on the same network. It's a plain net/http static file server
// (zero new dependencies), binding all interfaces by default and printing both
// a localhost and a LAN URL (the LAN IP via the same primaryIPv4 used by
// vminit).
//
// Single-file mode (path is a file) serves ONLY that file at every path, so
// sibling files in its directory are not exposed on the LAN. Directory mode
// serves the tree with the standard FileServer (directory listings included).
//
// serve blocks until Ctrl-C (or, with --once, until the first file is served),
// so an assistant runs it in the background and reads the printed URL.
// ============================================================================

const serveUsage = `Usage:
  zvk serve [options] [path]

Serve a file or directory over HTTP so a generated report can be opened in a
browser — yours, or a colleague's on the same network. By default it binds all
interfaces and prints both a localhost and a LAN URL.

Options:
  -p, --port <n>    Port (default 8000; falls back to a random free port if taken)
      --local       Bind 127.0.0.1 only (do not expose on the LAN)
  -b, --bind <ip>   Bind a specific address (overrides --local)
      --once        Exit after the first file is served
  -q, --quiet       Don't log each request
  -h, --help        Show this help

[path] defaults to the current directory. If it's a file, only that file is
served (siblings stay private) and the printed URL points straight at it.

Examples:
  zvk serve report.html           # share one report on the LAN
  zvk serve ./site --port 9000    # serve a directory on a fixed port
  zvk serve --local report.html   # localhost only
`

type serveOptions struct {
	path  string
	port  int
	local bool
	bind  string
	once  bool
	quiet bool
}

func runServe(args []string, stdout io.Writer) error {
	opts, showHelp, err := parseServeArgs(args)
	if err != nil {
		return err
	}
	if showHelp {
		fmt.Fprint(stdout, serveUsage)
		return nil
	}
	return doServe(opts, stdout)
}

func parseServeArgs(args []string) (opts serveOptions, showHelp bool, err error) {
	opts.port = 8000
	for i := 0; i < len(args); i++ {
		a := args[i]
		name, inlineVal, hasInline := a, "", false
		if len(a) > 2 && a[:2] == "--" {
			if eq := indexByte(a, '='); eq >= 0 {
				name, inlineVal, hasInline = a[:eq], a[eq+1:], true
			}
		}
		needValue := func() (string, error) {
			if hasInline {
				return inlineVal, nil
			}
			if i+1 >= len(args) {
				return "", usageErrorf("serve: %s needs a value", name)
			}
			i++
			return args[i], nil
		}
		switch name {
		case "-p", "--port":
			var v string
			if v, err = needValue(); err != nil {
				return
			}
			n, perr := parsePositiveInt(v)
			if perr != nil {
				err = usageErrorf("serve: --port %q: %v", v, perr)
				return
			}
			opts.port = n
		case "-b", "--bind":
			if opts.bind, err = needValue(); err != nil {
				return
			}
		case "--local":
			opts.local = true
		case "--once":
			opts.once = true
		case "-q", "--quiet":
			opts.quiet = true
		case "-h", "--help":
			showHelp = true
		default:
			if len(a) > 0 && a[0] == '-' && a != "-" {
				err = usageErrorf("serve: unknown flag '%s'", a)
				return
			}
			if opts.path != "" {
				err = usageErrorf("serve: unexpected extra argument '%s'", a)
				return
			}
			opts.path = a
		}
	}
	return
}

func doServe(opts serveOptions, stdout io.Writer) error {
	target := opts.path
	if target == "" {
		target = "."
	}
	info, err := os.Stat(target)
	if err != nil {
		return fmt.Errorf("serve: %w", err)
	}
	serveDir, indexFile := target, ""
	if !info.IsDir() {
		serveDir = filepath.Dir(target)
		indexFile = filepath.Base(target)
	}
	absDir, err := filepath.Abs(serveDir)
	if err != nil {
		return err
	}

	host := "0.0.0.0"
	if opts.local {
		host = "127.0.0.1"
	}
	if opts.bind != "" {
		host = opts.bind
	}

	ln, port, err := serveListen(host, opts.port)
	if err != nil {
		return fmt.Errorf("serve: listen: %w", err)
	}

	served := target
	if indexFile != "" {
		served, _ = filepath.Abs(target)
	}
	printServeURLs(stdout, host, port, indexFile, served)

	// Single-file mode serves that one file at every path, keeping siblings
	// private. Directory mode uses the standard tree server.
	var handler http.Handler
	if indexFile != "" {
		full := filepath.Join(absDir, indexFile)
		handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.ServeFile(w, r, full)
		})
	} else {
		handler = http.FileServer(http.Dir(absDir))
	}

	srv := &http.Server{}
	var stopOnce sync.Once
	stop := func() { stopOnce.Do(func() { go srv.Shutdown(context.Background()) }) }

	srv.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !opts.quiet {
			fmt.Fprintf(os.Stderr, "[zvk serve] %s %s %s\n", r.RemoteAddr, r.Method, r.URL.Path)
		}
		handler.ServeHTTP(w, r)
		if opts.once && r.Method == http.MethodGet {
			stop()
		}
	})

	if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("serve: %w", err)
	}
	return nil
}

// serveListen binds host:port, falling back to an OS-assigned free port when a
// requested fixed port is already taken.
func serveListen(host string, port int) (net.Listener, int, error) {
	ln, err := net.Listen("tcp", fmt.Sprintf("%s:%d", host, port))
	if err != nil && port != 0 {
		ln, err = net.Listen("tcp", host+":0")
	}
	if err != nil {
		return nil, 0, err
	}
	return ln, ln.Addr().(*net.TCPAddr).Port, nil
}

func printServeURLs(w io.Writer, host string, port int, indexFile, served string) {
	suffix := ""
	if indexFile != "" {
		suffix = "/" + indexFile
	}
	fmt.Fprintf(w, "[zvk serve] serving %s\n", served)
	fmt.Fprintf(w, "[zvk serve] local:  http://127.0.0.1:%d%s\n", port, suffix)
	if host != "127.0.0.1" {
		if ip, err := primaryIPv4(); err == nil && ip != "" {
			fmt.Fprintf(w, "[zvk serve] LAN:    http://%s:%d%s\n", ip, port, suffix)
		}
		fmt.Fprintf(w, "[zvk serve] exposed on the LAN (bind %s); press Ctrl-C to stop\n", host)
	} else {
		fmt.Fprintf(w, "[zvk serve] localhost only; press Ctrl-C to stop\n")
	}
}

// indexByte returns the index of the first b in s, or -1. (Local helper to keep
// this file free of an extra strings import for one call site.)
func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

// ----------------------------------------------------------------------------
// Claude Code awareness
// ----------------------------------------------------------------------------

func writeServeDoc(root string, stdout io.Writer) {
	if os.Getenv("ZVK_NO_DOCS") != "" {
		return
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		absRoot = root
	}
	claudePath := filepath.Join(absRoot, "serve", "CLAUDE.md")
	if err := writeFileAtomic(claudePath, []byte(serveClaudeMd()), 0o644); err != nil {
		fmt.Fprintf(stdout, "[zvk serve] note: could not write CLAUDE.md: %v\n", err)
		return
	}
	refreshZvkClaudeMd(absRoot, stdout)
}

func serveClaudeMd() string {
	return "# HTTP share (managed by zvk)\n\n" +
		"Auto-generated by zvk. Do not edit — overwritten on next `zvk self-install`.\n" +
		"Disable with `ZVK_NO_CLAUDE_MD=1`.\n\n" +
		"## What\n\n" +
		"`zvk serve [path]` serves a file or directory over HTTP. Use it to **share\n" +
		"a generated HTML report**: write the report to a file, then serve it so the\n" +
		"user (or a colleague on the same LAN) can open it in a browser.\n\n" +
		"## How to use it (it blocks — run it in the background)\n\n" +
		"`zvk serve` runs until stopped, so start it in the background and read the\n" +
		"printed URL from its output:\n\n" +
		"```sh\n" +
		"# 1. write the report\n" +
		"#    (generate report.html however you like)\n" +
		"# 2. share it — runs in the background, prints local + LAN URLs\n" +
		"zvk serve report.html\n" +
		"```\n\n" +
		"It prints, e.g.:\n\n" +
		"```\n" +
		"[zvk serve] local:  http://127.0.0.1:8000/report.html\n" +
		"[zvk serve] LAN:    http://192.168.1.42:8000/report.html\n" +
		"```\n\n" +
		"Give the user the URL. By default the report is reachable on the LAN; pass\n" +
		"`--local` to bind 127.0.0.1 only. A single file is served in isolation —\n" +
		"sibling files in its directory are NOT exposed.\n\n" +
		"## Options\n\n" +
		"- `--local` — localhost only (don't expose on the LAN)\n" +
		"- `-p, --port <n>` — fixed port (default 8000; auto-falls back if taken)\n" +
		"- `--once` — exit after the first file is served (one-shot share)\n" +
		"- `-q, --quiet` — don't log each request\n"
}
