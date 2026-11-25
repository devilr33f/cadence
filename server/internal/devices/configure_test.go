package devices_test

import (
	"net/http"
	"strings"
	"testing"
)

func (e *env) configure(t *testing.T, body string) (int, string) {
	t.Helper()
	return call(t, e.app(), http.MethodPost, "/api/v1/devices/"+e.id+"/capabilities/audio/configure", body)
}

// configure stores the config with its secret as one generation and pushes them together; the same body again is a
// new generation (a retry), and a secret it does not name stays
func TestConfigurePushesConfigAndSecretTogether(t *testing.T) {
	e := newEnv(t)
	e.sendReport(t, report(secretDesc()))
	if code, _ := e.putSecret(t, "other", "keep-me"); code != 204 {
		t.Fatal(code)
	}
	for i := range 2 {
		code, body := e.configure(t, `{"config":{"level":5},"secrets":{"apiKey":"pw-12345678"}}`)
		if code != 200 || body != `{"generation":`+string(rune('2'+i))+`}` {
			t.Fatalf("%d %s", code, body)
		}
	}
	sets := e.hub.configSets()
	if len(sets) != 3 {
		t.Fatalf("%d sets", len(sets))
	}
	for _, s := range sets[1:] {
		if !strings.Contains(s.Json, `"level":5`) || s.Secrets["apiKey"] != "pw-12345678" || s.Secrets["other"] != "keep-me" {
			t.Fatalf("set gen %d: json %s, secrets %d", s.Generation, s.Json, len(s.Secrets))
		}
	}
	if strings.Contains(e.logs.String(), "pw-12345678") {
		t.Fatal("the secret reached the log")
	}
	if d := e.detail(t).Capabilities[0].Secrets; !d[0].Set {
		t.Fatal("secret not shown as set")
	}
}

// refusals are codes alone, never the secret, and store nothing
func TestConfigureRefusals(t *testing.T) {
	e := newEnv(t)
	e.sendReport(t, report(secretDesc()))
	path := "/api/v1/devices/" + e.id + "/capabilities/"
	for _, c := range []struct {
		path, body string
		code       int
		want       string
	}{
		{"nope/configure", `{"config":{},"secrets":{"apiKey":"pw-secret-1"}}`, 404, `{"error":"unknown_capability"}`},
		{"audio/configure", `{"config":{},"secrets":{"nope":"pw-secret-1"}}`, 404, `{"error":"unknown_secret"}`},
		{"audio/configure", `{"config":{},"secrets":{"apiKey":""}}`, 422, `{"error":"secret_invalid"}`},
		{"audio/configure", `{"config":{},"secrets":{"apiKey":"` + strings.Repeat("x", 4097) + `"}}`, 422, `{"error":"secret_invalid"}`},
		{"audio/configure", `{"config":{"level":500},"secrets":{"apiKey":"pw-secret-1"}}`, 422, ``},
		{"audio/configure", `{"secrets":{"apiKey":"pw-secret-1"}}`, 422, `{"error":"invalid"}`},
		{"audio/configure", `{"config":{},"secrets":{"apiKey":5}}`, 422, `{"error":"invalid"}`},
		{"audio/configure", `nonsense pw-secret-1`, 422, `{"error":"invalid"}`},
	} {
		code, body := call(t, e.app(), http.MethodPost, path+c.path, c.body)
		if code != c.code || (c.want != "" && body != c.want) || strings.Contains(body, "pw-secret-1") {
			t.Fatalf("%s %s: %d %s", c.path, c.body, code, body)
		}
	}
	if n := len(e.hub.configSets()); n != 0 {
		t.Fatalf("%d pushes after refusals", n)
	}
	if e.detail(t).Capabilities[0].Secrets[0].Set {
		t.Fatal("a refused secret was stored")
	}
}
