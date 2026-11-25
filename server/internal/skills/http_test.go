package skills_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"

	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/api"
	"git.shino.boo/devilreef/cadence/server/internal/api/apitest"
	"git.shino.boo/devilreef/cadence/server/internal/skills"
	"git.shino.boo/devilreef/cadence/server/internal/storetest"
)

type noPing struct{}

func (noPing) Ping(context.Context) error { return nil }

type rig struct {
	t    *testing.T
	lib  *skills.Library
	call func(method, path, body string) (int, string)
	logs *bytes.Buffer
}

func newRig(t *testing.T) *rig {
	pool := storetest.Pool(t)
	logs := &bytes.Buffer{}
	log := slog.New(slog.NewTextHandler(logs, nil))
	lib := skills.New(skills.Deps{Store: skills.NewDBStore(pool), Builtins: skills.Builtins(), Log: log})
	if err := lib.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	app := api.NewApp(api.Options{
		DB: noPing{}, Version: "t", VerifyToken: apitest.Verify,
		Admin: []func(huma.API){func(a huma.API) { skills.RegisterAdmin(a, lib, log) }},
	})
	r := &rig{t: t, lib: lib, logs: logs}
	r.call = func(method, path, body string) (int, string) {
		req := httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		req.Header.Set("Authorization", "Bearer x")
		resp, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, strings.TrimSpace(string(b))
	}
	return r
}

type listed struct {
	Name, Source string
	Overrides    bool
	Enabled      bool
	Modes        []string
}

func (r *rig) list() []listed {
	r.t.Helper()
	code, b := r.call("GET", "/skills", "")
	var out []listed
	if code != 200 || json.Unmarshal([]byte(b), &out) != nil {
		r.t.Fatalf("list: %d %s", code, b)
	}
	return out
}

const ownBody = `{"description":"Кофемашина на кухне","body":"Скажи «готовлю» и вызови ha_control.","tools":["ha_control"],"triggers":["кофе","капучино"]}`

// the owner adds a skill, replaces and switches off a built-in, and deletes
// both: the next turn reads each change, and no line carries a skill's text
func TestSkillsAdmin(t *testing.T) {
	r := newRig(t)
	if got := r.list(); len(got) != 6 || got[0].Source != "builtin" {
		t.Fatalf("built-ins %+v", got)
	}
	if code, b := r.call("PUT", "/skills/coffee", ownBody); code != 200 || !strings.Contains(b, `"source":"owner"`) ||
		!strings.Contains(b, `"modes":null`) || !strings.Contains(b, `"enabled":true`) {
		t.Fatalf("put: %d %s", code, b)
	}
	if !slices.ContainsFunc(r.lib.For("fast"), func(s skillT) bool { return s.Name == "coffee" }) {
		t.Fatal("the next turn does not list coffee")
	}
	// a built-in replaced, then switched off, in one mode only
	off := `{"description":"TV","body":"x","enabled":false,"modes":["smart"]}`
	if code, b := r.call("PUT", "/skills/tv", off); code != 200 || !strings.Contains(b, `"overrides":true`) {
		t.Fatalf("override: %d %s", code, b)
	}
	if slices.ContainsFunc(r.lib.For("smart"), func(s skillT) bool { return s.Name == "tv" }) {
		t.Fatal("a switched-off skill is listed")
	}
	if code, b := r.call("DELETE", "/skills/tv", ""); code != 204 {
		t.Fatalf("delete override: %d %s", code, b)
	}
	if e, ok := r.lib.Get("tv"); !ok || e.Source != "builtin" || !e.Enabled {
		t.Fatalf("built-in not back: %+v", e)
	}
	for path, want := range map[string]string{"/skills/tv": `{"error":"builtin"}`, "/skills/nope": `{"error":"not_found"}`} {
		if code, b := r.call("DELETE", path, ""); b != want || code < 400 {
			t.Errorf("delete %s: %d %s", path, code, b)
		}
	}
	if code, _ := r.call("DELETE", "/skills/coffee", ""); code != 204 {
		t.Fatal("delete coffee")
	}
	for name, body := range map[string]string{
		"Bad": ownBody,
		"ok":  `{"description":"","body":"x"}`,
		"ok2": `{"description":"a\u0000b","body":"x"}`,
		"ok3": `{"description":"d","body":"x","triggers":["!!"]}`,
		"ok4": `{"description":"d","body":"x","tools":["a b"]}`,
	} {
		if code, b := r.call("PUT", "/skills/"+name, body); code != 422 {
			t.Errorf("put %s %s: %d %s", name, body, code, b)
		}
	}
	if logs := r.logs.String(); strings.Contains(logs, "Кофемашина") || strings.Contains(logs, "готовлю") {
		t.Fatalf("a skill's text in the log: %s", logs)
	}
}

type skillT = agent.Skill
