package simtv_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	simtv "git.shino.boo/devilreef/cadence/device/boards/sim-tv"
	"git.shino.boo/devilreef/cadence/device/internal/board"
	"git.shino.boo/devilreef/cadence/device/internal/capability"
	"git.shino.boo/devilreef/cadence/device/internal/screen/screentest"
	"git.shino.boo/devilreef/cadence/pkg/capschema"
	screenv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/screen/v1"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "stv")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func TestFingerprint(t *testing.T) {
	if err := simtv.New(env(nil), shortDir(t), nil).Fingerprint(context.Background()); !errors.Is(err, board.ErrFingerprint) {
		t.Fatalf("err = %v", err)
	}
	if err := simtv.New(env(map[string]string{"CADENCE_SIM_TV": "1"}), shortDir(t), nil).Fingerprint(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func byID(ps []capability.Provider) map[string]capability.Provider {
	m := map[string]capability.Provider{}
	for _, p := range ps {
		m[p.Descriptor().ID] = p
	}
	return m
}

// the screen's volume keys are presses on the board's input; the server's video plays on the screen paused until the
// gate opens, which without audiod it does once the screen's player took the level in force (rule 7)
func TestScreenKeysAndPlayOverTheBoard(t *testing.T) {
	dir := shortDir(t)
	b := simtv.New(env(map[string]string{"CADENCE_SIM_TV": "1"}), dir, nil)
	t.Cleanup(func() { _ = b.Close() })
	if err := b.Fingerprint(context.Background()); err != nil {
		t.Fatal(err)
	}
	caps := byID(b.Capabilities())
	for _, id := range []string{"audio.output", capschema.DisplayScreenID, capschema.MediaVideoID, capschema.InputRemoteID, capschema.MediaScreencastID} {
		if caps[id] == nil {
			t.Fatalf("no %s", id)
		}
	}
	// a screen's audiod has no url source: the server plays music on the screen's own player
	if caps["media.url"] != nil {
		t.Fatal("a screen reports media.url")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	in := b.Input()
	go in.Run(ctx, nil)
	s, err := screentest.Connect(ctx, filepath.Join(dir, "run", "screen.sock"), screentest.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	lv, err := s.Wait(2*time.Second, func(e *screenv1.Envelope) bool { return e.GetSetLevel() != nil })
	if err != nil || int(lv.GetSetLevel().GetLevel()) != b.Volume().Level() {
		t.Fatalf("level %v, want %d", lv, b.Volume().Level())
	}
	if _, err := s.Wait(2*time.Second, func(e *screenv1.Envelope) bool { return e.GetPlaybackGate().GetOpen() }); err != nil {
		t.Fatal("the gate never opened")
	}
	before := b.Volume().Level()
	_ = s.Key(screenv1.Key_KEY_VOLUME_DOWN)
	deadline := time.Now().Add(2 * time.Second)
	for b.Volume().Level() == before && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if b.Volume().Level() != before-5 {
		t.Fatalf("level %d after %d", b.Volume().Level(), before)
	}
	// the press reaches the player's own volume, never above what it pressed to
	if _, err := s.Wait(2*time.Second, func(e *screenv1.Envelope) bool {
		return e.GetSetLevel() != nil && int(e.GetSetLevel().GetLevel()) == before-5
	}); err != nil {
		t.Fatal("the pressed level never reached the player")
	}
	if _, err := caps[capschema.MediaVideoID].Command(ctx, "play", json.RawMessage(`{"url":"https://a.test/v.mp4"}`)); err != nil {
		t.Fatal(err)
	}
	if _, st := s.State(); st != cadencev2.PlaybackState_PLAYBACK_STATE_PLAYING {
		t.Fatalf("state %v", st)
	}
	select {
	case ms := <-b.MediaSessions():
		if !strings.HasPrefix(ms.GetSessionId(), "video-") {
			t.Fatalf("session %v", ms)
		}
	case <-ctx.Done():
		t.Fatal("no session")
	}
}

func TestFakeScreenConnectsItself(t *testing.T) {
	dir := shortDir(t)
	b := simtv.New(env(map[string]string{"CADENCE_SIM_TV": "1", "CADENCE_SIM_TV_FAKE_SCREEN": "1"}), dir, nil)
	t.Cleanup(func() { _ = b.Close() })
	if err := b.Fingerprint(context.Background()); err != nil {
		t.Fatal(err)
	}
	display := byID(b.Capabilities())[capschema.DisplayScreenID]
	deadline := time.After(3 * time.Second)
	for {
		select {
		case st := <-display.State():
			if strings.Contains(string(st), `"screen":true`) {
				return
			}
		case <-deadline:
			t.Fatal("the fake screen never attached")
		}
	}
}
