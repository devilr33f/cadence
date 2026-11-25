package vkcapsula

import (
	"strconv"

	"git.shino.boo/devilreef/cadence/device/internal/leds"
)

const ringChannelCount = 36

// perm[3i..3i+2] are the channels of pixel i's r, g, b
var perm = [ringChannelCount]int{1, 0, 2, 34, 33, 35, 31, 30, 32, 28, 27, 29, 25, 24, 26, 22, 21, 23, 19, 18, 20,
	16, 15, 17, 13, 12, 14, 10, 9, 11, 7, 6, 8, 4, 3, 5}

func ringChannels(px []leds.RGB) [ringChannelCount]byte {
	var out [ringChannelCount]byte
	for i, p := range px[:min(len(px), ringLEDs)] {
		out[perm[3*i]], out[perm[3*i+1]], out[perm[3*i+2]] = p.R, p.G, p.B
	}
	return out
}

// ringWriter writes the ring through /dev/topleds when its count answers 36, else through the sysfs leds, only the
// channels that changed. A failure leaves the ring unknown, so the next frame writes every channel.
type ringWriter struct {
	dev   *topleds
	tried bool
	// what the sysfs leds hold; nil until a whole frame reached them
	held *[ringChannelCount]byte
}

func (r *ringWriter) write(root string, ch [ringChannelCount]byte) error {
	if !r.tried {
		r.tried = true
		r.dev = openTopleds(under(root, "/dev/topleds"))
	}
	if r.dev != nil {
		if r.dev.set(&ch) == nil {
			return nil
		}
		r.dev.close()
		r.dev = nil
		r.held = nil
	}
	prev := r.held
	r.held = nil
	for c, v := range ch {
		if prev != nil && prev[c] == v {
			continue
		}
		if !writeLevel(under(root, "/sys/class/leds/top-led"+strconv.Itoa(c+1)+"/brightness"), v) {
			return &leds.SinkError{Reason: "ring_write_failed"}
		}
	}
	r.held = &ch
	return nil
}
