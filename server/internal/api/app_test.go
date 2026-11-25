package api_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/gofiber/fiber/v3"

	"git.shino.boo/devilreef/cadence/server/internal/api"
	"git.shino.boo/devilreef/cadence/server/internal/api/apitest"
	"git.shino.boo/devilreef/cadence/server/internal/auth"
)

type fakePinger struct{ err error }

func (f fakePinger) Ping(context.Context) error { return f.err }

func do(t *testing.T, o api.Options, req *http.Request) (int, string) {
	t.Helper()
	resp, err := api.NewApp(o).Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func get(t *testing.T, p api.Pinger) (int, string) {
	t.Helper()
	return do(t, api.Options{DB: p, Version: "t"}, httptest.NewRequest("GET", "/healthz", nil))
}

func TestHealthOK(t *testing.T) {
	code, body := get(t, fakePinger{})
	if code != 200 || body != `{"status":"ok","db":"ok","version":"t"}` {
		t.Fatalf("%d %s", code, body)
	}
}

func TestHealthDBDown(t *testing.T) {
	code, body := get(t, fakePinger{errors.New("dial tcp 10.0.0.5: password authentication failed for user cadence")})
	if code != 503 || body != `{"status":"degraded","db":"down","version":"t"}` {
		t.Fatalf("%d %s", code, body)
	}
}

type pingOut struct {
	Body struct {
		OK bool `json:"ok"`
	}
}

func adminOptions() api.Options {
	return api.Options{
		DB: fakePinger{}, Version: "t",
		VerifyToken: apitest.Only("good"),
		Admin: []func(huma.API){func(a huma.API) {
			huma.Get(a, "/ping", func(context.Context, *struct{}) (*pingOut, error) {
				out := &pingOut{}
				out.Body.OK = true
				return out, nil
			})
		}},
		Device: []func(fiber.Router){func(r fiber.Router) {
			r.Get("/hello", func(c fiber.Ctx) error { return c.SendString("hi") })
		}},
	}
}

func TestAdminRequiresBearer(t *testing.T) {
	for _, h := range []string{"", "Bearer bad", "Basic good", "Bearer "} {
		req := httptest.NewRequest("GET", "/api/v1/ping", nil)
		if h != "" {
			req.Header.Set("Authorization", h)
		}
		code, body := do(t, adminOptions(), req)
		if code != 401 || body != `{"error":"unauthorized"}` {
			t.Fatalf("%q: %d %s", h, code, body)
		}
	}
}

func TestAdminAcceptsBearer(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/v1/ping", nil)
	req.Header.Set("Authorization", "Bearer good")
	code, body := do(t, adminOptions(), req)
	if code != 200 || strings.TrimSpace(body) != `{"ok":true}` {
		t.Fatalf("%d %s", code, body)
	}
}

type cachedOut struct {
	CacheControl string `header:"Cache-Control"`
	Body         struct {
		OK bool `json:"ok"`
	}
}

// every answer under the admin prefix, refusals included, is kept out of
// caches unless its route says otherwise; the health check and the device
// api are left as they were
func TestAdminAnswersAreNotCached(t *testing.T) {
	o := adminOptions()
	o.Admin = append(o.Admin, func(a huma.API) {
		huma.Get(a, "/cached", func(context.Context, *struct{}) (*cachedOut, error) {
			return &cachedOut{CacheControl: "private, max-age=60"}, nil
		})
	})
	o.Plain = []api.PlainRoute{{Op: "plain", Method: http.MethodGet, Path: "/plain", Handler: func(c fiber.Ctx) error {
		return c.JSON(fiber.Map{"ok": true})
	}}}
	app := api.NewApp(o)
	for _, c := range []struct {
		path, bearer string
		code         int
		cache        string
	}{
		{"/api/v1/ping", "good", 200, "no-store"},
		{"/api/v1/ping", "", 401, "no-store"},
		{"/api/v1/nope", "good", 404, "no-store"},
		{"/api/v1/plain", "good", 200, "no-store"},
		{"/api/v1/openapi.json", "good", 200, "no-store"},
		{"/api/v1/cached", "good", 200, "private, max-age=60"},
		{"/healthz", "", 200, ""},
		{"/v2/hello", "", 200, ""},
	} {
		req := httptest.NewRequest(http.MethodGet, c.path, nil)
		if c.bearer != "" {
			req.Header.Set("Authorization", "Bearer "+c.bearer)
		}
		resp, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != c.code || resp.Header.Get("Cache-Control") != c.cache {
			t.Errorf("%s %q: %d Cache-Control %q", c.path, c.bearer, resp.StatusCode, resp.Header.Get("Cache-Control"))
		}
	}
}

func TestOpenAPIServed(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/v1/openapi.json", nil)
	req.Header.Set("Authorization", "Bearer good")
	code, body := do(t, adminOptions(), req)
	if code != 200 || len(body) == 0 || body[0] != '{' {
		t.Fatalf("%d %.80s", code, body)
	}
}

func TestDeviceGroupNeedsNoAdminAuth(t *testing.T) {
	code, body := do(t, adminOptions(), httptest.NewRequest("GET", "/v2/hello", nil))
	if code != 200 || body != "hi" {
		t.Fatalf("%d %s", code, body)
	}
}

func TestAdminOutageIs503AndLogsNoCause(t *testing.T) {
	var logs bytes.Buffer
	o := adminOptions()
	o.Log = slog.New(slog.NewJSONHandler(&logs, nil))
	o.VerifyToken = func(context.Context, string) (auth.Grants, error) {
		return auth.Grants{}, errors.New("password authentication failed for user x")
	}
	req := httptest.NewRequest("GET", "/api/v1/ping", nil)
	req.Header.Set("Authorization", "Bearer good")
	code, body := do(t, o, req)
	if code != 503 || body != `{"error":"unavailable"}` {
		t.Fatalf("%d %s", code, body)
	}
	if !strings.Contains(logs.String(), `"msg":"auth_unavailable"`) || strings.Contains(logs.String(), "password") {
		t.Fatalf("log: %s", logs.String())
	}
}

// a panic's value can carry anything, so neither the response nor the log has it
func TestPanicIs500AndLogsNoValue(t *testing.T) {
	var logs bytes.Buffer
	o := adminOptions()
	o.Log = slog.New(slog.NewJSONHandler(&logs, nil))
	o.Device = append(o.Device, func(r fiber.Router) {
		r.Get("/boom/:id", func(fiber.Ctx) error { panic("secret-xyz") })
	})
	code, body := do(t, o, httptest.NewRequest("GET", "/v2/boom/42", nil))
	if code != 500 || body != `{"error":"internal"}` {
		t.Fatalf("%d %s", code, body)
	}
	out := logs.String()
	if !strings.Contains(out, `"msg":"http_panic","method":"GET","route":"/v2/boom/:id"`) || strings.Contains(out, "secret-xyz") {
		t.Fatalf("log: %s", out)
	}
}

// fiber recycles a request's buffers for the next one, so a value a handler
// keeps must stay what its own request said (Ruling M5d-R10)
func TestKeptRequestValuesOutliveTheirRequest(t *testing.T) {
	var kept []string
	o := adminOptions()
	o.Device = append(o.Device, func(r fiber.Router) {
		r.Get("/keep/:x", func(c fiber.Ctx) error {
			kept = append(kept, c.Params("x"), c.Query("q"), c.Get("X-Kept"), string(c.Body()))
			return c.SendStatus(fiber.StatusNoContent)
		})
	})
	app := api.NewApp(o)
	for _, v := range []string{"aaaaaaaa", "bbbbbbbb"} {
		req := httptest.NewRequest("GET", "/v2/keep/"+v+"?q="+v, strings.NewReader(v))
		req.Header.Set("X-Kept", v)
		resp, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
	}
	want := []string{"aaaaaaaa", "aaaaaaaa", "aaaaaaaa", "aaaaaaaa", "bbbbbbbb", "bbbbbbbb", "bbbbbbbb", "bbbbbbbb"}
	if !slices.Equal(kept, want) {
		t.Fatalf("kept %q, want %q", kept, want)
	}
}
