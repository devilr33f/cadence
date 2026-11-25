package app

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/api"
	"git.shino.boo/devilreef/cadence/server/internal/api/apitest"
)

const committedSpec = "../../openapi/openapi.json"

type specDoc struct {
	Info struct {
		Version string `json:"version"`
	} `json:"info"`
	Security   []map[string][]string `json:"security"`
	Components struct {
		SecuritySchemes map[string]struct {
			Type   string `json:"type"`
			Scheme string `json:"scheme"`
			In     string `json:"in"`
			Name   string `json:"name"`
		} `json:"securitySchemes"`
	} `json:"components"`
	Tags []struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	} `json:"tags"`
	Paths map[string]map[string]struct {
		OperationID string                 `json:"operationId"`
		Summary     string                 `json:"summary"`
		Tags        []string               `json:"tags"`
		Security    *[]map[string][]string `json:"security"`
		Parameters  []struct {
			Name string `json:"name"`
			In   string `json:"in"`
		} `json:"parameters"`
		RequestBody *struct {
			Content map[string]struct {
				Schema map[string]any `json:"schema"`
			} `json:"content"`
		} `json:"requestBody"`
		Responses map[string]struct {
			Content map[string]struct {
				Schema map[string]any `json:"schema"`
			} `json:"content"`
			Headers map[string]any `json:"headers"`
		} `json:"responses"`
	} `json:"paths"`
}

func generated(t *testing.T) ([]byte, specDoc) {
	t.Helper()
	raw, err := AdminSpec("0.0.0-dev")
	if err != nil {
		t.Fatal(err)
	}
	var doc specDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return raw, doc
}

// the committed document is what the code generates, so an operation change
// that skips `make openapi` fails here too
func TestCommittedSpecIsCurrent(t *testing.T) {
	raw, _ := generated(t)
	want, err := os.ReadFile(committedSpec)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, want) {
		t.Fatal("server/openapi/openapi.json is stale: run `make openapi`")
	}
	again, _ := generated(t)
	if !bytes.Equal(raw, again) {
		t.Fatal("two generations differ")
	}
	if raw[len(raw)-1] != '\n' || bytes.Contains(raw, []byte("\t")) {
		t.Fatal("want indented json with a trailing newline")
	}
}

// every operation has a summary and one tag, each tag is listed once with a
// description (M7a-D3)
func TestSpecHygiene(t *testing.T) {
	_, doc := generated(t)
	if doc.Info.Version != "0.0.0-dev" {
		t.Fatalf("info.version %q", doc.Info.Version)
	}
	listed := map[string]bool{}
	for _, tag := range doc.Tags {
		if tag.Description == "" || listed[tag.Name] {
			t.Errorf("tag %q: description %q, listed twice %v", tag.Name, tag.Description, listed[tag.Name])
		}
		listed[tag.Name] = true
	}
	used := map[string]bool{}
	for path, ops := range doc.Paths {
		for method, op := range ops {
			if op.OperationID == "" || op.Summary == "" || len(op.Tags) != 1 || !listed[op.Tags[0]] {
				t.Errorf("%s %s: id %q summary %q tags %v", method, path, op.OperationID, op.Summary, op.Tags)
				continue
			}
			used[op.Tags[0]] = true
		}
	}
	for name := range listed {
		if !used[name] {
			t.Errorf("tag %q has no operation", name)
		}
	}
}

// operations with a raw body or query declare them, without changing how the
// route parses them (Task 1 and 2 carry)
func TestRawRoutesAreDocumented(t *testing.T) {
	_, doc := generated(t)
	bodies := []string{
		"put-device-config", "put-device-secret", "send-device-command", "announce-to-device", "create-provider", "update-provider",
		"put-global-settings", "put-device-settings", "create-memory", "edit-memory", "reply-to-task", "approve-task",
		"patch-mcp-tool", "acknowledge-mcp-tools",
	}
	queries := map[string][]string{
		"list-memories":               {"deviceId", "kind", "limit", "offset", "pinned", "scope", "status"},
		"list-device-compactions":     {"limit"},
		"list-device-learning":        {"limit"},
		"list-tasks":                  {"device", "kind", "limit", "status"},
		"inspect-task":                {"detail"},
		"list-homeassistant-entities": {"area", "domain", "limit", "q"},
		"list-homeassistant-usage":    {"device", "limit"},
		// typed queries, which huma documents itself
		"list-logs":                {"device", "level", "limit", "q", "since", "source", "until"},
		"list-device-metrics":      {"name", "since"},
		"list-device-turns":        {"before", "limit"},
		"get-device-artwork":       {"h"},
		"delete-device":            {"status"},
		"list-subscriptions":       {"device"},
		"list-subscription-topics": {"prefix"},
		"get-subscription-inbox":   {"device"},
		"search-catalog":           {"cursor", "duration", "kind", "limit", "provider", "q", "sort", "source", "uploaded"},
		"browse-catalog":           {"cursor", "limit", "path"},
		"get-catalog-item":         {"choice", "cursor", "group", "q", "sort", "tab"},
		"get-catalog-options":      {"choice", "episode"},
		"get-catalog-panel":        {"cursor"},
		"list-play-history":        {"before", "deviceId", "limit"},
		"list-music-favorites":     {"limit", "query"},
		"list-traces":              {"device", "limit", "live", "since"},
		"list-device-artifacts":    {"board"},
		"upload-wake-word-model":   {"word"},
		"train-wake-word":          {"keep", "word"},
	}
	for path, ops := range doc.Paths {
		for method, op := range ops {
			if slices.Contains(bodies, op.OperationID) {
				c, ok := op.RequestBody.Content["application/json"]
				if op.RequestBody == nil || !ok || len(c.Schema) == 0 {
					t.Errorf("%s %s %s: no json request body", method, path, op.OperationID)
				}
			}
			var got []string
			for _, p := range op.Parameters {
				if p.In == "query" {
					got = append(got, p.Name)
				}
			}
			slices.Sort(got)
			if !slices.Equal(got, queries[op.OperationID]) {
				t.Errorf("%s: query %v, want %v", op.OperationID, got, queries[op.OperationID])
			}
		}
	}
	// a command's answer is {"result": ...} as json, not a binary string
	for _, ops := range doc.Paths {
		for _, op := range ops {
			if op.OperationID != "send-device-command" {
				continue
			}
			c, ok := op.Responses["200"].Content["application/json"]
			if _, header := op.Responses["200"].Headers["Content-Type"]; header || !ok || c.Schema["type"] == "string" {
				t.Errorf("command response content %v", op.Responses["200"].Content)
			}
		}
	}
}

// the served copy sits behind the admin bearer and carries the server version
func TestServedSpecNeedsBearer(t *testing.T) {
	app := api.NewApp(api.Options{
		DB: nopPinger{}, Version: "v9", Tags: adminTags,
		VerifyToken: apitest.Only("good"),
		Admin:       adminOps(adminDeps{}),
	})
	for _, tc := range []struct {
		header string
		status int
	}{{"", 401}, {"Bearer bad", 401}, {"Bearer good", 200}} {
		req := httptest.NewRequest("GET", "/api/v1/openapi.json", nil)
		if tc.header != "" {
			req.Header.Set("Authorization", tc.header)
		}
		resp, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != tc.status {
			t.Fatalf("%q: status %d, want %d", tc.header, resp.StatusCode, tc.status)
		}
		if tc.status == 200 {
			var doc specDoc
			if err := json.Unmarshal(body, &doc); err != nil || doc.Info.Version != "v9" || len(doc.Tags) == 0 {
				t.Fatalf("served doc: %v version %q tags %d", err, doc.Info.Version, len(doc.Tags))
			}
		}
	}
}

// every call needs the admin bearer or the owner's session cookie, and the
// document says so; login and the session state need neither
func TestSpecDeclaresBearerOrCookie(t *testing.T) {
	raw, doc := generated(t)
	b, c := doc.Components.SecuritySchemes["bearer"], doc.Components.SecuritySchemes["cookie"]
	if b.Type != "http" || b.Scheme != "bearer" || c.Type != "apiKey" || c.In != "cookie" || c.Name != "__Host-cadence_session" {
		t.Fatalf("schemes %+v", doc.Components.SecuritySchemes)
	}
	if len(doc.Security) != 2 || doc.Security[0]["bearer"] == nil || doc.Security[1]["cookie"] == nil {
		t.Fatalf("security %v", doc.Security)
	}
	open := map[string]bool{}
	for _, ops := range doc.Paths {
		for _, op := range ops {
			if op.Security != nil && len(*op.Security) == 0 {
				open[op.OperationID] = true
			} else if op.Security != nil {
				t.Errorf("%s: own security %v", op.OperationID, *op.Security)
			}
		}
	}
	if len(open) != 2 || !open["create-session"] || !open["get-session"] {
		t.Fatalf("operations without auth: %v", open)
	}
	// no example carries a secret: the session operations show none at all
	var all struct {
		Paths map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(raw, &all); err != nil {
		t.Fatal(err)
	}
	if s := all.Paths["/session"]; len(s) == 0 || bytes.Contains(s, []byte(`"example`)) ||
		!bytes.Contains(s, []byte(api.SessionCookie)) || !bytes.Contains(s, []byte(api.BrowserCookie)) {
		t.Fatalf("session operations: %s", s)
	}
}

// huma's other document and schema paths sit behind the bearer too
func TestSpecSiblingsNeedBearer(t *testing.T) {
	app := api.NewApp(api.Options{
		DB: nopPinger{}, Version: "t", Tags: adminTags,
		VerifyToken: apitest.Refuse,
		Admin:       adminOps(adminDeps{}),
	})
	for _, path := range []string{"/api/v1/openapi.yaml", "/api/v1/openapi-3.0.json", "/api/v1/openapi-3.0.yaml", "/api/v1/schemas/ErrorBody.json"} {
		resp, err := app.Test(httptest.NewRequest("GET", path, nil))
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != 401 {
			t.Errorf("%s: status %d, want 401", path, resp.StatusCode)
		}
	}
}

// every json answer is typed for the dashboard (M7b): a list is an array, never
// array|null (its handler answers [] for none), no answer is a bare {} or a
// free-form object, and the settings answers name the keys a put takes, with
// none of them null (a null removes a key and is never stored)
func TestSpecRepliesAreTyped(t *testing.T) {
	raw, doc := generated(t)
	var named struct {
		Components struct {
			Schemas map[string]map[string]any `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(raw, &named); err != nil {
		t.Fatal(err)
	}
	// resolve follows a $ref to the named schema
	resolve := func(s map[string]any) map[string]any {
		if ref, ok := s["$ref"].(string); ok {
			return named.Components.Schemas[strings.TrimPrefix(ref, "#/components/schemas/")]
		}
		return s
	}
	var putKeys []string
	for _, ops := range doc.Paths {
		for _, op := range ops {
			if op.OperationID == "put-global-settings" {
				for k := range op.RequestBody.Content["application/json"].Schema["properties"].(map[string]any) {
					putKeys = append(putKeys, k)
				}
			}
		}
	}
	slices.Sort(putKeys)
	if len(putKeys) == 0 {
		t.Fatal("no settings keys in the put body")
	}
	for path, ops := range doc.Paths {
		for method, op := range ops {
			for status, r := range op.Responses {
				c, ok := r.Content["application/json"]
				if status[0] != '2' || !ok {
					continue
				}
				s := resolve(c.Schema)
				if len(s) == 0 {
					t.Errorf("%s %s %s: an untyped answer", method, path, status)
				}
				if typ, _ := s["type"].([]any); len(typ) > 0 {
					t.Errorf("%s: answer type %v", op.OperationID, typ)
				}
				if ap, ok := s["additionalProperties"]; ok && ap != false {
					t.Errorf("%s: a free-form answer", op.OperationID)
				}
				if !strings.HasSuffix(op.OperationID, "-settings") {
					continue
				}
				props, _ := s["properties"].(map[string]any)
				var keys []string
				for k, p := range props {
					keys = append(keys, k)
					if typ, _ := p.(map[string]any)["type"].([]any); len(typ) > 0 {
						t.Errorf("%s: %s is %v", op.OperationID, k, typ)
					}
				}
				slices.Sort(keys)
				if !slices.Equal(keys, putKeys) {
					t.Errorf("%s: keys %v, want %v", op.OperationID, keys, putKeys)
				}
			}
		}
	}
}

// the document says what the server sends (dashboard lane): get-task's answer
// holds the task's own fields beside its history, a task's question is null
// when it asks nothing, and the Home Assistant status names its version
func TestAnswersDeclareWhatIsSent(t *testing.T) {
	raw, _ := generated(t)
	var doc struct {
		Paths      map[string]map[string]json.RawMessage `json:"paths"`
		Components struct {
			Schemas map[string]struct {
				Properties map[string]struct {
					Type any    `json:"type"`
					Ref  string `json:"$ref"`
				} `json:"properties"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	schemaOf := func(path string) string {
		t.Helper()
		var op struct {
			Responses map[string]struct {
				Content map[string]struct {
					Schema struct {
						Ref string `json:"$ref"`
					} `json:"schema"`
				} `json:"content"`
			} `json:"responses"`
		}
		if err := json.Unmarshal(doc.Paths[path]["get"], &op); err != nil {
			t.Fatal(err)
		}
		ref := op.Responses["200"].Content["application/json"].Schema.Ref
		return strings.TrimPrefix(ref, "#/components/schemas/")
	}
	detail := doc.Components.Schemas[schemaOf("/tasks/{id}")]
	for _, field := range []string{"shortId", "kind", "title", "deviceId", "status", "progress", "question", "errorCode",
		"createdAt", "updatedAt", "finishedAt", "reportedAt", "result", "events", "notices"} {
		if _, ok := detail.Properties[field]; !ok {
			t.Errorf("get-task lacks %s", field)
		}
	}
	for _, name := range []string{schemaOf("/tasks/{id}"), schemaOf("/tasks/{id}/inspect")} {
		q := doc.Components.Schemas[name].Properties["question"]
		if types, _ := q.Type.([]any); !slices.Contains(types, any("null")) || !slices.Contains(types, any("object")) {
			t.Errorf("%s question: %v %s", name, q.Type, q.Ref)
		}
	}
	if v := doc.Components.Schemas[schemaOf("/integrations/homeassistant")].Properties["version"]; v.Type == nil {
		t.Error("the home assistant status names no version")
	}
}
