package settings_test

import (
	"slices"
	"strings"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/settings"
)

func TestBusyEntitiesKeyIsGlobalOnly(t *testing.T) {
	r := newRig(t)
	if got := r.effective(); got.BusyEntities != nil {
		t.Fatalf("default %v", got.BusyEntities)
	}
	r.put("/settings/global", `{"busyEntities":[`+
		`{"entity":"sensor.phone_state","state":"offhook","reason":"call"},`+
		`{"entity":"sensor.phone_state","state":"ringing","reason":"call"},`+
		`{"entity":"binary_sensor.mac_audio_input_in_use","state":"on","reason":"meeting"}]}`)
	want := []settings.BusyEntity{
		{Entity: "sensor.phone_state", State: "offhook", Reason: "call"},
		{Entity: "sensor.phone_state", State: "ringing", Reason: "call"},
		{Entity: "binary_sensor.mac_audio_input_in_use", State: "on", Reason: "meeting"},
	}
	if got := r.effective(); !slices.Equal(got.BusyEntities, want) {
		t.Fatalf("global %v", got.BusyEntities)
	}
	many := make([]string, 33)
	for i := range many {
		many[i] = `{"entity":"sensor.s` + strings.Repeat("x", i) + `","state":"on","reason":"dnd"}`
	}
	r.put("/settings/global", `{"busyEntities":[`+strings.Join(many[:32], ",")+`]}`)
	r.put("/settings/global", `{"busyEntities":[]}`)
	for _, body := range []string{
		`{"busyEntities":{}}`, `{"busyEntities":[null]}`, `{"busyEntities":["sensor.x"]}`,
		`{"busyEntities":[{"entity":"sensor.x","state":"on"}]}`,
		`{"busyEntities":[{"entity":"sensor.x","state":"on","reason":"sleep"}]}`,
		`{"busyEntities":[{"entity":"sensor.x","state":"","reason":"call"}]}`,
		`{"busyEntities":[{"entity":"Sensor.X","state":"on","reason":"call"}]}`,
		`{"busyEntities":[{"entity":"sensor.x","state":"on","reason":"call","devices":[]}]}`,
		`{"busyEntities":[{"entity":"sensor.x","state":"` + strings.Repeat("a", 256) + `","reason":"call"}]}`,
		`{"busyEntities":[{"entity":"sensor.x","state":"on","reason":"call"},{"entity":"sensor.x","state":"on","reason":"dnd"}]}`,
		`{"busyEntities":[` + strings.Join(many, ",") + `]}`,
	} {
		if st, b := r.do("PUT", "/settings/global", body); st != 422 || b != `{"error":"settings_invalid"}` {
			t.Fatalf("global %.90s: %d %s", body, st, b)
		}
	}
	// the household's phone makes every speaker busy
	if st, b := r.do("PUT", "/devices/"+r.dev+"/settings", `{"busyEntities":[]}`); st != 422 || b != `{"error":"settings_invalid"}` {
		t.Fatalf("device: %d %s", st, b)
	}
}

func TestAfterBusyKey(t *testing.T) {
	r := newRig(t)
	if got := r.effective(); got.AfterBusy != settings.AfterBusyAnnounce || got.InboxAfterBusy() {
		t.Fatalf("default %q", got.AfterBusy)
	}
	r.put("/devices/"+r.dev+"/settings", `{"afterBusy":"inbox"}`)
	if got := r.effective(); !got.InboxAfterBusy() {
		t.Fatalf("device %q", got.AfterBusy)
	}
	r.put("/settings/global", `{"afterBusy":"inbox"}`)
	r.put("/devices/"+r.dev+"/settings", `{"afterBusy":"announce"}`)
	if got := r.effective(); got.InboxAfterBusy() {
		t.Fatalf("device over global %q", got.AfterBusy)
	}
	for _, body := range []string{`{"afterBusy":"drop"}`, `{"afterBusy":true}`, `{"afterBusy":""}`} {
		if st, b := r.do("PUT", "/devices/"+r.dev+"/settings", body); st != 422 || b != `{"error":"settings_invalid"}` {
			t.Fatalf("device %s: %d %s", body, st, b)
		}
	}
	if st, b := r.do("PUT", "/settings/global", `{"afterBusy":"later"}`); st != 422 {
		t.Fatalf("global: %d %s", st, b)
	}
}
