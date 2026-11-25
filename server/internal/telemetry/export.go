package telemetry

import (
	"context"
	"errors"
	"iter"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// export bounds (M6b-D10)
const (
	// batches waiting, besides the one being sent; past it the oldest go
	exportQueue = 64
	// a batch holds at most this many lines, or points, and this many bytes of
	// their text (bodies; names and attributes), so the queue holds some 40 MiB
	// at most
	exportLogRows = 1000
	exportPoints  = maxPoints
	exportBytes   = 512 << 10
	// waits after a failure: from the first, doubled to the last
	exportBackoffMin = time.Second
	exportBackoffMax = time.Minute
	// how long a device lookup for stamping stays good
	exportDevicesFresh = 5 * time.Minute
	// how soon a device the lookup did not know is looked up again
	exportDevicesRetry = 10 * time.Second
)

// ExportClient posts one OTLP protobuf request of a signal (logs or
// metrics); *otlphttp.Client is one. An error with Permanent() true refused
// the request itself; one with Wait() names how long the collector asked to
// be left alone.
type ExportClient interface {
	Post(ctx context.Context, signal string, request []byte) error
}

type ExportDeps struct {
	// Devices are every device, to stamp what is exported; Reader.Devices is one
	Devices func(ctx context.Context) ([]DeviceInfo, error)
	Log     *slog.Logger
}

// ExportSignal is one signal's batches delivered and dropped.
type ExportSignal struct{ Sent, Dropped uint64 }

// ExportStats are the export's counts since the server started.
type ExportStats struct {
	Logs, Metrics ExportSignal
	Failures      uint64
	Queued        int
}

type exportBatch struct {
	signal  string
	logs    []LogRow
	metrics []MetricRow
}

// Exporter forwards stored lines and points to the collector set as its
// target, best effort: a bounded queue it drops the oldest of when full,
// never blocking the store, and a backoff of 1 s doubling to 60 s after a
// failure.
type Exporter struct {
	d ExportDeps

	mu     sync.Mutex
	queue  []*exportBatch
	target ExportClient
	wake   chan struct{}

	sentLogs, sentMetrics       atomic.Uint64
	droppedLogs, droppedMetrics atomic.Uint64
	failures                    atomic.Uint64

	backoffMin, backoffMax time.Duration
	stamp                  stamper
	// set by DevicesChanged: the next batch looks its devices up again
	devicesChanged atomic.Bool
}

// DevicesChanged says a device was renamed or deleted, so the next batch is
// stamped from a fresh lookup rather than one up to exportDevicesFresh old.
func (e *Exporter) DevicesChanged() { e.devicesChanged.Store(true) }

func NewExporter(d ExportDeps) *Exporter {
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	return &Exporter{d: d, wake: make(chan struct{}, 1), backoffMin: exportBackoffMin, backoffMax: exportBackoffMax,
		stamp: stamper{devices: d.Devices}}
}

// SetTarget makes c the collector; nil turns the export off and drops what
// waits.
func (e *Exporter) SetTarget(c ExportClient) {
	e.mu.Lock()
	e.target = c
	var gone []*exportBatch
	if c == nil {
		gone, e.queue = e.queue, nil
	}
	e.mu.Unlock()
	for _, b := range gone {
		e.dropped(b)
	}
	e.poke()
}

func (e *Exporter) poke() {
	select {
	case e.wake <- struct{}{}:
	default:
	}
}

func (e *Exporter) dropped(b *exportBatch) {
	if b.signal == kindLogs {
		e.droppedLogs.Add(1)
	} else {
		e.droppedMetrics.Add(1)
	}
}

// Logs queues stored rows, a LogStore listener: copied, in batches, and only
// while a target is set.
func (e *Exporter) Logs(rows []LogRow) {
	if !e.on() {
		return
	}
	size := func(r LogRow) int { return len(r.Body) + len(r.Source) }
	for batch := range batches(rows, exportLogRows, size) {
		e.enqueue(&exportBatch{signal: kindLogs, logs: batch})
	}
}

// Metrics queues stored points, a MetricStore listener, as Logs does.
func (e *Exporter) Metrics(rows []MetricRow) {
	if !e.on() {
		return
	}
	size := func(r MetricRow) int { return len(r.Name) + len(r.Attrs) }
	for batch := range batches(rows, exportPoints, size) {
		e.enqueue(&exportBatch{signal: kindMetrics, metrics: batch})
	}
}

// batches yields copies of rows in runs of at most most rows and exportBytes
// of their size, one row at least
func batches[T any](rows []T, most int, size func(T) int) iter.Seq[[]T] {
	return func(yield func([]T) bool) {
		for len(rows) > 0 {
			n, bytes := 0, 0
			for n < len(rows) && n < most && (n == 0 || bytes+size(rows[n]) <= exportBytes) {
				bytes += size(rows[n])
				n++
			}
			if !yield(append([]T(nil), rows[:n]...)) {
				return
			}
			rows = rows[n:]
		}
	}
}

func (e *Exporter) on() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.target != nil
}

func (e *Exporter) enqueue(b *exportBatch) {
	e.mu.Lock()
	var gone *exportBatch
	if len(e.queue) == exportQueue {
		gone = e.queue[0]
		e.queue = append(e.queue[:0], e.queue[1:]...)
	}
	e.queue = append(e.queue, b)
	e.mu.Unlock()
	if gone != nil {
		e.dropped(gone)
	}
	e.poke()
}

// Stats are the export's counts since the server started.
func (e *Exporter) Stats() ExportStats {
	e.mu.Lock()
	queued := len(e.queue)
	e.mu.Unlock()
	return ExportStats{
		Logs:     ExportSignal{Sent: e.sentLogs.Load(), Dropped: e.droppedLogs.Load()},
		Metrics:  ExportSignal{Sent: e.sentMetrics.Load(), Dropped: e.droppedMetrics.Load()},
		Failures: e.failures.Load(), Queued: queued,
	}
}

// Run sends the queue in order until stop; what waits then is left.
func (e *Exporter) Run(ctx context.Context) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		e.loop(ctx)
	}()
	return func() {
		cancel()
		<-done
	}
}

// next takes the oldest batch and the target to send it to, waiting for both.
func (e *Exporter) next(ctx context.Context) (*exportBatch, ExportClient, bool) {
	for {
		e.mu.Lock()
		if e.target != nil && len(e.queue) > 0 {
			b := e.queue[0]
			e.queue = append(e.queue[:0], e.queue[1:]...)
			t := e.target
			e.mu.Unlock()
			return b, t, true
		}
		e.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, nil, false
		case <-e.wake:
		}
	}
}

// putBack returns a batch that failed to the head of the queue, unless it
// was turned off meanwhile or newer ones filled it: then it is the oldest,
// and dropped.
func (e *Exporter) putBack(b *exportBatch) {
	e.mu.Lock()
	keep := e.target != nil && len(e.queue) < exportQueue
	if keep {
		e.queue = append([]*exportBatch{b}, e.queue...)
	}
	e.mu.Unlock()
	if !keep {
		e.dropped(b)
	}
}

type permanentError interface{ Permanent() bool }

type waitError interface{ Wait() time.Duration }

type codedError interface {
	Code() string
	Status() int
}

func (e *Exporter) loop(ctx context.Context) {
	backoff := e.backoffMin
	failing := 0
	for {
		b, target, ok := e.next(ctx)
		if !ok {
			return
		}
		err := e.send(ctx, target, b)
		if err == nil {
			if b.signal == kindLogs {
				e.sentLogs.Add(1)
			} else {
				e.sentMetrics.Add(1)
			}
			if failing > 0 {
				e.d.Log.Info("otlp_export_recovered", "failures", failing)
			}
			backoff, failing = e.backoffMin, 0
			continue
		}
		if ctx.Err() != nil {
			e.putBack(b)
			return
		}
		e.failures.Add(1)
		if failing == 0 {
			e.logFailure(b.signal, err)
		}
		failing++
		var perm permanentError
		if errors.As(err, &perm) && perm.Permanent() {
			e.dropped(b)
		} else {
			e.putBack(b)
		}
		wait := backoff
		var w waitError
		if errors.As(err, &w) {
			wait = max(wait, w.Wait())
		}
		backoff = min(backoff*2, e.backoffMax)
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// logFailure names the first failure of an outage by its code; never the
// collector's URL, header or answer
func (e *Exporter) logFailure(signal string, err error) {
	code, status := "unavailable", 0
	var c codedError
	if errors.As(err, &c) {
		code, status = c.Code(), c.Status()
	}
	e.d.Log.Warn("otlp_export_failed", "signal", signal, "code", code, "status", status)
}

func (e *Exporter) send(ctx context.Context, target ExportClient, b *exportBatch) error {
	if e.devicesChanged.Swap(false) {
		// stale at once; a failed lookup still stamps with what was known
		e.stamp.at = time.Time{}
	}
	devices := e.stamp.lookup(ctx, b)
	var (
		req []byte
		err error
	)
	if b.signal == kindLogs {
		req, err = encodeLogs(b.logs, devices)
	} else {
		req, err = encodeMetrics(b.metrics, devices)
	}
	if err != nil {
		return errEncode
	}
	return target.Post(ctx, b.signal, req)
}

// errEncode is a batch that cannot be encoded: sending it again cannot help
var errEncode = permanent{}

type permanent struct{}

func (permanent) Error() string   { return "telemetry: export encode failed" }
func (permanent) Permanent() bool { return true }
func (permanent) Code() string    { return "encode" }
func (permanent) Status() int     { return 0 }

// stamper caches the devices that stamp what is exported; only the export's
// loop uses it.
type stamper struct {
	devices func(ctx context.Context) ([]DeviceInfo, error)
	known   map[string]DeviceInfo
	at      time.Time
}

// lookup knows the devices of b's rows: looked up again when stale, or when
// one is missing and the last look is not recent; a failed look stamps the
// ids alone.
func (s *stamper) lookup(ctx context.Context, b *exportBatch) map[string]DeviceInfo {
	if s.devices == nil {
		return nil
	}
	missing := false
	check := func(id string) {
		if _, ok := s.known[id]; !ok && id != "" {
			missing = true
		}
	}
	for _, r := range b.logs {
		if r.DeviceID.Valid {
			check(r.DeviceID.String())
		}
	}
	for _, r := range b.metrics {
		check(r.DeviceID.String())
	}
	age := time.Since(s.at)
	if s.known != nil && age < exportDevicesFresh && (!missing || age < exportDevicesRetry) {
		return s.known
	}
	list, err := s.devices(ctx)
	s.at = time.Now()
	if err != nil {
		return s.known
	}
	s.known = make(map[string]DeviceInfo, len(list))
	for _, d := range list {
		s.known[d.ID] = d
	}
	return s.known
}
