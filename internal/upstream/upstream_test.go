package upstream

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
)

func TestURL(t *testing.T) {
	cases := []struct {
		base, query string
		elem        []string
		want        string
	}{
		{"http://m:40772", "", []string{"api", "iptv", "playlist"}, "http://m:40772/api/iptv/playlist"},
		{"http://m:40772", "decode=1", []string{"api", "services", "1024", "stream"}, "http://m:40772/api/services/1024/stream?decode=1"},
		{"http://m:40772/prefix", "", []string{"api", "x"}, "http://m:40772/prefix/api/x"},
		{"https://m", "a=b&c=%20", []string{"api", "x"}, "https://m/api/x?a=b&c=%20"},
	}
	for _, tc := range cases {
		base, err := url.Parse(tc.base)
		if err != nil {
			t.Fatal(err)
		}
		if got := URL(base, tc.query, tc.elem...); got != tc.want {
			t.Errorf("URL(%s, %q, %v) = %q, want %q", tc.base, tc.query, tc.elem, got, tc.want)
		}
	}
}

func TestURLDoesNotMutateBase(t *testing.T) {
	base, _ := url.Parse("http://m:40772")
	URL(base, "q=1", "api", "x")
	if base.Path != "" || base.RawQuery != "" {
		t.Errorf("base was modified: %+v", base)
	}
}

func TestClientStatus(t *testing.T) {
	cases := map[int]int{
		200: 502, 204: 502, 301: 502, 302: 502, 304: 502,
		400: 400, 404: 404, 429: 429, 500: 500, 503: 503, 599: 599,
		100: 502, 600: 502,
	}
	for in, want := range cases {
		if got := ClientStatus(in); got != want {
			t.Errorf("ClientStatus(%d) = %d, want %d", in, got, want)
		}
	}
}

func TestClientDoesNotFollowRedirects(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
	}))
	defer target.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redirector.Close()

	resp, err := NewClient().Get(redirector.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Errorf("status = %d, want the redirect response itself (302)", resp.StatusCode)
	}
	if hits.Load() != 0 {
		t.Error("the redirect target must not be requested")
	}
}

func TestClientIgnoresProxyEnvironment(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("http_proxy", "http://127.0.0.1:1")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	// httptest servers are on 127.0.0.1, which Go exempts from proxying, so
	// check the transport setting directly.
	tr, ok := NewClient().Transport.(*http.Transport)
	if !ok || tr.Proxy != nil {
		t.Error("the upstream client must not use proxy settings from the environment")
	}
	if !tr.DisableCompression {
		t.Error("compression must be disabled for an MPEG-TS stream")
	}
}

func TestNewRequestSendsUserAgent(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("User-Agent")
	}))
	defer srv.Close()

	req, err := NewRequest(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := NewClient().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got != UserAgent {
		t.Errorf("User-Agent = %q, want %q", got, UserAgent)
	}
}

func TestUserAgentCarriesTheBuildVersion(t *testing.T) {
	if version == "" {
		t.Fatal("version must default to a non-empty value for builds without -ldflags")
	}
	if want := "mirakurun-tsreadex-proxy/" + version; UserAgent != want {
		t.Errorf("UserAgent = %q, want %q", UserAgent, want)
	}
}
