package vkcapsula

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"git.shino.boo/devilreef/cadence/device/internal/board"
)

const (
	compatible = "amlogic, axg"
	soundCard  = "AML-AUGESOUND"
	clockLEDs  = 30
)

func (b *Board) Fingerprint(context.Context) error {
	if check := b.mismatch(); check != "" {
		b.log.Error("fingerprint_mismatch", "check", check)
		return board.ErrFingerprint
	}
	if !b.exists(wakeModel) {
		b.log.Warn("wake_model_missing")
	}
	return nil
}

func (b *Board) mismatch() string {
	raw, err := os.ReadFile(b.at("/proc/device-tree/compatible"))
	if err != nil || !bytes.Contains(bytes.ReplaceAll(raw, []byte{0}, []byte{' '}), []byte(compatible)) {
		return "compatible"
	}
	if !b.exists("/dev/topleds") {
		return "ring"
	}
	for _, n := range []int{1, clockLEDs} {
		if !b.exists("/sys/class/leds/clock-led" + strconv.Itoa(n) + "/brightness") {
			return "clock"
		}
	}
	if !b.hasInput("csk05t") {
		return "buttons"
	}
	cards, err := os.ReadFile(b.at("/proc/asound/cards"))
	if err != nil || !strings.Contains(string(cards), soundCard) {
		return "sound"
	}
	return ""
}

// the input class names a node in its device directory, as evdev's lookup reads it
func (b *Board) hasInput(name string) bool {
	names, _ := filepath.Glob(b.at("/sys/class/input/*/device/name"))
	for _, p := range names {
		raw, err := os.ReadFile(p) //nolint:gosec // sysfs, under the board's root
		if err == nil && strings.TrimSpace(string(raw)) == name {
			return true
		}
	}
	return false
}

func (b *Board) exists(p string) bool { _, err := os.Stat(b.at(p)); return err == nil }
func (b *Board) at(p string) string   { return under(b.root, p) }
