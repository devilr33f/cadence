package t3code_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/integrations/t3code"
	"git.shino.boo/devilreef/cadence/server/internal/integrations/t3code/t3codetest"
	"git.shino.boo/devilreef/cadence/server/internal/jsonschemax"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

func TestModule(t *testing.T) {
	m := t3code.Module
	if m.ID != "t3code" || m.Kind != providers.KindT3Code || len(m.Secrets) != 1 || m.Secrets[0] != "token" || m.Probe == nil {
		t.Fatalf("module %+v", m)
	}
	schema, err := jsonschemax.Compile(m.ConfigSchema)
	if err != nil {
		t.Fatal(err)
	}
	for _, cfg := range []string{`{}`, `{"baseUrl":"ftp://h"}`, `{"baseUrl":"https://h","x":1}`, `{"baseUrl":"https://"}`} {
		if _, ok := jsonschemax.Validate(schema, json.RawMessage(cfg)); ok {
			t.Errorf("schema took %s", cfg)
		}
	}
	for _, cfg := range []string{`{"baseUrl":"http://192.168.1.5:3773"}`, `{"baseUrl":"https://t3.example/prefix/"}`} {
		if _, ok := jsonschemax.Validate(schema, json.RawMessage(cfg)); !ok {
			t.Errorf("schema refused %s", cfg)
		}
		v, err := m.New(json.RawMessage(cfg), map[string]string{"token": "k"})
		if _, ok := v.(*t3code.Client); err != nil || !ok {
			t.Fatalf("%s: %T %v", cfg, v, err)
		}
	}
	for _, cfg := range []string{`{"baseUrl":"https://u:pw@h"}`, `not json`} {
		if _, err := m.New(json.RawMessage(cfg), map[string]string{"token": "k"}); err == nil || strings.Contains(err.Error(), "pw") {
			t.Errorf("%s: %v", cfg, err)
		}
	}
	if _, err := m.New(json.RawMessage(`{"baseUrl":"https://h"}`), nil); err == nil {
		t.Error("no token accepted")
	}
}

// probeWith builds the module's client for srv with token and runs its
// probe through srv's own transport.
func probeWith(t *testing.T, srv *httptest.Server, token string) error {
	t.Helper()
	c, err := t3code.NewClient(t3code.ClientConfig{BaseURL: srv.URL, Token: token, HTTP: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	return t3code.Module.Probe(context.Background(), c)
}

func TestProbe(t *testing.T) {
	fake := httptest.NewServer(t3codetest.New(t3codetest.Config{Token: testToken}))
	defer fake.Close()
	if err := probeWith(t, fake, testToken); err != nil {
		t.Fatalf("probe: %v", err)
	}
	if code := providers.ProbeCode(probeWith(t, fake, "wrong")); code != string(providers.ProbeAuth) {
		t.Fatalf("wrong token: %s", code)
	}
	var sawHeader, sawBearer bool
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawHeader = r.Header.Get("x-t3-orchestration-protocol") == "2"
		sawBearer = r.Header.Get("Authorization") == "Bearer "+testToken
		switch r.URL.Path {
		case "/old/api/orchestration/shell":
			w.WriteHeader(http.StatusUpgradeRequired)
		case "/json/api/orchestration/shell":
			_, _ = w.Write([]byte(`{"hello":"world"}`))
		default:
			w.WriteHeader(http.StatusTooManyRequests)
		}
	}))
	defer other.Close()
	for path, want := range map[string]providers.ProbeError{"/old": providers.ProbeBadResponse, "/json": providers.ProbeBadResponse, "/busy": providers.ProbeRateLimited} {
		c, _ := t3code.NewClient(t3code.ClientConfig{BaseURL: other.URL + path, Token: testToken, HTTP: other.Client()})
		if code := providers.ProbeCode(t3code.Module.Probe(context.Background(), c)); code != string(want) {
			t.Errorf("%s: %s, want %s", path, code, want)
		}
	}
	if !sawHeader || !sawBearer {
		t.Error("protocol header or bearer missing")
	}
	other.Close()
	c, _ := t3code.NewClient(t3code.ClientConfig{BaseURL: other.URL, Token: testToken, HTTP: other.Client()})
	err := t3code.Module.Probe(context.Background(), c)
	if code := providers.ProbeCode(err); code != string(providers.ProbeUnreachable) || strings.Contains(err.Error(), testToken) {
		t.Fatalf("closed server: %v", err)
	}
	if code := providers.ProbeCode(t3code.Module.Probe(context.Background(), "not a client")); code != string(providers.ProbeBadResponse) {
		t.Fatalf("wrong instance: %s", code)
	}
}
