package telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5/pgtype"

	"git.shino.boo/devilreef/cadence/server/internal/api"
)

const (
	// lines with no since: further back than any retention; a search (q or
	// level) without one reads the last day, which it may have to walk whole
	logsLookback   = 31 * 24 * time.Hour
	searchLookback = 24 * time.Hour
	// lines with no until: past the furthest a line's time may lie ahead
	logsAhead = 2 * logSkew
	// a metrics read gives at most this many points, and says it was cut
	maxSeriesPoints = 50000
	maxSeriesNames  = 20
	// byte bounds of the text filters, as the store keeps them
	maxQuery = 256
	// a top list and a comm as the device's state schema has them
	maxTopProcs = 10
	maxComm     = 32
	// how long one admin read may hold a pool connection
	adminTimeout = 10 * time.Second
	// the capability whose state names the top processes
	metricsCapability = "system.metrics"
)

// the lowest severity number of each level (OTLP's ranges)
var levels = map[string]int16{"trace": 1, "debug": 5, "info": 9, "warn": 13, "error": 17, "fatal": 21}

// AdminReader is what the admin routes read the store through; *Reader is one.
type AdminReader interface {
	Logs(ctx context.Context, q LogQuery) ([]LogView, error)
	Points(ctx context.Context, device pgtype.UUID, names []string, since time.Time, limit int) ([]Point, error)
	Device(ctx context.Context, id string) (DeviceInfo, error)
}

// AdminDeps are what the telemetry admin routes read.
type AdminDeps struct {
	Reader AdminReader
	// State is a capability's last reported state; devices.Service.State is one
	State func(ctx context.Context, deviceID, capability string) (json.RawMessage, bool)
	// Vitals are a device's latest vitals, false for none; VitalsTracker.Latest
	// is one, nil has none
	Vitals func(deviceID string) (Vitals, bool)
	Now    func() time.Time
	Log    *slog.Logger
	// Timeout bounds each request's reads; zero is 10 s
	Timeout time.Duration
}

type logsIn struct {
	Device string    `query:"device" format:"uuid" doc:"one device's lines"`
	Source string    `query:"source" maxLength:"64" doc:"one source's lines, server for the server's own"`
	Level  string    `query:"level" enum:"trace,debug,info,warn,error,fatal" doc:"the lowest severity"`
	Since  time.Time `query:"since" doc:"lines at or after"`
	Until  time.Time `query:"until" doc:"lines before"`
	Q      string    `query:"q" maxLength:"256" doc:"a case-insensitive substring of the body"`
	Limit  int       `query:"limit" minimum:"1" maximum:"1000" default:"200"`
}

type logsOut struct {
	Body struct {
		Logs []LogView `json:"logs"`
	}
}

type metricsIn struct {
	ID    string    `path:"id"`
	Name  []string  `query:"name,explode" required:"true" minItems:"1" maxItems:"20" doc:"a metric's name, repeated for more"`
	Since time.Time `query:"since" doc:"points at or after; at most 24 h back, the default"`
}

// PointView is one point; a sum's carries its start, and reset when it
// starts a new run (its value fell, or its start changed).
type PointView struct {
	Time  time.Time  `json:"ts"`
	Value float64    `json:"value"`
	Start *time.Time `json:"start,omitempty"`
	Reset bool       `json:"reset,omitempty"`
}

type SeriesView struct {
	Name   string          `json:"name"`
	Kind   string          `json:"kind" enum:"gauge,sum"`
	Attrs  json.RawMessage `json:"attrs"`
	Points []PointView     `json:"points"`
}

type metricsOut struct {
	Body struct {
		Series []SeriesView `json:"series"`
		// truncated: more points than one read gives
		Truncated bool `json:"truncated"`
	}
}

type processesIn struct {
	ID string `path:"id"`
}

// ProcView is one process of the device's top lists: never its command line.
type ProcView struct {
	PID  int     `json:"pid"`
	Comm string  `json:"comm"`
	CPU  float64 `json:"cpu"`
	RSS  uint64  `json:"rss"`
}

type processesOut struct {
	Body struct {
		TopCPU []ProcView `json:"topCpu"`
		TopRSS []ProcView `json:"topRss"`
	}
}

// RegisterAdmin adds GET /logs, /devices/{id}/metrics, /devices/{id}/processes
// and /devices/{id}/vitals.
func RegisterAdmin(h huma.API, d AdminDeps) {
	defer api.Describe(h, docs)
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	if d.Timeout <= 0 {
		d.Timeout = adminTimeout
	}
	a := &admin{d: d}
	op := func(id, path string) huma.Operation {
		return huma.Operation{OperationID: id, Method: http.MethodGet, Path: path, Tags: []string{tag}}
	}
	huma.Register(h, op("list-logs", "/logs"), a.logs)
	huma.Register(h, op("list-device-metrics", "/devices/{id}/metrics"), a.metrics)
	huma.Register(h, op("get-device-processes", "/devices/{id}/processes"), a.processes)
	huma.Register(h, op("get-device-vitals", "/devices/{id}/vitals"), a.vitals)
}

// invalid refuses a query parameter, by its JSON pointer as huma names it
func invalid(param string) error {
	return api.Error(http.StatusUnprocessableEntity, "invalid", "/query/"+param)
}

type admin struct{ d AdminDeps }

// failed answers a store failure by its operation alone: a read that ran out
// of its time is 503 timeout, any other 500
func (a *admin) failed(ctx context.Context, op string, err error) error {
	if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, errReadTimeout) {
		a.d.Log.Warn("telemetry_admin_failed", "op", op, "code", "timeout")
		return api.Error(http.StatusServiceUnavailable, "timeout")
	}
	a.d.Log.Error("telemetry_admin_failed", "op", op, "code", "store")
	return api.Error(http.StatusInternalServerError, "internal")
}

func (a *admin) logs(ctx context.Context, in *logsIn) (*logsOut, error) {
	ctx, cancel := context.WithTimeout(ctx, a.d.Timeout)
	defer cancel()
	now := a.d.Now()
	q := LogQuery{Source: in.Source, MinSeverity: levels[in.Level], Since: in.Since, Until: in.Until, Contains: in.Q, Limit: in.Limit}
	switch {
	case !q.Since.IsZero():
	case in.Q != "" || in.Level != "":
		q.Since = now.Add(-searchLookback)
	default:
		q.Since = now.Add(-logsLookback)
	}
	if q.Until.IsZero() {
		q.Until = now.Add(logsAhead)
	}
	if !q.Until.After(q.Since) {
		return nil, invalid("until")
	}
	if len(in.Q) > maxQuery || !utf8.ValidString(in.Q) || strings.ContainsRune(in.Q, 0) {
		return nil, invalid("q")
	}
	if len(in.Source) > maxSource {
		return nil, invalid("source")
	}
	if in.Device != "" && q.Device.Scan(in.Device) != nil {
		return nil, invalid("device")
	}
	rows, err := a.d.Reader.Logs(ctx, q)
	if err != nil {
		return nil, a.failed(ctx, "logs", err)
	}
	out := &logsOut{}
	out.Body.Logs = rows
	return out, nil
}

func (a *admin) device(ctx context.Context, id, op string) (pgtype.UUID, error) {
	d, err := a.d.Reader.Device(ctx, id)
	switch {
	case errors.Is(err, ErrUnknownDevice):
		return pgtype.UUID{}, api.Error(http.StatusNotFound, "not_found")
	case err != nil:
		return pgtype.UUID{}, a.failed(ctx, op, err)
	}
	u, err := parseDevice(d.ID)
	if err != nil {
		return pgtype.UUID{}, a.failed(ctx, op, err)
	}
	return u, nil
}

func (a *admin) metrics(ctx context.Context, in *metricsIn) (*metricsOut, error) {
	ctx, cancel := context.WithTimeout(ctx, a.d.Timeout)
	defer cancel()
	var names []string
	for _, n := range in.Name {
		if n == "" || len(n) > maxMetricName || !utf8.ValidString(n) || strings.ContainsRune(n, 0) {
			return nil, invalid("name")
		}
		if !slices.Contains(names, n) {
			names = append(names, n)
		}
	}
	if len(names) > maxSeriesNames {
		return nil, invalid("name")
	}
	dev, err := a.device(ctx, in.ID, "metrics")
	if err != nil {
		return nil, err
	}
	since := a.d.Now().Add(-MetricRetention)
	if in.Since.After(since) {
		since = in.Since
	}
	points, err := a.d.Reader.Points(ctx, dev, names, since, maxSeriesPoints+1)
	if err != nil {
		return nil, a.failed(ctx, "metrics", err)
	}
	out := &metricsOut{}
	if len(points) > maxSeriesPoints {
		points, out.Body.Truncated = points[:maxSeriesPoints], true
	}
	out.Body.Series = seriesViews(points)
	return out, nil
}

// seriesViews groups points, in name, attribute and time order, into series,
// marking where a sum restarts
func seriesViews(points []Point) []SeriesView {
	out := []SeriesView{}
	var cur *SeriesView
	var last Point
	for _, p := range points {
		if cur == nil || p.Name != cur.Name || p.Kind != last.Kind || string(p.Attrs) != string(cur.Attrs) {
			kind := "gauge"
			if p.Kind == KindSum {
				kind = "sum"
			}
			out = append(out, SeriesView{Name: p.Name, Kind: kind, Attrs: json.RawMessage(p.Attrs), Points: []PointView{}})
			cur = &out[len(out)-1]
			last = Point{}
		}
		v := PointView{Time: p.Time.UTC(), Value: p.Value}
		if p.Kind == KindSum {
			if !p.Start.IsZero() {
				start := p.Start.UTC()
				v.Start = &start
			}
			v.Reset = len(cur.Points) > 0 && (p.Value < last.Value || !p.Start.Equal(last.Start))
		}
		cur.Points = append(cur.Points, v)
		last = p
	}
	return out
}

func (a *admin) processes(ctx context.Context, in *processesIn) (*processesOut, error) {
	ctx, cancel := context.WithTimeout(ctx, a.d.Timeout)
	defer cancel()
	dev, err := a.device(ctx, in.ID, "processes")
	if err != nil {
		return nil, err
	}
	out := &processesOut{}
	out.Body.TopCPU, out.Body.TopRSS = []ProcView{}, []ProcView{}
	raw, ok := a.d.State(ctx, dev.String(), metricsCapability)
	if !ok {
		return out, nil
	}
	// only the two lists, and only the four fields of each process
	var state struct {
		TopCPU []ProcView `json:"topCpu"`
		TopRSS []ProcView `json:"topRss"`
	}
	if json.Unmarshal(raw, &state) != nil {
		return out, nil
	}
	out.Body.TopCPU, out.Body.TopRSS = topProcs(state.TopCPU), topProcs(state.TopRSS)
	return out, nil
}

// topProcs keeps a list within the device's schema, whatever was stored: 10
// processes, comm at 32 bytes on a rune boundary
func topProcs(ps []ProcView) []ProcView {
	out := make([]ProcView, 0, min(len(ps), maxTopProcs))
	for _, p := range ps[:min(len(ps), maxTopProcs)] {
		p.Comm = cutBytes(p.Comm, maxComm)
		out = append(out, p)
	}
	return out
}

type vitalsIn struct {
	ID string `path:"id"`
}

type vitalsOut struct{ Body Vitals }

// vitals answers the device's latest vitals: 404 no_vitals when it stored
// none in the last 10 minutes
func (a *admin) vitals(ctx context.Context, in *vitalsIn) (*vitalsOut, error) {
	ctx, cancel := context.WithTimeout(ctx, a.d.Timeout)
	defer cancel()
	dev, err := a.device(ctx, in.ID, "vitals")
	if err != nil {
		return nil, err
	}
	if a.d.Vitals != nil {
		if v, ok := a.d.Vitals(dev.String()); ok {
			return &vitalsOut{Body: v}, nil
		}
	}
	return nil, api.Error(http.StatusNotFound, "no_vitals")
}
