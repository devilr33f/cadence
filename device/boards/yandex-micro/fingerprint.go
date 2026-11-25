package micro

import (
	"bytes"
	"context"
	"os"
	"strconv"

	"git.shino.boo/devilreef/cadence/device/internal/board"
)

const (
	model         = "Yandex Station Micro v1.2"
	vendorLibrary = "/system/vendor/quasar/libs/libyandex_evil.so"
	captureNode   = "/dev/snd/pcmC0D1c"
)

func (b *Board) Fingerprint(context.Context) error {
	if check := b.mismatch(); check != "" {
		b.log.Error("fingerprint_mismatch", "check", check)
		return board.ErrFingerprint
	}
	return nil
}

func (b *Board) mismatch() string {
	raw, err := os.ReadFile(b.at("/proc/device-tree/model"))
	if err != nil || string(bytes.ReplaceAll(raw, []byte{0}, nil)) != model {
		return "model"
	}
	for _, colour := range []string{"red", "green", "blue"} {
		for i := 1; i <= ringLEDs; i++ {
			if !b.exists("/sys/class/leds/micro:" + colour + ":ring" + strconv.Itoa(i) + "/brightness") {
				return "leds"
			}
		}
	}
	if !b.exists("/dev/snd/controlC0") {
		return "sound"
	}
	if !b.exists(captureNode) {
		return "capture"
	}
	if !b.exists(vendorLibrary) {
		return "stock_library"
	}
	return ""
}

func (b *Board) exists(p string) bool { _, err := os.Stat(b.at(p)); return err == nil }
func (b *Board) at(p string) string   { return under(b.root, p) }
