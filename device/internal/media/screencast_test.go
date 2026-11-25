package media_test

import (
	"context"
	"testing"

	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
)

func (r *rig) stopScreencast() <-chan error {
	done := make(chan error, 1)
	go func() { done <- r.m.StopScreencast(context.Background()) }()
	return done
}

func (r *rig) session(id string, source audiodv1.MediaSourceKind, state audiodv1.PlaybackState) {
	r.m.MediaEnvelope(&audiodv1.Envelope{Body: &audiodv1.Envelope_MediaState{MediaState: &audiodv1.MediaState{
		SessionId: id, Source: source, State: state,
	}}})
}

// the screen's stop key stops audiod's session only while a mirror holds it: never the music that took its place
func TestStopScreencastStopsOnlyAMirrorSession(t *testing.T) {
	r := newRig(t)
	r.up(t)
	if c := code(wait(t, r.stopScreencast())); c != "no_session" {
		t.Fatalf("no session: %q", c)
	}
	r.session("ms-1", audiodv1.MediaSourceKind_MEDIA_SOURCE_KIND_URL, audiodv1.PlaybackState_PLAYBACK_STATE_PLAYING)
	if c := code(wait(t, r.stopScreencast())); c != "no_session" {
		t.Fatalf("music: %q", c)
	}
	r.none(t)

	r.session("ms-2", audiodv1.MediaSourceKind_MEDIA_SOURCE_KIND_SCREENCAST, audiodv1.PlaybackState_PLAYBACK_STATE_PLAYING)
	done := r.stopScreencast()
	mc := r.next(t).GetMediaCommand()
	if mc.GetAction() != audiodv1.MediaAction_MEDIA_ACTION_STOP || !requestID.MatchString(mc.GetRequestId()) {
		t.Fatalf("sent %v", mc)
	}
	r.answer(mc.GetRequestId(), true, "")
	if err := wait(t, done); err != nil {
		t.Fatal(err)
	}

	r.session("", audiodv1.MediaSourceKind_MEDIA_SOURCE_KIND_UNSPECIFIED, audiodv1.PlaybackState_PLAYBACK_STATE_IDLE)
	if c := code(wait(t, r.stopScreencast())); c != "no_session" {
		t.Fatalf("after its end: %q", c)
	}
	r.none(t)
}
