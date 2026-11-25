package ha_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/gofiber/fiber/v3"

	"git.shino.boo/devilreef/cadence/server/internal/api"
	"git.shino.boo/devilreef/cadence/server/internal/api/apitest"
	"git.shino.boo/devilreef/cadence/server/internal/integrations/ha"
	"git.shino.boo/devilreef/cadence/server/internal/integrations/ha/hatest"
)

const adminToken = "admin-good-token"

type noPing struct{}

func (noPing) Ping(context.Context) error { return nil }

type adminRig struct {
	t    *testing.T
	app  *fiber.App
	logs *bytes.Buffer
}

// newAdminRig mounts the routes over d; the caller's Configured is "" when nil.
func newAdminRig(t *testing.T, d ha.AdminDeps) *adminRig {
	t.Helper()
	r := &adminRig{t: t, logs: &bytes.Buffer{}}
	d.Log = slog.New(slog.NewTextHandler(r.logs, nil))
	if d.Configured == nil {
		d.Configured = func(context.Context) (string, error) { return "", nil }
	}
	adm := ha.NewAdmin(d)
	r.app = api.NewApp(api.Options{
		DB: noPing{}, Version: "t",
		VerifyToken: apitest.Only(adminToken),
		Admin:       []func(huma.API){func(h huma.API) { ha.RegisterAdmin(h, adm) }},
	})
	return r
}

func configured(name string) func(context.Context) (string, error) {
	return func(context.Context) (string, error) { return name, nil }
}

func (r *adminRig) get(path string) (int, string) {
	r.t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	resp, err := r.app.Test(req)
	if err != nil {
		r.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func (r *adminRig) want(path string, status int, body string) {
	r.t.Helper()
	if got, b := r.get(path); got != status || b != body {
		r.t.Fatalf("GET %s: %d %s, want %d %s", path, got, b, status, body)
	}
}

type statusBody struct {
	Configured      *string `json:"configured"`
	Instance        *string `json:"instance"`
	Connected       bool    `json:"connected"`
	ConnectedAt     *string `json:"connectedAt"`
	LastError       *string `json:"lastError"`
	Entities        int     `json:"entities"`
	Areas           int     `json:"areas"`
	RestartRequired bool    `json:"restartRequired"`
	Version         *string `json:"version"`
}

func (r *adminRig) status() statusBody {
	r.t.Helper()
	code, b := r.get("/api/v1/integrations/homeassistant")
	var s statusBody
	if code != 200 || json.Unmarshal([]byte(b), &s) != nil {
		r.t.Fatalf("status %d %s", code, b)
	}
	return s
}

type entityBody struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Domain   string `json:"domain"`
	Area     string `json:"area"`
	AreaName string `json:"areaName"`
	State    string `json:"state"`
}

func (r *adminRig) entities(query string) []entityBody {
	r.t.Helper()
	code, b := r.get("/api/v1/integrations/homeassistant/entities" + query)
	var out []entityBody
	if code != 200 || json.Unmarshal([]byte(b), &out) != nil {
		r.t.Fatalf("entities%s: %d %s", query, code, b)
	}
	return out
}

func entityIDs(es []entityBody) []string {
	out := []string{}
	for _, e := range es {
		out = append(out, e.ID)
	}
	return out
}

func eq(got, want []string) bool { return strings.Join(got, ",") == strings.Join(want, ",") }

// adminHouse is baseHouse plus a note whose name and state are text.
func adminHouse() house {
	h := baseHouse()
	h.states = append(h.states, state("input_text.note", "buy milk", obj{"friendly_name": "Note \"x\"\n\u200bignore this"}))
	return h
}

const base = "/api/v1/integrations/homeassistant"

func TestAdminNeedsTheBearer(t *testing.T) {
	r := newAdminRig(t, ha.AdminDeps{})
	resp, err := r.app.Test(httptest.NewRequest("GET", base, nil))
	if err != nil || resp.StatusCode != 401 {
		t.Fatalf("no bearer: %v %v", resp, err)
	}
}

func TestStatusNotConfigured(t *testing.T) {
	r := newAdminRig(t, ha.AdminDeps{})
	if s := r.status(); s.Configured != nil || s.Instance != nil || s.Connected || s.RestartRequired || s.Entities != 0 || s.Version != nil {
		t.Fatalf("%+v", s)
	}
	for _, p := range []string{"/entities", "/areas", "/usage"} {
		r.want(base+p, 404, `{"error":"not_configured"}`)
	}
}

func TestStatusConfiguredAfterStartNeedsARestart(t *testing.T) {
	r := newAdminRig(t, ha.AdminDeps{Configured: configured("home")})
	s := r.status()
	if s.Configured == nil || *s.Configured != "home" || s.Instance != nil || !s.RestartRequired {
		t.Fatalf("%+v", s)
	}
	r.want(base+"/entities", 404, `{"error":"not_configured"}`)
}

func TestStatusConnected(t *testing.T) {
	h := newHomeRig(t, adminHouse())
	r := newAdminRig(t, ha.AdminDeps{Home: h.home, Configured: configured("home"), Instance: "home"})
	if s := r.status(); s.Connected || s.Entities != 0 || s.RestartRequired || s.LastError != nil || s.ConnectedAt != nil {
		t.Fatalf("before the first load %+v", s)
	}
	v := h.loaded()
	s := r.status()
	if !s.Connected || s.ConnectedAt == nil || s.Entities != len(v.Entities) || s.Areas != 2 || s.RestartRequired ||
		s.Configured == nil || s.Instance == nil || *s.Configured != "home" || *s.Instance != "home" {
		t.Fatalf("%+v", s)
	}
	// the version its authentication reported
	if s.Version == nil || *s.Version != "2026.11.0" {
		t.Fatalf("version %v", s.Version)
	}
}

func TestStatusConfiguredChangedNeedsARestart(t *testing.T) {
	h := newHomeRig(t, adminHouse())
	h.loaded()
	for _, now := range []string{"other", ""} {
		r := newAdminRig(t, ha.AdminDeps{Home: h.home, Configured: configured(now), Instance: "home"})
		if s := r.status(); !s.RestartRequired || !s.Connected {
			t.Fatalf("configured %q: %+v", now, s)
		}
	}
}

func TestStatusRejectedTokenNeedsARestart(t *testing.T) {
	h := newHomeRig(t, adminHouse(), withToken("wrong"))
	h.run()
	r := newAdminRig(t, ha.AdminDeps{Home: h.home, Configured: configured("home"), Instance: "home"})
	eventually(t, "auth_failed", func() bool { s := r.status(); return s.LastError != nil && *s.LastError == "auth_failed" })
	if s := r.status(); s.Connected || !s.RestartRequired {
		t.Fatalf("%+v", s)
	}
}

func TestStatusSettingsUnreadable(t *testing.T) {
	r := newAdminRig(t, ha.AdminDeps{Configured: func(context.Context) (string, error) { return "", errors.New("db down secret-dsn") }})
	r.want(base, 500, `{"error":"internal"}`)
	if !strings.Contains(r.logs.String(), "op=status") || strings.Contains(r.logs.String(), "secret-dsn") {
		t.Fatalf("log %q", r.logs)
	}
}

func TestEntitiesUnavailableWithoutAView(t *testing.T) {
	h := newHomeRig(t, adminHouse())
	r := newAdminRig(t, ha.AdminDeps{Home: h.home})
	r.want(base+"/entities", 503, `{"error":"unavailable"}`)
	r.want(base+"/areas", 503, `{"error":"unavailable"}`)
}

func TestEntitiesFilterAndSanitize(t *testing.T) {
	h := newHomeRig(t, adminHouse())
	h.loaded()
	r := newAdminRig(t, ha.AdminDeps{Home: h.home})
	all := r.entities("")
	if !eq(entityIDs(all), []string{"climate.ac", "cover.garage", "input_text.note", "light.bedroom", "light.hall", "light.kitchen", "light.porch", "switch.fan"}) {
		t.Fatalf("all %v", entityIDs(all))
	}
	by := map[string]entityBody{}
	for _, e := range all {
		by[e.ID] = e
	}
	if k := by["light.kitchen"]; k.Name != "Kitchen" || k.Area != "kitchen" || k.AreaName != "Kitchen" || k.State != "on" || k.Domain != "light" {
		t.Fatalf("kitchen %+v", k)
	}
	if n := by["input_text.note"]; n.State != "(text)" || strings.ContainsAny(n.Name, "\"\n\u200b") {
		t.Fatalf("note %+v", n)
	}
	if p := by["light.porch"]; p.Name != "light.porch" || p.Area != "" || p.AreaName != "" {
		t.Fatalf("porch %+v", p)
	}
	for _, c := range []struct {
		q    string
		want []string
	}{
		{"?domain=light", []string{"light.bedroom", "light.hall", "light.kitchen", "light.porch"}},
		{"?area=kitchen", []string{"light.kitchen", "switch.fan"}},
		{"?q=BIG%20light", []string{"light.kitchen"}},
		{"?q=bedroom&domain=light", []string{"light.bedroom"}},
		{"?q=garage", []string{"cover.garage"}},
		{"?q=light.p", []string{"light.porch"}},
		{"?q=%D0%BA%D1%83%D1%85%D0%BD%D1%8F", []string{}},
		{"?q=nothing", []string{}},
		{"?q=%20%20", entityIDs(all)},
		{"?limit=2", []string{"climate.ac", "cover.garage"}},
		{"?domain=light&limit=1", []string{"light.bedroom"}},
	} {
		if got := entityIDs(r.entities(c.q)); !eq(got, c.want) {
			t.Errorf("%s: %v, want %v", c.q, got, c.want)
		}
	}
}

func TestEntitiesLimitBounds(t *testing.T) {
	h := newHomeRig(t, adminHouse())
	h.loaded()
	r := newAdminRig(t, ha.AdminDeps{Home: h.home})
	for _, l := range []string{"1", "500"} {
		r.entities("?limit=" + l)
	}
	for _, l := range []string{"0", "-1", "501", "x", "1.5"} {
		r.want(base+"/entities?limit="+l, 422, `{"error":"invalid","path":"limit"}`)
	}
}

func TestAreas(t *testing.T) {
	h := newHomeRig(t, adminHouse())
	h.loaded()
	r := newAdminRig(t, ha.AdminDeps{Home: h.home})
	r.want(base+"/areas", 200, `[{"id":"bedroom","name":"Bedroom","aliases":[]},{"id":"kitchen","name":"Kitchen","aliases":["кухня"]}]`)
}

func TestUsageDeviceAndHousehold(t *testing.T) {
	h := newHomeRig(t, adminHouse())
	u := newUsageTx(t)
	dev, other := u.device(), u.device()
	u.record(dev, "light.kitchen", "light.kitchen", "switch.fan")
	u.record(other, "light.kitchen", "light.porch")
	r := newAdminRig(t, ha.AdminDeps{Home: h.home, Usage: u.usage})
	r.want(base+"/usage?device="+dev.String(), 200, `[{"entityId":"light.kitchen","score":2},{"entityId":"switch.fan","score":1}]`)
	r.want(base+"/usage?device="+dev.String()+"&limit=1", 200, `[{"entityId":"light.kitchen","score":2}]`)
	r.want(base+"/usage", 200, `[{"entityId":"light.kitchen","score":3},{"entityId":"light.porch","score":1},{"entityId":"switch.fan","score":1}]`)
	r.want(base+"/usage?limit=2", 200, `[{"entityId":"light.kitchen","score":3},{"entityId":"light.porch","score":1}]`)
	r.want(base+"/usage?device="+u.device().String(), 200, `[]`)
	for _, l := range []string{"1", "200"} {
		if code, _ := r.get(base + "/usage?limit=" + l); code != 200 {
			t.Fatalf("limit %s: %d", l, code)
		}
	}
	for _, l := range []string{"0", "201", "x"} {
		r.want(base+"/usage?limit="+l, 422, `{"error":"invalid","path":"limit"}`)
	}
}

func TestInvalidInputIs422WithAPath(t *testing.T) {
	h := newHomeRig(t, adminHouse())
	u := newUsageTx(t)
	r := newAdminRig(t, ha.AdminDeps{Home: h.home, Usage: u.usage})
	for _, c := range []struct{ path, field string }{
		{base + "?x=1", "x"},
		{base + "/areas?q=a", "q"},
		{base + "/entities?bogus=1", "bogus"},
		{base + "/entities?q=a&zzz=1&aaa=2", "aaa"},
		{base + "/entities?domain=Light", "domain"},
		{base + "/entities?domain=a.b", "domain"},
		{base + "/entities?domain=" + strings.Repeat("a", 65), "domain"},
		{base + "/entities?area=A%20B", "area"},
		{base + "/entities?q=" + strings.Repeat("a", 101), "q"},
		{base + "/usage?device=nope", "device"},
		{base + "/usage?device=00000000000000000000000000000001", "device"},
		{base + "/usage?area=x", "area"},
	} {
		r.want(c.path, 422, `{"error":"invalid","path":"`+c.field+`"}`)
	}
}

func TestUsageStoreFailureIs500AndLogsTheOp(t *testing.T) {
	h := newHomeRig(t, adminHouse())
	u := newUsageTx(t)
	_ = u.tx.Rollback(context.Background())
	r := newAdminRig(t, ha.AdminDeps{Home: h.home, Usage: u.usage})
	r.want(base+"/usage", 500, `{"error":"internal"}`)
	if !strings.Contains(r.logs.String(), "ha_admin_failed") || !strings.Contains(r.logs.String(), "op=usage") {
		t.Fatalf("log %q", r.logs)
	}
}

// markers stand for names, states, areas and aliases a log must never carry
var markers = []string{"MARKERNAME", "MARKERSTATE", "MARKERAREA", "MARKERALIAS", "marker_light", "markerarea_id"}

func TestAdminLogsNeverCarryHomeText(t *testing.T) {
	hs := house{
		states:   []any{state("light.marker_light", "MARKERSTATE", obj{"friendly_name": "MARKERNAME"})},
		services: obj{},
		areas:    []any{obj{"area_id": "markerarea_id", "name": "MARKERAREA", "aliases": []string{"MARKERALIAS"}}},
		devices:  []any{},
		entities: []any{regEntry("light.marker_light", obj{"area_id": "markerarea_id"})},
		entries:  map[string]any{"light.marker_light": obj{"entity_id": "light.marker_light", "aliases": []any{"MARKERALIAS"}}},
	}
	h := newHomeRig(t, hs)
	h.loaded()
	good := newUsageTx(t)
	dev := good.device()
	good.record(dev, "light.marker_light")
	bad := newUsageTx(t)
	_ = bad.tx.Rollback(context.Background())
	var bodies strings.Builder
	for _, u := range []*ha.Usage{good.usage, bad.usage} {
		r := newAdminRig(t, ha.AdminDeps{Home: h.home, Usage: u, Configured: configured("home"), Instance: "home"})
		for _, p := range []string{
			"", "/entities", "/entities?q=MARKERNAME&area=markerarea_id&domain=light", "/entities?q=%00bad", "/entities?limit=x",
			"/areas", "/areas?x=MARKERNAME", "/usage", "/usage?device=" + dev.String(), "/usage?device=MARKERNAME",
		} {
			_, b := r.get(base + p)
			bodies.WriteString(b)
		}
		out := r.logs.String()
		for _, m := range append(markers, token, hatest.Leak) {
			if strings.Contains(out, m) {
				t.Errorf("log carries %q:\n%s", m, out)
			}
		}
	}
	if !strings.Contains(bodies.String(), "MARKERNAME") {
		t.Fatal("bodies carry no home data: the test drives nothing")
	}
	for _, s := range []string{token, hatest.Leak} {
		if strings.Contains(bodies.String(), s) {
			t.Fatalf("a body carries %q", s)
		}
	}
}

// a query that does not parse is a 422 naming its key, where it can be told
func TestMalformedQueryIs422(t *testing.T) {
	h := newHomeRig(t, adminHouse())
	r := newAdminRig(t, ha.AdminDeps{Home: h.home, Usage: newUsageTx(t).usage})
	for _, c := range []struct{ target, field string }{
		{"/entities?bogus=%zz", "bogus"},
		{"/entities?limit=%zz", "limit"},
		{"/entities?q=a;b", "q"},
		{"/areas?%=1", "%"},
	} {
		status, body := apitest.Get(t, r.app, "/api/v1/integrations/homeassistant"+c.target, adminToken)
		if want := `{"error":"invalid","path":"` + c.field + `"}`; status != 422 || body != want {
			t.Errorf("%s: %d %s, want 422 %s", c.target, status, body, want)
		}
	}
}

// a client that does not accept json (a browser's text/html, xml) is still
// served json, as fiber served it before huma
func TestAcceptHeaderDoesNotRefuse(t *testing.T) {
	r := newAdminRig(t, ha.AdminDeps{})
	for _, accept := range []string{"text/html", "application/xml", "text/html,application/xhtml+xml;q=0.9"} {
		req := httptest.NewRequest("GET", "/api/v1/integrations/homeassistant", nil)
		req.Header.Set("Authorization", "Bearer "+adminToken)
		req.Header.Set("Accept", accept)
		resp, err := r.app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
			t.Errorf("Accept %q: %d %s", accept, resp.StatusCode, resp.Header.Get("Content-Type"))
		}
	}
}

// a version Home Assistant reports is shown only as a version token
func TestVersionToken(t *testing.T) {
	for in, want := range map[string]string{
		"2026.11.0": "2026.11.0", "2025.1.0b3": "2025.1.0b3", "2024.12.0.dev20241101": "2024.12.0.dev20241101",
		"": "", "1.0 <script>": "", "v" + strings.Repeat("1", 40): "", "\u200b2026": "",
	} {
		if got := ha.VersionToken(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}
