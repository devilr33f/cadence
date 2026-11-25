// Package sources reads the device's logs into the pipeline: cadenced's own
// records, the kernel log and the files a board names.
package sources

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"

	"git.shino.boo/devilreef/cadence/device/internal/telemetry/logs"
)

// Emitter takes the records a source reads, from any goroutine, never
// blocking; *logs.Pipeline is one.
type Emitter interface{ Emit(r logs.Record) }

// Tee is cadenced's log handler: each record goes to w as the JSON line it
// always did, and, once a pipeline is attached, into it as source cadenced
// with that same line as its body. The pipeline must log to a logger of its
// own, never through the tee, or its trouble would loop back into it.
type Tee struct {
	h  slog.Handler
	sh *teeShared
}

// what a tee and every handler derived from it share
type teeShared struct {
	out  io.Writer
	sink atomic.Pointer[sinkBox]

	// held while a record is formatted and written, so lines keep their order
	mu  sync.Mutex
	buf []byte
}

type sinkBox struct{ e Emitter }

// the json handler formats into the shared buffer, under teeShared.mu
type capture struct{ sh *teeShared }

func (c capture) Write(p []byte) (int, error) {
	c.sh.buf = append(c.sh.buf, p...)
	return len(p), nil
}

// NewTee writes to w with slog's JSON handler and its default options.
func NewTee(w io.Writer) *Tee {
	sh := &teeShared{out: w}
	return &Tee{h: slog.NewJSONHandler(capture{sh}, nil), sh: sh}
}

// Attach makes every later record enter e too.
func (t *Tee) Attach(e Emitter) { t.sh.sink.Store(&sinkBox{e: e}) }

func (t *Tee) Enabled(ctx context.Context, l slog.Level) bool { return t.h.Enabled(ctx, l) }

func (t *Tee) Handle(ctx context.Context, r slog.Record) error {
	sh := t.sh
	sh.mu.Lock()
	defer sh.mu.Unlock()
	sh.buf = sh.buf[:0]
	if err := t.h.Handle(ctx, r); err != nil {
		return err
	}
	if box := sh.sink.Load(); box != nil {
		body, cut := logs.CapLine(string(trimNewline(sh.buf)))
		box.e.Emit(logs.Record{Time: r.Time, Severity: severity(r.Level), Source: "cadenced", Body: body, Truncated: cut})
	}
	_, err := sh.out.Write(sh.buf)
	return err
}

func (t *Tee) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &Tee{h: t.h.WithAttrs(attrs), sh: t.sh}
}

func (t *Tee) WithGroup(name string) slog.Handler { return &Tee{h: t.h.WithGroup(name), sh: t.sh} }

func trimNewline(b []byte) []byte {
	if n := len(b); n > 0 && b[n-1] == '\n' {
		return b[:n-1]
	}
	return b
}

// severity is the level a slog level falls in, a level between two taken as the lower
func severity(l slog.Level) logs.Severity {
	switch {
	case l >= slog.LevelError:
		return logs.Error
	case l >= slog.LevelWarn:
		return logs.Warn
	case l >= slog.LevelInfo:
		return logs.Info
	}
	return logs.Debug
}
