package screen_test

import (
	"sync"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/screen"
	"git.shino.boo/devilreef/cadence/device/internal/screen/screentest"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
	screenv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/screen/v1"
)

type audiodFake struct {
	mu        sync.Mutex
	infos     []*audiodv1.HlsPlaybackInfo
	externals []*audiodv1.ExternalSource
}

func (a *audiodFake) send(env *audiodv1.Envelope) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if i := env.GetHlsPlaybackInfo(); i != nil {
		a.infos = append(a.infos, i)
	}
	if e := env.GetExternalSource(); e != nil {
		a.externals = append(a.externals, e)
	}
	return nil
}

func (a *audiodFake) registered(id string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, e := range a.externals {
		if e.GetId() == id && e.GetOp() == audiodv1.ExternalOp_EXTERNAL_OP_UPDATE {
			return true
		}
	}
	return false
}

func (a *audiodFake) wait(t *testing.T, match func(*audiodv1.HlsPlaybackInfo) bool) *audiodv1.HlsPlaybackInfo {
	t.Helper()
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		a.mu.Lock()
		for _, i := range a.infos {
			if match(i) {
				a.mu.Unlock()
				return i
			}
		}
		a.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("no playback info")
	return nil
}

func cast(r *rig, c *audiodv1.HlsCast) {
	r.media.MediaEnvelope(&audiodv1.Envelope{Body: &audiodv1.Envelope_HlsCast{HlsCast: c}})
}

// an airplay video cast plays on the screen as a session cadenced owns: its start fraction a seek once the duration
// is known, the sender's scrub, rate and stop the player's transport, and the player's state the sender's playback
// info, ended at the end
func TestHlsCastPlaysAsAVideoSession(t *testing.T) {
	r := newRig(t, screen.Options{})
	a := &audiodFake{}
	r.media.MediaReady(a.send)
	s := r.connect(t, screentest.Options{})
	cast(r, &audiodv1.HlsCast{SessionId: "hls-1", Action: audiodv1.HlsCastAction_HLS_CAST_ACTION_PLAY,
		Url: "http://127.0.0.1:7100/master.m3u8", StartFraction: 0.25, ClientName: "iPhone"})
	mp := frame(t, s, func(e *screenv1.Envelope) bool { return e.GetMediaPlay() != nil }).GetMediaPlay()
	if mp.GetSessionId() != "hls-1" || mp.GetUrl() != "http://127.0.0.1:7100/master.m3u8" ||
		mp.GetMetadata().GetProvider().GetId() != "airplay" {
		t.Fatalf("play = %v", mp)
	}
	seek := frame(t, s, func(e *screenv1.Envelope) bool {
		return e.GetMediaCommand().GetAction() == screenv1.MediaAction_MEDIA_ACTION_SEEK
	}).GetMediaCommand()
	if seek.GetPositionMs() != 15000 {
		t.Fatalf("start seek = %v", seek)
	}
	// gated (rule 7): the player waits paused, and the sender hears it ready with rate 0
	info := a.wait(t, func(i *audiodv1.HlsPlaybackInfo) bool { return i.GetSessionId() == "hls-1" })
	if info.GetDurationMs() != 60000 || info.GetRate() != 0 || info.GetEnded() {
		t.Fatalf("info = %v", info)
	}
	// the session is the device's: one of audiod's external sessions, arbitrated like any other
	deadline := time.Now().Add(wait)
	for !a.registered("hls-1") {
		if time.Now().After(deadline) {
			t.Fatal("not registered with audiod")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cast(r, &audiodv1.HlsCast{SessionId: "hls-1", Action: audiodv1.HlsCastAction_HLS_CAST_ACTION_RATE, Rate: 1})
	frame(t, s, func(e *screenv1.Envelope) bool {
		return e.GetMediaCommand().GetAction() == screenv1.MediaAction_MEDIA_ACTION_RESUME
	})
	a.wait(t, func(i *audiodv1.HlsPlaybackInfo) bool { return i.GetRate() == 1 })
	cast(r, &audiodv1.HlsCast{SessionId: "hls-1", Action: audiodv1.HlsCastAction_HLS_CAST_ACTION_SCRUB, PositionMs: 30000})
	frame(t, s, func(e *screenv1.Envelope) bool { return e.GetMediaCommand().GetPositionMs() == 30000 })
	cast(r, &audiodv1.HlsCast{SessionId: "hls-1", Action: audiodv1.HlsCastAction_HLS_CAST_ACTION_STOP})
	frame(t, s, func(e *screenv1.Envelope) bool {
		return e.GetMediaCommand().GetAction() == screenv1.MediaAction_MEDIA_ACTION_STOP
	})
	end := a.wait(t, func(i *audiodv1.HlsPlaybackInfo) bool { return i.GetEnded() })
	if end.GetReason() != "finished" {
		t.Fatalf("end = %v", end)
	}
	// a cast it does not play is not acted on
	cast(r, &audiodv1.HlsCast{SessionId: "other", Action: audiodv1.HlsCastAction_HLS_CAST_ACTION_STOP})
	if _, err := s.Wait(100*time.Millisecond, func(e *screenv1.Envelope) bool {
		return e.GetMediaCommand().GetAction() == screenv1.MediaAction_MEDIA_ACTION_STOP && e != nil
	}); err == nil {
		// the stop above is still in the screen's frames; only a second one would be wrong
		n := 0
		for _, e := range s.Frames() {
			if e.GetMediaCommand().GetAction() == screenv1.MediaAction_MEDIA_ACTION_STOP {
				n++
			}
		}
		if n > 1 {
			t.Fatalf("%d stops", n)
		}
	}
}
