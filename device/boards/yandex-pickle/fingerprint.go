package pickle

import (
	"bytes"
	"context"
	"os"
	"strings"

	"git.shino.boo/devilreef/cadence/device/internal/board"
)

const (
	modelPrefix = "Yandex Pickle"
	lightSensor = "ltr308als01"
)

// Fingerprint is the cheap check at every start: the device tree's model and the nodes the board drives. The
// firmware's own fingerprint (versions, stock file hashes) is the installer's, as hashing vendor files at each start
// is too slow.
func (b *Board) Fingerprint(context.Context) error {
	if check := b.mismatch(); check != "" {
		b.log.Error("fingerprint_mismatch", "check", check)
		return board.ErrFingerprint
	}
	return nil
}

// mismatch names the first check the device fails, "" when it is a pickle
func (b *Board) mismatch() string {
	model, err := os.ReadFile(b.at("/proc/device-tree/model"))
	if err != nil || !bytes.HasPrefix(bytes.ReplaceAll(model, []byte{0}, nil), []byte(modelPrefix)) {
		return "model"
	}
	if !b.exists("/dev/fb0") || !b.exists("/dev/fb1") {
		return "framebuffer"
	}
	if !b.exists("/dev/snd/controlC0") {
		return "sound"
	}
	if !b.capturePCM() {
		return "capture"
	}
	if _, ok := findIIO(b.root, lightSensor); !ok {
		return "light_sensor"
	}
	return ""
}

func (b *Board) exists(p string) bool {
	_, err := os.Stat(b.at(p))
	return err == nil
}

// capturePCM is any capture device of card 0
func (b *Board) capturePCM() bool {
	entries, err := os.ReadDir(b.at("/dev/snd"))
	if err != nil {
		return false
	}
	for _, e := range entries {
		if n := e.Name(); strings.HasPrefix(n, "pcmC0D") && strings.HasSuffix(n, "c") {
			return true
		}
	}
	return false
}

// at is the device's path p under the board's root
func (b *Board) at(p string) string { return under(b.root, p) }
