package devices_test

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/server/internal/devices"
)

// split cuts r into parts of n descriptors, as a device sends a report too
// large for one frame.
func split(r *cadencev2.CapabilityReport, n int) []*cadencev2.CapabilityReport {
	var parts []*cadencev2.CapabilityReport
	for ds := r.Descriptors; len(ds) > 0; ds = ds[min(n, len(ds)):] {
		parts = append(parts, &cadencev2.CapabilityReport{Descriptors: ds[:min(n, len(ds))], Hash: r.Hash})
	}
	for i, p := range parts {
		p.Part, p.Parts = uint32(i), uint32(len(parts))
	}
	return parts
}

func descs(n int) []*cadencev2.Descriptor {
	ds := make([]*cadencev2.Descriptor, 0, n)
	for i := range n {
		ds = append(ds, desc(fmt.Sprintf("cap.%03d", i)))
	}
	return ds
}

func (e *env) stored(t *testing.T) int {
	t.Helper()
	return len(e.detail(t).Capabilities)
}

func (e *env) refused(reason string) bool {
	return strings.Contains(e.logs.String(), "msg=capability_report_invalid device_id="+e.id+" reason="+reason+"\n")
}

func TestLargeReportChunked(t *testing.T) {
	e := newEnv(t)
	r := report(descs(6)...)
	parts := split(r, 2)
	for _, p := range parts[:2] {
		e.sendReport(t, p)
		if e.stored(t) != 0 || len(e.storedHash(t)) != 0 {
			t.Fatalf("part %d of %d stored before the last", p.Part, p.Parts)
		}
	}
	e.sendReport(t, parts[2])
	if n := e.stored(t); n != 6 || !bytes.Equal(e.storedHash(t), r.Hash) {
		t.Fatalf("%d capabilities, hash %x", n, e.storedHash(t))
	}
	if w := e.svc.Hello(context.Background(), e.id, &cadencev2.Hello{CapabilitiesHash: r.Hash}); w.SendFullReport {
		t.Fatal("assembled report asked for again")
	}
	if strings.Contains(e.logs.String(), "capability_report_invalid") {
		t.Fatalf("log: %s", e.logs)
	}
}

func TestChunkOutOfOrder(t *testing.T) {
	e := newEnv(t)
	parts := split(report(descs(6)...), 2)
	for _, p := range []*cadencev2.CapabilityReport{parts[0], parts[2], parts[1]} {
		e.sendReport(t, p)
	}
	if e.stored(t) != 0 || len(e.storedHash(t)) != 0 || !e.refused("chunk") {
		t.Fatalf("log: %s", e.logs)
	}
	// a dropped assembly leaves nothing behind: the next run in order is stored
	for _, p := range parts {
		e.sendReport(t, p)
	}
	if e.stored(t) != 6 {
		t.Fatalf("%d capabilities", e.stored(t))
	}
}

func TestChunkHashMismatch(t *testing.T) {
	e := newEnv(t)
	parts := split(report(descs(6)...), 2)
	parts[1].Hash = report(desc("other")).Hash
	for _, p := range parts {
		e.sendReport(t, p)
	}
	if e.stored(t) != 0 || !e.refused("chunk") {
		t.Fatalf("log: %s", e.logs)
	}

	// parts that agree on a wrong hash are checked as a whole, as one frame is
	e = newEnv(t)
	r := report(descs(6)...)
	r.Hash = report(desc("other")).Hash
	for _, p := range split(r, 2) {
		e.sendReport(t, p)
	}
	if e.stored(t) != 0 || len(e.storedHash(t)) != 0 || !e.refused("hash") {
		t.Fatalf("log: %s", e.logs)
	}
}

func TestChunkTimeout(t *testing.T) {
	e := newEnv(t)
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	devices.SetNow(e.svc, func() time.Time { return now })
	parts := split(report(descs(6)...), 2)
	e.sendReport(t, parts[0])
	now = now.Add(10 * time.Second)
	e.sendReport(t, parts[1])
	if e.refused("chunk_timeout") {
		t.Fatal("a part at 10 s dropped")
	}
	// the age counts from part 0, not from the part before
	now = now.Add(time.Millisecond)
	e.sendReport(t, parts[2])
	if e.stored(t) != 0 || len(e.storedHash(t)) != 0 || !e.refused("chunk_timeout") {
		t.Fatalf("log: %s", e.logs)
	}
	for _, p := range parts {
		e.sendReport(t, p)
	}
	if e.stored(t) != 6 {
		t.Fatalf("%d capabilities", e.stored(t))
	}
}

func TestSingleFrameStillWorks(t *testing.T) {
	e := newEnv(t)
	r := report(desc("audio"), desc("leds"))
	e.sendReport(t, r)
	if e.stored(t) != 2 || !bytes.Equal(e.storedHash(t), r.Hash) {
		t.Fatalf("%d capabilities", e.stored(t))
	}
	one := report(desc("audio"))
	one.Parts = 1
	e.sendReport(t, one)
	if e.stored(t) != 1 || !bytes.Equal(e.storedHash(t), one.Hash) {
		t.Fatalf("parts=1: %d capabilities", e.stored(t))
	}

	// a whole report replaces one still arriving in parts
	parts := split(report(descs(4)...), 2)
	e.sendReport(t, parts[0])
	e.sendReport(t, r)
	e.sendReport(t, parts[1])
	if e.stored(t) != 2 || !bytes.Equal(e.storedHash(t), r.Hash) || !e.refused("chunk") {
		t.Fatalf("%d capabilities; log: %s", e.stored(t), e.logs)
	}
}

// a new link or the link's end starts the report over: its parts belong to that link
func TestChunkResetOnHelloAndClose(t *testing.T) {
	for _, c := range []struct {
		name  string
		reset func(e *env)
	}{
		{"hello", func(e *env) { e.svc.Hello(context.Background(), e.id, &cadencev2.Hello{}) }},
		{"close", func(e *env) { e.svc.Closed(e.id) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			parts := split(report(descs(4)...), 2)
			e.sendReport(t, parts[0])
			c.reset(e)
			e.sendReport(t, parts[1])
			if e.stored(t) != 0 || !e.refused("chunk") {
				t.Fatalf("log: %s", e.logs)
			}
		})
	}
}

func TestChunkLimits(t *testing.T) {
	e := newEnv(t)
	full := report(descs(256)...)
	for _, p := range split(full, 4) {
		e.sendReport(t, p)
	}
	if e.stored(t) != 256 || e.refused("chunk") {
		t.Fatalf("64 parts of 256 descriptors: %d stored; log: %s", e.stored(t), e.logs)
	}

	for name, parts := range map[string][]*cadencev2.CapabilityReport{
		"65 parts":        split(report(descs(65)...), 1),
		"257 descriptors": split(report(descs(257)...), 129),
		"part past parts": {{Descriptors: descs(1), Hash: full.Hash, Part: 2, Parts: 2}},
		"part of one":     {{Descriptors: descs(1), Hash: full.Hash, Part: 1}},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			for _, p := range parts {
				e.sendReport(t, p)
			}
			if e.stored(t) != 0 || !e.refused("chunk") {
				t.Fatalf("%d stored; log: %s", e.stored(t), e.logs)
			}
		})
	}
}

// the gateway serializes callbacks per device only, so the links of several
// devices reach the assemblies at once
func TestChunksOfManyDevicesAtOnce(t *testing.T) {
	svc := devices.NewService(nil, &fakeHub{}, nil, slog.New(slog.DiscardHandler))
	first := &cadencev2.Envelope{Body: &cadencev2.Envelope_CapabilityReport{CapabilityReport: split(report(descs(4)...), 2)[0]}}
	var wg sync.WaitGroup
	for i := range 8 {
		id := fmt.Sprintf("00000000-0000-0000-0000-%012d", i)
		wg.Go(func() {
			for range 50 {
				if !svc.Handle(context.Background(), id, first) {
					t.Error("part not handled")
				}
				svc.Closed(id)
			}
		})
	}
	wg.Wait()
}
