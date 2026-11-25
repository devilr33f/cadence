package bluetooth

import "testing"

func TestShortID(t *testing.T) {
	id := ShortID("00:11:22:33:44:55", "aa:bb:cc:dd:ee:01")
	if len(id) != 12 || id != ShortID("00:11:22:33:44:55", "AA:BB:CC:DD:EE:01") || id == ShortID("00:11:22:33:44:56", "AA:BB:CC:DD:EE:01") {
		t.Fatalf("id %q", id)
	}
}

func TestKind(t *testing.T) {
	for _, c := range []struct {
		icon  string
		class uint32
		want  string
	}{
		{"phone", 0, "phone"}, {"computer", 0, "computer"}, {"audio-headset", 0, "headphones"}, {"audio-card", 0, "speaker"},
		{"", 0x5a020c, "phone"}, {"", 0x3a010c, "computer"}, {"", 0x240418, "headphones"}, {"", 0x240414, "speaker"},
		{"", 0x000540, "other"}, {"input-gaming", 0, "other"},
	} {
		if got := kind(c.icon, c.class); got != c.want {
			t.Errorf("kind(%q, %#x) = %s, want %s", c.icon, c.class, got, c.want)
		}
	}
}

func TestVolumeMapping(t *testing.T) {
	for _, c := range []struct{ v, p int }{{0, 0}, {127, 100}, {64, 50}, {38, 30}, {1, 1}} {
		if got := toPercent(uint8(c.v)); got != c.p {
			t.Errorf("toPercent(%d) = %d", c.v, got)
		}
	}
	for _, c := range []struct{ p, v int }{{0, 0}, {100, 127}, {30, 38}, {50, 64}, {150, 127}} {
		if got := toAVRCP(c.p); int(got) != c.v {
			t.Errorf("toAVRCP(%d) = %d", c.p, got)
		}
	}
	if got := pcmLevel(0x80<<8 | 0x40); got != 64 {
		t.Errorf("muted channel 1, 64 on channel 2: %d", got)
	}
}
