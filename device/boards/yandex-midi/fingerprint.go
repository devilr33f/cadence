package midi

import (
	"bytes"
	"context"
	"os"
	"strings"

	"git.shino.boo/devilreef/cadence/device/internal/board"
)

const (
	model = "Yandex Station Midi"
	// the ring's framebuffer as sysfs spells it: 89 pixels in one row, 32 bits each, 356 bytes a line
	fbSysfs = "/sys/class/graphics/fb0"
	// the stock library audiod loads; only its presence is checked, never its content (rule 5)
	vendorLibrary = "/system/vendor/quasar/libs/libyandex_evil.so"
)

var fbAttributes = []struct{ name, want string }{{"virtual_size", "89,1"}, {"bits_per_pixel", "32"}, {"stride", "356"}}

// Fingerprint is the cheap check at every start: the device tree's model, the nodes the board drives and the stock
// library. The firmware's own fingerprint (versions) is the installer's.
func (b *Board) Fingerprint(context.Context) error {
	if check := b.mismatch(); check != "" {
		b.log.Error("fingerprint_mismatch", "check", check)
		return board.ErrFingerprint
	}
	return nil
}

// mismatch names the first check the device fails, "" when it is a midi
func (b *Board) mismatch() string {
	raw, err := os.ReadFile(b.at("/proc/device-tree/model"))
	if err != nil || string(bytes.ReplaceAll(raw, []byte{0}, nil)) != model {
		return "model"
	}
	if !b.exists(ringNode) {
		return "framebuffer"
	}
	for _, a := range fbAttributes {
		v, err := os.ReadFile(b.at(fbSysfs + "/" + a.name))
		if err != nil || strings.TrimRight(string(v), "\n") != a.want {
			return "framebuffer_geometry"
		}
	}
	if !b.exists("/dev/snd/controlC0") {
		return "sound"
	}
	if !b.capturePCM() {
		return "capture"
	}
	if !b.exists(vendorLibrary) {
		return "stock_library"
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
