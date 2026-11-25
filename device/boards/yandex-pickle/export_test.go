package pickle

import (
	"os"

	"git.shino.boo/devilreef/cadence/device/internal/input"
	"git.shino.boo/devilreef/cadence/device/internal/soloist"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
)

var (
	Configure  = configure
	PathsUnder = pathsUnder
)

// WithRoot runs the board on a fake device tree.
func WithRoot(root string) Option { return func(b *Board) { b.root = root } }

// WithSwitch reads the mute switch with read instead of the evdev ioctl, which a test's files do not answer.
func WithSwitch(read func(*os.File) (on, ok bool)) Option {
	return func(b *Board) { b.readSwitch = read }
}

// WithoutSoloistUpdates never fetches a build from spotify's cdn.
func WithoutSoloistUpdates() Option {
	return func(b *Board) { b.updates = func(Paths) *soloist.Updates { return nil } }
}

// ButtonEvents are what the board's evdev readers report to its input.
func ButtonEvents(b *Board) <-chan input.ButtonEvent {
	b.setup()
	return b.buttons.Events()
}

// ConfigureOf is the Configure the board starts audiod with.
func ConfigureOf(b *Board) *audiodv1.Configure { return b.configure() }
