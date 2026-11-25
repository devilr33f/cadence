package evdev

import (
	"context"
	"os"

	"git.shino.boo/devilreef/cadence/device/internal/atomicfile"
)

// LoadToggle reads the saved toggle position; a missing or unreadable file, or any other content, is unmuted.
func LoadToggle(path string) bool {
	raw, err := os.ReadFile(path) //nolint:gosec // the board's own state file
	return err == nil && string(raw) == "muted\n"
}

// SaveToggle writes the toggle position so a reboot keeps it.
func SaveToggle(path string, on bool) error {
	s := "unmuted\n"
	if on {
		s = "muted\n"
	}
	return atomicfile.Write(path, []byte(s))
}

// toggle flips the mute state for a press, sends the new position, then reports it for persistence; both run on
// the reader's goroutine, so the saved order is the press order
func (b *Buttons) toggle(ctx context.Context) bool {
	b.toggleMu.Lock()
	defer b.toggleMu.Unlock()
	b.toggleOn = !b.toggleOn
	ok := b.send(ctx, switchEvent(b.toggleOn))
	if b.layout.MuteToggled != nil {
		b.layout.MuteToggled(b.toggleOn)
	}
	return ok
}
