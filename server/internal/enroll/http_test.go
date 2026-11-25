package enroll_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/gofiber/fiber/v3"

	"git.shino.boo/devilreef/cadence/server/internal/api"
	"git.shino.boo/devilreef/cadence/server/internal/api/apitest"
	"git.shino.boo/devilreef/cadence/server/internal/enroll"
)

type noPing struct{}

func (noPing) Ping(context.Context) error { return nil }

func newApp(t *testing.T) *fiber.App {
	t.Helper()
	return newAppOnline(t, func(string) bool { return false })
}

// newAppOnline lists the devices online says are linked
func newAppOnline(t *testing.T, online func(deviceID string) bool) *fiber.App {
	t.Helper()
	s, _, _ := newSvc(t)
	return api.NewApp(api.Options{
		DB: noPing{}, Version: "t",
		VerifyToken: apitest.Verify,
		Admin:       []func(huma.API){func(a huma.API) { enroll.RegisterAdmin(a, s, online) }},
		Device:      []func(fiber.Router){func(r fiber.Router) { enroll.MountDevice(r, s) }},
	})
}

func send(t *testing.T, app *fiber.App, method, path, body string) (int, string) {
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

func TestCreateDeviceFlow(t *testing.T) {
	app := newApp(t)
	code, body := send(t, app, http.MethodPost, "/api/v1/devices", `{"name":"kitchen","model":"sim"}`)
	if code != 201 {
		t.Fatalf("%d %s", code, body)
	}
	var created struct {
		ID, Status, Code, CodeExpiresAt string
	}
	if err := json.Unmarshal([]byte(body), &created); err != nil || created.Code == "" || created.Status != "pending" || created.CodeExpiresAt == "" {
		t.Fatalf("%v %s", err, body)
	}
	code, body = send(t, app, http.MethodPost, "/v2/enroll", `{"code":"`+created.Code+`","fingerprint":{"board":"sim"}}`)
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	var out struct{ DeviceID, Token string }
	if err := json.Unmarshal([]byte(body), &out); err != nil || out.DeviceID != created.ID || !strings.HasPrefix(out.Token, "cdd_") {
		t.Fatalf("%v %s", err, body)
	}
	_, list := send(t, app, http.MethodGet, "/api/v1/devices", "")
	if !strings.Contains(list, `"status":"enrolled"`) {
		t.Fatalf("list: %s", list)
	}
}

func TestEnrollRefusedBody(t *testing.T) {
	app := newApp(t)
	for _, body := range []string{`{"code":"ABCD-EFGH","fingerprint":{"board":"sim"}}`, `not json`} {
		code, got := send(t, app, http.MethodPost, "/v2/enroll", body)
		if code != 403 || got != `{"error":"enroll_refused"}` {
			t.Fatalf("%d %s", code, got)
		}
	}
}

func TestCreateDeviceRefusals(t *testing.T) {
	app := newApp(t)
	for _, c := range []struct {
		body string
		code int
		want string
	}{
		{`{"name":"x","model":"toaster"}`, 422, `{"error":"unknown_model"}`},
		// huma's own checks: a field's pointer, never its value
		{`{"name":"x","model":"sim","owner":"secret-owner"}`, 422, `{"error":"invalid","path":"/body/owner"}`},
		{`{"name":"","model":"sim"}`, 422, `{"error":"invalid","path":"/body/name"}`},
		{`{"model":"sim"}`, 422, `{"error":"invalid","path":"/body"}`},
		{`{"name":"secret-name"`, 400, `{"error":"invalid","path":"/body"}`},
		// a rename's rules: trimmed, 1-64 characters, no control, format or separator character
		{`{"name":"   ","model":"sim"}`, 422, `{"error":"invalid","path":"/body/name"}`},
		{`{"name":"` + strings.Repeat("ё", 65) + `","model":"sim"}`, 422, `{"error":"invalid","path":"/body/name"}`},
		{`{"name":" ` + strings.Repeat("ё", 64) + `ё ","model":"sim"}`, 422, `{"error":"invalid","path":"/body/name"}`},
		{`{"name":"a\u200bb","model":"sim"}`, 422, `{"error":"invalid","path":"/body/name"}`},
		{`{"name":"a\nb","model":"sim"}`, 422, `{"error":"invalid","path":"/body/name"}`},
	} {
		if code, body := send(t, app, http.MethodPost, "/api/v1/devices", c.body); code != c.code || body != c.want {
			t.Fatalf("%s: %d %s", c.body, code, body)
		}
	}
	if _, list := send(t, app, http.MethodGet, "/api/v1/devices", ""); list != "[]" {
		t.Fatalf("created on a refusal: %s", list)
	}
}

// a created name is trimmed and no other device's, in any case
func TestCreateDeviceNames(t *testing.T) {
	app := newApp(t)
	code, body := send(t, app, http.MethodPost, "/api/v1/devices", `{"name":"  Кухня ","model":"sim"}`)
	if code != 201 || !strings.Contains(body, `"name":"Кухня"`) {
		t.Fatalf("%d %s", code, body)
	}
	// 64 once trimmed, longer as sent
	padded := strings.Repeat("ж", 64)
	if code, body := send(t, app, http.MethodPost, "/api/v1/devices", `{"name":"  `+padded+` ","model":"sim"}`); code != 201 || !strings.Contains(body, `"name":"`+padded+`"`) {
		t.Fatalf("padded: %d %s", code, body)
	}
	for _, name := range []string{"кухня", " КУХНЯ"} {
		if code, body := send(t, app, http.MethodPost, "/api/v1/devices", `{"name":"`+name+`","model":"sim"}`); code != 409 || body != `{"error":"name_taken"}` {
			t.Fatalf("%q: %d %s", name, code, body)
		}
	}
	if _, list := send(t, app, http.MethodGet, "/api/v1/devices", ""); strings.Count(list, `"id"`) != 2 {
		t.Fatalf("list %s", list)
	}
}

// the list says which devices are linked now, beside when each was last seen
func TestListSaysWhoIsOnline(t *testing.T) {
	var linked string
	app := newAppOnline(t, func(id string) bool { return id == linked })
	var ids []string
	for _, name := range []string{"kitchen", "hall"} {
		code, body := send(t, app, http.MethodPost, "/api/v1/devices", `{"name":"`+name+`","model":"sim"}`)
		var d struct{ ID string }
		if err := json.Unmarshal([]byte(body), &d); err != nil || code != 201 {
			t.Fatalf("%d %s", code, body)
		}
		ids = append(ids, d.ID)
	}
	linked = ids[1]
	_, body := send(t, app, http.MethodGet, "/api/v1/devices", "")
	var list []struct {
		ID         string
		Online     bool
		LastSeenAt *string
	}
	if err := json.Unmarshal([]byte(body), &list); err != nil || len(list) != 2 {
		t.Fatalf("%v %s", err, body)
	}
	if list[0].ID != ids[0] || list[0].Online || list[1].ID != ids[1] || !list[1].Online || list[0].LastSeenAt != nil ||
		!strings.Contains(body, `"online":false`) || !strings.Contains(body, `"lastSeenAt":null`) {
		t.Fatalf("%s", body)
	}
}

func TestBoards(t *testing.T) {
	_, body := send(t, newApp(t), http.MethodGet, "/api/v1/boards", "")
	if !strings.Contains(body, `"id":"sim"`) || !strings.Contains(body, `"id":"yandex-pickle"`) || !strings.Contains(body, `"id":"yandex-midi"`) || !strings.Contains(body, `"id":"yandex-micro"`) || !strings.Contains(body, `"id":"vk-capsula-mini"`) ||
		!strings.Contains(body, `"id":"sim-tv"`) || !strings.Contains(body, `"id":"rpi"`) || !strings.Contains(body, `"id":"android-tv"`) {
		t.Fatalf("%s", body)
	}
}

// an ha token lists the devices but enrols none (M7a-D6)
func TestHATokenListsDevices(t *testing.T) {
	app := newApp(t)
	if code, body := send(t, app, http.MethodPost, "/api/v1/devices", `{"name":"kitchen","model":"sim"}`); code != 201 {
		t.Fatalf("%d %s", code, body)
	}
	if code, body := apitest.Call(t, app, apitest.HABearer, http.MethodGet, "/api/v1/devices", ""); code != 200 || !strings.Contains(body, `"name":"kitchen"`) {
		t.Fatalf("list %d %s", code, body)
	}
	code, body := apitest.Call(t, app, apitest.HABearer, http.MethodPost, "/api/v1/devices", `{"name":"hall","model":"sim"}`)
	if code != 403 || body != `{"error":"forbidden"}` {
		t.Fatalf("create %d %s", code, body)
	}
	if _, body := send(t, app, http.MethodGet, "/api/v1/devices", ""); strings.Contains(body, "hall") {
		t.Fatalf("created %s", body)
	}
}
