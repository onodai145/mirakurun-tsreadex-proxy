package server

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/onodai145/mirakurun-tsreadex-proxy/internal/access"
	"github.com/onodai145/mirakurun-tsreadex-proxy/internal/stream"
	"github.com/onodai145/mirakurun-tsreadex-proxy/internal/testutil"
	"github.com/onodai145/mirakurun-tsreadex-proxy/internal/upstream"
)

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

const upstreamPlaylist = `#EXTM3U url-tvg="http://mirakurun:40772/api/iptv/xmltv"
#EXTINF:-1 tvg-id="3273601024" tvg-logo="http://mirakurun:40772/api/services/3273601024/logo" group-title="GR",NHK
http://mirakurun:40772/api/services/3273601024/stream
`

// fakeUpstream imitates the Mirakurun endpoints the proxy uses and counts requests.
func fakeUpstream(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/iptv/playlist", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/x-mpegURL; charset=utf-8")
		io.WriteString(w, upstreamPlaylist)
	})
	mux.HandleFunc("/api/iptv/xmltv", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/xml")
		io.WriteString(w, "<tv></tv>")
	})
	mux.HandleFunc("/api/services/{id}/logo", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "image/png")
		io.WriteString(w, "PNGDATA")
	})
	mux.HandleFunc("/api/services/{id}/stream", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		io.WriteString(w, "ts-bytes")
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &hits
}

type env struct {
	proxy *httptest.Server
	hits  *atomic.Int32
}

// newEnv wires the real router behind the real access middleware, the way
// main does, and serves it from the given (fake) remote address.
func newEnv(t *testing.T, remote, publicBase string, maxPlaylist int64) env {
	t.Helper()
	up, hits := fakeUpstream(t)
	return newEnvWithUpstream(t, up.URL, hits, remote, publicBase, maxPlaylist)
}

// newEnvWithUpstream is newEnv against an upstream the test provides.
func newEnvWithUpstream(t *testing.T, upstreamURL string, hits *atomic.Int32, remote, publicBase string, maxPlaylist int64) env {
	t.Helper()
	base, _ := url.Parse(upstreamURL)
	streamer, err := stream.New(stream.Config{
		Upstream:         base,
		Tsreadex:         testutil.StubScript(t, "exec cat"),
		MaxStreams:       4,
		FirstByteTimeout: 5 * time.Second,
	}, discard)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := access.NewPolicy(
		[]string{"10.0.0.0/8", "127.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"},
		[]string{"fc00::/7"}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	h := New(Deps{Upstream: base, PublicBaseURL: publicBase, Streamer: streamer, Log: discard, MaxPlaylistBytes: maxPlaylist})
	return env{proxy: testutil.NewServer(t, policy.Middleware(discard, h), remote), hits: hits}
}

func do(t *testing.T, method, url string, header http.Header, host string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range header {
		req.Header[k] = v
	}
	if host != "" {
		req.Host = host
	}
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}, Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

const lan = "192.168.1.5:5555"

func TestPlaylistIsRewrittenToTheRequestHost(t *testing.T) {
	e := newEnv(t, lan, "", 0)
	resp, body := do(t, http.MethodGet, e.proxy.URL+"/playlist.m3u", nil, "")
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	host := strings.TrimPrefix(e.proxy.URL, "http://")
	for _, want := range []string{
		`url-tvg="http://` + host + `/xmltv"`,
		`tvg-logo="http://` + host + `/logo/3273601024"`,
		"http://" + host + "/stream/3273601024",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("playlist missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "mirakurun:40772") {
		t.Errorf("an upstream URL leaked into the playlist:\n%s", body)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/x-mpegURL") {
		t.Errorf("Content-Type = %q", ct)
	}
}

func TestPlaylistUsesPublicBaseURL(t *testing.T) {
	e := newEnv(t, lan, "http://mirakurun-tsreadex-proxy:8080", 0)
	_, body := do(t, http.MethodGet, e.proxy.URL+"/playlist.m3u", nil, "")
	if !strings.Contains(body, "http://mirakurun-tsreadex-proxy:8080/stream/3273601024") {
		t.Errorf("PUBLIC_BASE_URL was not used:\n%s", body)
	}
}

func TestPlaylistTooLargeIs502(t *testing.T) {
	e := newEnv(t, lan, "", 16) // far smaller than the upstream playlist
	resp, body := do(t, http.MethodGet, e.proxy.URL+"/playlist.m3u", nil, "")
	if resp.StatusCode != 502 {
		t.Errorf("status = %d, want 502", resp.StatusCode)
	}
	if strings.Contains(body, "EXTM3U") {
		t.Errorf("a truncated playlist must not be served: %q", body)
	}
}

func TestXMLTVAndLogoPassThrough(t *testing.T) {
	e := newEnv(t, lan, "", 0)
	resp, body := do(t, http.MethodGet, e.proxy.URL+"/xmltv", nil, "")
	if resp.StatusCode != 200 || body != "<tv></tv>" || resp.Header.Get("Content-Type") != "application/xml" {
		t.Errorf("xmltv: status=%d ct=%q body=%q", resp.StatusCode, resp.Header.Get("Content-Type"), body)
	}
	resp, body = do(t, http.MethodGet, e.proxy.URL+"/logo/3273601024", nil, "")
	if resp.StatusCode != 200 || body != "PNGDATA" || resp.Header.Get("Content-Type") != "image/png" {
		t.Errorf("logo: status=%d ct=%q body=%q", resp.StatusCode, resp.Header.Get("Content-Type"), body)
	}
}

func TestPassThroughSendsOwnUserAgentUpstream(t *testing.T) {
	var got atomic.Value
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.Store(r.Header.Get("User-Agent"))
		io.WriteString(w, "<tv></tv>")
	}))
	defer up.Close()
	e := newEnvWithUpstream(t, up.URL, new(atomic.Int32), lan, "", 0)

	resp, _ := do(t, http.MethodGet, e.proxy.URL+"/xmltv", http.Header{"User-Agent": {"Jellyfin/10"}}, "")
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if ua, _ := got.Load().(string); ua != upstream.UserAgent {
		t.Errorf("upstream saw User-Agent %q, want %q", ua, upstream.UserAgent)
	}
}

func TestStreamGetAndHead(t *testing.T) {
	e := newEnv(t, lan, "", 0)
	resp, body := do(t, http.MethodGet, e.proxy.URL+"/stream/3273601024", nil, "")
	if resp.StatusCode != 200 || body != "ts-bytes" || resp.Header.Get("Content-Type") != "video/MP2T" {
		t.Errorf("GET: status=%d ct=%q body=%q", resp.StatusCode, resp.Header.Get("Content-Type"), body)
	}

	before := e.hits.Load()
	resp, body = do(t, http.MethodHead, e.proxy.URL+"/stream/3273601024", nil, "")
	if resp.StatusCode != 200 || body != "" || resp.Header.Get("Content-Type") != "video/MP2T" {
		t.Errorf("HEAD: status=%d ct=%q body=%q", resp.StatusCode, resp.Header.Get("Content-Type"), body)
	}
	if e.hits.Load() != before {
		t.Error("HEAD must not contact the upstream or start tsreadex")
	}
}

func TestRouting(t *testing.T) {
	e := newEnv(t, lan, "", 0)
	cases := []struct {
		method, path string
		want         int
	}{
		{"GET", "/api/config", 404}, // Mirakurun's own management API is not exposed
		{"GET", "/api/tuners", 404},
		{"GET", "/", 404},
		{"GET", "/playlist", 404},
		{"POST", "/playlist.m3u", 405},
		{"DELETE", "/xmltv", 405},
		{"POST", "/stream/1", 405},
		{"PUT", "/logo/1", 405},
		{"GET", "/stream/abc", 400},
		{"GET", "/stream/0", 400},
		{"GET", "/stream/100000", 400},
		{"GET", "/stream/65536", 400},
		{"GET", "/stream/+1", 400},
		{"GET", "/stream/12345678901", 400},
		{"HEAD", "/stream/100000", 400},
		{"GET", "/logo/abc", 400},
		{"GET", "/stream/1/extra", 404},
	}
	for _, tc := range cases {
		resp, _ := do(t, tc.method, e.proxy.URL+tc.path, nil, "")
		if resp.StatusCode != tc.want {
			t.Errorf("%s %s = %d, want %d", tc.method, tc.path, resp.StatusCode, tc.want)
		}
	}
	if e.hits.Load() != 0 {
		t.Errorf("rejected requests reached the upstream %d times", e.hits.Load())
	}
}

func TestMethodNotAllowedAdvertisesAllow(t *testing.T) {
	e := newEnv(t, lan, "", 0)
	resp, _ := do(t, http.MethodPost, e.proxy.URL+"/playlist.m3u", nil, "")
	if resp.Header.Get("Allow") != "GET" {
		t.Errorf("Allow = %q", resp.Header.Get("Allow"))
	}
}

func TestAccessControlCoversEveryEndpoint(t *testing.T) {
	endpoints := []struct{ method, path string }{
		{"GET", "/playlist.m3u"},
		{"GET", "/xmltv"},
		{"GET", "/logo/3273601024"},
		{"GET", "/stream/3273601024"},
		{"HEAD", "/stream/3273601024"},
		{"GET", "/api/config"}, // even paths that do not exist are guarded
	}

	t.Run("source outside the permitted ranges is dropped", func(t *testing.T) {
		e := newEnv(t, "8.8.8.8:1234", "", 0)
		client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}, Timeout: 5 * time.Second}
		for _, ep := range endpoints {
			req, _ := http.NewRequest(ep.method, e.proxy.URL+ep.path, nil)
			if resp, err := client.Do(req); err == nil {
				resp.Body.Close()
				t.Errorf("%s %s answered %d instead of dropping the connection", ep.method, ep.path, resp.StatusCode)
			}
		}
		if e.hits.Load() != 0 {
			t.Error("the upstream must not be contacted")
		}
	})

	t.Run("forbidden Origin, Referer and Host get 403", func(t *testing.T) {
		e := newEnv(t, lan, "", 0)
		bad := []struct {
			name   string
			header http.Header
			host   string
		}{
			{"origin", http.Header{"Origin": {"http://evil.example"}}, ""},
			{"referer", http.Header{"Referer": {"http://evil.example/"}}, ""},
			{"host", nil, "evil.example"},
		}
		for _, ep := range endpoints {
			for _, b := range bad {
				resp, _ := do(t, ep.method, e.proxy.URL+ep.path, b.header, b.host)
				if resp.StatusCode != 403 {
					t.Errorf("%s %s with bad %s = %d, want 403", ep.method, ep.path, b.name, resp.StatusCode)
				}
			}
		}
		if e.hits.Load() != 0 {
			t.Error("the upstream must not be contacted")
		}
	})
}

func TestNoCORSHeaders(t *testing.T) {
	e := newEnv(t, lan, "", 0)
	for _, path := range []string{"/playlist.m3u", "/xmltv", "/logo/1", "/stream/1"} {
		resp, _ := do(t, http.MethodGet, e.proxy.URL+path, http.Header{"Origin": {"http://localhost:3000"}}, "")
		for k := range resp.Header {
			if strings.HasPrefix(strings.ToLower(k), "access-control-") {
				t.Errorf("%s: unexpected CORS header %s", path, k)
			}
		}
	}
}

func TestUpstreamErrorStatusPassesThroughWithoutBody(t *testing.T) {
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "SECRET-internal-detail", http.StatusInternalServerError)
	}))
	defer failing.Close()
	base, _ := url.Parse(failing.URL)
	streamer, err := stream.New(stream.Config{Upstream: base, Tsreadex: testutil.StubScript(t, "exec cat"), MaxStreams: 1, FirstByteTimeout: time.Second}, discard)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(New(Deps{Upstream: base, Streamer: streamer, Log: discard}))
	defer srv.Close()

	for _, path := range []string{"/playlist.m3u", "/xmltv", "/logo/1"} {
		resp, body := do(t, http.MethodGet, srv.URL+path, nil, "")
		if resp.StatusCode != 500 {
			t.Errorf("%s: status = %d, want 500", path, resp.StatusCode)
		}
		if strings.Contains(body, "SECRET") || strings.Contains(body, base.Host) {
			t.Errorf("%s: the upstream detail leaked: %q", path, body)
		}
	}
}

func TestPassThroughCutsConnectionWhenUpstreamBodyIsTruncated(t *testing.T) {
	cases := []struct {
		name string
		sent int
		// A body that fits the server's write buffer never leaves the proxy, so
		// the client only sees a failed request. A larger one is flushed first:
		// the headers (200) reach the client and the body read must then fail.
		headersReachClient bool
	}{
		{"small body, nothing flushed yet", 20, false},
		{"large body, headers already sent", 256 << 10, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var hits atomic.Int32
			// The upstream promises 4 MiB, sends tc.sent bytes and closes the connection.
			truncated := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				w.Header().Set("Content-Type", "application/xml")
				w.Header().Set("Content-Length", "4194304")
				w.Write(bytes.Repeat([]byte("x"), tc.sent))
			}))
			defer truncated.Close()
			e := newEnvWithUpstream(t, truncated.URL, new(atomic.Int32), lan, "", 0)

			client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}, Timeout: 5 * time.Second}
			for _, path := range []string{"/xmltv", "/logo/1"} {
				resp, err := client.Get(e.proxy.URL + path)
				if err != nil {
					if tc.headersReachClient {
						t.Errorf("%s: the headers were already flushed, so Get should succeed: %v", path, err)
					}
					continue // cut before any header was flushed: the client saw a failure
				}
				_, err = io.ReadAll(resp.Body)
				resp.Body.Close()
				if err == nil {
					t.Errorf("%s: a truncated upstream body must cut the connection, not end the response cleanly", path)
				}
			}
			if got := hits.Load(); got != 2 {
				t.Errorf("the proxy reached the upstream %d times, want 2 (once per path)", got)
			}
		})
	}
}
