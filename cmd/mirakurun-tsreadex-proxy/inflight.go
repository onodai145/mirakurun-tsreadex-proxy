package main

import (
	"net/http"
	"sync/atomic"
	"time"
)

// inflight counts the requests being handled, so that shutdown can wait for
// the handlers (and the tsreadex processes they own) to finish after
// http.Server.Close, which does not wait for them.
//
// A counter that is polled, rather than a sync.WaitGroup, because a request can
// still arrive while wait runs, and Add concurrent with Wait is a WaitGroup
// misuse.
type inflight struct {
	n atomic.Int64
}

func (f *inflight) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.n.Add(1)
		defer f.n.Add(-1)
		next.ServeHTTP(w, r)
	})
}

// active returns the number of requests still being handled.
func (f *inflight) active() int64 { return f.n.Load() }

// wait blocks until no request is being handled or timeout elapses, and
// reports whether every handler finished.
func (f *inflight) wait(timeout time.Duration) bool {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for f.active() > 0 {
		select {
		case <-tick.C:
		case <-deadline.C:
			return f.active() == 0
		}
	}
	return true
}
