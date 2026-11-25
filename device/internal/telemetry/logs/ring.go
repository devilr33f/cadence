package logs

import (
	"slices"
	"sync"
)

// below this many removed records the backing array is not worth moving
const compactAfter = 1024

// Ring holds encoded records, oldest first, up to a total of maxBytes: a push
// into a full ring drops the oldest records first and counts them. It has one
// reader, which peeks a batch and acks what it sent. Its one lock is never
// held across anything slow.
//
// A record dropped while its batch is in flight counts once that batch is
// settled: not at all when it is acked (the server has it, or refused it,
// which the reader counts), as dropped when the next peek finds it unacked.
// So dropped never counts a record twice and never goes down.
type Ring struct {
	mu   sync.Mutex
	max  int
	recs [][]byte
	head int
	held int
	// first is the sequence number of recs[head]; the last Peek returned
	// peekN records from peeked, and pending of them were dropped since
	first, peeked uint64
	peekN         int
	pending       int
	dropped       uint64
}

func NewRing(maxBytes int) *Ring { return &Ring{max: maxBytes} }

// Push adds rec and returns the bytes held after it. A record larger than
// the whole ring is dropped itself.
func (r *Ring) Push(rec []byte) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(rec) > r.max {
		r.dropped++
		return r.held
	}
	for r.held+len(rec) > r.max {
		// drops go oldest first: those of the batch in flight are its first ones
		if r.first < r.peeked+uint64(r.peekN) { //nolint:gosec // a count of records
			r.pending++
		} else {
			r.dropped++
		}
		r.removeHead()
	}
	r.recs = append(r.recs, rec)
	r.held += len(rec)
	return r.held
}

// Peek returns the oldest records up to maxBytes in all, at least one when
// the ring holds any, and leaves them in place. The records are never changed.
func (r *Ring) Peek(maxBytes int) [][]byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	// the last batch was never acked: what of it was dropped is lost
	r.dropped += uint64(r.pending) //nolint:gosec // a count of records
	r.pending = 0
	n, size := 0, 0
	for _, rec := range r.recs[r.head:] {
		if n > 0 && size+len(rec) > maxBytes {
			break
		}
		n++
		size += len(rec)
	}
	r.peeked, r.peekN = r.first, n
	return slices.Clone(r.recs[r.head : r.head+n])
}

// Ack removes the first n records of the last Peek, those of them still held:
// what was dropped meanwhile is not removed twice, and counts as dropped only
// past the n.
func (r *Ring) Ack(n int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dropped += uint64(max(0, r.pending-n)) //nolint:gosec // a count of records
	r.pending, r.peekN = 0, 0
	end := r.peeked + uint64(n) //nolint:gosec // n counts records of one peek
	for r.first < end && r.head < len(r.recs) {
		r.removeHead()
	}
}

func (r *Ring) Bytes() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.held
}

func (r *Ring) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.recs) - r.head
}

func (r *Ring) Dropped() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.dropped
}

// drop counts a record that never reached the ring.
func (r *Ring) drop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dropped++
}

// removeHead runs under mu, with a record held.
func (r *Ring) removeHead() {
	r.held -= len(r.recs[r.head])
	r.recs[r.head] = nil
	r.head++
	r.first++
	switch {
	case r.head == len(r.recs):
		r.recs, r.head = r.recs[:0], 0
	case r.head >= compactAfter && 2*r.head >= len(r.recs):
		n := copy(r.recs, r.recs[r.head:])
		clear(r.recs[n:])
		r.recs, r.head = r.recs[:n], 0
	}
}
