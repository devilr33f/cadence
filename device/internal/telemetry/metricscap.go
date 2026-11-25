package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"sync/atomic"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/board"
	"git.shino.boo/devilreef/cadence/device/internal/capability"
	"git.shino.boo/devilreef/cadence/device/internal/telemetry/metrics"
	"git.shino.boo/devilreef/cadence/device/internal/telemetry/otlp"
)

const (
	MetricsID = "system.metrics"

	defaultIntervalS = 30
	minIntervalS     = 10
	maxIntervalS     = 300
)

var errMetricsInvalid = errors.New("system.metrics: invalid config")

// MetricsEnv is what cadenced hands every board for its system metrics: the client its logs ship through too, and
// its own counts, which the board's input, ringer and audiod count into. A nil client gives no system.metrics
// capability; a nil App counts nothing.
type MetricsEnv struct {
	Client *otlp.Client
	App    *metrics.App
}

// MetricsOptions are what a board hands its system.metrics capability.
type MetricsOptions struct {
	Env     MetricsEnv
	Profile board.TelemetryProfile
	// the board's /proc
	Proc string
	// whether the board's media plays; nil: no media gauge
	MediaPlaying func() bool
	// more series of the board's components (the screen's player), read at each round; nil: none
	Extra func() []metrics.Point
}

// Metrics is the system.metrics capability (M6b-D5): each interval (the config's intervalS) it collects a round of
// the board's system metrics and cadenced's counts and exports it, and reports the top processes as its state.
type Metrics struct {
	col   *metrics.Collector
	exp   *metrics.Exporter
	state chan json.RawMessage
	nudge chan struct{}
	// the config's interval, in units
	intervalS atomic.Int64
	// a second; a test's millisecond
	unit time.Duration
}

func NewMetrics(o MetricsOptions) *Metrics {
	m := &Metrics{
		col: metrics.NewCollector(metrics.Options{
			Proc: os.DirFS(o.Proc), Mounts: o.Profile.Mounts, TempSensor: o.Profile.TempSensor, App: o.Env.App,
			MediaPlaying: o.MediaPlaying, Extra: o.Extra,
		}),
		exp:   metrics.NewExporter(o.Env.Client, o.Env.App),
		state: make(chan json.RawMessage, 1), nudge: make(chan struct{}, 1), unit: time.Second,
	}
	m.intervalS.Store(defaultIntervalS)
	return m
}

func (*Metrics) Descriptor() capability.Descriptor {
	return capability.Descriptor{ID: MetricsID, Version: 1, ConfigSchema: SystemMetricsConfigSchema, StateSchema: SystemMetricsStateSchema}
}

// Apply takes a whole config: without intervalS the interval is 30 s. The next round is due an interval after the last.
func (m *Metrics) Apply(_ context.Context, _ int64, raw json.RawMessage) error {
	n, err := decodeMetricsConfig(raw)
	if err != nil {
		return &capability.ApplyError{Code: "invalid"}
	}
	if m.intervalS.Swap(n) != n {
		select {
		case m.nudge <- struct{}{}:
		default:
		}
	}
	return nil
}

// decodeMetricsConfig takes one JSON object; unknown fields, null and trailing data are refused, and an integral
// number (30.0) is an integer, as the schema reads it
func decodeMetricsConfig(raw []byte) (int64, error) {
	var f struct {
		IntervalS json.RawMessage `json:"intervalS"`
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		return 0, errMetricsInvalid
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return 0, errMetricsInvalid
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return 0, errMetricsInvalid
	}
	if f.IntervalS == nil {
		return defaultIntervalS, nil
	}
	var v float64
	if bytes.Equal(f.IntervalS, []byte("null")) || json.Unmarshal(f.IntervalS, &v) != nil || v != math.Trunc(v) ||
		v < minIntervalS || v > maxIntervalS {
		return 0, errMetricsInvalid
	}
	return int64(v), nil
}

func (*Metrics) Command(context.Context, string, json.RawMessage) (json.RawMessage, error) {
	return nil, capability.ErrUnsupported
}

func (m *Metrics) State() <-chan json.RawMessage { return m.state }

// Run collects, exports and reports a round at once and then each interval, until ctx ends.
func (m *Metrics) Run(ctx context.Context) {
	for {
		last := time.Now()
		r := m.col.Collect()
		m.exp.Export(ctx, r)
		if b, err := json.Marshal(r.Top); err == nil {
			m.offer(b)
		}
		for waiting := true; waiting; {
			due := last.Add(time.Duration(m.intervalS.Load()) * m.unit)
			t := time.NewTimer(time.Until(due))
			select {
			case <-ctx.Done():
				t.Stop()
				return
			case <-m.nudge:
				// a new interval: the round is due from the last one, maybe at once
			case <-t.C:
				waiting = false
			}
			t.Stop()
		}
	}
}

// offer replaces a state nobody took yet
func (m *Metrics) offer(b []byte) {
	for {
		select {
		case m.state <- b:
			return
		default:
		}
		select {
		case <-m.state:
		default:
		}
	}
}
