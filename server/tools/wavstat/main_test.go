package main

import (
	"bytes"
	"encoding/binary"
	"math"
	"strings"
	"testing"
)

func wav(format uint16, bits uint16, samples []int16) []byte {
	return wavChannels(format, bits, 1, samples)
}

// wavChannels writes a 48 kHz wav of interleaved samples.
func wavChannels(format, bits, channels uint16, samples []int16) []byte {
	var b bytes.Buffer
	data := len(samples) * 2
	b.WriteString("RIFF")
	_ = binary.Write(&b, binary.LittleEndian, uint32(36+data))
	b.WriteString("WAVEfmt ")
	_ = binary.Write(&b, binary.LittleEndian, uint32(16))
	_ = binary.Write(&b, binary.LittleEndian, format)
	_ = binary.Write(&b, binary.LittleEndian, channels)
	_ = binary.Write(&b, binary.LittleEndian, uint32(48000))
	_ = binary.Write(&b, binary.LittleEndian, uint32(48000*2*uint32(channels)))
	_ = binary.Write(&b, binary.LittleEndian, 2*channels)
	_ = binary.Write(&b, binary.LittleEndian, bits)
	b.WriteString("data")
	_ = binary.Write(&b, binary.LittleEndian, uint32(data))
	_ = binary.Write(&b, binary.LittleEndian, samples)
	return b.Bytes()
}

func tone(n int, amp float64) []int16 {
	s := make([]int16, n)
	for i := range s {
		s[i] = int16(amp * 32767 * math.Sin(2*math.Pi*440*float64(i)/48000))
	}
	return s
}

func TestStatTone(t *testing.T) {
	st, err := stat(wav(1, 16, tone(48000, 0.3)))
	if err != nil {
		t.Fatal(err)
	}
	if st.rate != 48000 || st.channels != 1 || st.bits != 16 || st.samples != 48000 {
		t.Fatalf("st = %+v", st)
	}
	if want := 0.3 * 32767 / math.Sqrt2; math.Abs(st.rms-want) > want*0.01 {
		t.Fatalf("rms = %v, want %v", st.rms, want)
	}
}

func TestStatSilence(t *testing.T) {
	st, err := stat(wav(1, 16, make([]int16, 4800)))
	if err != nil || st.rms != 0 {
		t.Fatalf("st = %+v, err = %v", st, err)
	}
}

func TestStatRejects(t *testing.T) {
	for name, raw := range map[string][]byte{
		"not riff":  []byte("hello world, this is not a wav file at all"),
		"float":     wav(3, 16, tone(10, 0.1)),
		"8 bit":     wav(1, 8, tone(10, 0.1)),
		"truncated": wav(1, 16, tone(10, 0.1))[:30],
	} {
		if _, err := stat(raw); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if !strings.Contains(format(stats{rate: 1}), "rate=1 ") {
		t.Error("format")
	}
}
