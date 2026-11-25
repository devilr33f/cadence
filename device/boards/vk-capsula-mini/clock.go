package vkcapsula

import (
	"strconv"

	"git.shino.boo/devilreef/cadence/device/internal/leds"
)

// the clock-led channel of segments a..g of each digit
var digitChannels = [digits][segments]int{
	{1, 28, 21, 17, 22, 29, 24},
	{26, 25, 19, 18, 23, 27, 20},
	{0, 2, 7, 5, 6, 4, 3},
	{10, 12, 11, 14, 13, 8, 9},
}

// both colon halves
var colonChannels = [2]int{16, 15}

const clockChannelCount = 30

// Segments writes each clock led that changed; the degree dot has no led. A failure part way leaves the leds
// unknown, so the next frame writes every one.
func (s *lightSink) Segments(level map[leds.Segment]uint8) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := make(map[int]uint8, clockChannelCount)
	for c := range clockChannelCount {
		next[c] = 0
	}
	for d := range digitChannels {
		for i, c := range digitChannels[d] {
			next[c] = level[leds.Segment{Digit: d, Index: i}]
		}
	}
	for _, c := range colonChannels {
		next[c] = level[leds.Colon]
	}
	prev := s.clock
	s.clock = nil
	for c := range clockChannelCount {
		if v, held := prev[c]; held && v == next[c] {
			continue
		}
		if !writeLevel(under(s.root, "/sys/class/leds/clock-led"+strconv.Itoa(c+1)+"/brightness"), next[c]) {
			return &leds.SinkError{Reason: "clock_write_failed"}
		}
	}
	s.clock = next
	return nil
}
