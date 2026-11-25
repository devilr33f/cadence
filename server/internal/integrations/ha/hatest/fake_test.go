package hatest_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"git.shino.boo/devilreef/cadence/server/internal/integrations/ha/hatest"
)

type peer struct {
	t  *testing.T
	ws *websocket.Conn
}

func dial(t *testing.T, f *hatest.Server, token string) (*peer, map[string]any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	ws, _, err := websocket.Dial(ctx, strings.Replace(f.URL(), "https", "wss", 1)+"/api/websocket", &websocket.DialOptions{HTTPClient: f.Client()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ws.CloseNow() })
	p := &peer{t: t, ws: ws}
	if m := p.read(); m["type"] != "auth_required" {
		t.Fatalf("first %v", m)
	}
	p.send(map[string]any{"type": "auth", "access_token": token})
	return p, p.read()
}

func (p *peer) send(v any) {
	b, _ := json.Marshal(v)
	if err := p.ws.Write(context.Background(), websocket.MessageText, b); err != nil {
		p.t.Fatal(err)
	}
}

func (p *peer) read() map[string]any {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, b, err := p.ws.Read(ctx)
	if err != nil {
		p.t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		p.t.Fatal(err)
	}
	return m
}

func TestFakeSpeaksTheProtocol(t *testing.T) {
	f := hatest.New("right")
	defer f.Close()
	if _, m := dial(t, f, "wrong"); m["type"] != "auth_invalid" {
		t.Fatalf("wrong token: %v", m)
	}
	p, m := dial(t, f, "right")
	if m["type"] != "auth_ok" {
		t.Fatalf("right token: %v", m)
	}
	f.SetResult("get_states", []map[string]any{{"entity_id": "light.a", "state": "on"}})
	p.send(map[string]any{"id": 1, "type": "get_states"})
	if m := p.read(); m["id"] != 1.0 || m["success"] != true || len(m["result"].([]any)) != 1 {
		t.Fatalf("get_states %v", m)
	}
	p.send(map[string]any{"id": 2, "type": "subscribe_events", "event_type": "state_changed"})
	if m := p.read(); m["success"] != true {
		t.Fatalf("subscribe %v", m)
	}
	f.Emit("state_changed", map[string]any{"entity_id": "light.a"})
	if m := p.read(); m["id"] != 2.0 || m["type"] != "event" {
		t.Fatalf("event %v", m)
	}
	p.send(map[string]any{"id": 3, "type": "call_service", "domain": "light", "service": "turn_off", "target": map[string]any{"entity_id": "light.a"}})
	if m := p.read(); m["success"] != true {
		t.Fatalf("call %v", m)
	}
	if c := f.Calls(); len(c) != 1 || c[0].Service != "turn_off" || c[0].Target["entity_id"] != "light.a" {
		t.Fatalf("calls %+v", c)
	}
	p.send(map[string]any{"id": 3, "type": "get_states"})
	if m := p.read(); m["success"] != false || m["error"].(map[string]any)["code"] != "id_reuse" {
		t.Fatalf("reused id %v", m)
	}
	if f.Auths() != 2 || f.Upgrades() != 2 || f.Conns() != 1 || f.Subscriptions("state_changed") != 1 {
		t.Fatalf("auths %d upgrades %d conns %d", f.Auths(), f.Upgrades(), f.Conns())
	}
	if f.Received("get_states") != 1 || f.Received("call_service") != 1 {
		t.Fatalf("received get_states %d call_service %d", f.Received("get_states"), f.Received("call_service"))
	}
}

func TestFakeRecordsAHeldCallOnReceipt(t *testing.T) {
	f := hatest.New("right")
	defer f.Close()
	p, _ := dial(t, f, "right")
	f.Hold("call_service")
	p.send(map[string]any{"id": 1, "type": "call_service", "domain": "light", "service": "turn_on"})
	deadline := time.Now().Add(5 * time.Second)
	for f.Held() != 1 {
		if time.Now().After(deadline) {
			t.Fatal("never held")
		}
		time.Sleep(2 * time.Millisecond)
	}
	if c := f.Calls(); len(c) != 1 || c[0].Service != "turn_on" {
		t.Fatalf("a held call is not recorded: %+v", c)
	}
	f.Release()
	if m := p.read(); m["success"] != true {
		t.Fatalf("released call %v", m)
	}
	if len(f.Calls()) != 1 {
		t.Fatalf("recorded twice: %+v", f.Calls())
	}
}

func TestFakeAnswersRegistryEntriesForTheAskedIDs(t *testing.T) {
	f := hatest.New("right")
	defer f.Close()
	p, _ := dial(t, f, "right")
	f.SetResult("config/entity_registry/get_entries", map[string]any{
		"light.a": map[string]any{"entity_id": "light.a", "aliases": []string{"lamp"}},
		"light.b": map[string]any{"entity_id": "light.b", "aliases": []string{}},
	})
	p.send(map[string]any{"id": 1, "type": "config/entity_registry/get_entries", "entity_ids": []string{"light.a", "light.zz"}})
	m := p.read()
	res, _ := m["result"].(map[string]any)
	if len(res) != 2 || res["light.a"] == nil || res["light.zz"] != nil {
		t.Fatalf("get_entries %v", m)
	}
	if _, ok := res["light.zz"]; !ok {
		t.Fatalf("an unknown id is not answered with null: %v", m)
	}
}

func TestFakeTellsEachCallItAnswered(t *testing.T) {
	f := hatest.New("right")
	defer f.Close()
	told := make(chan hatest.Call, 4)
	f.OnCall(func(c hatest.Call) { told <- c })
	p, _ := dial(t, f, "right")
	f.Fail("call_service", "not_found")
	p.send(map[string]any{"id": 1, "type": "call_service", "domain": "lock", "service": "unlock"})
	if m := p.read(); m["success"] != false {
		t.Fatalf("failed call %v", m)
	}
	f.Fail("call_service", "")
	p.send(map[string]any{"id": 2, "type": "call_service", "domain": "light", "service": "turn_off", "target": map[string]any{"entity_id": "light.a"}})
	if m := p.read(); m["success"] != true {
		t.Fatalf("call %v", m)
	}
	select {
	case c := <-told:
		if c.Domain != "light" || c.Service != "turn_off" || c.Target["entity_id"] != "light.a" {
			t.Fatalf("told %+v, want the answered call alone", c)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the answered call was never told")
	}
	if len(told) != 0 {
		t.Fatalf("%d more calls told", len(told))
	}
}

// a handler the caller serves itself: the e2e's fake home assistant listens
// on its own address with its own certificate
func TestFakeServesFromTheCallersServer(t *testing.T) {
	f := hatest.NewHandler("right")
	defer f.Close()
	srv := httptest.NewTLSServer(f)
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, strings.Replace(srv.URL, "https", "wss", 1)+"/api/websocket", &websocket.DialOptions{HTTPClient: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	p := &peer{t: t, ws: ws}
	defer func() { _ = ws.CloseNow() }()
	if m := p.read(); m["type"] != "auth_required" {
		t.Fatalf("first %v", m)
	}
	p.send(map[string]any{"type": "auth", "access_token": "right"})
	if m := p.read(); m["type"] != "auth_ok" {
		t.Fatalf("auth %v", m)
	}
}
