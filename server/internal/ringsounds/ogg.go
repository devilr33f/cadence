package ringsounds

import (
	"bytes"
	"encoding/binary"
)

// oggDurationMs is an Ogg Opus file's length from its last page's granule (48 kHz samples), less the pre-skip; 0 for
// a file it cannot read
func oggDurationMs(file []byte) int {
	last := bytes.LastIndex(file, []byte("OggS"))
	if last < 0 || len(file) < last+14 {
		return 0
	}
	granule := int64(binary.LittleEndian.Uint64(file[last+6:])) //nolint:gosec // a granule fits
	var preSkip int64
	if head := bytes.Index(file, []byte("OpusHead")); head >= 0 && len(file) >= head+12 {
		preSkip = int64(binary.LittleEndian.Uint16(file[head+10:]))
	}
	if granule <= preSkip {
		return 0
	}
	return int((granule - preSkip) * 1000 / 48000)
}
