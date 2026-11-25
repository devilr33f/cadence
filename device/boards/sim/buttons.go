package sim

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/input"
)

var pressKinds = map[string]input.Kind{"action": input.Action, "volume_up": input.VolumeUp, "volume_down": input.VolumeDown}

// buttons are the sim's own: <data>/press names one, which acts once, as a press of a real board's does on the same
// path (a session, a volume step, or a ring stopped without a step). A press of action cancels the session that runs,
// or begins a button session with no fixture, whose microphone fails at once (mic_failed): use the say file to speak;
// action serves to stop a ring or to cancel. Real boards never read the file.
type buttons struct {
	path   string
	log    *slog.Logger
	events chan input.ButtonEvent
}

func newButtons(dataDir string, log *slog.Logger) *buttons {
	return &buttons{path: filepath.Join(dataDir, "press"), log: log, events: make(chan input.ButtonEvent, triggerBuf)}
}

func (b *buttons) Events() <-chan input.ButtonEvent { return b.events }

func (b *buttons) poll(ctx context.Context) { pollEach(ctx, b.readPress) }

// readPress presses the button the press file names and removes the file. An empty file may be half written, so it
// stays for the next poll; a word that names no button is dropped with it.
func (b *buttons) readPress() {
	raw, err := os.ReadFile(b.path) //nolint:gosec // under the operator's data dir
	if err != nil {
		return
	}
	word := strings.TrimSpace(string(raw))
	if word == "" {
		return
	}
	if err := os.Remove(b.path); err != nil {
		return
	}
	kind, ok := pressKinds[word]
	if !ok {
		b.log.Warn("sim_press_unknown")
		return
	}
	select {
	case b.events <- input.ButtonEvent{Kind: kind, Pressed: true, At: time.Now()}:
	default:
		b.log.Warn("sim_press_dropped")
	}
}
