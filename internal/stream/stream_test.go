package stream

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/onodai145/tsreadex-proxy/internal/testutil"
)

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

// startProxy serves Streamer.Serve at /stream/{id} the way the real router does.
func startProxy(t *testing.T, upstreamURL, exe string, args []string, maxStreams int, firstByte time.Duration) *httptest.Server {
	t.Helper()
	base, err := url.Parse(upstreamURL)
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(Config{Upstream: base, Tsreadex: exe, Args: args, MaxStreams: maxStreams, FirstByteTimeout: firstByte}, discard)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/stream/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, sid, err := ParseID(r.PathValue("id"))
		if err != nil {
			http.Error(w, "bad id", http.StatusBadRequest)
			return
		}
		s.Serve(w, r, id, sid)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// bodyUpstream answers every request with a fixed body.
func bodyUpstream(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// endlessUpstream streams TS-sized chunks until the request is cancelled, and
// closes the returned channel when its handler has returned.
func endlessUpstream(t *testing.T) (*httptest.Server, <-chan struct{}) {
	t.Helper()
	done := make(chan struct{})
	var once atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if once.CompareAndSwap(false, true) {
			defer close(done)
		}
		fl := w.(http.Flusher)
		chunk := bytes.Repeat([]byte{0x47}, 188)
		for r.Context().Err() == nil {
			if _, err := w.Write(chunk); err != nil {
				return
			}
			fl.Flush()
			time.Sleep(5 * time.Millisecond)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, done
}

// waitGone waits for the stub to write its pid, then for that process to be
// gone. A zombie still answers kill(pid, 0), so this also proves it was reaped.
func waitGone(t *testing.T, pidFile string) {
	t.Helper()
	pid := 0
	deadline := time.Now().Add(3 * time.Second)
	for pid == 0 && time.Now().Before(deadline) {
		if b, err := os.ReadFile(pidFile); err == nil {
			if p, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
				pid = p
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if pid == 0 {
		t.Fatal("the stub never wrote its pid")
	}
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); err == syscall.ESRCH {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("process %d is still alive or not reaped", pid)
}

func TestServe_PassesBytesThroughAndHidesClientHeaders(t *testing.T) {
	var gotPath, gotQuery, gotAuth, gotCookie string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		gotAuth, gotCookie = r.Header.Get("Authorization"), r.Header.Get("Cookie")
		io.WriteString(w, "hello-ts-bytes")
	}))
	defer up.Close()
	proxy := startProxy(t, up.URL, testutil.StubScript(t, "exec cat"), nil, 4, 5*time.Second)

	req, _ := http.NewRequest(http.MethodGet, proxy.URL+"/stream/3273601024?decode=1", nil)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Cookie", "session=abc")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != 200 || string(body) != "hello-ts-bytes" {
		t.Fatalf("status=%d body=%q", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "video/MP2T" {
		t.Errorf("Content-Type = %q", ct)
	}
	if gotPath != "/api/services/3273601024/stream" || gotQuery != "decode=1" {
		t.Errorf("upstream saw %q ? %q", gotPath, gotQuery)
	}
	if gotAuth != "" || gotCookie != "" {
		t.Errorf("client headers leaked upstream: Authorization=%q Cookie=%q", gotAuth, gotCookie)
	}
}

func TestServe_RunsTsreadexWithServiceIDAndConfiguredArgs(t *testing.T) {
	argFile := filepath.Join(t.TempDir(), "args")
	stub := testutil.StubScript(t, `printf '%s\n' "$*" > `+argFile+"\nexec cat")
	proxy := startProxy(t, bodyUpstream(t, "x").URL, stub, []string{"-x", "18/38/39", "-b", "1"}, 4, 5*time.Second)

	resp, err := http.Get(proxy.URL + "/stream/3273601024")
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	b, err := os.ReadFile(argFile)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(b)); got != "-n 1024 -x 18/38/39 -b 1 -" {
		t.Errorf("tsreadex args = %q", got)
	}
}

func TestServe_UpstreamErrors(t *testing.T) {
	var redirectHits atomic.Int32
	redirectTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirectHits.Add(1) }))
	defer redirectTarget.Close()

	cases := []struct {
		name    string
		handler http.HandlerFunc
		want    int
	}{
		{"404 passes through without its body", func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "SECRET-internal-detail", http.StatusNotFound)
		}, 404},
		{"503 passes through", func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "busy", http.StatusServiceUnavailable)
		}, 503},
		{"redirect becomes 502 and is not followed", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, redirectTarget.URL, http.StatusFound)
		}, 502},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			up := httptest.NewServer(tc.handler)
			defer up.Close()
			proxy := startProxy(t, up.URL, testutil.StubScript(t, "exec cat"), nil, 4, 5*time.Second)
			resp, err := http.Get(proxy.URL + "/stream/1")
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != tc.want {
				t.Errorf("status = %d, want %d", resp.StatusCode, tc.want)
			}
			if strings.Contains(string(body), "SECRET") {
				t.Errorf("the upstream body leaked: %q", body)
			}
		})
	}
	if redirectHits.Load() != 0 {
		t.Error("the redirect target must not be requested")
	}
}

func TestServe_UpstreamUnreachableIs502(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := up.URL
	up.Close() // now refuses connections
	proxy := startProxy(t, url, testutil.StubScript(t, "exec cat"), nil, 4, 5*time.Second)
	resp, err := http.Get(proxy.URL + "/stream/1")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 502 {
		t.Errorf("status = %d, want 502", resp.StatusCode)
	}
	if strings.Contains(string(body), "127.0.0.1") {
		t.Errorf("the upstream address leaked to the client: %q", body)
	}
}

func TestServe_TsreadexExitsWithoutOutputIs502(t *testing.T) {
	proxy := startProxy(t, bodyUpstream(t, "data").URL, testutil.StubScript(t, "exit 3"), nil, 4, 5*time.Second)
	resp, err := http.Get(proxy.URL + "/stream/1")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 502 {
		t.Errorf("status = %d, want 502", resp.StatusCode)
	}
}

func TestServe_FirstByteTimeoutIs504AndKillsTsreadex(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	stub := testutil.StubScript(t, "echo $$ > "+pidFile+"\nexec sleep 30")
	proxy := startProxy(t, bodyUpstream(t, "data").URL, stub, nil, 4, 300*time.Millisecond)

	start := time.Now()
	resp, err := http.Get(proxy.URL + "/stream/1")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 504 {
		t.Errorf("status = %d, want 504", resp.StatusCode)
	}
	if time.Since(start) > 3*time.Second {
		t.Errorf("took %v, the timeout is 300ms", time.Since(start))
	}
	waitGone(t, pidFile)
}

func TestServe_ClientDisconnectBeforeFirstByte(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	stub := testutil.StubScript(t, "echo $$ > "+pidFile+"\nexec sleep 30")
	up, done := endlessUpstream(t)
	proxy := startProxy(t, up.URL, stub, nil, 4, 30*time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, proxy.URL+"/stream/1", nil)
	if resp, err := http.DefaultClient.Do(req); err == nil {
		resp.Body.Close()
		t.Fatal("expected the client to give up before any output")
	}

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("the upstream request was not cancelled")
	}
	waitGone(t, pidFile)
}

func TestServe_ClientDisconnectKillsTsreadexAndUpstream(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	stub := testutil.StubScript(t, "echo $$ > "+pidFile+"\nexec cat")
	up, done := endlessUpstream(t)
	proxy := startProxy(t, up.URL, stub, nil, 4, 5*time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, proxy.URL+"/stream/1", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(resp.Body, make([]byte, 188)); err != nil {
		t.Fatal(err)
	}
	cancel()
	resp.Body.Close()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("the upstream request was not cancelled")
	}
	waitGone(t, pidFile)
}

func TestServe_MaxStreams(t *testing.T) {
	up, _ := endlessUpstream(t)
	proxy := startProxy(t, up.URL, testutil.StubScript(t, "exec cat"), nil, 1, 5*time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, proxy.URL+"/stream/1", nil)
	first, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Body.Close()
	if _, err := io.ReadFull(first.Body, make([]byte, 188)); err != nil {
		t.Fatal(err)
	}

	second, err := http.Get(proxy.URL + "/stream/2")
	if err != nil {
		t.Fatal(err)
	}
	second.Body.Close()
	if second.StatusCode != 503 {
		t.Errorf("second stream status = %d, want 503", second.StatusCode)
	}
}

func TestServe_HugeStderrDoesNotStall(t *testing.T) {
	// 1 MiB of stderr without a newline: if it were not drained, tsreadex
	// would block on the full pipe and the stream would never start.
	stub := testutil.StubScript(t, "head -c 1048576 /dev/zero | tr '\\0' 'x' >&2\nexec cat")
	proxy := startProxy(t, bodyUpstream(t, "payload").URL, stub, nil, 4, 5*time.Second)
	resp, err := http.Get(proxy.URL + "/stream/1")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || string(body) != "payload" {
		t.Fatalf("status=%d body=%q", resp.StatusCode, body)
	}
}

func TestServe_TsreadexFailureAfterOutputAbortsConnection(t *testing.T) {
	proxy := startProxy(t, bodyUpstream(t, "ts-data").URL, testutil.StubScript(t, "cat\nexit 5"), nil, 4, 5*time.Second)
	resp, err := http.Get(proxy.URL + "/stream/1")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if _, err := io.ReadAll(resp.Body); err == nil {
		t.Fatal("a tsreadex failure after output must cut the connection, not end the stream cleanly")
	}
}

func TestNewFailsWhenTsreadexIsMissing(t *testing.T) {
	base, _ := url.Parse("http://127.0.0.1:1")
	_, err := New(Config{Upstream: base, Tsreadex: "definitely-not-installed-tsreadex", MaxStreams: 1, FirstByteTimeout: time.Second}, discard)
	if err == nil {
		t.Fatal("expected an error for a missing executable")
	}
}

func TestLineWriter(t *testing.T) {
	var buf bytes.Buffer
	lw := newLineWriter(slog.New(slog.NewTextHandler(&buf, nil)), 1024)

	lw.Write([]byte("first line\nsecond"))
	lw.Write([]byte(" line\n"))
	lw.Write([]byte(strings.Repeat("x", 100000))) // no newline: must be truncated, not grow without bound
	lw.Write([]byte("\npartial"))
	lw.Flush()

	out := buf.String()
	for _, want := range []string{"first line", "second line", "truncated", "partial"} {
		if !strings.Contains(out, want) {
			t.Errorf("log missing %q:\n%s", want, out)
		}
	}
	if len(out) > 20000 {
		t.Errorf("log output is %d bytes, the long line must be truncated", len(out))
	}
}
