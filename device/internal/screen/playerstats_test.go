package screen

import (
	"slices"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/telemetry/metrics"
	screenv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/screen/v1"
)

func point(ps []metrics.Point, name string, attrs ...metrics.Attr) (metrics.Point, bool) {
	for _, p := range ps {
		if p.Name == name && slices.Equal(p.Attrs, attrs) {
			return p, true
		}
	}
	return metrics.Point{}, false
}

// the player's figures are gauges while fresh; its per-item counts add up across items into sums that never go back
func TestPlayerStatsBecomeSeries(t *testing.T) {
	var p playerStats
	t0 := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	if got := p.points(t0); len(got) != 0 {
		t.Fatalf("points before any stats: %v", got)
	}
	p.take(&screenv1.PlayerStats{
		SessionId: "a", CacheSpeedBps: 2_500_000, BufferedMs: 42_000, AvsyncMs: -12, EstimatedVfFps: 23.976,
		FrameDrops: 4, DecoderDrops: 1, LowLevelSeeks: 2, Hwdec: "mediacodec", Underrun: true,
	}, t0)
	p.take(&screenv1.PlayerStats{SessionId: "a", FrameDrops: 6, DecoderDrops: 1, LowLevelSeeks: 2, Hwdec: "mediacodec"}, t0)
	// a new item starts its counts at 0 again
	p.take(&screenv1.PlayerStats{
		SessionId: "b", CacheSpeedBps: 1_000_000, BufferedMs: 8_000, AvsyncMs: 20, FrameDrops: 1, ByteLevelSeeks: 5,
		Hwdec: "Bad Token!",
	}, t0)
	got := p.points(t0.Add(10 * time.Second))
	want := map[string]float64{"cadence.player.cache_speed": 1_000_000, "cadence.player.buffered": 8, "cadence.player.avsync": 0.02}
	for name, v := range want {
		if pt, ok := point(got, name); !ok || pt.Kind != metrics.KindGauge || pt.Value != v {
			t.Fatalf("%s = %v %v, want %v", name, pt, ok, v)
		}
	}
	if _, ok := point(got, "cadence.player.hwdec", metrics.Attr{Key: "decoder", Value: "Bad Token!"}); ok {
		t.Fatal("a decoder name that is no token was exported")
	}
	sums := []struct {
		name  string
		attrs []metrics.Attr
		want  uint64
	}{
		{"cadence.player.frame_drops", nil, 7}, {"cadence.player.decoder_drops", nil, 1},
		{"cadence.player.seeks", []metrics.Attr{{Key: "level", Value: "low"}}, 2},
		{"cadence.player.seeks", []metrics.Attr{{Key: "level", Value: "byte"}}, 5},
	}
	for _, s := range sums {
		if pt, ok := point(got, s.name, s.attrs...); !ok || pt.Kind != metrics.KindSum || pt.Count != s.want {
			t.Fatalf("%s %v = %v %v, want %d", s.name, s.attrs, pt, ok, s.want)
		}
	}
	// stale figures leave the gauges out; the sums stay
	stale := p.points(t0.Add(time.Minute))
	if _, ok := point(stale, "cadence.player.buffered"); ok {
		t.Fatal("a stale gauge was exported")
	}
	if pt, ok := point(stale, "cadence.player.frame_drops"); !ok || pt.Count != 7 {
		t.Fatalf("stale drops = %v %v", pt, ok)
	}
	p.take(&screenv1.PlayerStats{SessionId: "b", Hwdec: "mediacodec-copy"}, t0.Add(time.Minute))
	if _, ok := point(p.points(t0.Add(time.Minute)), "cadence.player.hwdec", metrics.Attr{Key: "decoder", Value: "mediacodec-copy"}); !ok {
		t.Fatal("no hwdec gauge")
	}
}
