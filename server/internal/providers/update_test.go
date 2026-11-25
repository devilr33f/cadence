package providers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"

	"git.shino.boo/devilreef/cadence/server/internal/api"
	"git.shino.boo/devilreef/cadence/server/internal/api/apitest"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/secrets"
	"git.shino.boo/devilreef/cadence/server/internal/storetest"
)

const (
	oldRequired = "required-secret-old"
	newRequired = "required-secret-new"
	oldOptional = "optional-secret-old"
	newOptional = "optional-secret-new"
	badRequired = "required-secret-build-failure"
)

type editableSearch struct {
	n       int
	key     string
	cookies string
}

func (*editableSearch) Search(context.Context, string) ([]providers.SearchResult, error) {
	return nil, nil
}

func configNumber(raw json.RawMessage) int {
	var cfg struct{ N int }
	_ = json.Unmarshal(raw, &cfg)
	return cfg.N
}

// one instance through the real store: its update builds before it commits, and a failed build changes neither the row
// nor the running value; no answer or log carries a secret or the module's error, which deliberately quotes one
func TestUpdateProvider(t *testing.T) {
	ctx := context.Background()
	pool := storetest.Pool(t)
	box, err := secrets.New(bytes.Repeat([]byte{9}, 32))
	if err != nil {
		t.Fatal(err)
	}
	mod := providers.Module{
		ID: "editable-search", Kind: providers.KindSearch,
		ConfigSchema:    `{"type":"object","properties":{"n":{"type":"integer"}},"additionalProperties":false}`,
		Secrets:         []string{"api_key"},
		OptionalSecrets: []string{"cookies"},
		Check: func(cfg json.RawMessage) (string, bool) {
			var c struct{ N int }
			if json.Unmarshal(cfg, &c) != nil || c.N == 13 {
				return "/n", false
			}
			return "", true
		},
		New: func(cfg json.RawMessage, sec map[string]string) (any, error) {
			if sec["api_key"] == badRequired {
				return nil, errors.New("module refused " + badRequired)
			}
			var c struct{ N int }
			if err := json.Unmarshal(cfg, &c); err != nil {
				return nil, err
			}
			return &editableSearch{n: c.N, key: sec["api_key"], cookies: sec["cookies"]}, nil
		},
	}
	ins := providers.NewInstances(pool, box, providers.NewRegistry(true, mod))
	changes := 0
	ins.OnChange(func() { changes++ })
	created, err := ins.Create(ctx, "main", mod.ID, json.RawMessage(`{"n":1}`), map[string]string{
		"api_key": oldRequired,
		"cookies": oldOptional,
	})
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	app := api.NewApp(api.Options{
		DB: noPing{}, Version: "t",
		VerifyToken: apitest.Verify,
		Admin: []func(huma.API){func(a huma.API) {
			providers.RegisterAdmin(a, ins, slog.New(slog.NewJSONHandler(&logs, nil)))
		}},
	})
	var answers []string
	patch := func(name, body string) (int, providers.Instance, string) {
		req := httptest.NewRequest(http.MethodPatch, "/api/v1/providers/"+name, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer x")
		resp, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		var raw bytes.Buffer
		_, _ = raw.ReadFrom(resp.Body)
		_ = resp.Body.Close()
		text := strings.TrimSpace(raw.String())
		answers = append(answers, text)
		var out providers.Instance
		if resp.StatusCode == http.StatusOK && json.Unmarshal(raw.Bytes(), &out) != nil {
			t.Fatalf("bad instance answer: %s", text)
		}
		return resp.StatusCode, out, text
	}
	running := func() *editableSearch {
		v, err := ins.Search(ctx, "main")
		if err != nil {
			t.Fatal(err)
		}
		return v.(*editableSearch)
	}
	first := running()

	st, got, body := patch("main", `{"config":{"n":2},"secrets":{"api_key":"`+newRequired+`"}}`)
	second := running()
	if st != http.StatusOK || got.ID != created.ID || got.Name != "main" || configNumber(got.Config) != 2 ||
		!got.Secrets["api_key"].Set || !got.Secrets["cookies"].Set || second == first || second.n != 2 ||
		second.key != newRequired || second.cookies != oldOptional {
		t.Fatalf("replace config and key: %d %+v %+v %s", st, got, second, body)
	}

	st, got, _ = patch("main", `{"secrets":{"cookies":"`+newOptional+`"}}`)
	third := running()
	if st != http.StatusOK || configNumber(got.Config) != 2 || third == second || third.n != 2 ||
		third.key != newRequired || third.cookies != newOptional {
		t.Fatalf("replace only cookies: %d %+v %+v", st, got, third)
	}

	st, got, _ = patch("main", `{"clearSecrets":["cookies"]}`)
	fourth := running()
	if st != http.StatusOK || len(got.Secrets) != 1 || !got.Secrets["api_key"].Set || fourth == third ||
		fourth.key != newRequired || fourth.cookies != "" || changes != 4 {
		t.Fatalf("clear optional: %d %+v %+v", st, got, fourth)
	}

	for _, tc := range []struct {
		name, body, want string
		status           int
	}{
		{"main", `{"clearSecrets":["api_key"]}`, `{"error":"config_invalid","path":"/clearSecrets/api_key"}`, http.StatusUnprocessableEntity},
		{"main", `{"secrets":{"unknown":"not-stored"}}`, `{"error":"config_invalid","path":"/secrets"}`, http.StatusUnprocessableEntity},
		{"main", `{"clearSecrets":["unknown"]}`, `{"error":"config_invalid","path":"/clearSecrets"}`, http.StatusUnprocessableEntity},
		{"main", `{"config":{"n":"bad"}}`, `{"error":"config_invalid","path":"/n"}`, http.StatusUnprocessableEntity},
		{"main", `{"config":{"n":13}}`, `{"error":"config_invalid","path":"/n"}`, http.StatusUnprocessableEntity},
		{"main", `{"secrets":{"api_key":""}}`, `{"error":"config_invalid","path":"/secrets/api_key"}`, http.StatusUnprocessableEntity},
		{"ghost", `{}`, `{"error":"not_found"}`, http.StatusNotFound},
	} {
		st, _, body = patch(tc.name, tc.body)
		if st != tc.status || body != tc.want {
			t.Errorf("PATCH %s %s: %d %s, want %d %s", tc.name, tc.body, st, body, tc.status, tc.want)
		}
		if current := running(); current != fourth {
			t.Fatalf("a refused update replaced the running instance: %s", tc.body)
		}
	}

	st, _, body = patch("main", `{"secrets":{"api_key":"`+badRequired+`"}}`)
	if st != http.StatusUnprocessableEntity || body != `{"error":"build_failed"}` {
		t.Fatalf("failed build: %d %s", st, body)
	}
	if current := running(); current != fourth || current.key != newRequired || current.cookies != "" {
		t.Fatalf("a failed build changed the running instance: %+v", current)
	}
	list, err := ins.List(ctx)
	if err != nil || len(list) != 1 || configNumber(list[0].Config) != 2 || len(list[0].Secrets) != 1 {
		t.Fatalf("a failed build changed the row: %+v %v", list, err)
	}

	if changes != 4 {
		t.Fatalf("refused updates notified a change: %d", changes)
	}

	allAnswers := strings.Join(answers, "\n")
	for _, secret := range []string{oldRequired, newRequired, oldOptional, newOptional, badRequired, "not-stored"} {
		if strings.Contains(allAnswers, secret) || strings.Contains(logs.String(), secret) {
			t.Errorf("a secret escaped into an answer or the logs")
		}
	}
	if !strings.Contains(logs.String(), `"msg":"provider_update_failed"`) ||
		!strings.Contains(logs.String(), `"instance":"main"`) || !strings.Contains(logs.String(), `"code":"build_failed"`) {
		t.Fatalf("the failed build's log lacks its id and code: %s", logs.String())
	}
}
