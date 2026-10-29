// Package playlist rewrites the IPTV playlist of Mirakurun/mirakc so that
// every URL points at this proxy.
package playlist

import (
	"errors"
	"io"
	"regexp"
	"strings"
)

// ErrTooLarge is returned by ReadLimited when the input exceeds the limit.
var ErrTooLarge = errors.New("playlist exceeds the size limit")

var (
	reURLTvg = regexp.MustCompile(`url-tvg="[^"]*"`)
	reLogo   = regexp.MustCompile(`tvg-logo="[^"]*/api/services/(\d+)/logo"`)
	reStream = regexp.MustCompile(`^https?://[^/\s]+/api/services/(\d+)/stream(\?\S*)?$`)
)

// ReadLimited reads all of r, failing with ErrTooLarge beyond max bytes.
func ReadLimited(r io.Reader, max int64) (string, error) {
	b, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return "", err
	}
	if int64(len(b)) > max {
		return "", ErrTooLarge
	}
	return string(b), nil
}

// Rewrite points the XMLTV URL, the logo URLs and the stream URLs of src at
// publicBase. Lines that do not match are passed through unchanged, as are
// the line endings.
func Rewrite(src, publicBase string) string {
	base := strings.TrimRight(publicBase, "/")
	lines := strings.Split(src, "\n")
	for i, line := range lines {
		body := strings.TrimRight(line, "\r")
		eol := line[len(body):]
		lines[i] = rewriteLine(body, base) + eol
	}
	return strings.Join(lines, "\n")
}

func rewriteLine(line, base string) string {
	switch {
	case strings.HasPrefix(line, "#EXTM3U"):
		return reURLTvg.ReplaceAllLiteralString(line, `url-tvg="`+base+`/xmltv"`)
	case strings.HasPrefix(line, "#EXTINF"):
		return reLogo.ReplaceAllStringFunc(line, func(m string) string {
			return `tvg-logo="` + base + "/logo/" + reLogo.FindStringSubmatch(m)[1] + `"`
		})
	case strings.HasPrefix(line, "#"):
		return line
	}
	if m := reStream.FindStringSubmatch(line); m != nil {
		return base + "/stream/" + m[1] + m[2]
	}
	return line
}
