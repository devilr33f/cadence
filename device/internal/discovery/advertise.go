package discovery

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/netip"
	"slices"
	"sync"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// Port is the mDNS port; a query from any other port is a legacy one, answered by unicast.
const Port = 5353

var (
	// group is mDNS's IPv4 multicast group
	group = netip.MustParseAddr("224.0.0.251")

	errNoInterface = errors.New("discovery: no multicast interface")
)

// timings are the advertiser's; tests shorten them
type timings struct {
	announceGap time.Duration // between the two announcements (RFC 6762 §8.3: at least 1 s)
	recheck     time.Duration // how often the interfaces and their addresses are read again
}

var defaultTimings = timings{announceGap: time.Second, recheck: 30 * time.Second}

// Advertise answers for info on every up multicast interface until ctx is done, then says goodbye. ready, if not
// nil, is called once the socket listens. It returns an error only when it cannot start.
func Advertise(ctx context.Context, info Info, log *slog.Logger, ready func()) error {
	return advertise(ctx, info, log, ready, Port, defaultTimings)
}

// iface is an interface the device answers on, with its IPv4 addresses
type iface struct {
	ifi   net.Interface
	addrs []netip.Addr
}

// multicastIfaces lists the up, multicast, non-loopback, non point-to-point interfaces with an IPv4 address: a VPN's
// tunnel (point-to-point) is not a local network
func multicastIfaces() []iface {
	all, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []iface
	for _, ifi := range all {
		f := ifi.Flags
		if f&net.FlagUp == 0 || f&net.FlagMulticast == 0 || f&net.FlagLoopback != 0 || f&net.FlagPointToPoint != 0 {
			continue
		}
		as, err := ifi.Addrs()
		if err != nil {
			continue
		}
		var v4 []netip.Addr
		for _, a := range as {
			p, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip, ok := netip.AddrFromSlice(p.IP)
			if ok && ip.Unmap().Is4() && !ip.Unmap().IsLinkLocalUnicast() {
				v4 = append(v4, ip.Unmap())
			}
		}
		if len(v4) > 0 {
			out = append(out, iface{ifi: ifi, addrs: v4})
		}
	}
	return out
}

func sameIfaces(a, b []iface) bool {
	return slices.EqualFunc(a, b, func(x, y iface) bool {
		return x.ifi.Index == y.ifi.Index && slices.Equal(x.addrs, y.addrs)
	})
}

// transport is the socket the advertiser reads queries from and writes answers to
type transport interface {
	join(ifi *net.Interface) error
	// read returns a packet, its source and the index of the interface it came in on (0 if unknown)
	read(buf []byte) (n int, src netip.AddrPort, ifIndex int, err error)
	// multicast writes b to the group out of ifi
	multicast(b []byte, ifi *net.Interface) error
	unicast(b []byte, dst netip.AddrPort) error
	close() error
}

func advertise(ctx context.Context, info Info, log *slog.Logger, ready func(), port int, t timings) error {
	a, err := newAnswerer(info)
	if err != nil {
		return err
	}
	tr, err := listen(ctx, port)
	if err != nil {
		return err
	}
	ad := &advertiser{a: a, tr: tr, log: log, port: port, joined: map[int]bool{}}
	if !ad.refresh() {
		_ = tr.close()
		return errNoInterface
	}
	if ready != nil {
		ready()
	}
	log.Info("discovery_started", "interfaces", len(ad.current()))
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		ad.serve()
	}()
	ad.announce(ctx, t.announceGap)
	tick := time.NewTicker(t.recheck)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			ad.goodbye()
			_ = tr.close()
			<-readerDone
			log.Info("discovery_stopped")
			return nil
		case <-tick.C:
			before := ad.current()
			ad.refresh()
			if !sameIfaces(before, ad.current()) {
				// the device moved or got another address: tell the caches at once (RFC 6762 §8.4)
				ad.goodbyeOn(before, ad.current())
				ad.announce(ctx, t.announceGap)
			}
		}
	}
}

type advertiser struct {
	a    *answerer
	tr   transport
	log  *slog.Logger
	port int

	mu     sync.Mutex // guards ifaces and joined, and serializes writes
	ifaces []iface
	joined map[int]bool
}

func (ad *advertiser) current() []iface {
	ad.mu.Lock()
	defer ad.mu.Unlock()
	return slices.Clone(ad.ifaces)
}

// refresh reads the interfaces again and joins the group on new ones; false when none can be used
func (ad *advertiser) refresh() bool {
	ifs := multicastIfaces()
	ad.mu.Lock()
	defer ad.mu.Unlock()
	var usable []iface
	for _, f := range ifs {
		if !ad.joined[f.ifi.Index] {
			if err := ad.tr.join(&f.ifi); err != nil {
				continue
			}
			ad.joined[f.ifi.Index] = true
		}
		usable = append(usable, f)
	}
	ad.ifaces = usable
	return len(usable) > 0
}

func (ad *advertiser) send(b []byte, f iface) {
	ad.mu.Lock()
	defer ad.mu.Unlock()
	if err := ad.tr.multicast(b, &f.ifi); err != nil {
		ad.log.Debug("discovery_send_failed", "interface", f.ifi.Index)
	}
}

// announce sends every record twice, gap apart (RFC 6762 §8.3), on every interface with its own addresses
func (ad *advertiser) announce(ctx context.Context, gap time.Duration) {
	for i := range 2 {
		if i > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(gap):
			}
		}
		for _, f := range ad.current() {
			if b, err := packSet(ad.a.announcement(f.addrs, false)); err == nil {
				ad.send(b, f)
			}
		}
	}
}

func (ad *advertiser) goodbye() {
	for _, f := range ad.current() {
		if b, err := packSet(ad.a.announcement(f.addrs, true)); err == nil {
			ad.send(b, f)
		}
	}
}

// goodbyeOn withdraws what was said on an interface that went away or changed its addresses
func (ad *advertiser) goodbyeOn(before, now []iface) {
	for _, old := range before {
		i := slices.IndexFunc(now, func(f iface) bool { return f.ifi.Index == old.ifi.Index })
		if i >= 0 && slices.Equal(now[i].addrs, old.addrs) {
			continue
		}
		if b, err := packSet(ad.a.announcement(old.addrs, true)); err == nil {
			ad.send(b, old)
		}
	}
}

func (ad *advertiser) serve() {
	buf := make([]byte, 9000)
	for {
		n, src, idx, err := ad.tr.read(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		var q dnsmessage.Message
		if q.Unpack(buf[:n]) != nil || q.Response || q.OpCode != 0 || len(q.Questions) == 0 {
			continue
		}
		ad.reply(&q, src, idx)
	}
}

func (ad *advertiser) reply(q *dnsmessage.Message, src netip.AddrPort, idx int) {
	ifs := ad.current()
	at := slices.IndexFunc(ifs, func(f iface) bool { return f.ifi.Index == idx })
	if at < 0 {
		// no interface known for the packet: answer only if the asker shares a subnet with one
		at = slices.IndexFunc(ifs, func(f iface) bool { return onLink(f, src.Addr()) })
	}
	if at < 0 {
		return
	}
	f := ifs[at]
	legacy := int(src.Port()) != ad.port
	m, ok := ad.a.answer(q, f.addrs, legacy)
	if !ok {
		return
	}
	b, err := m.Pack()
	if err != nil {
		return
	}
	if legacy {
		ad.mu.Lock()
		_ = ad.tr.unicast(b, src)
		ad.mu.Unlock()
		return
	}
	// a shared record's answer waits 20-120 ms, so the answers of many devices do not collide (RFC 6762 §6)
	time.AfterFunc(time.Duration(20+rand.IntN(100))*time.Millisecond, func() { ad.send(b, f) }) //nolint:gosec // jitter only
}

// onLink tells whether ip is in one of f's subnets
func onLink(f iface, ip netip.Addr) bool {
	as, err := f.ifi.Addrs()
	if err != nil {
		return false
	}
	for _, a := range as {
		if p, ok := a.(*net.IPNet); ok && p.Contains(net.IP(ip.AsSlice())) {
			return true
		}
	}
	return false
}
