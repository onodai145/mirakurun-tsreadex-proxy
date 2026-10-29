// Command tsreadex-proxy serves Mirakurun/mirakc live streams through
// tsreadex over HTTP, for use as a Jellyfin Live TV tuner.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/onodai145/tsreadex-proxy/internal/access"
	"github.com/onodai145/tsreadex-proxy/internal/config"
	"github.com/onodai145/tsreadex-proxy/internal/server"
	"github.com/onodai145/tsreadex-proxy/internal/stream"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "tsreadex-proxy:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(os.Args[1:], os.Getenv)
	if err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(os.Stdout, nil))

	policy, err := access.NewPolicy(cfg.AllowIPv4, cfg.AllowIPv6, cfg.AllowedHostname, cfg.AllowOrigins)
	if err != nil {
		return err
	}
	streamer, err := stream.New(stream.Config{
		Upstream:         cfg.MirakurunURL,
		Tsreadex:         cfg.TsreadexPath,
		Args:             cfg.TsreadexArgs,
		MaxStreams:       cfg.MaxStreams,
		FirstByteTimeout: cfg.FirstByteTimeout,
	}, log)
	if err != nil {
		return err
	}
	handler := policy.Middleware(log, server.New(server.Deps{
		Upstream:      cfg.MirakurunURL,
		PublicBaseURL: cfg.PublicBaseURL,
		Streamer:      streamer,
		Log:           log,
	}))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	listeners, err := access.Listen(ctx, policy, cfg.ListenPort, log, access.SystemAddrs, 5*time.Second)
	if err != nil {
		return err
	}
	if len(listeners) == 0 {
		return errors.New("no listener could be started")
	}

	// No write timeout: streams are long-lived.
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, len(listeners))
	for _, ln := range listeners {
		go func() { errc <- srv.Serve(ln) }()
	}

	select {
	case <-ctx.Done():
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			srv.Close()
			return err
		}
	}

	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		srv.Close() // streams that outlive the grace period are cut
	}
	return nil
}
