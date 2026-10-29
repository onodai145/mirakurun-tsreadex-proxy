// Package upstream holds what every request to Mirakurun/mirakc has in common.
package upstream

import (
	"net/http"
	"net/url"
	"path"
)

// NewClient returns the HTTP client for upstream requests: no redirect
// following, no proxy from the environment, no transparent compression. It
// has no overall timeout because stream responses are long-lived; callers
// bound requests with a context.
func NewClient() *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = nil
	t.DisableCompression = true
	return &http.Client{
		Transport:     t,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// URL appends elem to the path of base and sets the raw query. Only the
// caller-validated elements are placed in the path, and base is not modified.
func URL(base *url.URL, rawQuery string, elem ...string) string {
	u := *base
	u.Path = path.Join(append([]string{"/", base.Path}, elem...)...)
	u.RawPath = ""
	u.RawQuery = rawQuery
	u.Fragment = ""
	return u.String()
}

// ClientStatus is the status returned to the client for a non-2xx upstream
// response: 4xx/5xx are passed through, anything else (such as a redirect)
// becomes 502.
func ClientStatus(code int) int {
	if code >= 400 && code <= 599 {
		return code
	}
	return http.StatusBadGateway
}
