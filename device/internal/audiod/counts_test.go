package audiod_test

import (
	"sync/atomic"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/audiod"
)

type restartCounts struct{ n atomic.Int64 }

func (c *restartCounts) AudiodRestarted() { c.n.Add(1) }

// each start after audiod died counts as a restart; the first start and the stop at shutdown do not
func TestRestartsCounted(t *testing.T) {
	counts := &restartCounts{}
	h := startSupervisorWith(t, 40, audiod.Timings{BackoffMin: 20 * time.Millisecond, BackoffMax: 20 * time.Millisecond},
		[]audiod.SupervisorOption{audiod.WithCounts(counts)}, "--exit-after-ready")
	h.waitLog(t, "audiod_start", 4)
	h.stop()
	if starts, n := h.log.count("audiod_start"), counts.n.Load(); n != int64(starts-1) {
		t.Fatalf("%d restarts counted for %d starts", n, starts)
	}
}
