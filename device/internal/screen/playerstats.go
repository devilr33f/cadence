package screen

import (
	"strings"
	"sync"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/telemetry/metrics"
	screenv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/screen/v1"
)

// the screen sends its figures every 10 s while a content session plays; older ones are a player that stopped
const playerStatsFresh = 25 * time.Second

// playerStats keeps the screen player's latest figures (PlayerStats) as system.metrics series: the gauges while they
// are fresh, and the counts, which the screen gives per item, as sums since cadenced started. Numbers and a decoder
// token only (rule 8).
type playerStats struct {
	mu   sync.Mutex
	last *screenv1.PlayerStats
	at   time.Time
	// the item's counts as last seen, and the totals they add to
	session string
	seen    [5]uint64
	total   [5]uint64
	counted bool
}

// the counts, in seen's and total's order
var playerCounts = [5]struct{ name, level string }{
	{"cadence.player.frame_drops", ""}, {"cadence.player.decoder_drops", ""},
	{"cadence.player.vo_delayed_frames", ""}, {"cadence.player.seeks", "low"}, {"cadence.player.seeks", "byte"},
}

func countsOf(st *screenv1.PlayerStats) [5]uint64 {
	return [5]uint64{
		st.GetFrameDrops(), st.GetDecoderDrops(), st.GetVoDelayedFrames(), st.GetLowLevelSeeks(), st.GetByteLevelSeeks(),
	}
}

func (p *playerStats) take(st *screenv1.PlayerStats, at time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := countsOf(st)
	same := st.GetSessionId() == p.session
	for i, n := range now {
		switch {
		case same && n >= p.seen[i]:
			p.total[i] += n - p.seen[i]
		default:
			// a new item (or one the player started over): its counts began at 0
			p.total[i] += n
		}
	}
	p.seen, p.session, p.counted = now, st.GetSessionId(), true
	p.last, p.at = st, at
}

// points is the round's player series at now
func (p *playerStats) points(now time.Time) []metrics.Point {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []metrics.Point
	if p.last != nil && now.Sub(p.at) < playerStatsFresh {
		st := p.last
		gauge := func(name string, v float64, attrs ...metrics.Attr) {
			out = append(out, metrics.Point{Name: name, Kind: metrics.KindGauge, Value: v, Attrs: attrs})
		}
		underrun := 0.0
		if st.GetUnderrun() {
			underrun = 1
		}
		gauge("cadence.player.cache_speed", float64(st.GetCacheSpeedBps()))
		gauge("cadence.player.input_rate", float64(st.GetInputRateBps()))
		gauge("cadence.player.cache_ahead", float64(st.GetCacheAheadBytes()))
		gauge("cadence.player.cache_total", float64(st.GetCacheTotalBytes()))
		gauge("cadence.player.buffered", float64(st.GetBufferedMs())/1000)
		gauge("cadence.player.underrun", underrun)
		gauge("cadence.player.vf_fps", float64(st.GetEstimatedVfFps()))
		gauge("cadence.player.display_fps", float64(st.GetDisplayFps()))
		gauge("cadence.player.avsync", float64(st.GetAvsyncMs())/1000)
		if d := decoderToken(st.GetHwdec()); d != "" {
			gauge("cadence.player.hwdec", 1, metrics.Attr{Key: "decoder", Value: d})
		}
	}
	if p.counted {
		for i, c := range playerCounts {
			var attrs []metrics.Attr
			if c.level != "" {
				attrs = []metrics.Attr{{Key: "level", Value: c.level}}
			}
			out = append(out, metrics.Point{Name: c.name, Kind: metrics.KindSum, Count: p.total[i], Attrs: attrs})
		}
	}
	return out
}

// decoderToken is mpv's hwdec name if it is a short token (mediacodec, vaapi-copy, no), else ""
func decoderToken(s string) string {
	if s == "" || len(s) > 32 {
		return ""
	}
	if strings.IndexFunc(s, func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' && r != '_'
	}) >= 0 {
		return ""
	}
	return s
}

// PlayerPoints is the screen player's figures as system.metrics series (cadence.player.*): its cache, input rate,
// frame rates and sync while it reports them, and its drops, late frames and source seeks counted since cadenced
// started.
func (s *Server) PlayerPoints() []metrics.Point { return s.stats.points(time.Now()) }
