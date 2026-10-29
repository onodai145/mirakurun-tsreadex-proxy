// Package access implements the access control that Mirakurun applies to
// its own API (source IP ranges, Origin/Referer checks against DNS rebinding
// and CSRF), plus the Host check and the listen-address selection.
package access

import (
	"fmt"
	"net/netip"
	"net/url"
	"strings"
)

// Policy decides which clients, origins and hostnames are permitted.
type Policy struct {
	v4, v6   []netip.Prefix
	hostname string
	origins  []string
}

// NewPolicy parses the CIDR ranges. hostname is the extra hostname clients
// use to reach the proxy; origins are extra permitted Origin/Referer values.
// Empty entries are ignored (an empty origin would match every Referer that
// starts with "/", "?" or "#"), and the slice is copied.
func NewPolicy(v4, v6 []string, hostname string, origins []string) (*Policy, error) {
	p := &Policy{hostname: strings.ToLower(hostname)}
	for _, o := range origins {
		if o = strings.TrimSpace(o); o != "" {
			p.origins = append(p.origins, o)
		}
	}
	for _, s := range v4 {
		pf, err := netip.ParsePrefix(s)
		if err != nil || !pf.Addr().Is4() {
			return nil, fmt.Errorf("invalid IPv4 CIDR range %q", s)
		}
		p.v4 = append(p.v4, pf.Masked())
	}
	for _, s := range v6 {
		pf, err := netip.ParsePrefix(s)
		if err != nil || !pf.Addr().Is6() {
			return nil, fmt.Errorf("invalid IPv6 CIDR range %q", s)
		}
		p.v6 = append(p.v6, pf.Masked())
	}
	return p, nil
}

// PermittedIP reports whether a is inside one of the permitted ranges.
// IPv4-mapped IPv6 addresses are judged as IPv4; a zone is ignored.
func (p *Policy) PermittedIP(a netip.Addr) bool {
	a = a.Unmap().WithZone("")
	prefixes := p.v6
	if a.Is4() {
		prefixes = p.v4
	}
	for _, pf := range prefixes {
		if pf.Contains(a) {
			return true
		}
	}
	return false
}

// hostnameAllowed mirrors Mirakurun's isPermittedHost: localhost, the
// configured hostname, or an IP address inside the permitted ranges.
func (p *Policy) hostnameAllowed(h string) bool {
	if h == "" {
		return false
	}
	h = strings.ToLower(h)
	if h == "localhost" || (p.hostname != "" && h == p.hostname) {
		return true
	}
	if a, err := netip.ParseAddr(h); err == nil {
		return p.PermittedIP(a)
	}
	return false
}

// PermittedOrigin checks an Origin header value.
func (p *Policy) PermittedOrigin(raw string) bool {
	if u, err := url.Parse(raw); err == nil && u.Host != "" && p.hostnameAllowed(u.Hostname()) {
		return true
	}
	for _, o := range p.origins {
		if raw == o {
			return true
		}
	}
	return false
}

// PermittedReferer checks a Referer header value. Extra origins match as a
// prefix, but only at the origin boundary, so that "https://a.example" does
// not match "https://a.example.evil.com/".
func (p *Policy) PermittedReferer(raw string) bool {
	if u, err := url.Parse(raw); err == nil && u.Host != "" && p.hostnameAllowed(u.Hostname()) {
		return true
	}
	for _, o := range p.origins {
		if raw == o {
			return true
		}
		if strings.HasPrefix(raw, o) && strings.ContainsRune("/?#", rune(raw[len(o)])) {
			return true
		}
	}
	return false
}

// PermittedHostHeader checks the Host request header ("name", "name:port",
// "[v6]:port"). This is the proxy's addition to Mirakurun's checks.
func (p *Policy) PermittedHostHeader(host string) bool {
	if host == "" || strings.ContainsAny(host, "/?#@\\ ") {
		return false
	}
	u, err := url.Parse("//" + host)
	if err != nil {
		return false
	}
	return p.hostnameAllowed(u.Hostname())
}
