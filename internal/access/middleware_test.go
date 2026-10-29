package access

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/onodai145/tsreadex-proxy/internal/testutil"
)

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

// start serves the middleware from the given (fake) remote address and
// returns the server and a counter of how often the inner handler ran.
func start(t *testing.T, p *Policy, remote string, log *slog.Logger) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		io.WriteString(w, "ok")
	})
	return testutil.NewServer(t, p.Middleware(log, inner), remote), &calls
}

func newClient() *http.Client {
	return &http.Client{Transport: &http.Transport{DisableKeepAlives: true}, Timeout: 5 * time.Second}
}

func get(t *testing.T, url string, header http.Header, host string) (*http.Response, error) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range header {
		req.Header[k] = v
	}
	if host != "" {
		req.Host = host
	}
	return newClient().Do(req)
}

func TestMiddlewareAllowsPermittedRequest(t *testing.T) {
	srv, calls := start(t, newPolicy(t, ""), "192.168.1.5:5555", discard)
	resp, err := get(t, srv.URL, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || string(body) != "ok" || calls.Load() != 1 {
		t.Fatalf("status=%d body=%q calls=%d", resp.StatusCode, body, calls.Load())
	}
	if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q", got)
	}
	for k := range resp.Header {
		if strings.HasPrefix(strings.ToLower(k), "access-control-") {
			t.Errorf("unexpected CORS header %s", k)
		}
	}
}

func TestMiddlewareDropsConnectionFromOutsideRange(t *testing.T) {
	srv, calls := start(t, newPolicy(t, ""), "8.8.8.8:1234", discard)
	resp, err := get(t, srv.URL, nil, "")
	if err == nil {
		resp.Body.Close()
		t.Fatalf("expected the connection to be dropped, got status %d", resp.StatusCode)
	}
	if calls.Load() != 0 {
		t.Error("the inner handler must not run")
	}
}

func TestMiddlewareIgnoresForwardedFor(t *testing.T) {
	srv, calls := start(t, newPolicy(t, ""), "8.8.8.8:1234", discard)
	h := http.Header{"X-Forwarded-For": {"192.168.1.1"}, "X-Real-Ip": {"192.168.1.1"}}
	if resp, err := get(t, srv.URL, h, ""); err == nil {
		resp.Body.Close()
		t.Fatal("a spoofed X-Forwarded-For must not unlock a disallowed source")
	}
	if calls.Load() != 0 {
		t.Error("the inner handler must not run")
	}
}

func TestMiddlewareHeaderChecks(t *testing.T) {
	cases := []struct {
		name   string
		header http.Header
		host   string
		want   int
	}{
		{"allowed origin", http.Header{"Origin": {"http://localhost:3000"}}, "", 200},
		{"evil origin", http.Header{"Origin": {"http://evil.example"}}, "", 403},
		{"null origin", http.Header{"Origin": {"null"}}, "", 403},
		{"empty origin", http.Header{"Origin": {""}}, "", 403},
		{"allowed referer", http.Header{"Referer": {"http://192.168.1.10/x"}}, "", 200},
		{"evil referer", http.Header{"Referer": {"http://evil.example/x"}}, "", 403},
		{"evil host (DNS rebinding)", nil, "evil.example", 403},
		{"configured hostname", nil, "tsreadex-proxy:8080", 200},
		{"ipv6 literal host", nil, "[fd00::1]:8080", 200},
		{"unconfigured name", nil, "other-name:8080", 403},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, calls := start(t, newPolicy(t, "tsreadex-proxy"), "192.168.1.5:5555", discard)
			resp, err := get(t, srv.URL, tc.header, tc.host)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tc.want {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tc.want)
			}
			if tc.want == 403 && calls.Load() != 0 {
				t.Error("the inner handler must not run on 403")
			}
		})
	}
}

func TestSourceAllowed(t *testing.T) {
	p := newPolicy(t, "")
	cases := map[string]bool{
		"192.168.1.5:80":          true,
		"[::ffff:192.168.1.5]:80": true, // IPv4-mapped
		"[::ffff:8.8.8.8]:80":     false,
		"[fd00::1]:80":            true,
		"8.8.8.8:80":              false,
		"not-an-address":          false,
		"":                        false,
	}
	for remote, want := range cases {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = remote
		if got := p.sourceAllowed(r); got != want {
			t.Errorf("sourceAllowed(%q) = %v, want %v", remote, got, want)
		}
	}
}

func TestMiddlewareAccessLog(t *testing.T) {
	var buf syncBuffer
	srv, _ := start(t, newPolicy(t, ""), "192.168.1.5:5555", slog.New(slog.NewTextHandler(&buf, nil)))
	resp, err := get(t, srv.URL+"/hello?x=1", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	// The log line is written after the handler returns, so wait for it.
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(buf.String(), "access") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	out := buf.String()
	for _, want := range []string{"method=GET", "status=200", "/hello?x=1", "remote=192.168.1.5:5555"} {
		if !strings.Contains(out, want) {
			t.Errorf("access log missing %q:\n%s", want, out)
		}
	}
}

func TestMiddlewareLogsDenialReason(t *testing.T) {
	var buf syncBuffer
	srv, _ := start(t, newPolicy(t, ""), "192.168.1.5:5555", slog.New(slog.NewTextHandler(&buf, nil)))
	resp, err := get(t, srv.URL+"/probe?x=1", http.Header{"User-Agent": {"scanner/1.0"}}, "evil.example")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	out := buf.String()
	for _, want := range []string{"denied", "reason=host", "remote=192.168.1.5:5555", "method=GET", "/probe?x=1", "scanner/1.0"} {
		if !strings.Contains(out, want) {
			t.Errorf("denial log missing %q:\n%s", want, out)
		}
	}
}

func TestMiddlewareLogsSourceIPDenial(t *testing.T) {
	var buf syncBuffer
	srv, _ := start(t, newPolicy(t, ""), "8.8.8.8:1234", slog.New(slog.NewTextHandler(&buf, nil)))
	if resp, err := get(t, srv.URL+"/probe?x=1", http.Header{"User-Agent": {"scanner/1.0"}}, ""); err == nil {
		resp.Body.Close()
		t.Fatal("expected the connection to be dropped")
	}
	// The denial is logged by the server goroutine, so wait for it.
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(buf.String(), "reason=source-ip") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	out := buf.String()
	for _, want := range []string{"denied", "reason=source-ip", "remote=8.8.8.8:1234", "method=GET", "/probe?x=1", "scanner/1.0"} {
		if !strings.Contains(out, want) {
			t.Errorf("source-ip denial log missing %q:\n%s", want, out)
		}
	}
}

func TestMiddlewareDenialLogIsBoundedAndOmitsHeaderValues(t *testing.T) {
	var buf syncBuffer
	srv, _ := start(t, newPolicy(t, ""), "192.168.1.5:5555", slog.New(slog.NewTextHandler(&buf, nil)))
	long := strings.Repeat("a", 5000)
	resp, err := get(t, srv.URL+"/"+long, http.Header{"User-Agent": {strings.Repeat("b", 5000)}}, "evil.example")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	out := buf.String()
	if !strings.Contains(out, "reason=host") {
		t.Fatalf("the denial was not logged:\n%.300s", out)
	}
	if strings.Contains(out, strings.Repeat("a", 300)) || strings.Contains(out, strings.Repeat("b", 300)) {
		t.Errorf("attacker-controlled URL / User-Agent must be truncated in the denial log, got %d bytes", len(out))
	}
	if !strings.Contains(out, "(truncated)") {
		t.Errorf("a truncated value should be marked as such:\n%.400s", out)
	}
	if strings.Contains(out, "evil.example") {
		t.Errorf("the offending Host value must not be logged:\n%.400s", out)
	}
}
