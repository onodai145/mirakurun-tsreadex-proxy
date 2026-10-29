// Package config loads and validates the proxy's startup configuration.
package config

import (
	"flag"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	defaultIPv4Ranges   = "10.0.0.0/8,127.0.0.0/8,172.16.0.0/12,192.168.0.0/16"
	defaultIPv6Ranges   = "fc00::/7"
	defaultTsreadexArgs = "-x 18/38/39 -b 1 -c 1 -u 2 -d 13"
)

// Config is the validated startup configuration.
type Config struct {
	MirakurunURL     *url.URL
	ListenPort       int
	PublicBaseURL    string
	TsreadexPath     string
	TsreadexArgs     []string
	MaxStreams       int
	FirstByteTimeout time.Duration
	AllowIPv4        []string
	AllowIPv6        []string
	AllowOrigins     []string
	AllowedHostname  string
}

// Load reads the configuration. Each setting comes from a flag (the
// lower-case, hyphenated form of the variable name), then the environment
// variable, then the default.
func Load(args []string, getenv func(string) string) (*Config, error) {
	fs := flag.NewFlagSet("mirakurun-tsreadex-proxy", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	str := func(name, envName, def string) *string {
		v := def
		if e := getenv(envName); e != "" {
			v = e
		}
		return fs.String(name, v, "env: "+envName)
	}
	mirakurunURL := str("mirakurun-url", "MIRAKURUN_URL", "")
	listenPort := str("listen-port", "LISTEN_PORT", "8080")
	publicBase := str("public-base-url", "PUBLIC_BASE_URL", "")
	tsPath := str("tsreadex-path", "TSREADEX_PATH", "tsreadex")
	tsArgs := str("tsreadex-args", "TSREADEX_ARGS", defaultTsreadexArgs)
	maxStreams := str("max-streams", "MAX_STREAMS", "8")
	firstByte := str("first-byte-timeout", "FIRST_BYTE_TIMEOUT", "15s")
	v4 := str("allow-ipv4-cidr-ranges", "ALLOW_IPV4_CIDR_RANGES", defaultIPv4Ranges)
	v6 := str("allow-ipv6-cidr-ranges", "ALLOW_IPV6_CIDR_RANGES", defaultIPv6Ranges)
	origins := str("allow-origins", "ALLOW_ORIGINS", "")
	hostname := str("allowed-hostname", "ALLOWED_HOSTNAME", "")

	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if fs.NArg() > 0 {
		return nil, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}

	c := &Config{
		TsreadexPath:    *tsPath,
		AllowIPv4:       splitCSV(*v4),
		AllowIPv6:       splitCSV(*v6),
		AllowOrigins:    splitCSV(*origins),
		AllowedHostname: *hostname,
	}

	var err error
	if c.MirakurunURL, err = parseBaseURL("MIRAKURUN_URL", *mirakurunURL, true); err != nil {
		return nil, err
	}
	if *publicBase != "" {
		u, err := parseBaseURL("PUBLIC_BASE_URL", *publicBase, false)
		if err != nil {
			return nil, err
		}
		c.PublicBaseURL = u.String()
	}
	if c.ListenPort, err = parseInt("LISTEN_PORT", *listenPort, 1, 65535); err != nil {
		return nil, err
	}
	if c.MaxStreams, err = parseInt("MAX_STREAMS", *maxStreams, 1, 1<<20); err != nil {
		return nil, err
	}
	if c.FirstByteTimeout, err = time.ParseDuration(*firstByte); err != nil || c.FirstByteTimeout <= 0 {
		return nil, fmt.Errorf("FIRST_BYTE_TIMEOUT: %q must be a positive duration such as 15s", *firstByte)
	}
	if c.TsreadexArgs, err = ParseTsreadexArgs(*tsArgs); err != nil {
		return nil, err
	}
	return c, nil
}

func parseInt(name, s string, min, max int) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil || n < min || n > max {
		return 0, fmt.Errorf("%s: %q must be an integer between %d and %d", name, s, min, max)
	}
	return n, nil
}

func parseBaseURL(name, raw string, required bool) (*url.URL, error) {
	if raw == "" {
		if required {
			return nil, fmt.Errorf("%s is required", name)
		}
		return nil, nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("%s: %q must be an http(s) URL with a host", name, raw)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("%s: %q must not contain a query or fragment", name, raw)
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""
	return u, nil
}

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Options that take a value and may be set through TSREADEX_ARGS.
var valueOptions = map[byte]bool{'l': true, 't': true, 'm': true, 'x': true, 'a': true, 'b': true, 'c': true, 'u': true, 'd': true}

// Options the proxy controls itself: -n is derived from the requested
// service, -z/-s/-r are not meaningful for a live pipe.
var proxyOwned = map[byte]bool{'n': true, 'z': true, 's': true, 'r': true}

// ParseTsreadexArgs splits and validates the extra tsreadex arguments.
// Only "-X value" pairs of known options are accepted: combined forms such as
// "-b1" are rejected so that a proxy-owned option can never slip through.
func ParseTsreadexArgs(s string) ([]string, error) {
	tokens := strings.Fields(s)
	for i := 0; i < len(tokens); i++ {
		t := tokens[i]
		if len(t) < 2 || t[0] != '-' {
			return nil, fmt.Errorf("TSREADEX_ARGS: positional argument %q is not allowed", t)
		}
		opt := t[1]
		if proxyOwned[opt] {
			return nil, fmt.Errorf("TSREADEX_ARGS: option -%c is controlled by the proxy", opt)
		}
		if !valueOptions[opt] || len(t) != 2 {
			return nil, fmt.Errorf("TSREADEX_ARGS: unsupported option %q", t)
		}
		i++
		if i >= len(tokens) || strings.HasPrefix(tokens[i], "-") {
			return nil, fmt.Errorf("TSREADEX_ARGS: option %s needs a value", t)
		}
	}
	return tokens, nil
}
