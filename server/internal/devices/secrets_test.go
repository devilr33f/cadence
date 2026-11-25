package devices_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"strings"
	"testing"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/server/internal/devices"
)

const secretPath = "/capabilities/audio/secrets/apiKey"

func secretDesc() *cadencev2.Descriptor {
	d := desc("audio")
	d.Secrets = []*cadencev2.SecretSpec{{Name: "apiKey", Description: "the key"}, {Name: "other", Description: "another"}}
	return d
}

func (e *env) putSecret(t *testing.T, name, value string) (int, string) {
	t.Helper()
	b, _ := json.Marshal(map[string]string{"value": value})
	return call(t, e.app(), http.MethodPut, "/api/v1/devices/"+e.id+"/capabilities/audio/secrets/"+name, string(b))
}

func TestSecretSetPushesAndDetailShowsFlagsOnly(t *testing.T) {
	e := newEnv(t)
	e.sendReport(t, report(secretDesc()))
	if code, body := e.putSecret(t, "apiKey", "s3cret-value"); code != 204 || body != "" {
		t.Fatalf("%d %s", code, body)
	}
	sets := e.hub.configSets()
	if len(sets) != 1 || sets[0].Generation != 1 || sets[0].Json != "{}" || sets[0].Secrets["apiKey"] != "s3cret-value" || len(sets[0].Secrets) != 1 {
		t.Fatalf("%d sets", len(sets))
	}
	code, body := call(t, e.app(), http.MethodGet, "/api/v1/devices/"+e.id, "")
	if code != 200 || strings.Contains(body, "s3cret-value") {
		t.Fatalf("%d leak=%v", code, strings.Contains(body, "s3cret-value"))
	}
	var got struct {
		Capabilities []struct {
			Secrets []struct {
				Name, Description string
				Set               bool
			}
		}
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err)
	}
	s := got.Capabilities[0].Secrets
	if len(s) != 2 || s[0].Name != "apiKey" || s[0].Description != "the key" || !s[0].Set || s[1].Name != "other" || s[1].Set {
		t.Fatalf("%+v", s)
	}
}

func TestSecretStaysAcrossConfigChangeAndDeleteClearsIt(t *testing.T) {
	e := newEnv(t)
	e.sendReport(t, report(secretDesc()))
	e.putSecret(t, "apiKey", "v1")
	e.putSecret(t, "other", "v2")
	if _, err := e.svc.SetConfig(context.Background(), e.id, "audio", json.RawMessage(`{"level":5}`)); err != nil {
		t.Fatal(err)
	}
	sets := e.hub.configSets()
	last := sets[len(sets)-1]
	if len(sets) != 3 || last.Generation != 3 || !strings.Contains(last.Json, "5") || last.Secrets["apiKey"] != "v1" || last.Secrets["other"] != "v2" {
		t.Fatalf("%d sets", len(sets))
	}
	code, _ := call(t, e.app(), http.MethodDelete, "/api/v1/devices/"+e.id+secretPath, "")
	sets = e.hub.configSets()
	last = sets[len(sets)-1]
	if code != 204 || len(sets) != 4 || last.Generation != 4 || !strings.Contains(last.Json, "5") ||
		len(last.Secrets) != 1 || last.Secrets["other"] != "v2" {
		t.Fatalf("%d %d sets", code, len(sets))
	}
	if d := e.detail(t).Capabilities[0].Secrets; d[0].Set {
		t.Fatal("still set")
	}
}

func TestSecretOfflineArrivesAtHello(t *testing.T) {
	e := newEnv(t)
	r := report(secretDesc())
	e.sendReport(t, r)
	e.hub.setOnline(false)
	if code, _ := e.putSecret(t, "apiKey", "late"); code != 204 {
		t.Fatal(code)
	}
	if len(e.hub.configSets()) != 0 {
		t.Fatal("pushed offline")
	}
	e.hub.setOnline(true)
	e.svc.Hello(context.Background(), e.id, &cadencev2.Hello{CapabilitiesHash: r.Hash})
	sets := e.hub.configSets()
	if len(sets) != 1 || sets[0].Secrets["apiKey"] != "late" {
		t.Fatalf("%d sets", len(sets))
	}
}

func TestSecretErrors(t *testing.T) {
	e := newEnv(t)
	e.sendReport(t, report(secretDesc()))
	base := "/api/v1/devices/" + e.id
	for _, c := range []struct {
		method, path, body string
		code               int
		want               string
	}{
		{http.MethodPut, base + "/capabilities/audio/secrets/nope", `{"value":"x"}`, 404, `{"error":"unknown_secret"}`},
		{http.MethodDelete, base + "/capabilities/audio/secrets/nope", ``, 404, `{"error":"unknown_secret"}`},
		{http.MethodPut, base + "/capabilities/nope/secrets/apiKey", `{"value":"x"}`, 404, `{"error":"unknown_capability"}`},
		{http.MethodDelete, base + "/capabilities/nope/secrets/apiKey", ``, 404, `{"error":"unknown_capability"}`},
		{http.MethodPut, base + secretPath, `{"value":""}`, 422, `{"error":"secret_invalid"}`},
		{http.MethodPut, base + secretPath, `{}`, 422, `{"error":"secret_invalid"}`},
		{http.MethodPut, base + secretPath, `{"value":5}`, 422, `{"error":"secret_invalid"}`},
		{http.MethodPut, base + secretPath, `nonsense`, 422, `{"error":"secret_invalid"}`},
		{http.MethodPut, base + secretPath, ``, 422, `{"error":"secret_invalid"}`},
		// a bad body reaches the name check first
		{http.MethodPut, base + "/capabilities/audio/secrets/nope", `nonsense`, 404, `{"error":"unknown_secret"}`},
		{http.MethodPut, base + secretPath, `{"value":"` + strings.Repeat("x", 4097) + `"}`, 422, `{"error":"secret_invalid"}`},
	} {
		if code, body := call(t, e.app(), c.method, c.path, c.body); code != c.code || body != c.want {
			t.Fatalf("%s %s: %d %s", c.method, c.path, code, body)
		}
	}
	if n := len(e.hub.configSets()); n != 0 {
		t.Fatalf("%d pushes after refusals", n)
	}
	if code, _ := e.putSecret(t, "apiKey", strings.Repeat("x", 4096)); code != 204 {
		t.Fatalf("4096 bytes refused: %d", code)
	}
}

func TestSecretCountsTowardFrameLimit(t *testing.T) {
	e := newEnv(t)
	d := secretDesc()
	d.ConfigSchema = ""
	e.sendReport(t, report(d))
	cfg := `{"blob":"` + strings.Repeat("x", devices.MaxConfigFrameBytes-300) + `"}`
	if _, err := e.svc.SetConfig(context.Background(), e.id, "audio", json.RawMessage(cfg)); err != nil {
		t.Fatal(err)
	}
	code, body := e.putSecret(t, "apiKey", strings.Repeat("y", 4000))
	if code != 413 || body != `{"error":"config_too_large"}` {
		t.Fatalf("%d %s", code, body)
	}
	if e.detail(t).Capabilities[0].Secrets[0].Set {
		t.Fatal("oversized secret stored")
	}
	if code, _ := e.putSecret(t, "apiKey", strings.Repeat("y", 200)); code != 204 {
		t.Fatal(code)
	}
	big := `{"blob":"` + strings.Repeat("x", devices.MaxConfigFrameBytes-150) + `"}`
	if _, err := e.svc.SetConfig(context.Background(), e.id, "audio", json.RawMessage(big)); !errors.Is(err, devices.ErrConfigTooLarge) {
		t.Fatalf("err=%v", err)
	}
}

func TestSecretSealedAtRestBoundToItsAAD(t *testing.T) {
	e := newEnv(t)
	e.sendReport(t, report(secretDesc()))
	e.putSecret(t, "apiKey", "plain-text-secret")
	var sealed []byte
	if err := e.pool.QueryRow(context.Background(), `SELECT sealed FROM device_capability_secrets`).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, []byte("plain-text-secret")) {
		t.Fatal("stored in clear")
	}
	if got, err := e.box.Open(sealed, devices.SecretAAD(e.id, "audio", "apiKey")); err != nil || string(got) != "plain-text-secret" {
		t.Fatalf("own aad: %v", err)
	}
	for _, aad := range [][]byte{
		devices.SecretAAD("00000000-0000-0000-0000-000000000001", "audio", "apiKey"),
		devices.SecretAAD(e.id, "leds", "apiKey"),
		devices.SecretAAD(e.id, "audio", "other"),
	} {
		if _, err := e.box.Open(sealed, aad); err == nil {
			t.Fatal("opened with another aad")
		}
	}
}

func TestSecretRowCopiedElsewhereIsNotPushed(t *testing.T) {
	e := newEnv(t)
	r := report(secretDesc())
	e.sendReport(t, r)
	e.hub.setOnline(false)
	e.putSecret(t, "apiKey", "v")
	if _, err := e.pool.Exec(context.Background(), `UPDATE device_capability_secrets SET name = 'other'`); err != nil {
		t.Fatal(err)
	}
	e.hub.setOnline(true)
	e.svc.Hello(context.Background(), e.id, &cadencev2.Hello{CapabilitiesHash: r.Hash})
	// sending without the secrets would make the device clear them
	if len(e.hub.configSets()) != 0 || !strings.Contains(e.logs.String(), "config_push_failed") {
		t.Fatal("pushed a config whose secrets did not open")
	}
	if _, err := e.svc.SetConfig(context.Background(), e.id, "audio", json.RawMessage(`{}`)); err == nil {
		t.Fatal("config accepted over unreadable secrets")
	}
}

func TestStaleSecretsStayInertAndReturnWithTheirDeclaration(t *testing.T) {
	e := newEnv(t)
	e.sendReport(t, report(secretDesc(), desc("leds")))
	e.putSecret(t, "apiKey", "a")
	e.putSecret(t, "other", "b")
	n := len(e.hub.configSets())
	// an older build reports without the capability: rows stay, nothing is pushed for it
	e.sendReport(t, report(desc("leds")))
	if len(e.hub.configSets()) != n {
		t.Fatal("pushed for a dropped capability")
	}
	var rows int
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM device_capability_secrets`).Scan(&rows)
	if rows != 2 {
		t.Fatalf("%d rows kept", rows)
	}
	// it returns declaring one name only: the undeclared stored one is never sent
	d := desc("audio")
	d.Secrets = []*cadencev2.SecretSpec{{Name: "apiKey"}}
	e.sendReport(t, report(d, desc("leds")))
	if _, err := e.svc.SetConfig(context.Background(), e.id, "audio", json.RawMessage(`{"level":1}`)); err != nil {
		t.Fatal(err)
	}
	sets := e.hub.configSets()
	last := sets[len(sets)-1]
	if len(last.Secrets) != 1 || last.Secrets["apiKey"] != "a" {
		t.Fatalf("sent %d secrets", len(last.Secrets))
	}
	// declared again: the stored value goes out again
	e.sendReport(t, report(secretDesc(), desc("leds")))
	if _, err := e.svc.SetConfig(context.Background(), e.id, "audio", json.RawMessage(`{"level":2}`)); err != nil {
		t.Fatal(err)
	}
	sets = e.hub.configSets()
	if last = sets[len(sets)-1]; last.Secrets["apiKey"] != "a" || last.Secrets["other"] != "b" {
		t.Fatal("secrets not sent again")
	}
}

func TestSecretResetRecoversCorruptedRow(t *testing.T) {
	e := newEnv(t)
	e.sendReport(t, report(secretDesc()))
	e.putSecret(t, "apiKey", "old")
	if _, err := e.pool.Exec(context.Background(), `UPDATE device_capability_secrets SET sealed = $1`, []byte{1, 2}); err != nil {
		t.Fatal(err)
	}
	if code, _ := e.putSecret(t, "apiKey", "new"); code != 204 {
		t.Fatalf("re-set refused: %d", code)
	}
	sets := e.hub.configSets()
	if last := sets[len(sets)-1]; last.Secrets["apiKey"] != "new" {
		t.Fatal("new value not pushed")
	}
}

// guard (rule 6): a secret value is never in a log line, a response, an error
// or any table in clear
func TestSecretValueLeavesNoTrace(t *testing.T) {
	e := newEnv(t)
	e.sendReport(t, report(secretDesc()))
	const value = "Zq9-private-secret-value"
	var out strings.Builder
	_, body := e.putSecret(t, "apiKey", value)
	out.WriteString(body)
	_, body = call(t, e.app(), http.MethodGet, "/api/v1/devices/"+e.id, "")
	out.WriteString(body)
	_, body = e.putSecret(t, "nope", value)
	out.WriteString(body)
	_, body = call(t, e.app(), http.MethodPut, "/api/v1/devices/"+e.id+secretPath, `{"value":"`+value+`"`)
	out.WriteString(body)
	_, body = call(t, e.app(), http.MethodDelete, "/api/v1/devices/"+e.id+secretPath, "")
	out.WriteString(body)
	e.hub.setOnline(false)
	e.putSecret(t, "apiKey", value)
	logs := e.logs.String()
	out.WriteString(logs)
	for _, want := range []string{"capability_secret", "action=set", "action=delete", "name=apiKey"} {
		if !strings.Contains(logs, want) {
			t.Fatalf("no audit line with %s", want)
		}
	}
	rows, err := e.pool.Query(context.Background(),
		`SELECT table_name FROM information_schema.tables WHERE table_schema = 'public'`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var n string
		_ = rows.Scan(&n)
		tables = append(tables, n)
	}
	rows.Close()
	for _, tb := range tables {
		var dump string
		q := fmt.Sprintf(`SELECT coalesce(string_agg(row_to_json(t)::text, ' '), '') FROM %q t`, tb)
		if err := e.pool.QueryRow(context.Background(), q).Scan(&dump); err != nil {
			t.Fatal(err)
		}
		out.WriteString(dump)
	}
	all := out.String()
	if strings.Contains(all, value) || strings.Contains(all, hex.EncodeToString([]byte(value))) {
		t.Fatal("secret value found in clear")
	}
}

// the log scrub needs every value the device may hold, named as the device
// names them: a row its current descriptor stopped declaring too
func TestScrubSecretsOpensEveryStoredValue(t *testing.T) {
	e := newEnv(t)
	leds := desc("leds")
	leds.Secrets = []*cadencev2.SecretSpec{{Name: "token"}}
	e.sendReport(t, report(secretDesc(), leds))
	e.putSecret(t, "apiKey", "audio-key-value")
	e.putSecret(t, "other", "audio-other-value")
	if code, _ := call(t, e.app(), http.MethodPut, "/api/v1/devices/"+e.id+"/capabilities/leds/secrets/token", `{"value":"leds-token-value"}`); code != 204 {
		t.Fatalf("leds secret %d", code)
	}
	e.sendReport(t, report(desc("audio"), leds))
	got, err := e.svc.ScrubSecrets(context.Background(), e.id)
	want := map[string]string{"audio.apiKey": "audio-key-value", "audio.other": "audio-other-value", "leds.token": "leds-token-value"}
	if err != nil || !maps.Equal(got, want) {
		t.Fatalf("%d secrets, err %v", len(got), err)
	}
	if got, err := e.svc.ScrubSecrets(context.Background(), "00000000-0000-0000-0000-000000000001"); err != nil || len(got) != 0 {
		t.Fatalf("other device: %d %v", len(got), err)
	}
	if _, err := e.svc.ScrubSecrets(context.Background(), "not-a-uuid"); err == nil {
		t.Fatal("bad id accepted")
	}
	// a value that does not open cannot be scrubbed, so the caller must refuse
	if _, err := e.pool.Exec(context.Background(), `UPDATE device_capability_secrets SET name = 'moved' WHERE name = 'token'`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.ScrubSecrets(context.Background(), e.id); err == nil {
		t.Fatal("an unopenable value was skipped")
	}
}
