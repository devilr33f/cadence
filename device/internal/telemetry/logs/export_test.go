package logs

import (
	"google.golang.org/protobuf/proto"

	logsv1 "git.shino.boo/devilreef/cadence/pkg/gen/otlp/logs/v1"
)

type Clock = clock

// SetClock replaces the pipeline's clock; set it before Emit and Run.
func SetClock(p *Pipeline, c Clock) { p.clock = c }

// Held decodes what the ring holds, oldest first, leaving the exporter's peek alone.
func Held(p *Pipeline) ([]*logsv1.LogRecord, error) {
	r := p.ring
	r.mu.Lock()
	recs := append([][]byte(nil), r.recs[r.head:]...)
	r.mu.Unlock()
	out := make([]*logsv1.LogRecord, 0, len(recs))
	for _, b := range recs {
		var lr logsv1.LogRecord
		if err := proto.Unmarshal(b, &lr); err != nil {
			return nil, err
		}
		out = append(out, &lr)
	}
	return out, nil
}

// RingCapacity is the length of the ring's backing array.
func RingCapacity(r *Ring) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return cap(r.recs)
}

// ScrubBody is what Emit keeps of a body before the cut.
func ScrubBody(s *Scrubber, body string) string { return s.scrubBody(body) }

// HeldRaw is what the ring holds, encoded, oldest first.
func HeldRaw(p *Pipeline) [][]byte {
	r := p.ring
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([][]byte, 0, len(r.recs)-r.head)
	for _, b := range r.recs[r.head:] {
		out = append(out, append([]byte(nil), b...))
	}
	return out
}

// SetRand replaces where the exporter draws its jitter, in [0, 1); set it before Run.
func SetRand(p *Pipeline, rnd func() float64) { p.rnd = rnd }
