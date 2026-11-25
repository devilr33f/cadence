package vkcapsula

import (
	"git.shino.boo/devilreef/cadence/device/internal/evdev"
	"git.shino.boo/devilreef/cadence/device/internal/input"
	"git.shino.boo/devilreef/cadence/device/internal/leds"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
)

// WithRoot runs the board on a fake device tree.
func WithRoot(root string) Option { return func(b *Board) { b.root = root } }

// ButtonEvents are what the board's evdev readers report to its input.
func ButtonEvents(b *Board) <-chan input.ButtonEvent {
	b.setup()
	return b.buttons.Events()
}

// ConfigureOf is the Configure the board starts audiod with.
func ConfigureOf(b *Board) *audiodv1.Configure { return b.configure() }

// ButtonLayout is the board's buttons as a fresh boot finds them.
func ButtonLayout(root string) evdev.Layout { return buttons(root, false, nil) }

// RingChannels is the 36 channel bytes of a ring frame.
func RingChannels(px []leds.RGB) [36]byte { return ringChannels(px) }
