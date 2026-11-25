package otlphttp_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/jsonschemax"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/providers/otlphttp"
)

func TestModule(t *testing.T) {
	m := otlphttp.Module
	if m.ID != "otlp-http" || m.Kind != providers.KindOTLP || len(m.Secrets) != 0 ||
		len(m.OptionalSecrets) != 1 || m.OptionalSecrets[0] != "authorization" {
		t.Fatalf("module %+v", m)
	}
	schema, err := jsonschemax.Compile(m.ConfigSchema)
	if err != nil {
		t.Fatal(err)
	}
	for _, cfg := range []string{
		`{}`, `{"url":"http://o.example"}`, `{"url":"https://o.example","x":1}`,
		`{"url":"https://o.example/` + strings.Repeat("a", 2048) + `"}`,
		`{"url":"https://o.example","timeoutMs":999}`, `{"url":"https://o.example","timeoutMs":30001}`,
		`{"url":"https://o.example","timeoutMs":1500.5}`,
	} {
		if _, ok := jsonschemax.Validate(schema, json.RawMessage(cfg)); ok {
			t.Errorf("schema took %s", cfg)
		}
	}
	for cfg, timeout := range map[string]time.Duration{
		`{"url":"https://o.example"}`:                             10 * time.Second,
		`{"url":"https://o.example:4318/otlp/","timeoutMs":1000}`: time.Second,
		`{"url":"https://o.example","timeoutMs":30000}`:           30 * time.Second,
	} {
		if _, ok := jsonschemax.Validate(schema, json.RawMessage(cfg)); !ok {
			t.Errorf("schema refused %s", cfg)
		}
		for _, sec := range []map[string]string{nil, {"authorization": "Bearer t0k"}} {
			v, err := m.New(json.RawMessage(cfg), sec)
			c, ok := v.(*otlphttp.Client)
			if err != nil || !ok || c.Timeout() != timeout || c.Host() == "" || strings.Contains(c.Host(), "/") {
				t.Fatalf("%s: %T %v", cfg, v, err)
			}
		}
	}
	// the endpoint's rules hold when the config is checked, so a URL the
	// client refuses (a credential in its userinfo or query) is never stored
	for url, ok := range map[string]bool{
		"https://o.example": true, "https://o.example:4318/otlp": true, "https://o.example/a/": true,
		"https://u:pw@o.example": false, "https://u@o.example": false, "https://o.example?k=pw": false,
		"https://o.example/?": false, "https://o.example/#pw": false, "https://o.example/a/../b": false,
		"https://o.example/%2e%2e/b": false, "https://o.example/a\\b": false, "https://o.example/a b": false,
		"https://o.example:port": false, "https://:443/": false,
	} {
		cfg, _ := json.Marshal(map[string]string{"url": url})
		path, checked := m.Check(cfg)
		_, err := m.New(cfg, nil)
		if checked != ok || (err == nil) != ok || (!ok && path != "/url") {
			t.Errorf("%q: checked %v at %q, built %v, want %v", url, checked, path, err, ok)
		}
		if err != nil && strings.Contains(err.Error(), "pw") {
			t.Errorf("%q: error names the url: %v", url, err)
		}
	}
	for _, auth := range []string{"Bearer a\nb", strings.Repeat("a", 4097), "Bearer é"} {
		if _, err := m.New(json.RawMessage(`{"url":"https://o.example"}`), map[string]string{"authorization": auth}); err == nil ||
			strings.Contains(err.Error(), auth) {
			t.Errorf("authorization %.20q: %v", auth, err)
		}
	}
	if _, err := m.New(json.RawMessage(`not json`), nil); err == nil {
		t.Error("bad config built")
	}
}
