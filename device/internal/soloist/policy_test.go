package soloist_test

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/volume"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
)

// a deliberate slider move moves the level like a command: persisted and in
// force, and told back, which audiod takes as an echo and as its guard's level
func TestSenderRequestIsAppliedAndPersisted(t *testing.T) {
	h := newHarness(t, withLevel(30))
	h.ready()
	h.senderVolume(60)
	h.waitLog(`"msg":"media_volume","sender":"soloist","volume":60`)
	if level, ok := h.store.Load(); !ok || level != 60 {
		t.Fatalf("stored %d, %v", level, ok)
	}
	if level, known := h.vol.Current(); level != 60 || !known {
		t.Fatalf("current %d, %v", level, known)
	}
	waitFor(t, "the level back", func() bool { return slices.Equal(h.a.levels(), []uint32{30, 60}) })
	settle()
	if got := h.a.levels(); !slices.Equal(got, []uint32{30, 60}) {
		t.Fatalf("levels = %v", got)
	}
}

// audiod's sender guard learns the speaker's level only from SenderVolumeSet:
// after a command to 80 and a slider move to 20, its view is 20, so a later
// nudge from soloist is judged against 20, never 80
func TestAudiodSeesTheSendersOwnLevel(t *testing.T) {
	h := newHarness(t, withLevel(30))
	h.ready()
	if _, err := h.vol.Set(80, volume.OriginCommand); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the command's level", func() bool { return slices.Equal(h.a.levels(), []uint32{30, 80}) })
	h.senderVolume(20)
	h.waitLog(`"msg":"media_volume","sender":"soloist","volume":20`)
	waitFor(t, "audiod's view", func() bool { return slices.Equal(h.a.levels(), []uint32{30, 80, 20}) })
}

// rule 7: a device with no level of its own moves for no sender, and has none
// to tell it back
func TestUnknownLevelRefusesSenders(t *testing.T) {
	h := newHarness(t)
	h.ready()
	h.senderVolume(80)
	h.waitLog(`"msg":"media_volume_refused","sender":"soloist","reason":"unknown"`)
	settle()
	if level, known := h.vol.Current(); level != 50 || known {
		t.Fatalf("current %d, %v", level, known)
	}
	if _, ok := h.store.Load(); ok {
		t.Fatal("an unknown level was stored")
	}
	if got := h.a.levels(); len(got) != 0 {
		t.Fatalf("levels = %v", got)
	}
}

// a ring is louder than any slider: the request is refused and the slider
// goes back to the level in force
func TestRingRefusesSenders(t *testing.T) {
	var ringing atomic.Bool
	ringing.Store(true)
	h := newHarness(t, withLevel(30), withRinging(ringing.Load))
	h.ready()
	h.senderVolume(90)
	h.waitLog(`"msg":"media_volume_refused","sender":"soloist","reason":"ring"`)
	waitFor(t, "the level back", func() bool { return slices.Equal(h.a.levels(), []uint32{30, 30}) })
	if level, _ := h.vol.Current(); level != 30 {
		t.Fatalf("current %d", level)
	}
	ringing.Store(false)
	h.senderVolume(45)
	h.waitLog(`"msg":"media_volume","sender":"soloist","volume":45`)
	waitFor(t, "the applied level", func() bool { return slices.Equal(h.a.levels(), []uint32{30, 30, 45}) })
}

// every change goes back to soloist as the speaker's level: a command, a
// sender request, a restore and a sender request the setter clamped
func TestEveryChangeGoesBackToSoloist(t *testing.T) {
	h := newHarness(t, withLevel(30))
	h.ready()
	if _, err := h.vol.Set(20, volume.OriginCommand); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the command's level", func() bool { return slices.Equal(h.a.levels(), []uint32{30, 20}) })
	h.senderVolume(70)
	waitFor(t, "the sender's level", func() bool { return slices.Equal(h.a.levels(), []uint32{30, 20, 70}) })
	if _, err := h.vol.Set(25, volume.OriginRestore); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the restored level", func() bool { return slices.Equal(h.a.levels(), []uint32{30, 20, 70, 25}) })
	h.senderVolume(250)
	h.waitLog(`"msg":"media_volume","sender":"soloist","volume":100`)
	waitFor(t, "the clamped level", func() bool { return slices.Equal(h.a.levels(), []uint32{30, 20, 70, 25, 100}) })
	settle()
	if got := h.a.levels(); !slices.Equal(got, []uint32{30, 20, 70, 25, 100}) {
		t.Fatalf("levels = %v", got)
	}
}

// a level the store cannot keep is never applied: the request is refused and
// the slider goes back
func TestStoreFailureRefusesSenders(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs a file where the state dir goes")
	}
	h := newHarness(t, withLevel(30))
	h.ready()
	state := filepath.Join(filepath.Dir(h.opts.Dir), "state")
	if err := os.RemoveAll(state); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(state, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	h.senderVolume(70)
	h.waitLog(`"msg":"media_volume_refused","sender":"soloist","reason":"store"`)
	waitFor(t, "the level back", func() bool { return slices.Equal(h.a.levels(), []uint32{30, 30}) })
	if level, _ := h.vol.Current(); level != 30 {
		t.Fatalf("current %d", level)
	}
}

// the setter's listeners run under its lock: an audiod that stops reading
// must never hold up a level change
func TestListenerNeverBlocksTheSetter(t *testing.T) {
	h := newHarness(t, withLevel(30))
	h.ready()
	block := make(chan struct{})
	h.a.mu.Lock()
	h.a.block = block
	h.a.mu.Unlock()
	defer close(block)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range 50 {
			if _, err := h.vol.Set(i%2*10+10, volume.OriginCommand); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	// a setter a blocked send holds up never finishes, so the budget only has to outlast a loaded host: under -race
	// beside other gates, the 50 sets (each one persisted) took more than 2 s
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("a blocked send held up the setter")
	}
	if level, _ := h.vol.Current(); level != 20 {
		t.Fatalf("current %d", level)
	}
}

// airplay's slider moves the level through the same policy, and both
// senders hear every change (M4c-R7), the mover's own included
func TestAirplaySenderIsAppliedAndEchoedToBoth(t *testing.T) {
	h := newHarness(t, withLevel(30))
	h.ready()
	h.senderVolumeFrom(audiodv1.Sender_SENDER_AIRPLAY, 65)
	h.waitLog(`"msg":"media_volume","sender":"airplay","volume":65`)
	if level, ok := h.store.Load(); !ok || level != 65 {
		t.Fatalf("stored %d, %v", level, ok)
	}
	waitFor(t, "both levels back", func() bool {
		return slices.Equal(h.a.levels(), []uint32{30, 65}) && slices.Equal(h.a.levelsFor(audiodv1.Sender_SENDER_AIRPLAY), []uint32{30, 65})
	})
	if _, err := h.vol.Set(10, volume.OriginCommand); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "a command's level to both", func() bool {
		return slices.Equal(h.a.levels(), []uint32{30, 65, 10}) && slices.Equal(h.a.levelsFor(audiodv1.Sender_SENDER_AIRPLAY), []uint32{30, 65, 10})
	})
}

// rule 7: airplay moves nothing while the level is unknown or a ring sounds
func TestAirplaySenderRefused(t *testing.T) {
	h := newHarness(t)
	h.ready()
	h.senderVolumeFrom(audiodv1.Sender_SENDER_AIRPLAY, 80)
	h.waitLog(`"msg":"media_volume_refused","sender":"airplay","reason":"unknown"`)
	if level, known := h.vol.Current(); level != 50 || known {
		t.Fatalf("current %d, %v", level, known)
	}
	var ringing atomic.Bool
	ringing.Store(true)
	r := newHarness(t, withLevel(30), withRinging(ringing.Load))
	r.ready()
	r.senderVolumeFrom(audiodv1.Sender_SENDER_AIRPLAY, 90)
	r.waitLog(`"msg":"media_volume_refused","sender":"airplay","reason":"ring"`)
	waitFor(t, "the level back", func() bool { return slices.Equal(r.a.levelsFor(audiodv1.Sender_SENDER_AIRPLAY), []uint32{30, 30}) })
	if level, _ := r.vol.Current(); level != 30 {
		t.Fatalf("current %d", level)
	}
}

// bluetooth's volume, which cadenced hears itself, moves the level through the same policy; its follower hears every
// change and every refusal, and audiod's senders the level too
func TestBluetoothSenderMovesTheLevel(t *testing.T) {
	h := newHarness(t, withLevel(30))
	var told atomic.Int32
	h.s.FollowLevel(func() { told.Add(1) })
	h.ready()
	h.s.SenderVolume(audiodv1.Sender_SENDER_BLUETOOTH, 70)
	h.waitLog(`"msg":"media_volume","sender":"bluetooth","volume":70`)
	if level, ok := h.store.Load(); !ok || level != 70 {
		t.Fatalf("stored %d, %v", level, ok)
	}
	waitFor(t, "the level back to the senders", func() bool {
		return told.Load() == 1 && slices.Equal(h.a.levels(), []uint32{30, 70})
	})
}

// rule 7: a ring refuses bluetooth's move too, and the follower is told the level in force
func TestBluetoothSenderRefusedWhileRinging(t *testing.T) {
	var ringing atomic.Bool
	ringing.Store(true)
	h := newHarness(t, withLevel(30), withRinging(ringing.Load))
	var told atomic.Int32
	h.s.FollowLevel(func() { told.Add(1) })
	h.ready()
	h.s.SenderVolume(audiodv1.Sender_SENDER_BLUETOOTH, 90)
	h.waitLog(`"msg":"media_volume_refused","sender":"bluetooth","reason":"ring"`)
	waitFor(t, "the follower told", func() bool { return told.Load() == 1 })
	if level, _ := h.vol.Current(); level != 30 {
		t.Fatalf("current %d", level)
	}
}
