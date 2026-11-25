package devices_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"git.shino.boo/devilreef/cadence/server/internal/api"
	"git.shino.boo/devilreef/cadence/server/internal/auth"
	"git.shino.boo/devilreef/cadence/server/internal/devices"
	"git.shino.boo/devilreef/cadence/server/internal/events"
	"git.shino.boo/devilreef/cadence/server/internal/store/db"
)

// recorder keeps what a service published.
type recorder struct {
	mu     sync.Mutex
	events []events.Event
}

func (r *recorder) Publish(e events.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

// take returns the events published since the last call, as the feed writes
// them but for their time.
func (r *recorder) take(t *testing.T) []string {
	t.Helper()
	r.mu.Lock()
	got := r.events
	r.events = nil
	r.mu.Unlock()
	out := []string{}
	for _, e := range got {
		if e.At.IsZero() {
			t.Errorf("%s has no time", e.Type)
		}
		b, err := json.Marshal(struct {
			Type     string `json:"type"`
			DeviceID string `json:"deviceId"`
			Data     any    `json:"data,omitempty"`
		}{e.Type, e.DeviceID, e.Data})
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, string(b))
	}
	return out
}

func wantEvents(t *testing.T, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("published\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// a rename publishes the device under its canonical id, online as the hub has
// it; a refused rename and a second delete publish nothing; a delete publishes
// its id alone
func TestRenameAndDeletePublish(t *testing.T) {
	e := newEnv(t)
	rec := &recorder{}
	e.svc.PublishTo(rec)
	ctx := context.Background()
	if err := e.svc.Rename(ctx, strings.ToUpper(e.id), " Hall "); err != nil {
		t.Fatal(err)
	}
	e.hub.setOnline(false)
	if err := e.svc.Rename(ctx, e.id, "Den"); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Rename(ctx, e.id, ""); err == nil {
		t.Fatal("an empty name renamed")
	}
	wantEvents(t, rec.take(t),
		`{"type":"device_renamed","deviceId":"`+e.id+`","data":{"name":"Hall","model":"sim","online":true}}`,
		`{"type":"device_renamed","deviceId":"`+e.id+`","data":{"name":"Den","model":"sim","online":false}}`,
	)
	if err := e.svc.Delete(ctx, strings.ToUpper(e.id)); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Delete(ctx, e.id); err == nil {
		t.Fatal("deleted twice")
	}
	wantEvents(t, rec.take(t), `{"type":"device_deleted","deviceId":"`+e.id+`"}`)
}

// device_volume goes out whenever the audio.output level the device reports
// differs from the one stored before, the first one too (Ruling M8-R1); the
// other fields, another capability and a refused state publish nothing
func TestDeviceVolumeOnLevelChange(t *testing.T) {
	e := newEnv(t)
	rec := &recorder{}
	e.svc.PublishTo(rec)
	ctx := context.Background()
	const outputState = `{"type":"object","properties":{"level":{"type":"integer"},"channels":{"type":"integer"}},"required":["level"]}`
	out := desc("audio.output")
	out.StateSchema = outputState
	e.sendReport(t, report(out, desc("audio")))
	for _, js := range []string{
		`{"level":30,"channels":1}`, `{"level":30,"channels":2}`, `{"level":"loud"}`, `{"level":45,"channels":2}`,
	} {
		e.svc.Handle(ctx, e.id, delta("audio.output", js))
	}
	e.svc.Handle(ctx, e.id, delta("audio", `{"playing":true}`))
	wantEvents(t, rec.take(t),
		`{"type":"device_volume","deviceId":"`+e.id+`","data":{"level":30}}`,
		`{"type":"device_volume","deviceId":"`+e.id+`","data":{"level":45}}`,
	)
}

// flakyStore is the pool, its device reads failed or held on demand: a held
// read runs, then answers once released, as a slow store would
type flakyStore struct {
	*pgxpool.Pool

	mu   sync.Mutex
	fail bool
	hold chan struct{}
	// reads that reached the store while held
	held atomic.Int32
}

const deviceByID = "-- name: DeviceByID "

func (s *flakyStore) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	if !strings.HasPrefix(sql, deviceByID) {
		return s.Pool.QueryRow(ctx, sql, args...)
	}
	s.mu.Lock()
	fail, hold := s.fail, s.hold
	s.mu.Unlock()
	if fail {
		return failedRow{}
	}
	row := s.Pool.QueryRow(ctx, sql, args...)
	if hold != nil {
		s.held.Add(1)
	}
	return heldRow{row: row, hold: hold}
}

// holdReads holds the device reads from now until the func returned is called
func (s *flakyStore) holdReads() (release func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	hold := make(chan struct{})
	s.hold = hold
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.hold = nil
		close(hold)
	}
}

func (s *flakyStore) failReads(v bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fail = v
}

type failedRow struct{}

func (failedRow) Scan(...any) error { return errors.New("store down: password=hunter2") }

type heldRow struct {
	row  pgx.Row
	hold chan struct{}
}

func (r heldRow) Scan(dest ...any) error {
	err := r.row.Scan(dest...)
	if r.hold != nil {
		<-r.hold
	}
	return err
}

// flakyEnv is the env's device with a service over a flakyStore
func flakyEnv(t *testing.T) (*env, *devices.Service, *flakyStore, *recorder, *bytes.Buffer) {
	t.Helper()
	e := newEnv(t)
	store := &flakyStore{Pool: e.pool}
	logs := &bytes.Buffer{}
	svc := devices.NewService(store, e.hub, e.box, slog.New(slog.NewTextHandler(logs, nil)))
	rec := &recorder{}
	svc.PublishTo(rec)
	return e, svc, store, rec, logs
}

// a link's presence comes from the name and model its last-seen write
// answered, so a store that fails then loses neither its online nor its
// offline; a rename keeps them current (M8-D2)
func TestPresenceFromTheCacheWhenTheStoreFails(t *testing.T) {
	e, svc, store, rec, logs := flakyEnv(t)
	svc.Remember(e.id, "kitchen", "sim")
	if err := svc.Rename(context.Background(), e.id, "Hall"); err != nil {
		t.Fatal(err)
	}
	store.failReads(true)
	svc.Linked(e.id)
	e.hub.setOnline(false)
	svc.Unlinked(e.id)
	svc.WaitPresence()
	wantEvents(t, rec.take(t),
		`{"type":"device_renamed","deviceId":"`+e.id+`","data":{"name":"Hall","model":"sim","online":true}}`,
		`{"type":"device_online","deviceId":"`+e.id+`","data":{"name":"Hall","model":"sim","online":true}}`,
		`{"type":"device_offline","deviceId":"`+e.id+`","data":{"name":"Hall","model":"sim","online":false}}`,
	)
	if strings.Contains(logs.String(), "presence") {
		t.Fatalf("log %s", logs)
	}
}

// without a cached name a link's presence reads the device after the hub let
// go of the link, and publishes it only while the hub still agrees; a failed
// read logs its event and no secret; a deleted device gets none after
// device_deleted, whatever a late read answers
func TestPresenceReadLate(t *testing.T) {
	e, svc, store, rec, logs := flakyEnv(t)
	store.failReads(true)
	svc.Linked(e.id)
	svc.WaitPresence()
	wantEvents(t, rec.take(t))
	if out := logs.String(); strings.Count(out, "msg=device_presence_failed device_id="+e.id+" type=device_online\n") != 1 ||
		strings.Contains(out, "hunter2") {
		t.Fatalf("log %s", out)
	}
	store.failReads(false)
	// the link closed before its online was read: that online is never published
	release := store.holdReads()
	svc.Linked(e.id)
	e.hub.setOnline(false)
	svc.Unlinked(e.id)
	release()
	svc.WaitPresence()
	wantEvents(t, rec.take(t),
		`{"type":"device_offline","deviceId":"`+e.id+`","data":{"name":"kitchen","model":"sim","online":false}}`)

	other, err := e.q.CreateDevice(context.Background(), db.CreateDeviceParams{Name: "den", Model: "sim"})
	if err != nil {
		t.Fatal(err)
	}
	dev := other.ID.String()
	e.hub.mu.Lock()
	e.hub.dev, e.hub.online = dev, true
	e.hub.mu.Unlock()
	release = store.holdReads()
	svc.Linked(dev)
	waitHeld(t, store, 1)
	if err := svc.Delete(context.Background(), dev); err != nil {
		t.Fatal(err)
	}
	svc.Unlinked(dev)
	release()
	svc.WaitPresence()
	wantEvents(t, rec.take(t), `{"type":"device_deleted","deviceId":"`+dev+`"}`)
}

// waitHeld waits until n held reads reached the store
func waitHeld(t *testing.T, store *flakyStore, n int32) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for store.held.Load() < n {
		if time.Now().After(deadline) {
			t.Fatalf("%d held reads, want %d", store.held.Load(), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// ownerCookie takes the one session cookie of the feed rig's owner
type ownerCookie struct{}

func (ownerCookie) Login(context.Context, string, []string, string) (auth.Grant, error) {
	return auth.Grant{}, auth.ErrUnauthorized
}

func (ownerCookie) Validate(_ context.Context, token string) (auth.Session, error) {
	if token == "owner-cookie" {
		return auth.Session{ExpiresAt: time.Now().Add(time.Hour)}, nil
	}
	return auth.Session{}, auth.ErrUnauthorized
}

func (ownerCookie) Logout(context.Context, string) error { return nil }

// feedRig serves the live feed over bus to an ha bearer and the owner's cookie
type feedRig struct{ url, origin string }

func startFeed(t *testing.T, bus *events.Bus) feedRig {
	t.Helper()
	feed := events.NewFeed(bus, nil)
	app := api.NewApp(api.Options{
		DB: noPing{}, Version: "t", Sessions: ownerCookie{},
		VerifyToken: func(_ context.Context, bearer string) (auth.Grants, error) {
			if bearer == "ha-token" {
				return auth.Grants{Scopes: auth.Scopes{auth.ScopeHA}}, nil
			}
			return auth.Grants{}, auth.ErrUnauthorized
		},
		Plain: []api.PlainRoute{feed.Route()},
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = app.Listener(ln, fiber.ListenConfig{DisableStartupMessage: true}) }()
	t.Cleanup(func() {
		feed.Close(int(websocket.StatusGoingAway), "")
		_ = app.Shutdown()
	})
	return feedRig{url: "ws://" + ln.Addr().String() + "/api/v1/live", origin: "http://" + ln.Addr().String()}
}

// open dials the feed and reads its first frame: what is published after it arrives
func (f feedRig) open(t *testing.T, header http.Header) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, f.url, &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.CloseNow() })
	if b := readText(t, c); b != `{"type":"subscribed"}` {
		t.Fatalf("first frame %s", b)
	}
	return c
}

func readText(t *testing.T, c *websocket.Conn) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	_, b, err := c.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// readEvent reads a feed frame without its time, which it must carry
func readEvent(t *testing.T, c *websocket.Conn) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(readText(t, c)), &m); err != nil {
		t.Fatal(err)
	}
	if at, _ := m["at"].(string); at == "" {
		t.Errorf("frame %v has no time", m)
	}
	delete(m, "at")
	b, _ := json.Marshal(m)
	return string(b)
}

// the whole lifecycle through the real gateway, enrolment, admin api and feed:
// enrolled, linked, renamed, closed, linked again and deleted, each event on an
// ha feed without a subscription, none on the owner's feed subscribed to logs
// alone, and no offline after the delete (M8-D2)
func TestPresenceReachesHA(t *testing.T) {
	r := newLinkRig(t)
	f := startFeed(t, r.bus)
	ha := f.open(t, http.Header{"Authorization": {"Bearer ha-token"}})
	owner := f.open(t, http.Header{"Cookie": {api.SessionCookie + "=owner-cookie"}, "Origin": {f.origin}})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := owner.Write(ctx, websocket.MessageText, []byte(`{"subscribe":["logs"]}`)); err != nil {
		t.Fatal(err)
	}
	if b := readText(t, owner); b != `{"type":"subscribed","topics":["logs"]}` {
		t.Fatalf("subscription %s", b)
	}

	id, token := r.enrol(t)
	device := func(typ, name string, online bool) string {
		b, _ := json.Marshal(map[string]any{"type": typ, "deviceId": id, "data": map[string]any{"name": name, "model": "sim", "online": online}})
		return string(b)
	}
	closed := func() {
		t.Helper()
		select {
		case got := <-r.closed:
			if got != id {
				t.Fatalf("OnClose(%s)", got)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("no OnClose")
		}
	}
	c := r.link(t, token)
	if code, body := call(t, r.admin, http.MethodPatch, "/api/v1/devices/"+id, `{"name":"Hall"}`); code != 200 {
		t.Fatalf("rename %d %s", code, body)
	}
	_ = c.Close(websocket.StatusNormalClosure, "")
	closed()
	r.link(t, token)
	if code, body := call(t, r.admin, http.MethodDelete, "/api/v1/devices/"+id, ""); code != 204 {
		t.Fatalf("delete %d %s", code, body)
	}
	closed()
	// what follows the delete on each feed: nothing of the device came between
	r.bus.Publish(events.Event{Type: events.MediaState, DeviceID: "after", At: time.Now()})
	r.bus.Publish(events.Event{Type: events.LogLine, DeviceID: "after", At: time.Now()})
	for i, want := range []string{
		device(events.DeviceAdded, "kitchen", false),
		device(events.DeviceOnline, "kitchen", true),
		device(events.DeviceRenamed, "Hall", true),
		device(events.DeviceOffline, "Hall", false),
		device(events.DeviceOnline, "Hall", true),
		`{"deviceId":"` + id + `","type":"device_deleted"}`,
		`{"deviceId":"after","type":"media_state"}`,
	} {
		if got := readEvent(t, ha); got != want {
			t.Fatalf("ha frame %d: %s, want %s", i, got, want)
		}
	}
	if got := readEvent(t, owner); got != `{"deviceId":"after","type":"log_line"}` {
		t.Fatalf("the logs feed read %s", got)
	}
}

// presenceOf reads the presence events sub holds now, by type, until the
// sentinel the test published after them
func presenceOf(t *testing.T, r *linkRig, sub *events.Subscription) []string {
	t.Helper()
	r.bus.Publish(events.Event{Type: "sentinel"})
	var got []string
	for {
		select {
		case e := <-sub.C:
			if e.Type == "sentinel" {
				return got
			}
			got = append(got, e.Type)
		case <-time.After(5 * time.Second):
			t.Fatalf("no sentinel after %v", got)
		}
	}
}

// a link that replaces another publishes one more device_online and no
// device_offline for the one it replaced (the hub gives that one no OnClose)
func TestReplacedLinkPublishesOneOnline(t *testing.T) {
	r := newLinkRig(t)
	_, token := r.enrol(t)
	sub := r.bus.Watch(nil)
	defer sub.Close()
	first := r.link(t, token)
	r.link(t, token)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if _, _, err := first.Read(ctx); websocket.CloseStatus(err) != websocket.StatusGoingAway {
		t.Fatalf("the replaced link: %v", err)
	}
	if got := strings.Join(presenceOf(t, r, sub), ","); got != "device_online,device_online" {
		t.Fatalf("published %s", got)
	}
	select {
	case id := <-r.closed:
		t.Fatalf("OnClose(%s) for a replaced link", id)
	default:
	}
}

// a store that answers late holds no link up: the hello, the close and the
// link again pass while the device reads wait, and once they answer only the
// device's online stands; with its name read, the next close publishes its
// offline at once, the store still slow (M8-D2)
func TestSlowStoreStallsNoLink(t *testing.T) {
	r := newLinkRig(t)
	devices.SetDBTimeout(r.svc, time.Minute)
	id, token := r.enrol(t)
	sub := r.bus.Watch(nil)
	defer sub.Close()
	r.forget.Store(true)
	release := r.store.holdReads()
	quick := func(what string, fn func()) {
		t.Helper()
		began := time.Now()
		fn()
		if d := time.Since(began); d > 2*time.Second {
			t.Fatalf("%s took %v", what, d)
		}
	}
	var c *websocket.Conn
	quick("the hello", func() { c = r.link(t, token) })
	quick("the close", func() {
		_ = c.Close(websocket.StatusNormalClosure, "")
		if got := <-r.closed; got != id {
			t.Fatalf("OnClose(%s)", got)
		}
	})
	quick("the link again", func() { c = r.link(t, token) })
	waitHeld(t, r.store, 3)
	release()
	r.svc.WaitPresence()
	for _, typ := range presenceOf(t, r, sub) {
		if typ != events.DeviceOnline {
			t.Fatalf("published %s", typ)
		}
	}
	release = r.store.holdReads()
	defer release()
	quick("the close with its name known", func() {
		_ = c.Close(websocket.StatusNormalClosure, "")
		<-r.closed
	})
	if got := strings.Join(presenceOf(t, r, sub), ","); got != events.DeviceOffline {
		t.Fatalf("published %s", got)
	}
}
