// Package opusframes reads and writes the .opusframes debug format:
// u16 LE packet length followed by the packet.
package opusframes

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
)

var errZeroLength = errors.New("opusframes: zero length packet")

func Read(r io.Reader) ([][]byte, error) {
	var out [][]byte
	var hdr [2]byte
	for {
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			if errors.Is(err, io.EOF) {
				return out, nil
			}
			return nil, fmt.Errorf("opusframes: truncated header: %w", err)
		}
		n := binary.LittleEndian.Uint16(hdr[:])
		if n == 0 {
			return nil, errZeroLength
		}
		p := make([]byte, n)
		if _, err := io.ReadFull(r, p); err != nil {
			return nil, fmt.Errorf("opusframes: truncated packet: %w", err)
		}
		out = append(out, p)
	}
}

func Write(w io.Writer, packet []byte) error {
	if len(packet) == 0 {
		return errZeroLength
	}
	if len(packet) > math.MaxUint16 {
		return errors.New("opusframes: packet too large")
	}
	buf := binary.LittleEndian.AppendUint16(make([]byte, 0, 2+len(packet)), uint16(len(packet))) //nolint:gosec // bounded above
	_, err := w.Write(append(buf, packet...))
	return err
}
