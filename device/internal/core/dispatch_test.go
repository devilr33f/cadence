package core

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"git.shino.boo/devilreef/cadence/device/internal/audio"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

// the switch is never dropped on its way to a busy controller: the activations queued before it go instead
func TestMuteNeverDroppedForTheLink(t *testing.T) {
	logs := &bytes.Buffer{}
	s := &Sync{log: slog.New(slog.NewTextHandler(logs, nil)), triggers: make(chan audio.Trigger, linkTriggers)}
	for range linkTriggers {
		s.dispatch(audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_WAKE, Mode: audio.IfIdle})
	}
	s.dispatch(audio.Trigger{Kind: cadencev2.Trigger_TRIGGER_BUTTON, Mode: audio.Toggle})
	s.dispatch(audio.Trigger{Mode: audio.Mute})
	if n := len(s.triggers); n != 1 {
		t.Fatalf("%d queued", n)
	}
	if tr := <-s.triggers; tr.Mode != audio.Mute {
		t.Fatalf("queued %+v", tr)
	}
	out := logs.String()
	if !strings.Contains(out, "msg=trigger_dropped trigger=TRIGGER_BUTTON reason=full") ||
		strings.Count(out, "msg=trigger_dropped trigger=TRIGGER_WAKE reason=muted") != linkTriggers {
		t.Fatalf("log: %s", out)
	}
}
