// Package api holds the HTTP surface.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humafiber"
	"github.com/gofiber/fiber/v3"
	fiberrecover "github.com/gofiber/fiber/v3/middleware/recover"

	"git.shino.boo/devilreef/cadence/server/internal/auth"
)

type Pinger interface {
	Ping(ctx context.Context) error
}

// Options wires the surface; later tasks add mounts through Admin and Device.
type Options struct {
	DB      Pinger
	Log     *slog.Logger
	Version string
	// VerifyToken answers a bearer API token's grants, its scopes and the MCP
	// tools it sees (M7a-D6)
	VerifyToken func(ctx context.Context, bearer string) (auth.Grants, error)
	// Sessions authenticates the owner's session cookie; nil: bearer only
	Sessions Sessions
	// Origin is the server's own origin a cookie request must come from
	// (CADENCE_PUBLIC_ORIGIN); empty: the request's scheme and Host
	Origin string
	// TrustedProxies are the addresses whose X-Forwarded-* headers name the
	// client and its scheme and host (CADENCE_TRUSTED_PROXY)
	TrustedProxies []string
	// CookieInsecure drops Secure and the name prefixes from the login's
	// cookies, for a plain-http dev stack (CADENCE_COOKIE_INSECURE)
	CookieInsecure bool
	Admin          []func(huma.API)
	// Tags are the document's top-level tags, one per area
	Tags []*huma.Tag
	// Plain are the admin routes that are no huma operation (the live feed,
	// the Prometheus exposition), each behind the scopes it names
	Plain []PlainRoute
	// MCP serves the MCP streamable endpoint at /mcp behind the mcp scope;
	// nil mounts nothing
	MCP    fiber.Handler
	Device []func(fiber.Router)
	// Trace runs first on every admin request (tracing.Middleware: a server
	// span each, refusals too); nil traces none
	Trace fiber.Handler
}

// PlainRoute is an admin route that is no huma operation. NewApp mounts it
// behind the auth middleware and its scopes: a caller needs one of them, admin
// has them all, and a route that names none is admin's alone.
type PlainRoute struct {
	// Op names the route in a refusal's log line
	Op, Method, Path string
	Scopes           []auth.Scope
	Handler          fiber.Handler
}

type health struct {
	Status  string `json:"status"`
	DB      string `json:"db"`
	Version string `json:"version"`
}

func NewApp(o Options) *fiber.App {
	// fiber recycles a request's buffers for the next one: immutable values
	// stay valid after their handler returns (Ruling M5d-R10)
	// the artifact upload posts a whole build (the midi tarball is 55 MiB, the cap is 512); fiber's
	// own default would refuse it at 4 MiB
	cfg := fiber.Config{Immutable: true, ErrorHandler: fiberErrors, BodyLimit: 640 << 20}
	if len(o.TrustedProxies) > 0 {
		// c.IP() walks X-Forwarded-For from the right past the trusted
		// proxies; from any other peer the headers are ignored
		cfg.TrustProxy, cfg.ProxyHeader, cfg.EnableIPValidation = true, fiber.HeaderXForwardedFor, true
		cfg.TrustProxyConfig = fiber.TrustProxyConfig{Proxies: o.TrustedProxies}
	}
	app := fiber.New(cfg)
	app.Use(fiberrecover.New(fiberrecover.Config{PanicHandler: panicked(o.Log)}))
	app.Get("/healthz", func(c fiber.Ctx) error {
		ctx, cancel := context.WithTimeout(c.Context(), time.Second)
		defer cancel()
		if err := o.DB.Ping(ctx); err != nil {
			return c.Status(fiber.StatusServiceUnavailable).JSON(health{"degraded", "down", o.Version})
		}
		return c.JSON(health{"ok", "ok", o.Version})
	})

	gate := authConfig{verify: o.VerifyToken, sessions: o.Sessions, origin: o.Origin, insecure: o.CookieInsecure, log: o.Log}
	handlers := []any{noStore, gate.handler}
	if o.Trace != nil {
		handlers = append([]any{o.Trace}, handlers...)
	}
	admin := app.Group(adminPrefix, handlers...)
	newHuma(app, admin, o.Version, o.Tags, o.Admin, o.Log)

	for _, r := range o.Plain {
		admin.Add([]string{r.Method}, r.Path, requireScope(r.Op, o.Log, r.Scopes...), r.Handler)
	}

	// the MCP endpoint answers every method the streamable transport may see
	// (the handler itself refuses what stateless mode does not serve)
	if o.MCP != nil {
		admin.Add([]string{fiber.MethodGet, fiber.MethodPost, fiber.MethodPut, fiber.MethodDelete, fiber.MethodPatch},
			"/mcp", requireScope("mcp", o.Log, auth.ScopeMCP), o.MCP)
	}

	device := app.Group("/v2")
	for _, mount := range o.Device {
		mount(device)
	}
	return app
}

// noStore keeps every admin answer out of caches, refusals included: the
// owner's devices, sessions and tokens change under a cached copy. A route
// that sets its own Cache-Control replaces it.
func noStore(c fiber.Ctx) error {
	c.Set(fiber.HeaderCacheControl, "no-store")
	return c.Next()
}

// newHuma mounts the admin operations on the group, the one way the server and
// the document generator both build the api. Huma's own docs page, schema and
// document routes stay off: they would bypass the scope gate. The document is
// served at openapi.json as an operation like the others (admin), nothing else.
func newHuma(app *fiber.App, group fiber.Router, version string, tags []*huma.Tag, ops []func(huma.API), log *slog.Logger) huma.API {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	cfg := huma.DefaultConfig("Cadence", version)
	cfg.DocsPath, cfg.OpenAPIPath, cfg.SchemasPath = "", "", ""
	// no $schema link: it would point at a placeholder host
	cfg.CreateHooks = nil
	cfg.Formats = map[string]huma.Format{"application/json": jsonFormat, "json": jsonFormat}
	cfg.Transformers = []huma.Transformer{emptyLists}
	cfg.Tags = tags
	// paths are relative to the group the api is mounted on
	cfg.Components.SecuritySchemes = map[string]*huma.SecurityScheme{
		"bearer": {
			Type: "http", Scheme: "bearer",
			Description: "an API token; an operation needs the scope its " + ScopeExtension + " names (admin has every scope), else 403 forbidden",
		},
		"cookie": {
			Type: "apiKey", In: "cookie", Name: SessionCookie,
			Description: "the owner's session from POST /session (cadence_session on a plain-http dev stack); a request other than GET, HEAD " +
				"or OPTIONS also sends " + CSRFHeader + ": 1 and, when it sends Origin or Referer, the server's own origin; a websocket upgrade sends that Origin",
		},
	}
	cfg.Security = []map[string][]string{{"bearer": {}}, {"cookie": {}}}
	cfg.Servers = []*huma.Server{{URL: adminPrefix}}
	humaAPI := humafiber.NewWithGroup(app, group, cfg)
	// before any operation: huma builds each one's chain as it registers
	humaAPI.UseMiddleware(scopeGate(humaAPI, log))
	for _, mount := range ops {
		mount(humaAPI)
	}
	documentScopes(humaAPI)
	documentLists(humaAPI)
	registerSpec(humaAPI)
	return humaAPI
}

// emptyLists answers a list with none as [], never null, so the document types
// every list answer as an array alone.
func emptyLists(_ huma.Context, _ string, v any) (any, error) {
	if rv := reflect.ValueOf(v); rv.Kind() == reflect.Slice && rv.IsNil() && rv.Type().Elem().Kind() != reflect.Uint8 {
		return reflect.MakeSlice(rv.Type(), 0, 0).Interface(), nil
	}
	return v, nil
}

// documentLists types each list answer as an array, not array|null: huma takes
// every slice for nullable, and emptyLists makes none null.
func documentLists(a huma.API) {
	for _, item := range a.OpenAPI().Paths {
		for _, op := range []*huma.Operation{item.Get, item.Put, item.Post, item.Delete, item.Patch, item.Head, item.Options, item.Trace} {
			if op == nil {
				continue
			}
			for status, resp := range op.Responses {
				if !strings.HasPrefix(status, "2") || resp == nil {
					continue
				}
				for _, mt := range resp.Content {
					if mt != nil && mt.Schema != nil && mt.Schema.Type == huma.TypeArray {
						mt.Schema.Nullable = false
					}
				}
			}
		}
	}
}

type specOut struct {
	ContentType string `header:"Content-Type"`
	Body        []byte
}

// registerSpec serves the document as huma would, as a hidden operation, so
// the scope gate holds it to admin.
func registerSpec(a huma.API) {
	var once sync.Once
	var doc []byte
	huma.Register(a, huma.Operation{OperationID: "get-openapi", Method: http.MethodGet, Path: "/openapi.json", Hidden: true},
		func(context.Context, *struct{}) (*specOut, error) {
			once.Do(func() { doc, _ = json.Marshal(a.OpenAPI()) })
			return &specOut{ContentType: "application/openapi+json", Body: doc}, nil
		})
}

// Spec is the admin api's OpenAPI document, built without a server: indented
// json with a trailing newline, its keys in a fixed order.
func Spec(version string, tags []*huma.Tag, ops []func(huma.API)) ([]byte, error) {
	app := fiber.New()
	doc, err := json.Marshal(newHuma(app, app.Group(adminPrefix), version, tags, ops, nil).OpenAPI())
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := json.Indent(&out, doc, "", "  "); err != nil {
		return nil, err
	}
	out.WriteByte('\n')
	return out.Bytes(), nil
}

// jsonFormat writes what fiber's c.JSON wrote (encoding/json's Marshal: html
// escaped, no trailing newline), so a route moved to huma answers the same bytes.
var jsonFormat = huma.Format{
	Marshal: func(w io.Writer, v any) error {
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		_, err = w.Write(b)
		return err
	},
	Unmarshal: json.Unmarshal,
}

// panicked answers a handler panic by event and route pattern only: its value
// can carry anything, so it reaches neither the log nor the response.
func panicked(log *slog.Logger) func(fiber.Ctx, any) error {
	return func(c fiber.Ctx, _ any) error {
		if log != nil {
			log.Error("http_panic", "method", c.Method(), "route", c.Route().Path)
		}
		c.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)
		return c.Status(fiber.StatusInternalServerError).SendString(`{"error":"internal"}`)
	}
}
