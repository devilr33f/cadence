package ambient

import (
	"bytes"
	"encoding/binary"
	"errors"
	"unicode/utf16"
)

var errPlist = errors.New("ambient: bad binary plist")

// decodeStrings reads a binary plist (bplist00) whose top object is a dict of strings, as Apple's .strings files
// in the Aerial bundle are; entries that are not string to string are skipped
func decodeStrings(data []byte) (map[string]string, error) {
	if len(data) < 8+32 || !bytes.HasPrefix(data, []byte("bplist00")) {
		return nil, errPlist
	}
	t := data[len(data)-32:]
	offSize, refSize := uint64(t[6]), uint64(t[7])
	size := uint64(len(data))
	numObjects := binary.BigEndian.Uint64(t[8:16])
	top := binary.BigEndian.Uint64(t[16:24])
	tableOff := binary.BigEndian.Uint64(t[24:32])
	if offSize < 1 || offSize > 8 || refSize < 1 || refSize > 8 || numObjects == 0 || top >= numObjects ||
		numObjects > size || tableOff >= size || tableOff+numObjects*offSize > size-32 {
		return nil, errPlist
	}
	p := plist{data: data[:len(data)-32], offSize: offSize, refSize: refSize, n: numObjects, table: tableOff}
	return p.stringDict(top)
}

type plist struct {
	data             []byte
	offSize, refSize uint64
	n, table         uint64
}

func readUint(b []byte) uint64 {
	var v uint64
	for _, c := range b {
		v = v<<8 | uint64(c)
	}
	return v
}

func (p plist) offset(obj uint64) (uint64, error) {
	if obj >= p.n {
		return 0, errPlist
	}
	at := p.table + obj*p.offSize
	off := readUint(p.data[at : at+p.offSize])
	if off >= uint64(len(p.data)) {
		return 0, errPlist
	}
	return off, nil
}

// length reads an object's count from its marker's low nibble, or the int that follows a nibble of 0xf; it returns
// the count and where the payload starts
func (p plist) length(at uint64) (uint64, uint64, error) {
	n := uint64(p.data[at] & 0x0f)
	at++
	if n != 0x0f {
		return n, at, nil
	}
	if at >= uint64(len(p.data)) || p.data[at]&0xf0 != 0x10 {
		return 0, 0, errPlist
	}
	size := uint64(1) << (p.data[at] & 0x0f)
	at++
	if size > 8 || at+size > uint64(len(p.data)) {
		return 0, 0, errPlist
	}
	return readUint(p.data[at : at+size]), at + size, nil
}

func (p plist) stringDict(obj uint64) (map[string]string, error) {
	at, err := p.offset(obj)
	if err != nil {
		return nil, err
	}
	if p.data[at]&0xf0 != 0xd0 {
		return nil, errPlist
	}
	n, start, err := p.length(at)
	if err != nil {
		return nil, err
	}
	refs := p.refSize
	if n > p.n || start+2*n*refs > uint64(len(p.data)) {
		return nil, errPlist
	}
	out := make(map[string]string, n)
	for i := range n {
		k, kok := p.str(readUint(p.data[start+i*refs : start+(i+1)*refs]))
		v, vok := p.str(readUint(p.data[start+(n+i)*refs : start+(n+i+1)*refs]))
		if kok && vok {
			out[k] = v
		}
	}
	return out, nil
}

// str reads an ascii (0x5), utf-16be (0x6) or utf-8 (0x7) string object
func (p plist) str(obj uint64) (string, bool) {
	at, err := p.offset(obj)
	if err != nil {
		return "", false
	}
	kind := p.data[at] & 0xf0
	n, start, err := p.length(at)
	if err != nil {
		return "", false
	}
	switch kind {
	case 0x50, 0x70:
		if start+n > uint64(len(p.data)) {
			return "", false
		}
		return string(p.data[start : start+n]), true
	case 0x60:
		if start+2*n > uint64(len(p.data)) {
			return "", false
		}
		u := make([]uint16, n)
		for i := range n {
			u[i] = binary.BigEndian.Uint16(p.data[start+2*i:])
		}
		return string(utf16.Decode(u)), true
	}
	return "", false
}
