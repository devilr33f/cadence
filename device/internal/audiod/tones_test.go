package audiod_test

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/audio"
	"git.shino.boo/devilreef/cadence/device/internal/audiod"
	"git.shino.boo/devilreef/cadence/device/internal/audiod/audiodtest"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
)

// toneStates keeps every ToneState the supervisor hands the ring
type toneStates struct {
	mu   sync.Mutex
	seen []string
}

func (s *toneStates) add(st *audiodv1.ToneState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seen = append(s.seen, fmt.Sprintf("%s %t %s", st.GetId(), st.GetSounding(), st.GetCode()))
}

func (s *toneStates) has(want string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Contains(s.seen, want)
}

func (s *toneStates) all() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.seen)
}

func TestTonesRoundTripThroughAudiod(t *testing.T) {
	states := &toneStates{}
	h := startSupervisorWith(t, 40, audiod.Timings{}, []audiod.SupervisorOption{audiod.WithToneState(states.add)})
	h.waitLog(t, "audiod_ready", 1)

	if err := h.sup.StartTone("alarm-1", audiodv1.ToneKind_TONE_KIND_ALARM); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the ring sounding", func() bool { return states.has("alarm-1 true ") })
	if err := h.sup.SetTransientLevel(60); err != nil {
		t.Fatal(err)
	}
	if err := h.sup.StopTone("alarm-1"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the ring stopped", func() bool { return states.has("alarm-1 false stopped") })

	got := strings.Join(afterConfigure(audiodtest.Records(h.dir), 1), ", ")
	want := "volume_set 40, duck_set false, logs_set false, tone_start alarm-1 TONE_KIND_ALARM, volume_set 60 transient, tone_stop alarm-1"
	if got != want {
		t.Fatalf("after configure:\n got %s\nwant %s", got, want)
	}
}

// an alarm's ramp is never the level in force: the next audiod gets the persisted level, and a ring that sounded on the
// one that went away is told it is gone, so it can start again
func TestTransientLevelAndRingEndWithTheirAudiod(t *testing.T) {
	states := &toneStates{}
	h := startSupervisorWith(t, 40, audiod.Timings{BackoffMin: 100 * time.Millisecond},
		[]audiod.SupervisorOption{audiod.WithToneState(states.add)}, "--crash-on-tone-stop")
	h.waitLog(t, "audiod_ready", 1)

	if err := h.sup.StartTone("alarm-1", audiodv1.ToneKind_TONE_KIND_ALARM); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the ring sounding", func() bool { return states.has("alarm-1 true ") })
	if err := h.sup.SetTransientLevel(60); err != nil {
		t.Fatal(err)
	}
	h.waitRecord(t, 1, "volume_set", "60 transient")
	// the fake dies on it, unanswered
	if err := h.sup.StopTone("alarm-1"); err != nil {
		t.Fatal(err)
	}
	h.waitLog(t, "audiod_ready", 2)

	if got := firstAfterConfigure(audiodtest.Records(h.dir), 2); got.Kind != "volume_set" || got.Detail != "40" {
		t.Fatalf("run 2 starts with %s %s", got.Kind, got.Detail)
	}
	if got, want := states.all(), []string{"alarm-1 true ", "alarm-1 false gone"}; !slices.Equal(got, want) {
		t.Fatalf("tone states %v, want %v", got, want)
	}
}

// a ramp step audiod refuses is no failure of the level in force: it logs as its own event, with audiod's reason, and
// audiod refuses one while no ring sounds, so a step that comes after the stop never moves the hardware
func TestARefusedRampStepIsNoFailedVolume(t *testing.T) {
	states := &toneStates{}
	h := startSupervisorWith(t, 40, audiod.Timings{}, []audiod.SupervisorOption{audiod.WithToneState(states.add)})
	h.waitLog(t, "audiod_ready", 1)

	if err := h.sup.SetTransientLevel(60); err != nil {
		t.Fatal(err)
	}
	h.waitLog(t, "audiod_ramp_refused", 1)
	if err := h.sup.StartTone("alarm-1", audiodv1.ToneKind_TONE_KIND_ALARM); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the ring sounding", func() bool { return states.has("alarm-1 true ") })
	if err := h.sup.SetTransientLevel(65); err != nil {
		t.Fatal(err)
	}
	if err := h.sup.StopTone("alarm-1"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the ring stopped", func() bool { return states.has("alarm-1 false " + audiod.ToneStopped) })
	if err := h.sup.SetTransientLevel(70); err != nil {
		t.Fatal(err)
	}
	h.waitLog(t, "audiod_ramp_refused", 2)

	refused := h.log.all("audiod_ramp_refused")
	for i, want := range []int64{60, 70} {
		if refused[i].attrs["volume"] != want || refused[i].attrs["reason"] != "no_ring" {
			t.Fatalf("refusal %d: %v", i, refused[i].attrs)
		}
	}
	if n := h.log.count("audiod_volume_failed"); n != 0 {
		t.Fatalf("%d audiod_volume_failed: %s", n, h.log.dump())
	}
}

// an error sound is a one-shot error tone naming its prompt, and audiod learns where the prompts are: share/prompts
// beside the bin its binary is in
func TestErrorSoundNamesItsPrompt(t *testing.T) {
	h := startSupervisorWith(t, 40, audiod.Timings{}, nil)
	h.waitLog(t, "audiod_ready", 1)
	sounder, ok := h.sup.IO(nil).Speaker.(audio.ErrorSounder)
	if !ok {
		t.Fatal("the supervised speaker plays no error sound")
	}
	sounder.ErrorSound("not_heard", "ru")
	sounder.ErrorSound("", "")
	h.waitRecord(t, 1, "tone", `TONE_KIND_ERROR prompt="not_heard" lang="ru"`)
	h.waitRecord(t, 1, "tone", `TONE_KIND_ERROR prompt="" lang=""`)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	h.waitRecord(t, 1, "configure_prompts", filepath.Join(filepath.Dir(filepath.Dir(exe)), "share", "prompts"))
}
