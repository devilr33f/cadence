package pickle

import (
	"os"
	"strconv"

	"git.shino.boo/devilreef/cadence/device/internal/leds"
)

const (
	clockLEDs = "/sys/class/leds/pickle:white:clock"
	// segment a..g, then the decimal point, of each digit: clock<letter><digit>
	segmentLetters = "ABCDEFGH"
)

// Segments writes each clock led that changed: one sysfs led per segment, the colon's two halves together, the degree
// dot, and the middle dot, which is never used and only needs to be off. A segment the map lacks (the decimal points)
// is dark. A failure part way leaves the leds unknown, so the next frame writes every one.
func (s *lightSink) Segments(level map[leds.Segment]uint8) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := map[string]uint8{"DM": 0, "DEGREE": level[leds.Degree], "DH": level[leds.Colon], "DL": level[leds.Colon]}
	for d := range digits {
		for i := range segments {
			next[string(segmentLetters[i])+strconv.Itoa(d)] = level[leds.Segment{Digit: d, Index: i}]
		}
	}
	prev := s.clock
	s.clock = nil
	for _, led := range clockOrder() {
		if v, held := prev[led]; held && v == next[led] {
			continue
		}
		if !writeLevel(under(s.root, clockLEDs+led+"/brightness"), next[led]) {
			return &leds.SinkError{Reason: "clock_write_failed"}
		}
	}
	s.clock = next
	return nil
}

// clockOrder is v1's: the middle dot, the degree dot, the digits, then the colon
func clockOrder() []string {
	order := []string{"DM", "DEGREE"}
	for d := range digits {
		for i := range segments {
			order = append(order, string(segmentLetters[i])+strconv.Itoa(d))
		}
	}
	return append(order, "DH", "DL")
}

func writeLevel(file string, v uint8) bool {
	f, err := os.OpenFile(file, os.O_WRONLY|os.O_TRUNC, 0) //nolint:gosec // sysfs, under the board's root
	if err != nil {
		return false
	}
	_, err = f.WriteString(strconv.Itoa(int(v)) + "\n")
	return f.Close() == nil && err == nil
}
