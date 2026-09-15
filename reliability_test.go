package main

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func putFile(t *testing.T, name, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestUnsafeNames(t *testing.T) {
	for _, name := range []string{"", ".", "..", "../outside", `..\outside`, "/tmp/key", `C:\key`, "key:stream", "NUL", "COM1.txt", "COM¹.txt", "NUL .txt", "name.", "name ", "key\nname"} {
		t.Run(fmt.Sprintf("%q", name), func(t *testing.T) {
			if validateName("name", name) == nil {
				t.Fatal("accepted unsafe name")
			}
			if name != "" {
				if err := runSSHKeygen([]string{"--name", name}, io.Discard); err == nil {
					t.Fatal("keygen accepted unsafe name")
				}
			}
			if _, err := requireName([]string{name}, "usage"); err == nil {
				t.Fatal("accepted unsafe SSH name")
			}
			if err := toolchainUninstall(zigTC, name, io.Discard); err == nil {
				t.Fatal("uninstall accepted unsafe version")
			}
		})
	}
	for _, name := range []string{"go1.25.0", "0.16.0-dev.123+abcd", "id_ed25519_work", "v24.1.0"} {
		if err := validateName("name", name); err != nil {
			t.Fatal(err)
		}
	}
}

func tarBytes(t *testing.T, headers ...*tar.Header) []byte {
	t.Helper()
	var b bytes.Buffer
	w := tar.NewWriter(&b)
	for _, h := range headers {
		if h.Mode == 0 {
			h.Mode = 0o755
		}
		if err := w.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Size > 0 {
			if _, err := w.Write(bytes.Repeat([]byte("x"), int(h.Size))); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestArchiveConfinement(t *testing.T) {
	for _, entries := range [][]*tar.Header{
		{{Name: "../outside", Typeflag: tar.TypeReg, Size: 1}},
		{{Name: `..\outside`, Typeflag: tar.TypeReg, Size: 1}},
		{{Name: "/outside", Typeflag: tar.TypeReg, Size: 1}},
		{{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "../outside"}, {Name: "link/file", Typeflag: tar.TypeReg, Size: 1}},
		{{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "/tmp"}},
		{{Name: "link", Typeflag: tar.TypeLink, Linkname: "../outside"}},
	} {
		dest := t.TempDir()
		if err := extractTar(bytes.NewReader(tarBytes(t, entries...)), dest, 0); err == nil {
			t.Fatalf("accepted unsafe archive: %+v", entries[0])
		}
	}
	// Confinement also applies to symlinks that were already present.
	if runtime.GOOS == "windows" {
		return
	}
	outside, dest := t.TempDir(), t.TempDir()
	putFile(t, filepath.Join(outside, "keep"), "original")
	if err := os.Symlink(outside, filepath.Join(dest, "link")); err != nil {
		t.Fatal(err)
	}
	data := tarBytes(t, &tar.Header{Name: "link/keep", Typeflag: tar.TypeReg, Size: 1})
	if err := extractTar(bytes.NewReader(data), dest, 0); err == nil {
		t.Fatal("wrote through escaping symlink")
	}
	got, _ := os.ReadFile(filepath.Join(outside, "keep"))
	if string(got) != "original" {
		t.Fatal("outside file modified")
	}
}

func TestArchiveInternalLinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unprivileged Windows cannot create symlinks")
	}
	dest := t.TempDir()
	data := tarBytes(t,
		&tar.Header{Name: "pkg/lib/tool", Typeflag: tar.TypeReg, Size: 3},
		&tar.Header{Name: "pkg/bin/tool", Typeflag: tar.TypeSymlink, Linkname: "../lib/tool"},
		&tar.Header{Name: "pkg/hard", Typeflag: tar.TypeLink, Linkname: "pkg/lib/tool"},
	)
	if err := extractTar(bytes.NewReader(data), dest, 1); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"lib/tool", "bin/tool", "hard"} {
		got, err := os.ReadFile(filepath.Join(dest, name))
		if err != nil || string(got) != "xxx" {
			t.Fatalf("%s: %q %v", name, got, err)
		}
	}
}

func zipBytes(t *testing.T, names ...string) []byte {
	t.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for _, name := range names {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte("payload")); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestZipConfinement(t *testing.T) {
	for _, name := range []string{"../outside", `..\outside`, "C:/outside", "/outside"} {
		if err := extractZip(zipBytes(t, name), t.TempDir(), 0); err == nil {
			t.Fatalf("accepted %s", name)
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func testToolchain() *toolchain {
	return &toolchain{
		name: "test", dirs: toolDirs{name: "test"}, channels: []channelInfo{{name: "stable"}}, defaultChannel: "stable",
		isInstalled: func(dir string) bool { return fileExists(filepath.Join(dir, "tool")) },
		bins: func(string) []binSpec {
			return []binSpec{{link: "test-tool", exe: func() string { return "tool" }}, {link: "test-helper", exe: func() string { return "helper" }}}
		},
	}
}

func TestInstallFailureRetryAndActivation(t *testing.T) {
	root := t.TempDir()
	t.Setenv("ZVK_ROOT", root)
	t.Setenv("ZVK_NO_MODIFY_PATH", "1")
	tc := testToolchain()
	data := zipBytes(t, "tool", "../escape")
	asset := toolAsset{version: "1.0", url: "https://unused.invalid/archive.zip", filename: "archive.zip"}
	tc.resolve = func(string, string) (toolAsset, error) { asset.sha256 = sha256Hex(data); return asset, nil }
	oldClient := httpClient
	httpClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(data))}, nil
	})}
	defer func() { httpClient = oldClient }()
	if err := toolchainInstall(tc, "stable", "latest", io.Discard); err == nil {
		t.Fatal("accepted broken archive")
	}
	if pathExists(tc.dirs.versionDir(root, "1.0")) {
		t.Fatal("published partial install")
	}
	if active, err := tc.dirs.readActive(root, "stable"); err != nil || active != "" {
		t.Fatalf("activated broken install: %q %v", active, err)
	}
	// A valid archive missing the helper must not be published either.
	data = zipBytes(t, "tool")
	if err := toolchainInstall(tc, "stable", "latest", io.Discard); err == nil {
		t.Fatal("accepted incomplete payload")
	}
	// Repair a partial installation left by an older release.
	putFile(t, filepath.Join(tc.dirs.versionDir(root, "1.0"), "tool"), "partial")
	data = zipBytes(t, "tool", "helper")
	if err := toolchainInstall(tc, "stable", "latest", io.Discard); err != nil {
		t.Fatal(err)
	}
	if active, err := tc.dirs.readActive(root, "stable"); err != nil || active != "1.0" {
		t.Fatalf("activation: %q %v", active, err)
	}
	if err := toolchainUninstall(tc, "1.0", io.Discard); err == nil {
		t.Fatal("removed active version")
	}
	// A malformed upstream version must fail before making a request.
	tc.resolve = func(string, string) (toolAsset, error) { return toolAsset{version: "../escape"}, nil }
	if err := toolchainInstall(tc, "stable", "latest", io.Discard); err == nil {
		t.Fatal("accepted unsafe upstream version")
	}
	entries, err := os.ReadDir(filepath.Join(root, tc.name))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".install-") {
			t.Fatalf("left staging directory: %s", e.Name())
		}
	}
}

func TestActivationFailurePreservesOldVersion(t *testing.T) {
	root := t.TempDir()
	tc := testToolchain()
	for _, version := range []string{"1.0", "2.0"} {
		for _, name := range []string{"tool", "helper"} {
			putFile(t, filepath.Join(tc.dirs.versionDir(root, version), name), version)
		}
	}
	if err := tc.activate(root, "stable", "1.0"); err != nil {
		t.Fatal(err)
	}
	obstruction := filepath.Join(binDir(root), binEntryName("test-helper"))
	if err := os.Remove(obstruction); err != nil {
		t.Fatal(err)
	}
	putFile(t, filepath.Join(obstruction, "keep"), "keep")
	if err := tc.activate(root, "stable", "2.0"); err == nil {
		t.Fatal("replaced non-empty directory")
	}
	active, err := tc.dirs.readActive(root, "stable")
	if err != nil || active != "1.0" {
		t.Fatalf("lost original mapping: %q %v", active, err)
	}
	if !fileExists(filepath.Join(obstruction, "keep")) {
		t.Fatal("deleted obstruction")
	}
}

func TestVersionSymlinkRefused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unprivileged Windows cannot create symlinks")
	}
	root, outside := t.TempDir(), t.TempDir()
	t.Setenv("ZVK_ROOT", root)
	tc := testToolchain()
	putFile(t, filepath.Join(outside, "tool"), "keep")
	if err := os.MkdirAll(tc.dirs.versionsDir(root), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, tc.dirs.versionDir(root, "1.0")); err != nil {
		t.Fatal(err)
	}
	if err := toolchainUninstall(tc, "1.0", io.Discard); err == nil {
		t.Fatal("accepted version symlink")
	}
	if !fileExists(filepath.Join(outside, "tool")) {
		t.Fatal("deleted outside file")
	}
}

func TestLockHelper(t *testing.T) {
	root := os.Getenv("ZVK_TEST_LOCK_ROOT")
	if root == "" {
		return
	}
	f, err := lockToolchain(root, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	fmt.Println("locked")
	var b [1]byte
	os.Stdin.Read(b[:])
}

func TestProcessLockReleasedAfterCrash(t *testing.T) {
	root := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestLockHelper$")
	cmd.Env = append(os.Environ(), "ZVK_TEST_LOCK_ROOT="+root)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil || line != "locked\n" {
		t.Fatalf("lock helper: %q %v", line, err)
	}
	if f, err := lockToolchain(root, "test"); err == nil {
		f.Close()
		t.Fatal("acquired held lock")
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	cmd.Wait()
	f, err := lockToolchain(root, "test")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
}

func TestReceiveConcurrentNames(t *testing.T) {
	dir := t.TempDir()
	putFile(t, filepath.Join(dir, "file.txt"), "original")
	putFile(t, filepath.Join(dir, "file-001.txt"), "also original")
	h := newReceiveHandler(serveOptions{quiet: true, maxBytes: 1024}, dir, true, func() {})
	const count = 20
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("POST", "/file.txt", strings.NewReader(fmt.Sprintf("body-%d", i))))
			if w.Code != 200 {
				t.Errorf("upload: %d %s", w.Code, w.Body.String())
			}
		}(i)
	}
	wg.Wait()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != count+2 {
		t.Fatalf("lost uploads: got %d files", len(entries))
	}
	contents := map[string]bool{}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		contents[string(b)] = true
	}
	if len(contents) != count+2 || !contents["original"] || !contents["also original"] {
		t.Fatalf("overwritten bodies: %v", contents)
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("connection interrupted") }

func TestReceiveFailureAndLimitPreserveDestination(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "out")
	putFile(t, dest, "original")
	if _, _, err := receiveBody(dest, false, "/", 1, io.MultiReader(strings.NewReader("partial"), failingReader{})); err == nil {
		t.Fatal("accepted failed read")
	}
	var stops atomic.Int32
	h := newReceiveHandler(serveOptions{quiet: true, once: true, maxBytes: 3}, dest, false, func() { stops.Add(1) })
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/", strings.NewReader("large")))
	if w.Code != 413 || stops.Load() != 0 {
		t.Fatalf("limit handling: %d, stops %d", w.Code, stops.Load())
	}
	b, _ := os.ReadFile(dest)
	if string(b) != "original" {
		t.Fatal("overwrote destination on failure")
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("PUT", "/", strings.NewReader("new")))
	if w.Code != 200 || stops.Load() != 1 {
		t.Fatalf("retry: %d stops %d", w.Code, stops.Load())
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/", strings.NewReader("bad")))
	if w.Code != 409 {
		t.Fatalf("accepted second transfer: %d", w.Code)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("left partial uploads: %v", entries)
	}
}

type blockingReader struct {
	started chan struct{}
	release chan struct{}
	sent    bool
}

func (b *blockingReader) Read(p []byte) (int, error) {
	if b.sent {
		return 0, io.EOF
	}
	close(b.started)
	<-b.release
	b.sent = true
	return copy(p, "body"), nil
}

func TestReceiveOnceRejectsConcurrentTransfer(t *testing.T) {
	var stops atomic.Int32
	h := newReceiveHandler(serveOptions{quiet: true, once: true, maxBytes: 1024}, t.TempDir(), true, func() { stops.Add(1) })
	body := &blockingReader{started: make(chan struct{}), release: make(chan struct{})}
	done := make(chan struct{})
	first := httptest.NewRecorder()
	go func() { defer close(done); h.ServeHTTP(first, httptest.NewRequest("POST", "/one", body)) }()
	<-body.started
	second := httptest.NewRecorder()
	h.ServeHTTP(second, httptest.NewRequest("POST", "/two", strings.NewReader("second")))
	close(body.release)
	<-done
	if second.Code != 409 || first.Code != 200 || stops.Load() != 1 {
		t.Fatalf("first=%d second=%d stops=%d", first.Code, second.Code, stops.Load())
	}
}

func TestActivationRollsBackAfterWriteFailure(t *testing.T) {
	root := t.TempDir()
	tc := testToolchain()
	if err := tc.activate(root, "stable", "1.0"); err != nil {
		t.Fatal(err)
	}
	primary := filepath.Join(binDir(root), binEntryName("test-tool"))
	before, err := snapshotEntry(primary)
	if err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(binDir(root), binEntryName("test-helper"))
	if err := os.Remove(helper); err != nil {
		t.Fatal(err)
	}
	// Inject an obstruction after preflight and after the first bin was replaced.
	tc.bins = func(string) []binSpec {
		return []binSpec{
			{link: "test-tool", exe: func() string { return "new-tool" }},
			{link: "test-helper", exe: func() string {
				if err := os.Mkdir(helper, 0o755); err != nil {
					t.Fatal(err)
				}
				return "helper"
			}},
		}
	}
	if err := tc.activate(root, "stable", "2.0"); err == nil {
		t.Fatal("expected write failure")
	}
	active, err := tc.dirs.readActive(root, "stable")
	if err != nil || active != "1.0" {
		t.Fatalf("lost channel: %q %v", active, err)
	}
	after, err := snapshotEntry(primary)
	if err != nil {
		t.Fatal(err)
	}
	if after.link != before.link || !bytes.Equal(after.data, before.data) {
		t.Fatal("did not restore primary entry")
	}
	if pathExists(helper) {
		t.Fatal("did not restore missing helper")
	}
}

func TestAtomicSymlinkPreservesDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unprivileged Windows cannot create symlinks")
	}
	dir := t.TempDir()
	entry := filepath.Join(dir, "entry")
	putFile(t, filepath.Join(entry, "keep"), "keep")
	if err := replaceSymlink("new-target", entry); err == nil {
		t.Fatal("replaced directory")
	}
	if !fileExists(filepath.Join(entry, "keep")) {
		t.Fatal("deleted existing directory")
	}
}

func TestChannelValidation(t *testing.T) {
	dir := t.TempDir()
	if err := setActiveVersion(dir, "../escape", "1.0"); err == nil {
		t.Fatal("accepted unsafe channel")
	}
	if err := setActiveVersion(dir, "stable", "../escape"); err == nil {
		t.Fatal("accepted unsafe version")
	}
	if _, err := readActiveVersion(dir, "../escape"); err == nil {
		t.Fatal("read unsafe channel")
	}
	if runtime.GOOS == "windows" {
		putFile(t, filepath.Join(dir, "stable.txt"), "../escape")
	} else {
		if err := os.Symlink("../../escape", filepath.Join(dir, "stable")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := readActiveVersion(dir, "stable"); err == nil {
		t.Fatal("accepted corrupted channel")
	}
}

func TestReceiveMethodsAndNames(t *testing.T) {
	dir := t.TempDir()
	h := newReceiveHandler(serveOptions{quiet: true, maxBytes: 1024}, dir, true, func() {})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != 405 || w.Header().Get("Allow") != "POST, PUT" {
		t.Fatal("missing method rejection")
	}
	for _, name := range []string{"/../", "/NUL", `/..\outside`, "/file:stream", "/.zvk-upload-private"} {
		w = httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("POST", name, strings.NewReader("data")))
		if w.Code != 200 {
			t.Fatalf("name %q: %d", name, w.Code)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "body-") {
			t.Fatalf("unsafe name preserved: %s", e.Name())
		}
	}
}

func TestServeLimitArguments(t *testing.T) {
	opts, _, err := parseServeArgs([]string{"--receive", "out"})
	if err != nil || opts.maxBytes != 1<<30 {
		t.Fatalf("default limit: %+v %v", opts, err)
	}
	for _, value := range []string{"-1", "abc", "9223372036854775808"} {
		if _, _, err := parseServeArgs([]string{"--max-bytes", value}); err == nil {
			t.Fatalf("accepted %q", value)
		}
	}
	opts, _, err = parseServeArgs([]string{"--max-bytes=0"})
	if err != nil || opts.maxBytes != 0 {
		t.Fatal("cannot disable limit")
	}
}

type failedResponse struct{ http.ResponseWriter }

func (w failedResponse) Write([]byte) (int, error) { return 0, errors.New("client disconnected") }

func TestTransferSuccess(t *testing.T) {
	for _, status := range []int{200, 206, 301, 404, 500} {
		w := &transferResponse{ResponseWriter: httptest.NewRecorder()}
		w.WriteHeader(status)
		w.Write([]byte("body"))
		if w.successful() != (status == 200 || status == 206) {
			t.Fatalf("status %d", status)
		}
	}
	w := &transferResponse{ResponseWriter: failedResponse{httptest.NewRecorder()}}
	w.Write([]byte("body"))
	if w.successful() {
		t.Fatal("disconnected client counted as successful")
	}
}

// pipeListener exercises the real HTTP server lifecycle without opening a port.
type pipeListener struct {
	conn     net.Conn
	accepted bool
	closed   chan struct{}
	once     sync.Once
}

func (l *pipeListener) Accept() (net.Conn, error) {
	if !l.accepted {
		l.accepted = true
		return l.conn, nil
	}
	<-l.closed
	return nil, net.ErrClosed
}
func (l *pipeListener) Close() error   { l.once.Do(func() { close(l.closed) }); return nil }
func (l *pipeListener) Addr() net.Addr { return &net.TCPAddr{} }

func TestShutdownWaitsForResponse(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	if err := client.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	ln := &pipeListener{conn: server, closed: make(chan struct{})}
	srv := &http.Server{}
	stop, stopped := serverStop(srv)
	started, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	srv.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "saved")
		stop()
		close(started)
		<-release
	})
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()
	defer srv.Close()
	response := make(chan string, 1)
	go func() {
		if _, err := fmt.Fprint(client, "GET / HTTP/1.1\r\nHost: localhost\r\nConnection: close\r\n\r\n"); err != nil {
			response <- err.Error()
			return
		}
		resp, err := http.ReadResponse(bufio.NewReader(client), nil)
		if err != nil {
			response <- err.Error()
			return
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			response <- err.Error()
			return
		}
		response <- string(body)
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not start")
	}
	select {
	case <-stopped:
		t.Fatal("shutdown finished before handler")
	default:
	}
	unblock()
	if got := <-response; got != "saved" {
		t.Fatalf("response was cut short: %q", got)
	}
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown did not complete")
	}
	if err := <-serveErr; !errors.Is(err, http.ErrServerClosed) {
		t.Fatal(err)
	}
}
