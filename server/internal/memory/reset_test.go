package memory_test

import (
	"context"
	"strings"
	"testing"
)

func TestResetCompactsOnlyTheClosedSegmentThenStartsFresh(t *testing.T) {
	r := newCompactRig(t)
	at := r.clock.Now() // reset and compact-now intentionally include fresh turns
	r.turn(r.d1, "before", at)
	boundary, err := r.convs.Reset(context.Background(), r.d1)
	if err != nil {
		t.Fatal(err)
	}
	// committed after the boundary: it must not enter the closed segment even
	// though the asynchronous job starts only now
	r.turn(r.d1, "after", at)
	if !r.c.CompactClosed(r.d1, boundary) {
		t.Fatal("the closed segment did not start")
	}
	r.c.Wait()
	reqs := r.llm.requests()
	if len(reqs) != 1 {
		t.Fatalf("%d requests", len(reqs))
	}
	in := input(reqs[0])
	if !strings.Contains(in, "User: before asked") || strings.Contains(in, "after asked") {
		t.Fatalf("closed segment input:\n%s", in)
	}
	if len(r.hooked) != 1 || len(r.hooked[0].Segment) != 2 {
		t.Fatalf("learning hook %+v", r.hooked)
	}
	if start, err := r.c.Start(context.Background(), r.d1); err != nil || start != boundary {
		t.Fatalf("start %d, %v", start, err)
	}

	r.llm.set(`{"summary":"Only the new conversation.","openThreads":[],"taskRefs":[]}`)
	if !r.c.RequestCompaction(r.d1) {
		t.Fatal("compact now did not start")
	}
	r.c.Wait()
	reqs = r.llm.requests()
	if len(reqs) != 1 {
		t.Fatalf("%d requests after compact now", len(reqs))
	}
	in = input(reqs[0])
	if !strings.Contains(in, "Previous summary: none") || !strings.Contains(in, "User: after asked") || strings.Contains(in, "before asked") {
		t.Fatalf("the new segment inherited the closed conversation:\n%s", in)
	}
	cs := r.compactions(r.d1)
	if len(cs) != 2 || cs[0].Summary != "Only the new conversation." {
		t.Fatalf("compactions %+v", cs)
	}
}
