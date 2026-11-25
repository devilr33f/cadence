package vkcapsula_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	vkcapsula "git.shino.boo/devilreef/cadence/device/boards/vk-capsula-mini"
	"git.shino.boo/devilreef/cadence/device/internal/board"
)

func fingerprint(tr *tree) (string, error) {
	log, buf := newLog()
	err := vkcapsula.New(tr.path("data/cadence"), log, vkcapsula.WithRoot(tr.root)).Fingerprint(context.Background())
	return buf.String(), err
}

func TestFingerprintAcceptsTheProbedCapsula(t *testing.T) {
	logs, err := fingerprint(capsulaTree(t))
	if err != nil {
		t.Fatalf("fingerprint = %v", err)
	}
	if strings.Contains(logs, "wake_model_missing") {
		t.Fatalf("logs = %q", logs)
	}
}

func TestMissingWakeModelIsLoggedNotRefused(t *testing.T) {
	tr := capsulaTree(t)
	tr.remove("usr/share/voice-agent/model.bin")
	logs, err := fingerprint(tr)
	if err != nil || !strings.Contains(logs, "msg=wake_model_missing") {
		t.Fatalf("fingerprint = %v, logs = %q", err, logs)
	}
}

func TestFingerprintRefusesAnotherDevice(t *testing.T) {
	for _, c := range []struct {
		name, check string
		edit        func(*tree)
	}{
		{"another soc", "compatible", func(tr *tree) { tr.write("proc/device-tree/compatible", "rockchip,rk3308\x00") }},
		{"no compatible", "compatible", func(tr *tree) { tr.remove("proc/device-tree/compatible") }},
		{"no ring", "ring", func(tr *tree) { tr.remove("dev/topleds") }},
		{"no first clock led", "clock", func(tr *tree) { tr.remove("sys/class/leds/clock-led1") }},
		{"no last clock led", "clock", func(tr *tree) { tr.remove("sys/class/leds/clock-led30") }},
		{"no touch pad", "buttons", func(tr *tree) { tr.remove("sys/class/input/event0") }},
		{"another card", "sound", func(tr *tree) { tr.write("proc/asound/cards", " 0 [Other]: other\n") }},
		{"no cards", "sound", func(tr *tree) { tr.remove("proc/asound/cards") }},
	} {
		t.Run(c.name, func(t *testing.T) {
			tr := capsulaTree(t)
			c.edit(tr)
			logs, err := fingerprint(tr)
			if !errors.Is(err, board.ErrFingerprint) {
				t.Fatalf("fingerprint = %v", err)
			}
			if !strings.Contains(logs, "msg=fingerprint_mismatch check="+c.check) {
				t.Fatalf("logs = %q", logs)
			}
		})
	}
}
