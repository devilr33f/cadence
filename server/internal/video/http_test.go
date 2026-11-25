package video_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/humatest"

	"git.shino.boo/devilreef/cadence/server/internal/devices"
	"git.shino.boo/devilreef/cadence/server/internal/video"
)

type devs struct{}

func (devs) Canonical(_ context.Context, id string) (string, error) {
	if id != "tv" && id != "speaker" {
		return "", devices.ErrNotFound
	}
	return id, nil
}

func TestAdminPlaysAYouTubeLinkOnTheScreen(t *testing.T) {
	r := newRig(t)
	cfg := huma.DefaultConfig("t", "1")
	cfg.CreateHooks = nil
	_, a := humatest.New(t, cfg)
	video.RegisterAdmin(a, r.svc, devs{})

	resp := a.Post("/devices/tv/video", map[string]any{"url": "https://youtu.be/" + vid + "?t=1m"})
	if resp.Code != http.StatusOK {
		t.Fatalf("%d %s", resp.Code, resp.Body)
	}
	var res video.PlayResult
	_ = json.Unmarshal(resp.Body.Bytes(), &res)
	if res.Mode != "video" || res.Height != 1080 || r.last().args["startMs"].(float64) != 60_000 {
		t.Fatalf("%+v %+v", res, r.last().args)
	}
	resp = a.Get("/devices/tv/video")
	if resp.Code != http.StatusOK || !strings.Contains(resp.Body.String(), `"id":"720p60"`) {
		t.Fatalf("%d %s", resp.Code, resp.Body)
	}
	if resp := a.Get("/devices/speaker/video"); resp.Code != http.StatusOK || !strings.Contains(resp.Body.String(), `"renditions":[]`) {
		t.Fatalf("%d %s", resp.Code, resp.Body)
	}

	cases := []struct {
		path string
		body map[string]any
		code int
		want string
	}{
		{"/devices/tv/video", map[string]any{"url": "https://evil.example/watch?v=" + vid}, http.StatusUnprocessableEntity, "invalid_url"},
		{"/devices/nope/video", map[string]any{"url": "https://youtu.be/" + vid}, http.StatusNotFound, "not_found"},
		{"/devices/speaker/video", map[string]any{"url": "https://youtu.be/" + vid, "screenOnly": true}, http.StatusConflict, "no_screen"},
		{"/devices/tv/video", map[string]any{"url": "https://youtu.be/AAAAAAAAAAA"}, http.StatusConflict, "unplayable"},
	}
	for _, c := range cases {
		resp := a.Post(c.path, c.body)
		if resp.Code != c.code || !strings.Contains(resp.Body.String(), c.want) {
			t.Errorf("%s %v: %d %s", c.path, c.body, resp.Code, resp.Body)
		}
	}
	if resp := a.Post("/devices/speaker/video", map[string]any{"url": "https://youtu.be/" + vid}); resp.Code != http.StatusOK ||
		!strings.Contains(resp.Body.String(), `"mode":"audio"`) {
		t.Fatalf("speaker: %d %s", resp.Code, resp.Body)
	}
	r.noLeak()
}
