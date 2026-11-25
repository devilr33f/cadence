package capability_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	"git.shino.boo/devilreef/cadence/device/internal/capability"
	"git.shino.boo/devilreef/cadence/pkg/caphash"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/pkg/wire"
)

type fake struct{ d capability.Descriptor }

func (f fake) Descriptor() capability.Descriptor { return f.d }
func (fake) Apply(context.Context, int64, json.RawMessage) error {
	return nil
}
func (fake) Command(context.Context, string, json.RawMessage) (json.RawMessage, error) {
	return nil, capability.ErrUnsupported
}
func (fake) State() <-chan json.RawMessage { return nil }

func TestReportSortedAndHashed(t *testing.T) {
	r := capability.Report([]capability.Provider{
		fake{capability.Descriptor{ID: "b.two", Version: 2, Secrets: []capability.SecretSpec{{Name: "k", Description: "d"}}}},
		fake{capability.Descriptor{ID: "a.one", Version: 1, ConfigSchema: "{}", StateSchema: "{}"}},
	})
	if len(r.Descriptors) != 2 || r.Descriptors[0].Id != "a.one" || r.Descriptors[1].Id != "b.two" {
		t.Fatalf("descriptors = %v", r.Descriptors)
	}
	if s := r.Descriptors[1].Secrets; len(s) != 1 || s[0].Name != "k" || s[0].Description != "d" {
		t.Fatalf("secrets = %v", s)
	}
	if len(r.Hash) == 0 || !bytes.Equal(r.Hash, caphash.Report(r)) {
		t.Fatalf("hash = %x", r.Hash)
	}
}

func TestReportCarriesCommands(t *testing.T) {
	cmds := map[string]string{"play": `{"type":"object"}`, "stop": `{"type":"object","additionalProperties":false}`}
	plain := capability.Report([]capability.Provider{fake{capability.Descriptor{ID: "a.one", Version: 1}}})
	r := capability.Report([]capability.Provider{fake{capability.Descriptor{ID: "a.one", Version: 1, Commands: cmds}}})
	if got := r.Descriptors[0].Commands; len(got) != 2 || got["play"] != cmds["play"] || got["stop"] != cmds["stop"] {
		t.Fatalf("commands = %v", got)
	}
	if bytes.Equal(r.Hash, plain.Hash) {
		t.Fatal("commands left out of the hash")
	}
}

func big(id string, n int) fake {
	return fake{capability.Descriptor{ID: id, Version: 1, ConfigSchema: `{"description":"` + strings.Repeat("x", n) + `"}`, StateSchema: "{}"}}
}

func encodes(t *testing.T, r *cadencev2.CapabilityReport) {
	t.Helper()
	if _, err := wire.Encode(&cadencev2.Envelope{Body: &cadencev2.Envelope_CapabilityReport{CapabilityReport: r}}); err != nil {
		t.Fatalf("part %d: %v", r.GetPart(), err)
	}
}

// an older server hashes the frame as it comes, so a report that fits goes
// without the chunk header, byte for byte as before
func TestSmallReportSingleFrame(t *testing.T) {
	ps := []capability.Provider{big("b.two", 10), big("a.one", 10)}
	rs, err := capability.Reports(ps)
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 1 || !proto.Equal(rs[0], capability.Report(ps)) || rs[0].Part != 0 || rs[0].Parts != 0 {
		t.Fatalf("reports = %v", rs)
	}
}

func TestLargeReportChunked(t *testing.T) {
	var ps []capability.Provider
	for i := 39; i >= 0; i-- {
		ps = append(ps, big(fmt.Sprintf("cap.%02d", i), wire.MaxFrameBytes/16))
	}
	rs, err := capability.Reports(ps)
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) < 3 {
		t.Fatalf("%d parts", len(rs))
	}
	whole := capability.Report(ps)
	var union []*cadencev2.Descriptor
	for i, r := range rs {
		encodes(t, r)
		if r.Part != uint32(i) || r.Parts != uint32(len(rs)) || len(r.Descriptors) == 0 {
			t.Fatalf("part %d: header %d/%d with %d descriptors", i, r.Part, r.Parts, len(r.Descriptors))
		}
		if !bytes.Equal(r.Hash, whole.Hash) {
			t.Fatalf("part %d: hash = %x, want %x", i, r.Hash, whole.Hash)
		}
		union = append(union, r.Descriptors...)
	}
	got := &cadencev2.CapabilityReport{Descriptors: union, Hash: whole.Hash}
	if !proto.Equal(got, whole) {
		t.Fatal("parts do not add up to the report")
	}
}

func TestDescriptorTooLarge(t *testing.T) {
	_, err := capability.Reports([]capability.Provider{big("a.small", 10), big("b.huge", wire.MaxFrameBytes)})
	var tooLarge *capability.TooLargeError
	if !errors.Is(err, capability.ErrDescriptorTooLarge) || !errors.As(err, &tooLarge) || tooLarge.ID != "b.huge" {
		t.Fatalf("err = %v", err)
	}
}
