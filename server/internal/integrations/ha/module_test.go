package ha_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/integrations/ha"
	"git.shino.boo/devilreef/cadence/server/internal/jsonschemax"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

func TestModule(t *testing.T) {
	m := ha.Module
	if m.ID != "homeassistant" || m.Kind != providers.KindHomeAssistant || len(m.Secrets) != 1 || m.Secrets[0] != "token" {
		t.Fatalf("module %+v", m)
	}
	schema, err := jsonschemax.Compile(m.ConfigSchema)
	if err != nil {
		t.Fatal(err)
	}
	for _, cfg := range []string{
		`{}`, `{"baseUrl":"ftp://ha.lan"}`, `{"baseUrl":"ha.lan:8123"}`, `{"baseUrl":"http://ha.lan","x":1}`,
		`{"baseUrl":"https://ha.lan/` + strings.Repeat("a", 512) + `"}`,
	} {
		if _, ok := jsonschemax.Validate(schema, json.RawMessage(cfg)); ok {
			t.Errorf("schema took %s", cfg)
		}
	}
	for _, cfg := range []string{`{"baseUrl":"http://ha.lan:8123"}`, `{"baseUrl":"https://ha.example/prefix/"}`} {
		if _, ok := jsonschemax.Validate(schema, json.RawMessage(cfg)); !ok {
			t.Errorf("schema refused %s", cfg)
		}
		v, err := m.New(json.RawMessage(cfg), map[string]string{"token": token})
		if _, ok := v.(*ha.Client); err != nil || !ok {
			t.Fatalf("%s: %T %v", cfg, v, err)
		}
	}
	if _, err := m.New(json.RawMessage(`{"baseUrl":"https://ha.example"}`), map[string]string{"token": ""}); err == nil {
		t.Error("an empty token accepted")
	}
	if _, err := m.New(json.RawMessage(`not json`), map[string]string{"token": token}); err == nil {
		t.Error("bad config accepted")
	}
}

func TestBaseURLRules(t *testing.T) {
	for base, want := range map[string]string{
		"http://ha.lan:8123":         "ws://ha.lan:8123/api/websocket",
		"https://ha.example":         "wss://ha.example/api/websocket",
		"https://ha.example///":      "wss://ha.example/api/websocket",
		"https://ha.example/ha/":     "wss://ha.example/ha/api/websocket",
		"https://ha.example/a/b//":   "wss://ha.example/a/b/api/websocket",
		"http://192.168.10.10:8123/": "ws://192.168.10.10:8123/api/websocket",
	} {
		c, err := ha.NewClient(ha.ClientConfig{BaseURL: base, Token: token})
		if err != nil {
			t.Fatalf("%s: %v", base, err)
		}
		if got := ha.URL(c); got != want {
			t.Errorf("%s: %s, want %s", base, got, want)
		}
	}
	for _, bad := range []string{
		"", "ha.lan", "ftp://ha.lan", "ws://ha.lan", "https://", "https://user:pw@ha.lan", "https://user@ha.lan",
		"https://ha.lan/?x=1", "https://ha.lan/#frag", "https://ha.lan/a b", " https://ha.lan", "https://ha.lan/\t",
		"https://ha.lan/" + strings.Repeat("a", 512),
	} {
		_, err := ha.NewClient(ha.ClientConfig{BaseURL: bad, Token: token})
		if !errors.Is(err, ha.ErrBadURL) {
			t.Errorf("%q: %v", bad, err)
			continue
		}
		if strings.Contains(err.Error(), "pw") || strings.Contains(err.Error(), "ha.lan") {
			t.Errorf("%q: error names the url: %v", bad, err)
		}
	}
	if _, err := ha.NewClient(ha.ClientConfig{BaseURL: "https://ha.lan"}); err == nil {
		t.Error("no token accepted")
	}
}
