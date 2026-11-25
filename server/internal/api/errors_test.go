package api_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/gofiber/fiber/v3"

	"git.shino.boo/devilreef/cadence/server/internal/api"
)

type reply struct {
	code        int
	body, ctype string
}

// serve registers ops beside the ping route and sends one admin request.
func serve(t *testing.T, log *slog.Logger, op func(huma.API), method, path, ctype, body string) reply {
	t.Helper()
	o := adminOptions()
	o.Log = log
	o.Admin = append(o.Admin, op)
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer good")
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	resp, err := api.NewApp(o).Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return reply{resp.StatusCode, string(b), resp.Header.Get("Content-Type")}
}

type item struct {
	N int `json:"n" minimum:"1"`
}

type echoIn struct {
	Body struct {
		N     int    `json:"n" minimum:"1"`
		Items []item `json:"items,omitempty"`
	}
}

func echo(a huma.API) {
	huma.Post(a, "/echo", func(context.Context, *echoIn) (*pingOut, error) { return &pingOut{}, nil })
}

// huma's own refusals speak the one model: a code and the field's json pointer, never a value
func TestHumaRefusalsAreInvalidAtAPath(t *testing.T) {
	for _, c := range []struct {
		body string
		code int
		want string
	}{
		{`{"n":0}`, 422, `{"error":"invalid","path":"/body/n"}`},
		{`{"n":1,"secret-x":"secret-value"}`, 422, `{"error":"invalid","path":"/body/secret-x"}`},
		{`{"n":1,"items":[{"n":1},{"n":-5}]}`, 422, `{"error":"invalid","path":"/body/items/1/n"}`},
		{`{"n":"secret-value"`, 400, `{"error":"invalid","path":"/body"}`},
	} {
		r := serve(t, nil, echo, http.MethodPost, "/api/v1/echo", "application/json", c.body)
		if r.code != c.code || r.body != c.want || r.ctype != "application/json" {
			t.Fatalf("%s: %+v", c.body, r)
		}
	}
}

func TestPathSegmentsEscaped(t *testing.T) {
	r := serve(t, nil, echo, http.MethodPost, "/api/v1/echo", "application/json", `{"n":1,"a/b~c":1}`)
	if r.code != 422 || r.body != `{"error":"invalid","path":"/body/a~1b~0c"}` {
		t.Fatalf("%+v", r)
	}
}

// a handler's cause can carry driver text: a 5xx is a bare code in the body and nowhere in the log
func TestHandlerFailureIsInternalWithoutItsCause(t *testing.T) {
	var logs bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logs, nil))
	boom := func(a huma.API) {
		huma.Get(a, "/boom", func(context.Context, *struct{}) (*pingOut, error) {
			return nil, errors.New("password authentication failed: secret-xyz")
		})
		huma.Get(a, "/boom-detail", func(context.Context, *struct{}) (*pingOut, error) {
			return nil, huma.Error503ServiceUnavailable("secret-xyz", errors.New("secret-xyz"))
		})
	}
	for _, path := range []string{"/api/v1/boom", "/api/v1/boom-detail"} {
		r := serve(t, log, boom, http.MethodGet, path, "", "")
		if r.code/100 != 5 || r.body != `{"error":"internal"}` || r.ctype != "application/json" {
			t.Fatalf("%s: %+v", path, r)
		}
	}
	if strings.Contains(logs.String(), "secret") {
		t.Fatalf("log: %s", logs.String())
	}
}

// a route's own refusal keeps its code, its status and, when it names a field, the pointer
func TestErrorConstructor(t *testing.T) {
	for _, c := range []struct {
		err  error
		code int
		want string
	}{
		{api.Error(http.StatusNotFound, "not_found"), 404, `{"error":"not_found"}`},
		{api.Error(http.StatusUnprocessableEntity, "config_invalid", "/level"), 422, `{"error":"config_invalid","path":"/level"}`},
		{api.Error(http.StatusUnprocessableEntity, "invalid_arguments", ""), 422, `{"error":"invalid_arguments","path":""}`},
		{api.Error(http.StatusGatewayTimeout, "timeout"), 504, `{"error":"timeout"}`},
		{api.Error(499, "canceled"), 499, `{"error":"canceled"}`},
		// a detail is a token or nothing (ruling M7a-R1)
		{api.ErrorWithDetail(http.StatusConflict, "command_failed", "no_session"), 409, `{"error":"command_failed","detail":"no_session"}`},
		{api.ErrorWithDetail(http.StatusConflict, "command_failed", "Not A Token! secret"), 409, `{"error":"command_failed"}`},
		{api.ErrorWithDetail(http.StatusConflict, "command_failed", ""), 409, `{"error":"command_failed"}`},
		{api.ErrorWithDetail(http.StatusConflict, "command_failed", "a-b"), 409, `{"error":"command_failed"}`},
		{api.ErrorWithDetail(http.StatusConflict, "command_failed", "a"+strings.Repeat("b", 63)), 409,
			`{"error":"command_failed","detail":"a` + strings.Repeat("b", 63) + `"}`},
		{api.ErrorWithDetail(http.StatusConflict, "command_failed", "a"+strings.Repeat("b", 64)), 409, `{"error":"command_failed"}`},
	} {
		fail := func(a huma.API) {
			huma.Get(a, "/fail", func(context.Context, *struct{}) (*pingOut, error) { return nil, c.err })
		}
		r := serve(t, nil, fail, http.MethodGet, "/api/v1/fail", "", "")
		if r.code != c.code || r.body != c.want || r.ctype != "application/json" {
			t.Fatalf("%s: %+v", c.want, r)
		}
	}
}

// a raw body reaches its handler as sent, whatever it holds, and huma requires none
func TestRawJSONBodyAsSent(t *testing.T) {
	var got []string
	raw := func(a huma.API) {
		huma.Put(a, "/raw", func(_ context.Context, in *struct{ api.RawJSON }) (*struct{}, error) {
			got = append(got, string(in.Bytes()))
			return nil, nil
		})
	}
	for _, c := range []struct{ ctype, body string }{
		{"application/json", ``},
		{"application/json", `{"a":1, "b" : [ 2 ]}`},
		{"application/json", `{not json`},
		{"text/plain", `["x"]`},
		{"", `{"a":1}`},
	} {
		if r := serve(t, nil, raw, http.MethodPut, "/api/v1/raw", c.ctype, c.body); r.code != 204 {
			t.Fatalf("%q: %+v", c.body, r)
		}
	}
	want := []string{``, `{"a":1, "b" : [ 2 ]}`, `{not json`, `["x"]`, `{"a":1}`}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q", got)
	}
}

// success bodies keep the bytes fiber's c.JSON wrote: html escaped, no trailing newline
func TestSuccessBodiesEncodedAsBefore(t *testing.T) {
	type out struct {
		Body struct {
			Title string `json:"title"`
		}
	}
	get := func(a huma.API) {
		huma.Get(a, "/title", func(context.Context, *struct{}) (*out, error) {
			o := &out{}
			o.Body.Title = "Rock & <Roll>"
			return o, nil
		})
	}
	r := serve(t, nil, get, http.MethodGet, "/api/v1/title", "", "")
	// \x5c is a backslash: the body holds json's \u escapes
	want := "{\"title\":\"Rock \x5cu0026 \x5cu003cRoll\x5cu003e\"}"
	if r.code != 200 || r.body != want || r.ctype != "application/json" {
		t.Fatalf("%+v", r)
	}
}

// a list answers an array, never null, so the document types it as one
func TestListRepliesAreArrays(t *testing.T) {
	type out struct{ Body []item }
	list := func(a huma.API) {
		huma.Get(a, "/list", func(context.Context, *struct{}) (*out, error) { return &out{}, nil })
	}
	if r := serve(t, nil, list, http.MethodGet, "/api/v1/list", "", ""); r.code != 200 || r.body != "[]" {
		t.Fatalf("%+v", r)
	}
	doc, err := api.Spec("t", nil, []func(huma.API){list})
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Paths map[string]map[string]struct {
			Responses map[string]struct {
				Content map[string]struct {
					Schema struct {
						Type any `json:"type"`
					} `json:"schema"`
				} `json:"content"`
			} `json:"responses"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(doc, &spec); err != nil {
		t.Fatal(err)
	}
	if typ := spec.Paths["/list"]["get"].Responses["200"].Content["application/json"].Schema.Type; typ != "array" {
		t.Fatalf("type %v", typ)
	}
}

// fiber's own refusals under the admin prefix speak the model too, never
// fiber's plain text or a handler's cause, as application/json like huma's;
// the device api and the health check keep fiber's
func TestFibersOwnErrorsSpeakTheModel(t *testing.T) {
	o := adminOptions()
	o.Admin = append(o.Admin, echo)
	o.Plain = []api.PlainRoute{
		{Op: "upgrade", Method: http.MethodGet, Path: "/upgrade", Handler: func(fiber.Ctx) error { return fiber.ErrUpgradeRequired }},
		{Op: "boom", Method: http.MethodGet, Path: "/boom", Handler: func(fiber.Ctx) error { return errors.New("driver says secret") }},
	}
	o.Device = append(o.Device, func(r fiber.Router) {
		r.Get("/upgrade", func(fiber.Ctx) error { return fiber.ErrUpgradeRequired })
	})
	app := api.NewApp(o)
	for _, c := range []struct {
		method, path string
		anon         bool
		code         int
		want         string
	}{
		{"GET", "/api/v1/ping", true, 401, `{"error":"unauthorized"}`},
		{"GET", "/api/v1/nope-secret", false, 404, `{"error":"not_found"}`},
		{"GET", "/API/V1/nope", false, 404, `{"error":"not_found"}`},
		{"GET", "/api/v1/echo", false, 405, `{"error":"method_not_allowed"}`},
		{"GET", "/api/v1/upgrade", false, 426, `{"error":"upgrade_required"}`},
		{"GET", "/api/v1/boom", false, 500, `{"error":"internal"}`},
		{"GET", "/v2/nope", false, 404, "Not Found"},
		{"GET", "/v2/upgrade", false, 426, "Upgrade Required"},
		{"GET", "/api/v1x/nope", false, 404, "Not Found"},
	} {
		req := httptest.NewRequest(c.method, c.path, nil)
		if !c.anon {
			req.Header.Set("Authorization", "Bearer good")
		}
		resp, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != c.code || string(b) != c.want {
			t.Errorf("%s %s: %d %s", c.method, c.path, resp.StatusCode, b)
		}
		if ct := resp.Header.Get("Content-Type"); strings.HasPrefix(c.want, "{") && ct != "application/json" {
			t.Errorf("%s %s: content type %q", c.method, c.path, ct)
		}
	}
	// a body past the app's 640 MiB limit (the artifact upload's) is refused
	// before any route, from its length alone
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = app.Listener(ln, fiber.ListenConfig{DisableStartupMessage: true}) }()
	t.Cleanup(func() { _ = app.Shutdown() })
	for path, want := range map[string]string{"/api/v1/echo": `{"error":"too_large"}`, "/v2/hello": "Request Entity Too Large"} {
		conn, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.WriteString(conn, "POST "+path+" HTTP/1.1\r\nHost: x\r\nAuthorization: Bearer good\r\n"+
			"Content-Type: application/json\r\nContent-Length: "+strconv.Itoa(640<<20+1)+"\r\n\r\n")
		resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		_ = conn.Close()
		if resp.StatusCode != 413 || string(b) != want {
			t.Errorf("%s: %d %s", path, resp.StatusCode, b)
		}
	}
}
