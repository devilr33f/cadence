package screen_test

import (
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/screen"
	"git.shino.boo/devilreef/cadence/device/internal/screen/screentest"
	"git.shino.boo/devilreef/cadence/pkg/capschema"
	screenv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/screen/v1"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

// the server's prepare shows the resolving step on the screen at once, and its failure after
func TestPrepareShowsTheResolvingStep(t *testing.T) {
	r := newRig(t, screen.Options{})
	s := r.connect(t, screentest.Options{})
	video := r.caps[capschema.MediaVideoID]
	if _, err := command(t, video, "prepare",
		`{"state":"resolving","title":"Песня","subtitle":"Артист","artworkUrl":"https://cadence.test/v2/images/abc"}`); err != nil {
		t.Fatal(err)
	}
	p := frame(t, s, func(e *screenv1.Envelope) bool { return e.GetMediaPreparing() != nil }).GetMediaPreparing()
	if p.GetState() != screenv1.PrepareState_PREPARE_STATE_RESOLVING || p.GetTitle() != "Песня" || p.GetSubtitle() != "Артист" ||
		p.GetArtworkUrl() != "https://cadence.test/v2/images/abc" {
		t.Fatalf("preparing = %v", p)
	}
	if _, err := command(t, video, "prepare", `{"state":"failed","code":"sign_in_required"}`); err != nil {
		t.Fatal(err)
	}
	p = frame(t, s, func(e *screenv1.Envelope) bool {
		return e.GetMediaPreparing().GetState() == screenv1.PrepareState_PREPARE_STATE_FAILED
	}).GetMediaPreparing()
	if p.GetCode() != "sign_in_required" {
		t.Fatalf("failed = %v", p)
	}
	for _, bad := range []string{`{}`, `{"state":"done"}`, `{"state":"failed","code":"Bad Code"}`, `{"state":"resolving","artworkUrl":"file:///x"}`} {
		if _, err := command(t, video, "prepare", bad); err == nil {
			t.Fatalf("%s taken", bad)
		}
	}
}

// a music video's audio view plays as a track on the audio page, with the views and the audio languages listed
func TestPlayCarriesTheViewsAndTheAudioLanguages(t *testing.T) {
	r := newRig(t, screen.Options{})
	s := r.connect(t, screentest.Options{})
	if _, err := command(t, r.caps[capschema.MediaVideoID], "play",
		`{"url":"https://a.test/audio","metadata":{"title":"Song","artist":"Artist","album":"LP","kind":"track"},`+
			`"view":"visualizer","views":["video","audio","visualizer"],`+
			`"renditions":[{"name":"1080p","height":1080,"hdr":"HDR10","selected":true},{"name":"720p","height":720}],`+
			`"audioChoices":[{"id":"a-en","language":"en","label":"English","original":true},`+
			`{"id":"a-ru","language":"ru","label":"Russian","dubbed":true,"selected":true}]}`); err != nil {
		t.Fatal(err)
	}
	mp := frame(t, s, func(e *screenv1.Envelope) bool { return e.GetMediaPlay() != nil }).GetMediaPlay()
	if mp.GetView() != screenv1.MediaView_MEDIA_VIEW_VISUALIZER || len(mp.GetViews()) != 3 ||
		mp.GetViews()[1] != screenv1.MediaView_MEDIA_VIEW_AUDIO || mp.GetMetadata().GetKind() != cadencev2.MediaKind_MEDIA_KIND_TRACK ||
		mp.GetMetadata().GetAlbum() != "LP" || mp.GetRenditions()[0].GetHdr() != "HDR10" {
		t.Fatalf("play = %v", mp)
	}
	if a := mp.GetAudioChoices(); len(a) != 2 || !a[0].GetOriginal() || a[1].GetId() != "a-ru" || !a[1].GetDubbed() ||
		!a[1].GetSelected() || a[1].GetLabel() != "Russian" {
		t.Fatalf("audio = %v", a)
	}
	// the device's session carries the track's metadata to the server
	var got *cadencev2.MediaSessionState
	deadline := time.After(wait)
	for got.GetSessionId() != mp.GetSessionId() {
		select {
		case got = <-r.media.Sessions():
		case <-deadline:
			t.Fatal("no session")
		}
	}
	if m := got.GetMetadata(); m.GetKind() != cadencev2.MediaKind_MEDIA_KIND_TRACK || m.GetAlbum() != "LP" || m.GetArtist() != "Artist" {
		t.Fatalf("session metadata = %v", m)
	}
	if _, err := command(t, r.caps[capschema.MediaVideoID], "play", `{"url":"https://a.test/x","view":"radio"}`); err == nil {
		t.Fatal("an unknown view taken")
	}
}
