package rpi

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/board"
	"git.shino.boo/devilreef/cadence/device/internal/cec"
	"git.shino.boo/devilreef/cadence/device/internal/input"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
	screenv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/screen/v1"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestFingerprintByDeviceTreeModel(t *testing.T) {
	root := t.TempDir()
	b := New(env(nil), t.TempDir(), nil, withRoot(root))
	if err := b.Fingerprint(context.Background()); !errors.Is(err, board.ErrFingerprint) {
		t.Fatalf("err = %v", err)
	}
	dt := filepath.Join(root, "proc", "device-tree")
	if err := os.MkdirAll(dt, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dt, "model"), []byte("Yandex Station\x00"), 0o600)
	if err := b.Fingerprint(context.Background()); !errors.Is(err, board.ErrFingerprint) {
		t.Fatalf("err = %v", err)
	}
	_ = os.WriteFile(filepath.Join(dt, "model"), []byte("Raspberry Pi 5 Model B Rev 1.0\x00"), 0o600)
	if err := b.Fingerprint(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestConfigurePlaysToTheHDMIDeviceWithASoftwareGain(t *testing.T) {
	c := configure("hdmi:CARD=vc4hdmi0")
	out := c.GetOutput()
	if out.GetBackend() != audiodv1.Backend_BACKEND_ALSA || out.GetDevice() != "hdmi:CARD=vc4hdmi0" ||
		out.GetMixerControl() != "" || c.GetCapture() != nil {
		t.Fatalf("configure = %v", c)
	}
}

func TestRemotesTakeTheVolumeKeysOnly(t *testing.T) {
	l := layout("/r", []string{"gpio_ir_recv", " ", "BT Remote"})
	if len(l.Devices) != 2 || l.Keys[keyVolumeUp] != input.VolumeUp || l.Keys[keyVolumeDown] != input.VolumeDown || len(l.Keys) != 2 {
		t.Fatalf("layout = %+v", l)
	}
}

func TestCECKeysVolumeToTheBoardTheRestToTheScreen(t *testing.T) {
	var d cecDecoder
	now := time.UnixMilli(1000)
	buttons := make(chan input.ButtonEvent, 4)
	var keys []*screenv1.InputEvent
	feed := func(opcode byte, ops ...byte) {
		if e, ok := d.frame(cec.Frame{Opcode: opcode, Ops: ops}, now); ok {
			route(e, buttons, func(e *screenv1.InputEvent) { keys = append(keys, e) })
		}
	}
	feed(opUserControlPressed, 0x41)
	feed(opUserControlReleased)
	feed(opUserControlPressed, 0x01)
	feed(opUserControlReleased)
	feed(opUserControlPressed, 0x7f) // unknown
	feed(opUserControlPressed)
	feed(opUserControlReleased)
	if len(buttons) != 2 {
		t.Fatalf("%d buttons", len(buttons))
	}
	if e := <-buttons; e.Kind != input.VolumeUp || !e.Pressed {
		t.Fatalf("press = %v", e)
	}
	if e := <-buttons; e.Pressed {
		t.Fatalf("release = %v", e)
	}
	if len(keys) != 2 || keys[0].GetKey() != screenv1.Key_KEY_UP || keys[0].GetSource() != screenv1.InputSource_INPUT_SOURCE_CEC ||
		keys[1].GetAction() != screenv1.KeyAction_KEY_ACTION_UP {
		t.Fatalf("keys = %v", keys)
	}
}
