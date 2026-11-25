package media_test

import "testing"

// the new styles apply and show in the state, and an old config without them still applies
func TestVizAppliesTheNewStyles(t *testing.T) {
	r := newRig(t)
	for _, style := range []string{"orbit", "beat"} {
		r.apply(t, "media.viz", `{"style":"`+style+`"}`)
		if got, want := state(t, r.caps["media.viz"]), `{"enabled":true,"source":"music","style":"`+style+`"}`; got != want {
			t.Fatalf("state = %s, want %s", got, want)
		}
	}
	r.apply(t, "media.viz", `{"enabled":true,"source":"all","style":"equalizer"}`)
	if got := state(t, r.caps["media.viz"]); got != `{"enabled":true,"source":"all","style":"equalizer"}` {
		t.Fatalf("state = %s", got)
	}
}
