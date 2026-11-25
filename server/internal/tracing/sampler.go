package tracing

import (
	"fmt"
	"sync/atomic"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// dynamicSampler is a TraceIDRatioBased sampler whose ratio can change while
// the process runs, so the tracing setting's sampleRatio applies live: the TracerProvider
// takes its sampler once, at construction, and there is no API to swap it.
//
// The delegate is rebuilt only when the ratio actually changes and is read
// through an atomic pointer, so the sampling path — which runs for every
// span — takes no lock.
type dynamicSampler struct {
	cur atomic.Pointer[samplerState]
}

// samplerState pairs a ratio with the delegate built for it.
type samplerState struct {
	ratio float64
	inner sdktrace.Sampler
}

// newDynamicSampler returns a sampler starting at ratio (clamped to 0..1).
func newDynamicSampler(ratio float64) *dynamicSampler {
	s := &dynamicSampler{}
	s.setRatio(ratio)
	return s
}

// setRatio installs a new ratio. Values outside 0..1 are clamped, matching
// what TraceIDRatioBased does with them.
func (s *dynamicSampler) setRatio(ratio float64) {
	switch {
	case ratio < 0:
		ratio = 0
	case ratio > 1:
		ratio = 1
	}
	if cur := s.cur.Load(); cur != nil && cur.ratio == ratio {
		return
	}
	s.cur.Store(&samplerState{ratio: ratio, inner: sdktrace.TraceIDRatioBased(ratio)})
}

// ratio is the ratio currently in force.
func (s *dynamicSampler) ratio() float64 {
	if cur := s.cur.Load(); cur != nil {
		return cur.ratio
	}
	return 0
}

// ShouldSample implements sdktrace.Sampler.
func (s *dynamicSampler) ShouldSample(p sdktrace.SamplingParameters) sdktrace.SamplingResult {
	cur := s.cur.Load()
	if cur == nil {
		return sdktrace.SamplingResult{Decision: sdktrace.Drop}
	}
	return cur.inner.ShouldSample(p)
}

// Description implements sdktrace.Sampler.
func (s *dynamicSampler) Description() string {
	return fmt.Sprintf("CadenceDynamicRatio{%g}", s.ratio())
}
