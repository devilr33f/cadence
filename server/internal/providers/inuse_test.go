package providers_test

import (
	"bytes"
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5/pgxpool"

	"git.shino.boo/devilreef/cadence/server/internal/api"
	"git.shino.boo/devilreef/cadence/server/internal/api/apitest"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/secrets"
	"git.shino.boo/devilreef/cadence/server/internal/settings"
	"git.shino.boo/devilreef/cadence/server/internal/store/db"
	"git.shino.boo/devilreef/cadence/server/internal/storetest"
	"git.shino.boo/devilreef/cadence/server/internal/subscriptions"
)

type inUseRig struct {
	t    *testing.T
	pool *pgxpool.Pool
	ins  *providers.Instances
	do   func(method, path string) (int, string)
	dev  string
}

func newInUseRig(t *testing.T) *inUseRig {
	t.Helper()
	pool := storetest.Pool(t)
	box, err := secrets.New(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	ins := providers.NewInstances(pool, box, providers.NewRegistry(true))
	app := api.NewApp(api.Options{
		DB: noPing{}, Version: "t", VerifyToken: apitest.Verify,
		Admin: []func(huma.API){func(a huma.API) { providers.RegisterAdmin(a, ins, nil) }},
	})
	d, err := db.New(pool).CreateDevice(context.Background(), db.CreateDeviceParams{Name: "d", Model: "sim"})
	if err != nil {
		t.Fatal(err)
	}
	r := &inUseRig{t: t, pool: pool, ins: ins, dev: d.ID.String()}
	r.do = func(method, path string) (int, string) {
		req := httptest.NewRequest(method, "/api/v1"+path, nil)
		req.Header.Set("Authorization", "Bearer x")
		resp, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		var b bytes.Buffer
		_, _ = b.ReadFrom(resp.Body)
		return resp.StatusCode, strings.TrimSpace(b.String())
	}
	return r
}

func (r *inUseRig) create(name string) {
	r.t.Helper()
	if _, err := r.ins.Create(context.Background(), name, "fake-stt", nil, nil); err != nil {
		r.t.Fatal(err)
	}
}

// setScope stores a scope's settings row as it is, "" for none
func (r *inUseRig) setScope(scope, data string) {
	r.t.Helper()
	var err error
	if data == "" {
		_, err = r.pool.Exec(context.Background(), `DELETE FROM settings WHERE scope = $1`, scope)
	} else {
		_, err = r.pool.Exec(context.Background(), `INSERT INTO settings (scope, data) VALUES ($1, $2)
			ON CONFLICT (scope) DO UPDATE SET data = EXCLUDED.data`, scope, data)
	}
	if err != nil {
		r.t.Fatal(err)
	}
}

func (r *inUseRig) exists(name string) bool {
	r.t.Helper()
	_, err := r.ins.Ref(context.Background(), name)
	if err != nil && !errors.Is(err, providers.ErrNotFound) {
		r.t.Fatal(err)
	}
	return err == nil
}

// an instance a setting names stays: any key that names one, in the global
// set or a device's own; once no setting names it, it goes
func TestDeleteProviderInUse(t *testing.T) {
	r := newInUseRig(t)
	r.create("main")
	device := map[string]bool{"stt": true, "tts": true, "llm": true, "embed": true, "weather": true, "search": true}
	for _, key := range settings.InstanceKeys {
		value := `"main"`
		if key == "mcp" {
			value = `["other","main"]`
		}
		scopes := []string{"global"}
		if device[key] {
			scopes = append(scopes, r.dev)
		}
		for _, scope := range scopes {
			r.setScope(scope, `{"language":"ru","`+key+`":`+value+`}`)
			if code, b := r.do("DELETE", "/providers/main"); code != 409 || b != `{"error":"in_use"}` {
				t.Fatalf("%s in %s: %d %s", key, scope, code, b)
			}
			if !r.exists("main") {
				t.Fatalf("%s in %s: deleted", key, scope)
			}
			r.setScope(scope, "")
		}
	}
	// what names another instance, or names it in no instance key, keeps nothing
	r.setScope("global", `{"stt":"other","voice":"main","model":"main","maintenanceModel":"main","mcp":["other"]}`)
	r.setScope(r.dev, `{"embed":"","persona":"main"}`)
	if code, b := r.do("DELETE", "/providers/main"); code != 204 || b != "" {
		t.Fatalf("free: %d %s", code, b)
	}
	if r.exists("main") {
		t.Fatal("not deleted")
	}
}

// a delete beside an open settings write that names the instance waits for
// it, then is refused: the write and the delete never both land
func TestDeleteProviderWaitsForASettingsWrite(t *testing.T) {
	r := newInUseRig(t)
	r.create("main")
	ctx := context.Background()
	// a write as settings makes one, open: under the lock, the row stored
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := db.New(tx)
	if err := settings.Lock(ctx, q); err != nil {
		t.Fatal(err)
	}
	if err := q.PutSettings(ctx, db.PutSettingsParams{Scope: "global", Data: []byte(`{"llm":"main"}`)}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- r.ins.Delete(ctx, "main") }()
	select {
	case err := <-done:
		t.Fatalf("deleted beside an open settings write: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, providers.ErrInUse) {
			t.Fatalf("after the write: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("delete still waiting")
	}
	if !r.exists("main") {
		t.Fatal("deleted")
	}
}

// a store failure answers 500 and logs its event with the instance alone, as
// the other admin groups do; a broken module schema logs the module
func TestProviderFailuresAreLogged(t *testing.T) {
	pool := storetest.Pool(t)
	box, err := secrets.New(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	broken := providers.Module{ID: "broken-stt", Kind: providers.KindSTT, ConfigSchema: `{`}
	r := newAdminRig(t, providers.NewInstances(pool, box, providers.NewRegistry(false, broken)), db.New(pool))
	if _, err := pool.Exec(context.Background(), `ALTER TABLE provider_instances RENAME TO gone`); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ method, path, line string }{
		{"GET", "/providers/modules", `"msg":"provider_catalog_failed","module":"broken-stt"`},
		{"POST", "/providers/main/test", `"msg":"provider_test_failed","instance":"main"`},
		{"DELETE", "/providers/main", `"msg":"provider_delete_failed","instance":"main"`},
		{"DELETE", "/providers/bad%20name", `"msg":"provider_delete_failed"}`},
	} {
		if code, b, _ := r.call(c.method, c.path); code != 500 || b != `{"error":"internal"}` {
			t.Fatalf("%s %s: %d %s", c.method, c.path, code, b)
		}
		if logs := r.logs.String(); !strings.Contains(logs, c.line) || strings.Contains(logs, "gone") {
			t.Fatalf("%s %s: logs %s", c.method, c.path, logs)
		}
	}
}

// an instance a subscription's topic names stays too, until it goes
func TestDeleteProviderSubscribed(t *testing.T) {
	r := newInUseRig(t)
	r.create("main")
	subs := subscriptions.NewStore(r.pool)
	s, err := subs.Create(context.Background(), subscriptions.Subscription{
		Topic: "fake-stt:main:*", Events: []string{"stopped"}, Delivery: "speaker", Device: r.dev,
		Until: "unsubscribed", CreatedBy: "api",
	})
	if err != nil {
		t.Fatal(err)
	}
	if code, b := r.do("DELETE", "/providers/main"); code != 409 || b != `{"error":"in_use"}` {
		t.Fatalf("subscribed: %d %s", code, b)
	}
	if err := subs.Delete(context.Background(), s.ID, ""); err != nil {
		t.Fatal(err)
	}
	if code, b := r.do("DELETE", "/providers/main"); code != 204 || b != "" {
		t.Fatalf("free: %d %s", code, b)
	}
	// a subscription names an instance that exists, of its module
	if _, err := subs.Create(context.Background(), subscriptions.Subscription{
		Topic: "fake-stt:main:*", Events: []string{"stopped"}, Delivery: "speaker", Device: r.dev,
		Until: "unsubscribed", CreatedBy: "api",
	}); !errors.Is(err, subscriptions.ErrInstance) {
		t.Fatalf("deleted instance: %v", err)
	}
}
