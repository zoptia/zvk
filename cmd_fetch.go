package main

import (
	"fmt"
	"io"
	"os"
	"runtime"
	"sort"
	"strings"

	fhttp "github.com/bogdanfinn/fhttp"
	tls_client "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"
)

// ============================================================================
// fetch — issue an HTTP request while impersonating a real browser's TLS +
// HTTP/2 fingerprint, so the response is what a Chrome user would get rather
// than what an anti-bot edge serves to scripts.
//
// WebFetch/curl/Go's net/http present a Go TLS ClientHello (a distinctive JA3)
// and a non-browser HTTP/2 SETTINGS frame. Cloudflare, Akamai, PerimeterX and
// friends fingerprint exactly that and answer with a challenge page or 403. By
// driving bogdanfinn/tls-client we reuse Chrome's real ClientHello, HTTP/2
// settings, header order and pseudo-header order, so the request is
// indistinguishable from the browser it claims to be.
//
// The default profile is profiles.DefaultClientProfile — the upstream-tracked
// "latest Chrome" — so `zvk fetch <url>` impersonates current Chrome with no
// version pinning on our side. `--profile <name>` selects any of the
// MappedTLSClients keys (`zvk fetch --list-profiles`).
//
// This is a single-shot client (no proxy server, no daemon): it fits the
// "self-contained single binary" model — the TLS-Client library is linked in,
// not downloaded or run as a sidecar.
// ============================================================================

const fetchUsage = `Usage:
  zvk fetch [options] <url>

Issue an HTTP request impersonating a real browser's TLS/HTTP2 fingerprint.
The default profile is the latest Chrome, so pages behind anti-bot edges
(Cloudflare, Akamai, ...) return real content instead of a challenge.

Options:
  -X, --method <M>      HTTP method (default GET, or POST when --data is set)
  -H, --header <k: v>   Add/override a request header (repeatable)
  -d, --data <body>     Request body; @file reads the body from a file
  -A, --user-agent <s>  Override the User-Agent header
  -p, --profile <name>  Client profile to impersonate (default: latest Chrome)
  -o, --output <file>   Write the body to <file> instead of stdout
  -i, --include         Print the status line and response headers before body
  -I, --head            Issue a HEAD request and print headers only
      --proxy <url>     Route through proxy (http://user:pass@host:port)
      --timeout <sec>   Request timeout in seconds (default 30)
  -k, --insecure        Skip TLS certificate verification
      --no-follow       Do not follow redirects
      --http1           Force HTTP/1.1
  -f, --fail            Exit non-zero on HTTP status >= 400
  -s, --silent          Suppress the stderr progress/status line
      --list-profiles   List available client profiles and exit
  -h, --help            Show this help

Examples:
  zvk fetch https://example.com
  zvk fetch -i https://api.github.com/repos/zoptia/zvk
  zvk fetch -X POST -H 'Content-Type: application/json' -d '{"a":1}' https://httpbin.org/post
  zvk fetch --profile firefox_133 https://tls.peet.ws/api/all
`

// fetchOptions holds the parsed command line for a single request.
type fetchOptions struct {
	url        string
	method     string
	headers    []string // raw "Key: value" entries, applied in order
	data       string
	hasData    bool
	userAgent  string
	profile    string
	output     string
	include    bool
	head       bool
	proxy      string
	timeoutSec int
	insecure   bool
	noFollow   bool
	http1      bool
	fail       bool
	silent     bool
}

func runFetch(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(stdout, fetchUsage)
		return nil
	}
	opts, listProfiles, showHelp, err := parseFetchArgs(args)
	if err != nil {
		return err
	}
	if showHelp {
		fmt.Fprint(stdout, fetchUsage)
		return nil
	}
	if listProfiles {
		printFetchProfiles(stdout)
		return nil
	}
	if opts.url == "" {
		return usageErrorf("fetch: missing <url> (see `zvk fetch --help`)")
	}
	return doFetch(opts, stdout)
}

// parseFetchArgs is a hand-rolled parser (matching the rest of the codebase's
// switch-based CLI handling) supporting `--flag=value`, `--flag value`, short
// flags, and bare boolean flags. The first non-flag argument is the URL.
func parseFetchArgs(args []string) (opts fetchOptions, listProfiles, showHelp bool, err error) {
	opts.method = ""
	opts.timeoutSec = 30

	// takeValue resolves the value for a flag: either the "=..." suffix already
	// split off, or the following argument.
	for i := 0; i < len(args); i++ {
		a := args[i]
		name, inlineVal, hasInline := a, "", false
		if strings.HasPrefix(a, "--") {
			if eq := strings.IndexByte(a, '='); eq >= 0 {
				name, inlineVal, hasInline = a[:eq], a[eq+1:], true
			}
		}
		needValue := func() (string, error) {
			if hasInline {
				return inlineVal, nil
			}
			if i+1 >= len(args) {
				return "", usageErrorf("fetch: %s needs a value", name)
			}
			i++
			return args[i], nil
		}

		switch name {
		case "-X", "--method":
			if opts.method, err = needValue(); err != nil {
				return
			}
		case "-H", "--header":
			var v string
			if v, err = needValue(); err != nil {
				return
			}
			opts.headers = append(opts.headers, v)
		case "-d", "--data":
			if opts.data, err = needValue(); err != nil {
				return
			}
			opts.hasData = true
		case "-A", "--user-agent":
			if opts.userAgent, err = needValue(); err != nil {
				return
			}
		case "-p", "--profile":
			if opts.profile, err = needValue(); err != nil {
				return
			}
		case "-o", "--output":
			if opts.output, err = needValue(); err != nil {
				return
			}
		case "--proxy":
			if opts.proxy, err = needValue(); err != nil {
				return
			}
		case "--timeout":
			var v string
			if v, err = needValue(); err != nil {
				return
			}
			n, perr := parsePositiveInt(v)
			if perr != nil {
				err = usageErrorf("fetch: --timeout %q: %v", v, perr)
				return
			}
			opts.timeoutSec = n
		case "-i", "--include":
			opts.include = true
		case "-I", "--head":
			opts.head = true
		case "-k", "--insecure":
			opts.insecure = true
		case "--no-follow":
			opts.noFollow = true
		case "--http1":
			opts.http1 = true
		case "-f", "--fail":
			opts.fail = true
		case "-s", "--silent":
			opts.silent = true
		case "--list-profiles":
			listProfiles = true
		case "-h", "--help":
			showHelp = true
		default:
			if strings.HasPrefix(a, "-") && a != "-" {
				err = usageErrorf("fetch: unknown flag '%s'", a)
				return
			}
			if opts.url != "" {
				err = usageErrorf("fetch: unexpected extra argument '%s'", a)
				return
			}
			opts.url = a
		}
	}
	return
}

func doFetch(opts fetchOptions, stdout io.Writer) error {
	profile, ok := resolveProfile(opts.profile)
	if !ok {
		return usageErrorf("fetch: unknown profile '%s' (see `zvk fetch --list-profiles`)", opts.profile)
	}

	clientOpts := []tls_client.HttpClientOption{
		tls_client.WithClientProfile(profile),
		tls_client.WithTimeoutSeconds(opts.timeoutSec),
		tls_client.WithCookieJar(tls_client.NewCookieJar()),
	}
	if opts.noFollow {
		clientOpts = append(clientOpts, tls_client.WithNotFollowRedirects())
	}
	if opts.insecure {
		clientOpts = append(clientOpts, tls_client.WithInsecureSkipVerify())
	}
	if opts.http1 {
		clientOpts = append(clientOpts, tls_client.WithForceHttp1())
	}
	if opts.proxy != "" {
		clientOpts = append(clientOpts, tls_client.WithProxyUrl(opts.proxy))
	}

	client, err := tls_client.NewHttpClient(tls_client.NewNoopLogger(), clientOpts...)
	if err != nil {
		return fmt.Errorf("fetch: build client: %w", err)
	}

	method := opts.method
	if method == "" {
		switch {
		case opts.head:
			method = fhttp.MethodHead
		case opts.hasData:
			method = fhttp.MethodPost
		default:
			method = fhttp.MethodGet
		}
	}
	method = strings.ToUpper(method)

	var body io.Reader
	if opts.hasData {
		payload, err := resolveData(opts.data)
		if err != nil {
			return fmt.Errorf("fetch: --data: %w", err)
		}
		body = strings.NewReader(payload)
	}

	req, err := fhttp.NewRequest(method, opts.url, body)
	if err != nil {
		return fmt.Errorf("fetch: bad request: %w", err)
	}
	req.Header = buildFetchHeaders(opts)

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("fetch: %s %s: %w", method, opts.url, err)
	}
	defer resp.Body.Close()

	if !opts.silent {
		fmt.Fprintf(os.Stderr, "[zvk fetch] %s %s -> %s (profile %s)\n",
			method, opts.url, resp.Status, profileLabel(opts.profile))
	}

	out := stdout
	if opts.output != "" {
		f, err := os.Create(opts.output)
		if err != nil {
			return fmt.Errorf("fetch: open %s: %w", opts.output, err)
		}
		defer f.Close()
		out = f
	}

	if opts.include || opts.head {
		writeResponseHead(out, resp)
	}
	if !opts.head {
		if _, err := io.Copy(out, resp.Body); err != nil {
			return fmt.Errorf("fetch: read body: %w", err)
		}
	}

	if opts.fail && resp.StatusCode >= 400 {
		return fmt.Errorf("fetch: HTTP %s", resp.Status)
	}
	return nil
}

// writeResponseHead prints the status line and headers (in received order) the
// way curl -i / -I does.
func writeResponseHead(w io.Writer, resp *fhttp.Response) {
	fmt.Fprintf(w, "%s %s\n", resp.Proto, resp.Status)
	for _, k := range headerOrder(resp.Header) {
		for _, v := range resp.Header[k] {
			fmt.Fprintf(w, "%s: %s\n", k, v)
		}
	}
	fmt.Fprintln(w)
}

func headerOrder(h fhttp.Header) []string {
	keys := make([]string, 0, len(h))
	for k := range h {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// resolveData returns the request body, reading from a file when the argument
// is "@path" (matching curl's convention).
func resolveData(arg string) (string, error) {
	if strings.HasPrefix(arg, "@") {
		data, err := os.ReadFile(arg[1:])
		if err != nil {
			return "", err
		}
		return string(data), nil
	}
	return arg, nil
}

func parsePositiveInt(s string) (int, error) {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("not a non-negative integer")
		}
		n = n*10 + int(c-'0')
	}
	if s == "" {
		return 0, fmt.Errorf("empty")
	}
	return n, nil
}

// ----------------------------------------------------------------------------
// profiles
// ----------------------------------------------------------------------------

// resolveProfile maps a user-facing profile name to a ClientProfile. The empty
// string and the convenience aliases map to the upstream "latest Chrome"
// default; anything else is looked up by key in MappedTLSClients.
func resolveProfile(name string) (profiles.ClientProfile, bool) {
	switch strings.ToLower(name) {
	case "", "chrome", "chrome-latest", "latest", "default":
		return profiles.DefaultClientProfile, true
	}
	if p, ok := profiles.MappedTLSClients[strings.ToLower(name)]; ok {
		return p, true
	}
	return profiles.ClientProfile{}, false
}

func profileLabel(name string) string {
	if name == "" {
		return "chrome-latest"
	}
	return strings.ToLower(name)
}

func printFetchProfiles(stdout io.Writer) {
	keys := make([]string, 0, len(profiles.MappedTLSClients))
	for k := range profiles.MappedTLSClients {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fmt.Fprintf(stdout, "Default profile: chrome-latest (latest Chrome, tracked by tls-client)\n\n")
	fmt.Fprintln(stdout, "Available profiles (pass with --profile):")
	for _, k := range keys {
		fmt.Fprintf(stdout, "  %s\n", k)
	}
}

// ----------------------------------------------------------------------------
// headers
// ----------------------------------------------------------------------------

// buildFetchHeaders starts from a realistic Chrome header set (with an explicit
// header order, since order itself is fingerprinted) and applies the user's -H
// overrides on top. Accept-Encoding is deliberately omitted so fhttp negotiates
// gzip and transparently decompresses — the body reaches the caller as readable
// text rather than a compressed blob.
func buildFetchHeaders(opts fetchOptions) fhttp.Header {
	ua := opts.userAgent
	if ua == "" {
		ua = chromeUserAgent()
	}

	h := fhttp.Header{
		"sec-ch-ua":                 {chromeSecChUa()},
		"sec-ch-ua-mobile":          {"?0"},
		"sec-ch-ua-platform":        {chromePlatform()},
		"upgrade-insecure-requests": {"1"},
		"user-agent":                {ua},
		"accept":                    {"text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7"},
		"sec-fetch-site":            {"none"},
		"sec-fetch-mode":            {"navigate"},
		"sec-fetch-user":            {"?1"},
		"sec-fetch-dest":            {"document"},
		"accept-language":           {"en-US,en;q=0.9"},
		fhttp.HeaderOrderKey: {
			"sec-ch-ua", "sec-ch-ua-mobile", "sec-ch-ua-platform",
			"upgrade-insecure-requests", "user-agent", "accept",
			"sec-fetch-site", "sec-fetch-mode", "sec-fetch-user",
			"sec-fetch-dest", "accept-language",
		},
	}

	// Apply -H overrides. A header given as "Key:" (empty value) removes it,
	// matching curl's behaviour for suppressing a default header.
	for _, raw := range opts.headers {
		key, val, found := strings.Cut(raw, ":")
		if !found {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		lower := strings.ToLower(key)
		if val == "" {
			delete(h, lower)
			continue
		}
		h[lower] = []string{val}
	}
	return h
}

// chromeUserAgent returns a current-Chrome UA string matching the host OS. The
// Chrome major aligns with profiles.DefaultClientProfile; override with -A if a
// specific build is needed.
func chromeUserAgent() string {
	const ver = "133.0.0.0"
	switch runtime.GOOS {
	case "windows":
		return "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/" + ver + " Safari/537.36"
	case "darwin":
		return "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/" + ver + " Safari/537.36"
	default:
		return "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/" + ver + " Safari/537.36"
	}
}

func chromeSecChUa() string {
	return `"Not(A:Brand";v="99", "Google Chrome";v="133", "Chromium";v="133"`
}

func chromePlatform() string {
	switch runtime.GOOS {
	case "windows":
		return `"Windows"`
	case "darwin":
		return `"macOS"`
	default:
		return `"Linux"`
	}
}
