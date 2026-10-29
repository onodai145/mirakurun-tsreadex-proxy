// Package stream pipes an upstream MPEG-TS stream through tsreadex and
// serves the result, one tsreadex process per client connection.
package stream

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/onodai145/mirakurun-tsreadex-proxy/internal/tsprobe"
	"github.com/onodai145/mirakurun-tsreadex-proxy/internal/upstream"
)

// Config configures a Streamer.
type Config struct {
	Upstream         *url.URL      // Mirakurun/mirakc base URL
	Tsreadex         string        // executable name or path
	Args             []string      // validated extra tsreadex arguments (no -n, no src)
	MaxStreams       int           // concurrent stream limit
	FirstByteTimeout time.Duration // how long to wait for the first output byte
}

// Streamer serves /stream/{id} requests.
type Streamer struct {
	cfg    Config
	exe    string
	client *http.Client
	slots  chan struct{}
	log    *slog.Logger
}

// New resolves the tsreadex executable (failing fast if it cannot be run).
func New(cfg Config, log *slog.Logger) (*Streamer, error) {
	exe, err := exec.LookPath(cfg.Tsreadex)
	if err != nil {
		return nil, fmt.Errorf("tsreadex executable is not usable: %w", err)
	}
	return &Streamer{
		cfg:    cfg,
		exe:    exe,
		client: upstream.NewClient(),
		slots:  make(chan struct{}, cfg.MaxStreams),
		log:    log,
	}, nil
}

// Serve streams the service id through tsreadex -n serviceID. The first-byte
// timeout covers everything up to tsreadex's first output byte, including
// opening the upstream stream.
func (s *Streamer) Serve(w http.ResponseWriter, r *http.Request, id uint64, serviceID int) {
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	default:
		http.Error(w, "too many streams", http.StatusServiceUnavailable)
		return
	}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	var timedOut atomic.Bool
	timer := time.AfterFunc(s.cfg.FirstByteTimeout, func() {
		timedOut.Store(true)
		cancel()
	})
	defer timer.Stop()

	target := upstream.URL(s.cfg.Upstream, r.URL.RawQuery, "api", "services", strconv.FormatUint(id, 10), "stream")
	req, err := upstream.NewRequest(ctx, target)
	if err != nil {
		http.Error(w, "bad gateway", http.StatusBadGateway)
		return
	}
	resp, err := s.client.Do(req)
	if err != nil {
		s.fail(w, r, &timedOut, "upstream request failed", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		code := upstream.ClientStatus(resp.StatusCode)
		s.log.Warn("upstream returned an error status", "status", resp.StatusCode, "service_id", serviceID)
		http.Error(w, http.StatusText(code), code)
		return
	}

	args := append([]string{"-n", strconv.Itoa(serviceID)}, s.cfg.Args...)
	args = append(args, "-")
	cmd := exec.CommandContext(ctx, s.exe, args...)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
	cmd.Stdin = s.tap(ctx, resp.Body, serviceID)
	stderr := newLineWriter(s.log, serviceID)
	cmd.Stderr = stderr
	cmd.WaitDelay = 3 * time.Second
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		s.fail(w, r, &timedOut, "cannot create the tsreadex pipe", err)
		return
	}
	if err := cmd.Start(); err != nil {
		s.fail(w, r, &timedOut, "cannot start tsreadex", err)
		return
	}

	// Wait exactly once. Cancelling first guarantees the stdin copier and the
	// process cannot outlive this request.
	var (
		waited  bool
		waitErr error
	)
	wait := func() error {
		if !waited {
			waited = true
			waitErr = cmd.Wait()
			stderr.Flush()
		}
		return waitErr
	}
	defer func() {
		cancel()
		_ = wait()
	}()

	buf := make([]byte, 64<<10)
	var (
		n    int
		rerr error
	)
	for n == 0 && rerr == nil {
		n, rerr = stdout.Read(buf)
	}
	if n == 0 {
		s.fail(w, r, &timedOut, "tsreadex produced no output", rerr)
		return
	}
	if !timer.Stop() {
		// The timer fired between the first read and now: it has cancelled ctx
		// (killing tsreadex), so what we hold is the tail of a dying stream.
		// Nothing has been sent yet, so answer with the timeout instead of
		// starting a response that would look like a short, clean one.
		timedOut.Store(true)
		s.fail(w, r, &timedOut, "tsreadex output arrived late", nil)
		return
	}

	w.Header().Set("Content-Type", "video/MP2T")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)
	if _, err := w.Write(buf[:n]); err != nil {
		return
	}
	_ = rc.Flush()
	for rerr == nil {
		n, rerr = stdout.Read(buf)
		if n > 0 {
			if _, err := w.Write(buf[:n]); err != nil {
				return // the client went away
			}
			_ = rc.Flush()
		}
	}

	// The output ended. If tsreadex exited first, the stdin copier may still be
	// blocked reading a silent upstream body, and Wait (even with WaitDelay)
	// would wait for it indefinitely. Closing the body unblocks it; on the
	// normal path the copier is already done, so this is harmless. ctx stays
	// untouched, so the ctx.Err() check below still tells our own kills apart.
	_ = resp.Body.Close()

	// A clean exit is a normal end of stream; a failure that we did not cause
	// must not look like one, so cut the connection.
	if err := wait(); err != nil && ctx.Err() == nil {
		s.log.Warn("tsreadex failed after producing output", "service_id", serviceID, "error", err)
		panic(http.ErrAbortHandler)
	}
}

// tap returns the reader tsreadex should read. At the debug level it also
// feeds a copy of the stream to a tsprobe.Probe, which logs whether the audio
// is dual mono; otherwise it is body itself.
func (s *Streamer) tap(ctx context.Context, body io.Reader, serviceID int) io.Reader {
	if !s.log.Enabled(ctx, slog.LevelDebug) {
		return body
	}
	probe := tsprobe.New(serviceID, func(r tsprobe.Result) {
		s.log.Debug("audio stream probed", "service_id", serviceID, "pid", r.PID,
			"channel_configuration", r.ChannelConfig, "dual_mono", r.DualMono)
	}, func(reason string) {
		s.log.Debug("audio probe gave up", "service_id", serviceID, "reason", reason)
	})
	return io.TeeReader(body, probe)
}

// fail answers a failure that happened before any output was sent.
func (s *Streamer) fail(w http.ResponseWriter, r *http.Request, timedOut *atomic.Bool, msg string, err error) {
	if r.Context().Err() != nil {
		return // the client is already gone
	}
	if timedOut.Load() {
		s.log.Warn(msg+": first byte timeout", "error", err)
		http.Error(w, "gateway timeout", http.StatusGatewayTimeout)
		return
	}
	s.log.Warn(msg, "error", err)
	http.Error(w, "bad gateway", http.StatusBadGateway)
}

const maxLogLine = 8 << 10

// lineWriter logs tsreadex's stderr line by line. It never fails and never
// blocks, and it truncates lines longer than maxLogLine, so a noisy tsreadex
// can neither stall on a full pipe nor exhaust memory.
type lineWriter struct {
	log       *slog.Logger
	serviceID int
	buf       []byte
	truncated bool
}

func newLineWriter(log *slog.Logger, serviceID int) *lineWriter {
	return &lineWriter{log: log, serviceID: serviceID}
}

func (w *lineWriter) Write(p []byte) (int, error) {
	for _, b := range p {
		if b == '\n' {
			w.Flush()
			continue
		}
		if len(w.buf) >= maxLogLine {
			w.truncated = true
			continue
		}
		w.buf = append(w.buf, b)
	}
	return len(p), nil
}

// Flush logs a pending partial line.
func (w *lineWriter) Flush() {
	if len(w.buf) == 0 {
		return
	}
	line := string(w.buf)
	if w.truncated {
		line += " ...(truncated)"
	}
	w.log.Info("tsreadex", "service_id", w.serviceID, "stderr", line)
	w.buf = w.buf[:0]
	w.truncated = false
}
