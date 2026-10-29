// Package server routes the proxy's four endpoints. Access control is applied
// around it by the caller (see access.Policy.Middleware).
package server

import (
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"

	"github.com/onodai145/mirakurun-tsreadex-proxy/internal/playlist"
	"github.com/onodai145/mirakurun-tsreadex-proxy/internal/stream"
	"github.com/onodai145/mirakurun-tsreadex-proxy/internal/upstream"
)

const defaultMaxPlaylistBytes = 8 << 20

// Deps are the collaborators of the router.
type Deps struct {
	Upstream         *url.URL
	PublicBaseURL    string // empty: derive from the (validated) request Host
	Streamer         *stream.Streamer
	Log              *slog.Logger
	MaxPlaylistBytes int64 // 0: 8 MiB
}

type handler struct {
	Deps
	client *http.Client
}

// New returns the router. Only the paths below exist; everything else is 404.
func New(d Deps) http.Handler {
	if d.MaxPlaylistBytes == 0 {
		d.MaxPlaylistBytes = defaultMaxPlaylistBytes
	}
	h := &handler{Deps: d, client: upstream.NewClient()}
	mux := http.NewServeMux()
	mux.HandleFunc("/playlist.m3u", getOnly(h.playlist))
	mux.HandleFunc("/xmltv", getOnly(h.xmltv))
	mux.HandleFunc("/logo/{id}", getOnly(h.logo))
	mux.HandleFunc("/stream/{id}", h.stream)
	return mux
}

func getOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		next(w, r)
	}
}

// fetch requests an upstream path. It sends no client headers, only the
// proxy's own User-Agent. On failure it has already answered the client and
// returns nil.
func (h *handler) fetch(w http.ResponseWriter, r *http.Request, elem ...string) *http.Response {
	req, err := upstream.NewRequest(r.Context(), upstream.URL(h.Upstream, "", elem...))
	if err != nil {
		http.Error(w, "bad gateway", http.StatusBadGateway)
		return nil
	}
	resp, err := h.client.Do(req)
	if err != nil {
		if r.Context().Err() == nil {
			h.Log.Warn("upstream request failed", "error", err)
		}
		http.Error(w, "bad gateway", http.StatusBadGateway)
		return nil
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		resp.Body.Close()
		h.Log.Warn("upstream returned an error status", "status", resp.StatusCode, "path", req.URL.Path)
		code := upstream.ClientStatus(resp.StatusCode)
		http.Error(w, http.StatusText(code), code)
		return nil
	}
	return resp
}

func (h *handler) playlist(w http.ResponseWriter, r *http.Request) {
	resp := h.fetch(w, r, "api", "iptv", "playlist")
	if resp == nil {
		return
	}
	defer resp.Body.Close()
	src, err := playlist.ReadLimited(resp.Body, h.MaxPlaylistBytes)
	if err != nil {
		h.Log.Warn("cannot read the upstream playlist", "error", err)
		http.Error(w, "bad gateway", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/x-mpegURL; charset=utf-8")
	io.WriteString(w, playlist.Rewrite(src, h.publicBase(r)))
}

// publicBase is PUBLIC_BASE_URL, or the scheme and Host of this request. The
// Host has already passed the access middleware; X-Forwarded-* is ignored.
func (h *handler) publicBase(r *http.Request) string {
	if h.PublicBaseURL != "" {
		return h.PublicBaseURL
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func (h *handler) xmltv(w http.ResponseWriter, r *http.Request) {
	h.passThrough(w, r, "application/xml", "api", "iptv", "xmltv")
}

func (h *handler) logo(w http.ResponseWriter, r *http.Request) {
	id, _, err := stream.ParseID(r.PathValue("id"))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	h.passThrough(w, r, "application/octet-stream", "api", "services", itoa(id), "logo")
}

// passThrough relays an upstream response body with its Content-Type.
func (h *handler) passThrough(w http.ResponseWriter, r *http.Request, defaultType string, elem ...string) {
	resp := h.fetch(w, r, elem...)
	if resp == nil {
		return
	}
	defer resp.Body.Close()
	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = defaultType
	}
	w.Header().Set("Content-Type", ct)
	if _, err := io.Copy(w, resp.Body); err != nil {
		// The headers are already sent, so a truncated body would look like a
		// complete one (a partial XMLTV would be imported as the whole guide).
		// Cut the connection instead. A client that went away also lands here,
		// which is harmless: http.ErrAbortHandler is not logged by net/http.
		panic(http.ErrAbortHandler)
	}
}

func (h *handler) stream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id, serviceID, err := stream.ParseID(r.PathValue("id"))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if r.Method == http.MethodHead {
		w.Header().Set("Content-Type", "video/MP2T")
		return
	}
	h.Streamer.Serve(w, r, id, serviceID)
}

func itoa(id uint64) string { return strconv.FormatUint(id, 10) }
