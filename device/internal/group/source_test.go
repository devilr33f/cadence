package group_test

import (
	"encoding/json"
	"strings"
	"testing"

	"git.shino.boo/devilreef/cadence/device/internal/group"
	"git.shino.boo/devilreef/cadence/pkg/capschema"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

func TestLocalSourceFromSessionKinds(t *testing.T) {
	const (
		playing   = cadencev2.PlaybackState_PLAYBACK_STATE_PLAYING
		buffering = cadencev2.PlaybackState_PLAYBACK_STATE_BUFFERING
		paused    = cadencev2.PlaybackState_PLAYBACK_STATE_PAUSED
		idle      = cadencev2.PlaybackState_PLAYBACK_STATE_IDLE
		none      = cadencev2.AudioRoute_AUDIO_ROUTE_UNSPECIFIED
		mixer     = cadencev2.AudioRoute_AUDIO_ROUTE_MIXER
		direct    = cadencev2.AudioRoute_AUDIO_ROUTE_DIRECT
		external  = cadencev2.AudioRoute_AUDIO_ROUTE_EXTERNAL
		remote    = cadencev2.AudioRoute_AUDIO_ROUTE_REMOTE
		mute      = cadencev2.AudioRoute_AUDIO_ROUTE_NONE
	)
	cases := []struct {
		name   string
		source cadencev2.MediaSource
		state  cadencev2.PlaybackState
		route  cadencev2.AudioRoute
		want   bool
	}{
		{"url playing", cadencev2.MediaSource_MEDIA_SOURCE_URL, playing, none, true},
		{"url buffering", cadencev2.MediaSource_MEDIA_SOURCE_URL, buffering, none, true},
		{"url paused", cadencev2.MediaSource_MEDIA_SOURCE_URL, paused, none, false},
		{"url idle", cadencev2.MediaSource_MEDIA_SOURCE_URL, idle, none, false},
		{"stream", cadencev2.MediaSource_MEDIA_SOURCE_STREAM, playing, none, true},
		{"soloist", cadencev2.MediaSource_MEDIA_SOURCE_SOLOIST, playing, none, true},
		{"airplay", cadencev2.MediaSource_MEDIA_SOURCE_AIRPLAY, playing, mixer, true},
		{"video on the mixer", cadencev2.MediaSource_MEDIA_SOURCE_VIDEO, playing, mixer, true},
		{"video direct", cadencev2.MediaSource_MEDIA_SOURCE_VIDEO, playing, direct, false},
		{"video muted", cadencev2.MediaSource_MEDIA_SOURCE_VIDEO, playing, mute, false},
		{"video with no route", cadencev2.MediaSource_MEDIA_SOURCE_VIDEO, playing, none, false},
		{"screencast on the mixer", cadencev2.MediaSource_MEDIA_SOURCE_SCREENCAST, playing, mixer, true},
		{"external on the mixer", cadencev2.MediaSource_MEDIA_SOURCE_EXTERNAL, playing, mixer, true},
		{"another app plays", cadencev2.MediaSource_MEDIA_SOURCE_EXTERNAL, playing, external, false},
		{"a handoff", cadencev2.MediaSource_MEDIA_SOURCE_EXTERNAL, playing, remote, false},
		{"url direct", cadencev2.MediaSource_MEDIA_SOURCE_URL, playing, direct, false},
		{"the group's own stream", cadencev2.MediaSource_MEDIA_SOURCE_GROUP, playing, none, false},
		{"bluetooth", cadencev2.MediaSource_MEDIA_SOURCE_BLUETOOTH, playing, none, false},
		{"nothing", cadencev2.MediaSource_MEDIA_SOURCE_UNSPECIFIED, playing, none, false},
		{"a follower's video", cadencev2.MediaSource_MEDIA_SOURCE_VIDEO, playing, mixer, false},
		{"a follower's silent video", cadencev2.MediaSource_MEDIA_SOURCE_VIDEO, playing, mute, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := &cadencev2.MediaSessionState{Source: c.source, State: c.state, AudioRoute: c.route,
				Follow: strings.HasPrefix(c.name, "a follower's")}
			if got := group.LocalSource(s); got != c.want {
				t.Fatalf("LocalSource = %v, want %v", got, c.want)
			}
		})
	}
	if group.LocalSource(nil) {
		t.Fatal("no session is a source")
	}
}

func TestLocalSourceInState(t *testing.T) {
	schema := compile(t, capschema.MediaGroupState)
	r := newRig(t)
	r.state(t)
	read := func() capschema.MediaGroupStatus {
		t.Helper()
		got := r.state(t)
		validate(t, schema, got)
		var st capschema.MediaGroupStatus
		if err := json.Unmarshal([]byte(got), &st); err != nil {
			t.Fatal(err)
		}
		return st
	}
	cast := &cadencev2.MediaSessionState{Source: cadencev2.MediaSource_MEDIA_SOURCE_AIRPLAY, State: cadencev2.PlaybackState_PLAYBACK_STATE_PLAYING}
	// out of any group the state stays off
	r.g.MediaSession(cast)
	if err := r.apply(t, memberCfg, secret); err != nil {
		t.Fatal(err)
	}
	if st := read(); st.State != "connecting" || !st.LocalSource {
		t.Fatalf("a member casting, before audiod = %+v", st)
	}
	r.g.GroupReady(r.send)
	r.g.GroupEnvelope(&audiodv1.Envelope{Body: &audiodv1.Envelope_GroupStatus{GroupStatus: &audiodv1.GroupStatus{
		GroupId: "g-1", Role: audiodv1.GroupRole_GROUP_ROLE_MEMBER, State: audiodv1.GroupState_GROUP_STATE_IDLE,
	}}})
	if st := read(); st.State != "idle" || !st.LocalSource {
		t.Fatalf("a member casting = %+v", st)
	}
	// the group's stream displaced the cast
	r.g.MediaSession(&cadencev2.MediaSessionState{Source: cadencev2.MediaSource_MEDIA_SOURCE_GROUP, State: cadencev2.PlaybackState_PLAYBACK_STATE_PLAYING})
	if st := read(); st.LocalSource {
		t.Fatalf("playing the group's stream = %+v", st)
	}
	// a passthrough film plays here alone
	r.g.MediaSession(&cadencev2.MediaSessionState{Source: cadencev2.MediaSource_MEDIA_SOURCE_VIDEO, State: cadencev2.PlaybackState_PLAYBACK_STATE_PLAYING,
		AudioRoute: cadencev2.AudioRoute_AUDIO_ROUTE_DIRECT})
	select {
	case v := <-r.cap.State():
		t.Fatalf("a direct session changed the state: %s", v)
	default:
	}
	if !strings.Contains(r.log.String(), `"msg":"group_source_local","reason":"direct"`) {
		t.Fatalf("log: %s", r.log.String())
	}
	r.g.MediaSession(&cadencev2.MediaSessionState{Source: cadencev2.MediaSource_MEDIA_SOURCE_VIDEO, State: cadencev2.PlaybackState_PLAYBACK_STATE_PLAYING,
		AudioRoute: cadencev2.AudioRoute_AUDIO_ROUTE_MIXER})
	if st := read(); !st.LocalSource {
		t.Fatalf("a film on the mixer = %+v", st)
	}
	r.g.MediaSession(&cadencev2.MediaSessionState{State: cadencev2.PlaybackState_PLAYBACK_STATE_IDLE})
	if st := read(); st.LocalSource {
		t.Fatalf("idle = %+v", st)
	}
}
