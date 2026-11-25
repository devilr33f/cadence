package media

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/netip"
	"slices"
	"syscall"
	"time"
)

var (
	errArtAddress = errors.New("artwork: address refused")
	errArtDNS     = errors.New("artwork: name not resolved")
	errArtConnect = errors.New("artwork: connect failed")
)

// reserved are the special-purpose blocks netip has no test for: none holds a
// cover art host, and several reach inside (this network, NAT64 and 6to4 carry
// a v4 address, site-local is routed internally)
var reserved = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"), // cgnat
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("::/96"),
	netip.MustParsePrefix("::ffff:0:0:0/96"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("3fff::/20"),
	netip.MustParsePrefix("5f00::/16"),
	netip.MustParsePrefix("fec0::/10"),
}

// forbidden says the server never dials a: loopback, private, link-local,
// CGNAT, multicast and unspecified addresses (M8-D4), v4 inside v6 as v4, any
// with a zone, and the reserved blocks.
func forbidden(a netip.Addr) bool {
	if a.Zone() != "" {
		return true
	}
	a = a.Unmap()
	return !a.IsGlobalUnicast() || a.IsPrivate() || slices.ContainsFunc(reserved, func(p netip.Prefix) bool { return p.Contains(a) })
}

// the addresses of one name a fetch dials at most, and the least time one
// attempt gets before the next address is tried
const (
	maxArtDials = 4
	minArtDial  = 1500 * time.Millisecond
)

// fetchDeadline carries a fetch's deadline, a time.Time, to its dial.
type fetchDeadline struct{}

// dialTLS connects to address and runs the TLS handshake on one deadline, the
// fetch's own: the transport dials apart from the request (without its
// cancel), so a connect and a handshake timed apart would outlive the fetch.
func (a *Artwork) dialTLS(ctx context.Context, network, address string) (net.Conn, error) {
	deadline, ok := ctx.Value(fetchDeadline{}).(time.Time)
	if !ok {
		deadline = time.Now().Add(a.timeout)
	}
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, errArtURL
	}
	conn, err := a.dialPinned(ctx, network, host, port)
	if err != nil {
		return nil, err
	}
	cfg := a.tls.Clone()
	cfg.ServerName = host
	tc := tls.Client(conn, cfg)
	if err := tc.HandshakeContext(ctx); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return tc, nil
}

// dialPinned resolves the name once and refuses it whole when any of its
// addresses is forbidden, then dials up to four of those addresses in order,
// never the name: no second answer (DNS rebinding) is ever asked for or dialed.
func (a *Artwork) dialPinned(ctx context.Context, network, host, port string) (net.Conn, error) {
	addrs, err := a.addresses(ctx, host)
	if err != nil {
		return nil, err
	}
	if slices.ContainsFunc(addrs, forbidden) {
		return nil, errArtAddress
	}
	tries := addrs[:min(len(addrs), maxArtDials)]
	for i, ip := range tries {
		conn, err := a.dialOnce(ctx, network, host, net.JoinHostPort(ip.Unmap().String(), port), len(tries)-i)
		switch {
		case err == nil:
			return conn, nil
		case ctx.Err() != nil:
			return nil, ctx.Err()
		case errors.Is(err, errArtAddress):
			return nil, errArtAddress
		}
	}
	return nil, errArtConnect
}

// dialOnce gives an attempt its share of what is left of the deadline when
// more addresses wait behind it, so one that hangs leaves time for the next.
func (a *Artwork) dialOnce(ctx context.Context, network, host, address string, addresses int) (net.Conn, error) {
	if deadline, ok := ctx.Deadline(); ok && addresses > 1 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, attemptBudget(time.Until(deadline), addresses))
		defer cancel()
	}
	return a.connect(ctx, network, host, address)
}

func attemptBudget(left time.Duration, addresses int) time.Duration {
	return max(left/time.Duration(addresses), minArtDial)
}

func (a *Artwork) addresses(ctx context.Context, host string) ([]netip.Addr, error) {
	if ip, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{ip}, nil
	}
	addrs, err := a.resolve(ctx, host)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil || len(addrs) == 0 {
		return nil, errArtDNS
	}
	return addrs, nil
}

func lookup(ctx context.Context, host string) ([]netip.Addr, error) {
	return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
}

// guardedDialer checks again, at the socket, the address it connects to: a
// second line behind dialPinned.
func guardedDialer() *net.Dialer {
	return &net.Dialer{Control: func(_, address string, _ syscall.RawConn) error {
		ap, err := netip.ParseAddrPort(address)
		if err != nil || forbidden(ap.Addr()) {
			return errArtAddress
		}
		return nil
	}}
}
