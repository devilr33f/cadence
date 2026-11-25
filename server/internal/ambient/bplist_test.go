package ambient

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
	"unicode/utf16"
)

// encodeStrings writes a dict of strings as a binary plist, as Apple's .strings files are (the tests' fixtures
// only: none of Apple's files is committed). ascii strings take 0x5, others utf-16be 0x6
func encodeStrings(m map[string]string) []byte {
	keys := slices.Sorted(func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	})
	var objs [][]byte
	str := func(s string) []byte {
		ascii := true
		for _, r := range s {
			if r > 0x7f {
				ascii = false
			}
		}
		var b bytes.Buffer
		marker := func(kind byte, n int) {
			if n < 15 {
				b.WriteByte(kind | byte(n))
				return
			}
			b.WriteByte(kind | 0x0f)
			b.WriteByte(0x11)
			_ = binary.Write(&b, binary.BigEndian, uint16(n))
		}
		if ascii {
			marker(0x50, len(s))
			b.WriteString(s)
		} else {
			u := utf16.Encode([]rune(s))
			marker(0x60, len(u))
			for _, c := range u {
				_ = binary.Write(&b, binary.BigEndian, c)
			}
		}
		return b.Bytes()
	}
	n := len(keys)
	// object 0 is the dict, then keys 1..n, then values n+1..2n; refs are 2 bytes
	var dict bytes.Buffer
	if n < 15 {
		dict.WriteByte(0xd0 | byte(n))
	} else {
		dict.WriteByte(0xdf)
		dict.WriteByte(0x11)
		_ = binary.Write(&dict, binary.BigEndian, uint16(n))
	}
	for i := range n {
		_ = binary.Write(&dict, binary.BigEndian, uint16(1+i))
	}
	for i := range n {
		_ = binary.Write(&dict, binary.BigEndian, uint16(1+n+i))
	}
	objs = append(objs, dict.Bytes())
	for _, k := range keys {
		objs = append(objs, str(k))
	}
	for _, k := range keys {
		objs = append(objs, str(m[k]))
	}
	var out bytes.Buffer
	out.WriteString("bplist00")
	var offsets []uint32
	for _, o := range objs {
		offsets = append(offsets, uint32(out.Len()))
		out.Write(o)
	}
	table := out.Len()
	for _, o := range offsets {
		_ = binary.Write(&out, binary.BigEndian, o)
	}
	trailer := make([]byte, 32)
	trailer[6], trailer[7] = 4, 2
	binary.BigEndian.PutUint64(trailer[8:], uint64(len(objs)))
	binary.BigEndian.PutUint64(trailer[16:], 0)
	binary.BigEndian.PutUint64(trailer[24:], uint64(table))
	out.Write(trailer)
	return out.Bytes()
}

func TestStringsRoundTrip(t *testing.T) {
	long := strings.Repeat("Над пляжем Санта‑Моники ", 3)
	in := map[string]string{"A": "Seals", "B_0": long, "AerialCategorySpace": "Планета Земля"}
	for i := range 20 {
		in[fmt.Sprintf("K%02d", i)] = fmt.Sprint(i)
	}
	got, err := decodeStrings(encodeStrings(in))
	if err != nil {
		t.Fatal(err)
	}
	if !maps.Equal(got, in) {
		t.Fatalf("got %v", got)
	}
}

func TestStringsRefusesGarbage(t *testing.T) {
	good := encodeStrings(map[string]string{"a": "b"})
	for name, data := range map[string][]byte{
		"empty":     nil,
		"magic":     append([]byte("xplist00"), good[8:]...),
		"truncated": good[:len(good)-5],
		"trailer":   append(slices.Clone(good[:len(good)-8]), 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff),
	} {
		if _, err := decodeStrings(data); err == nil {
			t.Errorf("%s: decoded", name)
		}
	}
}
