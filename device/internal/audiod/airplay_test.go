package audiod_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/airplay"
	"git.shino.boo/devilreef/cadence/device/internal/audiod"
	"git.shino.boo/devilreef/cadence/device/internal/audiod/audiodtest"
	"git.shino.boo/devilreef/cadence/device/internal/media"
	"git.shino.boo/devilreef/cadence/device/internal/soloist"
	"git.shino.boo/devilreef/cadence/device/internal/volume"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
)

// AirplayConfig follows every Ready after soloist's config and levels, and
// audiod's status becomes the capability's state
func TestAirplayConfigFollowsEveryReady(t *testing.T) {
	data := t.TempDir()
	store := volume.NewStore(data)
	if err := store.Save(40); err != nil {
		t.Fatal(err)
	}
	vol := volume.NewSetter(store, 50, slog.New(slog.NewTextHandler(io.Discard, nil)))
	s := soloist.New(vol, soloist.Options{Dir: filepath.Join(data, "soloist"), ScratchDir: filepath.Join(data, "run", "soloist"), CAFile: "/ca.pem"}, nil)
	a := airplay.New(airplay.Options{Dir: filepath.Join(data, "airplay"), ScratchDir: filepath.Join(data, "run", "airplay")}, nil)
	if err := a.Capability().Apply(context.Background(), 1, json.RawMessage(`{"enabled":true,"name":"Den"}`)); err != nil {
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
		[]audiod.SupervisorOption{audiod.WithMedia(m), audiod.WithSoloist(s), audiod.WithAirplay(a)}, "--airplay", "ports_busy", "--crash-on-command")
	deadline := time.After(10 * time.Second)
	for got := ""; got != `{"state":"failed","code":"ports_busy"}`; {
		select {
		case v := <-a.Capability().State():
			got = string(v)
		case <-deadline:
			t.Fatalf("no failed state; records %v", audiodtest.Records(h.dir))
		}
	}
	want := fmt.Sprintf("airplay_config enabled=true name=%q dir=%q scratch=%q", "Den", filepath.Join(data, "airplay"), filepath.Join(data, "run", "airplay"))
	got := afterConfigure(audiodtest.Records(h.dir), 1)
	// volume, duck, the children's lines off (no log pipeline), media settings, soloist's config and levels, then
	// airplay's config
	if len(got) < 8 || got[0] != "volume_set 40" || got[2] != "logs_set false" || !strings.HasPrefix(got[4], "soloist_config") ||
		got[len(got)-1] != want {
		t.Fatalf("run 1 after configure: %q", got)
	}
}

// handlers stands in for soloist and airplay, counting the connections airplay got
type handlers struct{ airplayReady atomic.Int32 }

func (*handlers) SoloistReady(func(*audiodv1.Envelope) error)   {}
func (*handlers) SoloistEnvelope(*audiodv1.Envelope)            {}
func (*handlers) SoloistGone()                                  {}
func (h *handlers) AirplayReady(func(*audiodv1.Envelope) error) { h.airplayReady.Add(1) }
func (*handlers) AirplayEnvelope(*audiodv1.Envelope)            {}
func (*handlers) AirplayGone()                                  {}

// airplay's sender volume and the levels it follows go through soloist's
// handler: a supervisor given airplay's handler alone refuses airplay
func TestAirplayNeedsSoloist(t *testing.T) {
	for _, withSoloist := range []bool{true, false} {
		logs := &recorder{}
		h := &handlers{}
		opts := []audiod.SupervisorOption{audiod.WithAirplay(h)}
		if withSoloist {
			opts = append(opts, audiod.WithSoloist(h))
		}
		audiod.NewSupervisor(audiod.Spec{}, 40, slog.New(logs), opts...)
		refusals := logs.all("audiod_airplay_refused")
		if withSoloist && len(refusals) != 0 {
			t.Fatalf("refused beside soloist: %s", logs.dump())
		}
		if !withSoloist && (len(refusals) != 1 || refusals[0].level != slog.LevelError || refusals[0].attrs["reason"] != "no_soloist") {
			t.Fatalf("not refused alone: %s", logs.dump())
		}
	}
}

// a refused airplay never gets a connection, and audiod never its config
func TestRefusedAirplayNeverStarts(t *testing.T) {
	a := &handlers{}
	h := startSupervisorWith(t, 40, audiod.Timings{}, []audiod.SupervisorOption{audiod.WithAirplay(a)}, "--airplay", "ports_busy")
	h.waitLog(t, "audiod_ready", 1)
	h.stop()
	if n := a.airplayReady.Load(); n != 0 {
		t.Fatalf("AirplayReady ran %d times", n)
	}
	for _, r := range audiodtest.Records(h.dir) {
		if r.Kind == "airplay_config" {
			t.Fatalf("audiod got an airplay config: %v", audiodtest.Records(h.dir))
		}
	}
}
