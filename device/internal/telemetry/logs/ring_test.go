package logs_test

import (
	"bytes"
	"fmt"
	"testing"

	"git.shino.boo/devilreef/cadence/device/internal/telemetry/logs"
)

func rec(i, size int) []byte {
	b := bytes.Repeat([]byte{'.'}, size)
	copy(b, fmt.Sprintf("%04d", i))
	return b
}

func index(t *testing.T, b []byte) int {
	t.Helper()
	var i int
	if _, err := fmt.Sscanf(string(b[:4]), "%04d", &i); err != nil {
		t.Fatal(err)
	}
	return i
}

func TestRingDropsOldestFirstAndCounts(t *testing.T) {
	r := logs.NewRing(100)
	for i := range 10 {
		r.Push(rec(i, 10))
	}
	if r.Bytes() != 100 || r.Len() != 10 || r.Dropped() != 0 {
		t.Fatalf("full ring: %d bytes, %d records, %d dropped", r.Bytes(), r.Len(), r.Dropped())
	}
	// 25 bytes more drop the three oldest
	if held := r.Push(rec(10, 25)); held != 95 {
		t.Fatalf("held = %d", held)
	}
	if r.Dropped() != 3 || r.Len() != 8 {
		t.Fatalf("dropped = %d, len = %d", r.Dropped(), r.Len())
	}
	if got := r.Peek(1000); index(t, got[0]) != 3 || index(t, got[len(got)-1]) != 10 {
		t.Fatalf("oldest = %d, newest = %d", index(t, got[0]), index(t, got[len(got)-1]))
	}
	// a record larger than the whole ring is dropped itself, and counted
	r.Push(rec(11, 101))
	if r.Dropped() != 4 || r.Len() != 8 {
		t.Fatalf("dropped = %d, len = %d", r.Dropped(), r.Len())
	}
}

func TestRingPeekAck(t *testing.T) {
	r := logs.NewRing(1000)
	for i := range 10 {
		r.Push(rec(i, 10))
	}
	got := r.Peek(35)
	if len(got) != 3 || index(t, got[0]) != 0 || r.Len() != 10 {
		t.Fatalf("peek = %d records, len = %d", len(got), r.Len())
	}
	// a peek with room for less than one record still returns one
	if got := r.Peek(1); len(got) != 1 {
		t.Fatalf("peek = %d records", len(got))
	}
	got = r.Peek(30)
	r.Ack(len(got))
	if r.Len() != 7 || r.Bytes() != 70 {
		t.Fatalf("after ack: %d records, %d bytes", r.Len(), r.Bytes())
	}
	if got := r.Peek(10); index(t, got[0]) != 3 {
		t.Fatalf("oldest after ack = %d", index(t, got[0]))
	}
	if got := logs.NewRing(10).Peek(10); len(got) != 0 {
		t.Fatalf("empty peek = %d", len(got))
	}
}

// records dropped from the ring while their batch is in flight were sent
// with it: the ack removes only what is left of the peeked ones, and counts
// none of them dropped
func TestRingAckAfterDrop(t *testing.T) {
	r := logs.NewRing(50)
	for i := range 5 {
		r.Push(rec(i, 10))
	}
	sent := r.Peek(30) // 0, 1, 2
	r.Push(rec(5, 10)) // drops 0
	r.Push(rec(6, 10)) // drops 1
	if r.Dropped() != 0 {
		t.Fatalf("dropped while in flight = %d", r.Dropped())
	}
	r.Ack(len(sent)) // removes 2 only
	got := r.Peek(1000)
	if len(got) != 4 || index(t, got[0]) != 3 {
		t.Fatalf("left = %d records from %d", len(got), index(t, got[0]))
	}
	if r.Dropped() != 0 {
		t.Fatalf("dropped = %d", r.Dropped())
	}
}

// a batch never acked loses what was dropped of it, counted at the next peek;
// drops past the batch count at once; an ack of part of a batch counts the
// drops past that part
func TestRingDropsOfUnsentRecordsCount(t *testing.T) {
	r := logs.NewRing(50)
	for i := range 5 {
		r.Push(rec(i, 10))
	}
	r.Peek(20)         // 0, 1: the send fails
	r.Push(rec(5, 10)) // drops 0
	r.Push(rec(6, 10)) // drops 1
	r.Push(rec(7, 10)) // drops 2, past the batch
	if r.Dropped() != 1 {
		t.Fatalf("dropped before the retry = %d", r.Dropped())
	}
	got := r.Peek(30) // the retry: 3, 4, 5
	if r.Dropped() != 3 || index(t, got[0]) != 3 {
		t.Fatalf("dropped at the retry = %d, oldest %d", r.Dropped(), index(t, got[0]))
	}
	r.Push(rec(8, 10)) // drops 3
	r.Push(rec(9, 10)) // drops 4
	r.Ack(1)           // only 3 went out: 4 is lost
	if r.Dropped() != 4 {
		t.Fatalf("dropped after a part ack = %d", r.Dropped())
	}
	if got := r.Peek(1000); len(got) != 5 || index(t, got[0]) != 5 {
		t.Fatalf("left = %d records from %d", len(got), index(t, got[0]))
	}
}

// pushing compacts the backing array instead of growing it without end
func TestRingLongRun(t *testing.T) {
	r := logs.NewRing(1 << 10)
	for i := range 200000 {
		r.Push(rec(i%10000, 8))
		if i%3 == 0 {
			r.Ack(len(r.Peek(16)))
		}
	}
	if r.Bytes() > 1<<10 || r.Bytes() != 8*r.Len() {
		t.Fatalf("%d bytes for %d records", r.Bytes(), r.Len())
	}
	if c := logs.RingCapacity(r); c > 4096 {
		t.Fatalf("backing array holds %d records", c)
	}
}
