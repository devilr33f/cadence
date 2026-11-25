package devices_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"git.shino.boo/devilreef/cadence/pkg/caphash"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/pkg/wire"
	"git.shino.boo/devilreef/cadence/server/internal/devices"
	"git.shino.boo/devilreef/cadence/server/internal/gateway"
	"git.shino.boo/devilreef/cadence/server/internal/secrets"
	"git.shino.boo/devilreef/cadence/server/internal/store/db"
	"git.shino.boo/devilreef/cadence/server/internal/storetest"
)

// fakeHub keys its one link by the canonical id, as the real hub does.
type fakeHub struct {
	mu      sync.Mutex
	dev     string
	online  bool
	sent    []*cadencev2.Envelope
	removed []string
	onSend  func(*cadencev2.Envelope) // runs after the send, outside the lock
}

func (h *fakeHub) Send(id string, env *cadencev2.Envelope) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.online || id != h.dev {
		return gateway.ErrOffline
	}
	h.sent = append(h.sent, env)
	if hook := h.onSend; hook != nil {
		h.mu.Unlock()
		hook(env)
		h.mu.Lock()
	}
	return nil
}

func (h *fakeHub) Online(id string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.online && id == h.dev
}

func (h *fakeHub) setOnline(v bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.online = v
}

func (h *fakeHub) Remove(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.removed = append(h.removed, id)
	if id == h.dev {
		h.online = false
	}
}

func (h *fakeHub) removedIDs() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.removed)
}

func (h *fakeHub) configSets() []*cadencev2.ConfigSet {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []*cadencev2.ConfigSet
	for _, e := range h.sent {
		if c := e.GetConfigSet(); c != nil {
			out = append(out, c)
		}
	}
	return out
}

type env struct {
	svc  *devices.Service
	hub  *fakeHub
	pool *pgxpool.Pool
	q    *db.Queries
	id   string
	logs *bytes.Buffer
	box  *secrets.Box
	// how often the service said a device's name or existence changed
	changes atomic.Int32
}

const levelSchema = `{"type":"object","properties":{"level":{"type":"integer","maximum":100}},"additionalProperties":false}`
const stateSchema = `{"type":"object","properties":{"playing":{"type":"boolean"}},"required":["playing"]}`

func newEnv(t *testing.T) *env {
	t.Helper()
	pool := storetest.Pool(t)
	q := db.New(pool)
	dev, err := q.CreateDevice(context.Background(), db.CreateDeviceParams{Name: "kitchen", Model: "sim"})
	if err != nil {
		t.Fatal(err)
	}
	logs := &bytes.Buffer{}
	hub := &fakeHub{dev: dev.ID.String(), online: true}
	log := slog.New(slog.NewTextHandler(logs, nil))
	box, err := secrets.New(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	e := &env{svc: devices.NewService(pool, hub, box, log), box: box, hub: hub, pool: pool, q: q, id: dev.ID.String(), logs: logs}
	e.svc.OnChange(func() { e.changes.Add(1) })
	return e
}

func report(ds ...*cadencev2.Descriptor) *cadencev2.CapabilityReport {
	r := &cadencev2.CapabilityReport{Descriptors: ds}
	r.Hash = caphash.Report(r)
	return r
}

func desc(id string) *cadencev2.Descriptor {
	return &cadencev2.Descriptor{Id: id, Version: 1, ConfigSchema: levelSchema, StateSchema: stateSchema}
}

func (e *env) sendReport(t *testing.T, r *cadencev2.CapabilityReport) {
	t.Helper()
	env := &cadencev2.Envelope{Body: &cadencev2.Envelope_CapabilityReport{CapabilityReport: r}}
	if !e.svc.Handle(context.Background(), e.id, env) {
		t.Fatal("report not handled")
	}
}

func (e *env) storedHash(t *testing.T) []byte {
	t.Helper()
	var id pgtype.UUID
	if err := id.Scan(e.id); err != nil {
		t.Fatal(err)
	}
	h, err := e.q.GetCapabilitiesHash(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func (e *env) detail(t *testing.T) devices.Detail {
	t.Helper()
	d, err := e.svc.Detail(context.Background(), e.id)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func (e *env) capability(t *testing.T, id string) devices.CapabilityView {
	t.Helper()
	for _, c := range e.detail(t).Capabilities {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("capability %s missing", id)
	return devices.CapabilityView{}
}

func TestFullReportWhenHashUnknown(t *testing.T) {
	e := newEnv(t)
	w := e.svc.Hello(context.Background(), e.id, &cadencev2.Hello{CapabilitiesHash: []byte{1}})
	if !w.SendFullReport {
		t.Fatal("no full report asked")
	}
	if w := e.svc.Hello(context.Background(), e.id, &cadencev2.Hello{}); !w.SendFullReport {
		t.Fatal("empty hash with empty store must still ask")
	}
}

func TestNoReportWhenHashMatches(t *testing.T) {
	e := newEnv(t)
	r := report(desc("audio"))
	e.sendReport(t, r)
	w := e.svc.Hello(context.Background(), e.id, &cadencev2.Hello{CapabilitiesHash: r.Hash})
	if w.SendFullReport {
		t.Fatal("report asked for with a matching hash")
	}
	w = e.svc.Hello(context.Background(), e.id, &cadencev2.Hello{CapabilitiesHash: []byte{9}})
	if !w.SendFullReport {
		t.Fatal("mismatching hash must ask")
	}
}

func TestReportRejectsBadHash(t *testing.T) {
	e := newEnv(t)
	r := report(desc("audio"))
	r.Hash = []byte("nope")
	e.sendReport(t, r)
	if len(e.detail(t).Capabilities) != 0 || len(e.storedHash(t)) != 0 {
		t.Fatal("bad hash stored something")
	}
	if !strings.Contains(e.logs.String(), "capability_report_invalid") || !strings.Contains(e.logs.String(), "reason=hash") {
		t.Fatalf("log: %s", e.logs)
	}
}

func TestReportRejectsBadSchema(t *testing.T) {
	e := newEnv(t)
	for _, edit := range []func(*cadencev2.Descriptor){
		func(d *cadencev2.Descriptor) { d.ConfigSchema = `{"type":"nonsense"}` },
		func(d *cadencev2.Descriptor) { d.Commands = map[string]string{"pause": `{"type":"nonsense"}`} },
	} {
		bad := desc("audio")
		edit(bad)
		e.logs.Reset()
		e.sendReport(t, report(bad))
		if len(e.detail(t).Capabilities) != 0 || len(e.storedHash(t)) != 0 {
			t.Fatal("bad schema stored something")
		}
		if !strings.Contains(e.logs.String(), "reason=schema") {
			t.Fatalf("log: %s", e.logs)
		}
	}
}

func TestReportRejectsRemoteSchemaRef(t *testing.T) {
	e := newEnv(t)
	bad := desc("audio")
	bad.ConfigSchema = `{"$ref":"http://127.0.0.1:1/x.json"}`
	e.sendReport(t, report(bad))
	if len(e.detail(t).Capabilities) != 0 || !strings.Contains(e.logs.String(), "reason=schema") {
		t.Fatalf("log: %s", e.logs)
	}
}

func TestReportRejectsDuplicateID(t *testing.T) {
	e := newEnv(t)
	e.sendReport(t, report(desc("audio"), desc("audio")))
	if len(e.detail(t).Capabilities) != 0 {
		t.Fatal("duplicate stored")
	}
	if !strings.Contains(e.logs.String(), "reason=duplicate") {
		t.Fatalf("log: %s", e.logs)
	}
}

func TestReportDropsRemovedCapability(t *testing.T) {
	e := newEnv(t)
	e.sendReport(t, report(desc("audio"), desc("leds")))
	if n := len(e.detail(t).Capabilities); n != 2 {
		t.Fatalf("%d capabilities", n)
	}
	e.sendReport(t, report(desc("audio")))
	caps := e.detail(t).Capabilities
	if len(caps) != 1 || caps[0].ID != "audio" {
		t.Fatalf("%+v", caps)
	}
}

func TestSetConfigPushesWhenOnline(t *testing.T) {
	e := newEnv(t)
	e.sendReport(t, report(desc("audio")))
	gen, err := e.svc.SetConfig(context.Background(), e.id, "audio", json.RawMessage(`{"level":50}`))
	if err != nil || gen != 1 {
		t.Fatalf("gen=%d err=%v", gen, err)
	}
	sets := e.hub.configSets()
	if len(sets) != 1 || sets[0].Capability != "audio" || sets[0].Generation != 1 || !strings.Contains(sets[0].Json, "50") {
		t.Fatalf("%+v", sets)
	}
}

func TestSetConfigOfflineThenPushedOnHello(t *testing.T) {
	e := newEnv(t)
	r := report(desc("audio"))
	e.sendReport(t, r)
	e.hub.setOnline(false)
	gen, err := e.svc.SetConfig(context.Background(), e.id, "audio", json.RawMessage(`{"level":7}`))
	if err != nil || gen != 1 {
		t.Fatalf("gen=%d err=%v", gen, err)
	}
	if len(e.hub.configSets()) != 0 {
		t.Fatal("pushed while offline")
	}
	e.hub.setOnline(true)
	e.svc.Hello(context.Background(), e.id, &cadencev2.Hello{CapabilitiesHash: r.Hash})
	sets := e.hub.configSets()
	if len(sets) != 1 || sets[0].Generation != 1 || !strings.Contains(sets[0].Json, "7") {
		t.Fatalf("%+v", sets)
	}
}

func TestReportPushesPendingConfig(t *testing.T) {
	e := newEnv(t)
	e.sendReport(t, report(desc("audio")))
	e.hub.setOnline(false)
	if _, err := e.svc.SetConfig(context.Background(), e.id, "audio", json.RawMessage(`{"level":7}`)); err != nil {
		t.Fatal(err)
	}
	e.hub.setOnline(true)
	e.sendReport(t, report(desc("audio"), desc("leds")))
	if sets := e.hub.configSets(); len(sets) != 1 || sets[0].Capability != "audio" {
		t.Fatalf("%+v", sets)
	}
}

func TestSetConfigInvalid(t *testing.T) {
	e := newEnv(t)
	e.sendReport(t, report(desc("audio")))
	_, err := e.svc.SetConfig(context.Background(), e.id, "audio", json.RawMessage(`{"level": 101}`))
	var invalid *devices.ConfigInvalidError
	if !errors.As(err, &invalid) || invalid.Path != "/level" {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "101") {
		t.Fatalf("error carries the value: %v", err)
	}
	if e.capability(t, "audio").DesiredGen != 0 {
		t.Fatal("invalid config stored")
	}
	_, err = e.svc.SetConfig(context.Background(), e.id, "audio", json.RawMessage(`[1]`))
	if !errors.As(err, &invalid) {
		t.Fatalf("non-object: %v", err)
	}
}

func TestSetConfigUnknownCapability(t *testing.T) {
	e := newEnv(t)
	_, err := e.svc.SetConfig(context.Background(), e.id, "audio", json.RawMessage(`{}`))
	if !errors.Is(err, devices.ErrUnknownCapability) {
		t.Fatalf("err = %v", err)
	}
}

func applied(capability string, gen int64, code string) *cadencev2.Envelope {
	return &cadencev2.Envelope{Body: &cadencev2.Envelope_ConfigApplied{
		ConfigApplied: &cadencev2.ConfigApplied{Capability: capability, Generation: gen, ErrorCode: code},
	}}
}

func TestAppliedGenerationRules(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.sendReport(t, report(desc("audio")))
	for _, lvl := range []string{`{"level":1}`, `{"level":2}`} {
		if _, err := e.svc.SetConfig(ctx, e.id, "audio", json.RawMessage(lvl)); err != nil {
			t.Fatal(err)
		}
	}
	if !e.svc.Handle(ctx, e.id, applied("audio", 3, "")) {
		t.Fatal("not handled")
	}
	if c := e.capability(t, "audio"); c.AppliedGen != 0 {
		t.Fatalf("future generation applied: %d", c.AppliedGen)
	}
	e.svc.Handle(ctx, e.id, applied("audio", 2, "bad_value"))
	if c := e.capability(t, "audio"); c.AppliedGen != 2 || c.AppliedError != "bad_value" {
		t.Fatalf("%+v", c)
	}
	e.svc.Handle(ctx, e.id, applied("audio", 1, ""))
	if c := e.capability(t, "audio"); c.AppliedGen != 2 || c.AppliedError != "bad_value" {
		t.Fatalf("stale generation applied: %+v", c)
	}
	if !strings.Contains(e.logs.String(), "config_applied_ignored") {
		t.Fatalf("log: %s", e.logs)
	}
}

func delta(capability, js string) *cadencev2.Envelope {
	return &cadencev2.Envelope{Body: &cadencev2.Envelope_StateDelta{
		StateDelta: &cadencev2.StateDelta{Capability: capability, Json: js},
	}}
}

func TestStateInvalidDropped(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.sendReport(t, report(desc("audio")))
	e.svc.Handle(ctx, e.id, delta("audio", `{"playing":"secret-value"}`))
	if _, ok := e.svc.State(ctx, e.id, "audio"); ok {
		t.Fatal("invalid state stored")
	}
	if !strings.Contains(e.logs.String(), "state_invalid") || strings.Contains(e.logs.String(), "secret-value") {
		t.Fatalf("log: %s", e.logs)
	}
}

func TestStateStored(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.sendReport(t, report(desc("audio")))
	e.svc.Handle(ctx, e.id, delta("audio", `{"playing":true}`))
	got, ok := e.svc.State(ctx, e.id, "audio")
	if !ok {
		t.Fatal("state missing")
	}
	var v struct{ Playing bool }
	if err := json.Unmarshal(got, &v); err != nil || !v.Playing {
		t.Fatalf("%s %v", got, err)
	}
}

// the config last set is what a caller compares against before it sets another, so an unchanged one adds no generation
func TestDesiredIsTheConfigLastSet(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	for _, id := range []string{e.id, "not-a-uuid"} {
		if _, err := e.svc.Desired(ctx, id, "audio"); !errors.Is(err, devices.ErrUnknownCapability) {
			t.Fatalf("%s before the report: %v", id, err)
		}
	}
	e.sendReport(t, report(desc("audio")))
	if got, err := e.svc.Desired(ctx, e.id, "audio"); err != nil || got != nil {
		t.Fatalf("none set: %s %v", got, err)
	}
	if _, err := e.svc.SetConfig(ctx, e.id, "audio", json.RawMessage(`{"level":50}`)); err != nil {
		t.Fatal(err)
	}
	got, err := e.svc.Desired(ctx, e.id, "audio")
	var v struct{ Level int }
	if err != nil || json.Unmarshal(got, &v) != nil || v.Level != 50 {
		t.Fatalf("set: %s %v", got, err)
	}
}

func TestHandleIgnoresOtherMessages(t *testing.T) {
	e := newEnv(t)
	other := &cadencev2.Envelope{Body: &cadencev2.Envelope_SessionCancel{SessionCancel: &cadencev2.SessionCancel{}}}
	if e.svc.Handle(context.Background(), e.id, other) {
		t.Fatal("handled a foreign message")
	}
}

func TestSetConfigNearLimitPushesCompacted(t *testing.T) {
	e := newEnv(t)
	open := desc("audio")
	open.ConfigSchema = ""
	e.sendReport(t, report(open))
	var b strings.Builder
	b.WriteByte('{')
	for i := range 1625 {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `"k%04d":1`, i)
	}
	b.WriteByte('}')
	if gen, err := e.svc.SetConfig(context.Background(), e.id, "audio", json.RawMessage(b.String())); err != nil || gen != 1 {
		t.Fatalf("gen=%d err=%v", gen, err)
	}
	if got := e.capability(t, "audio").Desired; len(got) <= devices.MaxConfigFrameBytes {
		t.Fatalf("scenario too small: stored form is %d bytes", len(got))
	}
	sets := e.hub.configSets()
	if len(sets) != 1 {
		t.Fatalf("%d pushes", len(sets))
	}
	env := &cadencev2.Envelope{Body: &cadencev2.Envelope_ConfigSet{ConfigSet: sets[0]}}
	if _, err := wire.Encode(env); err != nil {
		t.Fatalf("pushed frame does not fit: %v", err)
	}
}

// the link's reader holds the device's lifecycle lock through these calls, so
// a hung store must not park it, nor the device's next link behind it
func TestLinkCallsBoundedWhenStoreHangs(t *testing.T) {
	e := newEnv(t)
	e.sendReport(t, report(desc("audio")))
	devices.SetDBTimeout(e.svc, 100*time.Millisecond)
	ctx := context.Background()
	tx, err := e.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	if _, err := tx.Exec(ctx, "LOCK TABLE devices, device_capabilities IN ACCESS EXCLUSIVE MODE"); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, event string
		call        func()
	}{
		{"hello", "hello_failed", func() { e.svc.Hello(ctx, e.id, &cadencev2.Hello{CapabilitiesHash: []byte{1}}) }},
		{"report", "capability_store_failed", func() {
			e.svc.Handle(ctx, e.id, &cadencev2.Envelope{Body: &cadencev2.Envelope_CapabilityReport{CapabilityReport: report(desc("audio"))}})
		}},
		{"applied", "config_applied_failed", func() { e.svc.Handle(ctx, e.id, applied("audio", 1, "")) }},
		{"state", "state_failed", func() { e.svc.Handle(ctx, e.id, delta("audio", `{"playing":true}`)) }},
	} {
		done := make(chan struct{})
		go func() {
			defer close(done)
			c.call()
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: still waiting on the hung store", c.name)
		}
		if !strings.Contains(e.logs.String(), "msg="+c.event) {
			t.Fatalf("%s: log: %s", c.name, e.logs)
		}
	}
}

func (h *fakeHub) setOnSend(f func(*cadencev2.Envelope)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.onSend = f
}

// two capabilities pending, ready for a hello push
func pendingTwo(t *testing.T) (*env, *cadencev2.CapabilityReport) {
	t.Helper()
	e := newEnv(t)
	r := report(desc("a"), desc("b"))
	e.sendReport(t, r)
	e.hub.setOnline(false)
	for _, c := range []string{"a", "b"} {
		if _, err := e.svc.SetConfig(context.Background(), e.id, c, json.RawMessage(`{"level":1}`)); err != nil {
			t.Fatal(err)
		}
	}
	e.hub.setOnline(true)
	return e, r
}

func TestPushesStayOrdered(t *testing.T) {
	e, r := pendingTwo(t)
	var once sync.Once
	var wg sync.WaitGroup
	e.hub.setOnSend(func(env *cadencev2.Envelope) {
		if env.GetConfigSet().GetCapability() != "a" {
			return
		}
		once.Do(func() {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, err := e.svc.SetConfig(context.Background(), e.id, "b", json.RawMessage(`{"level":2}`)); err != nil {
					t.Error(err)
				}
			}()
			time.Sleep(50 * time.Millisecond)
		})
	})
	e.svc.Hello(context.Background(), e.id, &cadencev2.Hello{CapabilitiesHash: r.Hash})
	wg.Wait()
	var last int64
	var n int
	for _, c := range e.hub.configSets() {
		if c.Capability != "b" {
			continue
		}
		n++
		if c.Generation <= last {
			t.Fatalf("b gen %d after %d", c.Generation, last)
		}
		last = c.Generation
	}
	if n != 2 || last != 2 {
		t.Fatalf("b pushes=%d last=%d", n, last)
	}
}

func TestSetConfigWaitsForPendingPush(t *testing.T) {
	e, r := pendingTwo(t)
	inPush, release := make(chan struct{}), make(chan struct{})
	var first atomic.Bool
	e.hub.setOnSend(func(*cadencev2.Envelope) {
		if first.CompareAndSwap(false, true) {
			close(inPush)
			<-release
		}
	})
	helloDone := make(chan struct{})
	go func() {
		defer close(helloDone)
		e.svc.Hello(context.Background(), e.id, &cadencev2.Hello{CapabilitiesHash: r.Hash})
	}()
	<-inPush
	setDone := make(chan struct{})
	go func() {
		defer close(setDone)
		if _, err := e.svc.SetConfig(context.Background(), e.id, "a", json.RawMessage(`{"level":2}`)); err != nil {
			t.Error(err)
		}
	}()
	select {
	case <-setDone:
		t.Fatal("SetConfig did not wait for the push in flight")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	for _, c := range []chan struct{}{helloDone, setDone} {
		select {
		case <-c:
		case <-time.After(5 * time.Second):
			t.Fatal("stuck")
		}
	}
}
