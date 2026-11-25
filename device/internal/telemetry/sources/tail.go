package sources

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"sync/atomic"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/telemetry/logs"
)

const (
	tailEvery = 500 * time.Millisecond
	// a missing file is looked for again this often
	tailRetry = 5 * time.Second
	tailChunk = 32 << 10
	// read from a file at most this at a time
	tailBudget = 1 << 20
)

// Tail reads a board's log file into the pipeline line by line, from its end
// at the start: a restart never reads it again. A rotation (a new file at the
// path, or the file truncated) is read from its start, after what the old one
// still held; a missing file is looked for every 5 s and, once there, read
// from its start. A line past logs.MaxLine is cut there, the rest of it
// skipped.
type Tail struct {
	source, path string
	emit         Emitter
	every, retry time.Duration

	f        *os.File
	info     os.FileInfo
	partial  []byte
	skipping bool
	// what tests watch
	opened atomic.Bool
	offset atomic.Int64
}

func NewTail(source, path string, emit Emitter) *Tail {
	return &Tail{source: source, path: path, emit: emit, every: tailEvery, retry: tailRetry}
}

// Run tails the file until ctx ends.
func (t *Tail) Run(ctx context.Context) {
	defer t.close()
	t.open(true)
	buf := make([]byte, tailChunk)
	for {
		wait := t.every
		if t.f == nil {
			wait = t.retry
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		if t.f == nil {
			t.open(false)
			continue
		}
		t.read(buf)
		t.follow(buf)
	}
}

// open opens the file at the path, at its end when atEnd, else at its start
func (t *Tail) open(atEnd bool) {
	f, err := os.Open(t.path)
	if err != nil {
		return
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return
	}
	var off int64
	if atEnd {
		if off, err = f.Seek(0, io.SeekEnd); err != nil {
			_ = f.Close()
			return
		}
	}
	t.f, t.info = f, info
	t.offset.Store(off)
	t.opened.Store(true)
}

func (t *Tail) close() {
	if t.f != nil {
		_ = t.f.Close()
		t.f = nil
		t.opened.Store(false)
	}
}

// read takes what the file holds past the offset, at most tailBudget at a
// time, so a file that grows faster than it is read never holds the tail
func (t *Tail) read(buf []byte) {
	for taken := 0; taken < tailBudget; {
		n, err := t.f.Read(buf)
		if n > 0 {
			taken += n
			t.offset.Add(int64(n))
			t.lines(buf[:n])
		}
		if err != nil || n == 0 {
			return
		}
	}
}

// follow looks whether the path still names the file read, at its length:
// another file there or a shorter one is a rotation, read from its start. A
// file moved away is read to its end first, for what came since the read.
func (t *Tail) follow(buf []byte) {
	now, err := os.Stat(t.path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		t.read(buf)
		t.flush()
		t.close()
	case err != nil:
	case !os.SameFile(t.info, now):
		t.read(buf)
		t.flush()
		t.close()
		t.open(false)
	case now.Size() < t.offset.Load():
		t.flush()
		if _, err := t.f.Seek(0, io.SeekStart); err != nil {
			t.close()
			return
		}
		t.offset.Store(0)
	}
}

func (t *Tail) lines(b []byte) {
	for len(b) > 0 {
		i := bytes.IndexByte(b, '\n')
		chunk := b
		if i >= 0 {
			chunk = b[:i]
		}
		if !t.skipping {
			t.partial = append(t.partial, chunk[:min(len(chunk), logs.MaxLine+1-len(t.partial))]...)
			if len(t.partial) > logs.MaxLine {
				t.send(t.partial[:logs.MaxLine], true)
				t.partial, t.skipping = t.partial[:0], true
			}
		}
		if i < 0 {
			return
		}
		if !t.skipping {
			t.send(t.partial, false)
		}
		t.partial, t.skipping = t.partial[:0], false
		b = b[i+1:]
	}
}

// flush sends the line the old file never ended, as cut: its end may be a secret's start
func (t *Tail) flush() {
	if !t.skipping {
		t.send(t.partial, true)
	}
	t.partial, t.skipping = t.partial[:0], false
}

func (t *Tail) send(line []byte, cut bool) {
	line = bytes.TrimSuffix(line, []byte("\r"))
	if len(line) == 0 {
		return
	}
	t.emit.Emit(logs.Record{Severity: logs.Info, Source: t.source, Body: string(line), Truncated: cut})
}
