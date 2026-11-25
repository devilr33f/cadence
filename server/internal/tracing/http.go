package tracing

import (
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v3"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"git.shino.boo/devilreef/cadence/pkg/tracectx"
)

// Middleware traces the requests of the router it is used on: one server
// span each, named by method and route template (GET /api/v1/devices/:id),
// never the raw path, with the status. A caller's traceparent (Home
// Assistant, a script) makes the span its child. Websocket upgrades are left
// alone, a feed being one long connection, and so is any path skip names.
func Middleware(skip func(path string) bool) fiber.Handler {
	return func(c fiber.Ctx) error {
		if !enabled.Load() || isUpgrade(c) || (skip != nil && skip(c.Path())) {
			return c.Next()
		}
		own := c.Route().Path
		method := cut(c.Method(), 16)
		ctx := tracectx.Extract(c.Context(), tracectx.Carrier{Traceparent: c.Get(tracectx.KeyTraceparent), Tracestate: c.Get(tracectx.KeyTracestate)})
		ctx, span := StartKind(ctx, "api", method, trace.SpanKindServer, attribute.String(AttrHTTPMethod, method))
		c.SetContext(ctx)
		err := c.Next()
		status := c.Response().StatusCode()
		var fe *fiber.Error
		switch {
		case errors.As(err, &fe):
			status = fe.Code
		case err != nil:
			status = fiber.StatusInternalServerError
		}
		route := c.Route().Path
		if route == own {
			// nothing past the middleware matched: the raw path stays out
			route = "unmatched"
		}
		span.SetName(method + " " + cut(route, maxName))
		span.SetAttributes(attribute.String(AttrHTTPRoute, route), attribute.Int(AttrHTTPStatus, status),
			attribute.Int(AttrHTTPRequestSize, len(c.Request().Body())), attribute.Int(AttrHTTPResponseSize, len(c.Response().Body())))
		if status >= fiber.StatusInternalServerError {
			Fail(span, "status_"+strconv.Itoa(status))
		}
		span.End()
		return err
	}
}

func isUpgrade(c fiber.Ctx) bool {
	return strings.EqualFold(c.Get(fiber.HeaderUpgrade), "websocket")
}

// Transport wraps next (nil: http.DefaultTransport) so a request made under
// a recording span gets a client span of its own, named by method and host
// (POST openrouter.ai), with the status and the body sizes, never the path
// or the query, and carries its traceparent. The span ends with the body:
// at EOF, at an error or at Close, so a streamed answer is timed whole and
// its bytes counted as they come. A 101 (a websocket upgrade) ends it at the
// handshake, OK, and hands the body on untouched: that body is the socket
// (an io.ReadWriteCloser the websocket library needs as it is), whose life
// belongs to the caller's stream span. A request with no span over it is passed on
// as it is: background polling makes no trace of its own.
func Transport(next http.RoundTripper) http.RoundTripper {
	if next == nil {
		next = http.DefaultTransport
	}
	return &transport{next: next}
}

type transport struct{ next http.RoundTripper }

// CloseIdleConnections reaches the wrapped transport, as http.Client's does.
func (t *transport) CloseIdleConnections() {
	if c, ok := t.next.(interface{ CloseIdleConnections() }); ok {
		c.CloseIdleConnections()
	}
}

func (t *transport) RoundTrip(r *http.Request) (*http.Response, error) {
	if !enabled.Load() || !trace.SpanFromContext(r.Context()).IsRecording() {
		return t.next.RoundTrip(r)
	}
	host, port := r.URL.Hostname(), r.URL.Port()
	method := cut(r.Method, 16)
	attrs := []attribute.KeyValue{attribute.String(AttrHTTPMethod, method), attribute.String(AttrServerAddress, cut(host, maxValue))}
	if p, err := strconv.Atoi(port); err == nil {
		attrs = append(attrs, attribute.Int(AttrServerPort, p))
	}
	if r.ContentLength > 0 {
		attrs = append(attrs, attribute.Int64(AttrHTTPRequestSize, r.ContentLength))
	}
	ctx, span := StartKind(r.Context(), "http", method+" "+cut(hostName(host), maxName), trace.SpanKindClient, attrs...)
	out := r.Clone(ctx)
	if c := tracectx.Inject(ctx); !c.Empty() {
		out.Header.Set(tracectx.KeyTraceparent, c.Traceparent)
		if c.Tracestate != "" {
			out.Header.Set(tracectx.KeyTracestate, c.Tracestate)
		}
	}
	resp, err := t.next.RoundTrip(out)
	if err != nil {
		End(span, err)
		return nil, err
	}
	span.SetAttributes(attribute.Int(AttrHTTPStatus, resp.StatusCode))
	if resp.StatusCode == http.StatusSwitchingProtocols {
		// wrapped, the socket would not be a ReadWriteCloser and every dial would fail
		span.AddEvent("upgraded")
		span.End()
		return resp, nil
	}
	if resp.StatusCode >= http.StatusBadRequest {
		Fail(span, "status_"+strconv.Itoa(resp.StatusCode))
	}
	if resp.Body == nil || resp.Body == http.NoBody {
		span.End()
		return resp, nil
	}
	resp.Body = &tracedBody{ReadCloser: resp.Body, span: span, c: counter{sc: span.SpanContext(), last: time.Now()}}
	return resp, nil
}

// hostName is host without a trailing dot; an ip stays as it is.
func hostName(host string) string {
	if net.ParseIP(host) != nil {
		return host
	}
	return strings.TrimSuffix(host, ".")
}

// tracedBody ends the request's span with the body.
type tracedBody struct {
	io.ReadCloser
	span trace.Span
	c    counter
	read int64
	once sync.Once
}

func (b *tracedBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.read += int64(n)
	b.c.count(n, err != nil)
	if err != nil {
		b.finish(err)
	}
	return n, err
}

func (b *tracedBody) Close() error {
	err := b.ReadCloser.Close()
	b.finish(nil)
	return err
}

func (b *tracedBody) finish(err error) {
	b.once.Do(func() {
		b.c.flush(time.Now())
		b.span.SetAttributes(attribute.Int64(AttrHTTPResponseSize, b.read))
		if err != nil && !errors.Is(err, io.EOF) {
			FailErr(b.span, err)
		}
		b.span.End()
	})
}
