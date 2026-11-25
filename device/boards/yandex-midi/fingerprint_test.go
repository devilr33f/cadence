package midi_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	midi "git.shino.boo/devilreef/cadence/device/boards/yandex-midi"
	"git.shino.boo/devilreef/cadence/device/internal/board"
)

func fingerprint(t *testing.T, tr *tree) (string, error) {
	t.Helper()
	log, buf := newLog()
	err := midi.New(tr.path("data/cadence"), log, midi.WithRoot(tr.root)).Fingerprint(context.Background())
	return buf.String(), err
}

func TestFingerprintAcceptsAMidi(t *testing.T) {
	if logs, err := fingerprint(t, midiTree(t)); err != nil || logs != "" {
		t.Fatalf("fingerprint = %v, logs %q", err, logs)
	}
	// sysfs ends its values with a newline, which counts for nothing
	tr := midiTree(t)
	tr.write("sys/class/graphics/fb0/virtual_size", "89,1")
	tr.write("sys/class/graphics/fb0/stride", "356")
	if _, err := fingerprint(t, tr); err != nil {
		t.Fatalf("values without a newline: %v", err)
	}
}

func TestFingerprintRefusesAnotherDevice(t *testing.T) {
	for _, c := range []struct {
		name  string
		check string
		edit  func(*tree)
	}{
		{"a pickle", "model", func(tr *tree) { tr.write("proc/device-tree/model", "Yandex Pickle rev 3\x00") }},
		{"a longer model", "model", func(tr *tree) { tr.write("proc/device-tree/model", "Yandex Station Midi 2\x00") }},
		{"the model further in", "model", func(tr *tree) { tr.write("proc/device-tree/model", "New Yandex Station Midi\x00") }},
		{"a shorter model", "model", func(tr *tree) { tr.write("proc/device-tree/model", "Yandex Station\x00") }},
		{"no model", "model", func(tr *tree) { tr.remove("proc/device-tree/model") }},
		{"no framebuffer", "framebuffer", func(tr *tree) { tr.remove("dev/fb0") }},
		{"a longer ring", "framebuffer_geometry", func(tr *tree) { tr.write("sys/class/graphics/fb0/virtual_size", "90,1\n") }},
		{"the ring further in", "framebuffer_geometry", func(tr *tree) { tr.write("sys/class/graphics/fb0/virtual_size", "189,1\n") }},
		{"two rows", "framebuffer_geometry", func(tr *tree) { tr.write("sys/class/graphics/fb0/virtual_size", "89,2\n") }},
		{"another depth", "framebuffer_geometry", func(tr *tree) { tr.write("sys/class/graphics/fb0/bits_per_pixel", "24\n") }},
		{"another stride", "framebuffer_geometry", func(tr *tree) { tr.write("sys/class/graphics/fb0/stride", "267\n") }},
		{"no framebuffer sysfs", "framebuffer_geometry", func(tr *tree) { tr.remove("sys/class/graphics/fb0") }},
		{"no sound card", "sound", func(tr *tree) { tr.remove("dev/snd/controlC0") }},
		{"playback only", "capture", func(tr *tree) { tr.remove("dev/snd/pcmC0D1c") }},
		{"a capture pcm of another card", "capture", func(tr *tree) {
			tr.remove("dev/snd/pcmC0D1c")
			tr.write("dev/snd/pcmC1D0c", "")
		}},
		{"no stock library", "stock_library", func(tr *tree) { tr.remove("system/vendor/quasar/libs/libyandex_evil.so") }},
		{"the stock library elsewhere", "stock_library", func(tr *tree) {
			tr.remove("system/vendor/quasar")
			tr.write("system/vendor/.quasar-disabled/libs/libyandex_evil.so", "")
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			tr := midiTree(t)
			c.edit(tr)
			logs, err := fingerprint(t, tr)
			if !errors.Is(err, board.ErrFingerprint) {
				t.Fatalf("fingerprint = %v", err)
			}
			if strings.Count(logs, "msg=fingerprint_mismatch") != 1 || !strings.Contains(logs, "msg=fingerprint_mismatch check="+c.check+"\n") {
				t.Fatalf("logs = %q", logs)
			}
		})
	}
}
