package core_test

import (
	"strings"
	"testing"

	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

type mediaBoard struct {
	fakeBoard
	sessions chan *cadencev2.MediaSessionState
}

func (b mediaBoard) MediaSessions() <-chan *cadencev2.MediaSessionState { return b.sessions }

func mediaState(id string, st cadencev2.PlaybackState, title string) *cadencev2.MediaSessionState {
	return &cadencev2.MediaSessionState{
		SessionId: id, Source: cadencev2.MediaSource_MEDIA_SOURCE_URL, State: st, Rate: 1,
		Metadata: &cadencev2.MediaMetadata{Title: title}, Actions: []string{"pause", "stop"},
	}
}

func (p *peer) mediaState(want *cadencev2.MediaSessionState) {
	p.t.Helper()
	env := p.recv()
	if got := env.GetMediaSessionState(); !proto.Equal(got, want) {
		p.t.Fatalf("frame = %v, want media state %v", env, want)
	}
}

func TestMediaSessionStateForwarded(t *testing.T) {
	ch := make(chan *cadencev2.MediaSessionState, 1)
	first, second := mediaState("ms-1", cadencev2.PlaybackState_PLAYBACK_STATE_PLAYING, "SECRET-ONE"),
		mediaState("ms-1", cadencev2.PlaybackState_PLAYBACK_STATE_PAUSED, "SECRET-TWO")
	ch <- first
	ls, buf := startSync(t, mediaBoard{fakeBoard{id: "fake"}, ch}, openStore(t))
	p := ls.accept(t)
	p.handshake(false)
	p.mediaState(first)
	ch <- second
	p.mediaState(second)

	// the server forgets it on a disconnect: a new link gets the latest once
	_ = p.c.Close(websocket.StatusGoingAway, "restart")
	p = ls.accept(t)
	p.handshake(false)
	p.mediaState(second)
	if a := p.configSet("c.unknown", 1, `{}`); a.GetCapability() != "c.unknown" {
		t.Fatalf("applied = %v", a)
	}
	if out := buf.String(); strings.Contains(out, "SECRET") {
		t.Fatalf("log carries media metadata: %s", out)
	}
}
