package pickle_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	pickle "git.shino.boo/devilreef/cadence/device/boards/yandex-pickle"
	"git.shino.boo/devilreef/cadence/device/internal/board"
)

func fingerprint(t *testing.T, tr *tree) (string, error) {
	t.Helper()
	log, buf := newLog()
	err := pickle.New(tr.path("data/cadence"), log, pickle.WithRoot(tr.root)).Fingerprint(context.Background())
	return buf.String(), err
}

func TestFingerprintAcceptsAPickle(t *testing.T) {
	if _, err := fingerprint(t, pickleTree(t)); err != nil {
		t.Fatalf("fingerprint = %v", err)
	}
	// the sensor is found by its name, whatever index the kernel gave it
	tr := pickleTree(t)
	tr.remove("sys/bus/iio/devices/" + iioDir(1))
	tr.write("sys/bus/iio/devices/"+iioDir(7)+"/name", "ltr308als01")
	if _, err := fingerprint(t, tr); err != nil {
		t.Fatalf("sensor at another index: %v", err)
	}
}

func TestFingerprintRefusesAnotherDevice(t *testing.T) {
	for _, c := range []struct {
		name  string
		check string
		edit  func(*tree)
	}{
		{"another model", "model", func(tr *tree) { tr.write("proc/device-tree/model", "Yandex Midi\x00") }},
		{"the model further in", "model", func(tr *tree) { tr.write("proc/device-tree/model", "Not a Yandex Pickle\x00") }},
		{"no model", "model", func(tr *tree) { tr.remove("proc/device-tree/model") }},
		{"no ring framebuffer", "framebuffer", func(tr *tree) { tr.remove("dev/fb1") }},
		{"no button framebuffer", "framebuffer", func(tr *tree) { tr.remove("dev/fb0") }},
		{"no sound card", "sound", func(tr *tree) { tr.remove("dev/snd/controlC0") }},
		{"playback only", "capture", func(tr *tree) { tr.remove("dev/snd/pcmC0D1c") }},
		{"a capture pcm of another card", "capture", func(tr *tree) {
			tr.remove("dev/snd/pcmC0D1c")
			tr.write("dev/snd/pcmC1D0c", "")
		}},
		{"no light sensor", "light_sensor", func(tr *tree) { tr.remove("sys/bus/iio/devices/" + iioDir(1)) }},
		{"another sensor's name", "light_sensor", func(tr *tree) {
			tr.write("sys/bus/iio/devices/"+iioDir(1)+"/name", "ltr308als01x\n")
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			tr := pickleTree(t)
			c.edit(tr)
			logs, err := fingerprint(t, tr)
			if !errors.Is(err, board.ErrFingerprint) {
				t.Fatalf("fingerprint = %v", err)
			}
			if !strings.Contains(logs, "msg=fingerprint_mismatch check="+c.check) {
				t.Fatalf("logs = %q", logs)
			}
		})
	}
}
