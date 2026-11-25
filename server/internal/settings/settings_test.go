package settings_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5/pgxpool"

	"git.shino.boo/devilreef/cadence/server/internal/api"
	"git.shino.boo/devilreef/cadence/server/internal/api/apitest"
	"git.shino.boo/devilreef/cadence/server/internal/settings"
	"git.shino.boo/devilreef/cadence/server/internal/store/db"
	"git.shino.boo/devilreef/cadence/server/internal/storetest"
)

type rig struct {
	t    *testing.T
	q    *db.Queries
	pool *pgxpool.Pool
	s    *settings.Service
	app  *fiber.App
	dev  string
}

type noPing struct{}

func (noPing) Ping(context.Context) error { return nil }

func newRig(t *testing.T) *rig {
	pool := storetest.Pool(t)
	q := db.New(pool)
	s := settings.NewService(pool)
	app := api.NewApp(api.Options{
		DB: noPing{}, Version: "t",
		VerifyToken: apitest.Verify,
		Admin:       []func(huma.API){func(a huma.API) { settings.RegisterAdmin(a, s) }},
	})
	dev, err := q.CreateDevice(context.Background(), db.CreateDeviceParams{Name: "d", Model: "sim"})
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{t: t, pool: pool, q: q, s: s, app: app, dev: dev.ID.String()}
	// the instances the tests name: a key newly names only one that exists
	r.addInstances(knownInstances...)
	return r
}

var knownInstances = func() []string {
	names := []string{"a", "b", "g", "dev", "dev2", "mem", "other", "agent", "home", "github", "notes.v2",
		"grafana.cloud-1", "otel", "v-stt", "v-tts", "v-llm", "v-weather", "v-search", "v-embed"}
	for i := range 32 {
		names = append(names, fmt.Sprintf("s%d", i))
	}
	return names
}()

func (r *rig) addInstances(names ...string) {
	r.t.Helper()
	if _, err := r.pool.Exec(context.Background(),
		`INSERT INTO provider_instances (name, module) SELECT unnest($1::text[]), 'fake-stt'`, names); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) dropInstance(name string) {
	r.t.Helper()
	if _, err := r.q.DeleteProviderInstance(context.Background(), name); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) do(method, path, body string) (int, string) {
	req := httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer x")
	resp, err := r.app.Test(req)
	if err != nil {
		r.t.Fatal(err)
	}
	var b bytes.Buffer
	_, _ = b.ReadFrom(resp.Body)
	_ = resp.Body.Close()
	return resp.StatusCode, strings.TrimSpace(b.String())
}

func (r *rig) put(path, body string) {
	r.t.Helper()
	if st, b := r.do("PUT", path, body); st != 200 {
		r.t.Fatalf("put %s: %d %s", path, st, b)
	}
}

func (r *rig) effective() settings.Values {
	r.t.Helper()
	v, err := r.s.Effective(context.Background(), r.dev)
	if err != nil {
		r.t.Fatal(err)
	}
	return v
}

func TestDefaultsApply(t *testing.T) {
	s := settings.NewService(storetest.Pool(t))
	got, err := s.Effective(context.Background(), "00000000-0000-0000-0000-000000000001")
	want := settings.Values{
		Language: "en", TimeZone: "UTC", Mode: "fast", AllowedModes: []string{"fast", "smart"},
		ModeRevert: "after_idle", WeatherLocation: "Bishkek, Kyrgyzstan", HistoryTurns: 20, CompactAfter: 10,
		CompactionMaxTokens: settings.DefaultCompactionMaxTokens,
		HermesApprovals:     "voice", QuietHours: settings.QuietHours{Enabled: true, Start: "23:00", End: "08:00"},
		HASensitive:          "woken",
		FollowUp:             settings.FollowUp{Enabled: true, WindowMs: 5000, Limit: 3},
		LogRetentionDays:     7,
		AfterBusy:            "announce",
		RemoteTargets:        "allow",
		ErrorSound:           "prompt",
		SubscriptionDelivery: "speaker", SubscriptionApprovals: "off",
		Tracing: settings.Tracing{Enabled: true, SampleRatio: 1, RingSize: 500},
	}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v err %v", got, err)
	}
}

func TestSparseOverrideFalsy(t *testing.T) {
	r := newRig(t)
	r.put("/settings/global", `{"tools":["weather"],"historyTurns":9,"persona":"p"}`)
	if got := r.effective(); !reflect.DeepEqual(got.Tools, []string{"weather"}) || got.HistoryTurns != 9 {
		t.Fatalf("global only: %+v", got)
	}
	r.put("/devices/"+r.dev+"/settings", `{"tools":[],"historyTurns":0,"persona":""}`)
	got := r.effective()
	if got.Tools == nil || len(got.Tools) != 0 || got.HistoryTurns != 0 || got.Persona != "" {
		t.Fatalf("device falsy overrides lost: %+v", got)
	}
}

func TestNullRemovesOverride(t *testing.T) {
	r := newRig(t)
	r.put("/settings/global", `{"tts":"g","language":"ru"}`)
	r.put("/devices/"+r.dev+"/settings", `{"tts":"dev","voice":"v"}`)
	r.put("/devices/"+r.dev+"/settings", `{"tts":null}`)
	got := r.effective()
	if got.TTS != "g" || got.Voice != "v" || got.Language != "ru" {
		t.Fatalf("patch semantics: %+v", got)
	}
	if st, b := r.do("GET", "/devices/"+r.dev+"/settings", ""); st != 200 || b != `{"voice":"v"}` {
		t.Fatalf("stored: %d %s", st, b)
	}
}

func TestInvalidSettings(t *testing.T) {
	r := newRig(t)
	for _, body := range []string{
		`{"timeZone":"Mars/Base"}`, `{"historyTurns":51}`, `{"historyTurns":-1}`,
		`{"bogus":"x"}`, `{"modeRevert":"sometimes"}`, `{"tools":"weather"}`, `[]`, `null`,
		// the route's own check answers a body huma would refuse or require
		``, `{"stt":`,
	} {
		for _, path := range []string{"/settings/global", "/devices/" + r.dev + "/settings"} {
			if st, b := r.do("PUT", path, body); st != 422 || b != `{"error":"settings_invalid"}` {
				t.Fatalf("%s %s: %d %s", path, body, st, b)
			}
		}
	}
	r.put("/settings/global", `{"timeZone":"Asia/Bishkek","historyTurns":50}`)
}

// a persona rides in every turn's system prompt: MaxPersona characters, not
// bytes, so a Cyrillic one gets as many as a Latin one
func TestPersonaLength(t *testing.T) {
	r := newRig(t)
	at := strings.Repeat("ж", settings.MaxPersona)
	r.put("/devices/"+r.dev+"/settings", `{"persona":"`+at+`"}`)
	if got := r.effective(); got.Persona != at {
		t.Fatalf("persona of %d characters lost", settings.MaxPersona)
	}
	for _, path := range []string{"/settings/global", "/devices/" + r.dev + "/settings"} {
		if st, b := r.do("PUT", path, `{"persona":"`+at+`ж"}`); st != 422 || b != `{"error":"settings_invalid"}` {
			t.Errorf("%s: %d %s", path, st, b)
		}
	}
}

func TestInvalidTimeZone(t *testing.T) {
	r := newRig(t)
	if st, _ := r.do("PUT", "/settings/global", `{"timeZone":"Not/AZone"}`); st != 422 {
		t.Fatalf("status %d", st)
	}
}

func TestGlobalEmptyStringIsUnset(t *testing.T) {
	r := newRig(t)
	r.put("/settings/global", `{"stt":"a","voice":""}`)
	if st, b := r.do("GET", "/settings/global", ""); st != 200 || b != `{"stt":"a"}` {
		t.Fatalf("%d %s", st, b)
	}
}

// regression: a path id in another spelling stored the row under a scope
// Effective, which gets the canonical id, never read
func TestDeviceScopeCanonical(t *testing.T) {
	r := newRig(t)
	upper := strings.ToUpper(r.dev)
	r.put("/devices/"+upper+"/settings", `{"tts":"dev"}`)
	r.put("/devices/"+strings.ReplaceAll(upper, "-", "")+"/settings", `{"tts":"dev2"}`)
	if eff := r.effective(); eff.TTS != "dev2" {
		t.Fatalf("effective %+v", eff)
	}
	if st, b := r.do("GET", "/devices/"+upper+"/settings", ""); st != 200 || b != `{"tts":"dev2"}` {
		t.Fatalf("device %d %s", st, b)
	}
}

func TestPutGetRoundTrip(t *testing.T) {
	r := newRig(t)
	r.put("/settings/global", `{"stt":"a","tts":"b","language":"en"}`)
	r.put("/devices/"+r.dev+"/settings", `{"tts":"dev","language":"ru"}`)
	if st, b := r.do("GET", "/settings/global", ""); st != 200 || !strings.Contains(b, `"stt":"a"`) {
		t.Fatalf("get global: %d %s", st, b)
	}
	if st, b := r.do("GET", "/devices/"+r.dev+"/settings", ""); st != 200 || !strings.Contains(b, `"tts":"dev"`) {
		t.Fatalf("get device: %d %s", st, b)
	}
	if eff := r.effective(); eff.STT != "a" || eff.TTS != "dev" || eff.Language != "ru" {
		t.Fatalf("effective %+v", eff)
	}
	if st, b := r.do("PUT", "/devices/00000000-0000-0000-0000-000000000009/settings", `{}`); st != 404 || b != `{"error":"not_found"}` {
		t.Fatalf("unknown device: %d %s", st, b)
	}
}

func TestMigrationDropsEmptyStrings(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if err := r.q.PutSettings(ctx, db.PutSettingsParams{Scope: r.dev, Data: []byte(
		`{"stt":"","tts":"x","voice":"","llm":"","model":"","language":""}`)}); err != nil {
		t.Fatal(err)
	}
	if err := r.q.PutSettings(ctx, db.PutSettingsParams{Scope: "other", Data: []byte(`{"stt":""}`)}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("../store/migrations/00006_settings_sparse.sql")
	if err != nil {
		t.Fatal(err)
	}
	up := strings.SplitN(strings.SplitN(string(raw), "-- +goose Down", 2)[0], "-- +goose Up", 2)[1]
	if _, err := r.pool.Exec(ctx, up); err != nil {
		t.Fatal(err)
	}
	if st, b := r.do("GET", "/devices/"+r.dev+"/settings", ""); st != 200 || b != `{"tts":"x"}` {
		t.Fatalf("device row: %d %s", st, b)
	}
	if v, err := r.q.GetSettings(ctx, "other"); err != nil || string(v) != `{}` {
		t.Fatalf("other row: %s %v", v, err)
	}
	if eff := r.effective(); eff.Language != "en" || eff.TTS != "x" {
		t.Fatalf("effective %+v", eff)
	}
}

func TestConcurrentPatchesKeepAllKeys(t *testing.T) {
	r := newRig(t)
	keys := []string{"stt", "tts", "voice", "llm", "model", "persona", "weather", "search", "embed",
		"weatherLocation", "language", "timeZone", "mode", "modeRevert"}
	vals := map[string]string{"timeZone": "UTC", "mode": "fast", "modeRevert": "never", "language": "ru"}
	var wg sync.WaitGroup
	n := 0
	for _, k := range keys {
		v := vals[k]
		if v == "" {
			v = "v-" + k
		}
		wg.Add(1)
		n++
		go func() {
			defer wg.Done()
			r.put("/devices/"+r.dev+"/settings", `{"`+k+`":"`+v+`"}`)
		}()
	}
	for _, p := range []string{`{"tools":["a"]}`, `{"historyTurns":3}`, `{"allowedModes":["fast"]}`} {
		wg.Add(1)
		n++
		go func() {
			defer wg.Done()
			r.put("/devices/"+r.dev+"/settings", p)
		}()
	}
	wg.Wait()
	o, err := r.s.Device(context.Background(), r.dev)
	if err != nil || len(o) != n {
		t.Fatalf("want %d keys, got %d: %v", n, len(o), err)
	}
}

func TestExactKeysOnly(t *testing.T) {
	r := newRig(t)
	for _, path := range []string{"/settings/global", "/devices/" + r.dev + "/settings"} {
		if st, b := r.do("PUT", path, `{"TTS":"x"}`); st != 422 || b != `{"error":"settings_invalid"}` {
			t.Fatalf("%s: %d %s", path, st, b)
		}
	}
	if o, err := r.s.Device(context.Background(), r.dev); err != nil || len(o) != 0 {
		t.Fatalf("stored %v %v", o, err)
	}
	if o, err := r.s.Global(context.Background()); err != nil || len(o) != 0 {
		t.Fatalf("stored %v %v", o, err)
	}
}

func TestDeviceRejectsEmptyRequiredKeys(t *testing.T) {
	r := newRig(t)
	for _, k := range []string{"mode", "modeRevert", "language", "timeZone", "hermesApprovals"} {
		if st, b := r.do("PUT", "/devices/"+r.dev+"/settings", `{"`+k+`":""}`); st != 422 || b != `{"error":"settings_invalid"}` {
			t.Fatalf("%s: %d %s", k, st, b)
		}
	}
	r.put("/devices/"+r.dev+"/settings", `{"mode":"smart"}`)
	r.put("/devices/"+r.dev+"/settings", `{"mode":null}`)
}

func TestModeMustBeAllowed(t *testing.T) {
	r := newRig(t)
	r.addMode("turbo")
	bad := func(body string) {
		t.Helper()
		if st, b := r.do("PUT", "/devices/"+r.dev+"/settings", body); st != 422 || b != `{"error":"settings_invalid"}` {
			t.Fatalf("%s: %d %s", body, st, b)
		}
	}
	bad(`{"mode":"turbo"}`)
	r.put("/devices/"+r.dev+"/settings", `{"mode":"smart"}`)
	bad(`{"allowedModes":["fast"]}`)
	r.put("/settings/global", `{"allowedModes":["fast","smart","turbo"]}`)
	r.put("/devices/"+r.dev+"/settings", `{"mode":"turbo"}`)
	if got := r.effective(); got.Mode != "turbo" {
		t.Fatalf("%+v", got)
	}
}

func TestEmbedKey(t *testing.T) {
	r := newRig(t)
	if got := r.effective(); got.Embed != "" {
		t.Fatalf("default %q", got.Embed)
	}
	r.put("/settings/global", `{"embed":"mem"}`)
	if got := r.effective(); got.Embed != "mem" {
		t.Fatalf("global %q", got.Embed)
	}
	r.put("/devices/"+r.dev+"/settings", `{"embed":"other"}`)
	if got := r.effective(); got.Embed != "other" {
		t.Fatalf("device %q", got.Embed)
	}
	// a device can switch memory recall off with an empty override
	r.put("/devices/"+r.dev+"/settings", `{"embed":""}`)
	if got := r.effective(); got.Embed != "" {
		t.Fatalf("device off %q", got.Embed)
	}
	r.put("/settings/global", `{"embed":""}`)
	if o, err := r.s.Global(context.Background()); err != nil || len(o) != 0 {
		t.Fatalf("global empty is unset: %v %v", o, err)
	}
	if st, b := r.do("PUT", "/settings/global", `{"embed":5}`); st != 422 || b != `{"error":"settings_invalid"}` {
		t.Fatalf("%d %s", st, b)
	}
}

func TestMemoryScopesKey(t *testing.T) {
	r := newRig(t)
	if got := r.effective(); got.MemoryScopes != nil {
		t.Fatalf("default %v", got.MemoryScopes)
	}
	r.put("/settings/global", `{"memoryScopes":["device"]}`)
	if got := r.effective(); !slices.Equal(got.MemoryScopes, []string{"device"}) {
		t.Fatalf("global %v", got.MemoryScopes)
	}
	// an empty list is memory off, not unset
	r.put("/devices/"+r.dev+"/settings", `{"memoryScopes":[]}`)
	if got := r.effective(); got.MemoryScopes == nil || len(got.MemoryScopes) != 0 {
		t.Fatalf("device %#v", got.MemoryScopes)
	}
	r.put("/devices/"+r.dev+"/settings", `{"memoryScopes":null}`)
	if got := r.effective(); !slices.Equal(got.MemoryScopes, []string{"device"}) {
		t.Fatalf("override removed %v", got.MemoryScopes)
	}
	for _, body := range []string{`{"memoryScopes":["room"]}`, `{"memoryScopes":["global","global"]}`, `{"memoryScopes":"global"}`} {
		if st, b := r.do("PUT", "/settings/global", body); st != 422 || b != `{"error":"settings_invalid"}` {
			t.Fatalf("%s: %d %s", body, st, b)
		}
	}
}

func TestCompactionKeys(t *testing.T) {
	r := newRig(t)
	if got := r.effective(); got.CompactAfter != 10 || got.CompactionMaxTokens != settings.DefaultCompactionMaxTokens ||
		got.MaintenanceModel != "" || got.HistoryTurns != 20 {
		t.Fatalf("defaults %+v", got)
	}
	r.put("/settings/global", `{"compactAfter":1,"compactionMaxTokens":16384,"maintenanceModel":"cheap-model","historyTurns":50}`)
	if got := r.effective(); got.CompactAfter != 1 || got.CompactionMaxTokens != 16384 ||
		got.MaintenanceModel != "cheap-model" || got.HistoryTurns != 50 {
		t.Fatalf("global %+v", got)
	}
	r.put("/devices/"+r.dev+"/settings", `{"compactAfter":120,"compactionMaxTokens":4096,"maintenanceModel":""}`)
	if got := r.effective(); got.CompactAfter != 120 || got.CompactionMaxTokens != 4096 || got.MaintenanceModel != "" {
		t.Fatalf("device %+v", got)
	}
	r.put("/devices/"+r.dev+"/settings", `{"compactAfter":null,"compactionMaxTokens":null}`)
	if got := r.effective(); got.CompactAfter != 1 || got.CompactionMaxTokens != 16384 {
		t.Fatalf("override removed %+v", got)
	}
	r.put("/settings/global", `{"compactAfter":null}`)
	if got := r.effective(); got.CompactAfter != 10 {
		t.Fatalf("global unset %+v", got)
	}
	for _, body := range []string{
		`{"compactAfter":0}`, `{"compactAfter":121}`, `{"compactAfter":-1}`, `{"compactAfter":"10"}`, `{"compactAfter":1.5}`,
		`{"compactionMaxTokens":63}`, `{"compactionMaxTokens":32769}`, `{"compactionMaxTokens":0}`, `{"compactionMaxTokens":"8192"}`,
		`{"historyTurns":51}`, `{"maintenanceModel":5}`,
	} {
		for _, path := range []string{"/settings/global", "/devices/" + r.dev + "/settings"} {
			if st, b := r.do("PUT", path, body); st != 422 || b != `{"error":"settings_invalid"}` {
				t.Fatalf("%s %s: %d %s", path, body, st, b)
			}
		}
	}
}

func TestMemoryApprovalKey(t *testing.T) {
	r := newRig(t)
	if got := r.effective(); got.MemoryApproval {
		t.Fatal("approval on by default")
	}
	r.put("/settings/global", `{"memoryApproval":true}`)
	if got := r.effective(); !got.MemoryApproval {
		t.Fatal("global on not applied")
	}
	r.put("/devices/"+r.dev+"/settings", `{"memoryApproval":false}`)
	if got := r.effective(); got.MemoryApproval {
		t.Fatal("device off not applied")
	}
	// the global value stays the global one whatever a device sets
	if g, err := r.s.EffectiveGlobal(context.Background()); err != nil || !g.MemoryApproval || g.HistoryTurns != 20 {
		t.Fatalf("global %+v %v", g, err)
	}
	r.put("/devices/"+r.dev+"/settings", `{"memoryApproval":null}`)
	if got := r.effective(); !got.MemoryApproval {
		t.Fatal("override removed")
	}
	for _, body := range []string{`{"memoryApproval":"yes"}`, `{"memoryApproval":1}`} {
		if st, b := r.do("PUT", "/settings/global", body); st != 422 || b != `{"error":"settings_invalid"}` {
			t.Fatalf("%s: %d %s", body, st, b)
		}
	}
}

func TestHermesKeyIsGlobalOnly(t *testing.T) {
	r := newRig(t)
	if g, _ := r.s.EffectiveGlobal(context.Background()); g.Hermes != "" {
		t.Fatalf("default %q", g.Hermes)
	}
	r.put("/settings/global", `{"hermes":"agent"}`)
	if g, err := r.s.EffectiveGlobal(context.Background()); err != nil || g.Hermes != "agent" {
		t.Fatalf("global %+v %v", g, err)
	}
	// one hermes serves every device, so a device cannot pick its own
	for _, body := range []string{`{"hermes":"other"}`, `{"hermes":""}`, `{"hermes":null}`} {
		if st, b := r.do("PUT", "/devices/"+r.dev+"/settings", body); st != 422 || b != `{"error":"settings_invalid"}` {
			t.Fatalf("device %s: %d %s", body, st, b)
		}
	}
	if st, b := r.do("PUT", "/settings/global", `{"hermes":5}`); st != 422 || b != `{"error":"settings_invalid"}` {
		t.Fatalf("%d %s", st, b)
	}
}

func TestHomeAssistantKeyIsGlobalOnly(t *testing.T) {
	r := newRig(t)
	if g, _ := r.s.EffectiveGlobal(context.Background()); g.HomeAssistant != "" {
		t.Fatalf("default %q", g.HomeAssistant)
	}
	r.put("/settings/global", `{"homeAssistant":"home"}`)
	if g, err := r.s.EffectiveGlobal(context.Background()); err != nil || g.HomeAssistant != "home" {
		t.Fatalf("global %+v %v", g, err)
	}
	// one home assistant serves every device
	for _, body := range []string{`{"homeAssistant":"other"}`, `{"homeAssistant":""}`, `{"homeAssistant":null}`} {
		if st, b := r.do("PUT", "/devices/"+r.dev+"/settings", body); st != 422 || b != `{"error":"settings_invalid"}` {
			t.Fatalf("device %s: %d %s", body, st, b)
		}
	}
	if st, b := r.do("PUT", "/settings/global", `{"homeAssistant":5}`); st != 422 || b != `{"error":"settings_invalid"}` {
		t.Fatalf("%d %s", st, b)
	}
}

func TestHAAreaKeyIsDeviceOnly(t *testing.T) {
	r := newRig(t)
	if got := r.effective(); got.HAArea != "" {
		t.Fatalf("default %q", got.HAArea)
	}
	r.put("/devices/"+r.dev+"/settings", `{"haArea":"living_room_2"}`)
	if got := r.effective(); got.HAArea != "living_room_2" {
		t.Fatalf("device %q", got.HAArea)
	}
	for _, body := range []string{
		`{"haArea":""}`, `{"haArea":"Living Room"}`, `{"haArea":"кухня"}`, `{"haArea":"a-b"}`, `{"haArea":5}`,
		`{"haArea":"` + strings.Repeat("a", 65) + `"}`,
	} {
		if st, b := r.do("PUT", "/devices/"+r.dev+"/settings", body); st != 422 || b != `{"error":"settings_invalid"}` {
			t.Fatalf("device %s: %d %s", body, st, b)
		}
	}
	r.put("/devices/"+r.dev+"/settings", `{"haArea":"`+strings.Repeat("a", 64)+`"}`)
	// an area is where one device stands, never every device's
	for _, body := range []string{`{"haArea":"kitchen"}`, `{"haArea":null}`} {
		if st, b := r.do("PUT", "/settings/global", body); st != 422 || b != `{"error":"settings_invalid"}` {
			t.Fatalf("global %s: %d %s", body, st, b)
		}
	}
	r.put("/devices/"+r.dev+"/settings", `{"haArea":null}`)
	if got := r.effective(); got.HAArea != "" {
		t.Fatalf("removed %q", got.HAArea)
	}
}

func TestHASensitiveKeyIsGlobalOnly(t *testing.T) {
	r := newRig(t)
	if got := r.effective(); got.HASensitive != settings.HASensitiveWoken {
		t.Fatalf("default %q", got.HASensitive)
	}
	r.put("/settings/global", `{"haSensitive":"deny"}`)
	if got := r.effective(); got.HASensitive != settings.HASensitiveDeny {
		t.Fatalf("global %q", got.HASensitive)
	}
	// unset brings the default back
	r.put("/settings/global", `{"haSensitive":""}`)
	if got := r.effective(); got.HASensitive != settings.HASensitiveWoken {
		t.Fatalf("unset %q", got.HASensitive)
	}
	for _, body := range []string{`{"haSensitive":"always"}`, `{"haSensitive":"DENY"}`, `{"haSensitive":true}`, `{"haSensitive":["deny"]}`} {
		if st, b := r.do("PUT", "/settings/global", body); st != 422 || b != `{"error":"settings_invalid"}` {
			t.Fatalf("global %s: %d %s", body, st, b)
		}
	}
	// one policy for the whole home: a speaker in a follow-up-prone room cannot loosen it
	for _, body := range []string{`{"haSensitive":"woken"}`, `{"haSensitive":"deny"}`, `{"haSensitive":null}`} {
		if st, b := r.do("PUT", "/devices/"+r.dev+"/settings", body); st != 422 || b != `{"error":"settings_invalid"}` {
			t.Fatalf("device %s: %d %s", body, st, b)
		}
	}
}

func TestHAFavoritesKeyIsGlobalOnly(t *testing.T) {
	r := newRig(t)
	if got := r.effective(); got.HAFavorites != nil {
		t.Fatalf("default %v", got.HAFavorites)
	}
	long := "sensor." + strings.Repeat("a", 121)
	r.put("/settings/global", `{"haFavorites":["light.kitchen","climate.living_room_2","`+long+`"]}`)
	if got := r.effective(); !slices.Equal(got.HAFavorites, []string{"light.kitchen", "climate.living_room_2", long}) {
		t.Fatalf("global %v", got.HAFavorites)
	}
	ids := make([]string, 32)
	for i := range ids {
		ids[i] = `"light.l` + strings.Repeat("x", i) + `"`
	}
	r.put("/settings/global", `{"haFavorites":[`+strings.Join(ids, ",")+`]}`)
	if got := r.effective(); len(got.HAFavorites) != 32 {
		t.Fatalf("32 favorites: %d", len(got.HAFavorites))
	}
	r.put("/settings/global", `{"haFavorites":[]}`)
	if got := r.effective(); got.HAFavorites == nil || len(got.HAFavorites) != 0 {
		t.Fatalf("empty %v", got.HAFavorites)
	}
	for _, body := range []string{
		`{"haFavorites":"light.kitchen"}`, `{"haFavorites":[5]}`, `{"haFavorites":[null]}`, `{"haFavorites":[""]}`,
		`{"haFavorites":["kitchen"]}`, `{"haFavorites":["Light.Kitchen"]}`, `{"haFavorites":["light.kitchen-2"]}`,
		`{"haFavorites":["1light.kitchen"]}`, `{"haFavorites":["light.kitchen "]}`, `{"haFavorites":["light.кухня"]}`,
		`{"haFavorites":["light."]}`, `{"haFavorites":["` + long + `a"]}`,
		`{"haFavorites":["light.kitchen","light.hall","light.kitchen"]}`,
		`{"haFavorites":[` + strings.Join(ids, ",") + `,"light.one_more"]}`,
	} {
		if st, b := r.do("PUT", "/settings/global", body); st != 422 || b != `{"error":"settings_invalid"}` {
			t.Fatalf("global %.80s: %d %s", body, st, b)
		}
	}
	// the favorites are the household's, never one speaker's
	for _, body := range []string{`{"haFavorites":["light.kitchen"]}`, `{"haFavorites":[]}`, `{"haFavorites":null}`} {
		if st, b := r.do("PUT", "/devices/"+r.dev+"/settings", body); st != 422 || b != `{"error":"settings_invalid"}` {
			t.Fatalf("device %s: %d %s", body, st, b)
		}
	}
}

func TestHermesApprovalsKey(t *testing.T) {
	r := newRig(t)
	if got := r.effective(); got.HermesApprovals != "voice" {
		t.Fatalf("default %q", got.HermesApprovals)
	}
	r.put("/settings/global", `{"hermesApprovals":"dashboard_only"}`)
	if got := r.effective(); got.HermesApprovals != "dashboard_only" {
		t.Fatalf("global %q", got.HermesApprovals)
	}
	// a device may differ: a speaker in a shared room can be kept off approvals
	r.put("/devices/"+r.dev+"/settings", `{"hermesApprovals":"voice"}`)
	if got := r.effective(); got.HermesApprovals != "voice" {
		t.Fatalf("device %q", got.HermesApprovals)
	}
	for _, body := range []string{`{"hermesApprovals":"always"}`, `{"hermesApprovals":"VOICE"}`, `{"hermesApprovals":true}`} {
		for _, path := range []string{"/settings/global", "/devices/" + r.dev + "/settings"} {
			if st, b := r.do("PUT", path, body); st != 422 || b != `{"error":"settings_invalid"}` {
				t.Fatalf("%s %s: %d %s", path, body, st, b)
			}
		}
	}
}

func TestQuietHoursKey(t *testing.T) {
	r := newRig(t)
	r.put("/settings/global", `{"quietHours":{"enabled":true,"start":"22:30","end":"07:00"}}`)
	if got := r.effective().QuietHours; got != (settings.QuietHours{Enabled: true, Start: "22:30", End: "07:00"}) {
		t.Fatalf("global %+v", got)
	}
	// the whole object is one key: a device replaces it, null brings the global one back
	r.put("/devices/"+r.dev+"/settings", `{"quietHours":{"enabled":false,"start":"00:00","end":"06:00"}}`)
	if got := r.effective().QuietHours; got != (settings.QuietHours{Start: "00:00", End: "06:00"}) {
		t.Fatalf("device %+v", got)
	}
	r.put("/devices/"+r.dev+"/settings", `{"quietHours":null}`)
	if got := r.effective().QuietHours; got.Start != "22:30" {
		t.Fatalf("override removed %+v", got)
	}
	for _, body := range []string{
		`{"quietHours":true}`, `{"quietHours":""}`, `{"quietHours":{}}`,
		`{"quietHours":{"enabled":true,"start":"23:00"}}`,
		`{"quietHours":{"enabled":"yes","start":"23:00","end":"08:00"}}`,
		`{"quietHours":{"enabled":true,"start":"23:00","end":"08:00","days":[]}}`,
		`{"quietHours":{"enabled":true,"start":"24:00","end":"08:00"}}`,
		`{"quietHours":{"enabled":true,"start":"23:60","end":"08:00"}}`,
		`{"quietHours":{"enabled":true,"start":"7:00","end":"08:00"}}`,
		`{"quietHours":{"enabled":true,"start":"08:00","end":"08:00"}}`,
		`{"quietHours":{"enabled":true,"start":null,"end":"08:00"}}`,
	} {
		for _, path := range []string{"/settings/global", "/devices/" + r.dev + "/settings"} {
			if st, b := r.do("PUT", path, body); st != 422 || b != `{"error":"settings_invalid"}` {
				t.Fatalf("%s %s: %d %s", path, body, st, b)
			}
		}
	}
}

func TestFollowUpKey(t *testing.T) {
	r := newRig(t)
	r.put("/settings/global", `{"followUp":{"enabled":true,"windowMs":2000,"limit":10}}`)
	if got := r.effective().FollowUp; got != (settings.FollowUp{Enabled: true, WindowMs: 2000, Limit: 10}) {
		t.Fatalf("global %+v", got)
	}
	// the whole object is one key: a device replaces it, null brings the global one back
	r.put("/devices/"+r.dev+"/settings", `{"followUp":{"enabled":false,"windowMs":15000,"limit":1}}`)
	if got := r.effective().FollowUp; got != (settings.FollowUp{WindowMs: 15000, Limit: 1}) {
		t.Fatalf("device %+v", got)
	}
	r.put("/devices/"+r.dev+"/settings", `{"followUp":null}`)
	if got := r.effective().FollowUp; got.WindowMs != 2000 {
		t.Fatalf("override removed %+v", got)
	}
	r.put("/settings/global", `{"followUp":null}`)
	if got := r.effective().FollowUp; got != settings.Defaults().FollowUp {
		t.Fatalf("defaults %+v", got)
	}
	for _, body := range []string{
		`{"followUp":true}`, `{"followUp":""}`, `{"followUp":{}}`,
		`{"followUp":{"enabled":true,"windowMs":5000}}`,
		`{"followUp":{"enabled":true,"limit":3}}`,
		`{"followUp":{"windowMs":5000,"limit":3}}`,
		`{"followUp":{"enabled":true,"windowMs":5000,"limit":3,"chain":0}}`,
		`{"followUp":{"enabled":"yes","windowMs":5000,"limit":3}}`,
		`{"followUp":{"enabled":null,"windowMs":5000,"limit":3}}`,
		`{"followUp":{"enabled":true,"windowMs":1999,"limit":3}}`,
		`{"followUp":{"enabled":true,"windowMs":15001,"limit":3}}`,
		`{"followUp":{"enabled":true,"windowMs":5000.5,"limit":3}}`,
		`{"followUp":{"enabled":true,"windowMs":5000,"limit":0}}`,
		`{"followUp":{"enabled":true,"windowMs":5000,"limit":11}}`,
	} {
		for _, path := range []string{"/settings/global", "/devices/" + r.dev + "/settings"} {
			if st, b := r.do("PUT", path, body); st != 422 || b != `{"error":"settings_invalid"}` {
				t.Fatalf("%s %s: %d %s", path, body, st, b)
			}
		}
	}
}

// the alarms key: any of its keys within the device's bounds, at either scope, the device's whole over the global one
func TestAlarmsKey(t *testing.T) {
	r := newRig(t)
	r.put("/settings/global", `{"alarms":{"rampSeconds":300,"rampFrom":"quiet","alarmLimitSeconds":900,"timerLimitSeconds":30,`+
		`"reminderLimitSeconds":60,"snoozeMinutes":5,"autoSnooze":2}}`)
	if got := r.effective().Alarms; got.RampSeconds == nil || *got.RampSeconds != 300 || *got.RampFrom != "quiet" {
		t.Fatalf("global %+v", got)
	}
	r.put("/devices/"+r.dev+"/settings", `{"alarms":{}}`)
	if got := r.effective().Alarms; got.RampSeconds != nil {
		t.Fatalf("device %+v", got)
	}
	for _, body := range []string{
		`{"alarms":true}`, `{"alarms":{"rampSeconds":-1}}`, `{"alarms":{"rampSeconds":1801}}`,
		`{"alarms":{"rampFrom":"loud"}}`, `{"alarms":{"alarmLimitSeconds":59}}`, `{"alarms":{"timerLimitSeconds":601}}`,
		`{"alarms":{"reminderLimitSeconds":9}}`, `{"alarms":{"snoozeMinutes":0}}`, `{"alarms":{"autoSnooze":6}}`,
		`{"alarms":{"snoozeMinutes":null}}`, `{"alarms":{"timeZone":"UTC"}}`, `{"alarms":{"snoozeMinutes":5.5}}`,
	} {
		for _, path := range []string{"/settings/global", "/devices/" + r.dev + "/settings"} {
			if st, b := r.do("PUT", path, body); st != 422 || b != `{"error":"settings_invalid"}` {
				t.Fatalf("%s %s: %d %s", path, body, st, b)
			}
		}
	}
}

func TestTracingKey(t *testing.T) {
	r := newRig(t)
	r.put("/settings/global", `{"tracing":{"enabled":false,"sampleRatio":0.25,"ringSize":10}}`)
	if g, err := r.s.EffectiveGlobal(context.Background()); err != nil || g.Tracing != (settings.Tracing{SampleRatio: 0.25, RingSize: 10}) {
		t.Fatalf("global %+v %v", g.Tracing, err)
	}
	r.put("/settings/global", `{"tracing":null}`)
	if g, _ := r.s.EffectiveGlobal(context.Background()); g.Tracing != settings.Defaults().Tracing {
		t.Fatalf("defaults %+v", g.Tracing)
	}
	for _, body := range []string{
		`{"tracing":true}`, `{"tracing":{}}`, `{"tracing":{"enabled":true,"sampleRatio":1}}`,
		`{"tracing":{"enabled":true,"sampleRatio":1.5,"ringSize":500}}`,
		`{"tracing":{"enabled":true,"sampleRatio":-0.1,"ringSize":500}}`,
		`{"tracing":{"enabled":true,"sampleRatio":1,"ringSize":9}}`,
		`{"tracing":{"enabled":true,"sampleRatio":1,"ringSize":5001}}`,
		`{"tracing":{"enabled":true,"sampleRatio":1,"ringSize":500,"budget":1}}`,
		`{"tracing":{"enabled":null,"sampleRatio":1,"ringSize":500}}`,
	} {
		if st, b := r.do("PUT", "/settings/global", body); st != 422 || b != `{"error":"settings_invalid"}` {
			t.Fatalf("%s: %d %s", body, st, b)
		}
	}
	// global only: a device cannot turn the server's tracing
	if st, _ := r.do("PUT", "/devices/"+r.dev+"/settings", `{"tracing":{"enabled":false,"sampleRatio":1,"ringSize":500}}`); st != 422 {
		t.Fatalf("device set: %d", st)
	}
}

func TestMCPKeyIsGlobalOnly(t *testing.T) {
	r := newRig(t)
	if g, _ := r.s.EffectiveGlobal(context.Background()); g.MCP != nil {
		t.Fatalf("default %v", g.MCP)
	}
	r.put("/settings/global", `{"mcp":["github","notes.v2"]}`)
	if g, err := r.s.EffectiveGlobal(context.Background()); err != nil || !slices.Equal(g.MCP, []string{"github", "notes.v2"}) {
		t.Fatalf("global %v %v", g.MCP, err)
	}
	names := make([]string, 32)
	for i := range names {
		names[i] = fmt.Sprintf(`"s%d"`, i)
	}
	r.put("/settings/global", `{"mcp":[`+strings.Join(names, ",")+`]}`)
	r.put("/settings/global", `{"mcp":[]}`)
	for _, body := range []string{
		`{"mcp":"github"}`, `{"mcp":[5]}`, `{"mcp":[""]}`, `{"mcp":["bad name"]}`, `{"mcp":[".dot"]}`,
		`{"mcp":["` + strings.Repeat("a", 65) + `"]}`, `{"mcp":["a","b","a"]}`,
		`{"mcp":[` + strings.Join(names, ",") + `,"one_more"]}`,
	} {
		if st, b := r.do("PUT", "/settings/global", body); st != 422 || b != `{"error":"settings_invalid"}` {
			t.Fatalf("global %.80s: %d %s", body, st, b)
		}
	}
	// the servers run once for the household, never per speaker
	for _, body := range []string{`{"mcp":["github"]}`, `{"mcp":[]}`, `{"mcp":null}`} {
		if st, b := r.do("PUT", "/devices/"+r.dev+"/settings", body); st != 422 || b != `{"error":"settings_invalid"}` {
			t.Fatalf("device %s: %d %s", body, st, b)
		}
	}
}

func TestCheckMCPRefusesNewNamesOnly(t *testing.T) {
	r := newRig(t)
	var checked []string
	refuse := map[string]error{"gone": settings.ErrInvalid, "down": errors.New("db down")}
	r.s.CheckMCP(func(_ context.Context, name string) error {
		checked = append(checked, name)
		return refuse[name]
	})
	r.put("/settings/global", `{"mcp":["a"]}`)
	r.put("/settings/global", `{"mcp":["a","b"]}`)
	if !slices.Equal(checked, []string{"a", "b"}) {
		t.Fatalf("checked %v", checked)
	}
	r.addInstances("gone")
	if st, b := r.do("PUT", "/settings/global", `{"mcp":["a","b","gone"]}`); st != 422 || b != invalidAt("mcp") {
		t.Fatalf("gone: %d %s", st, b)
	}
	if st, b := r.do("PUT", "/settings/global", `{"mcp":["down"]}`); st != 500 || b != `{"error":"internal"}` {
		t.Fatalf("down: %d %s", st, b)
	}
	if g, _ := r.s.EffectiveGlobal(context.Background()); !slices.Equal(g.MCP, []string{"a", "b"}) {
		t.Fatalf("a refused list was stored: %v", g.MCP)
	}
	// a name listed before passes though its instance went away since, so it
	// never blocks the other keys
	refuse["a"] = settings.ErrInvalid
	checked = nil
	r.put("/settings/global", `{"mcp":["a"],"language":"ru"}`)
	r.put("/settings/global", `{"language":"en"}`)
	if len(checked) != 0 {
		t.Fatalf("checked %v", checked)
	}
}

// t3code is a global list of instance names, each checked when it is new,
// and an instance it names counts as in use
func TestT3CodeKey(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	refuse := map[string]error{"notes": settings.ErrInvalid}
	var checked []string
	r.s.CheckList("t3code", func(_ context.Context, name string) error {
		checked = append(checked, name)
		return refuse[name]
	})
	r.addInstances("work", "notes")
	r.put("/settings/global", `{"t3code":["work"]}`)
	if g, err := r.s.EffectiveGlobal(ctx); err != nil || !slices.Equal(g.T3Code, []string{"work"}) {
		t.Fatalf("global %v %v", g.T3Code, err)
	}
	if named, err := settings.NamesInstance(ctx, r.q, "work"); err != nil || !named {
		t.Fatalf("named %v %v", named, err)
	}
	if st, b := r.do("PUT", "/settings/global", `{"t3code":["work","notes"]}`); st != 422 || b != invalidAt("t3code") {
		t.Fatalf("refused name: %d %s", st, b)
	}
	if !slices.Equal(checked, []string{"work", "notes"}) {
		t.Fatalf("checked %v", checked)
	}
	for _, body := range []string{`{"t3code":["bad name"]}`, `{"t3code":["a","a"]}`, `{"t3code":[1]}`} {
		if st, b := r.do("PUT", "/settings/global", body); st != 422 || b != `{"error":"settings_invalid"}` {
			t.Fatalf("global %s: %d %s", body, st, b)
		}
	}
	if st, b := r.do("PUT", "/devices/"+r.dev+"/settings", `{"t3code":["work"]}`); st != 422 || b != `{"error":"settings_invalid"}` {
		t.Fatalf("device: %d %s", st, b)
	}
	r.put("/settings/global", `{"t3code":[]}`)
	if named, _ := settings.NamesInstance(ctx, r.q, "work"); named {
		t.Fatal("named once the list is empty")
	}
}

func TestOnGlobalChangeHearsAStoredGlobalSet(t *testing.T) {
	r := newRig(t)
	calls := 0
	r.s.OnGlobalChange(func() { calls++ })
	r.put("/settings/global", `{"language":"ru"}`)
	if st, _ := r.do("PUT", "/settings/global", `{"language":5}`); st != 422 {
		t.Fatalf("status %d", st)
	}
	r.put("/devices/"+r.dev+"/settings", `{"language":"en"}`)
	if calls != 1 {
		t.Fatalf("heard %d changes", calls)
	}
}

func TestOnDeviceChangeHearsAStoredDeviceSet(t *testing.T) {
	r := newRig(t)
	var heard []string
	r.s.OnDeviceChange(func(id string) { heard = append(heard, id) })
	r.put("/devices/"+r.dev+"/settings", `{"language":"en"}`)
	if st, _ := r.do("PUT", "/devices/"+r.dev+"/settings", `{"language":5}`); st != 422 {
		t.Fatalf("status %d", st)
	}
	r.put("/settings/global", `{"language":"ru"}`)
	if len(heard) != 1 || heard[0] != r.dev {
		t.Fatalf("heard %v", heard)
	}
}

func TestLogRetentionDaysKeyIsGlobalOnly(t *testing.T) {
	r := newRig(t)
	if g, _ := r.s.EffectiveGlobal(context.Background()); g.LogRetentionDays != 7 {
		t.Fatalf("default %d", g.LogRetentionDays)
	}
	for _, days := range []int{1, 30} {
		r.put("/settings/global", fmt.Sprintf(`{"logRetentionDays":%d}`, days))
		if g, err := r.s.EffectiveGlobal(context.Background()); err != nil || g.LogRetentionDays != days {
			t.Fatalf("global %d: %d %v", days, g.LogRetentionDays, err)
		}
	}
	// the typed reply answers the number as a number
	if st, b := r.do("GET", "/settings/global", ""); st != 200 || b != `{"logRetentionDays":30}` {
		t.Fatalf("reply: %d %s", st, b)
	}
	r.put("/settings/global", `{"logRetentionDays":null}`)
	if g, _ := r.s.EffectiveGlobal(context.Background()); g.LogRetentionDays != 7 {
		t.Fatalf("unset %d", g.LogRetentionDays)
	}
	for _, body := range []string{
		`{"logRetentionDays":0}`, `{"logRetentionDays":31}`, `{"logRetentionDays":-1}`, `{"logRetentionDays":1.5}`,
		`{"logRetentionDays":"7"}`, `{"logRetentionDays":true}`,
	} {
		if st, b := r.do("PUT", "/settings/global", body); st != 422 || b != `{"error":"settings_invalid"}` {
			t.Fatalf("global %s: %d %s", body, st, b)
		}
	}
	// the server keeps one log store for every device
	for _, body := range []string{`{"logRetentionDays":3}`, `{"logRetentionDays":null}`} {
		if st, b := r.do("PUT", "/devices/"+r.dev+"/settings", body); st != 422 || b != `{"error":"settings_invalid"}` {
			t.Fatalf("device %s: %d %s", body, st, b)
		}
	}
}

func TestTelemetryExportKeyIsGlobalOnly(t *testing.T) {
	r := newRig(t)
	if g, _ := r.s.EffectiveGlobal(context.Background()); g.TelemetryExport != "" {
		t.Fatalf("default %q", g.TelemetryExport)
	}
	r.put("/settings/global", `{"telemetryExport":"grafana.cloud-1"}`)
	if g, err := r.s.EffectiveGlobal(context.Background()); err != nil || g.TelemetryExport != "grafana.cloud-1" {
		t.Fatalf("global %q %v", g.TelemetryExport, err)
	}
	if st, b := r.do("GET", "/settings/global", ""); st != 200 || b != `{"telemetryExport":"grafana.cloud-1"}` {
		t.Fatalf("reply: %d %s", st, b)
	}
	// "" and null both turn the export off
	for _, off := range []string{`""`, `null`} {
		r.put("/settings/global", `{"telemetryExport":"otel"}`)
		r.put("/settings/global", `{"telemetryExport":`+off+`}`)
		if g, _ := r.s.EffectiveGlobal(context.Background()); g.TelemetryExport != "" {
			t.Fatalf("%s left %q", off, g.TelemetryExport)
		}
	}
	for _, body := range []string{
		`{"telemetryExport":5}`, `{"telemetryExport":["otel"]}`, `{"telemetryExport":"bad name"}`,
		`{"telemetryExport":".dot"}`, `{"telemetryExport":"` + strings.Repeat("a", 65) + `"}`,
	} {
		if st, b := r.do("PUT", "/settings/global", body); st != 422 || b != `{"error":"settings_invalid"}` {
			t.Fatalf("global %s: %d %s", body, st, b)
		}
	}
	// the server exports every device's lines through one collector
	for _, body := range []string{`{"telemetryExport":"otel"}`, `{"telemetryExport":null}`} {
		if st, b := r.do("PUT", "/devices/"+r.dev+"/settings", body); st != 422 || b != `{"error":"settings_invalid"}` {
			t.Fatalf("device %s: %d %s", body, st, b)
		}
	}
}

// a device's settings write that races its delete stores nothing: the write
// waits for the delete's transaction, then finds no device (no orphan row)
func TestPutDeviceWaitsForItsDelete(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `DELETE FROM devices WHERE id = $1::uuid`, r.dev); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := r.s.PutDevice(ctx, r.dev, []byte(`{"voice":"v"}`))
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("wrote beside an open delete: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, settings.ErrNotFound) {
			t.Fatalf("after the delete: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("write still waiting")
	}
	var n int
	if err := r.pool.QueryRow(ctx, `SELECT count(*) FROM settings WHERE scope = $1`, r.dev).Scan(&n); err != nil || n != 0 {
		t.Fatalf("%d orphan rows, %v", n, err)
	}
}

func (r *rig) addMode(name string) {
	r.t.Helper()
	if err := r.q.PutAgentMode(context.Background(), db.PutAgentModeParams{
		Name: name, Reasoning: "low", MaxSteps: 3, MaxSeconds: 30, Interim: "speak", Aliases: []byte(`{}`),
	}); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) dropMode(name string) {
	r.t.Helper()
	if _, err := r.q.DeleteAgentMode(context.Background(), name); err != nil {
		r.t.Fatal(err)
	}
}

// a write that names a mode its scope did not name before needs that mode's
// row; the names it named before pass, so a row gone since blocks nothing
func TestSettingsNameOnlyModesThatExist(t *testing.T) {
	r := newRig(t)
	dev := "/devices/" + r.dev + "/settings"
	for _, c := range [][3]string{
		{"/settings/global", `{"allowedModes":["fast","turbo"]}`, "allowedModes"},
		{"/settings/global", `{"mode":"turbo"}`, "mode"},
		{"/settings/global", `{"compactionMode":"turbo"}`, "compactionMode"},
		{"/settings/global", `{"memoryMode":"turbo"}`, "memoryMode"},
		{dev, `{"allowedModes":["fast","turbo"]}`, "allowedModes"},
		{dev, `{"compactionMode":"turbo"}`, "compactionMode"},
		{dev, `{"memoryMode":"turbo"}`, "memoryMode"},
	} {
		if st, b := r.do("PUT", c[0], c[1]); st != 422 || b != invalidAt(c[2]) {
			t.Fatalf("%s %s: %d %s", c[0], c[1], st, b)
		}
	}
	if o, err := r.s.Global(context.Background()); err != nil || len(o) != 0 {
		t.Fatalf("stored %v %v", o, err)
	}
	r.addMode("turbo")
	r.put("/settings/global", `{"allowedModes":["fast","turbo"],"compactionMode":"turbo"}`)
	r.put(dev, `{"allowedModes":["fast","turbo"],"mode":"turbo"}`)
	if names, err := settings.NamedModes(context.Background(), r.q); err != nil || !slices.Contains(names, "turbo") {
		t.Fatalf("compaction mode not held: %v %v", names, err)
	}
	r.dropMode("turbo")
	r.put("/settings/global", `{"allowedModes":["fast","turbo"],"persona":"p"}`)
	r.put(dev, `{"persona":"p"}`)
	r.put(dev, `{"mode":"fast","allowedModes":null}`)
	if st, b := r.do("PUT", dev, `{"mode":"turbo"}`); st != 422 || b != invalidAt("mode") {
		t.Fatalf("a gone mode named again: %d %s", st, b)
	}
	// dropping the global list brings back the default one, which names smart
	r.put("/settings/global", `{"allowedModes":["fast"]}`)
	r.dropMode("smart")
	if st, b := r.do("PUT", "/settings/global", `{"persona":"p"}`); st != 422 || b != invalidAt("allowedModes") {
		t.Fatalf("the default list named a gone mode: %d %s", st, b)
	}
}

// invalidAt is the refusal of a key that names what is not there
func invalidAt(key string) string { return `{"error":"settings_invalid","path":"/body/` + key + `"}` }

// a key that names a provider instance it did not name before needs that
// instance's row; what it named before passes, so an instance gone since
// blocks nothing
func TestSettingsNameOnlyInstancesThatExist(t *testing.T) {
	r := newRig(t)
	dev := "/devices/" + r.dev + "/settings"
	global := []string{"stt", "tts", "llm", "embed", "weather", "search", "hermes", "homeAssistant", "telemetryExport"}
	for _, k := range global {
		body := `{"` + k + `":"gone"}`
		if st, b := r.do("PUT", "/settings/global", body); st != 422 || b != invalidAt(k) {
			t.Fatalf("global %s: %d %s", body, st, b)
		}
		// a device sets the ones it may set
		if slices.Contains([]string{"hermes", "homeAssistant", "telemetryExport"}, k) {
			continue
		}
		if st, b := r.do("PUT", dev, body); st != 422 || b != invalidAt(k) {
			t.Fatalf("device %s: %d %s", body, st, b)
		}
	}
	if st, b := r.do("PUT", "/settings/global", `{"mcp":["a","gone"]}`); st != 422 || b != invalidAt("mcp") {
		t.Fatalf("mcp: %d %s", st, b)
	}
	if o, err := r.s.Global(context.Background()); err != nil || len(o) != 0 {
		t.Fatalf("global stored %v %v", o, err)
	}
	if o, err := r.s.Device(context.Background(), r.dev); err != nil || len(o) != 0 {
		t.Fatalf("device stored %v %v", o, err)
	}
	r.addInstances("gone")
	r.put("/settings/global", `{"stt":"gone","mcp":["a","gone"],"hermes":"gone"}`)
	r.put(dev, `{"llm":"gone"}`)
	r.dropInstance("gone")
	// kept as they were, beside another key's change
	r.put("/settings/global", `{"stt":"gone","mcp":["a","gone"],"hermes":"gone","language":"ru"}`)
	r.put(dev, `{"voice":"v"}`)
	// a key names it anew: under another key, or once it was dropped
	for _, c := range [][3]string{
		{"/settings/global", `{"stt":"gone","tts":"gone","mcp":["a","gone"],"hermes":"gone"}`, "tts"},
		{dev, `{"stt":"gone"}`, "stt"},
		{dev, `{"llm":null}`, ""},
		{dev, `{"llm":"gone"}`, "llm"},
	} {
		st, b := r.do("PUT", c[0], c[1])
		if c[2] == "" {
			if st != 200 {
				t.Fatalf("%s %s: %d %s", c[0], c[1], st, b)
			}
			continue
		}
		if st != 422 || b != invalidAt(c[2]) {
			t.Fatalf("%s %s: %d %s", c[0], c[1], st, b)
		}
	}
	// "" names none: a device switches embedding off with it
	r.put(dev, `{"embed":""}`)
}

// a write naming an instance whose delete is open waits for it, then is
// refused: the delete and the write never both land
func TestSettingsWriteWaitsForAProviderDelete(t *testing.T) {
	r := newRig(t)
	r.addInstances("main")
	ctx := context.Background()
	// a delete as providers makes one, open: under the lock, the row gone
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := db.New(tx)
	if err := settings.Lock(ctx, q); err != nil {
		t.Fatal(err)
	}
	if _, err := q.DeleteProviderInstance(ctx, "main"); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 2)
	go func() {
		_, err := r.s.PutGlobal(ctx, []byte(`{"llm":"main"}`))
		done <- err
	}()
	go func() {
		_, err := r.s.PutDevice(ctx, r.dev, []byte(`{"stt":"main"}`))
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("wrote beside an open provider delete: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		select {
		case err := <-done:
			if !errors.Is(err, settings.ErrInvalid) {
				t.Fatalf("after the delete: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("write still waiting")
		}
	}
	var n int
	if err := r.pool.QueryRow(ctx, `SELECT count(*) FROM settings`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("%d rows stored, %v", n, err)
	}
}

// a write naming a mode whose delete is open waits for it, then is refused
func TestSettingsWriteWaitsForAModeDelete(t *testing.T) {
	r := newRig(t)
	r.addMode("turbo")
	ctx := context.Background()
	// a delete as agent makes one, open: under the lock, the row gone
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := db.New(tx)
	if err := q.LockAgentModes(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := q.DeleteAgentMode(ctx, "turbo"); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 2)
	go func() {
		_, err := r.s.PutGlobal(ctx, []byte(`{"allowedModes":["fast","smart","turbo"]}`))
		done <- err
	}()
	go func() {
		_, err := r.s.PutDevice(ctx, r.dev, []byte(`{"allowedModes":["fast","turbo"]}`))
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("wrote beside an open mode delete: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		select {
		case err := <-done:
			if !errors.Is(err, settings.ErrInvalid) {
				t.Fatalf("after the delete: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("write still waiting")
		}
	}
	var n int
	if err := r.pool.QueryRow(ctx, `SELECT count(*) FROM settings`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("%d rows stored, %v", n, err)
	}
}

func TestCatalogVoiceoversKeyIsGlobalOnly(t *testing.T) {
	r := newRig(t)
	if got := r.effective().CatalogVoiceovers; len(got.Prefer) != 0 || len(got.Avoid) != 0 {
		t.Fatalf("default %+v", got)
	}
	r.put("/settings/global", `{"catalogVoiceovers":{"prefer":["AniLibria","anime365","Studio Band"],"avoid":["animevost"]}}`)
	got := r.effective().CatalogVoiceovers
	if !slices.Equal(got.Prefer, []string{"AniLibria", "anime365", "Studio Band"}) || !slices.Equal(got.Avoid, []string{"animevost"}) {
		t.Fatalf("global %+v", got)
	}
	r.put("/settings/global", `{"catalogVoiceovers":{"prefer":["AniDUB"]}}`)
	if got := r.effective().CatalogVoiceovers; !slices.Equal(got.Prefer, []string{"AniDUB"}) || len(got.Avoid) != 0 {
		t.Fatalf("replaced %+v", got)
	}
	long := strings.Repeat("я", 65)
	for _, body := range []string{
		`{"catalogVoiceovers":["AniLibria"]}`, `{"catalogVoiceovers":"AniLibria"}`,
		`{"catalogVoiceovers":{"prefer":["AniLibria"],"order":1}}`,
		`{"catalogVoiceovers":{"prefer":[""]}}`, `{"catalogVoiceovers":{"prefer":["  -  "]}}`,
		`{"catalogVoiceovers":{"prefer":["` + long + `"]}}`,
		// one name twice, case and separators aside, or both preferred and avoided
		`{"catalogVoiceovers":{"prefer":["AniLibria","anilibria"]}}`,
		`{"catalogVoiceovers":{"prefer":["Studio Band"],"avoid":["studio-band"]}}`,
		`{"catalogVoiceovers":{"prefer":[1]}}`,
	} {
		if st, b := r.do("PUT", "/settings/global", body); st != 422 || b != `{"error":"settings_invalid"}` {
			t.Fatalf("%s: %d %s", body, st, b)
		}
	}
	if st, b := r.do("PUT", "/devices/"+r.dev+"/settings", `{"catalogVoiceovers":{"prefer":["AniDUB"]}}`); st != 422 {
		t.Fatalf("device: %d %s", st, b)
	}
}

// youtubeVotes sends video ids to a third party: off unless the owner sets it, for the whole household
func TestYouTubeVotesIsOffAndGlobalOnly(t *testing.T) {
	r := newRig(t)
	if r.effective().YouTubeVotes {
		t.Fatal("on by default")
	}
	r.put("/settings/global", `{"youtubeVotes":true}`)
	if !r.effective().YouTubeVotes {
		t.Fatal("not turned on")
	}
	for _, body := range []string{`{"youtubeVotes":"yes"}`, `{"youtubeVotes":1}`} {
		if st, b := r.do("PUT", "/settings/global", body); st != 422 || b != `{"error":"settings_invalid"}` {
			t.Fatalf("%s: %d %s", body, st, b)
		}
	}
	if st, b := r.do("PUT", "/devices/"+r.dev+"/settings", `{"youtubeVotes":false}`); st != 422 {
		t.Fatalf("device: %d %s", st, b)
	}
	r.put("/settings/global", `{"youtubeVotes":null}`)
	if r.effective().YouTubeVotes {
		t.Fatal("not turned off by removing the key")
	}
}
