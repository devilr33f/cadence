package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"regexp"
	"sync"
	"sync/atomic"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/board"
	"git.shino.boo/devilreef/cadence/device/internal/capability"
	"git.shino.boo/devilreef/cadence/device/internal/telemetry/logs"
	"git.shino.boo/devilreef/cadence/device/internal/telemetry/sources"
	"git.shino.boo/devilreef/cadence/device/internal/tracing"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
)

const (
	LogsID = "logs"

	// at most one state this often, and only a changed one
	logsStateEvery = 30 * time.Second
	// how often the stats are looked at for a change
	logsStatePoll = time.Second
	// what a child of audiod's that is none of the known ones ships as
	otherChild = "audiod-child"
	// as many sources as the pipeline keeps apart
	maxConfigSources = 64
	// how long a stop waits for the kernel log's reader
	kmsgStopWait = 5 * time.Second
)

// audiod's own lines, then those of its children
var audiodSources = map[string]bool{"audiod": true, "soloist": true, "shairport-sync": true, "nqptp": true, "dbus": true,
	"avahi": true}

var (
	sourceName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	levelNames = map[string]logs.Severity{"debug": logs.Debug, "info": logs.Info, "warn": logs.Warn, "error": logs.Error}
	errInvalid = errors.New("logs: invalid config")
)

// Sink is the pipeline as the logs capability drives it; *logs.Pipeline is one.
type Sink interface {
	sources.Emitter
	SetLevels(bySource map[string]logs.Severity, def logs.Severity)
	Stats() logs.Stats
}

// LogsEnv is what cadenced hands every board for its logs: the pipeline (nil
// for none: the board then has no logs capability) and the local log alone,
// which audiod's own lines go to, as they enter the pipeline as audiod's.
type LogsEnv struct {
	Sink  Sink
	Local *slog.Logger
}

// LogsOptions are what a board hands its logs capability.
type LogsOptions struct {
	Sink    Sink
	Profile board.TelemetryProfile
	// the kernel log's device, which the board names: read while the profile's Kmsg and the config's kmsg are on
	KmsgPath string
	// cadenced's own log
	Log *slog.Logger
}

// Logs is the logs capability (M6b-D2): the config sets the level per source,
// whether the kernel log is read and whether audiod's children's lines ship;
// its state is the pipeline's counts. It also starts the board's sources and
// is audiod's side of its lines (audiod.LogsHandler).
type Logs struct {
	sink       Sink
	profile    board.TelemetryProfile
	log        *slog.Logger
	openKmsg   func() (sources.KmsgFile, error)
	stateEvery time.Duration
	state      chan json.RawMessage
	kmsgNudge  chan struct{}

	// the config's: on until a config says otherwise
	kmsgOn     atomic.Bool
	thirdParty atomic.Bool
	// bumped by each config that turns the kernel log back on, so a reader that failed starts again even when the
	// loop saw neither the off nor the on
	kmsgGen atomic.Uint64

	// held while a LogsSet goes out, so audiod gets them in order
	mu   sync.Mutex
	send func(*audiodv1.Envelope) error
}

func NewLogs(o LogsOptions) *Logs {
	l := &Logs{
		sink: o.Sink, profile: o.Profile, log: o.Log, stateEvery: logsStateEvery,
		state: make(chan json.RawMessage, 1), kmsgNudge: make(chan struct{}, 1),
	}
	if o.KmsgPath != "" {
		l.openKmsg = func() (sources.KmsgFile, error) { return sources.OpenKmsg(o.KmsgPath) }
	}
	l.kmsgOn.Store(true)
	l.thirdParty.Store(true)
	return l
}

func (*Logs) Descriptor() capability.Descriptor {
	return capability.Descriptor{ID: LogsID, Version: 1, ConfigSchema: LogsConfigSchema, StateSchema: LogsStateSchema}
}

type logsConfig struct {
	levels                   map[string]logs.Severity
	kmsg, thirdParty, traces bool
}

// Apply takes a whole config: what it leaves out is the default (info for
// every source, the kernel log where the board has one, the children's lines on).
func (l *Logs) Apply(_ context.Context, _ int64, raw json.RawMessage) error {
	cfg, err := decodeLogsConfig(raw)
	if err != nil {
		return &capability.ApplyError{Code: "invalid"}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sink.SetLevels(cfg.levels, logs.Info)
	if l.kmsgOn.Swap(cfg.kmsg) != cfg.kmsg {
		if cfg.kmsg {
			l.kmsgGen.Add(1)
		}
		select {
		case l.kmsgNudge <- struct{}{}:
		default:
		}
	}
	if l.thirdParty.Swap(cfg.thirdParty) != cfg.thirdParty {
		l.sendLocked()
	}
	// the device's spans ship beside its lines; a build without tracing has none to ship either way
	tracing.SetEnabled(cfg.traces)
	return nil
}

// decodeLogsConfig takes one JSON object; unknown fields, null and trailing data are refused
func decodeLogsConfig(raw []byte) (logsConfig, error) {
	var f struct {
		Sources    json.RawMessage `json:"sources"`
		Kmsg       json.RawMessage `json:"kmsg"`
		ThirdParty json.RawMessage `json:"thirdParty"`
		Traces     json.RawMessage `json:"traces"`
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		return logsConfig{}, errInvalid
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return logsConfig{}, errInvalid
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return logsConfig{}, errInvalid
	}
	cfg := logsConfig{kmsg: true, thirdParty: true, traces: true}
	for _, b := range []struct {
		raw json.RawMessage
		to  *bool
	}{{f.Kmsg, &cfg.kmsg}, {f.ThirdParty, &cfg.thirdParty}, {f.Traces, &cfg.traces}} {
		if b.raw != nil && (bytes.Equal(b.raw, []byte("null")) || json.Unmarshal(b.raw, b.to) != nil) {
			return logsConfig{}, errInvalid
		}
	}
	if f.Sources == nil {
		return cfg, nil
	}
	var byName map[string]string
	if bytes.Equal(f.Sources, []byte("null")) || json.Unmarshal(f.Sources, &byName) != nil || len(byName) > maxConfigSources {
		return logsConfig{}, errInvalid
	}
	cfg.levels = make(map[string]logs.Severity, len(byName))
	for name, level := range byName {
		sev, ok := levelNames[level]
		if !ok || !sourceName.MatchString(name) {
			return logsConfig{}, errInvalid
		}
		cfg.levels[name] = sev
	}
	return cfg, nil
}

func (*Logs) Command(context.Context, string, json.RawMessage) (json.RawMessage, error) {
	return nil, capability.ErrUnsupported
}

func (l *Logs) State() <-chan json.RawMessage { return l.state }

// Run tails the board's files, reads the kernel log while it is wanted and
// reports the state, until ctx ends.
func (l *Logs) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for _, f := range l.profile.LogFiles {
		t := sources.NewTail(f.Source, f.Path, l.sink)
		wg.Go(func() { t.Run(ctx) })
	}
	wg.Go(func() { l.runKmsg(ctx) })
	l.reportStates(ctx)
	wg.Wait()
}

// runKmsg reads the kernel log while the board has one and the config wants
// it. A reader that failed is not started again until a config turns it off
// and on, which the generation says even when both came before the loop looked.
func (l *Logs) runKmsg(ctx context.Context) {
	var r *kmsgReader
	failed := false
	var failedGen uint64
	defer func() {
		if r != nil {
			l.stopKmsg(r)
		}
	}()
	for {
		want := l.profile.Kmsg && l.openKmsg != nil && l.kmsgOn.Load()
		if failed && (!want || l.kmsgGen.Load() != failedGen) {
			failed = false
		}
		switch {
		case want && r == nil && !failed:
			r = l.startKmsg(ctx)
		case !want && r != nil:
			l.stopKmsg(r)
			r = nil
		}
		var ended <-chan struct{}
		if r != nil {
			ended = r.done
		}
		select {
		case <-ctx.Done():
			return
		case <-l.kmsgNudge:
		case <-ended:
			l.stopKmsg(r)
			failedGen = r.gen
			r, failed = nil, true
		}
	}
}

type kmsgReader struct {
	cancel context.CancelFunc
	done   chan struct{}
	// the generation it started in: a later one starts a failed reader again
	gen uint64
}

func (l *Logs) startKmsg(ctx context.Context) *kmsgReader {
	rctx, cancel := context.WithCancel(ctx)
	r := &kmsgReader{cancel: cancel, done: make(chan struct{}), gen: l.kmsgGen.Load()}
	k := sources.NewKmsg(l.openKmsg, l.sink)
	go func() {
		defer close(r.done)
		if err := k.Run(rctx); err != nil {
			l.log.Warn("kmsg_failed")
		}
	}()
	return r
}

// stopKmsg waits a bounded time for the reader: a read the runtime cannot
// interrupt must not hold up a config or the shutdown; that reader is left to
// end on its own
func (l *Logs) stopKmsg(r *kmsgReader) {
	r.cancel()
	select {
	case <-r.done:
	case <-time.After(kmsgStopWait):
		l.log.Warn("kmsg_stop_timeout")
	}
}

type logsState struct {
	Dropped   uint64   `json:"dropped"`
	Rejected  uint64   `json:"rejected"`
	RingBytes uint64   `json:"ringBytes"`
	Sources   []string `json:"sources"`
}

// reportStates offers the state at once, then each change, at most once per stateEvery
func (l *Logs) reportStates(ctx context.Context) {
	var (
		last   []byte
		sentAt time.Time
	)
	poll := min(logsStatePoll, l.stateEvery)
	for {
		st := l.sink.Stats()
		b, err := json.Marshal(logsState{
			Dropped: st.Dropped, Rejected: st.Rejected, RingBytes: st.RingBytes,
			Sources: append(make([]string, 0, len(st.Sources)), st.Sources...),
		})
		if err == nil && !bytes.Equal(b, last) && (last == nil || time.Since(sentAt) >= l.stateEvery) {
			l.offer(b)
			last, sentAt = b, time.Now()
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(poll):
		}
	}
}

// offer replaces a state nobody took yet
func (l *Logs) offer(b []byte) {
	for {
		select {
		case l.state <- b:
			return
		default:
		}
		select {
		case <-l.state:
		default:
		}
	}
}

// LogsReady tells a new audiod connection whether its children's lines ship.
func (l *Logs) LogsReady(send func(*audiodv1.Envelope) error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.send = send
	l.sendLocked()
}

func (l *Logs) LogsGone() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.send = nil
}

// sendLocked runs under mu
func (l *Logs) sendLocked() {
	if l.send == nil {
		return
	}
	set := &audiodv1.LogsSet{ThirdParty: l.thirdParty.Load()}
	_ = l.send(&audiodv1.Envelope{Body: &audiodv1.Envelope_LogsSet{LogsSet: set}})
}

// LogLine ships one line of audiod's: its own, or a child's while the
// children's lines ship (audiod may send one before it took a LogsSet off).
// It never blocks.
func (l *Logs) LogLine(line *audiodv1.LogLine) {
	src := line.GetSource()
	if src != "audiod" {
		if !audiodSources[src] {
			src = otherChild
		}
		if !l.thirdParty.Load() {
			return
		}
	}
	text, cut := logs.CapLine(line.GetText())
	l.sink.Emit(logs.Record{
		Severity: lineSeverity(line.GetLevel()), Source: src, Body: text,
		Truncated: cut || line.GetTruncated(), Continued: line.GetContinued(),
	})
}

func lineSeverity(level audiodv1.LogLevel) logs.Severity {
	switch level {
	case audiodv1.LogLevel_LOG_LEVEL_DEBUG:
		return logs.Debug
	case audiodv1.LogLevel_LOG_LEVEL_WARN:
		return logs.Warn
	case audiodv1.LogLevel_LOG_LEVEL_ERROR:
		return logs.Error
	}
	return logs.Info
}
