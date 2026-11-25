package airplay_test

import (
	"strings"
	"testing"

	"git.shino.boo/devilreef/cadence/device/internal/airplay"
)

func names(cs []string) string { return strings.Join(cs, "|") }

func sentNames(s *sink) []string {
	var out []string
	for _, c := range s.configs() {
		out = append(out, c.GetName())
	}
	return out
}

// phones see the config's name, else the device's own, else Cadence
func TestNamePrecedence(t *testing.T) {
	for _, c := range []struct{ cfg, device, want string }{
		{`{"enabled":true}`, "", "Cadence"},
		{`{"enabled":true}`, "Bedroom Station 2", "Bedroom Station 2"},
		{`{"enabled":true}`, "  Кухня \n", "Кухня"},
		{`{"enabled":true}`, strings.Repeat("ё", 20), strings.Repeat("ё", 16)},
		{`{"enabled":true,"name":"Den"}`, "Bedroom Station 2", "Den"},
	} {
		a, p, _ := newAirplay(t)
		a.SetDeviceName(c.device)
		if err := apply(p, c.cfg); err != nil {
			t.Fatal(err)
		}
		if got := stored(a).GetName(); got != c.want {
			t.Errorf("%s with %q: %q, want %q", c.cfg, c.device, got, c.want)
		}
	}
}

// a rename re-advertises while no config names the receiver, and sends nothing when the name phones see stays
func TestRenameFollows(t *testing.T) {
	a, p, logs := newAirplay(t)
	s := &sink{}
	a.AirplayReady(s.send)
	if err := apply(p, `{"enabled":true}`); err != nil {
		t.Fatal(err)
	}
	a.SetDeviceName("Bedroom Station 2")
	a.SetDeviceName("Bedroom Station 2")
	a.SetDeviceName("")
	a.SetDeviceName("Cadence")
	a.SetDeviceName("Hall")
	if got := names(sentNames(s)); got != "Cadence|Cadence|Bedroom Station 2|Cadence|Hall" {
		t.Fatalf("names %s", got)
	}
	if err := apply(p, `{"enabled":true,"name":"Den"}`); err != nil {
		t.Fatal(err)
	}
	a.SetDeviceName("Attic")
	if got := names(sentNames(s)); got != "Cadence|Cadence|Bedroom Station 2|Cadence|Hall|Den" {
		t.Fatalf("names with a configured one %s", got)
	}
	// the config's name gone, the device's comes back
	if err := apply(p, `{"enabled":true}`); err != nil {
		t.Fatal(err)
	}
	if got := sentNames(s); got[len(got)-1] != "Attic" {
		t.Fatalf("names %v", got)
	}
	// the state never names it, nor does the log
	if got := latest(t, p); strings.Contains(got, "Attic") || strings.Contains(logs.String(), "Attic") {
		t.Fatalf("name leaked: state %s log %s", got, logs)
	}
}

// with audiod down a rename waits for its next Ready; the screens' Configure without a name takes the device's
func TestRenameWhileAudiodDownAndScreencast(t *testing.T) {
	a, _, _ := newAirplay(t)
	s := &sink{}
	a.AirplayReady(s.send)
	a.AirplayGone()
	a.Configure(airplay.Config{Enabled: true, Mirroring: true})
	a.SetDeviceName("Bedroom TV")
	if n := len(s.configs()); n != 1 {
		t.Fatalf("%d configs with audiod down", n)
	}
	if got := stored(a).GetName(); got != "Bedroom TV" {
		t.Fatalf("name %q", got)
	}
	a.Configure(airplay.Config{Enabled: true, Name: "Зал"})
	if got := stored(a).GetName(); got != "Зал" {
		t.Fatalf("configured name %q", got)
	}
}
