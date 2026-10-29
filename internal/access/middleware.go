package access

import (
	"log/slog"
	"net/http"
	"net/netip"
	"time"
)

// Middleware applies the access checks in Mirakurun's order (source IP,
// Origin, Referer) plus the Host check, then logs the request. A request from
// outside the permitted ranges is dropped without any response.
func (p *Policy) Middleware(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !p.sourceAllowed(r) {
			log.Warn("denied", "reason", "source-ip", "remote", r.RemoteAddr)
			dropConnection(w)
			return
		}
		if reason := p.headerViolation(r); reason != "" {
			log.Warn("denied", "reason", reason, "remote", r.RemoteAddr)
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}

		w.Header().Set("X-Content-Type-Options", "nosniff")
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		// Logged from a defer so that an aborted response (panic with
		// http.ErrAbortHandler) is still recorded.
		defer func() {
			log.Info("access",
				"remote", r.RemoteAddr,
				"method", r.Method,
				"url", r.URL.RequestURI(),
				"status", rec.status,
				"duration", time.Since(start),
				"user_agent", r.UserAgent())
		}()
		next.ServeHTTP(rec, r)
	})
}

// sourceAllowed judges the connection's own address; forwarding headers such
// as X-Forwarded-For are never consulted.
func (p *Policy) sourceAllowed(r *http.Request) bool {
	ap, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	return p.PermittedIP(ap.Addr())
}

// headerViolation returns the name of the first failed header check, or "".
func (p *Policy) headerViolation(r *http.Request) string {
	if v, ok := r.Header["Origin"]; ok && (len(v) == 0 || !p.PermittedOrigin(v[0])) {
		return "origin"
	}
	if v, ok := r.Header["Referer"]; ok && (len(v) == 0 || !p.PermittedReferer(v[0])) {
		return "referer"
	}
	if !p.PermittedHostHeader(r.Host) {
		return "host"
	}
	return ""
}

// dropConnection closes the connection without writing a response, like
// Mirakurun's req.socket.end().
func dropConnection(w http.ResponseWriter) {
	if hj, ok := w.(http.Hijacker); ok {
		if conn, _, err := hj.Hijack(); err == nil {
			conn.Close()
			return
		}
	}
	w.Header().Set("Connection", "close")
	w.WriteHeader(http.StatusForbidden)
}

type statusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.wrote {
		s.status = code
		s.wrote = true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	s.wrote = true
	return s.ResponseWriter.Write(b)
}

func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap lets http.NewResponseController reach the underlying writer.
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }
