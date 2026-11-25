package audiod_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/audio"
	"git.shino.boo/devilreef/cadence/device/internal/audiod"
	"git.shino.boo/devilreef/cadence/device/internal/audiod/audiodtest"
	"git.shino.boo/devilreef/cadence/device/internal/capability"
	"git.shino.boo/devilreef/cadence/device/internal/media"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

func startMedia(t *testing.T, args ...string) (*harness, *media.Media) {
	t.Helper()
	m := media.New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	h := startSupervisorWith(t, 40, audiod.Timings{BackoffMin: 50 * time.Millisecond}, []audiod.SupervisorOption{audiod.WithMedia(m)}, args...)
	h.waitLog(t, "audiod_ready", 1)
	return h, m
}

func provider(t *testing.T, m *media.Media, id string) capability.Provider {
	t.Helper()
	for _, p := range m.Capabilities() {
		if p.Descriptor().ID == id {
			return p
		}
	}
	t.Fatalf("no %s", id)
	return nil
}

// commandCode runs a command and returns its code, "" for ok
func commandCode(t *testing.T, m *media.Media, id, name, args string) string {
	t.Helper()
	_, err := provider(t, m, id).Command(context.Background(), name, json.RawMessage(args))
	var ce *capability.CommandError
	switch {
	case err == nil:
		return ""
	case errors.As(err, &ce):
		return ce.Code
	default:
		t.Fatalf("%s.%s: %v", id, name, err)
		return ""
	}
}

// afterConfigure is what a run received after its Configure, in order
func afterConfigure(recs []audiodtest.Record, run int) []string {
	var out []string
	seen := false
	for _, r := range recs {
		if r.Run != run {
			continue
		}
		if seen {
			out = append(out, r.Kind+" "+r.Detail)
		}
		seen = seen || r.Kind == "configure"
	}
	return out
}

func nextSession(t *testing.T, m *media.Media) *cadencev2.MediaSessionState {
	t.Helper()
	select {
	case s := <-m.Sessions():
		return s
	case <-time.After(5 * time.Second):
		t.Fatal("no media session state")
		return nil
	}
}

// audiod forgets the duck and the settings when its client goes: each Ready
// gets them again, right after the VolumeSet, and a command it never answered
// is answered unavailable
func TestMediaSettingsAndDuckFollowEveryReady(t *testing.T) {
	h, m := startMedia(t, "--crash-on-command")
	const flat, muffled = "duck=DUCK_MODE_DUCK eq=0/0 viz=true/VIZ_SOURCE_MUSIC", "duck=DUCK_MODE_MUFFLE eq=0/0 viz=true/VIZ_SOURCE_MUSIC"
	h.waitRecord(t, 1, "media_settings", flat)
	if err := provider(t, m, "media.player").Apply(context.Background(), 1, json.RawMessage(`{"duckMode":"muffle"}`)); err != nil {
		t.Fatal(err)
	}
	h.waitRecord(t, 1, "media_settings", muffled)
	ducker, ok := h.sup.IO(nil).Speaker.(audio.Ducker)
	if !ok {
		t.Fatal("the supervised speaker does not duck")
	}
	ducker.Duck(true)
	h.waitRecord(t, 1, "duck_set", "true")

	if c := commandCode(t, m, "media.player", "pause", `{}`); c != "unavailable" {
		t.Fatalf("pause cut by a crash: code %q", c)
	}
	h.waitLog(t, "audiod_ready", 2)
	h.waitRecord(t, 2, "media_settings", muffled)
	recs := audiodtest.Records(h.dir)
	// the children's lines off between them: the supervisor has no log pipeline
	if got := afterConfigure(recs, 1); len(got) < 4 || got[0] != "volume_set 40" || got[1] != "duck_set false" ||
		got[2] != "logs_set false" || got[3] != "media_settings "+flat {
		t.Fatalf("run 1 after configure: %q", got)
	}
	if got := afterConfigure(recs, 2); len(got) < 4 || got[0] != "volume_set 40" || got[1] != "duck_set true" ||
		got[2] != "logs_set false" || got[3] != "media_settings "+muffled {
		t.Fatalf("run 2 after configure: %q", got)
	}
	ducker.Duck(false)
	h.waitRecord(t, 2, "duck_set", "false")
}

func TestURLPlayRoundTripsThroughAudiod(t *testing.T) {
	h, m := startMedia(t, "--media-state")
	if s := nextSession(t, m); s.GetState() != cadencev2.PlaybackState_PLAYBACK_STATE_IDLE || s.GetSessionId() != "" {
		t.Fatalf("state after ready = %v", s)
	}
	select {
	case f := <-m.Viz():
		if f.Bands[0] != 7 || f.Bands[15] != 200 {
			t.Fatalf("viz frame %v", f)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no viz frame")
	}

	title := strings.Repeat("ж", 150)
	args, _ := json.Marshal(map[string]any{"url": "https://radio.example/x.flac", "metadata": map[string]string{"title": title}})
	if c := commandCode(t, m, "media.url", "play", string(args)); c != "" {
		t.Fatalf("play: code %q", c)
	}
	h.waitRecord(t, 1, "url_play", "URL_MODE_SESSION 0 https://radio.example/x.flac title_bytes=254 utf8=true")
	s := nextSession(t, m)
	if s.GetState() != cadencev2.PlaybackState_PLAYBACK_STATE_PLAYING || s.GetSource() != cadencev2.MediaSource_MEDIA_SOURCE_URL ||
		s.GetMetadata().GetTitle() != title[:254] || strings.Join(s.GetActions(), ",") != "pause,stop" {
		t.Fatalf("state = %v", s)
	}
	if c := commandCode(t, m, "media.player", "seek", `{"positionMs":5000}`); c != "" {
		t.Fatalf("seek: code %q", c)
	}
	h.waitRecord(t, 1, "media_command", "MEDIA_ACTION_SEEK 5000")
}

func TestAudiodCodesPassThrough(t *testing.T) {
	h, m := startMedia(t, "--media-code=not_found")
	if c := commandCode(t, m, "media.url", "play", `{"url":"http://radio.example/gone","mode":"overlay","startMs":20}`); c != "not_found" {
		t.Fatalf("play: code %q", c)
	}
	h.waitRecord(t, 1, "url_play", "URL_MODE_OVERLAY 20 http://radio.example/gone title_bytes=0 utf8=true")
	if c := commandCode(t, m, "media.player", "stop", `{}`); c != "not_found" {
		t.Fatalf("stop: code %q", c)
	}
	h.waitRecord(t, 1, "media_command", "MEDIA_ACTION_STOP 0")
}

// a screen's player registered with audiod takes the speaker under its own id, and the server gets the player's
// state; the server's duck goes over as a SessionControl, and the tap's config
func TestExternalSessionThroughTheSupervisor(t *testing.T) {
	h, m := startMedia(t)
	m.UseTap(&audiodv1.TapConfig{Path: "@tap-test"})
	m.UpdateExternal(media.External{State: &cadencev2.MediaSessionState{
		SessionId: "video-1", Source: cadencev2.MediaSource_MEDIA_SOURCE_VIDEO, State: cadencev2.PlaybackState_PLAYBACK_STATE_PLAYING,
		AudioRoute: cadencev2.AudioRoute_AUDIO_ROUTE_DIRECT, Metadata: &cadencev2.MediaMetadata{Title: "T"},
	}})
	s := nextSession(t, m)
	for s.GetSessionId() != "video-1" {
		s = nextSession(t, m)
	}
	if s.GetMetadata().GetTitle() != "T" || s.GetSource() != cadencev2.MediaSource_MEDIA_SOURCE_VIDEO {
		t.Fatalf("session = %v", s)
	}
	if !m.GateOpen() {
		t.Fatal("the gate is shut with the level held")
	}
	m.ServerDuck(&cadencev2.Duck{Active: true, Level: 0.2})
	h.waitRecord(t, 1, "external_source", "video-1 EXTERNAL_OP_UPDATE PLAYBACK_STATE_PLAYING AUDIO_ROUTE_DIRECT")
	h.waitRecord(t, 1, "tap_config", "@tap-test")
	h.waitRecord(t, 1, "session_control", `"" duck=true/0.2`)
}
