package metrics

import (
	"context"
	"math"
	"math/rand/v2"
	"net/http"
	"time"

	"google.golang.org/protobuf/proto"

	"git.shino.boo/devilreef/cadence/device/internal/telemetry/otlp"
	commonv1 "git.shino.boo/devilreef/cadence/pkg/gen/otlp/common/v1"
	metricsv1 "git.shino.boo/devilreef/cadence/pkg/gen/otlp/metrics/v1"
)

// Poster sends one OTLP request; *otlp.Client is one.
type Poster interface {
	Post(ctx context.Context, path string, msg proto.Message) (status int, retryAfter time.Duration, err error)
}

// the units of the gauges (ucum); the counts have none
var units = map[string]string{
	"system.cpu.utilization": "1", "system.cpu.load_average.1m": "1", "system.cpu.load_average.5m": "1",
	"system.cpu.load_average.15m": "1", "system.memory.limit": "By", "system.memory.available": "By",
	"system.memory.used": "By", "system.filesystem.limit": "By", "system.filesystem.free": "By",
	"system.temperature": "Cel", "cadence.media_playing": "1",
	"cadence.player.cache_speed": "By/s", "cadence.player.input_rate": "By/s", "cadence.player.cache_ahead": "By",
	"cadence.player.cache_total": "By", "cadence.player.buffered": "s", "cadence.player.underrun": "1",
	"cadence.player.vf_fps": "Hz", "cadence.player.display_fps": "Hz", "cadence.player.avsync": "s",
	"cadence.player.hwdec": "1",
}

// Exporter posts each round as one ExportMetricsServiceRequest. There is no ring: a round that fails is dropped and
// counted in cadence.metrics_dropped, and the next one carries every count since the start. A server that asks to
// wait (429, 503) gets no round until then, and up to 20 % longer, so the devices it held come back spread out. Not
// safe for concurrent use.
type Exporter struct {
	client Poster
	app    *App
	// no round goes out before this, as the server asked
	holdUntil time.Time
	// draws the stretch of a server's wait, in [0, 1)
	rnd func() float64
}

func NewExporter(client Poster, app *App) *Exporter {
	return &Exporter{client: client, app: app, rnd: rand.Float64}
}

// Export reports whether the server took the round.
func (e *Exporter) Export(ctx context.Context, r Round) bool {
	if r.At.Before(e.holdUntil) {
		e.app.metricsDropped()
		return false
	}
	status, wait, err := e.client.Post(ctx, otlp.MetricsPath, otlp.MetricsRequest(encode(r, e.app.Start())))
	if err == nil && status >= 200 && status < 300 {
		return true
	}
	if status == http.StatusTooManyRequests || status == http.StatusServiceUnavailable {
		e.holdUntil = r.At.Add(otlp.Stretch(wait, e.rnd))
	}
	e.app.metricsDropped()
	return false
}

// encode puts every point of a name into one metric, in the order the names first come
func encode(r Round, start time.Time) []*metricsv1.Metric {
	var out []*metricsv1.Metric
	byName := map[string]*metricsv1.Metric{}
	for _, p := range r.Points {
		attrs := make([]*commonv1.KeyValue, 0, len(p.Attrs))
		for _, a := range p.Attrs {
			attrs = append(attrs, otlp.StringAttr(a.Key, a.Value))
		}
		m := byName[p.Name]
		if p.Kind == KindSum {
			pt := otlp.IntPoint(start, r.At, int64(min(p.Count, math.MaxInt64)), attrs) //nolint:gosec // capped
			if m == nil {
				m = otlp.MonotonicSum(p.Name, units[p.Name], nil)
			}
			if s := m.GetSum(); s != nil {
				s.DataPoints = append(s.DataPoints, pt)
			}
		} else {
			pt := otlp.DoublePoint(r.At, p.Value, attrs)
			if m == nil {
				m = otlp.Gauge(p.Name, units[p.Name], nil)
			}
			if g := m.GetGauge(); g != nil {
				g.DataPoints = append(g.DataPoints, pt)
			}
		}
		if byName[p.Name] == nil {
			byName[p.Name] = m
			out = append(out, m)
		}
	}
	return out
}

func (a *App) metricsDropped() {
	if a != nil {
		a.dropped.Inc()
	}
}
