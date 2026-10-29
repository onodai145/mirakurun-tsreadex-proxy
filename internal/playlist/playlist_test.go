package playlist

import (
	"errors"
	"strings"
	"testing"
)

const mirakurunPlaylist = `#EXTM3U url-tvg="http://mirakurun:40772/api/iptv/xmltv"
#KODIPROP:mimetype=video/mp2t
#EXTINF:-1 tvg-id="3273601024" tvg-logo="http://mirakurun:40772/api/services/3273601024/logo" group-title="GR",NHK総合
http://mirakurun:40772/api/services/3273601024/stream
#KODIPROP:mimetype=video/mp2t
#EXTINF:-1 tvg-id="3273701032" group-title="GR",NHK Eテレ
http://mirakurun:40772/api/services/3273701032/stream
`

const mirakurunWant = `#EXTM3U url-tvg="http://proxy:8080/xmltv"
#KODIPROP:mimetype=video/mp2t
#EXTINF:-1 tvg-id="3273601024" tvg-logo="http://proxy:8080/logo/3273601024" group-title="GR",NHK総合
http://proxy:8080/stream/3273601024
#KODIPROP:mimetype=video/mp2t
#EXTINF:-1 tvg-id="3273701032" group-title="GR",NHK Eテレ
http://proxy:8080/stream/3273701032
`

func TestRewriteMirakurunPlaylist(t *testing.T) {
	if got := Rewrite(mirakurunPlaylist, "http://proxy:8080"); got != mirakurunWant {
		t.Errorf("got:\n%s\nwant:\n%s", got, mirakurunWant)
	}
}

func TestRewriteMirakcPlaylist(t *testing.T) {
	// mirakc has no url-tvg attribute and appends a query to the stream URL.
	src := "#EXTM3U\n" +
		`#EXTINF:-1 tvg-id="3273601024" tvg-logo="http://mirakc:40772/api/services/3273601024/logo" group-title="GR",NHK` + "\n" +
		"http://mirakc:40772/api/services/3273601024/stream?decode=1&x=a%20b\n"
	want := "#EXTM3U\n" +
		`#EXTINF:-1 tvg-id="3273601024" tvg-logo="http://proxy:8080/logo/3273601024" group-title="GR",NHK` + "\n" +
		"http://proxy:8080/stream/3273601024?decode=1&x=a%20b\n"
	if got := Rewrite(src, "http://proxy:8080"); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestRewrite_CRLF(t *testing.T) {
	src := "#EXTM3U url-tvg=\"http://m:1/api/iptv/xmltv\"\r\n#EXTINF:-1 tvg-id=\"1\",A\r\nhttp://m:1/api/services/1/stream\r\n"
	want := "#EXTM3U url-tvg=\"http://p/xmltv\"\r\n#EXTINF:-1 tvg-id=\"1\",A\r\nhttp://p/stream/1\r\n"
	if got := Rewrite(src, "http://p"); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRewrite_NoTrailingNewlineAndEmpty(t *testing.T) {
	if got := Rewrite("http://m:1/api/services/7/stream", "http://p"); got != "http://p/stream/7" {
		t.Errorf("got %q", got)
	}
	if got := Rewrite("", "http://p"); got != "" {
		t.Errorf("got %q", got)
	}
	if got := Rewrite("\n\n", "http://p"); got != "\n\n" {
		t.Errorf("got %q", got)
	}
}

func TestRewrite_LeavesOtherLinesAlone(t *testing.T) {
	src := "#EXTM3U\n" +
		"#EXTINF:-1,Other\n" +
		"http://example.com/other.ts\n" +
		"http://m:1/api/services/abc/stream\n" + // non-numeric id
		"http://m:1/api/services/1/stream/extra\n" + // different path
		"#COMMENT http://m:1/api/services/1/stream\n"
	if got := Rewrite(src, "http://p"); got != src {
		t.Errorf("lines that do not match must be unchanged:\n%s", got)
	}
}

func TestRewrite_TrimsTrailingSlashOfBase(t *testing.T) {
	got := Rewrite("http://m:1/api/services/1/stream\n", "http://p:8080/")
	if got != "http://p:8080/stream/1\n" {
		t.Errorf("got %q", got)
	}
}

func TestRewrite_BaseWithSpecialCharsIsLiteral(t *testing.T) {
	got := Rewrite("#EXTM3U url-tvg=\"x\"\nhttp://m:1/api/services/1/stream\n", `http://p/$1\0`)
	if !strings.Contains(got, `url-tvg="http://p/$1\0/xmltv"`) || !strings.Contains(got, `http://p/$1\0/stream/1`) {
		t.Errorf("the base must be inserted literally, got:\n%s", got)
	}
}

func TestReadLimited(t *testing.T) {
	if got, err := ReadLimited(strings.NewReader("12345"), 5); err != nil || got != "12345" {
		t.Errorf("exactly at the limit: got %q, %v", got, err)
	}
	if _, err := ReadLimited(strings.NewReader("123456"), 5); !errors.Is(err, ErrTooLarge) {
		t.Errorf("one byte over the limit: err = %v, want ErrTooLarge", err)
	}
	if got, err := ReadLimited(strings.NewReader(""), 5); err != nil || got != "" {
		t.Errorf("empty: got %q, %v", got, err)
	}
}
