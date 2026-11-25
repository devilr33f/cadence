package tasks

import (
	"testing"
	"time"
)

// busy mode holds an update while the device is busy, a woken session's end
// included, and its end alone lets it go: no sweep needed
func TestBusyHoldsUpdatesUntilItEnds(t *testing.T) {
	r := newReportRig(t)
	r.busy.Store(true)
	r.start()
	tk := r.task("x")
	r.finish(tk, "r")
	// the event turn still runs: it speaks nothing, and its notice waits
	r.waitTurns(1)
	r.noneSpoken()
	// the user woke the device and spoke, so quiet hours would yield; busy does not
	r.rep.SessionEnded(r.dev.String(), wake, true)
	r.noneSpoken()
	r.rep.sweep(r.ctx)
	r.noneSpoken()
	if n := r.undelivered(); n != 1 {
		t.Fatalf("%d undelivered, want the held one", n)
	}
	r.busy.Store(false)
	r.rep.BusyChanged(r.dev.String())
	r.waitSpoken("NOTICE " + tk.ShortID + " done")
}

// a busy change that leaves the device busy announces nothing, and one for
// another device leaves this one's hold alone
func TestBusyChangeWhileStillBusy(t *testing.T) {
	r := newReportRig(t)
	r.busy.Store(true)
	r.start()
	tk := r.task("x")
	r.finish(tk, "r")
	r.waitTurns(1)
	r.rep.BusyChanged(r.dev.String())
	r.rep.BusyChanged("00000000-0000-0000-0000-000000000009")
	r.noneSpoken()
	// quiet hours still hold once busy ends at night
	r.clock.Set(time.Date(2026, 11, 9, 2, 0, 0, 0, time.UTC))
	r.busy.Store(false)
	r.rep.BusyChanged(r.dev.String())
	r.noneSpoken()
	r.clock.Set(time.Date(2026, 11, 9, 8, 0, 0, 0, time.UTC))
	r.rep.sweep(r.ctx)
	r.waitSpoken("NOTICE " + tk.ShortID + " done")
}
