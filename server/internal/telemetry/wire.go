package telemetry

import (
	"errors"
	"math"

	"google.golang.org/protobuf/encoding/protowire"
)

// the OTLP requests are walked on the wire, never decoded into the generated
// messages: a message decoded costs some hundred bytes per field of two, so 16
// MiB of tiny records would take gigabytes, while the walk keeps only the rows
// it stores
var errMalformed = errors.New("telemetry: malformed request")

type field struct {
	num protowire.Number
	typ protowire.Type
	u   uint64 // a varint, fixed32 or fixed64 value
	b   []byte // a length-delimited value
}

// walk calls fn for each field of msg in order. Groups, which OTLP never
// uses, and anything that is not well-formed are errMalformed.
func walk(msg []byte, fn func(f field) error) error {
	for len(msg) > 0 {
		num, typ, n := protowire.ConsumeTag(msg)
		if n < 0 {
			return errMalformed
		}
		msg = msg[n:]
		f := field{num: num, typ: typ}
		switch typ {
		case protowire.VarintType:
			f.u, n = protowire.ConsumeVarint(msg)
		case protowire.Fixed64Type:
			f.u, n = protowire.ConsumeFixed64(msg)
		case protowire.Fixed32Type:
			var v uint32
			v, n = protowire.ConsumeFixed32(msg)
			f.u = uint64(v)
		case protowire.BytesType:
			f.b, n = protowire.ConsumeBytes(msg)
		default:
			return errMalformed
		}
		if n < 0 {
			return errMalformed
		}
		msg = msg[n:]
		if err := fn(f); err != nil {
			return err
		}
	}
	return nil
}

// each walks msg, calling fn with the value of each length-delimited field num.
func each(msg []byte, num protowire.Number, fn func(b []byte) error) error {
	return walk(msg, func(f field) error {
		if f.num != num || f.typ != protowire.BytesType {
			return nil
		}
		return fn(f.b)
	})
}

type valueKind int

const (
	kindNone valueKind = iota
	kindString
	kindBool
	kindInt
	kindDouble
	// an array, a key-value list, bytes or a string table index: never stored
	kindOther
)

// anyValue is an OTLP AnyValue; s points into the request.
type anyValue struct {
	kind valueKind
	s    []byte
	u    uint64
}

func (v anyValue) double() float64 { return math.Float64frombits(v.u) }

// field numbers of AnyValue's oneof
const (
	anyString  = 1
	anyBool    = 2
	anyInt     = 3
	anyDouble  = 4
	anyArray   = 5
	anyKVList  = 6
	anyBytes   = 7
	anyStrIdx  = 8
	kvKey      = 1
	kvValue    = 2
	kvKeyIndex = 3
)

// parseAny reads an AnyValue: the oneof's last member wins, a member of
// another wire type than its own is skipped as unknown.
func parseAny(b []byte) (anyValue, error) {
	var v anyValue
	err := walk(b, func(f field) error {
		switch {
		case f.num == anyString && f.typ == protowire.BytesType:
			v = anyValue{kind: kindString, s: f.b}
		case f.num == anyBool && f.typ == protowire.VarintType:
			v = anyValue{kind: kindBool, u: f.u}
		case f.num == anyInt && f.typ == protowire.VarintType:
			v = anyValue{kind: kindInt, u: f.u}
		case f.num == anyDouble && f.typ == protowire.Fixed64Type:
			v = anyValue{kind: kindDouble, u: f.u}
		case (f.num == anyArray || f.num == anyKVList || f.num == anyBytes) && f.typ == protowire.BytesType,
			f.num == anyStrIdx && f.typ == protowire.VarintType:
			v = anyValue{kind: kindOther}
		}
		return nil
	})
	return v, err
}

// parseKeyValue reads a KeyValue; a key given as a string table index reads as empty.
func parseKeyValue(b []byte) (key []byte, v anyValue, err error) {
	err = walk(b, func(f field) error {
		switch {
		case f.num == kvKey && f.typ == protowire.BytesType:
			key = f.b
		case f.num == kvKeyIndex && f.typ == protowire.VarintType:
			key = nil
		case f.num == kvValue && f.typ == protowire.BytesType:
			var perr error
			v, perr = parseAny(f.b)
			return perr
		}
		return nil
	})
	return key, v, err
}
