package tvboard

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/capability"
	"git.shino.boo/devilreef/cadence/device/internal/volume"
	"git.shino.boo/devilreef/cadence/pkg/capschema"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
)

type sink struct {
	mu   sync.Mutex
	envs []*audiodv1.Envelope
}

func (s *sink) send(e *audiodv1.Envelope) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.envs = append(s.envs, e)
	return nil
}

func (s *sink) airplay() *audiodv1.AirplayConfig {
	s.mu.Lock()
	defer s.mu.Unlock()
	var last *audiodv1.AirplayConfig
	for _, e := range s.envs {
		if c := e.GetAirplayConfig(); c != nil {
			last = c
		}
	}
	return last
}

func (s *sink) tap() *audiodv1.TapConfig {
	s.mu.Lock()
	defer s.mu.Unlock()
	var last *audiodv1.TapConfig
	for _, e := range s.envs {
		if c := e.GetTapConfig(); c != nil {
			last = c
		}
	}
	return last
}

func newBoard(t *testing.T) *Board {
	t.Helper()
	dir, err := os.MkdirTemp("", "tvb")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	b := New(Options{
		ID: "sim-tv", DataDir: dir, DefaultLevel: 30, ScreenSocket: filepath.Join(dir, "s.sock"),
		TapPath: filepath.Join(dir, "tap.sock"), AudiodBinary: "/nonexistent/audiod", AudiodSocket: filepath.Join(dir, "a.sock"),
		Configure: &audiodv1.Configure{Output: &audiodv1.OutputConfig{Backend: audiodv1.Backend_BACKEND_FILE, Channels: 2}},
	})
	t.Cleanup(func() { _ = b.Close() })
	return b
}

func screencastOf(t *testing.T, b *Board) capability.Provider {
	t.Helper()
	for _, p := range b.Capabilities() {
		if p.Descriptor().ID == capschema.MediaScreencastID {
			return p
		}
	}
	t.Fatal("no media.screencast")
	return nil
}

func waitState(t *testing.T, p capability.Provider, want string) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	var last string
	for {
		select {
		case st := <-p.State():
			last = string(st)
			if strings.Contains(last, want) {
				return
			}
		case <-deadline:
			t.Fatalf("state %s, want %s", last, want)
		}
	}
}

// media.screencast's config becomes audiod's libairplay config, its mirroring the tap's mirror frames, and the
// receiver's status its state
func TestScreencastConfiguresLibairplay(t *testing.T) {
	b := newBoard(t)
	sc := screencastOf(t, b)
	ap, med := &sink{}, &sink{}
	b.airplay.AirplayReady(ap.send)
	b.media.MediaReady(med.send)
	if c := ap.airplay(); c.GetEnabled() || c.GetBackend() != audiodv1.AirplayBackend_AIRPLAY_BACKEND_LIBAIRPLAY {
		t.Fatalf("before a config: %v", c)
	}
	if tp := med.tap(); tp.GetMirror() || !tp.GetPcm() {
		t.Fatalf("tap before a config: %v", tp)
	}
	waitState(t, sc, `"state":"off"`)
	cfg := `{"enabled":true,"name":"Гостиная","pin":true,"maxHeight":1080,"h265":true}`
	if err := sc.Apply(context.Background(), 1, json.RawMessage(cfg)); err != nil {
		t.Fatal(err)
	}
	c := ap.airplay()
	if !c.GetEnabled() || c.GetName() != "Гостиная" || !c.GetMirroring() || !c.GetRequirePin() || !c.GetH265() ||
		c.GetMaxHeight() != 1080 || c.GetMaxWidth() != 1920 || c.GetScratchDir() != filepath.Join(b.o.DataDir, "run", "airplay") {
		t.Fatalf("config %v", c)
	}
	if !med.tap().GetMirror() {
		t.Fatalf("tap %v", med.tap())
	}
	waitState(t, sc, `"state":"starting"`)
	b.airplay.AirplayEnvelope(&audiodv1.Envelope{Body: &audiodv1.Envelope_AirplayStatus{AirplayStatus: &audiodv1.AirplayStatus{
		State: audiodv1.AirplayState_AIRPLAY_STATE_FAILED, Code: "scratch_failed",
	}}})
	waitState(t, sc, `"state":"failed","code":"scratch_failed"`)
	b.airplay.AirplayEnvelope(&audiodv1.Envelope{Body: &audiodv1.Envelope_AirplayStatus{AirplayStatus: &audiodv1.AirplayStatus{
		State: audiodv1.AirplayState_AIRPLAY_STATE_READY,
	}}})
	waitState(t, sc, `"state":"ready"`)
	b.screen.MirrorState(&audiodv1.MirrorState{SessionId: "m1", Active: true, ClientName: "iPhone"})
	waitState(t, sc, `"mirroring":true,"clientName":"iPhone"`)
	if err := sc.Apply(context.Background(), 2, json.RawMessage(`{"enabled":false}`)); err != nil {
		t.Fatal(err)
	}
	if c := ap.airplay(); c.GetEnabled() || c.GetName() != "Cadence" {
		t.Fatalf("disabled config %v", c)
	}
	if med.tap().GetMirror() {
		t.Fatal("tap still carries mirror frames")
	}
}

// the sender's slider moves the level through rule 7's sender policy, which tells audiod the level in force
func TestScreencastSenderVolume(t *testing.T) {
	b := newBoard(t)
	_ = b.Capabilities()
	// a sender moves only a level the device owns (rule 7)
	if _, err := b.vol.Set(30, volume.OriginCommand); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go b.senders.Run(ctx)
	s := &sink{}
	b.senders.SoloistReady(s.send)
	b.senders.SoloistEnvelope(&audiodv1.Envelope{Body: &audiodv1.Envelope_SenderVolume{SenderVolume: &audiodv1.SenderVolume{
		Sender: audiodv1.Sender_SENDER_AIRPLAY, Level: 44,
	}}})
	deadline := time.Now().Add(2 * time.Second)
	for b.vol.Level() != 44 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if b.vol.Level() != 44 {
		t.Fatalf("level %d", b.vol.Level())
	}
	for time.Now().Before(deadline) {
		s.mu.Lock()
		n := 0
		for _, e := range s.envs {
			if v := e.GetSenderVolumeSet(); v != nil && v.GetLevel() == 44 {
				n++
			}
		}
		s.mu.Unlock()
		if n > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("audiod never heard the level in force")
}

// a screen's audiod is built without the url source and answers every url play unsupported (the bedroom tv's
// music_play), so a screen never reports media.url, with audiod or without: music plays on its own player
func TestScreenWithAudiodReportsNoMediaURL(t *testing.T) {
	b := newBoard(t)
	if !b.runsAudiod() {
		t.Fatal("the board runs no audiod")
	}
	for _, p := range b.Capabilities() {
		if p.Descriptor().ID == "media.url" {
			t.Fatal("a screen with audiod reports media.url")
		}
	}
}
