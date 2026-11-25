package screen_test

import (
	"encoding/json"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/screen"
	"git.shino.boo/devilreef/cadence/device/internal/screen/screentest"
	"git.shino.boo/devilreef/cadence/pkg/capschema"
	screenv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/screen/v1"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

const followGroup = "0b6f3a52-6a1e-4d2b-9c51-3c1d2a4e5f60"

// a follower's play (multiroom P4) reaches the screen with its group, says so in media.video's state, never becomes
// the device's session (the group's carries its sound), and the server's stop ends it
func TestFollowPlayIsTheScreensAloneAndStops(t *testing.T) {
	r := newRig(t, screen.Options{})
	s := r.connect(t, screentest.Options{})
	video := r.caps[capschema.MediaVideoID]
	out, err := command(t, video, "play", `{"url":"https://example.test/v.m3u8","follow":{"groupId":"`+followGroup+`"}}`)
	if err != nil {
		t.Fatal(err)
	}
	var played struct{ SessionID string }
	_ = json.Unmarshal(out, &played)
	mp := frame(t, s, func(e *screenv1.Envelope) bool { return e.GetMediaPlay() != nil }).GetMediaPlay()
	if mp.GetFollowGroupId() != followGroup || mp.GetSessionId() != played.SessionID {
		t.Fatalf("play = %v", mp)
	}
	state(t, video, `"follow":{"groupId":"`+followGroup+`"}`)
	select {
	case got := <-r.media.Sessions():
		if got.GetSessionId() == played.SessionID {
			t.Fatalf("a follower's video became the device's session: %v", got)
		}
	case <-time.After(200 * time.Millisecond):
	}
	// another session's stop is left alone
	if _, err := command(t, video, "stop", `{"sessionId":"video-0"}`); err != nil {
		t.Fatal(err)
	}
	if id, _ := s.State(); id != played.SessionID {
		t.Fatalf("stopped another session: %q", id)
	}
	if _, err := command(t, video, "stop", `{"sessionId":"`+played.SessionID+`"}`); err != nil {
		t.Fatal(err)
	}
	if id, _ := s.State(); id != "" {
		t.Fatal("not stopped")
	}
	state(t, video, `{"tracks":[]}`)
}

// a play of the screen's own says no group
func TestOwnPlayFollowsNoGroup(t *testing.T) {
	r := newRig(t, screen.Options{})
	s := r.connect(t, screentest.Options{})
	if _, err := command(t, r.caps[capschema.MediaVideoID], "play", `{"url":"https://example.test/v.m3u8"}`); err != nil {
		t.Fatal(err)
	}
	if mp := frame(t, s, func(e *screenv1.Envelope) bool { return e.GetMediaPlay() != nil }).GetMediaPlay(); mp.GetFollowGroupId() != "" {
		t.Fatalf("play = %v", mp)
	}
	state(t, r.caps[capschema.MediaVideoID], `"tracks":[{"id":"a1"`)
}

// a follower's offered transport is the server's: the fields it sends for the follower's session (its leader's
// next and previous) reach the screen, and a screen that connects later gets them again; the follower's play
// itself offers none of its own
func TestAFollowersServerFieldsReachTheScreen(t *testing.T) {
	r := newRig(t, screen.Options{})
	s := r.connect(t, screentest.Options{})
	out, err := command(t, r.caps[capschema.MediaVideoID], "play", `{"url":"https://example.test/v.m3u8","follow":{"groupId":"`+followGroup+`"}}`)
	if err != nil {
		t.Fatal(err)
	}
	var played struct{ SessionID string }
	_ = json.Unmarshal(out, &played)
	mp := frame(t, s, func(e *screenv1.Envelope) bool { return e.GetMediaPlay() != nil }).GetMediaPlay()
	if len(mp.GetActions()) != 0 {
		t.Fatalf("a follower's play offers %v", mp.GetActions())
	}
	st := &cadencev2.MediaSessionState{SessionId: played.SessionID, Actions: []string{"next", "previous"},
		UpNext: []*cadencev2.MediaQueueItem{{Ref: "ce:4", Title: "Next"}}}
	if !r.srv.LinkMessage(&cadencev2.Envelope{Body: &cadencev2.Envelope_MediaSessionState{MediaSessionState: st}}) {
		t.Fatal("fields not taken")
	}
	got := frame(t, s, func(e *screenv1.Envelope) bool { return e.GetMediaSessionState() != nil }).GetMediaSessionState()
	if got.GetSessionId() != played.SessionID || len(got.GetActions()) != 2 || got.GetActions()[0] != "next" {
		t.Fatalf("fields = %v", got)
	}
	s2 := r.connect(t, screentest.Options{})
	frame(t, s2, func(e *screenv1.Envelope) bool { return e.GetMediaSessionState().GetSessionId() == played.SessionID })
}
