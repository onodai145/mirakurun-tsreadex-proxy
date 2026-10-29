package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestInflightWaitReturnsImmediatelyWhenIdle(t *testing.T) {
	var f inflight
	if !f.wait(time.Second) {
		t.Fatal("wait reported active requests on an idle tracker")
	}
}

func TestInflightWaitTimesOutWhileHandlerRuns(t *testing.T) {
	var f inflight
	started := make(chan struct{})
	release := make(chan struct{})
	h := f.wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		close(started)
		<-release
	}))
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	}()
	<-started

	if f.wait(50 * time.Millisecond) {
		t.Fatal("wait returned true while a handler was still running")
	}
	close(release)
	<-done
	if !f.wait(time.Second) {
		t.Fatal("wait did not see the handler finish")
	}
}

func TestInflightCountsPanickingHandlerAsFinished(t *testing.T) {
	var f inflight
	h := f.wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	}))
	func() {
		defer func() { _ = recover() }()
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	}()
	if !f.wait(time.Second) {
		t.Fatal("a panicking handler was still counted as active")
	}
}
