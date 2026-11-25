package audio_test

import (
	"bytes"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"git.shino.boo/devilreef/cadence/device/internal/audio"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

func drain(q chan audio.Trigger) []audio.Mode {
	var out []audio.Mode
	for {
		select {
		case t := <-q:
			out = append(out, t.Mode)
		default:
			return out
		}
	}
}

// the switch and a ring always find room: the activations before them go, as would a session they end, and an older
// one of their own mode, while the other's stays
func TestTheSwitchAndARingAlwaysFindRoom(t *testing.T) {
	logs := &bytes.Buffer{}
	log := slog.New(slog.NewTextHandler(logs, nil))
	q := make(chan audio.Trigger, 4)
	press := audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_BUTTON, Mode: audio.Toggle}
	for range 5 {
		audio.Enqueue(q, press, log)
	}
	audio.Enqueue(q, audio.Trigger{Mode: audio.Mute}, log)
	audio.Enqueue(q, press, log)
	audio.Enqueue(q, audio.Trigger{Mode: audio.Ring}, log)
	audio.Enqueue(q, audio.Trigger{Mode: audio.Ring}, log)
	if got, want := drain(q), []audio.Mode{audio.Mute, audio.Ring}; !slices.Equal(got, want) {
		t.Fatalf("queued %v, want %v", got, want)
	}
	for reason, n := range map[string]int{"full": 1, "muted": 4, "ringing": 1} {
		if got := strings.Count(logs.String(), "reason="+reason); got != n {
			t.Errorf("%d drops for %s, want %d: %s", got, reason, n, logs)
		}
	}
}
