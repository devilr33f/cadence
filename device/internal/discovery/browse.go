package discovery

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"slices"
	"strings"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// Device is one advertised device as a browser saw it.
type Device struct {
	Instance string // the DNS-SD instance name, "Cadence <board> <id>"
	DeviceID string
	Board    string
	Version  string
	Host     string
	Addrs    []netip.Addr
}

// Browse asks the local network for Cadence devices for wait and returns those that answered, by instance name. It
// queries from a port of its own (a legacy query, RFC 6762 §6.7), so it needs neither the mDNS port nor privileges
// and works beside the system's responder.
func Browse(ctx context.Context, wait time.Duration) ([]Device, error) {
	return browse(ctx, wait, netip.AddrPortFrom(group, Port))
}

func browse(ctx context.Context, wait time.Duration, dst netip.AddrPort) ([]Device, error) {
	var lc net.ListenConfig
	pc, err := lc.ListenPacket(ctx, "udp4", "0.0.0.0:0")
	if err != nil {
		return nil, err
	}
	defer func() { _ = pc.Close() }()
	q, err := query()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	go func() {
		// three queries spread over the wait, as one datagram can be lost
		for i := range 3 {
			if i > 0 {
				select {
				case <-ctx.Done():
					return
				case <-time.After(wait / 4):
				}
			}
			_, _ = pc.WriteTo(q, net.UDPAddrFromAddrPort(dst))
		}
	}()
	go func() {
		<-ctx.Done()
		_ = pc.SetReadDeadline(time.Now())
	}()
	c := newCollector()
	buf := make([]byte, 9000)
	for {
		n, _, err := pc.ReadFrom(buf)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				break
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				break
			}
			continue
		}
		var m dnsmessage.Message
		if m.Unpack(buf[:n]) == nil && m.Response {
			c.add(&m)
		}
	}
	return c.devices(), nil
}

func query() ([]byte, error) {
	name, err := dnsmessage.NewName(Service)
	if err != nil {
		return nil, err
	}
	m := dnsmessage.Message{Questions: []dnsmessage.Question{{Name: name, Type: dnsmessage.TypePTR, Class: dnsmessage.ClassINET}}}
	return m.Pack()
}

// collector gathers the records of every answer, keyed by name, and joins them into devices
type collector struct {
	instances map[string]bool
	srv       map[string]string
	txt       map[string][]string
	addrs     map[string][]netip.Addr
}

func newCollector() *collector {
	return &collector{instances: map[string]bool{}, srv: map[string]string{}, txt: map[string][]string{}, addrs: map[string][]netip.Addr{}}
}

func (c *collector) add(m *dnsmessage.Message) {
	for _, r := range slices.Concat(m.Answers, m.Additionals) {
		name := strings.ToLower(r.Header.Name.String())
		switch b := r.Body.(type) {
		case *dnsmessage.PTRResource:
			if name == Service {
				c.instances[b.PTR.String()] = true
			}
		case *dnsmessage.SRVResource:
			c.srv[name] = strings.ToLower(b.Target.String())
		case *dnsmessage.TXTResource:
			c.txt[name] = b.TXT
		case *dnsmessage.AResource:
			ip := netip.AddrFrom4(b.A)
			if !slices.Contains(c.addrs[name], ip) {
				c.addrs[name] = append(c.addrs[name], ip)
			}
		}
	}
}

func (c *collector) devices() []Device {
	var out []Device
	for inst := range c.instances {
		key := strings.ToLower(inst)
		kv := map[string]string{}
		for _, t := range c.txt[key] {
			k, v, _ := strings.Cut(t, "=")
			kv[strings.ToLower(k)] = v
		}
		if kv[keyID] == "" {
			continue
		}
		host := c.srv[key]
		d := Device{
			Instance: strings.TrimSuffix(inst, "."+Service), DeviceID: kv[keyID], Board: kv[keyBoard], Version: kv[keyVersion],
			Host: strings.TrimSuffix(host, "."), Addrs: slices.Clone(c.addrs[host]),
		}
		slices.SortFunc(d.Addrs, func(a, b netip.Addr) int { return a.Compare(b) })
		out = append(out, d)
	}
	slices.SortFunc(out, func(a, b Device) int { return strings.Compare(a.Instance, b.Instance) })
	return out
}
