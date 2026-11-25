package mcp_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/integrations/mcp"
	"git.shino.boo/devilreef/cadence/server/internal/jsonschemax"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

func TestModule(t *testing.T) {
	m := mcp.Module
	if m.ID != "mcp-http" || m.Kind != providers.KindMCP || len(m.Secrets) != 0 ||
		len(m.OptionalSecrets) != 1 || m.OptionalSecrets[0] != "authorization" {
		t.Fatalf("module %+v", m)
	}
	schema, err := jsonschemax.Compile(m.ConfigSchema)
	if err != nil {
		t.Fatal(err)
	}
	for _, cfg := range []string{
		`{}`, `{"url":"http://m.example/mcp"}`, `{"url":"https://m.example/mcp","x":1}`,
		`{"url":"https://m.example/` + strings.Repeat("a", 2048) + `"}`,
		`{"url":"https://m.example/mcp","toolPrefix":"Bad"}`, `{"url":"https://m.example/mcp","toolPrefix":"1a"}`,
		`{"url":"https://m.example/mcp","toolPrefix":"abcdefghijklmnopq"}`,
		`{"url":"https://m.example/mcp","timeoutMs":999}`, `{"url":"https://m.example/mcp","timeoutMs":30001}`,
		`{"url":"https://m.example/mcp","timeoutMs":1500.5}`,
		`{"url":"https://m.example/mcp","maxResultChars":255}`, `{"url":"https://m.example/mcp","maxResultChars":65537}`,
	} {
		if _, ok := jsonschemax.Validate(schema, json.RawMessage(cfg)); ok {
			t.Errorf("schema took %s", cfg)
		}
	}
	type want struct {
		timeout time.Duration
		chars   int
		prefix  string
	}
	for cfg, w := range map[string]want{
		`{"url":"https://m.example/mcp"}`: {8 * time.Second, 8192, ""},
		`{"url":"https://m.example:8443/a/mcp","toolPrefix":"a_b1","timeoutMs":1000,"maxResultChars":256}`:         {time.Second, 256, "a_b1"},
		`{"url":"https://m.example/mcp","toolPrefix":"abcdefghijklmnop","timeoutMs":30000,"maxResultChars":65536}`: {30 * time.Second, 65536, "abcdefghijklmnop"},
	} {
		if _, ok := jsonschemax.Validate(schema, json.RawMessage(cfg)); !ok {
			t.Errorf("schema refused %s", cfg)
		}
		for _, sec := range []map[string]string{nil, {"authorization": "Bearer t0k"}} {
			v, err := m.New(json.RawMessage(cfg), sec)
			c, ok := v.(*mcp.Client)
			if err != nil || !ok || c.Timeout() != w.timeout || c.MaxResultChars() != w.chars || c.ToolPrefix() != w.prefix {
				t.Fatalf("%s: %T %v", cfg, v, err)
			}
			if st := c.Status(); st.State != mcp.StateUnavailable || st.Host == "" || strings.Contains(st.Host, "/") {
				t.Fatalf("status %+v", st)
			}
		}
	}
	for _, cfg := range []string{`{"url":"https://u:pw@m.example/mcp"}`, `{"url":"https://m.example/mcp?k=pw"}`, `not json`} {
		if _, err := m.New(json.RawMessage(cfg), nil); err == nil || strings.Contains(err.Error(), "pw") {
			t.Errorf("%s: %v", cfg, err)
		}
	}
	// the endpoint's rules hold when the config is checked, so a URL the
	// client refuses (a credential in its userinfo or query) is never stored
	for url, ok := range map[string]bool{
		"https://m.example/mcp": true, "https://m.example:8443/a/mcp": true, "https://m.example/a@b/mcp": true,
		"https://u:pw@m.example/mcp": false, "https://u@m.example/mcp": false, "https://m.example/mcp?k=pw": false,
		"https://m.example/mcp?": false, "https://m.example/mcp#pw": false, "https://m.example/a/../mcp": false,
		"https://m.example/%2e%2e/mcp": false, "https://m.example/a\\b": false, "https://m.example/a b": false,
		"https://m.example:port/mcp": false, "https://:443/mcp": false,
	} {
		cfg, _ := json.Marshal(map[string]string{"url": url})
		path, checked := m.Check(cfg)
		_, err := m.New(cfg, nil)
		if checked != ok || (err == nil) != ok || (!ok && path != "/url") {
			t.Errorf("%q: checked %v at %q, built %v, want %v", url, checked, path, err, ok)
		}
	}
	for _, bad := range []string{"Bearer\nsecret", "Bearer sécret", strings.Repeat("s", 4097)} {
		_, err := m.New(json.RawMessage(`{"url":"https://m.example/mcp"}`), map[string]string{"authorization": bad})
		if err == nil || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "sss") {
			t.Errorf("authorization %q: %v", bad[:8], err)
		}
	}
}
