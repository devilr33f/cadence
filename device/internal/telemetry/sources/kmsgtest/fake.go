// Package kmsgtest is a fake kernel log for tests: each Read returns one
// record, or the error queued in its place, as /dev/kmsg does.
package kmsgtest

import (
	"io"
	"os"
	"sync"
)

// File is one open of the fake kernel log. Records queued before the reader
// seeks to the end are the boot buffer it skips.
type File struct {
	mu      sync.Mutex
	changed chan struct{}
	queue   []item
	closed  bool
	seeks   int
}

type item struct {
	rec []byte
	err error
}

func New() *File { return &File{changed: make(chan struct{})} }

// Add queues a record, without its newline.
func (f *File) Add(rec string) { f.push(item{rec: []byte(rec + "\n")}) }

// Fail queues an error a Read returns in a record's place.
func (f *File) Fail(err error) { f.push(item{err: err}) }

func (f *File) push(it item) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queue = append(f.queue, it)
	f.notify()
}

// notify runs under mu
func (f *File) notify() {
	close(f.changed)
	f.changed = make(chan struct{})
}

// Read blocks until a record is queued or the file is closed.
func (f *File) Read(p []byte) (int, error) {
	for {
		f.mu.Lock()
		if f.closed {
			f.mu.Unlock()
			return 0, os.ErrClosed
		}
		if len(f.queue) > 0 {
			it := f.queue[0]
			f.queue = f.queue[1:]
			f.mu.Unlock()
			if it.err != nil {
				return 0, it.err
			}
			return copy(p, it.rec), nil
		}
		changed := f.changed
		f.mu.Unlock()
		<-changed
	}
}

// Seek to the end skips every record queued so far.
func (f *File) Seek(offset int64, whence int) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if offset != 0 || whence != io.SeekEnd {
		return 0, os.ErrInvalid
	}
	f.queue = nil
	f.seeks++
	return 0, nil
}

func (f *File) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.closed {
		f.closed = true
		f.notify()
	}
	return nil
}

// Closed reports whether the reader let the file go.
func (f *File) Closed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

// Seeks is how often the reader went to the end.
func (f *File) Seeks() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.seeks
}

// Pending is how many records and errors wait to be read.
func (f *File) Pending() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.queue)
}
