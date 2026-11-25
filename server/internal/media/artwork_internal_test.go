package media

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"net/http"
	"net/netip"
	"strconv"
	"testing"
	"time"
)

func key(s string) artKey { return sha256.Sum256([]byte(s)) }

func img(n int) artImage { return artImage{contentType: "image/png", data: bytes.Repeat([]byte{1}, n)} }

// held reports whether c holds exactly names, without touching their order.
func held(c *artCache, names ...string) bool {
	if len(c.byKey) != len(names) || c.order.Len() != len(names) {
		return false
	}
	for _, n := range names {
		if _, ok := c.byKey[key(n)]; !ok {
			return false
		}
	}
	return true
}

// the cache holds 32 images and 32 MiB at most (M8-D4), dropping the least
// recently used first
func TestArtCacheEvictsLeastRecentlyUsed(t *testing.T) {
	if c := newArtCache(); c.maxEntries != 32 || c.maxBytes != 32<<20 {
		t.Fatalf("limits %d entries %d bytes", c.maxEntries, c.maxBytes)
	}
	c := newArtCacheOf(3, 10)
	for _, n := range []string{"a", "b", "c"} {
		c.put(key(n), img(1))
	}
	if _, ok := c.get(key("a")); !ok {
		t.Fatal("a not held")
	}
	c.put(key("d"), img(1))
	if !held(c, "a", "c", "d") {
		t.Fatal("by count: b, the least recently used, should be gone")
	}
	c.put(key("e"), img(8))
	if !held(c, "a", "d", "e") || c.bytes != 10 {
		t.Fatalf("by bytes: c should be gone, %d bytes held", c.bytes)
	}
	// a key put again replaces its image and its size
	c.put(key("d"), img(3))
	if got, _ := c.get(key("d")); len(got.data) != 3 || !held(c, "d") || c.bytes != 3 {
		t.Fatalf("replaced: %d bytes held", c.bytes)
	}
	// an image over the whole budget is never held, and evicts nothing
	c.put(key("f"), img(11))
	if !held(c, "d") {
		t.Fatal("an image over the budget was held or evicted another")
	}
	if _, ok := c.get(key("b")); ok {
		t.Fatal("an evicted key answered")
	}
}

// what the dial refuses besides the named classes, and the public neighbours
// of each refused block, which it dials
func TestForbiddenAddresses(t *testing.T) {
	for _, a := range []string{
		"8.8.8.8", "93.184.215.14", "1.1.1.1", "9.255.255.255", "11.0.0.1", "100.63.255.255", "100.128.0.1",
		"126.255.255.255", "128.0.0.1", "169.253.255.255", "172.15.255.255", "172.32.0.1", "192.167.255.255",
		"192.169.0.1", "192.88.98.255", "192.88.100.1", "198.17.255.255", "198.20.0.1", "223.255.255.255", "2606:4700:4700::1111", "2a00:1450::1",
		"2001:4860:4860::8888", "::ffff:8.8.8.8",
	} {
		if forbidden(netip.MustParseAddr(a)) {
			t.Errorf("%s refused", a)
		}
	}
	for _, a := range []string{"fe80::1%eth0", "2606:4700::1%1"} {
		if !forbidden(netip.MustParseAddr(a)) {
			t.Errorf("%s (a zone) dialed", a)
		}
	}
	if !forbidden(netip.Addr{}) {
		t.Error("the zero address dialed")
	}
}

// a proxy would resolve the name itself, past the pin
func TestArtworkUsesNoProxy(t *testing.T) {
	tr, ok := NewArtwork().client.Transport.(*http.Transport)
	if !ok || tr.Proxy != nil {
		t.Fatal("the artwork transport goes through a proxy")
	}
}

// each address but the last gets its share of what is left, never under 1.5 s
func TestAttemptBudget(t *testing.T) {
	for _, c := range []struct {
		left      time.Duration
		addresses int
		want      time.Duration
	}{
		{4 * time.Second, 2, 2 * time.Second},
		{5 * time.Second, 4, 1500 * time.Millisecond},
		{2 * time.Second, 2, 1500 * time.Millisecond},
		{6 * time.Second, 3, 2 * time.Second},
	} {
		if got := attemptBudget(c.left, c.addresses); got != c.want {
			t.Errorf("%v over %d: %v, want %v", c.left, c.addresses, got, c.want)
		}
	}
}

// failures are kept 30 s and at most 64 of them, the expired dropped first
func TestFailMemo(t *testing.T) {
	now := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	m := newFailMemo()
	m.put(key("a"), errArtStatus, now)
	if err := m.get(key("a"), now.Add(29*time.Second)); !errors.Is(err, errArtStatus) {
		t.Fatalf("within 30 s: %v", err)
	}
	if m.get(key("a"), now.Add(30*time.Second)) != nil {
		t.Fatal("kept past 30 s")
	}
	for i := range 100 {
		m.put(key(strconv.Itoa(i)), errArtType, now.Add(time.Duration(i)*time.Millisecond))
	}
	later := now.Add(time.Second)
	if kept := m.get(key("35"), later) != nil; len(m.until) != 64 || kept {
		t.Fatalf("%d held, the earliest kept %v", len(m.until), kept)
	}
	if m.get(key("36"), later) == nil {
		t.Fatal("a later failure was dropped")
	}
	m.put(key("x"), errArtType, now.Add(31*time.Second))
	if len(m.until) != 1 {
		t.Fatalf("%d held: the expired failures were not dropped", len(m.until))
	}
}
