// Command opusframes encodes a 16 kHz mono 16-bit WAV into a .opusframes file
// (20 ms Opus packets). It needs cgo and libopus, so it runs in the fixtures image.
package main

import (
	"bufio"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"os"

	"git.shino.boo/devilreef/cadence/pkg/opusframes"
	"git.shino.boo/devilreef/cadence/server/internal/audio"
)

const sampleRate = 16000

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "opusframes:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("opusframes", flag.ContinueOnError)
	in := fs.String("in", "", "16 kHz mono 16-bit wav")
	out := fs.String("out", "", "output .opusframes file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *in == "" || *out == "" {
		return errors.New("-in and -out are required")
	}
	raw, err := os.ReadFile(*in)
	if err != nil {
		return err
	}
	pcm, err := parseWAV(raw)
	if err != nil {
		return err
	}
	enc, err := audio.NewEncoder(audio.EncoderOptions{
		SampleRate: sampleRate, Channels: 1, Bitrate: 24000, Complexity: 10, Application: audio.AppVoIP,
	})
	if err != nil {
		return err
	}
	f, err := os.Create(*out) //nolint:gosec // the path is the operator's own flag
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	if err := encode(enc, pcm, w); err != nil {
		_ = f.Close()
		return err
	}
	if err := w.Flush(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// encode pads the last frame with silence.
func encode(enc audio.Encoder, pcm []int16, w *bufio.Writer) error {
	n := enc.FrameSamples()
	for off := 0; off < len(pcm); off += n {
		frame := make([]int16, n)
		copy(frame, pcm[off:])
		p, err := enc.Encode(frame)
		if err != nil {
			return err
		}
		if err := opusframes.Write(w, p); err != nil {
			return err
		}
	}
	return nil
}

func parseWAV(b []byte) ([]int16, error) {
	if len(b) < 12 || string(b[0:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		return nil, errors.New("not a wav file")
	}
	fmtSeen := false
	for p := 12; p+8 <= len(b); {
		id, size := string(b[p:p+4]), int(binary.LittleEndian.Uint32(b[p+4:p+8]))
		body := p + 8
		switch id {
		case "fmt ":
			if size < 16 || body+16 > len(b) {
				return nil, errors.New("short fmt chunk")
			}
			format := binary.LittleEndian.Uint16(b[body:])
			ch := binary.LittleEndian.Uint16(b[body+2:])
			rate := binary.LittleEndian.Uint32(b[body+4:])
			bits := binary.LittleEndian.Uint16(b[body+14:])
			if format != 1 || ch != 1 || rate != sampleRate || bits != 16 {
				return nil, errors.New("wav must be 16 kHz mono 16-bit pcm")
			}
			fmtSeen = true
		case "data":
			if !fmtSeen {
				return nil, errors.New("data before fmt")
			}
			end := min(body+size, len(b))
			pcm := make([]int16, (end-body)/2)
			for i := range pcm {
				pcm[i] = int16(binary.LittleEndian.Uint16(b[body+2*i:])) //nolint:gosec // sample bits
			}
			return pcm, nil
		}
		p = body + size + size&1
	}
	return nil, errors.New("no data chunk")
}
