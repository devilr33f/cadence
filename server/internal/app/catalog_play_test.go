package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"

	"git.shino.boo/devilreef/cadence/pkg/rpc"
	"git.shino.boo/devilreef/cadence/server/internal/api"
	"git.shino.boo/devilreef/cadence/server/internal/api/apitest"
	"git.shino.boo/devilreef/cadence/server/internal/catalog"
	"git.shino.boo/devilreef/cadence/server/internal/screens/calls"
)

// play-catalog answers an ha token's play as the screen's own media.play would, mapping its
// rpc status to the admin error model; its body carries the ref, episode, exact choices and from
func TestCatalogPlayRoute(t *testing.T) {
	var got calls.Call
	var reply calls.Reply
	srv := api.NewApp(api.Options{DB: nopPinger{}, Version: "t", VerifyToken: apitest.Verify,
		Admin: []func(huma.API){
			func(a huma.API) {
				catalog.RegisterPlayAdmin(a, func(_ context.Context, c calls.Call) calls.Reply {
					got = c
					return reply
				})
			},
		}})
	post := func(body string) (int, []byte) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/devices/tv-1/catalog/play", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+apitest.HABearer)
		resp, err := srv.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, b
	}
	reply = calls.OK(map[string]string{"sessionId": "video-9", "episode": "7"})
	code, body := post(`{"ref":"c:abc","episode":"7","choices":{"source":"anime365","voiceover":"AniLibria"}}`)
	if code != http.StatusOK {
		t.Fatalf("ok play: %d %s", code, body)
	}
	var answered struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(body, &answered); err != nil || answered.SessionID != "video-9" {
		t.Fatalf("ok play body %s (%v)", body, err)
	}
	if got.DeviceID != "tv-1" || got.Method != rpc.MediaPlay ||
		string(got.Args) != `{"ref":"c:abc","episode":"7","choices":{"source":"anime365","voiceover":"AniLibria"}}` {
		t.Fatalf("the call the method got: %s %s %s", got.DeviceID, got.Method, got.Args)
	}
	for _, tc := range []struct {
		status string
		code   string
		want   int
	}{
		{rpc.StatusInvalid, "invalid", http.StatusUnprocessableEntity},
		{rpc.StatusExpired, "unknown_ref", http.StatusNotFound},
		{rpc.StatusNotFound, "not_found", http.StatusNotFound},
		{rpc.StatusRefused, "unplayable", http.StatusConflict},
		{rpc.StatusUnavailable, "timeout", http.StatusBadGateway},
		{"internal", "", http.StatusInternalServerError},
	} {
		reply = calls.Reply{Status: tc.status, JSON: map[string]string{"code": tc.code}}
		code, body := post(`{"ref":"c:abc"}`)
		if code != tc.want {
			t.Fatalf("%s: %d %s, want %d", tc.status, code, body, tc.want)
		}
		if tc.want != http.StatusInternalServerError && !strings.Contains(string(body), `"error":"`+tc.code+`"`) {
			t.Fatalf("%s: body %s, want error %s", tc.status, body, tc.code)
		}
	}
	// the body the document promises: unknown fields and a missing ref are refused
	if code, _ := post(`{"ref":"c:abc","startMs":5}`); code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown field: %d, want 422", code)
	}
	if code, _ := post(`{}`); code != http.StatusUnprocessableEntity {
		t.Fatalf("no ref: %d, want 422", code)
	}
}
