// Command wavstat prints the format and RMS of a 16-bit PCM wav file, or with
// -goertzel one tone's level per window, for the e2e checks of audiod's output.
package main

import (
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
)

type stats struct {
	rate, channels, bits, samples int
	rms                           float64
}

// pcm is a wav's format and its interleaved samples.
type pcm struct {
	rate, channels int
	samples        []int16
}

func main() {
	in := flag.String("in", "", "wav file")
	hz := flag.Float64("goertzel", 0, "print this tone's level (Hz) per window and channel instead of the totals")
	windowMs := flag.Int("window", 100, "window length in ms for -goertzel")
	flag.Parse()
	if *in == "" {
		fmt.Fprintln(os.Stderr, "wavstat: -in is required")
		os.Exit(2)
	}
	if err := run(*in, *hz, *windowMs); err != nil {
		fmt.Fprintln(os.Stderr, "wavstat:", err)
		os.Exit(1)
	}
}

func run(in string, hz float64, windowMs int) error {
	raw, err := os.ReadFile(in) //nolint:gosec // the path is the caller's -in flag
	if err != nil {
		return err
	}
	if hz == 0 {
		st, err := stat(raw)
		if err != nil {
			return err
		}
		fmt.Println(format(st))
		return nil
	}
	p, err := parse(raw)
	if err != nil {
		return err
	}
	levels, err := toneLevels(p, hz, windowMs)
	if err != nil {
		return err
	}
	for _, w := range levels {
		fmt.Println(formatLevel(w))
	}
	return nil
}

func format(s stats) string {
	return fmt.Sprintf("rate=%d channels=%d bits=%d samples=%d rms=%.1f", s.rate, s.channels, s.bits, s.samples, s.rms)
}

func stat(raw []byte) (stats, error) {
	p, err := parse(raw)
	if err != nil {
		return stats{}, err
	}
	st := stats{rate: p.rate, channels: p.channels, bits: 16, samples: len(p.samples)}
	var sum float64
	for _, v := range p.samples {
		sum += float64(v) * float64(v)
	}
	if st.samples > 0 {
		st.rms = math.Sqrt(sum / float64(st.samples))
	}
	return st, nil
}

func parse(raw []byte) (pcm, error) {
	if len(raw) < 12 || string(raw[0:4]) != "RIFF" || string(raw[8:12]) != "WAVE" {
		return pcm{}, errors.New("not a RIFF/WAVE file")
	}
	var p pcm
	var haveFmt bool
	for pos := 12; pos+8 <= len(raw); {
		id, size := string(raw[pos:pos+4]), int(binary.LittleEndian.Uint32(raw[pos+4:]))
		body := pos + 8
		switch id {
		case "fmt ":
			if size < 16 || body+16 > len(raw) {
				return pcm{}, errors.New("short fmt chunk")
			}
			if binary.LittleEndian.Uint16(raw[body:]) != 1 {
				return pcm{}, errors.New("not PCM")
			}
			p.channels = int(binary.LittleEndian.Uint16(raw[body+2:]))
			p.rate = int(binary.LittleEndian.Uint32(raw[body+4:]))
			if binary.LittleEndian.Uint16(raw[body+14:]) != 16 {
				return pcm{}, errors.New("not 16-bit")
			}
			haveFmt = true
		case "data":
			if !haveFmt {
				return pcm{}, errors.New("data before fmt")
			}
			end := min(body+size, len(raw))
			p.samples = make([]int16, 0, (end-body)/2)
			for i := body; i+2 <= end; i += 2 {
				p.samples = append(p.samples, int16(binary.LittleEndian.Uint16(raw[i:]))) //nolint:gosec // two's complement
			}
			return p, nil
		}
		pos = body + size + size%2
	}
	return pcm{}, errors.New("no data chunk")
}
