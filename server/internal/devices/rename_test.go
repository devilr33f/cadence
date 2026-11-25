package devices_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/devices"
	"git.shino.boo/devilreef/cadence/server/internal/store/db"
)

func (e *env) name(t *testing.T) string {
	t.Helper()
	return e.detail(t).Name
}

func TestRenameDevice(t *testing.T) {
	e := newEnv(t)
	for _, name := range []string{"Hall", "кухня"} {
		if _, err := e.q.CreateDevice(context.Background(), db.CreateDeviceParams{Name: name, Model: "sim"}); err != nil {
			t.Fatal(err)
		}
	}
	app := e.app()
	path := "/api/v1/devices/" + e.id
	code, body := call(t, app, http.MethodPatch, path, `{"name":"  Living room  "}`)
	var got struct{ ID, Name string }
	if err := json.Unmarshal([]byte(body), &got); err != nil || code != 200 || got.ID != e.id || got.Name != "Living room" {
		t.Fatalf("%d %s", code, body)
	}
	if n := e.name(t); n != "Living room" {
		t.Fatalf("stored %q", n)
	}
	// its own name, as it is or in another case, is no clash, and the path id may be spelled otherwise
	for _, name := range []string{"Living room", "LIVING ROOM"} {
		if code, body := call(t, app, http.MethodPatch, "/api/v1/devices/"+strings.ToUpper(e.id), `{"name":"`+name+`"}`); code != 200 {
			t.Fatalf("own name %q: %d %s", name, code, body)
		}
	}
	// 64 once trimmed, longer as sent
	padded := strings.Repeat("ж", 64)
	if code, body := call(t, app, http.MethodPatch, path, `{"name":"  `+padded+` "}`); code != 200 || e.name(t) != padded {
		t.Fatalf("padded 64 characters %d %s", code, body)
	}
	long := strings.Repeat("ё", 64)
	if code, body := call(t, app, http.MethodPatch, path, `{"name":"`+long+`"}`); code != 200 || e.name(t) != long {
		t.Fatalf("64 characters %d %s", code, body)
	}
	if n := e.changes.Load(); n != 5 {
		t.Fatalf("%d changes heard, want 5", n)
	}
	const badName = `{"error":"invalid","path":"/body/name"}`
	for _, c := range []struct {
		body string
		code int
		want string
	}{
		{`{"name":"` + long + `ё"}`, 422, badName},
		{`{"name":"   "}`, 422, badName},
		{`{"name":""}`, 422, badName},
		{`{"name":"a\u0000b"}`, 422, badName},
		{`{"name":"a\nb"}`, 422, badName},
		// format characters and line or paragraph separators: blank, reversed or broken names
		{`{"name":"\u200b"}`, 422, badName},
		{`{"name":"hall\u200b"}`, 422, badName},
		{`{"name":"\ufeffhall"}`, 422, badName},
		{`{"name":"a\u202eb"}`, 422, badName},
		{`{"name":"a\u2028b"}`, 422, badName},
		{`{"name":"a\u2029b"}`, 422, badName},
		{`{"name":"x","model":"other"}`, 422, `{"error":"invalid","path":"/body/model"}`},
		{`{}`, 422, `{"error":"invalid","path":"/body"}`},
		// another device's name, in any case and any script
		{`{"name":" hall "}`, 409, `{"error":"name_taken"}`},
		{`{"name":"Кухня"}`, 409, `{"error":"name_taken"}`},
	} {
		if code, body := call(t, app, http.MethodPatch, path, c.body); code != c.code || body != c.want {
			t.Fatalf("%s: %d %s", c.body, code, body)
		}
	}
	if n := e.name(t); n != long || e.changes.Load() != 5 {
		t.Fatalf("a refusal renamed it: %q, %d changes", n, e.changes.Load())
	}
	for _, id := range []string{"00000000-0000-0000-0000-000000000000", "nope"} {
		if code, body := call(t, app, http.MethodPatch, "/api/v1/devices/"+id, `{"name":"Hall"}`); code != 404 || body != `{"error":"not_found"}` {
			t.Fatalf("%s: %d %s", id, code, body)
		}
	}
}

// a rename waits for another one's transaction, so two cannot both take a
// name that was free when they began
func TestRenameWaitsForAnother(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	other, err := e.q.CreateDevice(ctx, db.CreateDeviceParams{Name: "hall", Model: "sim"})
	if err != nil {
		t.Fatal(err)
	}
	// the other rename, holding its transaction open
	tx, err := e.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := e.q.WithTx(tx).RenameDevice(ctx, db.RenameDeviceParams{ID: other.ID, Name: "Den"}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- e.svc.Rename(ctx, e.id, "den") }()
	select {
	case err := <-done:
		t.Fatalf("renamed beside an open rename: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, devices.ErrNameTaken) {
			t.Fatalf("after the other's commit: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("rename still waiting")
	}
	if n := e.name(t); n != "kitchen" {
		t.Fatalf("renamed to %q", n)
	}
}
