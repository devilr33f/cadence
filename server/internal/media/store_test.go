package media_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/gofiber/fiber/v3"
	"google.golang.org/protobuf/proto"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/server/internal/api"
	"git.shino.boo/devilreef/cadence/server/internal/api/apitest"
	"git.shino.boo/devilreef/cadence/server/internal/auth"
	"git.shino.boo/devilreef/cadence/server/internal/devices"
	"git.shino.boo/devilreef/cadence/server/internal/events"
	"git.shino.boo/devilreef/cadence/server/internal/media"
)

const devID = "dev-1"

// fakeDevices knows one device, in any letter case, as devices.Service does.
type fakeDevices struct{ err error }

func (f fakeDevices) Canonical(_ context.Context, id string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	id = strings.ToLower(id)
	if id != devID && id != "dev-2" {
		return "", devices.ErrNotFound
	}
	return id, nil
}

type noPing struct{}

func (noPing) Ping(context.Context) error { return nil }

type env struct {
	store *media.Store
	sub   *events.Subscription
	devs  media.Devices
	art   *media.Artwork
	// verify is the token check: apitest.Verify unless a test takes its own
	verify func(context.Context, string) (auth.Grants, error)
	logs   *bytes.Buffer
}

func newEnv(t *testing.T) *env {
	t.Helper()
	bus := events.NewBus(0)
	sub := bus.Watch(func(ev events.Event) bool { return ev.DeviceID == devID })
	t.Cleanup(sub.Close)
	logs := &bytes.Buffer{}
	return &env{
		store: media.NewStore(bus, slog.New(slog.NewTextHandler(logs, nil))), sub: sub, devs: fakeDevices{}, verify: apitest.Verify,
		logs: logs,
	}
}

func (e *env) get(t *testing.T, id string) (int, string) {
	t.Helper()
	return e.getAs(t, "x", id)
}

func (e *env) getAs(t *testing.T, bearer, id string) (int, string) {
	t.Helper()
	code, _, b := e.do(t, bearer, "/api/v1/devices/"+id+"/media")
	return code, string(b)
}

func (e *env) app() *fiber.App {
	return api.NewApp(api.Options{
		DB: noPing{}, Version: "t",
		VerifyToken: e.verify,
		Admin:       []func(huma.API){func(a huma.API) { media.RegisterAdmin(a, e.store, e.devs, e.art) }},
	})
}

// do sends GET path to the media routes as bearer.
func (e *env) do(t *testing.T, bearer, path string) (int, http.Header, []byte) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := e.app().Test(req, fiber.TestConfig{Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, b
}

// event returns the next bus event's data as JSON, or "" when none came.
func (e *env) event(t *testing.T) string {
	t.Helper()
	select {
	case ev := <-e.sub.C:
		if ev.Type != "media_state" || ev.DeviceID != devID || ev.At.IsZero() {
			t.Fatalf("event %+v", ev)
		}
		b, err := json.Marshal(ev.Data)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	default:
		return ""
	}
}

func handle(t *testing.T, s *media.Store, dev string, m *cadencev2.MediaSessionState) {
	t.Helper()
	if !s.Handle(dev, &cadencev2.Envelope{Body: &cadencev2.Envelope_MediaSessionState{MediaSessionState: m}}) {
		t.Fatal("media state not consumed")
	}
}

func playing() *cadencev2.MediaSessionState {
	vol := uint32(40)
	return &cadencev2.MediaSessionState{
		SessionId: "ms-1", Source: cadencev2.MediaSource_MEDIA_SOURCE_URL,
		State:      cadencev2.PlaybackState_PLAYBACK_STATE_PLAYING,
		PositionMs: 1500, PositionAtMs: time.Date(2026, 11, 8, 10, 0, 0, 123e6, time.UTC).UnixMilli(),
		Rate: 1, DurationMs: 180000, Volume: &vol,
		Metadata: &cadencev2.MediaMetadata{Title: "T", Artist: "A", Album: "B", ArtworkUrl: "https://art.example/c.jpg"},
		Actions:  []string{"pause", "stop", "seek"},
	}
}

const playingJSON = `{"sessionId":"ms-1","source":"url","state":"playing","positionMs":1500,` +
	`"positionAt":"2026-11-08T10:00:00.123Z","rate":1,"durationMs":180000,"volume":40,` +
	`"metadata":{"title":"T","artist":"A","album":"B","artworkUrl":"https://art.example/c.jpg",` +
	`"artworkHash":"ac3a6b502b228734cd190349f7aedd7c0187859f4bba069dbad628206e371f0e"},` +
	`"actions":["pause","stop","seek"]}`

func TestSessionShown(t *testing.T) {
	e := newEnv(t)
	if code, body := e.get(t, devID); code != 200 || body != `{"state":"idle"}` {
		t.Fatalf("before: %d %s", code, body)
	}
	handle(t, e.store, devID, playing())
	if code, body := e.get(t, strings.ToUpper(devID)); code != 200 || body != playingJSON {
		t.Fatalf("%d %s", code, body)
	}
	if got := e.event(t); got != playingJSON {
		t.Fatalf("event %s", got)
	}
	if !strings.Contains(e.logs.String(), "msg=media_state device_id=dev-1 state=playing source=url\n") {
		t.Fatalf("log: %s", e.logs)
	}

	// each message replaces the last; a volume the device does not know is left out
	paused := playing()
	paused.State, paused.Volume, paused.Metadata, paused.Actions = cadencev2.PlaybackState_PLAYBACK_STATE_PAUSED, nil, nil, nil
	handle(t, e.store, devID, paused)
	want := `{"sessionId":"ms-1","source":"url","state":"paused","positionMs":1500,` +
		`"positionAt":"2026-11-08T10:00:00.123Z","rate":1,"durationMs":180000,` +
		`"metadata":{"title":"","artist":"","album":"","artworkUrl":"","artworkHash":""},"actions":[]}`
	if code, body := e.get(t, devID); code != 200 || body != want {
		t.Fatalf("%d %s", code, body)
	}
	if got := e.event(t); got != want {
		t.Fatalf("event %s", got)
	}
}

// the screen's video player says how far ahead it buffered and, while it waits for the cache, how full it is
func TestBufferShown(t *testing.T) {
	e := newEnv(t)
	m := playing()
	m.Source, m.State, m.Volume, m.Metadata, m.Actions = cadencev2.MediaSource_MEDIA_SOURCE_VIDEO,
		cadencev2.PlaybackState_PLAYBACK_STATE_BUFFERING, nil, nil, nil
	m.BufferedMs, m.BufferingPercent = 4200, proto.Uint32(130)
	handle(t, e.store, devID, m)
	want := `{"sessionId":"ms-1","source":"video","state":"buffering","positionMs":1500,` +
		`"positionAt":"2026-11-08T10:00:00.123Z","rate":1,"durationMs":180000,"bufferedMs":4200,"bufferingPercent":100,` +
		`"metadata":{"title":"","artist":"","album":"","artworkUrl":"","artworkHash":""},"actions":[]}`
	if code, body := e.get(t, devID); code != 200 || body != want {
		t.Fatalf("%d %s", code, body)
	}
	if got := e.event(t); got != want {
		t.Fatalf("event %s", got)
	}
}

func TestIdleClears(t *testing.T) {
	e := newEnv(t)
	handle(t, e.store, devID, playing())
	e.event(t)
	idle := &cadencev2.MediaSessionState{State: cadencev2.PlaybackState_PLAYBACK_STATE_IDLE}
	handle(t, e.store, devID, idle)
	if code, body := e.get(t, devID); code != 200 || body != `{"state":"idle"}` {
		t.Fatalf("%d %s", code, body)
	}
	if got := e.event(t); got != `{"state":"idle"}` {
		t.Fatalf("event %s", got)
	}
	// none stays none: no event
	handle(t, e.store, devID, idle)
	if got := e.event(t); got != "" {
		t.Fatalf("event %s", got)
	}
	// an idle session that still has an id is a session; a position the device never stamped has no time
	named := &cadencev2.MediaSessionState{SessionId: "ms-2", State: cadencev2.PlaybackState_PLAYBACK_STATE_IDLE}
	handle(t, e.store, devID, named)
	want := `{"sessionId":"ms-2","source":"unspecified","state":"idle","positionMs":0,"rate":0,"durationMs":0,` +
		`"metadata":{"title":"","artist":"","album":"","artworkUrl":"","artworkHash":""},"actions":[]}`
	if _, body := e.get(t, devID); body != want {
		t.Fatal(body)
	}
}

func TestDisconnectClears(t *testing.T) {
	e := newEnv(t)
	handle(t, e.store, devID, playing())
	handle(t, e.store, "dev-2", playing())
	e.event(t)
	e.store.Closed(devID)
	if code, body := e.get(t, devID); code != 200 || body != `{"state":"idle"}` {
		t.Fatalf("%d %s", code, body)
	}
	if got := e.event(t); got != `{"state":"idle"}` {
		t.Fatalf("event %s", got)
	}
	if _, body := e.get(t, "dev-2"); body != playingJSON {
		t.Fatalf("other device: %s", body)
	}
	// a device with no session closes without an event
	e.store.Closed(devID)
	if got := e.event(t); got != "" {
		t.Fatalf("event %s", got)
	}
}

func TestOversizeAndUnknownDropped(t *testing.T) {
	e := newEnv(t)
	m := playing()
	m.SessionId = strings.Repeat("s", 64)
	m.Source, m.State = cadencev2.MediaSource(99), cadencev2.PlaybackState(42)
	m.Rate = float32(math.NaN())
	m.Metadata = &cadencev2.MediaMetadata{
		Title:      strings.Repeat("t", 256),
		Artist:     strings.Repeat("a", 255),
		Album:      strings.Repeat("é", 128), // 256 bytes
		ArtworkUrl: "https://art.example/" + strings.Repeat("x", 2048-20),
	}
	m.Actions = []string{"pause", "explode", "pause", strings.Repeat("x", 300), "next"}
	handle(t, e.store, devID, m)
	_, body := e.get(t, devID)
	var got struct {
		SessionID, Source, State string
		Rate                     float64
		Metadata                 struct{ Title, Artist, Album, ArtworkURL, ArtworkHash string }
		Actions                  []string
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err, body)
	}
	if got.SessionID != "" || got.Source != "unspecified" || got.State != "unspecified" || got.Rate != 0 {
		t.Fatalf("%s", body)
	}
	if got.Metadata.Title != "" || got.Metadata.Artist != m.Metadata.Artist || got.Metadata.Album != "" ||
		got.Metadata.ArtworkURL != "" || got.Metadata.ArtworkHash != "" {
		t.Fatalf("%s", body)
	}
	if strings.Join(got.Actions, ",") != "pause,next" {
		t.Fatalf("actions %v", got.Actions)
	}
	// the longest kept url
	m.Metadata.ArtworkUrl = m.Metadata.ArtworkUrl[:2047]
	handle(t, e.store, devID, m)
	if _, body := e.get(t, devID); !strings.Contains(body, m.Metadata.ArtworkUrl) {
		t.Fatal("a 2047-byte artwork url was dropped")
	}
}

func TestUnknownDevice(t *testing.T) {
	e := newEnv(t)
	if code, body := e.get(t, "nope"); code != 404 || body != `{"error":"not_found"}` {
		t.Fatalf("%d %s", code, body)
	}
	// the cause can carry driver text: neither the body nor the log has it
	e.devs = fakeDevices{err: errors.New("db down: secret-db-text")}
	if code, body := e.get(t, devID); code != 500 || body != `{"error":"internal"}` {
		t.Fatalf("%d %s", code, body)
	}
	if logs := e.logs.String(); !strings.Contains(logs, "msg=media_state_failed") || strings.Contains(logs, "secret") {
		t.Fatalf("log: %s", logs)
	}
}

func TestHandleIgnoresOtherMessages(t *testing.T) {
	e := newEnv(t)
	other := &cadencev2.Envelope{Body: &cadencev2.Envelope_CommandResult{CommandResult: &cadencev2.CommandResult{}}}
	if e.store.Handle(devID, other) {
		t.Fatal("consumed a command result")
	}
}

// an ha token reads a device's media session (M7a-D6)
func TestHATokenReadsTheSession(t *testing.T) {
	e := newEnv(t)
	handle(t, e.store, devID, playing())
	if code, body := e.getAs(t, apitest.HABearer, devID); code != 200 || body != playingJSON {
		t.Fatalf("%d %s", code, body)
	}
}

// a multiroom member plays its leader's stream: its session says so (MEDIA_SOURCE_GROUP), never unspecified
func TestGroupMemberSessionShown(t *testing.T) {
	e := newEnv(t)
	m := playing()
	m.Source = cadencev2.MediaSource_MEDIA_SOURCE_GROUP
	handle(t, e.store, devID, m)
	want := strings.Replace(playingJSON, `"source":"url"`, `"source":"group"`, 1)
	if code, body := e.get(t, devID); code != 200 || body != want {
		t.Fatalf("%d %s", code, body)
	}
}

func TestSessionObserverSeesTheWholeLifecycle(t *testing.T) {
	e := newEnv(t)
	type report struct {
		previous, current string
		provider          string
	}
	var reports []report
	e.store.Decorate(func(_ string, s *media.MediaSession) { s.Provider = "YouTube Music" })
	e.store.OnSession(func(_ string, previous, current *media.MediaSession) {
		r := report{}
		if previous != nil {
			r.previous = previous.State
		}
		if current != nil {
			r.current, r.provider = current.State, current.Provider
		}
		reports = append(reports, r)
	})
	handle(t, e.store, devID, playing())
	paused := playing()
	paused.State = cadencev2.PlaybackState_PLAYBACK_STATE_PAUSED
	handle(t, e.store, devID, paused)
	e.store.Closed(devID)
	want := []report{{current: "playing", provider: "YouTube Music"}, {previous: "playing", current: "paused", provider: "YouTube Music"}, {previous: "paused"}}
	if len(reports) != len(want) {
		t.Fatalf("reports %+v", reports)
	}
	for i := range want {
		if reports[i] != want[i] {
			t.Fatalf("report %d %+v, want %+v", i, reports[i], want[i])
		}
	}
}
