package screen_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/screen"
	"git.shino.boo/devilreef/cadence/device/internal/screen/screentest"
	"git.shino.boo/devilreef/cadence/pkg/capschema"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
	screenv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/screen/v1"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

// audiod as cadenced's media plane sees it: what cadenced sends it, and its envelopes handed in by the test
type fakeAudiod struct{ sent chan *audiodv1.Envelope }

func (a *fakeAudiod) send(env *audiodv1.Envelope) error {
	a.sent <- env
	return nil
}

func (a *fakeAudiod) next(t *testing.T, match func(*audiodv1.Envelope) bool) *audiodv1.Envelope {
	t.Helper()
	deadline := time.After(wait)
	for {
		select {
		case env := <-a.sent:
			if match(env) {
				return env
			}
		case <-deadline:
			t.Fatal("audiod got nothing that matched")
			return nil
		}
	}
}

func castState(state audiodv1.PlaybackState, artwork string) *audiodv1.Envelope {
	return &audiodv1.Envelope{Body: &audiodv1.Envelope_MediaState{MediaState: &audiodv1.MediaState{
		SessionId: "ms-1", Source: audiodv1.MediaSourceKind_MEDIA_SOURCE_KIND_AIRPLAY, State: state,
		PositionMs: 94000, PositionAtMs: time.Now().UnixMilli(), DurationMs: 123000,
		Metadata: &audiodv1.MediaMetadata{Title: "worms", Artist: "Северные огни", Album: "worms", ArtworkId: artwork, Sender: "iPhone Маши"},
		Actions: []audiodv1.MediaAction{
			audiodv1.MediaAction_MEDIA_ACTION_PAUSE, audiodv1.MediaAction_MEDIA_ACTION_RESUME, audiodv1.MediaAction_MEDIA_ACTION_STOP,
		},
	}}}
}

// an airplay cast over the screen's video: audiod's arbitration asks the screen's player to pause (displaced), the
// device's session is the cast, and the screen is told of it with its sender, its cover (a file it reads) and its
// colours; its controls go to audiod, and its end leaves the screen no session
func TestACastDisplacesTheVideoAndShowsOnTheScreen(t *testing.T) {
	covers := t.TempDir()
	r := newRig(t, screen.Options{CoverDir: filepath.Join(covers, "screen")})
	jpeg, err := os.ReadFile("../../../screen/sim/examples/cover-dusk.jpg")
	if err != nil {
		t.Fatal(err)
	}
	art := filepath.Join(covers, "artwork")
	if err := os.WriteFile(art, jpeg, 0o600); err != nil {
		t.Fatal(err)
	}
	r.media.UseArtwork(art, nil)
	a := &fakeAudiod{sent: make(chan *audiodv1.Envelope, 64)}
	r.media.MediaReady(a.send)
	s := r.connect(t, screentest.Options{})
	// no cast yet: the screen has none
	if np := frame(t, s, func(e *screenv1.Envelope) bool { return e.GetNowPlaying() != nil }).GetNowPlaying(); np.GetSessionId() != "" {
		t.Fatalf("now playing at connect %v", np)
	}

	// the screen plays a film, registered with audiod as an external session
	out, err := command(t, r.caps[capschema.MediaVideoID], "play", `{"url":"https://example.test/v.m3u8","metadata":{"title":"T"}}`)
	if err != nil {
		t.Fatal(err)
	}
	var played struct{ SessionID string }
	_ = json.Unmarshal(out, &played)
	a.next(t, func(e *audiodv1.Envelope) bool { return e.GetExternalSource().GetId() == played.SessionID })

	// the cast plays: audiod sets the film aside, which pauses the screen's player
	r.media.MediaEnvelope(&audiodv1.Envelope{Body: &audiodv1.Envelope_ExternalAction{ExternalAction: &audiodv1.ExternalAction{
		Id: played.SessionID, Action: audiodv1.MediaAction_MEDIA_ACTION_PAUSE, Reason: "displaced",
	}}})
	r.media.MediaEnvelope(castState(audiodv1.PlaybackState_PLAYBACK_STATE_PLAYING, "a1"))
	if mc := frame(t, s, func(e *screenv1.Envelope) bool { return e.GetMediaCommand() != nil }).GetMediaCommand(); mc.GetAction() != screenv1.MediaAction_MEDIA_ACTION_PAUSE {
		t.Fatalf("the player got %v", mc)
	}
	np := frame(t, s, func(e *screenv1.Envelope) bool { return e.GetNowPlaying().GetSessionId() == "ms-1" }).GetNowPlaying()
	if np.GetSource() != cadencev2.MediaSource_MEDIA_SOURCE_AIRPLAY || np.GetState() != cadencev2.PlaybackState_PLAYBACK_STATE_PLAYING ||
		np.GetTitle() != "worms" || np.GetArtist() != "Северные огни" || np.GetSender() != "iPhone Маши" ||
		np.GetDurationMs() != 123000 || len(np.GetActions()) != 3 {
		t.Fatalf("now playing %v", np)
	}
	// the server's session is the cast, never with the sender's name
	var cur *cadencev2.MediaSessionState
	deadline := time.After(wait)
	for cur.GetSource() != cadencev2.MediaSource_MEDIA_SOURCE_AIRPLAY {
		select {
		case cur = <-r.media.Sessions():
		case <-deadline:
			t.Fatal("the cast is not the device's session")
		}
	}
	if b, _ := json.Marshal(cur); bytes.Contains(b, []byte("iPhone")) {
		t.Fatal("the sender's name went to the server")
	}

	// the cover, once read, is a file the screen reads, with its tint
	np = frame(t, s, func(e *screenv1.Envelope) bool { return e.GetNowPlaying().GetArtworkPath() != "" }).GetNowPlaying()
	got, err := os.ReadFile(np.GetArtworkPath())
	if err != nil || !bytes.Equal(got, jpeg) || len(np.GetArtworkSha256()) != 64 {
		t.Fatalf("cover %q: %v", np.GetArtworkPath(), err)
	}
	if fi, err := os.Stat(np.GetArtworkPath()); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("cover mode %v", fi.Mode())
	}
	if np.GetColorA() == 0 || np.GetColorB() == 0 || np.GetColorA() == np.GetColorB() {
		t.Fatalf("colours %06x %06x", np.GetColorA(), np.GetColorB())
	}
	t.Logf("cover sha256 %s colours %d %d", np.GetArtworkSha256(), np.GetColorA(), np.GetColorB())

	// the screen's pause goes to audiod as media.player's; one for another session is dropped
	if err := s.Send(&screenv1.Envelope{Body: &screenv1.Envelope_NowPlayingCommand{NowPlayingCommand: &screenv1.NowPlayingCommand{
		SessionId: "ms-0", Action: screenv1.MediaAction_MEDIA_ACTION_STOP,
	}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Send(&screenv1.Envelope{Body: &screenv1.Envelope_NowPlayingCommand{NowPlayingCommand: &screenv1.NowPlayingCommand{
		SessionId: "ms-1", Action: screenv1.MediaAction_MEDIA_ACTION_PAUSE,
	}}}); err != nil {
		t.Fatal(err)
	}
	mc := a.next(t, func(e *audiodv1.Envelope) bool { return e.GetMediaCommand() != nil }).GetMediaCommand()
	if mc.GetAction() != audiodv1.MediaAction_MEDIA_ACTION_PAUSE {
		t.Fatalf("audiod got %v (the stale stop went through?)", mc)
	}
	r.media.MediaEnvelope(&audiodv1.Envelope{Body: &audiodv1.Envelope_CommandResult{CommandResult: &audiodv1.CommandResult{
		RequestId: mc.GetRequestId(), Ok: true,
	}}})
	r.media.MediaEnvelope(castState(audiodv1.PlaybackState_PLAYBACK_STATE_PAUSED, "a1"))
	frame(t, s, func(e *screenv1.Envelope) bool {
		return e.GetNowPlaying().GetState() == cadencev2.PlaybackState_PLAYBACK_STATE_PAUSED
	})

	// a screen that reconnects gets the cast as it stands
	s2 := r.connect(t, screentest.Options{})
	if np := frame(t, s2, func(e *screenv1.Envelope) bool { return e.GetNowPlaying() != nil }).GetNowPlaying(); np.GetSessionId() != "ms-1" || np.GetArtworkPath() == "" {
		t.Fatalf("now playing at reconnect %v", np)
	}

	// the sender went: no session, nothing resumes the film
	r.media.MediaEnvelope(&audiodv1.Envelope{Body: &audiodv1.Envelope_MediaState{MediaState: &audiodv1.MediaState{}}})
	frame(t, s2, func(e *screenv1.Envelope) bool {
		return e.GetNowPlaying() != nil && e.GetNowPlaying().GetSessionId() == ""
	})
	for _, f := range s2.Frames() {
		if mc := f.GetMediaCommand(); mc != nil && mc.GetAction() == screenv1.MediaAction_MEDIA_ACTION_RESUME {
			t.Fatal("the film was resumed")
		}
	}
}

// a screen that joined a multiroom group shows the group's session (the leader's, played here) in audio mode, and its
// transport goes to audiod as media.player's, whose group source hands it to the leader
func TestAGroupSessionShowsOnTheScreenAndItsTransportGoesToAudiod(t *testing.T) {
	r := newRig(t, screen.Options{})
	a := &fakeAudiod{sent: make(chan *audiodv1.Envelope, 64)}
	r.media.MediaReady(a.send)
	s := r.connect(t, screentest.Options{})
	r.media.MediaEnvelope(&audiodv1.Envelope{Body: &audiodv1.Envelope_MediaState{MediaState: &audiodv1.MediaState{
		SessionId: "grp-1", Source: audiodv1.MediaSourceKind_MEDIA_SOURCE_KIND_GROUP,
		State: audiodv1.PlaybackState_PLAYBACK_STATE_PLAYING, PositionMs: 1000, PositionAtMs: time.Now().UnixMilli(),
		Metadata: &audiodv1.MediaMetadata{Title: "t", Artist: "a"},
		Actions: []audiodv1.MediaAction{
			audiodv1.MediaAction_MEDIA_ACTION_PAUSE, audiodv1.MediaAction_MEDIA_ACTION_NEXT,
		},
	}}})
	np := frame(t, s, func(e *screenv1.Envelope) bool { return e.GetNowPlaying().GetSessionId() == "grp-1" }).GetNowPlaying()
	if np.GetSource() != cadencev2.MediaSource_MEDIA_SOURCE_GROUP || np.GetTitle() != "t" || len(np.GetActions()) != 2 {
		t.Fatalf("now playing %v", np)
	}
	if err := s.Send(&screenv1.Envelope{Body: &screenv1.Envelope_NowPlayingCommand{NowPlayingCommand: &screenv1.NowPlayingCommand{
		SessionId: "grp-1", Action: screenv1.MediaAction_MEDIA_ACTION_NEXT,
	}}}); err != nil {
		t.Fatal(err)
	}
	mc := a.next(t, func(e *audiodv1.Envelope) bool { return e.GetMediaCommand() != nil }).GetMediaCommand()
	if mc.GetAction() != audiodv1.MediaAction_MEDIA_ACTION_NEXT {
		t.Fatalf("audiod got %v", mc)
	}
	r.media.MediaEnvelope(&audiodv1.Envelope{Body: &audiodv1.Envelope_CommandResult{CommandResult: &audiodv1.CommandResult{
		RequestId: mc.GetRequestId(), Ok: true,
	}}})
}
