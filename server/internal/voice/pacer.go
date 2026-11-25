package voice

import (
	"context"
	"time"
)

// burstPackets is the 200 ms the device may run ahead of real time.
const burstPackets = 10

// packet is one encoded frame of the reply; gen is the tts segment that made it.
type packet struct {
	gen  uint64
	opus []byte
}

// pace sends the first burstPackets packets as they come, then one per tick.
// A packet live rejects, its speech discarded, goes without a tick. It
// returns once packets is closed and drained, ctx is done or send fails.
func pace(ctx context.Context, packets <-chan packet, live func(packet) bool, send func([]byte) error, tick <-chan time.Time) {
	for n := 0; ; {
		var p packet
		select {
		case <-ctx.Done():
			return
		case q, ok := <-packets:
			if !ok {
				return
			}
			p = q
		}
		if !live(p) {
			continue
		}
		if n >= burstPackets {
			select {
			case <-ctx.Done():
				return
			case <-tick:
			}
			// discarded while it waited
			if !live(p) {
				continue
			}
		}
		if send(p.opus) != nil {
			return
		}
		if n++; n == burstPackets {
			// the ticker runs from before the burst: a tick that fell due meanwhile
			// would let one more packet through at once
			select {
			case <-tick:
			default:
			}
		}
	}
}
