package telemetry

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io"
)

var (
	errTooLarge = errors.New("telemetry: body too large")
	errGzip     = errors.New("telemetry: bad gzip")
)

// gunzip decompresses b, at most limit bytes: a longer body is errTooLarge,
// and the buffer never grows past limit+1 bytes, whatever the body holds.
func gunzip(b []byte, limit int) ([]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, errGzip
	}
	buf := make([]byte, 0, min(limit+1, max(4*len(b), 64<<10)))
	for {
		if len(buf) == cap(buf) {
			grown := make([]byte, len(buf), min(limit+1, 2*cap(buf)))
			copy(grown, buf)
			buf = grown
		}
		n, err := zr.Read(buf[len(buf):cap(buf)])
		buf = buf[:len(buf)+n]
		if len(buf) > limit {
			return nil, errTooLarge
		}
		if errors.Is(err, io.EOF) {
			return buf, nil
		}
		if err != nil {
			return nil, errGzip
		}
	}
}
