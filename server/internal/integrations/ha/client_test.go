package ha_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/integrations/ha"
	"git.shino.boo/devilreef/cadence/server/internal/integrations/ha/hatest"
)

func TestAuthenticatesAndAnswersCommands(t *testing.T) {
	r := newRig(t)
	r.fake.SetResult("get_states", []map[string]any{{"entity_id": "light.kitchen", "state": "on"}})
	if r.c.Connected() {
		t.Fatal("connected before Run")
	}
	before := time.Now()
	r.run()
	r.waitConnected()
	st := r.c.Status()
	if !st.Connected || st.ConnectedAt.Before(before) || st.LastError != "" {
		t.Fatalf("status %+v", st)
	}
	got, err := r.c.Command(bg(), "get_states", nil)
	if err != nil || !strings.Contains(string(got), `"light.kitchen"`) {
		t.Fatalf("get_states %s %v", got, err)
	}
	if r.fake.Auths() != 1 || r.logs.count("ha_connected") != 1 {
		t.Fatalf("auths %d logs %s", r.fake.Auths(), r.logs)
	}
}

func TestInvalidTokenStopsAfterOneAttempt(t *testing.T) {
	r := newRig(t, withToken("not-the-token"))
	r.run()
	select {
	case <-r.done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run kept going after auth_invalid")
	}
	time.Sleep(5 * fast.BackoffMax)
	if n := r.fake.Auths(); n != 1 {
		t.Fatalf("%d auth attempts, home assistant bans an ip that retries", n)
	}
	if st := r.c.Status(); st.Connected || st.LastError != ha.CodeAuthFailed {
		t.Fatalf("status %+v", st)
	}
	if r.logs.count("ha_auth_failed") != 1 || r.logs.count("ha_reconnect") != 0 {
		t.Fatalf("logs %s", r.logs)
	}
	_, err := r.c.Command(bg(), "get_states", nil)
	wantCode(t, err, ha.CodeUnavailable)
}

func TestResultsMatchTheirCommandWhileEventsInterleave(t *testing.T) {
	r := newRig(t)
	var mu sync.Mutex
	var events []string
	r.c.Subscribe("state_changed", func(data json.RawMessage) {
		mu.Lock()
		events = append(events, string(data))
		mu.Unlock()
	})
	r.run()
	r.waitConnected()
	r.fake.SetResult("first", "one")
	r.fake.SetResult("second", map[string]int{"n": 2})
	r.fake.Hold("first")
	r.fake.Hold("second")
	type answer struct {
		raw string
		err error
	}
	answers := map[string]chan answer{"first": make(chan answer, 1), "second": make(chan answer, 1)}
	for typ, ch := range answers {
		go func() {
			raw, err := r.c.Command(bg(), typ, nil)
			ch <- answer{string(raw), err}
		}()
	}
	eventually(t, "both commands held", func() bool { return r.fake.Held() == 2 })
	r.fake.Emit("state_changed", map[string]int{"seq": 1})
	r.fake.Emit("other_event", map[string]int{"seq": 99})
	r.fake.Emit("state_changed", map[string]int{"seq": 2})
	r.fake.Release()
	r.fake.Emit("state_changed", map[string]int{"seq": 3})
	if a := <-answers["first"]; a.err != nil || a.raw != `"one"` {
		t.Fatalf("first %+v", a)
	}
	if a := <-answers["second"]; a.err != nil || a.raw != `{"n":2}` {
		t.Fatalf("second %+v", a)
	}
	eventually(t, "three events", func() bool { mu.Lock(); defer mu.Unlock(); return len(events) == 3 })
	if want := []string{`{"seq":1}`, `{"seq":2}`, `{"seq":3}`}; !slices.Equal(events, want) {
		t.Fatalf("events %v", events)
	}
}

func TestConcurrentCommandsKeepIDsIncreasing(t *testing.T) {
	r := newRig(t)
	r.fake.SetResult("get_config", map[string]string{"version": "x"})
	r.run()
	r.waitConnected()
	var wg sync.WaitGroup
	var failed atomic.Int32
	for range 40 {
		wg.Go(func() {
			if _, err := r.c.Command(bg(), "get_config", nil); err != nil {
				failed.Add(1)
			}
		})
	}
	wg.Wait()
	// the fake answers a non-increasing id with id_reuse, as home assistant does
	if n := failed.Load(); n != 0 {
		t.Fatalf("%d commands failed", n)
	}
}

func TestHomeAssistantErrorsBecomeCodes(t *testing.T) {
	r := newRig(t)
	r.run()
	r.waitConnected()
	for typ, want := range map[string]string{
		"not_found":                 "ha_error:not_found",
		"service_validation_error":  "ha_error:service_validation_error",
		"Not A Code":                "ha_error:unknown",
		strings.Repeat("a", 41):     "ha_error:unknown",
		"code\nwith\ncontrol chars": "ha_error:unknown",
	} {
		r.fake.Fail("cmd", typ)
		_, err := r.c.Command(bg(), "cmd", nil)
		wantCode(t, err, want)
		if !errors.Is(err, ha.ErrHA) || errors.Is(err, ha.ErrUnavailable) {
			t.Fatalf("%q: errors.Is mismatch for %v", typ, err)
		}
	}
	_, err := r.c.Command(bg(), "never_scripted", nil)
	wantCode(t, err, "ha_error:unknown_command")
	if !r.c.Connected() {
		t.Fatal("an error result closed the connection")
	}
}

func TestCommandTimeout(t *testing.T) {
	r := newRig(t, withTimings(tuned(func(t *ha.Timings) { t.Command = 300 * time.Millisecond })))
	r.run()
	r.waitConnected()
	r.fake.Hold("slow")
	start := time.Now()
	_, err := r.c.Command(bg(), "slow", nil)
	wantCode(t, err, ha.CodeTimeout)
	if !errors.Is(err, ha.ErrTimeout) || time.Since(start) < 300*time.Millisecond {
		t.Fatalf("%v after %v", err, time.Since(start))
	}
	// a caller's shorter deadline wins and also reads as a timeout
	ctx, cancel := context.WithTimeout(bg(), 10*time.Millisecond)
	defer cancel()
	start = time.Now()
	_, err = r.c.Command(ctx, "slow", nil)
	wantCode(t, err, ha.CodeTimeout)
	if time.Since(start) > 200*time.Millisecond {
		t.Fatalf("waited %v past a 10 ms deadline", time.Since(start))
	}
	if !r.c.Connected() || r.fake.Upgrades() != 1 {
		t.Fatal("a timed out command closed the connection")
	}
}

func TestCancelledCallSendsNothing(t *testing.T) {
	r := newRig(t)
	r.run()
	r.waitConnected()
	ctx, cancel := context.WithCancel(bg())
	cancel()
	err := r.c.Call(ctx, "light", "turn_off", nil, "light.kitchen")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled call: %v", err)
	}
	if err := r.c.Call(bg(), "light", "turn_on", nil, "light.kitchen"); err != nil {
		t.Fatal(err)
	}
	if calls := r.fake.Calls(); len(calls) != 1 || calls[0].Service != "turn_on" {
		t.Fatalf("calls %+v", calls)
	}
	if !r.c.Connected() || r.fake.Upgrades() != 1 {
		t.Fatal("a cancelled call closed the connection")
	}
}

func TestCallSendsServiceDataAndTarget(t *testing.T) {
	r := newRig(t)
	r.run()
	r.waitConnected()
	if err := r.c.Call(bg(), "light", "turn_on", map[string]any{"brightness_pct": 40}, "light.kitchen"); err != nil {
		t.Fatal(err)
	}
	if err := r.c.Call(bg(), "homeassistant", "reload_all", nil, ""); err != nil {
		t.Fatal(err)
	}
	calls := r.fake.Calls()
	if len(calls) != 2 {
		t.Fatalf("calls %+v", calls)
	}
	c := calls[0]
	if c.Domain != "light" || c.Service != "turn_on" || c.ServiceData["brightness_pct"] != 40.0 ||
		c.Target["entity_id"] != "light.kitchen" || len(c.Target) != 1 || c.ReturnResponse {
		t.Fatalf("call %+v", c)
	}
	if c := calls[1]; c.ServiceData != nil || c.Target != nil {
		t.Fatalf("bare call %+v", c)
	}
	r.fake.Fail("call_service", "service_validation_error")
	wantCode(t, r.c.Call(bg(), "light", "turn_on", nil, "light.kitchen"), "ha_error:service_validation_error")
}

func TestDropFailsPendingThenReconnectsAndResubscribes(t *testing.T) {
	r := newRig(t)
	var events atomic.Int32
	r.c.Subscribe("state_changed", func(json.RawMessage) { events.Add(1) })
	var connects atomic.Int32
	r.c.OnConnect(func(context.Context) error { connects.Add(1); return nil })
	r.run()
	r.waitConnected()
	r.fake.Hold("slow")
	pending := make(chan error, 1)
	go func() {
		_, err := r.c.Command(bg(), "slow", nil)
		pending <- err
	}()
	eventually(t, "command held", func() bool { return r.fake.Held() == 1 })
	r.fake.Drop()
	select {
	case err := <-pending:
		wantCode(t, err, ha.CodeUnavailable)
	case <-time.After(time.Second):
		t.Fatal("the pending command outlived its connection")
	}
	r.fake.Release()
	eventually(t, "reconnected", func() bool { return r.fake.Upgrades() == 2 && r.c.Connected() })
	if connects.Load() != 2 || r.fake.Subscriptions("state_changed") != 1 {
		t.Fatalf("connects %d subscriptions %d", connects.Load(), r.fake.Subscriptions("state_changed"))
	}
	r.fake.Emit("state_changed", map[string]any{})
	eventually(t, "event on the new connection", func() bool { return events.Load() == 1 })
	if r.logs.count("ha_disconnected") != 1 || !strings.Contains(r.logs.String(), "msg=ha_disconnected code=unavailable") ||
		len(r.logs.delays()) != 1 {
		t.Fatalf("logs %s", r.logs)
	}
}

func TestReconnectBacksOffAndResetsAfterAStableConnection(t *testing.T) {
	r := newRig(t)
	r.run()
	r.waitConnected()
	r.fake.Restart(400 * time.Millisecond)
	eventually(t, "back after the restart", func() bool { return r.fake.Upgrades() == 2 && r.c.Connected() })
	d := r.logs.delays()
	if len(d) < 4 || !slices.Equal(d[:4], []int{10, 20, 40, 40}) {
		t.Fatalf("delays %v: want 10 ms doubling to the 40 ms cap", d)
	}
	time.Sleep(2 * fast.StableAfter)
	r.fake.Drop()
	eventually(t, "back after the drop", func() bool { return r.fake.Upgrades() == 3 && r.c.Connected() })
	if d := r.logs.delays(); d[len(d)-1] != 10 {
		t.Fatalf("delays %v: a stable connection resets the backoff", d)
	}
	if st := r.c.Status(); st.LastError != ha.CodeUnavailable {
		t.Fatalf("status %+v", st)
	}
}

func TestOnConnectRunsAfterResubscribingAndItsErrorReconnects(t *testing.T) {
	r := newRig(t)
	r.fake.SetResult("get_states", []any{})
	r.c.Subscribe("state_changed", func(json.RawMessage) {})
	var calls atomic.Int32
	var seen []string
	r.c.OnConnect(func(ctx context.Context) error {
		n := calls.Add(1)
		_, err := r.c.Command(ctx, "get_states", nil)
		seen = append(seen, strings.Join([]string{
			strconv.FormatBool(r.fake.Subscriptions("state_changed") == 1), strconv.FormatBool(r.c.Connected()),
			strconv.FormatBool(err == nil),
		}, ","))
		if n == 1 {
			return errors.New("reload failed")
		}
		return nil
	})
	r.run()
	// the connected line is written a moment after the client is
	eventually(t, "connected on the second try", func() bool {
		return r.fake.Upgrades() == 2 && r.c.Connected() && r.logs.count("ha_connected") >= 1
	})
	if want := []string{"true,false,true", "true,false,true"}; !slices.Equal(seen, want) {
		t.Fatalf("subscribed,connected,command per hook run: %v", seen)
	}
	if st := r.c.Status(); st.LastError != ha.CodeBadResponse {
		t.Fatalf("status %+v", st)
	}
	if r.logs.count("ha_connected") != 1 || !strings.Contains(r.logs.String(), "code=bad_response") {
		t.Fatalf("logs %s", r.logs)
	}
}

func TestRedirectedUpgradeRefused(t *testing.T) {
	other := hatest.New(token)
	defer other.Close()
	r := newRig(t)
	r.fake.Redirect(other.URL() + "/api/websocket")
	r.run()
	eventually(t, "a failed attempt", func() bool { return len(r.logs.delays()) >= 2 })
	if other.Upgrades() != 0 || other.Auths() != 0 || r.c.Connected() {
		t.Fatal("the redirect was followed")
	}
	if st := r.c.Status(); st.LastError != ha.CodeUnavailable {
		t.Fatalf("status %+v", st)
	}
}

func TestUntrustedCertificateRefused(t *testing.T) {
	r := newRig(t, noRoots())
	r.run()
	eventually(t, "a failed attempt", func() bool { return len(r.logs.delays()) >= 2 })
	if r.fake.Upgrades() != 0 || r.fake.Auths() != 0 || r.c.Connected() {
		t.Fatal("reached a server with an untrusted certificate")
	}
	if st := r.c.Status(); st.LastError != ha.CodeUnavailable {
		t.Fatalf("status %+v", st)
	}
}

func TestMissedPongClosesTheConnection(t *testing.T) {
	r := newRig(t, withTimings(tuned(func(t *ha.Timings) {
		t.PingEvery, t.PongWait = 15*time.Millisecond, 100*time.Millisecond
	})))
	r.run()
	r.waitConnected()
	time.Sleep(150 * time.Millisecond)
	if r.fake.Upgrades() != 1 || !r.c.Connected() {
		t.Fatal("answered pings closed the connection")
	}
	r.fake.Hold("ping")
	eventually(t, "closed for a missed pong", func() bool { return r.c.Status().LastError == ha.CodeTimeout })
	if !strings.Contains(r.logs.String(), "msg=ha_disconnected code=timeout") {
		t.Fatalf("logs %s", r.logs)
	}
	r.fake.Release()
	eventually(t, "reconnected", func() bool { return r.fake.Upgrades() >= 2 && r.c.Connected() })
}

func TestMessageOverTheReadLimitClosesTheConnection(t *testing.T) {
	r := newRig(t, withTimings(tuned(func(t *ha.Timings) { t.ReadLimit = 4096 })))
	r.fake.SetResult("big", strings.Repeat("x", 8192))
	r.fake.SetResult("small", "ok")
	r.run()
	r.waitConnected()
	_, err := r.c.Command(bg(), "big", nil)
	wantCode(t, err, ha.CodeUnavailable)
	eventually(t, "closed as a bad response", func() bool { return r.c.Status().LastError == ha.CodeBadResponse })
	eventually(t, "reconnected", func() bool { return r.fake.Upgrades() == 2 && r.c.Connected() })
	if raw, err := r.c.Command(bg(), "small", nil); err != nil || string(raw) != `"ok"` {
		t.Fatalf("small %s %v", raw, err)
	}
}

func TestPathPrefixKept(t *testing.T) {
	r := newRig(t, withBase("/ha//"))
	r.run()
	r.waitConnected()
	if p := r.fake.LastPath(); p != "/ha/api/websocket" {
		t.Fatalf("path %q", p)
	}
}

func TestNoCommandWithoutAConnection(t *testing.T) {
	r := newRig(t)
	_, err := r.c.Command(bg(), "get_states", nil)
	wantCode(t, err, ha.CodeUnavailable)
	if !errors.Is(err, ha.ErrUnavailable) {
		t.Fatalf("%v", err)
	}
}

func TestEachSubscriptionSeesOnlyItsOwnEvents(t *testing.T) {
	r := newRig(t)
	var mu sync.Mutex
	got := map[string][]string{}
	for _, typ := range []string{"area_registry_updated", "device_registry_updated"} {
		r.c.Subscribe(typ, func(data json.RawMessage) {
			mu.Lock()
			got[typ] = append(got[typ], string(data))
			mu.Unlock()
		})
	}
	r.run()
	r.waitConnected()
	r.fake.Emit("area_registry_updated", map[string]string{"area_id": "a"})
	r.fake.Emit("device_registry_updated", map[string]string{"device_id": "d"})
	r.fake.Emit("area_registry_updated", map[string]string{"area_id": "b"})
	eventually(t, "three events", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(got["area_registry_updated"])+len(got["device_registry_updated"]) == 3
	})
	if a, d := got["area_registry_updated"], got["device_registry_updated"]; !slices.Equal(a, []string{`{"area_id":"a"}`, `{"area_id":"b"}`}) ||
		!slices.Equal(d, []string{`{"device_id":"d"}`}) {
		t.Fatalf("routed %v", got)
	}
}

func TestShortConnectionsKeepTheBackoffGrowing(t *testing.T) {
	r := newRig(t, withTimings(tuned(func(t *ha.Timings) { t.StableAfter = 5 * time.Second })))
	r.run()
	r.waitConnected()
	r.fake.Drop()
	eventually(t, "back after the first drop", func() bool { return r.fake.Upgrades() == 2 && r.c.Connected() })
	r.fake.Drop()
	eventually(t, "back after the second drop", func() bool { return r.fake.Upgrades() == 3 && r.c.Connected() })
	if d := r.logs.delays(); !slices.Equal(d, []int{10, 20}) {
		t.Fatalf("delays %v: a connection shorter than StableAfter keeps doubling", d)
	}
}

func TestUnencodableFieldsAreABadResponse(t *testing.T) {
	r := newRig(t)
	r.run()
	r.waitConnected()
	_, err := r.c.Command(bg(), "get_states", map[string]any{"bad": make(chan int)})
	wantCode(t, err, ha.CodeBadResponse)
	if r.fake.Received("get_states") != 0 || !r.c.Connected() {
		t.Fatal("an unencodable command was sent or closed the connection")
	}
}

func TestOnDisconnectRunsOnceAfterEachConnection(t *testing.T) {
	r := newRig(t)
	var mu sync.Mutex
	var order []string
	r.c.Subscribe("state_changed", func(json.RawMessage) { mu.Lock(); order = append(order, "event"); mu.Unlock() })
	r.c.OnDisconnect(func() { mu.Lock(); order = append(order, "down"); mu.Unlock() })
	r.run()
	r.waitConnected()
	r.fake.Emit("state_changed", map[string]any{})
	eventually(t, "the event", func() bool { mu.Lock(); defer mu.Unlock(); return len(order) == 1 })
	r.fake.Drop()
	eventually(t, "reconnected", func() bool { return r.fake.Upgrades() == 2 && r.c.Connected() })
	mu.Lock()
	if !slices.Equal(order, []string{"event", "down"}) {
		t.Fatalf("order %v", order)
	}
	mu.Unlock()
	r.stop()
	if !slices.Equal(order, []string{"event", "down", "down"}) {
		t.Fatalf("after stop %v", order)
	}
}
