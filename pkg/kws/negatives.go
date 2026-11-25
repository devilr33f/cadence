package kws

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"math"
)

// Negatives are general speech, music and noise as embeddings, which training learns the word against and calibrates
// its threshold on: independent windows of Context embeddings, and continuous streams (each a run of embeddings) for
// counting false detections per hour. They come from openWakeWord's precomputed features (CC BY-NC-SA 4.0), which the
// server fetches into the owner's install and never redistributes; this package only reads and writes the file.
type Negatives struct {
	Windows [][]float32 // each Context x EmbeddingDim
	Streams [][]float32 // each n x EmbeddingDim
	// where they came from, for the owner to see (urls and counts, no content)
	Source string
}

const negativesMagic = "CKN1"

// MaxNegativesBytes refuses a negatives file past it unread (a usual one is about 20 MiB)
const MaxNegativesBytes = 512 << 20

var ErrNegatives = errors.New("kws: malformed negatives file")

type negativesHeader struct {
	Windows int    `json:"windows"`
	Streams []int  `json:"streams"`
	Source  string `json:"source"`
}

// WriteNegatives writes n: "CKN1", u32 header length, a json header (window count, stream lengths, source), then every
// value as float16, windows first, little-endian.
func WriteNegatives(w io.Writer, n *Negatives) error {
	h := negativesHeader{Windows: len(n.Windows), Source: n.Source}
	for _, s := range n.Streams {
		h.Streams = append(h.Streams, len(s)/EmbeddingDim)
	}
	head, err := json.Marshal(h)
	if err != nil {
		return err
	}
	var pre [8]byte
	copy(pre[:], negativesMagic)
	binary.LittleEndian.PutUint32(pre[4:], uint32(len(head))) //nolint:gosec // a header of a few hundred bytes
	if _, err := w.Write(append(pre[:], head...)); err != nil {
		return err
	}
	buf := make([]byte, 0, 64<<10)
	flush := func() error {
		_, err := w.Write(buf)
		buf = buf[:0]
		return err
	}
	for _, block := range append(append([][]float32(nil), n.Windows...), n.Streams...) {
		for _, v := range block {
			buf = binary.LittleEndian.AppendUint16(buf, ToHalf(v))
			if len(buf) == cap(buf) {
				if err := flush(); err != nil {
					return err
				}
			}
		}
	}
	return flush()
}

// ReadNegatives parses WriteNegatives' file.
func ReadNegatives(b []byte) (*Negatives, error) {
	if len(b) < 8 || string(b[:4]) != negativesMagic || len(b) > MaxNegativesBytes {
		return nil, ErrNegatives
	}
	hl := int(binary.LittleEndian.Uint32(b[4:]))
	if hl > len(b)-8 {
		return nil, ErrNegatives
	}
	var h negativesHeader
	if err := json.Unmarshal(b[8:8+hl], &h); err != nil || h.Windows < 0 || h.Windows > len(b) {
		return nil, ErrNegatives
	}
	values := h.Windows * Context * EmbeddingDim
	for _, s := range h.Streams {
		if s < 0 || s > len(b) {
			return nil, ErrNegatives
		}
		values += s * EmbeddingDim
	}
	data := b[8+hl:]
	if values < 0 || len(data) != 2*values {
		return nil, ErrNegatives
	}
	all := make([]float32, values)
	for i := range all {
		all[i] = FromHalf(binary.LittleEndian.Uint16(data[2*i:]))
	}
	n := &Negatives{Source: h.Source}
	off := 0
	for range h.Windows {
		n.Windows = append(n.Windows, all[off:off+Context*EmbeddingDim:off+Context*EmbeddingDim])
		off += Context * EmbeddingDim
	}
	for _, s := range h.Streams {
		n.Streams = append(n.Streams, all[off:off+s*EmbeddingDim:off+s*EmbeddingDim])
		off += s * EmbeddingDim
	}
	return n, nil
}

// FromHalf decodes an IEEE 754 binary16 value.
func FromHalf(h uint16) float32 {
	sign := uint32(h&0x8000) << 16
	exp := uint32(h>>10) & 0x1f
	frac := uint32(h & 0x3ff)
	switch exp {
	case 0:
		v := float32(frac) * (1.0 / (1 << 24))
		if sign != 0 {
			v = -v
		}
		return v
	case 0x1f:
		return math.Float32frombits(sign | 0x7f800000 | frac<<13)
	}
	return math.Float32frombits(sign | (exp+112)<<23 | frac<<13)
}

// ToHalf encodes v as binary16, rounding to nearest even; past its range it saturates to infinity.
func ToHalf(v float32) uint16 {
	b := math.Float32bits(v)
	sign := uint16(b>>16) & 0x8000
	exp := int(b>>23&0xff) - 127 + 15
	frac := b & 0x7fffff
	switch {
	case b&0x7fffffff > 0x7f800000: // nan
		return sign | 0x7e00
	case exp >= 0x1f:
		return sign | 0x7c00
	case exp <= 0:
		if exp < -10 {
			return sign
		}
		frac |= 0x800000
		shift := uint32(14 - exp)
		half := frac >> shift
		rem := frac & (1<<shift - 1)
		mid := uint32(1) << (shift - 1)
		if rem > mid || rem == mid && half&1 == 1 {
			half++
		}
		return sign | uint16(half) //nolint:gosec // a subnormal's fraction, under 11 bits
	}
	half := uint32(exp)<<10 | frac>>13
	rem := frac & 0x1fff
	if rem > 0x1000 || rem == 0x1000 && half&1 == 1 {
		half++
	}
	return sign | uint16(half) //nolint:gosec // exponent and fraction, under 16 bits
}
