package config

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadDefaults(t *testing.T) {
	c, err := Load(nil, env(map[string]string{"MIRAKURUN_URL": "http://mirakurun:40772"}))
	if err != nil {
		t.Fatal(err)
	}
	if got := c.MirakurunURL.String(); got != "http://mirakurun:40772" {
		t.Errorf("MirakurunURL = %q", got)
	}
	if c.ListenPort != 8080 {
		t.Errorf("ListenPort = %d", c.ListenPort)
	}
	if c.PublicBaseURL != "" {
		t.Errorf("PublicBaseURL = %q", c.PublicBaseURL)
	}
	if c.TsreadexPath != "tsreadex" {
		t.Errorf("TsreadexPath = %q", c.TsreadexPath)
	}
	wantArgs := []string{"-x", "18/38/39", "-b", "1", "-c", "1", "-u", "2", "-d", "13"}
	if !reflect.DeepEqual(c.TsreadexArgs, wantArgs) {
		t.Errorf("TsreadexArgs = %v", c.TsreadexArgs)
	}
	if c.MaxStreams != 8 {
		t.Errorf("MaxStreams = %d", c.MaxStreams)
	}
	if c.FirstByteTimeout != 15*time.Second {
		t.Errorf("FirstByteTimeout = %v", c.FirstByteTimeout)
	}
	wantV4 := []string{"10.0.0.0/8", "127.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"}
	if !reflect.DeepEqual(c.AllowIPv4, wantV4) {
		t.Errorf("AllowIPv4 = %v", c.AllowIPv4)
	}
	if !reflect.DeepEqual(c.AllowIPv6, []string{"fc00::/7"}) {
		t.Errorf("AllowIPv6 = %v", c.AllowIPv6)
	}
	if len(c.AllowOrigins) != 0 || c.AllowedHostname != "" {
		t.Errorf("AllowOrigins = %v, AllowedHostname = %q", c.AllowOrigins, c.AllowedHostname)
	}
}

func TestLoadOverridesAndNormalizes(t *testing.T) {
	c, err := Load(nil, env(map[string]string{
		"MIRAKURUN_URL":          "http://m:40772/",
		"LISTEN_PORT":            "9000",
		"PUBLIC_BASE_URL":        "http://proxy.lan:9000/",
		"TSREADEX_PATH":          "/usr/local/bin/tsreadex",
		"TSREADEX_ARGS":          "-m 2 -t 10",
		"MAX_STREAMS":            "3",
		"FIRST_BYTE_TIMEOUT":     "500ms",
		"ALLOW_IPV4_CIDR_RANGES": "192.168.0.0/16, 10.0.0.0/8",
		"ALLOW_ORIGINS":          "https://a.example,https://b.example",
		"ALLOWED_HOSTNAME":       "tsreadex-proxy",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if got := c.MirakurunURL.String(); got != "http://m:40772" {
		t.Errorf("MirakurunURL = %q (trailing slash must be trimmed)", got)
	}
	if c.PublicBaseURL != "http://proxy.lan:9000" {
		t.Errorf("PublicBaseURL = %q", c.PublicBaseURL)
	}
	if c.ListenPort != 9000 || c.MaxStreams != 3 || c.FirstByteTimeout != 500*time.Millisecond {
		t.Errorf("got %+v", c)
	}
	if !reflect.DeepEqual(c.TsreadexArgs, []string{"-m", "2", "-t", "10"}) {
		t.Errorf("TsreadexArgs = %v", c.TsreadexArgs)
	}
	if !reflect.DeepEqual(c.AllowIPv4, []string{"192.168.0.0/16", "10.0.0.0/8"}) {
		t.Errorf("AllowIPv4 = %v", c.AllowIPv4)
	}
	if !reflect.DeepEqual(c.AllowOrigins, []string{"https://a.example", "https://b.example"}) {
		t.Errorf("AllowOrigins = %v", c.AllowOrigins)
	}
	if c.AllowedHostname != "tsreadex-proxy" {
		t.Errorf("AllowedHostname = %q", c.AllowedHostname)
	}
}

func TestLoadFlagOverridesEnv(t *testing.T) {
	c, err := Load([]string{"-listen-port", "9100", "-mirakurun-url", "http://flag:1"}, env(map[string]string{
		"MIRAKURUN_URL": "http://env:2",
		"LISTEN_PORT":   "8081",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.ListenPort != 9100 || c.MirakurunURL.Host != "flag:1" {
		t.Errorf("got port=%d url=%s", c.ListenPort, c.MirakurunURL)
	}
}

func TestLoadRejectsInvalid(t *testing.T) {
	base := map[string]string{"MIRAKURUN_URL": "http://m:40772"}
	cases := []struct {
		name string
		set  map[string]string
		args []string
	}{
		{"missing MIRAKURUN_URL", map[string]string{}, nil},
		{"ftp scheme", map[string]string{"MIRAKURUN_URL": "ftp://m"}, nil},
		{"no host", map[string]string{"MIRAKURUN_URL": "http://"}, nil},
		{"query in url", map[string]string{"MIRAKURUN_URL": "http://m/?a=1"}, nil},
		{"not a url", map[string]string{"PUBLIC_BASE_URL": "not a url"}, nil},
		{"port 0", map[string]string{"LISTEN_PORT": "0"}, nil},
		{"port too large", map[string]string{"LISTEN_PORT": "70000"}, nil},
		{"port not a number", map[string]string{"LISTEN_PORT": "abc"}, nil},
		{"max streams 0", map[string]string{"MAX_STREAMS": "0"}, nil},
		{"bad timeout", map[string]string{"FIRST_BYTE_TIMEOUT": "soon"}, nil},
		{"zero timeout", map[string]string{"FIRST_BYTE_TIMEOUT": "0s"}, nil},
		{"forbidden tsreadex arg", map[string]string{"TSREADEX_ARGS": "-n 1"}, nil},
		{"positional argument", nil, []string{"extra"}},
		{"unknown flag", nil, []string{"-nope"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := map[string]string{}
			for k, v := range base {
				m[k] = v
			}
			for k, v := range tc.set {
				m[k] = v
			}
			if tc.name == "missing MIRAKURUN_URL" {
				delete(m, "MIRAKURUN_URL")
			}
			if _, err := Load(tc.args, env(m)); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestParseTsreadexArgs(t *testing.T) {
	valid := []string{
		"",
		"-x 18/38/39 -b 1 -c 1 -u 2 -d 13",
		"-m 2 -t 10",
		"  -b   1  ",
	}
	for _, s := range valid {
		if _, err := ParseTsreadexArgs(s); err != nil {
			t.Errorf("ParseTsreadexArgs(%q) = %v, want ok", s, err)
		}
	}

	invalid := map[string]string{
		"-n 1":          "proxy-owned -n",
		"-z x":          "proxy-owned -z",
		"-s 0":          "proxy-owned -s",
		"-r -":          "proxy-owned -r",
		"-n1":           "proxy-owned, combined form",
		"-b 1 -n 5":     "proxy-owned after valid option",
		"-b1":           "combined form is unsupported",
		"foo":           "positional",
		"-":             "stdin marker is added by the proxy",
		"-b":            "missing value",
		"-b -c":         "value looks like an option",
		"-q 1":          "unknown option",
		"-b 1 src.m2ts": "positional after options",
	}
	for s, why := range invalid {
		if _, err := ParseTsreadexArgs(s); err == nil {
			t.Errorf("ParseTsreadexArgs(%q) = ok, want error (%s)", s, why)
		}
	}
}

func TestParseTsreadexArgsErrorMentionsOption(t *testing.T) {
	_, err := ParseTsreadexArgs("-n 1")
	if err == nil || !strings.Contains(err.Error(), "-n") {
		t.Errorf("error should name the option, got %v", err)
	}
}
