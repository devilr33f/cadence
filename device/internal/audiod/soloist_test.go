package audiod_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/audiod"
	"git.shino.boo/devilreef/cadence/device/internal/audiod/audiodtest"
	"git.shino.boo/devilreef/cadence/device/internal/capability"
	"git.shino.boo/devilreef/cadence/device/internal/media"
	"git.shino.boo/devilreef/cadence/device/internal/soloist"
	"git.shino.boo/devilreef/cadence/device/internal/volume"
)

const fakeKey = "TESTKEY-not-a-real-one"

type soloistRig struct {
	*harness
	m    *media.Media
	s    *soloist.Soloist
	vol  *volume.Setter
	dir  string
	logs *recorder
}

// startSoloist runs the fake audiod with media and soloist, soloist enabled
// with a key and the device at level 40
func startSoloist(t *testing.T, args ...string) *soloistRig {
	t.Helper()
	data := t.TempDir()
	store := volume.NewStore(data)
	if err := store.Save(40); err != nil {
		t.Fatal(err)
	}
	logs := &recorder{}
	vol := volume.NewSetter(store, 50, slog.New(logs))
	s := soloist.New(vol, soloist.Options{Dir: filepath.Join(data, "soloist"), ScratchDir: filepath.Join(data, "run", "soloist"), CAFile: "/ca.pem"}, slog.New(logs))
	sa, ok := s.Capability().(capability.SecretApplier)
	if !ok {
		t.Fatal("media.soloist takes no secrets")
	}
	if err := sa.ApplySecrets(context.Background(), 1, json.RawMessage(`{"enabled":true,"deviceName":"Den"}`), map[string]string{"apiKey": fakeKey}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	m := media.New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	h := startSupervisorWith(t, 40, audiod.Timings{BackoffMin: 50 * time.Millisecond},
		[]audiod.SupervisorOption{audiod.WithMedia(m), audiod.WithSoloist(s)}, args...)
	return &soloistRig{harness: h, m: m, s: s, vol: vol, dir: data, logs: logs}
}

func (r *soloistRig) waitState(t *testing.T, want string) {
	t.Helper()
	ch := r.s.Capability().State()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case v := <-ch:
			if string(v) == want {
				return
			}
		case <-deadline:
			t.Fatalf("no state %s", want)
		}
	}
}

// audiod forgets soloist with its client: each Ready gets soloist's config
// after the volume, the duck and the media settings, and then the level, so
// audiod's sender guard knows it before soloist runs
func TestSoloistConfigFollowsEveryReady(t *testing.T) {
	r := startSoloist(t, "--crash-on-command")
	const settings = "media_settings duck=DUCK_MODE_DUCK eq=0/0 viz=true/VIZ_SOURCE_MUSIC"
	config := fmt.Sprintf("soloist_config enabled=true key_bytes=%d name=%q dir=%q scratch=%q ca=%q initial=40",
		len(fakeKey), "Den", filepath.Join(r.dir, "soloist"), filepath.Join(r.dir, "run", "soloist"), "/ca.pem")
	r.waitRecord(t, 1, "sender_volume_set", "sender=SENDER_SOLOIST level=40")
	if got := afterConfigure(audiodtest.Records(r.harness.dir), 1); len(got) < 6 || got[0] != "volume_set 40" ||
		got[1] != "duck_set false" || got[2] != "logs_set false" || got[3] != settings || got[4] != config ||
		got[5] != "sender_volume_set sender=SENDER_SOLOIST level=40" {
		t.Fatalf("run 1 after configure: %q", got)
	}
	if _, err := r.vol.Set(30, volume.OriginCommand); err != nil {
		t.Fatal(err)
	}
	r.waitRecord(t, 1, "sender_volume_set", "sender=SENDER_SOLOIST level=30")

	if c := commandCode(t, r.m, "media.player", "pause", `{}`); c != "unavailable" {
		t.Fatalf("pause cut by a crash: code %q", c)
	}
	r.waitLog(t, "audiod_ready", 2)
	r.waitRecord(t, 2, "sender_volume_set", "sender=SENDER_SOLOIST level=30")
	got := afterConfigure(audiodtest.Records(r.harness.dir), 2)
	if len(got) < 6 || got[0] != "volume_set 40" || got[4] != strings.Replace(config, "initial=40", "initial=30", 1) ||
		got[5] != "sender_volume_set sender=SENDER_SOLOIST level=30" {
		t.Fatalf("run 2 after configure: %q", got)
	}
	for _, rec := range audiodtest.Records(r.harness.dir) {
		if strings.Contains(rec.Detail, fakeKey) {
			t.Fatalf("the fake recorded the key: %v", rec)
		}
	}
}

// audiod's soloist envelopes reach soloist: its status becomes the state, and
// a slider move moves the level
func TestSoloistEnvelopesReachSoloist(t *testing.T) {
	r := startSoloist(t, "--soloist")
	r.waitState(t, `{"state":"no_build"}`)
	// the fake moves the slider to the level it was told, plus 5
	eventually(t, "the slider's level", func() bool { level, _ := r.vol.Current(); return level == 45 })
	if r.logs.count("media_volume") != 1 {
		t.Fatalf("log: %s", r.logs.dump())
	}
	r.stop()
	r.waitState(t, `{"state":"starting"}`)
}
