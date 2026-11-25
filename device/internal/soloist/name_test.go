package soloist_test

import (
	"strings"
	"testing"
)

func (a *fakeAudiod) names() string {
	var out []string
	for _, c := range a.configs() {
		out = append(out, c.GetDeviceName())
	}
	return strings.Join(out, "|")
}

// spotify's apps see the config's name, else the device's own, else Cadence
func TestNamePrecedence(t *testing.T) {
	for _, c := range []struct{ cfg, device, want string }{
		{`{"enabled":true}`, "", "Cadence"},
		{`{"enabled":true}`, "Bedroom Station 2", "Bedroom Station 2"},
		{`{"enabled":true}`, " Кухня\t", "Кухня"},
		{`{"enabled":true}`, strings.Repeat("Bedroom ", 5), "Bedroom Bedroom Bedroom Bedroom"},
		{`{"enabled":true,"deviceName":"Den"}`, "Bedroom Station 2", "Den"},
	} {
		h := newHarness(t)
		h.s.SetDeviceName(c.device)
		h.mustApply(c.cfg, map[string]string{"apiKey": testKey})
		h.ready()
		if got := h.a.names(); got != c.want {
			t.Errorf("%s with %q: %q, want %q", c.cfg, c.device, got, c.want)
		}
	}
}

// a rename sends soloist's config again only when the name it advertises changes: audiod restarts a running soloist
// for a new name, so the same name, or one a config overrides, sends nothing
func TestRenameFollows(t *testing.T) {
	h := newHarness(t)
	h.mustApply(`{"enabled":true}`, map[string]string{"apiKey": testKey})
	h.ready()
	h.s.SetDeviceName("Bedroom Station 2")
	h.s.SetDeviceName("Bedroom Station 2")
	h.s.SetDeviceName("Hall")
	if got := h.a.names(); got != "Cadence|Bedroom Station 2|Hall" {
		t.Fatalf("names %s", got)
	}
	if last := h.a.configs()[2]; !last.GetEnabled() || last.GetApiKey() != testKey {
		t.Fatalf("a rename's config enabled=%v key bytes=%d", last.GetEnabled(), len(last.GetApiKey()))
	}
	h.mustApply(`{"enabled":true,"deviceName":"Den"}`, map[string]string{"apiKey": testKey})
	h.s.SetDeviceName("Attic")
	if got := h.a.names(); got != "Cadence|Bedroom Station 2|Hall|Den" {
		t.Fatalf("names with a configured one %s", got)
	}
	// audiod down: the name waits for its next ready
	h.mustApply(`{"enabled":true}`, map[string]string{"apiKey": testKey})
	h.s.SoloistGone()
	h.s.SetDeviceName("Porch")
	if got := h.a.names(); got != "Cadence|Bedroom Station 2|Hall|Den|Attic" {
		t.Fatalf("names with audiod down %s", got)
	}
	h.ready()
	if got := h.a.names(); !strings.HasSuffix(got, "|Attic|Porch") {
		t.Fatalf("names after ready %s", got)
	}
	settle()
	for _, s := range h.allStates() {
		if strings.Contains(s, "Porch") || strings.Contains(s, "Attic") {
			t.Fatalf("state names the device: %s", s)
		}
	}
	if strings.Contains(h.logs.String(), "Porch") {
		t.Fatalf("log names the device: %s", h.logs)
	}
}
