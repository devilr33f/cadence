package tracing

import (
	"context"
	"errors"
	"sync"
	"testing"

	"go.opentelemetry.io/otel/trace"
	"google.golang.org/protobuf/proto"

	coltracev1 "git.shino.boo/devilreef/cadence/pkg/gen/otlp/collector/trace/v1"
)

func mustSpanID(t *testing.T, hex string) trace.SpanID {
	t.Helper()
	id, err := trace.SpanIDFromHex(hex)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// setup is a provider on, sampling everything, shut down at the test's end.
func setup(t *testing.T) *Provider {
	t.Helper()
	p := Setup(Options{Version: "test", Settings: Settings{Enabled: true, SampleRatio: 1, RingSize: 100}})
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })
	return p
}

// fakeCollector records what the export posts.
type fakeCollector struct {
	mu   sync.Mutex
	reqs []*coltracev1.ExportTraceServiceRequest
	sigs []string
	err  error
	got  chan struct{}
}

func newCollector() *fakeCollector { return &fakeCollector{got: make(chan struct{}, 64)} }

func (c *fakeCollector) Post(_ context.Context, signal string, b []byte) error {
	c.mu.Lock()
	defer func() {
		c.mu.Unlock()
		c.got <- struct{}{}
	}()
	if c.err != nil {
		return c.err
	}
	var req coltracev1.ExportTraceServiceRequest
	if err := proto.Unmarshal(b, &req); err != nil {
		return err
	}
	c.reqs = append(c.reqs, &req)
	c.sigs = append(c.sigs, signal)
	return nil
}

func (c *fakeCollector) requests() []*coltracev1.ExportTraceServiceRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]*coltracev1.ExportTraceServiceRequest(nil), c.reqs...)
}

type permanentErr struct{}

func (permanentErr) Error() string   { return "refused" }
func (permanentErr) Permanent() bool { return true }

var errSecretText = errors.New("dial https://user:hunter2@collector.example/v1/traces?key=abc failed")
