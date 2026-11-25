// Package discovery advertises an enrolled device on its local network over multicast DNS (RFC 6762) as a DNS-SD
// service (RFC 6763), _cadence._tcp, and browses for such devices. cadenced listens on no port, so the service's
// port is 0: the advertisement says that a device is there and what it is, it does not offer a connection.
package discovery

import (
	"net/netip"
	"strings"

	"golang.org/x/net/dns/dnsmessage"
)

const (
	// Service is the DNS-SD service type every device advertises.
	Service = "_cadence._tcp.local."
	// servicesEnum lists the types a host offers, for browsers that enumerate them (RFC 6763 §9)
	servicesEnum = "_services._dns-sd._udp.local."

	// RFC 6762 §10: 120 s for records naming a host, 75 min for the others
	hostTTL  = 120
	otherTTL = 4500
	// RFC 6762 §6.7: a legacy unicast answer's records live at most 10 s
	legacyTTL = 10

	// the top bit of a record's class in a multicast answer: the record is unique, a cache drops older ones
	cacheFlush = 1 << 15
	// the top bit of a question's class: the asker would take a unicast answer
	unicastResponse = 1 << 15

	// TXT keys
	keyID      = "id"
	keyBoard   = "board"
	keyVersion = "ver"
	keyProto   = "proto"
	// the TXT record's version, so a browser can tell a later layout
	txtProto = "1"
)

// Info is what a device advertises: ids and versions only, nothing personal (its name lives on the server).
type Info struct {
	DeviceID string
	Board    string
	Version  string
}

// names are the instance's and the host's: the board and the start of the device id make both unique on a network,
// so no probing (RFC 6762 §8.1) is done; a host name of its own keeps clear of the system's (avahi's, the vendor's)
type names struct {
	service, instance, host, enum dnsmessage.Name
}

func shortID(id string) string {
	s := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			return r
		case r >= 'A' && r <= 'Z':
			return r + 'a' - 'A'
		}
		return -1
	}, id)
	if len(s) > 8 {
		s = s[:8]
	}
	return s
}

func (i Info) names() (names, error) {
	short := shortID(i.DeviceID)
	label := "Cadence " + labelSafe(i.Board) + " " + short
	var n names
	var err error
	if n.service, err = dnsmessage.NewName(Service); err != nil {
		return names{}, err
	}
	if n.enum, err = dnsmessage.NewName(servicesEnum); err != nil {
		return names{}, err
	}
	if n.instance, err = dnsmessage.NewName(label + "." + Service); err != nil {
		return names{}, err
	}
	if n.host, err = dnsmessage.NewName("cadence-" + short + ".local."); err != nil {
		return names{}, err
	}
	return n, nil
}

// labelSafe keeps a board id inside one DNS label
func labelSafe(s string) string {
	s = strings.ReplaceAll(s, ".", "-")
	if len(s) > 40 {
		s = s[:40]
	}
	return s
}

func (i Info) txt() []string {
	return []string{keyProto + "=" + txtProto, keyID + "=" + i.DeviceID, keyBoard + "=" + i.Board, keyVersion + "=" + i.Version}
}

// answerer builds the device's records and answers questions about them.
type answerer struct {
	info  Info
	names names
}

func newAnswerer(info Info) (*answerer, error) {
	n, err := info.names()
	if err != nil {
		return nil, err
	}
	return &answerer{info: info, names: n}, nil
}

type recordSet struct {
	answers, additionals []dnsmessage.Resource
}

func header(name dnsmessage.Name, typ dnsmessage.Type, ttl uint32, unique bool) dnsmessage.ResourceHeader {
	class := dnsmessage.ClassINET
	if unique {
		class |= cacheFlush
	}
	return dnsmessage.ResourceHeader{Name: name, Type: typ, Class: class, TTL: ttl}
}

func (a *answerer) ptr(ttl uint32) dnsmessage.Resource {
	return dnsmessage.Resource{
		Header: header(a.names.service, dnsmessage.TypePTR, ttl, false),
		Body:   &dnsmessage.PTRResource{PTR: a.names.instance},
	}
}

func (a *answerer) enumPTR(ttl uint32) dnsmessage.Resource {
	return dnsmessage.Resource{
		Header: header(a.names.enum, dnsmessage.TypePTR, ttl, false),
		Body:   &dnsmessage.PTRResource{PTR: a.names.service},
	}
}

func (a *answerer) srv(ttl uint32, unique bool) dnsmessage.Resource {
	return dnsmessage.Resource{
		Header: header(a.names.instance, dnsmessage.TypeSRV, ttl, unique),
		Body:   &dnsmessage.SRVResource{Target: a.names.host},
	}
}

func (a *answerer) txtRecord(ttl uint32, unique bool) dnsmessage.Resource {
	return dnsmessage.Resource{
		Header: header(a.names.instance, dnsmessage.TypeTXT, ttl, unique),
		Body:   &dnsmessage.TXTResource{TXT: a.info.txt()},
	}
}

func (a *answerer) hostAddrs(addrs []netip.Addr, ttl uint32, unique bool) []dnsmessage.Resource {
	var out []dnsmessage.Resource
	for _, ip := range addrs {
		if !ip.Is4() {
			continue
		}
		out = append(out, dnsmessage.Resource{
			Header: header(a.names.host, dnsmessage.TypeA, ttl, unique),
			Body:   &dnsmessage.AResource{A: ip.As4()},
		})
	}
	return out
}

// announcement is every record, for an unsolicited response (RFC 6762 §8.3); goodbye sends it with ttl 0 (§10.1)
func (a *answerer) announcement(addrs []netip.Addr, goodbye bool) recordSet {
	scale := func(ttl uint32) uint32 {
		if goodbye {
			return 0
		}
		return ttl
	}
	rs := recordSet{answers: []dnsmessage.Resource{
		a.ptr(scale(otherTTL)), a.enumPTR(scale(otherTTL)), a.srv(scale(hostTTL), true), a.txtRecord(scale(otherTTL), true),
	}}
	rs.answers = append(rs.answers, a.hostAddrs(addrs, scale(hostTTL), true)...)
	return rs
}

// answer returns the records for q's questions; legacy is a query from a port other than 5353, which gets a plain
// DNS answer: its id and questions echoed, short ttls and no cache-flush bit (RFC 6762 §6.7). ok is false when no
// question is about this device.
func (a *answerer) answer(q *dnsmessage.Message, addrs []netip.Addr, legacy bool) (*dnsmessage.Message, bool) {
	ttl := func(t uint32) uint32 {
		if legacy {
			return min(t, legacyTTL)
		}
		return t
	}
	unique := !legacy
	var rs recordSet
	seen := map[string]bool{}
	add := func(dst *[]dnsmessage.Resource, rr ...dnsmessage.Resource) {
		for _, r := range rr {
			k := r.Header.Name.String() + "/" + r.Header.Type.String() + "/" + r.Body.GoString()
			if seen[k] {
				continue
			}
			seen[k] = true
			*dst = append(*dst, r)
		}
	}
	for _, question := range q.Questions {
		if question.Class&^unicastResponse != dnsmessage.ClassINET && question.Class&^unicastResponse != dnsmessage.ClassANY {
			continue
		}
		name, typ := strings.ToLower(question.Name.String()), question.Type
		is := func(t dnsmessage.Type) bool { return typ == t || typ == dnsmessage.TypeALL }
		switch name {
		case strings.ToLower(a.names.service.String()):
			if is(dnsmessage.TypePTR) {
				add(&rs.answers, a.ptr(ttl(otherTTL)))
				add(&rs.additionals, a.srv(ttl(hostTTL), unique), a.txtRecord(ttl(otherTTL), unique))
				add(&rs.additionals, a.hostAddrs(addrs, ttl(hostTTL), unique)...)
			}
		case strings.ToLower(a.names.enum.String()):
			if is(dnsmessage.TypePTR) {
				add(&rs.answers, a.enumPTR(ttl(otherTTL)))
			}
		case strings.ToLower(a.names.instance.String()):
			if is(dnsmessage.TypeSRV) {
				add(&rs.answers, a.srv(ttl(hostTTL), unique))
				add(&rs.additionals, a.hostAddrs(addrs, ttl(hostTTL), unique)...)
			}
			if is(dnsmessage.TypeTXT) {
				add(&rs.answers, a.txtRecord(ttl(otherTTL), unique))
			}
		case strings.ToLower(a.names.host.String()):
			if is(dnsmessage.TypeA) {
				add(&rs.answers, a.hostAddrs(addrs, ttl(hostTTL), unique)...)
			}
		}
	}
	if len(rs.answers) == 0 {
		return nil, false
	}
	m := &dnsmessage.Message{
		Header:      dnsmessage.Header{Response: true, Authoritative: true},
		Answers:     rs.answers,
		Additionals: rs.additionals,
	}
	if legacy {
		m.ID = q.ID
		m.Questions = q.Questions
	}
	return m, true
}

func packSet(rs recordSet) ([]byte, error) {
	m := dnsmessage.Message{
		Header:      dnsmessage.Header{Response: true, Authoritative: true},
		Answers:     rs.answers,
		Additionals: rs.additionals,
	}
	return m.Pack()
}
