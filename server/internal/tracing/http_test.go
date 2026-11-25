package tracing

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"
	"go.opentelemetry.io/otel/trace"
)

func TestMiddlewareNamesByRouteAndJoinsTheCaller(t *testing.T) {
	p := setup(t)
	app := fiber.New()
	var inner trace.SpanContext
	api := app.Group("/api/v1", Middleware(func(path string) bool { return strings.HasPrefix(path, "/api/v1/traces") }))
	api.Get("/devices/:id", func(c fiber.Ctx) error {
		inner = trace.SpanContextFromContext(c.Context())
		return c.SendString("ok")
	})
	api.Get("/traces", func(c fiber.Ctx) error { return c.SendString("[]") })
	api.Get("/boom", func(fiber.Ctx) error { return fiber.NewError(fiber.StatusServiceUnavailable, "down") })

	req := httptest.NewRequest(http.MethodGet, "/api/v1/devices/3f1c?token=abc", nil)
	req.Header.Set("traceparent", "00-0102030405060708090a0b0c0d0e0f10-1112131415161718-01")
	if _, err := app.Test(req); err != nil {
		t.Fatal(err)
	}
	tr, ok := p.Store().Trace("0102030405060708090a0b0c0d0e0f10")
	if !ok || len(tr.Spans) != 1 {
		t.Fatalf("caller's trace not joined: %+v", p.Store().List(ListQuery{}))
	}
	v := tr.Spans[0]
	if v.Name != "GET /api/v1/devices/:id" || v.Kind != "server" || v.ParentID != "1112131415161718" || !v.RemoteParent ||
		v.Attrs[AttrHTTPStatus] != int64(200) || v.Attrs[AttrHTTPRoute] != "/api/v1/devices/:id" {
		t.Fatalf("span %+v", v)
	}
	if inner.SpanID().String() != v.SpanID {
		t.Fatal("the handler's context is not the request span's")
	}
	for _, path := range []string{"/api/v1/traces", "/api/v1/boom", "/api/v1/nowhere/secret-name"} {
		if _, err := app.Test(httptest.NewRequest(http.MethodGet, path, nil)); err != nil {
			t.Fatal(err)
		}
	}
	names := map[string]SpanView{}
	for _, sum := range p.Store().List(ListQuery{}).Traces {
		tr, _ := p.Store().Trace(sum.TraceID)
		names[tr.Spans[0].Name] = tr.Spans[0]
	}
	if _, ok := names["GET /api/v1/traces"]; ok {
		t.Fatal("a skipped path traced")
	}
	if b := names["GET /api/v1/boom"]; b.StatusCode != "status_503" || b.Attrs[AttrHTTPStatus] != int64(503) {
		t.Fatalf("failure %+v", b)
	}
	if _, ok := names["GET unmatched"]; !ok || len(names) != 3 {
		t.Fatalf("names %v", names)
	}
}

func TestTransportSpansTheCallAndCarriesTheContext(t *testing.T) {
	p := setup(t)
	var seen string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("traceparent")
		_, _ = io.WriteString(w, strings.Repeat("a", 5000))
	}))
	defer srv.Close()
	client := &http.Client{Transport: Transport(nil)}

	// no span over it: passed on as it is
	resp, err := client.Get(srv.URL + "/v1/chat?key=secret")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if seen != "" || len(p.Store().List(ListQuery{}).Traces) != 0 {
		t.Fatal("traced without a parent")
	}

	ctx, parent := Start(context.Background(), "agent", "llm.request")
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/v1/chat?key=secret", strings.NewReader("{}"))
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if req.Header.Get("traceparent") != "" {
		t.Fatal("the caller's request was changed")
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	End(parent, nil)
	tr, _ := p.Store().Trace(parent.SpanContext().TraceID().String())
	if len(tr.Spans) != 2 {
		t.Fatalf("spans %d", len(tr.Spans))
	}
	c := tr.Spans[1]
	if c.Name != "POST 127.0.0.1" || c.Kind != "client" || c.Live || c.Attrs[AttrHTTPStatus] != int64(200) ||
		c.Attrs[AttrHTTPResponseSize] != int64(5000) || c.Attrs[AttrHTTPRequestSize] != int64(2) || c.Stream == nil ||
		c.Stream.BytesIn != 5000 {
		t.Fatalf("client span %+v", c)
	}
	if !strings.Contains(seen, c.SpanID) {
		t.Fatalf("traceparent %q, span %s", seen, c.SpanID)
	}
	for k, v := range c.Attrs {
		if s, ok := v.(string); ok && (strings.Contains(s, "secret") || strings.Contains(s, "/v1/chat")) {
			t.Fatalf("%s carries the url: %s", k, s)
		}
	}
}
