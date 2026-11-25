package uniproxy

import (
	"errors"
	"math"

	"google.golang.org/protobuf/encoding/protowire"
)

// the few protobuf messages uniproxy speaks are written and read field by field: no .proto of yandex's is vendored

var errMalformed = errors.New("uniproxy: malformed message")

func str(b []byte, n protowire.Number, s string) []byte {
	return protowire.AppendString(protowire.AppendTag(b, n, protowire.BytesType), s)
}

func sub(b []byte, n protowire.Number, m []byte) []byte {
	return protowire.AppendBytes(protowire.AppendTag(b, n, protowire.BytesType), m)
}

func integer(b []byte, n protowire.Number, v uint64) []byte {
	return protowire.AppendVarint(protowire.AppendTag(b, n, protowire.VarintType), v)
}

func boolean(b []byte, n protowire.Number, v bool) []byte {
	return integer(b, n, protowire.EncodeBool(v))
}

// fields is one decoded message: the last value per field (proto3), every length-delimited occurrence in order,
// fixed32 read as an ieee float. nested messages stay raw bytes
type fields struct {
	varints map[protowire.Number]uint64
	blobs   map[protowire.Number][][]byte
	floats  map[protowire.Number]float32
}

func decode(b []byte) (fields, error) {
	f := fields{varints: map[protowire.Number]uint64{}, blobs: map[protowire.Number][][]byte{}, floats: map[protowire.Number]float32{}}
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return f, errMalformed
		}
		b = b[n:]
		switch typ {
		case protowire.VarintType:
			v, n := protowire.ConsumeVarint(b)
			if n < 0 {
				return f, errMalformed
			}
			f.varints[num] = v
			b = b[n:]
		case protowire.Fixed64Type:
			_, n := protowire.ConsumeFixed64(b)
			if n < 0 {
				return f, errMalformed
			}
			b = b[n:]
		case protowire.BytesType:
			v, n := protowire.ConsumeBytes(b)
			if n < 0 {
				return f, errMalformed
			}
			f.blobs[num] = append(f.blobs[num], v)
			b = b[n:]
		case protowire.Fixed32Type:
			v, n := protowire.ConsumeFixed32(b)
			if n < 0 {
				return f, errMalformed
			}
			f.floats[num] = math.Float32frombits(v)
			b = b[n:]
		default:
			return f, errMalformed
		}
	}
	return f, nil
}

func (f fields) varint(n protowire.Number) (uint64, bool) {
	v, ok := f.varints[n]
	return v, ok
}

func (f fields) bytes(n protowire.Number) ([]byte, bool) {
	all := f.blobs[n]
	if len(all) == 0 {
		return nil, false
	}
	return all[len(all)-1], true
}

func (f fields) string(n protowire.Number) string {
	b, _ := f.bytes(n)
	return string(b)
}

func (f fields) repeated(n protowire.Number) [][]byte { return f.blobs[n] }
