// Package testutil holds helpers shared by the tests of this module.
package testutil

import (
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

type remoteListener struct {
	net.Listener
	remote net.Addr
}

func (l remoteListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return remoteConn{Conn: c, remote: l.remote}, nil
}

type remoteConn struct {
	net.Conn
	remote net.Addr
}

func (c remoteConn) RemoteAddr() net.Addr { return c.remote }

// NewServer starts an HTTP server whose connections appear to come from
// remote ("ip:port"), so that source-IP checks can be tested over a real
// connection. The server is closed when the test ends.
func NewServer(t *testing.T, h http.Handler, remote string) *httptest.Server {
	t.Helper()
	ap, err := netip.ParseAddrPort(remote)
	if err != nil {
		t.Fatalf("bad remote address %q: %v", remote, err)
	}
	srv := httptest.NewUnstartedServer(h)
	srv.Listener = remoteListener{Listener: srv.Listener, remote: net.TCPAddrFromAddrPort(ap)}
	srv.Start()
	t.Cleanup(srv.Close)
	return srv
}
