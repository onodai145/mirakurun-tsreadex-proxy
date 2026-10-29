package access

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"os"
	"strings"
	"testing"
	"time"
)

func ifaceAddr(s string, loopback bool) IfaceAddr {
	return IfaceAddr{Addr: netip.MustParseAddr(s), Loopback: loopback}
}

func addrStrings(addrs []netip.Addr) []string {
	var out []string
	for _, a := range addrs {
		out = append(out, a.String())
	}
	return out
}

func TestSelectListenAddrs(t *testing.T) {
	all := []IfaceAddr{
		ifaceAddr("203.0.113.5", false), // public
		ifaceAddr("192.168.1.10", false),
		ifaceAddr("127.0.0.1", true),
		ifaceAddr("fd00::1", false),
		ifaceAddr("::1", true),
		ifaceAddr("2001:db8::1", false), // public
		ifaceAddr("fe80::1%eth0", false),
	}

	t.Run("public and unpermitted addresses are excluded", func(t *testing.T) {
		got, lan4 := SelectListenAddrs(all, newPolicy(t, ""))
		want := []string{"192.168.1.10", "127.0.0.1", "fd00::1"}
		if strings.Join(addrStrings(got), ",") != strings.Join(want, ",") {
			t.Errorf("selected = %v, want %v", addrStrings(got), want)
		}
		if !lan4 {
			t.Error("a non-loopback permitted IPv4 address exists")
		}
	})

	t.Run("loopback only does not count as a LAN address", func(t *testing.T) {
		got, lan4 := SelectListenAddrs([]IfaceAddr{ifaceAddr("127.0.0.1", true)}, newPolicy(t, ""))
		if len(got) != 1 || lan4 {
			t.Errorf("selected=%v lan4=%v", got, lan4)
		}
	})

	t.Run("only public addresses select nothing", func(t *testing.T) {
		got, lan4 := SelectListenAddrs([]IfaceAddr{ifaceAddr("203.0.113.5", false)}, newPolicy(t, ""))
		if len(got) != 0 || lan4 {
			t.Errorf("selected=%v lan4=%v", got, lan4)
		}
	})

	t.Run("a scoped address keeps its zone when its range is permitted", func(t *testing.T) {
		p, err := NewPolicy(defaultV4, []string{"fc00::/7", "fe80::/10"}, "", nil)
		if err != nil {
			t.Fatal(err)
		}
		got, _ := SelectListenAddrs(all, p)
		found := false
		for _, a := range got {
			if a.String() == "fe80::1%eth0" {
				found = true
			}
		}
		if !found {
			t.Errorf("selected = %v, want fe80::1%%eth0 included", addrStrings(got))
		}
	})

	t.Run("::1 is not listened on by default", func(t *testing.T) {
		got, _ := SelectListenAddrs(all, newPolicy(t, ""))
		for _, a := range got {
			if a.String() == "::1" {
				t.Error("::1 is outside fc00::/7 and must not be listened on")
			}
		}
	})
}

func TestListenBindsOnlyPermittedAddresses(t *testing.T) {
	enumerate := func() ([]IfaceAddr, error) {
		return []IfaceAddr{
			ifaceAddr("127.0.0.2", false), // stands in for a LAN address
			ifaceAddr("127.0.0.1", true),
			ifaceAddr("8.8.8.8", false), // public: must never be bound
		}, nil
	}
	lns, err := Listen(context.Background(), newPolicy(t, ""), 0, discard, enumerate, 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		for _, l := range lns {
			l.Close()
		}
	}()
	if len(lns) != 2 {
		t.Fatalf("got %d listeners, want 2", len(lns))
	}
	for _, l := range lns {
		host, _, _ := net.SplitHostPort(l.Addr().String())
		if host != "127.0.0.1" && host != "127.0.0.2" {
			t.Errorf("unexpected listen address %s", l.Addr())
		}
	}
}

func TestListenWaitsForANetworkAddress(t *testing.T) {
	calls := 0
	enumerate := func() ([]IfaceAddr, error) {
		calls++
		if calls < 3 {
			return []IfaceAddr{ifaceAddr("127.0.0.1", true)}, nil // loopback only: keep waiting
		}
		return []IfaceAddr{ifaceAddr("127.0.0.2", false)}, nil
	}
	lns, err := Listen(context.Background(), newPolicy(t, ""), 0, discard, enumerate, 5*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range lns {
		l.Close()
	}
	if calls != 3 {
		t.Errorf("enumerate called %d times, want 3", calls)
	}
}

func TestListenStopsWaitingWhenCancelled(t *testing.T) {
	enumerate := func() ([]IfaceAddr, error) { return []IfaceAddr{ifaceAddr("127.0.0.1", true)}, nil }
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := Listen(ctx, newPolicy(t, ""), 0, discard, enumerate, 10*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want DeadlineExceeded", err)
	}
}

func TestListenFailsWhenAnIPv4AddressCannotBeBound(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.2:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	port := busy.Addr().(*net.TCPAddr).Port
	enumerate := func() ([]IfaceAddr, error) { return []IfaceAddr{ifaceAddr("127.0.0.2", false)}, nil }
	if _, err := Listen(context.Background(), newPolicy(t, ""), port, discard, enumerate, 10*time.Millisecond); err == nil {
		t.Fatal("expected an error when the IPv4 port is taken")
	}
}

func TestListenSkipsAnIPv6AddressThatCannotBeBound(t *testing.T) {
	enumerate := func() ([]IfaceAddr, error) {
		return []IfaceAddr{
			ifaceAddr("127.0.0.2", false),
			ifaceAddr("fd00::1234", false), // not assigned to this machine
		}, nil
	}
	lns, err := Listen(context.Background(), newPolicy(t, ""), 0, discard, enumerate, 10*time.Millisecond)
	if err != nil {
		t.Fatalf("an unbindable IPv6 address must not be fatal: %v", err)
	}
	for _, l := range lns {
		l.Close()
	}
	if len(lns) != 1 {
		t.Errorf("got %d listeners, want 1", len(lns))
	}
}

// permitOnly returns a policy that permits just the one IPv4 address, so that
// a loopback client can be either inside (127.0.0.2) or outside (127.0.0.1).
func permitOnly(t *testing.T, addr string) *Policy {
	t.Helper()
	p, err := NewPolicy([]string{addr + "/32"}, nil, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestFilterListenerClosesDeniedConnectionsAtAccept(t *testing.T) {
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln := permitOnly(t, "127.0.0.2").FilterListener(inner, discard)
	defer ln.Close()

	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err == nil {
			accepted <- c
		}
	}()

	// Outside the permitted range: the connection is closed without a reply,
	// and the request is never handed to the server.
	denied, err := net.Dial("tcp", inner.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer denied.Close()
	denied.Write([]byte("GET /playlist.m3u HTTP/1.1\r\nHost: localhost\r\n\r\n"))
	denied.SetReadDeadline(time.Now().Add(2 * time.Second))
	if n, err := denied.Read(make([]byte, 1)); n != 0 || err == nil || errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("denied client read = %d, %v; want the connection closed with no data", n, err)
	}
	select {
	case c := <-accepted:
		c.Close()
		t.Fatal("Accept returned a connection from outside the permitted range")
	default:
	}

	// Inside the range: the connection comes through the same listener.
	d := net.Dialer{LocalAddr: &net.TCPAddr{IP: net.ParseIP("127.0.0.2")}}
	ok, err := d.Dial("tcp", inner.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer ok.Close()
	select {
	case c := <-accepted:
		c.Close()
	case <-time.After(2 * time.Second):
		t.Fatal("Accept did not return the permitted connection")
	}
}

func TestFilterListenerAcceptReturnsWhenClosed(t *testing.T) {
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln := permitOnly(t, "127.0.0.2").FilterListener(inner, discard)
	errc := make(chan error, 1)
	go func() {
		_, err := ln.Accept()
		errc <- err
	}()
	ln.Close()
	select {
	case err := <-errc:
		if !errors.Is(err, net.ErrClosed) {
			t.Errorf("Accept error = %v, want net.ErrClosed", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Accept did not return after Close")
	}
}
