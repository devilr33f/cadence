package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
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

// the current-session routes read real catalog options and switch through the screen's own media.play; only codes,
// never a title or choice, reach the log
func TestCatalogSessionRoutes(t *testing.T) {
	svc, ref := fixtureCatalog(t)
	tracker := &haCatalogSession{current: catalog.SessionState{SessionID: "video-31", Ref: ref, Episode: "7",
		Choices: catalog.Selection{catalog.GroupVoiceover: "AniLibria", catalog.GroupQuality: "1080"}}}
	var logs bytes.Buffer
	var got calls.Call
	plays := 0
	srv := api.NewApp(api.Options{DB: nopPinger{}, Version: "t", Log: slog.New(slog.NewTextHandler(&logs, nil)),
		VerifyToken: apitest.Verify, Admin: []func(huma.API){func(a huma.API) {
			catalog.RegisterSessionAdmin(a, svc, func(_ context.Context, c calls.Call) calls.Reply {
				got, plays = c, plays+1
				return calls.OK(map[string]string{"sessionId": "video-32", "episode": "7"})
			}, tracker)
		}}})
	request := func(method, path, body string) (int, []byte) {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
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
	code, body := request(http.MethodGet, "/api/v1/devices/tv-1/catalog/session", "")
	if code != http.StatusOK {
		t.Fatalf("current: %d %s", code, body)
	}
	var current catalog.SessionView
	if err := json.Unmarshal(body, &current); err != nil || current.Ref != ref || current.SessionID != "video-31" ||
		len(current.Options.Groups) != 2 || current.Options.Selected[catalog.GroupVoiceover] != "AniLibria" {
		t.Fatalf("current body %s (%v)", body, err)
	}
	code, body = request(http.MethodPost, "/api/v1/devices/tv-1/catalog/select",
		`{"group":"voiceover","choice":"Crunchyroll"}`)
	if code != http.StatusOK || plays != 1 {
		t.Fatalf("select: %d %s, plays %d", code, body, plays)
	}
	var args struct {
		Ref      catalog.Ref         `json:"ref"`
		Episode  string              `json:"episode"`
		Choices  catalog.Selection   `json:"choices"`
		From     catalog.ResolveFrom `json:"from"`
		Replaces string              `json:"replaces"`
		Reason   string              `json:"reason"`
	}
	if err := json.Unmarshal(got.Args, &args); err != nil || got.Method != rpc.MediaPlay || args.Ref != ref ||
		args.Episode != "7" || args.Choices[catalog.GroupVoiceover] != "Crunchyroll" || len(args.Choices) != 1 ||
		args.From != catalog.FromStart || args.Replaces != "video-31" || args.Reason != "switch" {
		t.Fatalf("switch args %s (%v)", got.Args, err)
	}
	code, body = request(http.MethodPost, "/api/v1/devices/tv-1/catalog/select",
		`{"group":"voiceover","choice":"dead"}`)
	if code != http.StatusUnprocessableEntity || !strings.Contains(string(body), `"error":"invalid_choice"`) || plays != 1 {
		t.Fatalf("unavailable choice: %d %s, plays %d", code, body, plays)
	}
	tracker.current = catalog.SessionState{}
	code, _ = request(http.MethodGet, "/api/v1/devices/tv-1/catalog/session", "")
	if code != http.StatusNotFound {
		t.Fatalf("no current session: %d", code)
	}
	if log := logs.String(); strings.Contains(log, "Crunchyroll") || strings.Contains(log, "Rebuilding a World") ||
		strings.Contains(log, string(ref)) {
		t.Fatalf("catalog data in the log: %s", log)
	}
}

func TestCatalogChoiceReadRoutes(t *testing.T) {
	svc, ref := fixtureCatalog(t)
	srv := api.NewApp(api.Options{DB: nopPinger{}, Version: "t", VerifyToken: apitest.Verify,
		Admin: []func(huma.API){func(a huma.API) { catalog.RegisterAdmin(a, svc) }}})
	get := func(path string) (int, []byte) {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+apitest.HABearer)
		resp, err := srv.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, body
	}
	code, body := get("/api/v1/catalog/items/" + string(ref) + "/options?choice=voiceover%3DCrunchyroll")
	var options catalog.OptionsView
	if code != http.StatusOK || json.Unmarshal(body, &options) != nil ||
		options.Selected[catalog.GroupVoiceover] != "Crunchyroll" {
		t.Fatalf("selected options: %d %s", code, body)
	}
	code, _ = get("/api/v1/catalog/items/" + string(ref) + "/options?choice=voiceover%3DAniLibria&choice=voiceover%3DCrunchyroll")
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("duplicate selection: %d", code)
	}
	code, body = get("/api/v1/catalog/items/" + string(ref) + "/alternatives")
	var alternatives catalog.AlternativesView
	if code != http.StatusOK || json.Unmarshal(body, &alternatives) != nil || len(alternatives.Items) != 1 ||
		alternatives.Items[0].Ref != ref || alternatives.Items[0].Quality != 1080 || !alternatives.Items[0].Current {
		t.Fatalf("alternatives: %d %s", code, body)
	}
}
