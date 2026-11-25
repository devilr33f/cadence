package micro

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"git.shino.boo/devilreef/cadence/device/internal/input"
	"git.shino.boo/devilreef/cadence/device/internal/leds"
)

func TestConfigureMatchesStockAudio(t *testing.T) {
	b := New("", nil)
	cfg := b.configure()
	if cfg.Capture.Device != "default" || cfg.Capture.Channels != 6 || cfg.Capture.SampleRate != 48000 ||
		cfg.Capture.PeriodFrames != 512 || cfg.Capture.BufferFrames != 2048 {
		t.Fatalf("capture = %+v", cfg.Capture)
	}
	if got := cfg.Capture.Layout.MicChannels; len(got) != 4 {
		t.Fatalf("mics = %v", got)
	}
	if got := cfg.Capture.Layout.ReferenceChannels; len(got) != 2 || got[0] != 4 || got[1] != 5 {
		t.Fatalf("references = %v", got)
	}
	engines := cfg.Capture.Engines
	if !engines.Enabled || engines.VqePreset != "yandexmicro" || engines.Library != stockLibrary || engines.CommandModel != "" {
		t.Fatalf("engines = %+v", engines)
	}
	if cfg.Output.MixerControl != "Master" || cfg.Output.Channels != 2 {
		t.Fatalf("output = %+v", cfg.Output)
	}
}

func TestButtonsMapOnlyVerifiedKeys(t *testing.T) {
	layout := buttons("")
	if len(layout.Devices) != 1 || layout.Devices[0].Name != "sar0_pad0" {
		t.Fatalf("devices = %+v", layout.Devices)
	}
	for code, want := range map[uint16]input.Kind{114: input.VolumeDown, 115: input.VolumeUp, 0x246: input.Action} {
		if layout.Keys[code] != want {
			t.Fatalf("key %d = %v", code, layout.Keys[code])
		}
	}
	if len(layout.Keys) != 3 {
		t.Fatalf("keys = %+v", layout.Keys)
	}
}

func TestRingWritesNineBrightnessControls(t *testing.T) {
	root := t.TempDir()
	plantLEDs(t, root)
	sink := &lightSink{root: root}
	if err := sink.Ring([]leds.RGB{{R: 1, G: 2, B: 3}, {R: 4, G: 5, B: 6}, {R: 7, G: 8, B: 9}}); err != nil {
		t.Fatal(err)
	}
	for colour, values := range map[string][]string{"red": {"1", "4", "7"}, "green": {"2", "5", "8"}, "blue": {"3", "6", "9"}} {
		for i, ring := range []string{"ring1", "ring2", "ring3"} {
			path := filepath.Join(root, "sys/class/leds/micro:"+colour+":"+ring+"/brightness")
			raw, err := os.ReadFile(path)
			if err != nil || string(raw) != values[i]+"\n" {
				t.Fatalf("%s = %q, %v", path, raw, err)
			}
		}
	}
}

func TestFingerprintAcceptsTheProbedLite(t *testing.T) {
	root := t.TempDir()
	plantLEDs(t, root)
	files := map[string]string{
		"proc/device-tree/model": model + "\x00", "dev/snd/controlC0": "", "dev/snd/pcmC0D1c": "",
		"system/vendor/quasar/libs/libyandex_evil.so": "stock",
	}
	for name, data := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	b := New("", nil)
	b.root = root
	if err := b.Fingerprint(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func plantLEDs(t *testing.T, root string) {
	t.Helper()
	for _, colour := range []string{"red", "green", "blue"} {
		for _, ring := range []string{"ring1", "ring2", "ring3"} {
			path := filepath.Join(root, "sys/class/leds/micro:"+colour+":"+ring+"/brightness")
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("0\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
}
