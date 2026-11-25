package sources

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"sync"
	"syscall"

	"git.shino.boo/devilreef/cadence/device/internal/telemetry/logs"
)

// the kernel writes a record of at most this, its dictionary lines included
const kmsgRecord = 8192

// KmsgFile is the kernel log as its reader takes it: each Read one record,
// Seek(0, io.SeekEnd) past every record there is, and Close ending a Read
// that waits.
type KmsgFile interface {
	io.ReadCloser
	io.Seeker
}

// OpenKmsg opens the kernel log's device at path, which the board names. A
// file the runtime polls is what lets Close end a waiting Read.
func OpenKmsg(path string) (KmsgFile, error) { return os.Open(path) } //nolint:gosec // the board's device

// Kmsg reads the kernel log into the pipeline as source kmsg, from its end on.
type Kmsg struct {
	open func() (KmsgFile, error)
	emit Emitter
}

func NewKmsg(open func() (KmsgFile, error), emit Emitter) *Kmsg {
	return &Kmsg{open: open, emit: emit}
}

// Run reads until ctx ends (nil) or the log fails: it could not be opened, or
// a read failed with anything but an overrun.
func (k *Kmsg) Run(ctx context.Context) error {
	f, err := k.open()
	if err != nil {
		return err
	}
	// the boot buffer was read at boot, or by the last cadenced: a restart reads on from now
	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		_ = f.Close()
		return err
	}
	var once sync.Once
	closeFile := func() { once.Do(func() { _ = f.Close() }) }
	defer closeFile()
	stop := context.AfterFunc(ctx, closeFile)
	defer stop()

	buf := make([]byte, kmsgRecord)
	var last uint64
	seen := false
	for {
		n, err := f.Read(buf)
		switch {
		case ctx.Err() != nil:
			return nil
		case errors.Is(err, syscall.EPIPE):
			// overwritten before it was read: the next read gets the oldest one left, and its sequence says how many went
			continue
		case err != nil:
			return err
		}
		rec, seq, ok := parseKmsg(buf[:n])
		if !ok {
			continue
		}
		if seen && seq > last+1 {
			k.emit.Emit(logs.Record{Severity: logs.Warn, Source: "kmsg", Body: fmt.Sprintf("dropped %d lines", seq-last-1)})
		}
		last, seen = seq, true
		k.emit.Emit(rec)
	}
}

// ParseKmsg is one record of the kernel log, "<priority>,<sequence>,<usec>,<flags>[,...];<message>", then its
// dictionary lines, each starting with a space, appended. The priority's low three bits are its level.
func ParseKmsg(raw []byte) (logs.Record, bool) {
	rec, _, ok := parseKmsg(raw)
	return rec, ok
}

func parseKmsg(raw []byte) (logs.Record, uint64, bool) {
	prefix, rest, ok := bytes.Cut(raw, []byte(";"))
	if !ok {
		return logs.Record{}, 0, false
	}
	fields := bytes.Split(prefix, []byte(","))
	if len(fields) < 3 {
		return logs.Record{}, 0, false
	}
	pri, err := strconv.ParseUint(string(fields[0]), 10, 32)
	if err != nil {
		return logs.Record{}, 0, false
	}
	seq, err := strconv.ParseUint(string(fields[1]), 10, 64)
	if err != nil {
		return logs.Record{}, 0, false
	}
	lines := bytes.Split(bytes.TrimSuffix(rest, []byte("\n")), []byte("\n"))
	body := bytes.Clone(lines[0])
	for _, l := range lines[1:] {
		if len(l) > 0 && l[0] == ' ' {
			body = append(body, l...)
		}
	}
	text, cut := logs.CapLine(string(body))
	return logs.Record{Severity: kernelSeverity(pri & 7), Source: "kmsg", Body: text, Truncated: cut}, seq, true
}

// syslog's levels: emergency to error, warning, notice and info, debug
func kernelSeverity(level uint64) logs.Severity {
	switch {
	case level <= 3:
		return logs.Error
	case level == 4:
		return logs.Warn
	case level <= 6:
		return logs.Info
	}
	return logs.Debug
}
