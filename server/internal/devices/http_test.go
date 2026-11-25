package devices_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/gofiber/fiber/v3"

	"git.shino.boo/devilreef/cadence/server/internal/api"
	"git.shino.boo/devilreef/cadence/server/internal/api/apitest"
	"git.shino.boo/devilreef/cadence/server/internal/devices"
)

type noPing struct{}

func (noPing) Ping(context.Context) error { return nil }

func (e *env) app() *fiber.App {
	return api.NewApp(api.Options{
		DB: noPing{}, Version: "t",
		VerifyToken: apitest.Verify,
		Admin:       []func(huma.API){func(a huma.API) { devices.RegisterAdmin(a, e.svc) }},
	})
}

func call(t *testing.T, app *fiber.App, method, path, body string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer x")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestDeviceDetail(t *testing.T) {
	e := newEnv(t)
	e.sendReport(t, report(desc("audio")))
	app := e.app()
	code, body := call(t, app, http.MethodPut, "/api/v1/devices/"+e.id+"/capabilities/audio/config", `{"level":40}`)
	if code != 200 || body != `{"generation":1}` {
		t.Fatalf("%d %s", code, body)
	}
	e.svc.Handle(context.Background(), e.id, applied("audio", 1, ""))
	e.svc.Handle(context.Background(), e.id, delta("audio", `{"playing":true}`))
	code, body = call(t, app, http.MethodGet, "/api/v1/devices/"+e.id, "")
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	var got struct {
		ID, Name, Model, Status string
		Online                  bool
		LastSeenAt              *string
		Capabilities            []struct {
			ID           string
			Version      int
			Desired      map[string]int
			DesiredGen   int
			AppliedGen   int
			AppliedError string
			State        map[string]bool
		}
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err, body)
	}
	c := got.Capabilities
	if got.ID != e.id || got.Name != "kitchen" || got.Model != "sim" || got.Status != "pending" || !got.Online ||
		got.LastSeenAt != nil || len(c) != 1 || c[0].ID != "audio" || c[0].Version != 1 || c[0].Desired["level"] != 40 ||
		c[0].DesiredGen != 1 || c[0].AppliedGen != 1 || !c[0].State["playing"] {
		t.Fatalf("%s", body)
	}
}

// regression: a path id in another spelling was stored but never pushed, as the
// hub keys links by the canonical id
func TestPathIDCanonicalized(t *testing.T) {
	e := newEnv(t)
	e.sendReport(t, report(desc("audio")))
	app := e.app()
	upper := strings.ToUpper(e.id)
	for i, id := range []string{upper, strings.ReplaceAll(upper, "-", "")} {
		code, body := call(t, app, http.MethodPut, "/api/v1/devices/"+id+"/capabilities/audio/config", `{"level":40}`)
		if want := fmt.Sprintf(`{"generation":%d}`, i+1); code != 200 || body != want {
			t.Fatalf("%s: %d %s", id, code, body)
		}
		if sets := e.hub.configSets(); len(sets) != i+1 || sets[i].Generation != int64(i+1) {
			t.Fatalf("%s: pushed %v", id, sets)
		}
	}
	code, body := call(t, app, http.MethodGet, "/api/v1/devices/"+upper, "")
	var got struct {
		ID     string
		Online bool
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil || code != 200 || got.ID != e.id || !got.Online {
		t.Fatalf("%d %s", code, body)
	}
}

func TestDeviceDetailNotFound(t *testing.T) {
	e := newEnv(t)
	for _, id := range []string{"00000000-0000-0000-0000-000000000000", "nope"} {
		code, body := call(t, e.app(), http.MethodGet, "/api/v1/devices/"+id, "")
		if code != 404 || body != `{"error":"not_found"}` {
			t.Fatalf("%d %s", code, body)
		}
	}
}

func TestPutConfig422Body(t *testing.T) {
	e := newEnv(t)
	e.sendReport(t, report(desc("audio")))
	for _, c := range []struct{ body, want string }{
		{`{"level":101}`, `{"error":"config_invalid","path":"/level"}`},
		// the route's own check answers a body huma would refuse or require
		{``, `{"error":"config_invalid","path":""}`},
		{`{"level":`, `{"error":"config_invalid","path":""}`},
		{`[40]`, `{"error":"config_invalid","path":""}`},
	} {
		code, body := call(t, e.app(), http.MethodPut, "/api/v1/devices/"+e.id+"/capabilities/audio/config", c.body)
		if code != 422 || body != c.want {
			t.Fatalf("%q: %d %s", c.body, code, body)
		}
	}
}

func TestPutConfigUnknownCapability404(t *testing.T) {
	e := newEnv(t)
	code, body := call(t, e.app(), http.MethodPut, "/api/v1/devices/"+e.id+"/capabilities/nope/config", `{}`)
	if code != 404 || body != `{"error":"not_found"}` {
		t.Fatalf("%d %s", code, body)
	}
}

func TestPutConfigTooLarge413(t *testing.T) {
	e := newEnv(t)
	big := desc("audio")
	big.ConfigSchema = ""
	e.sendReport(t, report(big))
	body := `{"blob":"` + strings.Repeat("x", 17*1024) + `"}`
	code, got := call(t, e.app(), http.MethodPut, "/api/v1/devices/"+e.id+"/capabilities/audio/config", body)
	if code != 413 || got != `{"error":"config_too_large"}` {
		t.Fatalf("%d %.80s", code, got)
	}
	if e.capability(t, "audio").DesiredGen != 0 || len(e.hub.configSets()) != 0 {
		t.Fatal("oversized config stored or sent")
	}
}

func TestAdminRoutesRequireBearer(t *testing.T) {
	e := newEnv(t)
	app := api.NewApp(api.Options{
		DB: noPing{}, Version: "t",
		VerifyToken: apitest.Refuse,
		Admin:       []func(huma.API){func(a huma.API) { devices.RegisterAdmin(a, e.svc) }},
	})
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/devices/" + e.id},
		{http.MethodPatch, "/api/v1/devices/" + e.id},
		{http.MethodDelete, "/api/v1/devices/" + e.id},
		{http.MethodPut, "/api/v1/devices/" + e.id + "/capabilities/audio/config"},
		{http.MethodPut, "/api/v1/devices/" + e.id + secretPath},
		{http.MethodDelete, "/api/v1/devices/" + e.id + secretPath},
	} {
		req := httptest.NewRequest(c.method, c.path, strings.NewReader(`{}`))
		resp, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != 401 || string(b) != `{"error":"unauthorized"}` {
			t.Fatalf("%s %d %s", c.method, resp.StatusCode, b)
		}
	}
}

// an ha token reads a device but changes none of its config or secrets, and
// neither renames nor deletes it (M7a-D6)
func TestHATokenReadsTheDevice(t *testing.T) {
	e := newEnv(t)
	e.sendReport(t, report(desc("audio")))
	app := e.app()
	if code, body := apitest.Call(t, app, apitest.HABearer, http.MethodGet, "/api/v1/devices/"+e.id, ""); code != 200 || !strings.Contains(body, `"id":"audio"`) {
		t.Fatalf("detail %d %s", code, body)
	}
	for _, q := range [][3]string{
		{http.MethodPut, "/capabilities/audio/config", `{"level":40}`},
		{http.MethodPut, "/capabilities/audio/secrets/x", `{"value":"v"}`},
		{http.MethodDelete, "/capabilities/audio/secrets/x", ""},
		{http.MethodPatch, "", `{"name":"hall"}`},
		{http.MethodDelete, "", ""},
	} {
		if code, body := apitest.Call(t, app, apitest.HABearer, q[0], "/api/v1/devices/"+e.id+q[1], q[2]); code != 403 || body != `{"error":"forbidden"}` {
			t.Fatalf("%s %s: %d %s", q[0], q[1], code, body)
		}
	}
	if _, body := call(t, app, http.MethodGet, "/api/v1/devices/"+e.id, ""); !strings.Contains(body, `"desiredGen":0`) ||
		!strings.Contains(body, `"name":"kitchen"`) {
		t.Fatalf("device changed: %s", body)
	}
	if len(e.hub.removedIDs()) != 0 {
		t.Fatal("an ha token reached the hub")
	}
}
