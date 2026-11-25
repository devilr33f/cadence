package vkcapsula_test

import (
	"strings"
	"testing"

	vkcapsula "git.shino.boo/devilreef/cadence/device/boards/vk-capsula-mini"
	"git.shino.boo/devilreef/cadence/device/internal/board"
)

func TestBoardIdentity(t *testing.T) {
	b := vkcapsula.New("", nil)
	if b.ID() != "vk-capsula-mini" {
		t.Fatalf("id = %q", b.ID())
	}
	p := b.Telemetry()
	if !p.Kmsg || len(p.Mounts) != 2 || p.Mounts[0] != "/data" || p.Mounts[1] != "/tmp" {
		t.Fatalf("telemetry = %+v", p)
	}
	if l, ok := b.Lights(); !ok || l == nil {
		t.Fatalf("lights = %v, %v", l, ok)
	}
	var _ board.Board = b
	if got := b.Paths(); got.Model != "/usr/share/voice-agent/model.bin" || got.MuteState != "/data/cadence/state/mute" {
		t.Fatalf("paths = %+v", got)
	}
}

func TestCapabilitiesHaveNoBluetoothOrSoloist(t *testing.T) {
	tr := capsulaTree(t)
	b := vkcapsula.New(tr.path("data/cadence"), nil, vkcapsula.WithRoot(tr.root))
	t.Cleanup(func() { _ = b.Close() })
	caps := b.Capabilities()
	if len(caps) == 0 {
		t.Fatal("no capabilities")
	}
	for _, c := range caps {
		name := strings.ToLower(c.Descriptor().ID)
		if strings.Contains(name, "bluetooth") || strings.Contains(name, "soloist") || strings.Contains(name, "spotify") ||
			strings.Contains(name, "airplay") {
			t.Fatalf("capability %q", c.Descriptor().ID)
		}
	}
}

// network.wifi runs over the firmware's own wpa_cli on wlan0
func TestCapabilitiesHaveWiFi(t *testing.T) {
	tr := capsulaTree(t)
	b := vkcapsula.New(tr.path("data/cadence"), nil, vkcapsula.WithRoot(tr.root))
	t.Cleanup(func() { _ = b.Close() })
	found := false
	for _, c := range b.Capabilities() {
		found = found || c.Descriptor().ID == "network.wifi"
	}
	if !found {
		t.Fatal("no network.wifi")
	}
	if p := b.WiFi(); p.Interface != "wlan0" || p.Dial == nil {
		t.Fatalf("wifi profile %+v", p)
	}
	if _, err := b.WiFi().Dial(t.Context()); err == nil {
		t.Fatal("dialed without a wpa_cli in the tree")
	}
}
