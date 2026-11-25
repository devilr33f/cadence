package micro

import (
	"os"
	"strconv"
	"sync"

	"git.shino.boo/devilreef/cadence/device/internal/leds"
)

const ringLEDs = 3

// the Lite's real LED interface is one Linux LED-class brightness file per
// colour and ring position; its fb0 is a dummy and never drives the hardware.
type lightSink struct {
	root  string
	mu    sync.Mutex
	shown map[string]uint8
}

func (*lightSink) Geometry() leds.Geometry { return leds.Geometry{Ring: ringLEDs} }
func (s *lightSink) Ring(px []leds.RGB) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.shown == nil {
		s.shown = map[string]uint8{}
	}
	for i := 0; i < ringLEDs; i++ {
		var p leds.RGB
		if i < len(px) {
			p = px[i]
		}
		for _, c := range []struct {
			name  string
			value uint8
		}{{"red", p.R}, {"green", p.G}, {"blue", p.B}} {
			path := under(s.root, "/sys/class/leds/micro:"+c.name+":ring"+strconv.Itoa(i+1)+"/brightness")
			if value, ok := s.shown[path]; ok && value == c.value {
				continue
			}
			if err := os.WriteFile(path, []byte(strconv.Itoa(int(c.value))+"\n"), 0); err != nil {
				return &leds.SinkError{Reason: "led_write_failed"}
			}
			s.shown[path] = c.value
		}
	}
	return nil
}
func (*lightSink) Button([]leds.RGB) error               { return nil }
func (*lightSink) Segments(map[leds.Segment]uint8) error { return nil }
func (s *lightSink) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.shown = nil
}
