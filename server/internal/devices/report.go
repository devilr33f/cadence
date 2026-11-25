package devices

import (
	"bytes"
	"context"
	"math"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"google.golang.org/protobuf/encoding/protojson"

	"git.shino.boo/devilreef/cadence/pkg/caphash"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/server/internal/store/db"
)

// a report in parts holds at most this much, and its parts follow part 0
// within chunkTimeout: the device sends them back to back
const (
	maxReportParts       = 64
	maxReportDescriptors = 256
	chunkTimeout         = 10 * time.Second
)

// assembly gathers the parts of a report too large for one frame.
type assembly struct {
	hash        []byte
	parts       uint32
	next        uint32
	descriptors []*cadencev2.Descriptor
	started     time.Time
}

// report takes one report frame: a single frame is the whole report, a part
// joins the device's assembly, which is stored once its last part is in.
func (s *Service) report(ctx context.Context, deviceID string, r *cadencev2.CapabilityReport) {
	id, ok := parseID(deviceID)
	if !ok {
		return
	}
	whole, reason := s.assemble(deviceID, r)
	switch {
	case reason != "":
		s.invalidReport(deviceID, reason)
	case whole != nil:
		s.storeReport(ctx, deviceID, id, whole)
	}
}

// assemble returns the whole report once r completes it, or nil while parts
// are missing; a refused part drops the assembly and returns why.
func (s *Service) assemble(deviceID string, r *cadencev2.CapabilityReport) (*cadencev2.CapabilityReport, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.assemblies[deviceID]
	delete(s.assemblies, deviceID)
	part, parts := r.GetPart(), r.GetParts()
	switch {
	case parts <= 1 && part == 0:
		return r, ""
	case parts > maxReportParts || part >= parts:
		return nil, "chunk"
	case part == 0:
		a = &assembly{hash: r.GetHash(), parts: parts, started: s.now()}
	case a == nil:
		return nil, "chunk"
	case s.now().Sub(a.started) > chunkTimeout:
		return nil, "chunk_timeout"
	case part != a.next || parts != a.parts || !bytes.Equal(r.GetHash(), a.hash):
		return nil, "chunk"
	}
	if len(a.descriptors)+len(r.GetDescriptors()) > maxReportDescriptors {
		return nil, "chunk"
	}
	a.descriptors = append(a.descriptors, r.GetDescriptors()...)
	if a.next++; a.next < a.parts {
		s.assemblies[deviceID] = a
		return nil, ""
	}
	return &cadencev2.CapabilityReport{Descriptors: a.descriptors, Hash: a.hash}, ""
}

// dropAssembly forgets the parts of the device's last link.
func (s *Service) dropAssembly(deviceID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.assemblies, deviceID)
}

func (s *Service) invalidReport(deviceID, reason string) {
	s.log.Warn("capability_report_invalid", "device_id", deviceID, "reason", reason)
}

// storeReport validates the whole report, stores its descriptors, deletes the
// capabilities it lacks, then stores its hash and pushes the pending configs.
func (s *Service) storeReport(ctx context.Context, deviceID string, id pgtype.UUID, r *cadencev2.CapabilityReport) {
	if !bytes.Equal(r.GetHash(), caphash.Report(r)) {
		s.invalidReport(deviceID, "hash")
		return
	}
	seen := map[string]bool{}
	for _, d := range r.GetDescriptors() {
		switch {
		case d.GetId() == "":
			s.invalidReport(deviceID, "id")
			return
		case seen[d.GetId()]:
			s.invalidReport(deviceID, "duplicate")
			return
		}
		seen[d.GetId()] = true
		if !schemasCompile(d) {
			s.invalidReport(deviceID, "schema")
			return
		}
	}
	keep := make([]string, 0, len(r.GetDescriptors()))
	for _, d := range r.GetDescriptors() {
		raw, err := protojson.Marshal(d)
		if err == nil {
			cctx, cancel := s.linkCtx(ctx)
			err = s.q.UpsertCapability(cctx, db.UpsertCapabilityParams{
				DeviceID: id, Capability: d.GetId(), Version: int32(d.GetVersion() & math.MaxInt32), Descriptor: raw,
			})
			cancel()
		}
		if err != nil {
			s.log.Error("capability_store_failed", "device_id", deviceID)
			return
		}
		keep = append(keep, d.GetId())
	}
	// the hash is stored last, so a half-stored report is asked for again
	cctx, cancel := s.linkCtx(ctx)
	err := s.q.DeleteCapabilitiesNotIn(cctx, db.DeleteCapabilitiesNotInParams{DeviceID: id, Keep: keep})
	cancel()
	if err == nil {
		cctx, cancel = s.linkCtx(ctx)
		err = s.q.SetCapabilitiesHash(cctx, db.SetCapabilitiesHashParams{ID: id, CapabilitiesHash: r.GetHash()})
		cancel()
	}
	if err != nil {
		s.log.Error("capability_store_failed", "device_id", deviceID)
		return
	}
	s.pushPending(ctx, deviceID, id)
}

// schemasCompile reports whether the config, state and command schemas all compile.
func schemasCompile(d *cadencev2.Descriptor) bool {
	srcs := []string{d.GetConfigSchema(), d.GetStateSchema()}
	for _, src := range d.GetCommands() {
		srcs = append(srcs, src)
	}
	for _, src := range srcs {
		if _, err := compileSchema(src); err != nil {
			return false
		}
	}
	return true
}
