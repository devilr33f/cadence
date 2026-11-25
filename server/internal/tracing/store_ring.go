package tracing

import "time"

// finishLocked marks a trace finished and evicts beyond the ring and budget.
func (s *Store) finishLocked(t *traceState) {
	if t.done {
		return
	}
	t.done = true
	s.completed = append(s.completed, t.id)
}

// trimLocked evicts finished traces, oldest first, until the ring holds at
// most ringSize and the store at most its budget. A trace still running is
// never finished, so never evicted: when only those are left the budget is
// allowed to run over, holding too much being the cheaper mistake.
func (s *Store) trimLocked() {
	n := 0
	for n < len(s.completed) && (len(s.completed)-n > s.ringSize || s.bytes > s.budget) {
		s.evictLocked(s.completed[n])
		n++
	}
	if n > 0 {
		s.completed = append(s.completed[:0], s.completed[n:]...)
	}
}

func (s *Store) evictLocked(id string) {
	t := s.traces[id]
	if t == nil {
		return
	}
	for _, st := range t.spans {
		if st.view.Live {
			delete(s.live, st.id)
		}
	}
	s.bytes -= t.bytes
	delete(s.traces, id)
}

// removeCompletedLocked takes id out of the finished order without evicting
// it: the trace came back to life.
func (s *Store) removeCompletedLocked(id string) {
	for i, c := range s.completed {
		if c == id {
			s.completed = append(s.completed[:i], s.completed[i+1:]...)
			return
		}
	}
}

// sweepLocked force-ends spans live longer than leakAfter, at most once a
// sweepEvery, on whichever store call comes next: no goroutine to stop. It
// returns the events for the caller to publish once the lock is released.
func (s *Store) sweepLocked(now time.Time) []SpanEvent {
	if now.Sub(s.lastSweep) < sweepEvery {
		return nil
	}
	s.lastSweep = now
	var evs []SpanEvent
	for id, st := range s.live {
		if now.Sub(st.view.Start) <= leakAfter {
			continue
		}
		delete(s.live, id)
		t := s.traces[st.trace]
		end := now
		st.view.Live, st.view.End = false, &end
		st.view.DurationMs = ms(now.Sub(st.view.Start))
		st.view.Status, st.view.StatusCode = StatusError, "leaked"
		if t == nil {
			continue
		}
		t.live--
		t.hasError = true
		evs = append(evs, s.touchLocked(t, st, EventSpanEnd, now))
		if t.live == 0 {
			s.finishLocked(t)
		}
	}
	s.trimLocked()
	return evs
}

func ms(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }
