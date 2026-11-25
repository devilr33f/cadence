package volume_test

import (
	"context"
	"encoding/json"
	"testing"

	"git.shino.boo/devilreef/cadence/device/internal/volume"
	"git.shino.boo/devilreef/cadence/pkg/capschema"
)

func TestOutputConfigSetsTheDelayTrim(t *testing.T) {
	var got []int
	out := volume.NewOutput(at(t, 50), known, 2, 128000, volume.WithDelay(25, func(ms int) { got = append(got, ms) }))
	if out.Descriptor().ConfigSchema != capschema.AudioOutputConfig {
		t.Fatal("a speaker's audio.output advertises the speaker config")
	}
	for _, c := range []struct {
		cfg string
		ok  bool
	}{
		{`{"delayMs":-40}`, true},
		{`{}`, true},
		{`{"delayMs":1001}`, false},
		{`{"volume":{"mode":"system"}}`, false},
		{`{"other":1}`, false},
	} {
		if err := out.Apply(context.Background(), 1, json.RawMessage(c.cfg)); (err == nil) != c.ok {
			t.Fatalf("%s: err %v", c.cfg, err)
		}
	}
	// an unset trim is the board's own
	if len(got) != 2 || got[0] != -40 || got[1] != 25 {
		t.Fatalf("trims %v", got)
	}
}

func TestScreenOutputTakesTheVolumeMode(t *testing.T) {
	out := volume.NewOutput(at(t, 50), known, 2, 128000, volume.ScreenOutput())
	if out.Descriptor().ConfigSchema != capschema.AudioOutputScreenConfig {
		t.Fatal("a screen's audio.output advertises the screen config")
	}
	if volume.VolumeMode(out) != "software" {
		t.Fatal("software by default")
	}
	if err := out.Apply(context.Background(), 1, json.RawMessage(`{"delayMs":80,"volume":{"mode":"system"}}`)); err != nil {
		t.Fatal(err)
	}
	if volume.VolumeMode(out) != "system" {
		t.Fatal("mode not kept")
	}
	if err := out.Apply(context.Background(), 2, json.RawMessage(`{"volume":{"mode":"hdmi"}}`)); err == nil {
		t.Fatal("an unknown mode applied")
	}
	if err := out.Apply(context.Background(), 3, json.RawMessage(`{}`)); err != nil || volume.VolumeMode(out) != "software" {
		t.Fatalf("an empty config is software again: %v", err)
	}
}
