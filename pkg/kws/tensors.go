package kws

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

// tensor is a dense float32 array, row-major.
type tensor struct {
	shape []int
	data  []float32
}

const tensorMagic = "CKW1"

var errTensors = errors.New("kws: malformed tensor file")

// readTensors parses tools/kws/export.py's container: "CKW1", u32 count, then per tensor u8 name length, the name,
// u8 rank, u32 dims and the float32 data, little-endian throughout.
func readTensors(b []byte) (map[string]tensor, error) {
	r := reader{b: b}
	if string(r.take(4)) != tensorMagic {
		return nil, errTensors
	}
	n := int(r.u32())
	out := make(map[string]tensor, n)
	for range n {
		name := string(r.take(int(r.u8())))
		rank := int(r.u8())
		shape := make([]int, rank)
		size := 1
		for i := range shape {
			shape[i] = int(r.u32())
			size *= shape[i]
		}
		if r.err != nil || size < 0 || size > len(r.b)/4 {
			return nil, errTensors
		}
		raw := r.take(4 * size)
		data := make([]float32, size)
		for i := range data {
			data[i] = math.Float32frombits(binary.LittleEndian.Uint32(raw[4*i:]))
		}
		out[name] = tensor{shape: shape, data: data}
	}
	if r.err != nil {
		return nil, r.err
	}
	return out, nil
}

// lookup is t[name] with exactly shape.
func lookup(t map[string]tensor, name string, shape ...int) ([]float32, error) {
	v, ok := t[name]
	if !ok || len(v.shape) != len(shape) {
		return nil, fmt.Errorf("kws: tensor %s missing or of another rank", name)
	}
	for i, d := range shape {
		if v.shape[i] != d {
			return nil, fmt.Errorf("kws: tensor %s is %v, want %v", name, v.shape, shape)
		}
	}
	return v.data, nil
}

// reader takes little-endian fields from b; past its end it keeps err and answers zeros.
type reader struct {
	b   []byte
	err error
}

func (r *reader) take(n int) []byte {
	if r.err != nil || n < 0 || n > len(r.b) {
		r.err = errTensors
		return make([]byte, max(n, 0))
	}
	out := r.b[:n]
	r.b = r.b[n:]
	return out
}

func (r *reader) u8() uint8   { return r.take(1)[0] }
func (r *reader) u32() uint32 { return binary.LittleEndian.Uint32(r.take(4)) }
