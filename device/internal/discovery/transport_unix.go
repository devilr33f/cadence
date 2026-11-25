//go:build unix

package discovery

import (
	"context"
	"net"
	"net/netip"
	"strconv"
	"syscall"

	"golang.org/x/net/ipv4"
)

// udpTransport shares the mDNS port with the system's responder (avahi, mDNSResponder, a vendor's): both set
// SO_REUSEADDR and SO_REUSEPORT, and the kernel hands every multicast packet to each
type udpTransport struct {
	pc   net.PacketConn
	p    *ipv4.PacketConn
	port int
}

func reuse(_, _ string, c syscall.RawConn) error {
	var serr error
	err := c.Control(func(fd uintptr) {
		serr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
		if serr == nil && soReusePort != 0 {
			// an old kernel may lack it: SO_REUSEADDR alone shares a multicast port on linux
			_ = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, soReusePort, 1)
		}
	})
	if err != nil {
		return err
	}
	return serr
}

func listen(ctx context.Context, port int) (transport, error) {
	lc := net.ListenConfig{Control: reuse}
	pc, err := lc.ListenPacket(ctx, "udp4", net.JoinHostPort("0.0.0.0", strconv.Itoa(port)))
	if err != nil {
		return nil, err
	}
	p := ipv4.NewPacketConn(pc)
	// the interface a query came in on picks the addresses answered with; a system that cannot tell is answered by
	// subnet instead
	_ = p.SetControlMessage(ipv4.FlagInterface, true)
	_ = p.SetMulticastTTL(255)
	// browsers on the same host hear the device too
	_ = p.SetMulticastLoopback(true)
	return &udpTransport{pc: pc, p: p, port: port}, nil
}

func (t *udpTransport) join(ifi *net.Interface) error {
	return t.p.JoinGroup(ifi, &net.UDPAddr{IP: group.AsSlice()})
}

func (t *udpTransport) read(buf []byte) (int, netip.AddrPort, int, error) {
	n, cm, src, err := t.p.ReadFrom(buf)
	if err != nil {
		return 0, netip.AddrPort{}, 0, err
	}
	u, ok := src.(*net.UDPAddr)
	if !ok {
		return 0, netip.AddrPort{}, 0, net.InvalidAddrError("not udp")
	}
	idx := 0
	if cm != nil {
		idx = cm.IfIndex
	}
	return n, u.AddrPort(), idx, nil
}

func (t *udpTransport) multicast(b []byte, ifi *net.Interface) error {
	if err := t.p.SetMulticastInterface(ifi); err != nil {
		return err
	}
	_, err := t.p.WriteTo(b, nil, &net.UDPAddr{IP: group.AsSlice(), Port: t.port})
	return err
}

func (t *udpTransport) unicast(b []byte, dst netip.AddrPort) error {
	_, err := t.pc.WriteTo(b, net.UDPAddrFromAddrPort(dst))
	return err
}

func (t *udpTransport) close() error { return t.pc.Close() }
