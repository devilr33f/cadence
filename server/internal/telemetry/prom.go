package telemetry

import (
	"context"
	"log/slog"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/adaptor"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"git.shino.boo/devilreef/cadence/server/internal/api"
	"git.shino.boo/devilreef/cadence/server/internal/auth"
)

const (
	// a series whose latest point is older is left out (M6b-D9)
	seriesFresh = 10 * time.Minute
	// the series of one device a scrape serves at most, by name and attributes
	maxDeviceSeries = 1000
	scrapeTimeout   = 10 * time.Second
	deviceHelp      = "A device metric: its latest point of the last 10 minutes."
)

// PromDeps are what /metrics serves from. Ingest, Export and SelfDropped
// may be nil: their counts are then zero.
type PromDeps struct {
	// Series is the latest point of each series since since, at most
	// perDevice of each device; Reader.Latest is one
	Series      func(ctx context.Context, since time.Time, perDevice int) ([]Series, error)
	Ingest      *Ingest
	Export      *Exporter
	SelfDropped func() uint64
	Now         func() time.Time
	Log         *slog.Logger
}

// Prom serves the Prometheus exposition: every device's fresh series and the
// server's own process, ingest and export counts, from no global registry.
type Prom struct {
	d   PromDeps
	reg *prometheus.Registry
}

func NewProm(d PromDeps) *Prom {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		serverCollector{d: d})
	return &Prom{d: d, reg: reg}
}

// Route is GET /metrics as a plain admin route, its body the Prometheus text
// exposition: an admin token or one of scope metrics may scrape (M7a-D6).
func (p *Prom) Route() api.PlainRoute {
	return api.PlainRoute{
		Op: "metrics", Method: fiber.MethodGet, Path: "/metrics", Scopes: []auth.Scope{auth.ScopeMetrics}, Handler: p.serve,
	}
}

func (p *Prom) serve(c fiber.Ctx) error {
	ctx, cancel := context.WithTimeout(c.Context(), scrapeTimeout)
	defer cancel()
	// one more than a device is served tells it was cut
	rows, err := p.d.Series(ctx, p.d.Now().Add(-seriesFresh), maxDeviceSeries+1)
	if err != nil {
		// the cause can carry driver text, so only the event is logged
		p.d.Log.Warn("metrics_scrape_failed", "code", "store")
	}
	devices := prometheus.NewRegistry()
	devices.MustRegister(&deviceCollector{rows: rows, up: err == nil})
	h := promhttp.HandlerFor(prometheus.Gatherers{p.reg, devices}, promhttp.HandlerOpts{
		ErrorHandling: promhttp.ContinueOnError, ErrorLog: gatherLog{p.d.Log},
	})
	return adaptor.HTTPHandler(h)(c)
}

// gatherLog logs that gathering failed, never why: the error quotes label values
type gatherLog struct{ log *slog.Logger }

func (g gatherLog) Println(...any) { g.log.Warn("metrics_scrape_failed", "code", "gather") }

var (
	storeUpDesc = prometheus.NewDesc("cadence_server_scrape_store_up",
		"1 when this scrape read the device series from the store.", nil, nil)
	omittedDesc = prometheus.NewDesc("cadence_server_scrape_series_omitted",
		"Device series this scrape left out: reserved (a name the server's own metrics take), duplicate "+
			"(labels another series took), invalid.",
		[]string{"reason"}, nil)
	cappedDesc = prometheus.NewDesc("cadence_server_scrape_devices_capped",
		"Devices whose series this scrape cut at 1000.", nil, nil)
)

// deviceCollector serves one scrape's device series; it describes nothing,
// as its names come from the devices.
type deviceCollector struct {
	rows []Series
	up   bool
}

func (*deviceCollector) Describe(chan<- *prometheus.Desc) {}

type promSeries struct {
	name          string
	kind          int16
	names, values []string
	at            time.Time
	value         float64
}

func (c *deviceCollector) Collect(ch chan<- prometheus.Metric) {
	up := 0.0
	if c.up {
		up = 1
	}
	ch <- prometheus.MustNewConstMetric(storeUpDesc, prometheus.GaugeValue, up)
	omitted := map[string]float64{"reserved": 0, "duplicate": 0, "invalid": 0}
	byKey := map[string]*promSeries{}
	var order []*promSeries
	perDevice := map[string]int{}
	capped := 0
	for _, s := range c.rows {
		perDevice[s.DeviceID]++
		if n := perDevice[s.DeviceID]; n > maxDeviceSeries {
			if n == maxDeviceSeries+1 {
				capped++
			}
			continue
		}
		name := promName(s.Name, s.Kind)
		if reservedName(name) {
			omitted["reserved"]++
			continue
		}
		names, values, key := seriesLabels(s)
		if seen := byKey[name+"\x00"+key]; seen != nil {
			omitted["duplicate"]++
			if s.Time.After(seen.at) {
				seen.at, seen.value = s.Time, s.Value
			}
			continue
		}
		ps := &promSeries{name: name, kind: s.Kind, names: names, values: values, at: s.Time, value: s.Value}
		byKey[name+"\x00"+key] = ps
		order = append(order, ps)
	}
	for _, s := range order {
		vt := prometheus.GaugeValue
		if s.kind == KindSum {
			vt = prometheus.CounterValue
		}
		m, err := prometheus.NewConstMetric(prometheus.NewDesc(s.name, deviceHelp, s.names, nil), vt, s.value, s.values...)
		if err != nil {
			omitted["invalid"]++
			continue
		}
		ch <- m
	}
	for reason, n := range omitted {
		ch <- prometheus.MustNewConstMetric(omittedDesc, prometheus.GaugeValue, n, reason)
	}
	ch <- prometheus.MustNewConstMetric(cappedDesc, prometheus.GaugeValue, float64(capped))
}
