package telemetry

import (
	"context"
	"errors"
	"log/slog"
	"mime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5/pgtype"

	"git.shino.boo/devilreef/cadence/server/internal/auth"
)

// request caps (M6b-D8)
const (
	maxCompressed   = 2 << 20
	maxDecompressed = 16 << 20
	// a trace request is decoded into messages, not walked: a tighter cap
	maxTracesDecompressed = 4 << 20
	// a store call of one request
	storeTimeout = 10 * time.Second
	// a device's warnings beyond one a minute are counted into the next
	warnEvery = time.Minute
	// bodies decompressed at once, server-wide: 16 MiB buffers each at most
	decompressSlots = 4
	// how long a request waits for a slot before a 503
	slotWait = 5 * time.Second
	// how long a request's token lookup may take before a 503: with the slot
	// wait and the store, well inside the device's 30 s request timeout
	authTimeout = 5 * time.Second
)

const (
	kindLogs    = "logs"
	kindMetrics = "metrics"
	kindTraces  = "traces"
)

// IngestDeps are what the OTLP routes need.
type IngestDeps struct {
	// AuthDevice returns the id of the device a token belongs to, auth.ErrUnauthorized if none.
	AuthDevice func(ctx context.Context, token string) (string, error)
	// Secrets returns the device's capability secrets by the names the device scrubs them under.
	Secrets func(ctx context.Context, deviceID string) (map[string]string, error)
	Logs    *LogStore
	Metrics *MetricStore
	// Traces takes the devices' spans; nil leaves /otlp/v1/traces unmounted
	Traces TraceSink
	// Device names a device for its spans' stamp; Reader.Device is one, nil
	// stamps the id alone
	Device func(ctx context.Context, id string) (DeviceInfo, error)
	Now    func() time.Time
	Log    *slog.Logger
}

// Ingest serves POST /otlp/v1/logs and /otlp/v1/metrics on the device API:
// OTLP/HTTP protobuf with the device's bearer token.
type Ingest struct {
	d IngestDeps

	mu      sync.Mutex
	devices map[string]*deviceState

	slots       chan struct{}
	slotWait    time.Duration
	authTimeout time.Duration
	// a failed token lookup's warning, at most one a minute server-wide
	authWarn authWarning
	gunzip   func(b []byte, limit int) ([]byte, error)
	// counts by kind; the map is never written after NewIngest
	counts map[string]*signalCounts
}

// deviceState is a device's bucket and its warning throttle.
type deviceState struct {
	bucket
	// a request of logs, of metrics, of traces under way: one each at a time, so a
	// device's concurrent requests cannot all pass its bucket before any is charged
	logsBusy, metricsBusy, tracesBusy atomic.Bool

	warnMu     sync.Mutex
	warnedAt   time.Time
	suppressed int
}

func (d *deviceState) busy(kind string) *atomic.Bool {
	switch kind {
	case kindLogs:
		return &d.logsBusy
	case kindTraces:
		return &d.tracesBusy
	}
	return &d.metricsBusy
}

func NewIngest(d IngestDeps) *Ingest {
	if d.Now == nil {
		d.Now = time.Now
	}
	return &Ingest{d: d, devices: map[string]*deviceState{},
		slots: make(chan struct{}, decompressSlots), slotWait: slotWait, authTimeout: authTimeout,
		authWarn: authWarning{every: warnEvery}, gunzip: gunzip,
		counts: map[string]*signalCounts{kindLogs: newSignalCounts(), kindMetrics: newSignalCounts(), kindTraces: newSignalCounts()}}
}

func (in *Ingest) Mount(r fiber.Router) {
	r.Post("/otlp/v1/logs", in.serve(kindLogs))
	r.Post("/otlp/v1/metrics", in.serve(kindMetrics))
	if in.d.Traces != nil {
		r.Post("/otlp/v1/traces", in.serve(kindTraces))
	}
}

func (in *Ingest) device(id string) *deviceState {
	in.mu.Lock()
	defer in.mu.Unlock()
	d := in.devices[id]
	if d == nil {
		d = &deviceState{}
		in.devices[id] = d
	}
	return d
}

// outcome of one request: its status, for a failure the code its warning
// names, for a 429 the wait, and for a success the response and its counts
type outcome struct {
	status           int
	code             string
	wait             time.Duration
	response         []byte
	stored, rejected int
	// the decompressed bytes a refused gzip body is charged as
	charge int
}

// decompress gunzips raw in one of the server's decompression slots, at most
// limit bytes, or answers why not.
func (in *Ingest) decompress(raw []byte, limit int) ([]byte, outcome, bool) {
	wait := time.NewTimer(in.slotWait)
	defer wait.Stop()
	select {
	case in.slots <- struct{}{}:
	case <-wait.C:
		o := failed(fiber.StatusServiceUnavailable, "busy")
		o.charge = len(raw)
		return nil, o, false
	}
	body, err := in.gunzip(raw, limit)
	<-in.slots
	switch {
	case errors.Is(err, errTooLarge):
		o := failed(fiber.StatusRequestEntityTooLarge, "too_large")
		o.charge = limit
		return nil, o, false
	case err != nil:
		o := failed(fiber.StatusBadRequest, "gzip")
		o.charge = len(raw)
		return nil, o, false
	}
	return body, outcome{}, true
}

func failed(status int, code string) outcome { return outcome{status: status, code: code} }

func (in *Ingest) serve(kind string) fiber.Handler {
	return func(c fiber.Ctx) error {
		token, ok := strings.CutPrefix(c.Get(fiber.HeaderAuthorization), "Bearer ")
		if !ok || token == "" {
			return status(c, fiber.StatusUnauthorized)
		}
		id, err := in.authDevice(c.Context(), token)
		switch {
		case errors.Is(err, auth.ErrUnauthorized):
			return status(c, fiber.StatusUnauthorized)
		case err != nil:
			in.authWarn.fail(in.d.Log, kind)
			in.counts[kind].fail("auth")
			return status(c, fiber.StatusServiceUnavailable)
		}
		dev := in.device(id)
		busy := dev.busy(kind)
		if !busy.CompareAndSwap(false, true) {
			in.counts[kind].rateLimited.Add(1)
			c.Set(fiber.HeaderRetryAfter, "1")
			return status(c, fiber.StatusTooManyRequests)
		}
		defer busy.Store(false)
		o := in.handle(c, kind, id, token, dev)
		in.counts[kind].count(o)
		if o.code != "" {
			in.warn(dev, kind, id, o.code)
		}
		if o.wait > 0 {
			c.Set(fiber.HeaderRetryAfter, strconv.Itoa(retryAfter(o.wait)))
		}
		if o.status != fiber.StatusOK {
			return status(c, o.status)
		}
		c.Set(fiber.HeaderContentType, "application/x-protobuf")
		return c.Status(fiber.StatusOK).Send(o.response)
	}
}

func (in *Ingest) authDevice(ctx context.Context, token string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, in.authTimeout)
	defer cancel()
	return in.d.AuthDevice(ctx, token)
}

// status answers with code alone: no body text
func status(c fiber.Ctx, code int) error {
	c.Status(code)
	return nil
}

func (in *Ingest) handle(c fiber.Ctx, kind, id, token string, dev *deviceState) outcome {
	if !protobuf(c.Get(fiber.HeaderContentType)) {
		return failed(fiber.StatusUnsupportedMediaType, "content_type")
	}
	gzipped, ok := encoding(c.Get(fiber.HeaderContentEncoding))
	if !ok {
		return failed(fiber.StatusUnsupportedMediaType, "content_encoding")
	}
	raw := c.BodyRaw()
	if len(raw) > maxCompressed {
		return failed(fiber.StatusRequestEntityTooLarge, "too_large")
	}
	now := in.d.Now()
	// a gzip body decompresses to about its own size at least: the compressed
	// bytes are taken before the work of decompressing, the rest charged after,
	// so a device's concurrent requests are paid for as they come in
	if w := dev.take(now, len(raw)); w > 0 {
		return outcome{status: fiber.StatusTooManyRequests, wait: w}
	}
	body := raw
	if gzipped {
		var (
			o  outcome
			ok bool
		)
		limit := maxDecompressed
		if kind == kindTraces {
			limit = maxTracesDecompressed
		}
		if body, o, ok = in.decompress(raw, limit); !ok {
			dev.charge(now, o.charge-len(raw))
			return o
		}
		// in debt already, as its other signal's request left it meanwhile: refused
		// as soon as its size is known, charged all the same
		if w := dev.settle(now, len(body)-len(raw)); w > 0 {
			return outcome{status: fiber.StatusTooManyRequests, wait: w}
		}
	}
	ctx, cancel := context.WithTimeout(c.Context(), storeTimeout)
	defer cancel()
	device, err := parseDevice(id)
	if err != nil {
		return failed(fiber.StatusServiceUnavailable, "device")
	}
	var r outcome
	switch kind {
	case kindLogs:
		r = in.logs(ctx, device, id, token, body, now)
	case kindTraces:
		r = in.traces(ctx, id, token, body, now)
	default:
		r = in.metrics(ctx, device, body, now)
	}
	if r.code != "" {
		return r
	}
	in.d.Log.Debug("otlp_ingest", "kind", kind, "device_id", id, "records", r.stored, "rejected", r.rejected, "bytes", len(body))
	return r
}

func parseDevice(id string) (pgtype.UUID, error) {
	var u pgtype.UUID
	return u, u.Scan(id)
}

// protobuf is the OTLP/HTTP binary content type, parameters aside.
func protobuf(contentType string) bool {
	mt, _, err := mime.ParseMediaType(contentType)
	return err == nil && mt == "application/x-protobuf"
}

// encoding reports whether the body is gzipped; any other coding than gzip or identity is refused.
func encoding(contentEncoding string) (gzipped, ok bool) {
	switch strings.ToLower(strings.TrimSpace(contentEncoding)) {
	case "", "identity":
		return false, true
	case "gzip":
		return true, true
	}
	return false, false
}

// warn logs a device's failure at most once a minute, counting the ones it held back.
func (in *Ingest) warn(dev *deviceState, kind, id, code string) {
	dev.warnMu.Lock()
	now := in.d.Now()
	if !dev.warnedAt.IsZero() && !now.Before(dev.warnedAt) && now.Sub(dev.warnedAt) < warnEvery {
		dev.suppressed++
		dev.warnMu.Unlock()
		return
	}
	held := dev.suppressed
	dev.warnedAt, dev.suppressed = now, 0
	dev.warnMu.Unlock()
	in.d.Log.Warn("otlp_ingest_failed", "kind", kind, "device_id", id, "code", code, "suppressed", held)
}
