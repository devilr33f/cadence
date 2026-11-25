package capability

import (
	"errors"
	"maps"
	"math"
	"slices"
	"strings"

	"google.golang.org/protobuf/proto"

	"git.shino.boo/devilreef/cadence/pkg/caphash"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/pkg/wire"
)

// ErrDescriptorTooLarge: one descriptor alone does not fit in a report frame.
var ErrDescriptorTooLarge = errors.New("capability: descriptor too large for a report frame")

// TooLargeError names the descriptor that does not fit.
type TooLargeError struct{ ID string }

func (*TooLargeError) Error() string { return ErrDescriptorTooLarge.Error() }
func (*TooLargeError) Unwrap() error { return ErrDescriptorTooLarge }

// Report is the whole report in one message, whether or not it fits a frame.
func Report(ps []Provider) *cadencev2.CapabilityReport {
	r := &cadencev2.CapabilityReport{Descriptors: descriptors(ps)}
	r.Hash = caphash.Report(r)
	return r
}

// Reports is the report as the link sends it: one frame without a chunk header
// when it fits, else parts packed greedily in id order, each with the hash of
// the whole report.
func Reports(ps []Provider) ([]*cadencev2.CapabilityReport, error) {
	whole := Report(ps)
	if fits(whole) {
		return []*cadencev2.CapabilityReport{whole}, nil
	}
	var parts []*cadencev2.CapabilityReport
	for _, d := range whole.Descriptors {
		if n := len(parts); n > 0 {
			last := parts[n-1]
			if last.Descriptors = append(last.Descriptors, d); fits(last) {
				continue
			}
			last.Descriptors = last.Descriptors[:len(last.Descriptors)-1]
		}
		// sized with the largest header, so the real one cannot push it over
		p := &cadencev2.CapabilityReport{
			Descriptors: []*cadencev2.Descriptor{d}, Hash: whole.Hash, Part: math.MaxUint32, Parts: math.MaxUint32,
		}
		if !fits(p) {
			return nil, &TooLargeError{ID: d.GetId()}
		}
		parts = append(parts, p)
	}
	for i, p := range parts {
		p.Part, p.Parts = uint32(i), uint32(len(parts)) //nolint:gosec // a few parts, far below 2^32
	}
	return parts, nil
}

func fits(r *cadencev2.CapabilityReport) bool {
	env := &cadencev2.Envelope{Body: &cadencev2.Envelope_CapabilityReport{CapabilityReport: r}}
	return proto.Size(env) <= wire.MaxFrameBytes
}

func descriptors(ps []Provider) []*cadencev2.Descriptor {
	ds := make([]Descriptor, 0, len(ps))
	for _, p := range ps {
		ds = append(ds, p.Descriptor())
	}
	slices.SortStableFunc(ds, func(a, b Descriptor) int { return strings.Compare(a.ID, b.ID) })
	out := make([]*cadencev2.Descriptor, 0, len(ds))
	for _, d := range ds {
		pd := &cadencev2.Descriptor{
			Id: d.ID, Version: d.Version, ConfigSchema: d.ConfigSchema, StateSchema: d.StateSchema, Commands: maps.Clone(d.Commands),
		}
		for _, s := range d.Secrets {
			pd.Secrets = append(pd.Secrets, &cadencev2.SecretSpec{Name: s.Name, Description: s.Description})
		}
		out = append(out, pd)
	}
	return out
}
