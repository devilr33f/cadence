package discovery

import (
	"net/netip"
	"slices"
	"strings"
	"testing"

	"golang.org/x/net/dns/dnsmessage"
)

var testInfo = Info{DeviceID: "1A2B3C4D-5e6f-4a1b-9c8d-0123456789ab", Board: "yandex-pickle", Version: "v2.1.0"}

func question(t *testing.T, name string, typ dnsmessage.Type, unicast bool) dnsmessage.Question {
	t.Helper()
	n, err := dnsmessage.NewName(name)
	if err != nil {
		t.Fatal(err)
	}
	class := dnsmessage.ClassINET
	if unicast {
		class |= unicastResponse
	}
	return dnsmessage.Question{Name: n, Type: typ, Class: class}
}

func mustAnswerer(t *testing.T) *answerer {
	t.Helper()
	a, err := newAnswerer(testInfo)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

var addrs = []netip.Addr{netip.MustParseAddr("192.168.1.50"), netip.MustParseAddr("fe80::1")}

func types(rs []dnsmessage.Resource) []string {
	var out []string
	for _, r := range rs {
		out = append(out, r.Header.Type.String())
	}
	return out
}

func TestNamesAreShortLowercaseAndUnique(t *testing.T) {
	a := mustAnswerer(t)
	if got := a.names.instance.String(); got != "Cadence yandex-pickle 1a2b3c4d._cadence._tcp.local." {
		t.Fatalf("instance %q", got)
	}
	if got := a.names.host.String(); got != "cadence-1a2b3c4d.local." {
		t.Fatalf("host %q", got)
	}
}

// a browser's PTR query gets the instance, and in the additional section all it needs to reach it, so it does not
// have to ask again
func TestPTRQueryGetsTheInstanceWithItsRecords(t *testing.T) {
	a := mustAnswerer(t)
	m, ok := a.answer(&dnsmessage.Message{Questions: []dnsmessage.Question{question(t, Service, dnsmessage.TypePTR, false)}}, addrs, false)
	if !ok {
		t.Fatal("no answer")
	}
	if got := types(m.Answers); !slices.Equal(got, []string{"TypePTR"}) {
		t.Fatalf("answers %v", got)
	}
	// the IPv6 address is not answered as an A record
	if got := types(m.Additionals); !slices.Equal(got, []string{"TypeSRV", "TypeTXT", "TypeA"}) {
		t.Fatalf("additionals %v", got)
	}
	if m.Answers[0].Header.Class != dnsmessage.ClassINET {
		t.Fatal("a shared PTR carries the cache-flush bit")
	}
	for _, r := range m.Additionals {
		if r.Header.Class != dnsmessage.ClassINET|cacheFlush {
			t.Fatalf("unique %s lacks the cache-flush bit", r.Header.Type)
		}
	}
	srv := m.Additionals[0].Body.(*dnsmessage.SRVResource)
	if srv.Port != 0 || srv.Target.String() != "cadence-1a2b3c4d.local." {
		t.Fatalf("srv %+v", srv)
	}
	txt := m.Additionals[1].Body.(*dnsmessage.TXTResource).TXT
	want := []string{"proto=1", "id=" + testInfo.DeviceID, "board=yandex-pickle", "ver=v2.1.0"}
	if !slices.Equal(txt, want) {
		t.Fatalf("txt %q", txt)
	}
	if a := m.Additionals[2].Body.(*dnsmessage.AResource).A; a != [4]byte{192, 168, 1, 50} {
		t.Fatalf("a %v", a)
	}
	if m.ID != 0 || len(m.Questions) != 0 || !m.Response || !m.Authoritative {
		t.Fatalf("multicast header %+v questions %d", m.Header, len(m.Questions))
	}
}

// a query from another port than 5353 is a plain DNS client's: its id and questions come back, ttls are short and
// no cache-flush bit is set
func TestLegacyQueryGetsAPlainDNSAnswer(t *testing.T) {
	a := mustAnswerer(t)
	q := &dnsmessage.Message{Header: dnsmessage.Header{ID: 4242}, Questions: []dnsmessage.Question{question(t, Service, dnsmessage.TypePTR, false)}}
	m, ok := a.answer(q, addrs, true)
	if !ok {
		t.Fatal("no answer")
	}
	if m.ID != 4242 || len(m.Questions) != 1 {
		t.Fatalf("id %d questions %d", m.ID, len(m.Questions))
	}
	for _, r := range slices.Concat(m.Answers, m.Additionals) {
		if r.Header.TTL > legacyTTL || r.Header.Class != dnsmessage.ClassINET {
			t.Fatalf("%s ttl %d class %v", r.Header.Type, r.Header.TTL, r.Header.Class)
		}
	}
}

func TestDirectQueriesAndAnyAreAnswered(t *testing.T) {
	a := mustAnswerer(t)
	for _, tc := range []struct {
		name string
		typ  dnsmessage.Type
		want []string
	}{
		{"Cadence yandex-pickle 1a2b3c4d._cadence._tcp.local.", dnsmessage.TypeSRV, []string{"TypeSRV"}},
		{"cadence yandex-pickle 1A2B3C4D._CADENCE._tcp.local.", dnsmessage.TypeTXT, []string{"TypeTXT"}},
		{"Cadence yandex-pickle 1a2b3c4d._cadence._tcp.local.", dnsmessage.TypeALL, []string{"TypeSRV", "TypeTXT"}},
		{"cadence-1a2b3c4d.local.", dnsmessage.TypeA, []string{"TypeA"}},
		{"_services._dns-sd._udp.local.", dnsmessage.TypePTR, []string{"TypePTR"}},
	} {
		m, ok := a.answer(&dnsmessage.Message{Questions: []dnsmessage.Question{question(t, tc.name, tc.typ, true)}}, addrs, false)
		if !ok || !slices.Equal(types(m.Answers), tc.want) {
			t.Fatalf("%s %s: ok %v answers %v", tc.name, tc.typ, ok, m)
		}
	}
}

func TestOtherQuestionsAreLeftToOthers(t *testing.T) {
	a := mustAnswerer(t)
	for _, q := range []dnsmessage.Question{
		question(t, "_airplay._tcp.local.", dnsmessage.TypePTR, false),
		question(t, Service, dnsmessage.TypeA, false),
		question(t, "cadence-ffffffff.local.", dnsmessage.TypeA, false),
		question(t, "cadence-1a2b3c4d.local.", dnsmessage.TypeAAAA, false),
	} {
		if m, ok := a.answer(&dnsmessage.Message{Questions: []dnsmessage.Question{q}}, addrs, false); ok {
			t.Fatalf("%s %s answered: %v", q.Name, q.Type, m)
		}
	}
}

// the goodbye withdraws every record the announcement made
func TestAnnouncementAndGoodbye(t *testing.T) {
	a := mustAnswerer(t)
	hello, bye := a.announcement(addrs, false), a.announcement(addrs, true)
	if got := types(hello.answers); !slices.Equal(got, []string{"TypePTR", "TypePTR", "TypeSRV", "TypeTXT", "TypeA"}) {
		t.Fatalf("announcement %v", got)
	}
	for i, r := range bye.answers {
		if r.Header.TTL != 0 || r.Header.Name != hello.answers[i].Header.Name {
			t.Fatalf("goodbye %d: %+v", i, r.Header)
		}
	}
	if _, err := packSet(hello); err != nil {
		t.Fatal(err)
	}
}

// what the advertiser answers is what the browser reads back
func TestCollectorReadsTheAnswer(t *testing.T) {
	a := mustAnswerer(t)
	m, _ := a.answer(&dnsmessage.Message{Questions: []dnsmessage.Question{question(t, Service, dnsmessage.TypePTR, false)}}, addrs, true)
	b, err := m.Pack()
	if err != nil {
		t.Fatal(err)
	}
	var back dnsmessage.Message
	if err := back.Unpack(b); err != nil {
		t.Fatal(err)
	}
	c := newCollector()
	c.add(&back)
	got := c.devices()
	if len(got) != 1 {
		t.Fatalf("devices %+v", got)
	}
	d := got[0]
	if d.Instance != "Cadence yandex-pickle 1a2b3c4d" || d.DeviceID != testInfo.DeviceID || d.Board != "yandex-pickle" ||
		d.Version != "v2.1.0" || d.Host != "cadence-1a2b3c4d.local" || !slices.Equal(d.Addrs, addrs[:1]) {
		t.Fatalf("device %+v", d)
	}
}

func TestCollectorSkipsForeignInstances(t *testing.T) {
	c := newCollector()
	name, _ := dnsmessage.NewName(Service)
	other, _ := dnsmessage.NewName("Not ours._cadence._tcp.local.")
	c.add(&dnsmessage.Message{Answers: []dnsmessage.Resource{{
		Header: dnsmessage.ResourceHeader{Name: name, Type: dnsmessage.TypePTR, Class: dnsmessage.ClassINET},
		Body:   &dnsmessage.PTRResource{PTR: other},
	}}})
	if got := c.devices(); len(got) != 0 {
		t.Fatalf("an instance without our TXT listed: %+v", got)
	}
}

func TestBoardWithDotsStaysOneLabel(t *testing.T) {
	a, err := newAnswerer(Info{DeviceID: "abc", Board: "android.tv", Version: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if got := a.names.instance.String(); !strings.HasPrefix(got, "Cadence android-tv abc.") {
		t.Fatalf("instance %q", got)
	}
}
