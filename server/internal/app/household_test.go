package app

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/pkg/capschema"
	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/devices"
	"git.shino.boo/devilreef/cadence/server/internal/enroll"
	"git.shino.boo/devilreef/cadence/server/internal/settings"
)

// a screen's tiebreak signals: off and its drawer from its display.screen state, playing from its session, its
// last input from its last screen context
func TestHouseholdReadsTheScreensSignals(t *testing.T) {
	at := time.Date(2026, 11, 15, 20, 0, 0, 0, time.UTC)
	states := map[string]string{
		"tv1": `{"power":"on","screen":true,"drawerOpen":true}`,
		"tv2": `{"power":"off","screen":true}`,
	}
	h := household(householdDeps{
		list: func(context.Context) ([]enroll.Listed, error) {
			return []enroll.Listed{{ID: "tv1", Status: "enrolled"}, {ID: "tv2", Status: "enrolled"}}, nil
		},
		detail: func(_ context.Context, id string) (devices.Detail, error) {
			return devices.Detail{Capabilities: []devices.CapabilityView{{ID: capschema.DisplayScreenID, State: json.RawMessage(states[id])}}}, nil
		},
		settings: func(context.Context, string) (settings.Values, error) { return settings.Values{HAArea: "bedroom"}, nil },
		online:   func(string) bool { return true },
		playing: func(id string) (agent.NowPlaying, bool) {
			return agent.NowPlaying{Title: "x", State: "playing"}, id == "tv2"
		},
		seen: func(id string) time.Time {
			if id == "tv1" {
				return at
			}
			return time.Time{}
		},
	})
	all, err := h(context.Background())
	if err != nil || len(all) != 2 {
		t.Fatal(all, err)
	}
	if w := all[0].Watch; w.Off || !w.DrawerOpen || w.Playing || !w.LastInput.Equal(at) {
		t.Fatalf("tv1 %+v", w)
	}
	if w := all[1].Watch; !w.Off || w.DrawerOpen || !w.Playing || !w.LastInput.IsZero() {
		t.Fatalf("tv2 %+v", w)
	}
}
