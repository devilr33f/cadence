// Package caphash computes the capability report hash that Hello carries: that
// of the whole set, whichever part of it one frame holds.
package caphash

import (
	"crypto/sha256"
	"slices"
	"strings"

	"google.golang.org/protobuf/proto"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

func Report(r *cadencev2.CapabilityReport) []byte {
	c := proto.Clone(r).(*cadencev2.CapabilityReport)
	c.Hash, c.Part, c.Parts = nil, 0, 0
	slices.SortStableFunc(c.Descriptors, func(a, b *cadencev2.Descriptor) int {
		return strings.Compare(a.GetId(), b.GetId())
	})
	b, err := proto.MarshalOptions{Deterministic: true}.Marshal(c)
	if err != nil {
		// a plain message of strings and numbers cannot fail to marshal
		panic(err)
	}
	sum := sha256.Sum256(b)
	return sum[:]
}
