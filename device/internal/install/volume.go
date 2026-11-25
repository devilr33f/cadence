package install

import (
	"strconv"
	"strings"

	"git.shino.boo/devilreef/cadence/device/internal/volume"
)

// v1Level reads v1's state/volume exactly as v1's persisted() did (ruling M2-R9): at most 8 bytes, at most one
// newline, at its end, then 1 to 3 digits (leading zeros allowed) and at most 100. anything else v1 itself did not
// trust, so it is invalid, never clamped: a corrupt file must not come out loud (rule 7)
func v1Level(raw []byte) (int, bool) {
	if len(raw) > 8 {
		return 0, false
	}
	s := strings.TrimSuffix(string(raw), "\n")
	if s == "" || len(s) > 3 {
		return 0, false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(s)
	if err != nil || n > 100 {
		return 0, false
	}
	return n, true
}

// toV2 is v2's state/volume for v1's file (nil when there is none), false when v1 kept no level it trusted: v2 then
// gets no file, so the wrapper and cadenced start at their default with the level unknown, and a sender's volume is
// not taken against a level nobody set (ruling M2-R9 as amended)
func toV2(raw []byte) ([]byte, int, bool) {
	level, ok := v1Level(raw)
	if !ok {
		return nil, 0, false
	}
	return volume.EncodeLevel(level), level, true
}

// toV1 is v1's state/volume for v2's file, false when v2 has no level: v1 then keeps its own
func toV1(raw []byte) ([]byte, int, bool) {
	level, ok := volume.ParseLevel(raw)
	if !ok {
		return nil, 0, false
	}
	return []byte(strconv.Itoa(level) + "\n"), level, true
}
