// Package tracingtest turns tracing on for one test and reads back the spans
// it recorded, so instrumented packages can prove what joins which trace.
package tracingtest

import (
	"context"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/pkg/tracectx"
	"git.shino.boo/devilreef/cadence/server/internal/tracing"
)

// DeviceTrace and DeviceSpan are a device's voice.turn as phase 3's link
// layer would hand it down: a sampled remote parent.
const (
	DeviceTrace = "0af7651916cd43dd8448eb211c80319c"
	DeviceSpan  = "b7ad6b7169203331"
)

// Setup records every span until the test ends; tests using it must not run
// in parallel, as the provider is otel's global.
func Setup(t *testing.T) *tracing.Provider {
	t.Helper()
	p := tracing.Setup(tracing.Options{Version: "test",
		Settings: tracing.Settings{Enabled: true, SampleRatio: 1, RingSize: 100}})
	t.Cleanup(func() {
		p.Apply(tracing.Settings{Enabled: false, SampleRatio: 1, RingSize: 100})
		_ = p.Shutdown(context.Background())
	})
	return p
}

// Remote is ctx under the device's voice.turn.
func Remote(ctx context.Context) context.Context {
	return tracectx.Extract(ctx, tracectx.Carrier{Traceparent: "00-" + DeviceTrace + "-" + DeviceSpan + "-01"})
}

// Wait returns the trace's spans once every name in want has ended in it,
// failing the test after 5 s.
func Wait(t *testing.T, p *tracing.Provider, traceID string, want ...string) []tracing.SpanView {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		tr, _ := p.Store().Trace(traceID)
		if ended(tr.Spans, want) {
			return tr.Spans
		}
		if time.Now().After(deadline) {
			names := make([]string, 0, len(tr.Spans))
			for _, s := range tr.Spans {
				names = append(names, s.Name)
			}
			t.Fatalf("trace %s: want %v ended, have %v", traceID, want, names)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func ended(spans []tracing.SpanView, want []string) bool {
	for _, w := range want {
		found := false
		for _, s := range spans {
			if s.Name == w && s.End != nil {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// Find is the first span named name, failing the test without one.
func Find(t *testing.T, spans []tracing.SpanView, name string) tracing.SpanView {
	t.Helper()
	for _, s := range spans {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("no span %s", name)
	return tracing.SpanView{}
}
