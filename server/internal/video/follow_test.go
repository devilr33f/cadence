package video_test

import (
	"context"
	"strings"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/video"
)

const followGroup = "0b6f3a52-6a1e-4d2b-9c51-3c1d2a4e5f60"

// a group's follower screen gets the leader's video (multiroom P4): resolved for its own profile through its own
// relay token, with its group and no position of its own; a quality switch on it keeps it a follower, and its session
// is no video of its own the fan-out would follow in turn
func TestAFollowerGetsTheLeadersVideoWithItsGroup(t *testing.T) {
	r := newRig(t)
	r.screens["tv2"] = `{"power":"on","screen":true,"maxHeight":720,"codecs":["h264"]}`
	r.reports["tv2"] = map[string]bool{"media.video/play": true}
	if _, err := r.svc.Play(context.Background(), "tv", video.Request{VideoID: vid, StartMS: 5000, Title: "A Title", Music: true, View: video.ViewVideo}); err != nil {
		t.Fatal(err)
	}
	req, ok := r.svc.Current("tv", "video-1")
	if !ok || req.VideoID != vid || req.StartMS != 0 || !req.Music || req.View != video.ViewVideo {
		t.Fatalf("current = %+v, %v", req, ok)
	}
	if _, ok := r.svc.Current("tv", "video-0"); ok {
		t.Fatal("another session is the service's")
	}
	session, err := r.svc.Follow(context.Background(), "tv2", req, followGroup)
	if err != nil || session != "video-2" {
		t.Fatalf("follow = %q, %v", session, err)
	}
	if p := r.profiles[1]; p.MaxHeight != 720 {
		t.Fatalf("the follower resolved with %+v", p)
	}
	s := r.last()
	f, _ := s.args["follow"].(map[string]any)
	if s.dev != "tv2" || s.name != "play" || f["groupId"] != followGroup || s.args["startMs"] != nil {
		t.Fatalf("sent %+v", s)
	}
	leader := r.sent[0].args["url"].(string)
	follower := s.args["url"].(string)
	if token := func(u string) string {
		return strings.Split(strings.TrimPrefix(u, "https://cadence.example/v2/stream/"), "/")[0]
	}; token(leader) == token(follower) {
		t.Fatal("the follower shares the leader's relay token")
	}
	if _, ok := r.svc.Current("tv2", session); ok {
		t.Fatal("a follower's session is followed in turn")
	}
	if _, handled, err := r.svc.Intercept(context.Background(), "tv2", "media.video", "select_track", []byte(`{"kind":"video","id":"720p30"}`)); !handled || err != nil {
		t.Fatalf("handled %v err %v", handled, err)
	}
	if f, _ := r.last().args["follow"].(map[string]any); f["groupId"] != followGroup || r.last().args["startMs"] != nil {
		t.Fatalf("a follower's switch = %+v", r.last())
	}
	// a device without a screen follows nothing
	if _, err := r.svc.Follow(context.Background(), "speaker", req, followGroup); err == nil {
		t.Fatal("a speaker followed a video")
	}
	r.noLeak()
}
