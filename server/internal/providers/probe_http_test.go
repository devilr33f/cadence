package providers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"git.shino.boo/devilreef/cadence/server/internal/api"
	"git.shino.boo/devilreef/cadence/server/internal/api/apitest"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/secrets"
	"git.shino.boo/devilreef/cadence/server/internal/store/db"
	"git.shino.boo/devilreef/cadence/server/internal/storetest"
)

const upstreamLeak = "LEAKY-UPSTREAM-TEXT"

type adminRig struct {
	t    *testing.T
	ins  *providers.Instances
	q    *db.Queries
	logs *bytes.Buffer
	call func(method, path string) (int, string, string)
}

func newAdminRig(t *testing.T, ins *providers.Instances, q *db.Queries) adminRig {
	t.Helper()
	logs := &bytes.Buffer{}
	app := api.NewApp(api.Options{
		DB: noPing{}, Version: "t", VerifyToken: apitest.Verify,
		Admin: []func(huma.API){func(a huma.API) {
			providers.RegisterAdmin(a, ins, slog.New(slog.NewJSONHandler(logs, nil)))
		}},
	})
	call := func(method, path string) (int, string, string) {
		req := httptest.NewRequest(method, "/api/v1"+path, nil)
		req.Header.Set("Authorization", "Bearer x")
		resp, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		var b bytes.Buffer
		_, _ = b.ReadFrom(resp.Body)
		return resp.StatusCode, strings.TrimSpace(b.String()), resp.Header.Get("Retry-After")
	}
	return adminRig{t: t, ins: ins, q: q, logs: logs, call: call}
}

func catalogModules() []providers.Module {
	return []providers.Module{
		{ID: "b-stt", Kind: providers.KindSTT, ConfigSchema: `{"type":"object"}`},
		{ID: "x-llm", Kind: providers.KindLLM, ConfigSchema: `{"type":"object","additionalProperties":false}`, Secrets: []string{"apiKey"}},
		{
			ID: "a-stt", Kind: providers.KindSTT, Secrets: []string{"apiKey"}, OptionalSecrets: []string{"region"},
			// written by hand, its keys in no sorted order: served in this order, compact
			ConfigSchema: `{
				"type": "object",
				"properties": {"zone": {"type": "string"}, "n": {"type": "integer", "minimum": 1}},
				"additionalProperties": false
			}`,
		},
	}
}

// the catalog lists every module by kind, then id, with its config schema as
// the module wrote it (its keys in its order, compact) and its secrets by name
// only; the fakes only with dev fakes on
func TestModuleCatalog(t *testing.T) {
	ids := func(body string) []string {
		var list []struct{ ID string }
		if err := json.Unmarshal([]byte(body), &list); err != nil {
			t.Fatalf("%v: %s", err, body)
		}
		out := make([]string, 0, len(list))
		for _, m := range list {
			out = append(out, m.ID)
		}
		return out
	}
	for devFakes, want := range map[bool]string{
		false: "x-llm a-stt b-stt",
		true:  "fake-embed fake-llm x-llm a-stt b-stt fake-stt fake-tts",
	} {
		r := newAdminRig(t, providers.NewInstances(nil, nil, providers.NewRegistry(devFakes, catalogModules()...)), nil)
		st, body, _ := r.call("GET", "/providers/modules")
		if got := strings.Join(ids(body), " "); st != 200 || got != want {
			t.Fatalf("dev fakes %v: %d %s", devFakes, st, got)
		}
		if !strings.Contains(body, `{"id":"a-stt","kind":"stt","configSchema":{"type":"object","properties":{"zone":{"type":"string"},"n":{"type":"integer","minimum":1}},"additionalProperties":false},`+
			`"secrets":[{"name":"apiKey","required":true},{"name":"region","required":false}]}`) ||
			!strings.Contains(body, `{"id":"b-stt","kind":"stt","configSchema":{"type":"object"},"secrets":[]}`) {
			t.Fatalf("entries: %s", body)
		}
		// every module's schema, the fakes' too, byte for byte as it wrote it, compact
		var served []struct {
			ID           string          `json:"id"`
			ConfigSchema json.RawMessage `json:"configSchema"`
		}
		if err := json.Unmarshal([]byte(body), &served); err != nil {
			t.Fatal(err)
		}
		for i, m := range r.ins.Modules() {
			var want bytes.Buffer
			if err := json.Compact(&want, []byte(m.ConfigSchema)); err != nil {
				t.Fatal(err)
			}
			if served[i].ID != m.ID || !bytes.Equal(served[i].ConfigSchema, want.Bytes()) {
				t.Fatalf("%s: served %s, wrote %s", m.ID, served[i].ConfigSchema, want.Bytes())
			}
		}
	}
}

// probed is one build of a probed-stt instance
type probed struct{ providers.STT }

type probeRig struct {
	adminRig
	builds atomic.Int32
	seen   chan any
	probe  atomic.Value
}

func newProbeRig(t *testing.T) *probeRig {
	t.Helper()
	pool := storetest.Pool(t)
	box, err := secrets.New(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	r := &probeRig{seen: make(chan any, 8)}
	r.setProbe(func(context.Context) error { return nil })
	mods := []providers.Module{
		{
			ID: "probed-stt", Kind: providers.KindSTT, Secrets: []string{"apiKey"},
			ConfigSchema: `{"type":"object","properties":{"fail":{"type":"boolean"}}}`,
			New: func(cfg json.RawMessage, sec map[string]string) (any, error) {
				r.builds.Add(1)
				var c struct{ Fail bool }
				if json.Unmarshal(cfg, &c) != nil || c.Fail {
					return nil, errors.New(upstreamLeak)
				}
				if sec["apiKey"] != secretValue {
					return nil, errors.New("wrong secret")
				}
				return &probed{}, nil
			},
			Probe: func(ctx context.Context, v any) error {
				r.seen <- v
				return r.probe.Load().(func(context.Context) error)(ctx)
			},
		},
		{
			ID: "unprobed-stt", Kind: providers.KindSTT, ConfigSchema: `{"type":"object"}`,
			New: func(json.RawMessage, map[string]string) (any, error) { return &probed{}, nil },
		},
	}
	q := db.New(pool)
	r.adminRig = newAdminRig(t, providers.NewInstances(pool, box, providers.NewRegistry(false, mods...)), q)
	return r
}

func (r *probeRig) setProbe(f func(context.Context) error) { r.probe.Store(f) }

func (r *probeRig) create(name, module, cfg string) {
	r.t.Helper()
	var sec map[string]string
	if module == "probed-stt" {
		sec = map[string]string{"apiKey": secretValue}
	}
	if _, err := r.ins.Create(context.Background(), name, module, json.RawMessage(cfg), sec); err != nil {
		r.t.Fatal(err)
	}
}

type testAnswer struct {
	OK   bool    `json:"ok"`
	Code *string `json:"code"`
	MS   *int64  `json:"ms"`
}

func (r *probeRig) test(name string) (int, testAnswer, string) {
	r.t.Helper()
	st, body, retry := r.call("POST", "/providers/"+name+"/test")
	var a testAnswer
	if st == 200 {
		if err := json.Unmarshal([]byte(body), &a); err != nil || a.MS == nil || *a.MS < 0 {
			r.t.Fatalf("answer %s", body)
		}
	}
	return st, a, retry
}

func code(a testAnswer) string {
	if a.Code == nil {
		return ""
	}
	return *a.Code
}

func TestProviderTest(t *testing.T) {
	r := newProbeRig(t)
	clock := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	providers.SetNow(t, func() time.Time { return clock })
	r.create("main", "probed-stt", `{}`)

	if st, a, _ := r.test("main"); st != 200 || !a.OK || code(a) != "" {
		t.Fatalf("ok: %d %+v", st, a)
	}
	// one test per instance per 5 s, the wait in whole seconds
	clock = clock.Add(1500 * time.Millisecond)
	if st, body, retry := r.call("POST", "/providers/main/test"); st != 429 || body != `{"error":"rate_limited"}` || retry != "4" {
		t.Fatalf("again: %d %s retry %q", st, body, retry)
	}
	clock = clock.Add(3500 * time.Millisecond)
	r.setProbe(func(context.Context) error { return errors.Join(providers.ProbeAuth, errors.New(upstreamLeak)) })
	if st, a, _ := r.test("main"); st != 200 || a.OK || code(a) != "auth" {
		t.Fatalf("after the window: %d %+v", st, a)
	}
	if st, body, _ := r.call("POST", "/providers/ghost/test"); st != 404 || body != `{"error":"not_found"}` {
		t.Fatalf("ghost: %d %s", st, body)
	}

	// any other failure, or a panic, is bad_response; never its text
	for i, f := range []func(context.Context) error{
		func(context.Context) error { return errors.New(upstreamLeak) },
		func(context.Context) error { panic(upstreamLeak) },
	} {
		name := "other" + string(rune('a'+i))
		r.create(name, "probed-stt", `{}`)
		r.setProbe(f)
		if st, a, _ := r.test(name); st != 200 || a.OK || code(a) != "bad_response" {
			t.Fatalf("%s: %d %+v", name, st, a)
		}
	}

	// the probe runs under the test's own deadline
	r.create("deadline", "probed-stt", `{}`)
	r.setProbe(func(ctx context.Context) error {
		if d, ok := ctx.Deadline(); !ok || time.Until(d) > 10*time.Second || time.Until(d) < 9*time.Second {
			return errors.New("deadline")
		}
		return nil
	})
	if st, a, _ := r.test("deadline"); st != 200 || !a.OK {
		t.Fatalf("deadline: %d %+v", st, a)
	}

	// no probe, a module no longer registered, an instance that does not build
	r.create("bare", "unprobed-stt", `{}`)
	r.create("broken", "probed-stt", `{"fail":true}`)
	if _, err := r.q.CreateProviderInstance(context.Background(), db.CreateProviderInstanceParams{
		Name: "old", Module: "removed-module", Config: []byte(`{}`),
	}); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"bare": "unsupported", "old": "unsupported", "broken": "build_failed"} {
		if st, a, _ := r.test(name); st != 200 || a.OK || code(a) != want {
			t.Fatalf("%s: %d %+v", name, st, a)
		}
	}

	logs := r.logs.String()
	if strings.Count(logs, `"msg":"provider_test"`) != 8 || !strings.Contains(logs, `"instance":"main","code":"ok","ms":`) ||
		!strings.Contains(logs, `"instance":"main","code":"auth","ms":`) || !strings.Contains(logs, `"instance":"broken","code":"build_failed","ms":`) {
		t.Fatalf("logs: %s", logs)
	}
	if strings.Contains(logs, upstreamLeak) || strings.Contains(logs, secretValue) {
		t.Fatal("logs carry upstream text or a secret")
	}
}

// a test builds the instance afresh: the instance the runtime cached is left
// as it is, and a test caches nothing
func TestProviderTestLeavesTheCachedInstance(t *testing.T) {
	r := newProbeRig(t)
	ctx := context.Background()
	r.create("main", "probed-stt", `{}`)
	cached, err := r.ins.STT(ctx, "main")
	if err != nil {
		t.Fatal(err)
	}
	if st, a, _ := r.test("main"); st != 200 || !a.OK {
		t.Fatalf("test: %d %+v", st, a)
	}
	if v := <-r.seen; v == any(cached) {
		t.Fatal("the probe ran on the cached instance")
	}
	again, err := r.ins.STT(ctx, "main")
	if err != nil || again != cached || r.builds.Load() != 2 {
		t.Fatalf("cache changed: same %v builds %d err %v", again == cached, r.builds.Load(), err)
	}
	r.create("fresh", "probed-stt", `{}`)
	if st, _, _ := r.test("fresh"); st != 200 {
		t.Fatalf("fresh: %d", st)
	}
	<-r.seen
	if _, err := r.ins.STT(ctx, "fresh"); err != nil || r.builds.Load() != 4 {
		t.Fatalf("a test cached its build: builds %d err %v", r.builds.Load(), err)
	}
}
