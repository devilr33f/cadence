package caphash_test

import (
	"bytes"
	"testing"

	"git.shino.boo/devilreef/cadence/pkg/caphash"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

func report() *cadencev2.CapabilityReport {
	return &cadencev2.CapabilityReport{Descriptors: []*cadencev2.Descriptor{
		{Id: "b", Version: 1, ConfigSchema: "{}"},
		{Id: "a", Version: 2, Secrets: []*cadencev2.SecretSpec{{Name: "k"}}},
	}}
}

func TestHashIgnoresHashField(t *testing.T) {
	r := report()
	want := caphash.Report(r)
	r.Hash = []byte{1, 2, 3}
	if !bytes.Equal(caphash.Report(r), want) {
		t.Fatal("hash field changed the hash")
	}
	if len(want) != 32 {
		t.Fatalf("len %d", len(want))
	}
}

// every part of a chunked report carries the hash of the whole set, so the
// chunk header cannot enter it
func TestHashIgnoresChunkHeader(t *testing.T) {
	r := report()
	want := caphash.Report(r)
	for _, h := range []struct{ part, parts uint32 }{{0, 3}, {2, 3}} {
		r.Part, r.Parts = h.part, h.parts
		if !bytes.Equal(caphash.Report(r), want) {
			t.Fatalf("part %d of %d changed the hash", h.part, h.parts)
		}
	}
}

func TestHashIgnoresDescriptorOrder(t *testing.T) {
	r := report()
	want := caphash.Report(r)
	r.Descriptors[0], r.Descriptors[1] = r.Descriptors[1], r.Descriptors[0]
	if !bytes.Equal(caphash.Report(r), want) {
		t.Fatal("order changed the hash")
	}
	if r.Descriptors[0].Id != "a" {
		t.Fatal("input was mutated")
	}
}

func TestHashChangesWithSchema(t *testing.T) {
	r := report()
	want := caphash.Report(r)
	r.Descriptors[0].ConfigSchema = `{"type":"object"}`
	if bytes.Equal(caphash.Report(r), want) {
		t.Fatal("schema change kept the hash")
	}
}
