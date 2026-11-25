package uniproxy

import (
	"bytes"
	"testing"
)

func muxed(t *testing.T, packets [][]byte) []byte {
	t.Helper()
	m := newOggMuxer(42)
	out := m.headerPages()
	for _, p := range packets {
		page, err := m.page(p)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, page...)
	}
	return out
}

func samePackets(t *testing.T, got, want [][]byte) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d packets, want %d", len(got), len(want))
	}
	for i := range got {
		if !bytes.Equal(got[i], want[i]) {
			t.Fatalf("packet %d differs", i)
		}
	}
}

// the header packets are left out, packets of any size come back whole, in any split of the bytes
func TestOggRoundTrip(t *testing.T) {
	want := [][]byte{bytes.Repeat([]byte{1}, 10), bytes.Repeat([]byte{2}, 255), bytes.Repeat([]byte{3}, 700), {4}}
	stream := muxed(t, want)
	samePackets(t, newOggDemuxer().push(stream), want)
	d := newOggDemuxer()
	var got [][]byte
	for i := 0; i < len(stream); i += 7 {
		got = append(got, d.push(stream[i:min(i+7, len(stream))])...)
	}
	samePackets(t, got, want)
}

// a corrupt page is skipped and the demuxer finds the next one
func TestOggResyncsPastACorruptPage(t *testing.T) {
	want := [][]byte{{1, 1}, {2, 2}, {3, 3}}
	stream := muxed(t, want)
	// OpusHead's page is 47 bytes, OpusTags' 51, each two-byte packet's 30: the second packet's body starts at 156
	second := 47 + 51 + 30
	if !bytes.Equal(stream[second:second+4], oggCapture) {
		t.Fatal("page layout changed")
	}
	stream[second+oggHeaderBytes+1] ^= 0xFF
	samePackets(t, newOggDemuxer().push(stream), [][]byte{{1, 1}, {3, 3}})
}

func TestOggRefusesAPacketPastOnePage(t *testing.T) {
	if _, err := newOggMuxer(1).page(make([]byte, 255*255)); err == nil {
		t.Fatal("oversized packet muxed")
	}
}
