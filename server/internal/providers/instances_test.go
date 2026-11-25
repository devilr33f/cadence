package providers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"

	"git.shino.boo/devilreef/cadence/server/internal/api"
	"git.shino.boo/devilreef/cadence/server/internal/api/apitest"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/providers/otlphttp"
	"git.shino.boo/devilreef/cadence/server/internal/secrets"
	"git.shino.boo/devilreef/cadence/server/internal/store/db"
	"git.shino.boo/devilreef/cadence/server/internal/storetest"
)

const secretValue = "s3cr3t-value-xyz"

type nopSTT struct{}

func (nopSTT) Start(context.Context, providers.STTOptions) (providers.STTStream, error) {
	return nil, nil
}

func newInstances(t *testing.T) (*providers.Instances, *db.Queries, *int) {
	t.Helper()
	pool := storetest.Pool(t)
	box, err := secrets.New(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	built := new(int)
	keyed := providers.Module{
		ID: "keyed-stt", Kind: providers.KindSTT, Secrets: []string{"api_key"},
		ConfigSchema: `{"type":"object","properties":{"n":{"type":"integer"}},"additionalProperties":false}`,
		Check: func(cfg json.RawMessage) (string, bool) {
			var c struct{ N int }
			if json.Unmarshal(cfg, &c) != nil || c.N == 13 {
				return "/n", false
			}
			return "", true
		},
		New: func(_ json.RawMessage, s map[string]string) (any, error) {
			*built++
			if s["api_key"] != secretValue {
				return nil, errors.New("wrong secret")
			}
			return nopSTT{}, nil
		},
	}
	q := db.New(pool)
	return providers.NewInstances(pool, box, providers.NewRegistry(true, keyed)), q, built
}

func TestCreateSealsSecrets(t *testing.T) {
	ins, q, _ := newInstances(t)
	ctx := context.Background()
	inst, err := ins.Create(ctx, "main", "keyed-stt", json.RawMessage(`{"n":1}`), map[string]string{"api_key": secretValue})
	if err != nil {
		t.Fatal(err)
	}
	row, err := q.GetProviderInstance(ctx, "main")
	if err != nil {
		t.Fatal(err)
	}
	if len(row.Secrets) == 0 || bytes.Contains(row.Secrets, []byte(secretValue)) {
		t.Fatal("secrets column empty or holds the plain value")
	}
	out, _ := json.Marshal(inst)
	if strings.Contains(string(out), secretValue) || !strings.Contains(string(out), `"secrets":{"api_key":{"set":true}}`) {
		t.Fatalf("api output: %s", out)
	}
	list, err := ins.List(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %v %v", list, err)
	}
	out, _ = json.Marshal(list)
	if strings.Contains(string(out), secretValue) || !strings.Contains(string(out), `{"set":true}`) {
		t.Fatalf("list output: %s", out)
	}
	if _, err := ins.STT(ctx, "main"); err != nil {
		t.Fatalf("build with unsealed secret: %v", err)
	}
}

func TestCreateRejectsInvalidConfig(t *testing.T) {
	ins, q, _ := newInstances(t)
	ctx := context.Background()
	_, err := ins.Create(ctx, "bad", "keyed-stt", json.RawMessage(`{"n":"leaky-value"}`), nil)
	var inv *providers.ConfigInvalidError
	if !errors.As(err, &inv) || inv.Path != "/n" {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "leaky-value") {
		t.Fatal("error carries the value")
	}
	// a rule the schema cannot say: the module's own check, nothing stored
	_, err = ins.Create(ctx, "checked", "keyed-stt", json.RawMessage(`{"n":13}`), map[string]string{"api_key": secretValue})
	if !errors.As(err, &inv) || inv.Path != "/n" {
		t.Fatalf("checked err = %v", err)
	}
	if _, err := q.GetProviderInstance(ctx, "checked"); err == nil {
		t.Fatal("a config its module refused was stored")
	}
	if _, err = ins.Create(ctx, "u", "nope", nil, nil); !errors.Is(err, providers.ErrUnknownModule) {
		t.Fatalf("err = %v", err)
	}
	_, err = ins.Create(ctx, "u", "keyed-stt", json.RawMessage(`{}`), map[string]string{"other": "x"})
	if !errors.As(err, &inv) {
		t.Fatalf("unknown secret err = %v", err)
	}
}

func TestDuplicateName(t *testing.T) {
	ins, _, _ := newInstances(t)
	ctx := context.Background()
	if _, err := ins.Create(ctx, "d", "fake-tts", nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := ins.Create(ctx, "d", "fake-tts", nil, nil); !errors.Is(err, providers.ErrNameTaken) {
		t.Fatalf("err = %v", err)
	}
}

func TestWrongKind(t *testing.T) {
	ins, _, _ := newInstances(t)
	ctx := context.Background()
	if _, err := ins.Create(ctx, "t", "fake-tts", nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := ins.STT(ctx, "t"); !errors.Is(err, providers.ErrWrongKind) {
		t.Fatalf("err = %v", err)
	}
	if _, err := ins.TTS(ctx, "t"); err != nil {
		t.Fatal(err)
	}
	if _, err := ins.LLM(ctx, "missing"); !errors.Is(err, providers.ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestDeleteDropsCache(t *testing.T) {
	ins, _, built := newInstances(t)
	ctx := context.Background()
	sec := map[string]string{"api_key": secretValue}
	if _, err := ins.Create(ctx, "c", "keyed-stt", nil, sec); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := ins.STT(ctx, "c"); err != nil {
			t.Fatal(err)
		}
	}
	if *built != 1 {
		t.Fatalf("built %d times, want cached once", *built)
	}
	if err := ins.Delete(ctx, "c"); err != nil {
		t.Fatal(err)
	}
	if _, err := ins.STT(ctx, "c"); !errors.Is(err, providers.ErrNotFound) {
		t.Fatalf("after delete err = %v", err)
	}
	if err := ins.Delete(ctx, "c"); !errors.Is(err, providers.ErrNotFound) {
		t.Fatalf("second delete err = %v", err)
	}
	if _, err := ins.Create(ctx, "c", "keyed-stt", nil, sec); err != nil {
		t.Fatal(err)
	}
	if _, err := ins.STT(ctx, "c"); err != nil || *built != 2 {
		t.Fatalf("rebuilt %d err %v", *built, err)
	}
}

type noPing struct{}

func (noPing) Ping(context.Context) error { return nil }

func TestHTTPBodies(t *testing.T) {
	ins, _, _ := newInstances(t)
	app := api.NewApp(api.Options{
		DB: noPing{}, Version: "t",
		VerifyToken: apitest.Verify,
		Admin:       []func(huma.API){func(a huma.API) { providers.RegisterAdmin(a, ins, nil) }},
	})
	do := func(method, path, body string) (int, string) {
		req := httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer x")
		resp, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		var b bytes.Buffer
		_, _ = b.ReadFrom(resp.Body)
		return resp.StatusCode, strings.TrimSpace(b.String())
	}
	st, body := do("POST", "/providers", `{"name":"k","module":"keyed-stt","config":{},"secrets":{"api_key":"`+secretValue+`"}}`)
	if st != 201 || strings.Contains(body, secretValue) || !strings.Contains(body, `"secrets":{"api_key":{"set":true}}`) {
		t.Fatalf("create: %d %s", st, body)
	}
	cases := []struct {
		method, path, body string
		status             int
		want               string
	}{
		{"POST", "/providers", `{"name":"k","module":"keyed-stt","secrets":{"api_key":"x"}}`, 409, `{"error":"name_taken"}`},
		{"POST", "/providers", `{"name":"y","module":"keyed-stt"}`, 422, `{"error":"config_invalid","path":"/secrets/api_key"}`},
		{"POST", "/providers", `{"name":"y","module":"keyed-stt","secrets":{"api_key":""}}`, 422, `{"error":"config_invalid","path":"/secrets/api_key"}`},
		{"POST", "/providers", `{"name":"z","module":"nope"}`, 422, `{"error":"unknown_module"}`},
		{"POST", "/providers", `{"name":"z","module":"keyed-stt","config":{"n":"x"}}`, 422, `{"error":"config_invalid","path":"/n"}`},
		{"POST", "/providers", `{"name":"bad name","module":"keyed-stt"}`, 422, `{"error":"config_invalid","path":"/name"}`},
		// the catalog's path segment, which a GET of an instance by name would never reach
		{"POST", "/providers", `{"name":"Modules","module":"keyed-stt","secrets":{"api_key":"x"}}`, 422, `{"error":"config_invalid","path":"/name"}`},
		// the route's own check answers a body huma would refuse or require
		{"POST", "/providers", ``, 422, `{"error":"config_invalid","path":""}`},
		{"POST", "/providers", `{"name":`, 422, `{"error":"config_invalid","path":""}`},
		{"DELETE", "/providers/ghost", ``, 404, `{"error":"not_found"}`},
	}
	for _, c := range cases {
		if st, b := do(c.method, c.path, c.body); st != c.status || b != c.want {
			t.Errorf("%s %s: %d %s", c.method, c.path, st, b)
		}
	}
	if st, b := do("GET", "/providers", ""); st != 200 || !strings.Contains(b, `"name":"k"`) {
		t.Errorf("list: %d %s", st, b)
	}
	if st, _ := do("DELETE", "/providers/k", ""); st != 204 {
		t.Errorf("delete status %d", st)
	}
}

func TestWeatherSearchKinds(t *testing.T) {
	ins, _, _ := newInstances(t)
	ctx := context.Background()
	if _, err := ins.Create(ctx, "t", "fake-tts", nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := ins.Weather(ctx, "t"); !errors.Is(err, providers.ErrWrongKind) {
		t.Fatalf("weather err = %v", err)
	}
	if _, err := ins.Search(ctx, "t"); !errors.Is(err, providers.ErrWrongKind) {
		t.Fatalf("search err = %v", err)
	}
	if _, err := ins.Weather(ctx, "missing"); !errors.Is(err, providers.ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestEmbedKindAndOptionalSecrets(t *testing.T) {
	pool := storetest.Pool(t)
	box, _ := secrets.New(bytes.Repeat([]byte{7}, 32))
	var gotKey string
	opt := providers.Module{
		ID: "opt-embed", Kind: providers.KindEmbed, OptionalSecrets: []string{"apiKey"},
		ConfigSchema: `{"type":"object","additionalProperties":false}`,
		New: func(_ json.RawMessage, s map[string]string) (any, error) {
			gotKey = s["apiKey"]
			return struct{ providers.Embedder }{}, nil
		},
	}
	ins := providers.NewInstances(pool, box, providers.NewRegistry(true, opt))
	ctx := context.Background()
	if _, err := ins.Create(ctx, "no-key", "opt-embed", nil, nil); err != nil {
		t.Fatalf("optional secret required: %v", err)
	}
	if _, err := ins.Embed(ctx, "no-key"); err != nil || gotKey != "" {
		t.Fatalf("embed: %v key %q", err, gotKey)
	}
	inst, err := ins.Create(ctx, "keyed", "opt-embed", nil, map[string]string{"apiKey": secretValue})
	if err != nil || !inst.Secrets["apiKey"].Set {
		t.Fatalf("create: %v %+v", err, inst)
	}
	if _, err := ins.Embed(ctx, "keyed"); err != nil || gotKey != secretValue {
		t.Fatalf("embed: %v", err)
	}
	var invalid *providers.ConfigInvalidError
	if _, err := ins.Create(ctx, "bad", "opt-embed", nil, map[string]string{"other": "x"}); !errors.As(err, &invalid) {
		t.Fatalf("unknown secret accepted: %v", err)
	}
	if inst, err := ins.Create(ctx, "e", "opt-embed", nil, map[string]string{"apiKey": ""}); err != nil || inst.Secrets["apiKey"].Set {
		t.Fatalf("empty optional secret: %v", err)
	}
	if _, err := ins.Create(ctx, "t", "fake-tts", nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := ins.Embed(ctx, "t"); !errors.Is(err, providers.ErrWrongKind) {
		t.Fatalf("err = %v", err)
	}
}

func TestRefNamesTheInstanceAndItsKind(t *testing.T) {
	ins, q, _ := newInstances(t)
	ctx := context.Background()
	inst, err := ins.Create(ctx, "t", "fake-tts", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ref, err := ins.Ref(ctx, "t"); err != nil || ref.ID != inst.ID || ref.Kind != providers.KindTTS {
		t.Fatalf("ref %+v %v", ref, err)
	}
	if _, err := ins.Ref(ctx, "missing"); !errors.Is(err, providers.ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
	// deleted and created again under its name: another instance
	if err := ins.Delete(ctx, "t"); err != nil {
		t.Fatal(err)
	}
	again, err := ins.Create(ctx, "t", "fake-tts", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ref, err := ins.Ref(ctx, "t"); err != nil || ref.ID != again.ID || ref.ID == inst.ID {
		t.Fatalf("again %+v %v", ref, err)
	}
	if _, err := q.CreateProviderInstance(ctx, db.CreateProviderInstanceParams{Name: "old", Module: "removed-module", Config: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := ins.Ref(ctx, "old"); !errors.Is(err, providers.ErrUnknownModule) {
		t.Fatalf("old: %v", err)
	}
}

func TestOnChangeHearsCreateAndDelete(t *testing.T) {
	ins, _, _ := newInstances(t)
	ctx := context.Background()
	calls := 0
	ins.OnChange(func() { calls++ })
	if _, err := ins.Create(ctx, "t", "fake-tts", nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := ins.Create(ctx, "t", "fake-tts", nil, nil); !errors.Is(err, providers.ErrNameTaken) {
		t.Fatalf("taken: %v", err)
	}
	if err := ins.Delete(ctx, "t"); err != nil {
		t.Fatal(err)
	}
	if err := ins.Delete(ctx, "t"); !errors.Is(err, providers.ErrNotFound) {
		t.Fatalf("gone: %v", err)
	}
	if calls != 2 {
		t.Fatalf("heard %d changes", calls)
	}
}

// the export's authorization header is sealed like any provider secret: the
// API says it is set, never what it is
func TestOTLPInstanceAnswersItsSecretAsSet(t *testing.T) {
	pool := storetest.Pool(t)
	box, _ := secrets.New(bytes.Repeat([]byte{7}, 32))
	ins := providers.NewInstances(pool, box, providers.NewRegistry(false, otlphttp.Module))
	app := api.NewApp(api.Options{
		DB: noPing{}, Version: "t",
		VerifyToken: apitest.Verify,
		Admin:       []func(huma.API){func(a huma.API) { providers.RegisterAdmin(a, ins, nil) }},
	})
	do := func(method, path, body string) (int, string) {
		req := httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer x")
		resp, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		var b bytes.Buffer
		_, _ = b.ReadFrom(resp.Body)
		return resp.StatusCode, strings.TrimSpace(b.String())
	}
	st, body := do("POST", "/providers", `{"name":"otel","module":"otlp-http","config":{"url":"https://otel.example:4318"},`+
		`"secrets":{"authorization":"Bearer `+secretValue+`"}}`)
	if st != 201 || strings.Contains(body, secretValue) || !strings.Contains(body, `"secrets":{"authorization":{"set":true}}`) {
		t.Fatalf("create: %d %s", st, body)
	}
	if st, b := do("GET", "/providers", ""); st != 200 || strings.Contains(b, secretValue) || !strings.Contains(b, `{"set":true}`) {
		t.Fatalf("list: %d %s", st, b)
	}
	for cfg, path := range map[string]string{
		`{"url":"http://otel.example"}`: "/url", `{"url":"https://u:pw@otel.example"}`: "/url", `{}`: "",
	} {
		st, b := do("POST", "/providers", `{"name":"bad","module":"otlp-http","config":`+cfg+`}`)
		if st != 422 || !strings.Contains(b, `"error":"config_invalid"`) || (path != "" && !strings.Contains(b, `"path":"`+path+`"`)) {
			t.Fatalf("%s: %d %s", cfg, st, b)
		}
	}
	c, err := providers.Get[*otlphttp.Client](context.Background(), ins, "otel", providers.KindOTLP)
	if err != nil || c.Host() != "otel.example:4318" {
		t.Fatalf("build: %v", err)
	}
}
