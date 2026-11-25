package kws

import (
	"encoding/binary"
	"errors"
	"math"
)

// ErrWAV is a WAV file this package cannot read: not RIFF/WAVE, an encoding other than 16, 24 or 32-bit PCM or
// 32-bit float, or a truncated one.
var ErrWAV = errors.New("kws: unreadable wav")

// MaxWAVSeconds is the longest recording ReadWAV takes (a dataset's ambient or speech recording is a minute or so; resampling five minutes of 48 kHz takes a few seconds)
const MaxWAVSeconds = 5 * 60

// ReadWAV decodes a WAV file to 16 kHz mono on the int16 scale: channels are averaged, other rates resampled.
func ReadWAV(b []byte) ([]int16, error) {
	if len(b) < 12 || string(b[:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		return nil, ErrWAV
	}
	var (
		format, channels, bits uint16
		rate                   uint32
		data                   []byte
		haveFmt                bool
	)
	for p := 12; p+8 <= len(b); {
		id, size := string(b[p:p+4]), int(binary.LittleEndian.Uint32(b[p+4:]))
		body := b[p+8:]
		if size < 0 || size > len(body) {
			// a streamed wav's data chunk may claim more than it has, or 0xffffffff
			if id != "data" {
				return nil, ErrWAV
			}
			size = len(body)
		}
		body = body[:size]
		switch id {
		case "fmt ":
			if size < 16 {
				return nil, ErrWAV
			}
			format = binary.LittleEndian.Uint16(body)
			channels = binary.LittleEndian.Uint16(body[2:])
			rate = binary.LittleEndian.Uint32(body[4:])
			bits = binary.LittleEndian.Uint16(body[14:])
			if format == 0xfffe && size >= 26 {
				format = binary.LittleEndian.Uint16(body[24:])
			}
			haveFmt = true
		case "data":
			data = body
		}
		p += 8 + size + size%2
	}
	if !haveFmt || data == nil || channels == 0 || rate < 4000 || rate > 192000 {
		return nil, ErrWAV
	}
	width := int(bits) / 8
	switch {
	case format == 1 && (bits == 16 || bits == 24 || bits == 32):
	case format == 3 && bits == 32:
	default:
		return nil, ErrWAV
	}
	frame := width * int(channels)
	n := len(data) / frame
	if n > MaxWAVSeconds*int(rate) {
		return nil, ErrWAV
	}
	mono := make([]float64, n)
	for i := range n {
		var sum float64
		for c := range int(channels) {
			s := data[i*frame+c*width:]
			switch {
			case format == 3:
				sum += float64(math.Float32frombits(binary.LittleEndian.Uint32(s))) * 32768
			case bits == 16:
				sum += float64(int16(binary.LittleEndian.Uint16(s))) //nolint:gosec // the sample's two's complement
			case bits == 24:
				sum += float64(int32(uint32(s[0])<<8|uint32(s[1])<<16|uint32(s[2])<<24) >> 8) //nolint:gosec // two's complement
			default:
				sum += float64(int32(binary.LittleEndian.Uint32(s))) / 65536 //nolint:gosec // two's complement
			}
		}
		mono[i] = sum / float64(channels)
	}
	if rate != SampleRate {
		mono = resample(mono, float64(rate)/SampleRate)
	}
	out := make([]int16, len(mono))
	for i, v := range mono {
		out[i] = clip16(v)
	}
	return out, nil
}

// resample reads x at ratio input samples per output sample with a windowed-sinc filter, low-passed below both
// rates' Nyquist, so a 48 kHz recording loses no band it has under 8 kHz and folds nothing above it down.
func resample(x []float64, ratio float64) []float64 {
	const taps = 16
	cutoff := min(1, 1/ratio)
	n := int(float64(len(x)) / ratio)
	out := make([]float64, n)
	half := float64(taps) / cutoff
	for i := range out {
		center := float64(i) * ratio
		lo, hi := int(math.Ceil(center-half)), int(math.Floor(center+half))
		var sum, norm float64
		for j := max(lo, 0); j <= min(hi, len(x)-1); j++ {
			d := float64(j) - center
			w := cutoff * sinc(cutoff*d) * (0.5 + 0.5*math.Cos(math.Pi*d/half))
			sum += x[j] * w
			norm += w
		}
		if norm != 0 {
			out[i] = sum / norm
		}
	}
	return out
}

func sinc(x float64) float64 {
	if x == 0 {
		return 1
	}
	return math.Sin(math.Pi*x) / (math.Pi * x)
}

func clip16(v float64) int16 { return int16(min(max(math.Round(v), -32768), 32767)) }

// WAV16 is pcm as a 16 kHz mono 16-bit WAV file (tests and tools).
func WAV16(pcm []int16) []byte {
	b := make([]byte, 44+2*len(pcm))
	copy(b, "RIFF")
	binary.LittleEndian.PutUint32(b[4:], uint32(36+2*len(pcm))) //nolint:gosec // a test file's size
	copy(b[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(b[16:], 16)
	binary.LittleEndian.PutUint16(b[20:], 1)
	binary.LittleEndian.PutUint16(b[22:], 1)
	binary.LittleEndian.PutUint32(b[24:], SampleRate)
	binary.LittleEndian.PutUint32(b[28:], 2*SampleRate)
	binary.LittleEndian.PutUint16(b[32:], 2)
	binary.LittleEndian.PutUint16(b[34:], 16)
	copy(b[36:], "data")
	binary.LittleEndian.PutUint32(b[40:], uint32(2*len(pcm))) //nolint:gosec // a test file's size
	for i, s := range pcm {
		binary.LittleEndian.PutUint16(b[44+2*i:], uint16(s)) //nolint:gosec // the sample's two's complement
	}
	return b
}
