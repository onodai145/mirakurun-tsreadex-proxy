package access

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"strconv"
	"time"
)

// IfaceAddr is one address of a network interface. IPv6 link-local
// addresses carry the interface name as their zone.
type IfaceAddr struct {
	Addr     netip.Addr
	Loopback bool
}

// SelectListenAddrs keeps the addresses inside the permitted ranges.
// haveLAN4 reports whether any of them is a non-loopback IPv4 address.
func SelectListenAddrs(addrs []IfaceAddr, p *Policy) (selected []netip.Addr, haveLAN4 bool) {
	for _, a := range addrs {
		if !p.PermittedIP(a.Addr) {
			continue
		}
		selected = append(selected, a.Addr)
		if a.Addr.Is4() && !a.Loopback {
			haveLAN4 = true
		}
	}
	return selected, haveLAN4
}

// SystemAddrs lists the addresses of all interfaces that are up.
func SystemAddrs() ([]IfaceAddr, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var out []IfaceAddr
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			return nil, err
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			addr, ok := netip.AddrFromSlice(ipnet.IP)
			if !ok {
				continue
			}
			addr = addr.Unmap()
			if addr.Is6() && (addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast()) {
				addr = addr.WithZone(ifc.Name)
			}
			out = append(out, IfaceAddr{Addr: addr, Loopback: addr.IsLoopback()})
		}
	}
	return out, nil
}

// Listen binds one listener per permitted interface address. It never binds a
// wildcard address. Like Mirakurun it retries until a permitted non-loopback
// IPv4 address exists (the network may not be up yet at boot), and it does not
// follow address changes afterwards. A failure to bind an IPv4 address is
// fatal; an IPv6 address that cannot be bound is skipped with a warning.
func Listen(ctx context.Context, p *Policy, port int, log *slog.Logger, enumerate func() ([]IfaceAddr, error), retry time.Duration) ([]net.Listener, error) {
	var selected []netip.Addr
	for {
		addrs, err := enumerate()
		if err != nil {
			log.Warn("cannot enumerate network interfaces", "error", err)
		} else {
			var haveLAN4 bool
			selected, haveLAN4 = SelectListenAddrs(addrs, p)
			if haveLAN4 {
				break
			}
			log.Warn("no permitted non-loopback IPv4 address yet; retrying", "retry_in", retry)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(retry):
		}
	}

	var (
		lc        net.ListenConfig
		listeners []net.Listener
	)
	for _, a := range selected {
		hostport := net.JoinHostPort(a.String(), strconv.Itoa(port))
		ln, err := lc.Listen(ctx, "tcp", hostport)
		if err != nil {
			if a.Is4() {
				for _, l := range listeners {
					l.Close()
				}
				return nil, fmt.Errorf("listen %s: %w", hostport, err)
			}
			log.Warn("cannot listen on IPv6 address; skipping", "addr", hostport, "error", err)
			continue
		}
		log.Info("listening", "addr", ln.Addr().String())
		listeners = append(listeners, ln)
	}
	return listeners, nil
}
