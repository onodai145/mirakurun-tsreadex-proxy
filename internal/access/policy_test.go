package access

import (
	"net/netip"
	"testing"
)

var (
	defaultV4 = []string{"10.0.0.0/8", "127.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"}
	defaultV6 = []string{"fc00::/7"}
)

// newPolicy builds a policy with Mirakurun's default ranges.
func newPolicy(t *testing.T, hostname string, origins ...string) *Policy {
	t.Helper()
	p, err := NewPolicy(defaultV4, defaultV6, hostname, origins)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestNewPolicyRejectsBadRanges(t *testing.T) {
	cases := []struct {
		name   string
		v4, v6 []string
	}{
		{"prefix too long", []string{"10.0.0.0/33"}, nil},
		{"not a cidr", []string{"abc"}, nil},
		{"ipv6 in the ipv4 list", []string{"fc00::/7"}, nil},
		{"ipv4 in the ipv6 list", nil, []string{"10.0.0.0/8"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewPolicy(tc.v4, tc.v6, "", nil); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestPermittedIP(t *testing.T) {
	p := newPolicy(t, "")
	cases := map[string]bool{
		"192.168.1.5":          true,
		"10.1.2.3":             true,
		"172.16.0.1":           true,
		"172.31.255.255":       true,
		"172.32.0.1":           false,
		"8.8.8.8":              false,
		"127.0.0.1":            true,
		"::1":                  false, // not inside fc00::/7
		"fd00::1":              true,
		"2001:db8::1":          false,
		"::ffff:192.168.1.5":   true, // IPv4-mapped IPv6 is judged as IPv4
		"::ffff:8.8.8.8":       false,
		"fe80::1%eth0":         false,
		"fd00::1%eth0":         true, // the zone is ignored
		"203.0.113.9":          false,
		"192.169.0.1":          false,
		"100.64.0.1":           false, // CGNAT is not private
		"0.0.0.0":              false,
		"255.255.255.255":      false,
		"::":                   false,
		"fdff:ffff:ffff::1234": true,
	}
	for s, want := range cases {
		if got := p.PermittedIP(netip.MustParseAddr(s)); got != want {
			t.Errorf("PermittedIP(%s) = %v, want %v", s, got, want)
		}
	}
	if p.PermittedIP(netip.Addr{}) {
		t.Error("the zero Addr must not be permitted")
	}
}

func TestPermittedOrigin(t *testing.T) {
	p := newPolicy(t, "tsreadex-proxy", "https://app.example")
	cases := map[string]bool{
		"http://localhost:3000":      true,
		"http://LOCALHOST":           true,
		"http://192.168.1.10:8080":   true,
		"http://tsreadex-proxy:8080": true,
		"http://TSREADEX-PROXY":      true,
		"http://[fd00::1]:80":        true,
		"http://evil.example":        false,
		"http://8.8.8.8":             false,
		"http://[::1]:8080":          false,
		"null":                       false,
		"":                           false,
		"://":                        false,
		"https://app.example":        true, // exact match with ALLOW_ORIGINS
		"https://app.example/":       false,
		"https://app.example.evil":   false,
	}
	for s, want := range cases {
		if got := p.PermittedOrigin(s); got != want {
			t.Errorf("PermittedOrigin(%q) = %v, want %v", s, got, want)
		}
	}
}

func TestPermittedReferer(t *testing.T) {
	p := newPolicy(t, "", "https://app.example")
	cases := map[string]bool{
		"http://localhost/page":                     true,
		"http://192.168.1.10:8080/a?b=c":            true,
		"http://evil.example/x":                     false,
		"":                                          false,
		"https://app.example":                       true,
		"https://app.example/path":                  true,
		"https://app.example?x=1":                   true,
		"https://app.example#frag":                  true,
		"https://app.example.evil.com/":             false, // must stop at the origin boundary
		"https://app.examplevil/":                   false,
		"https://other.example/https://app.example": false,
	}
	for s, want := range cases {
		if got := p.PermittedReferer(s); got != want {
			t.Errorf("PermittedReferer(%q) = %v, want %v", s, got, want)
		}
	}
}

func TestPermittedHostHeader(t *testing.T) {
	p := newPolicy(t, "")
	withName := newPolicy(t, "tsreadex-proxy")
	cases := []struct {
		host string
		p    *Policy
		want bool
	}{
		{"localhost", p, true},
		{"localhost:8080", p, true},
		{"LOCALHOST:8080", p, true},
		{"127.0.0.1:8080", p, true},
		{"192.168.1.2", p, true},
		{"[fd00::1]:8080", p, true},
		{"[fd00::1]", p, true},
		{"[::1]:8080", p, false},
		{"8.8.8.8", p, false},
		{"evil.example", p, false},
		{"evil.example:80", p, false},
		{"tsreadex-proxy:8080", p, false}, // not configured
		{"tsreadex-proxy:8080", withName, true},
		{"TSREADEX-PROXY", withName, true},
		{"", p, false},
		{"localhost/evil", p, false},
		{"localhost?x", p, false},
		{"user@localhost", p, false},
		{"local host", p, false},
		{"localhost\\evil", p, false},
	}
	for _, tc := range cases {
		if got := tc.p.PermittedHostHeader(tc.host); got != tc.want {
			t.Errorf("PermittedHostHeader(%q) = %v, want %v", tc.host, got, tc.want)
		}
	}
}
