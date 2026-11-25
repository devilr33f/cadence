package midi_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	midi "git.shino.boo/devilreef/cadence/device/boards/yandex-midi"
	"git.shino.boo/devilreef/cadence/device/internal/wifi"
)

// the wifi profile dials wpa_cli under the board's root: absent, it answers
// nothing (the capability reports unsupported); present, it hands out the
// backend. nothing else on the midi rewrites the supplicant's conf.
func TestWiFiProfileDialsWpaCLI(t *testing.T) {
	tr := midiTree(t)
	log, _ := newLog()
	b := midi.New(tr.root, log, midi.WithRoot(tr.root))
	p := b.WiFi()
	if p.Interface != "wlan0" {
		t.Fatalf("interface = %q", p.Interface)
	}
	ctx := context.Background()
	if _, err := p.Dial(ctx); err == nil {
		t.Fatal("dial without wpa_cli")
	}
	cli := filepath.Join(tr.root, "usr", "sbin", "wpa_cli")
	if err := os.MkdirAll(filepath.Dir(cli), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cli, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	be, err := p.Dial(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := be.(*wifi.WpaCLI); !ok {
		t.Fatalf("backend = %T", be)
	}
}
