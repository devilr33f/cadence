package media_test

import (
	"context"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"git.shino.boo/devilreef/cadence/device/internal/media"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

type call struct {
	id, action, reason string
	position           uint64
}

type fakePlayer struct {
	calls    chan call
	controls chan *audiodv1.RouteControl
}

func newPlayer() *fakePlayer {
	return &fakePlayer{calls: make(chan call, 8), controls: make(chan *audiodv1.RouteControl, 8)}
}

func (p *fakePlayer) ExternalCommand(_ context.Context, id, action string, position uint64, reason string) error {
	p.calls <- call{id, action, reason, position}
	return nil
}

func (p *fakePlayer) RouteControl(rc *audiodv1.RouteControl) { p.controls <- rc }

func video(id string, state cadencev2.PlaybackState, route cadencev2.AudioRoute) media.External {
	return media.External{State: &cadencev2.MediaSessionState{
		SessionId: id, Source: cadencev2.MediaSource_MEDIA_SOURCE_VIDEO, State: state, PositionMs: 1234,
		AudioRoute: route, AudioControls: []cadencev2.AudioControl{cadencev2.AudioControl_AUDIO_CONTROL_DUCK},
		Metadata: &cadencev2.MediaMetadata{Title: "t"},
	}}
}

func TestExternalRegisteredWithAudiodAndAgainAfterReady(t *testing.T) {
	r := newRig(t)
	r.up(t)
	r.m.UpdateExternal(video("v1", cadencev2.PlaybackState_PLAYBACK_STATE_PLAYING, cadencev2.AudioRoute_AUDIO_ROUTE_DIRECT))
	es := r.next(t).GetExternalSource()
	want := &audiodv1.ExternalSource{
		Id: "v1", Op: audiodv1.ExternalOp_EXTERNAL_OP_UPDATE, Kind: audiodv1.MediaSourceKind_MEDIA_SOURCE_KIND_VIDEO,
		State: audiodv1.PlaybackState_PLAYBACK_STATE_PLAYING, AudioRoute: audiodv1.AudioRoute_AUDIO_ROUTE_DIRECT,
		AudioControls: []audiodv1.AudioControl{audiodv1.AudioControl_AUDIO_CONTROL_DUCK}, PositionMs: 1234,
	}
	if !proto.Equal(es, want) {
		t.Fatalf("external = %v", es)
	}
	// a new audiod forgot it
	r.m.MediaGone()
	r.ready()
	if r.next(t).GetMediaSettings() == nil {
		t.Fatal("settings first")
	}
	if es := r.next(t).GetExternalSource(); es.GetId() != "v1" {
		t.Fatalf("not registered again: %v", es)
	}
	// idle unregisters it
	r.m.UpdateExternal(video("v1", cadencev2.PlaybackState_PLAYBACK_STATE_IDLE, cadencev2.AudioRoute_AUDIO_ROUTE_DIRECT))
	if es := r.next(t).GetExternalSource(); es.GetOp() != audiodv1.ExternalOp_EXTERNAL_OP_UNREGISTER {
		t.Fatalf("not unregistered: %v", es)
	}
}

// a follower screen's video (multiroom P4) never reaches audiod, where it would take the speaker from the group's
// session that carries its sound, nor becomes the device's session
func TestFollowerVideoIsNoExternal(t *testing.T) {
	r := newRig(t)
	r.up(t)
	follow := video("v2", cadencev2.PlaybackState_PLAYBACK_STATE_PLAYING, cadencev2.AudioRoute_AUDIO_ROUTE_NONE)
	follow.State.Follow = true
	r.m.UpdateExternal(follow)
	r.m.UpdateExternal(video("v1", cadencev2.PlaybackState_PLAYBACK_STATE_PLAYING, cadencev2.AudioRoute_AUDIO_ROUTE_DIRECT))
	if es := r.next(t).GetExternalSource(); es.GetId() != "v1" {
		t.Fatalf("first to audiod = %v, want v1 alone", es)
	}
}

// audiod names the session; the server gets the player's own details under it
func TestExternalSessionStateIsThePlayers(t *testing.T) {
	r := newRig(t)
	p := newPlayer()
	r.m.UseExternalPlayer(p)
	r.up(t)
	r.m.UpdateExternal(video("v1", cadencev2.PlaybackState_PLAYBACK_STATE_PLAYING, cadencev2.AudioRoute_AUDIO_ROUTE_DIRECT))
	r.next(t)
	r.mediaState(&audiodv1.MediaState{
		SessionId: "v1", Source: audiodv1.MediaSourceKind_MEDIA_SOURCE_KIND_VIDEO,
		State: audiodv1.PlaybackState_PLAYBACK_STATE_PLAYING,
	})
	s := session(t, r.m)
	if s.GetSessionId() != "v1" || s.GetPositionMs() != 1234 || s.GetMetadata().GetTitle() != "t" ||
		s.GetAudioRoute() != cadencev2.AudioRoute_AUDIO_ROUTE_DIRECT {
		t.Fatalf("session = %v", s)
	}
	// its transport is the player's, with a seek's position, never audiod's
	if err := wait(t, r.command("media.player", "seek", `{"positionMs":5000}`)); err != nil {
		t.Fatal(err)
	}
	if c := <-p.calls; c != (call{"v1", "seek", "", 5000}) {
		t.Fatalf("call = %v", c)
	}
	r.none(t)
	// a later change of the player's reaches the server while it holds the speaker
	r.m.UpdateExternal(video("v1", cadencev2.PlaybackState_PLAYBACK_STATE_PAUSED, cadencev2.AudioRoute_AUDIO_ROUTE_DIRECT))
	r.next(t)
	if s := session(t, r.m); s.GetState() != cadencev2.PlaybackState_PLAYBACK_STATE_PAUSED {
		t.Fatalf("session = %v", s)
	}
	// audiod's own source takes over: commands go to audiod again
	r.mediaState(&audiodv1.MediaState{SessionId: "ms-1", Source: audiodv1.MediaSourceKind_MEDIA_SOURCE_KIND_URL,
		State: audiodv1.PlaybackState_PLAYBACK_STATE_PLAYING})
	done := r.command("media.player", "pause", `{}`)
	if r.next(t).GetMediaCommand() == nil {
		t.Fatal("pause not sent to audiod")
	}
	_ = done
}

func TestExternalActionReachesThePlayer(t *testing.T) {
	r := newRig(t)
	p := newPlayer()
	r.m.UseExternalPlayer(p)
	r.up(t)
	r.m.MediaEnvelope(&audiodv1.Envelope{Body: &audiodv1.Envelope_ExternalAction{ExternalAction: &audiodv1.ExternalAction{
		Id: "v1", Action: audiodv1.MediaAction_MEDIA_ACTION_PAUSE, Reason: "displaced",
	}}})
	select {
	case c := <-p.calls:
		if c != (call{"v1", "pause", "displaced", 0}) {
			t.Fatalf("call = %v", c)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no call")
	}
	// a seek carries its position through audiod
	r.m.MediaEnvelope(&audiodv1.Envelope{Body: &audiodv1.Envelope_ExternalAction{ExternalAction: &audiodv1.ExternalAction{
		Id: "v1", Action: audiodv1.MediaAction_MEDIA_ACTION_SEEK, PositionMs: 754_000,
	}}})
	select {
	case c := <-p.calls:
		if c != (call{"v1", "seek", "", 754_000}) {
			t.Fatalf("call = %v", c)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no seek")
	}
	rc := &audiodv1.RouteControl{SessionId: "v1", Control: &audiodv1.RouteControl_Mute{Mute: &audiodv1.Mute{Muted: true}}}
	r.m.MediaEnvelope(&audiodv1.Envelope{Body: &audiodv1.Envelope_RouteControl{RouteControl: rc}})
	if got := <-p.controls; !proto.Equal(got, rc) {
		t.Fatalf("control = %v", got)
	}
}

// rule 7: a duck never raises a level
func TestServerDuckGoesToAudiodClampedAndAgainAfterReady(t *testing.T) {
	r := newRig(t)
	r.up(t)
	r.m.ServerDuck(&cadencev2.Duck{Active: true, Level: 3, RampMs: 200, Reason: cadencev2.DuckReason_DUCK_REASON_ASSISTANT})
	d := r.next(t).GetSessionControl().GetDuck()
	if !d.GetActive() || d.GetLevel() != 1 || d.GetRampMs() != 200 || d.GetReason() != audiodv1.DuckReason_DUCK_REASON_ASSISTANT {
		t.Fatalf("duck = %v", d)
	}
	r.m.MediaGone()
	r.ready()
	r.next(t)
	if d := r.next(t).GetSessionControl().GetDuck(); !d.GetActive() {
		t.Fatalf("duck not sent again: %v", d)
	}
	r.m.ServerDuck(&cadencev2.Duck{Active: false})
	r.next(t)
	r.m.MediaGone()
	r.ready()
	r.next(t)
	r.none(t)
}

func TestServerDuckWithoutAudiodGoesToADirectPlayer(t *testing.T) {
	r := newRig(t)
	p := newPlayer()
	r.m.UseExternalPlayer(p)
	r.m.UpdateExternal(video("v1", cadencev2.PlaybackState_PLAYBACK_STATE_PLAYING, cadencev2.AudioRoute_AUDIO_ROUTE_DIRECT))
	if s := session(t, r.m); s.GetSessionId() != "v1" {
		t.Fatalf("session = %v", s)
	}
	r.m.ServerDuck(&cadencev2.Duck{Active: true, Level: 0.2})
	if rc := <-p.controls; rc.GetSessionId() != "v1" || rc.GetDuck().GetLevel() != float32(0.2) {
		t.Fatalf("control = %v", rc)
	}
}

func TestGateFollowsTheLevelAudiodHolds(t *testing.T) {
	r := newRig(t)
	gates := make(chan bool, 4)
	r.m.OnGate(func(open bool) { gates <- open })
	r.up(t)
	applied := func(ok, transient bool) {
		r.m.MediaEnvelope(&audiodv1.Envelope{Body: &audiodv1.Envelope_VolumeApplied{VolumeApplied: &audiodv1.VolumeApplied{
			Level: 40, Ok: ok, Transient: transient,
		}}})
	}
	if r.m.GateOpen() {
		t.Fatal("open before a level held")
	}
	applied(true, false)
	if !<-gates || !r.m.GateOpen() {
		t.Fatal("not open")
	}
	applied(false, true)
	if !r.m.GateOpen() {
		t.Fatal("a refused ramp step shut it")
	}
	applied(false, false)
	if <-gates {
		t.Fatal("not shut")
	}
	applied(true, false)
	<-gates
	r.m.MediaGone()
	if <-gates {
		t.Fatal("open without audiod")
	}
}

func TestTapConfigSentNowAndAfterEveryReady(t *testing.T) {
	r := newRig(t)
	r.up(t)
	r.m.UseTap(&audiodv1.TapConfig{Path: "@cadence-tap", Pcm: true})
	if tc := r.next(t).GetTapConfig(); tc.GetPath() != "@cadence-tap" || !tc.GetPcm() {
		t.Fatalf("tap = %v", tc)
	}
	r.m.MediaGone()
	r.ready()
	r.next(t)
	if tc := r.next(t).GetTapConfig(); tc.GetPath() != "@cadence-tap" {
		t.Fatalf("tap = %v", tc)
	}
}
