package agent_test

import (
	"context"
	"slices"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/agent"
)

var btOutput = agent.Need{Capability: agent.CapBluetoothOutput, Write: true}

func btHost(id, name, room string) agent.DeviceInfo {
	return agent.DeviceInfo{
		ID: id, Name: name, Room: room, Kind: agent.KindSpeaker, Online: true,
		Caps: []string{agent.CapAudioOutput, agent.CapBluetooth, agent.CapBluetoothOutput}, RemoteTargets: "allow",
	}
}

// a bluetooth output need left at here on a receive-only speaker goes to the room's host, else the household's only
// one; several answer ambiguous, none no_capability
func TestBluetoothOutputHereGoesToTheHost(t *testing.T) {
	self := speakerInfo()
	self.Caps = append(self.Caps, agent.CapBluetooth)
	offlinePi := btHost("pi-off", "Старая малина", "living_room")
	offlinePi.Online = false
	cases := []struct {
		name  string
		ds    []agent.DeviceInfo
		want  string
		cands []string
	}{
		{"the household's only host", []agent.DeviceInfo{self, btHost("pi", "Малина", "attic")}, "pi", nil},
		{"the room's host before another room's",
			[]agent.DeviceInfo{self, btHost("pi", "Малина", "living_room"), btHost("pi2", "Гараж", "garage")}, "pi", nil},
		{"two in the household, none in the room",
			[]agent.DeviceInfo{self, btHost("pi", "Малина", "attic"), btHost("pi2", "Гараж", "garage")},
			"ambiguous_device", []string{"Малина", "Гараж"}},
		{"two in the room", []agent.DeviceInfo{self, btHost("pi", "Малина", "living_room"), btHost("pi2", "Вторая", "living_room")},
			"ambiguous_device", []string{"Малина", "Вторая"}},
		{"none online", []agent.DeviceInfo{self, offlinePi}, "no_capability", nil},
		{"none at all", []agent.DeviceInfo{self}, "no_capability", nil},
		{"this device has it", []agent.DeviceInfo{btHost(device, "Колонка", "living_room"), btHost("pi", "Малина", "living_room")}, device, nil},
	}
	for _, c := range cases {
		got, e := resolver(c.ds...).ResolveTarget(context.Background(), woken(), "", btOutput)
		switch {
		case e != nil && e.Code != c.want:
			t.Fatalf("%s: %+v, want %s", c.name, e, c.want)
		case e != nil && c.cands != nil && !slices.Equal(e.Candidates, c.cands):
			t.Fatalf("%s: candidates %q, want %q", c.name, e.Candidates, c.cands)
		case e == nil && got.ID != c.want:
			t.Fatalf("%s: %+v, want %s", c.name, got, c.want)
		}
	}
	// the receive need stays on this device, and without it answers no_capability rather than finding a host
	r := resolver(speakerInfo(), btHost("pi", "Малина", "living_room"))
	if _, e := r.ResolveTarget(context.Background(), woken(), "", agent.Need{Capability: agent.CapBluetooth, Write: true}); e == nil || e.Code != "no_capability" {
		t.Fatalf("receive need: %+v", e)
	}
}
