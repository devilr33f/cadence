package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5/pgtype"

	"git.shino.boo/devilreef/cadence/pkg/caphash"
	"git.shino.boo/devilreef/cadence/pkg/capschema"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/pkg/wire"
	"git.shino.boo/devilreef/cadence/server/internal/alarmview"
	"git.shino.boo/devilreef/cadence/server/internal/api"
	"git.shino.boo/devilreef/cadence/server/internal/api/apitest"
	"git.shino.boo/devilreef/cadence/server/internal/catalog"
	"git.shino.boo/devilreef/cadence/server/internal/catalog/catalogtest"
	"git.shino.boo/devilreef/cadence/server/internal/commands"
	"git.shino.boo/devilreef/cadence/server/internal/devices"
	"git.shino.boo/devilreef/cadence/server/internal/enroll"
	"git.shino.boo/devilreef/cadence/server/internal/events"
	"git.shino.boo/devilreef/cadence/server/internal/gateway"
	"git.shino.boo/devilreef/cadence/server/internal/groups"
	"git.shino.boo/devilreef/cadence/server/internal/images"
	"git.shino.boo/devilreef/cadence/server/internal/media"
	"git.shino.boo/devilreef/cadence/server/internal/secrets"
	"git.shino.boo/devilreef/cadence/server/internal/store/db"
	"git.shino.boo/devilreef/cadence/server/internal/storetest"
	"git.shino.boo/devilreef/cadence/server/internal/telemetry"
)

var updateHA = flag.Bool("update", false, "rewrite the home assistant component's contract fixtures")

const haFixtures = "../../../integrations/home_assistant/tests/fixtures"

// what a run makes anew (device ids, times) is pinned to these in the fixtures
const (
	fixtureKitchen = "0b6f3c52-7d1e-4a8b-9f2c-5e4d3a2b1c0d"
	fixtureHall    = "5a9e7f10-2c3d-4e5f-8a6b-7c8d9e0f1a2b"
	fixtureGroup   = "9c1d2e3f-4a5b-4c6d-8e7f-0a1b2c3d4e5f"
	fixtureAt      = "2026-11-12T10:00:00Z"
	// a catalog session's cover: the server's own image proxy at the public origin, its id the proxy issued
	fixtureOrigin  = "https://cadence.example"
	fixtureArtURL  = "https://art.example.com/covers/rebuilding.png"
	fixtureCoverID = "Zm1Ub0NhdENkNjRWY2hmQ2Fk"
)

// fixtureCover is the image the proxy fetches for fixtureArtURL
var fixtureCover = append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{3}, 400)...)

var fixtureTimes = regexp.MustCompile(`"(at|lastSeenAt|createdAt|updatedAt)":"[^"]*"`)

// the descriptors cadenced reports for these capabilities, copied by hand from device/internal/volume and
// device/internal/media (internal to the device module, so not importable here): a device-side schema change does
// not fail the drift check, which holds the server's answers, not the device's descriptors; copy it again then
var fixtureReport = []*cadencev2.Descriptor{
	{
		Id: "audio.output", Version: 1, ConfigSchema: `{"type":"object","properties":{},"additionalProperties":false}`,
		StateSchema: `{"type":"object","properties":{"level":{"type":"integer"},"channels":{"type":"integer"},"maxBitrate":{"type":"integer"}},"required":["level","channels","maxBitrate"]}`,
		Commands: map[string]string{
			"set_level":    `{"type":"object","properties":{"level":{"type":"integer","minimum":0,"maximum":100},"sessionId":{"type":"string","minLength":1,"maxLength":63}},"required":["level"],"additionalProperties":false}`,
			"change_level": `{"type":"object","properties":{"delta":{"type":"integer","minimum":-100,"maximum":100},"sessionId":{"type":"string","minLength":1,"maxLength":63}},"required":["delta"],"additionalProperties":false}`,
		},
	},
	{
		Id: "media.player", Version: 1,
		ConfigSchema: `{"type":"object","properties":{"duckMode":{"enum":["duck","muffle"]}},"additionalProperties":false}`,
		StateSchema:  `{"type":"object","properties":{"duckMode":{"enum":["duck","muffle"]}},"required":["duckMode"],"additionalProperties":false}`,
		Commands: map[string]string{
			"pause": `{"type":"object","additionalProperties":false}`, "resume": `{"type":"object","additionalProperties":false}`,
			"stop": `{"type":"object","additionalProperties":false}`, "next": `{"type":"object","additionalProperties":false}`,
			"previous": `{"type":"object","additionalProperties":false}`,
			"seek":     `{"type":"object","properties":{"positionMs":{"type":"integer","minimum":0}},"required":["positionMs"],"additionalProperties":false}`,
		},
	},
	{
		Id: "media.url", Version: 1, ConfigSchema: `{"type":"object","additionalProperties":false}`,
		Commands: map[string]string{"play": `{"type":"object","properties":{"url":{"type":"string","pattern":"^[Hh][Tt][Tt][Pp][Ss]?://.","maxLength":2047},` +
			`"mode":{"enum":["session","overlay"]},"metadata":{"type":"object","properties":{"title":{"type":"string"},"artist":{"type":"string"},` +
			`"album":{"type":"string"},"artworkUrl":{"type":"string","pattern":"^[Hh][Tt][Tt][Pp][Ss]?://.","maxLength":2047}},"additionalProperties":false},` +
			`"startMs":{"type":"integer","minimum":0}},"required":["url"],"additionalProperties":false}`},
	},
}

// fixtureGroupDesc is media.group's descriptor (pkg/capschema), which the kitchen reports once its detail fixtures are
// taken: they stay what the component's tests read
// fixtureAlarmsDesc is the alarms capability as a speaker reports it, its schemas loosened: the device's own are
// device/internal/alarms/schemas.go
var fixtureAlarmsDesc = &cadencev2.Descriptor{
	Id: alarmview.Capability, Version: 1, ConfigSchema: `{"type":"object"}`, StateSchema: `{"type":"object"}`,
	Commands: map[string]string{
		"stop_ring": `{"type":"object","properties":{"kind":{"type":"string"},"id":{"type":"string"}},"additionalProperties":false}`,
		"snooze": `{"type":"object","properties":{"kind":{"type":"string"},"id":{"type":"string"},"minutes":{"type":"integer"}},` +
			`"additionalProperties":false}`,
		"set_timer": `{"type":"object","properties":{"sessionId":{"type":"string"},"id":{"type":"string"},"seconds":{"type":"integer"}},` +
			`"required":["sessionId","id","seconds"],"additionalProperties":false}`,
	},
}

// the alarms states the fixture's kitchen reports: an alarm, a timer and a reminder, then the alarm ringing, then
// snoozed
const (
	fixtureAlarms = `{"alarms":[{"id":"a1","time":"07:30","days":["mon","tue","wed","thu","fri"],"level":40,` +
		`"next":"2026-11-13T07:30:00Z"}],"timers":[{"id":"t1","seconds":600,"endsAt":"2026-11-12T10:10:00Z",` +
		`"next":"2026-11-12T10:10:00Z"}],"reminders":[{"id":"r1","time":"18:00","date":"2026-11-12","label":"Take the bins out",` +
		`"next":"2026-11-12T18:00:00Z"}],"ringing":null,"snoozed":[],"lastRing":null}`
	fixtureAlarmsSnoozed = `{"alarms":[{"id":"a1","time":"07:30","days":["mon","tue","wed","thu","fri"],"level":40,` +
		`"next":"2026-11-13T07:39:00Z"}],"timers":[{"id":"t1","seconds":600,"endsAt":"2026-11-12T10:10:00Z",` +
		`"next":"2026-11-12T10:10:00Z"}],"reminders":[{"id":"r1","time":"18:00","date":"2026-11-12","label":"Take the bins out",` +
		`"next":"2026-11-12T18:00:00Z"}],"ringing":null,"snoozed":[{"kind":"alarm","id":"a1","until":"2026-11-13T07:39:00Z"}],` +
		`"lastRing":{"kind":"alarm","id":"a1","at":"2026-11-13T07:30:20Z","stoppedBy":"snooze"}}`
)

var fixtureGroupDesc = &cadencev2.Descriptor{
	Id: capschema.MediaGroupID, Version: 1, ConfigSchema: capschema.MediaGroupConfig, StateSchema: capschema.MediaGroupState,
	Secrets: []*cadencev2.SecretSpec{{Name: capschema.MediaGroupKeySecret, Description: "the group key"}},
}

// haCatalogSession is the fixture's current-session owner, a screenplay tracker in the app.
type haCatalogSession struct{ current catalog.SessionState }

func (s *haCatalogSession) Current(string) (catalog.SessionState, bool) {
	return s.current, s.current.Ref != ""
}

// fixtureCatalog is a real catalog service over one fake source, with a title's voiceover and quality choices.
func fixtureCatalog(t *testing.T) (*catalog.Service, catalog.Ref) {
	t.Helper()
	provider := &catalogtest.Fake{
		Provider:   catalog.ProviderTorrplayer,
		Caps:       catalog.Capabilities{catalog.CapBrowse, catalog.CapItem, catalog.CapOptions, catalog.CapResolve},
		SourceList: []catalog.SourceInfo{{ID: "anime365", Name: "anime365", Enabled: true}},
	}
	key := catalog.Key{Source: "anime365", Kind: catalog.KindSeries, ID: "fixture-title"}
	key = provider.Add(catalogtest.Entry{
		Item: catalog.Item{Key: key, Title: "Rebuilding a World", Show: &catalog.ShowInfo{Year: 2023, Format: "TV"}},
		Groups: []catalog.ChoiceGroup{
			{Kind: catalog.GroupVoiceover, Label: "Voiceover", Choices: []catalog.Choice{
				{ID: "AniLibria", Label: "AniLibria", Stream: &catalog.StreamSource{Quality: 1080}},
				{ID: "Crunchyroll", Label: "Crunchyroll", Stream: &catalog.StreamSource{Quality: 720}},
				{ID: "dead", Label: "Unavailable dub", Unavailable: true, Reason: catalog.FailNoSeeders},
			}},
			{Kind: catalog.GroupQuality, Label: "Quality", Choices: []catalog.Choice{
				{ID: "1080", Label: "1080p"}, {ID: "720", Label: "720p"},
			}},
		},
		Play: &catalog.Playable{Kind: catalog.PlayStream, Stream: &catalog.Stream{URL: "https://media.example/episode"}},
	})
	provider.Feeds = map[string][]catalog.Key{catalog.FeedContinue: {key}}
	svc := catalog.New(catalog.Deps{Providers: func(context.Context) []catalog.Configured {
		return []catalog.Configured{{Instance: "anime", Provider: provider}}
	}})
	page, err := svc.Browse(context.Background(), catalog.Household, "torrplayer/continue", "", nil, 0)
	if err != nil || len(page.Sections) != 1 || len(page.Sections[0].Items) != 1 {
		t.Fatalf("fixture catalog browse: %+v %v", page, err)
	}
	return svc, page.Sections[0].Items[0].Ref
}

// haRig is the app's device link, admin operations and live feed on one store,
// wired as Build wires them for what an ha token reaches
type haRig struct {
	t        *testing.T
	srv      *fiber.App
	enrolled *enroll.Service
	vitals   *telemetry.VitalsTracker
	images   *images.Proxy
	catalog  *haCatalogSession
	catRef   catalog.Ref
	groups   *groups.Service
	caps     *devices.Service
	addr     string
	ids      map[string]string // real id -> fixture id
}

func newHARig(t *testing.T) *haRig {
	t.Helper()
	pool := storetest.Pool(t)
	log := slog.New(slog.DiscardHandler)
	enrolled := enroll.NewService(db.New(pool), time.Now)
	bus := events.NewBus(64)
	enrolled.PublishTo(bus)
	hub := gateway.NewHub()
	box, err := secrets.New(bytes.Repeat([]byte{9}, 32))
	if err != nil {
		t.Fatal(err)
	}
	caps := devices.NewService(pool, hub, box, log)
	caps.PublishTo(bus)
	multiroom := groups.New(groups.Deps{Store: pool, Box: box, Devices: caps, List: groupDevices(enrolled.List), Online: hub.Online,
		Bus: bus, Log: log})
	caps.OnState(multiroom.StateChanged)
	caps.OnState(alarmview.NewWatcher(alarmview.Deps{Publish: bus.Publish, Now: func() time.Time {
		return time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	}}).StateChanged)
	caps.ShowGroups(multiroom.Refs)
	enrolled.ShowGroups(multiroom.Refs)
	sessions := media.NewStore(bus, log)
	vitals := telemetry.NewVitals(telemetry.VitalsDeps{Bus: bus})
	cmds := commands.New(caps, log)
	feed := events.NewFeed(bus, log)
	artwork := media.NewArtwork()
	// the fixture catalog session's cover, as a screen's session names it: the server's own image proxy
	imageProxy := images.New(images.Config{Public: func(rawURL string) (string, []byte, error) {
		if rawURL == fixtureArtURL {
			return "image/png", fixtureCover, nil
		}
		return "", nil, images.ErrUnavailable
	}})
	artwork.Own = ownImages(fixtureOrigin, imageProxy)
	catalogSvc, catRef := fixtureCatalog(t)
	catalogSessions := &haCatalogSession{}
	link := deviceLink(linkDeps{
		caps: caps, cmds: cmds, sessions: sessions, voices: fakeVoices{}, seen: enrolled.Seen, presence: caps, groups: multiroom,
		version: "t", log: log,
	})
	srv := api.NewApp(api.Options{
		DB: pool, Log: log, Version: "t", VerifyToken: apitest.Verify, Tags: adminTags,
		Admin: adminOps(adminDeps{
			enrolled: enrolled, online: hub.Online, caps: caps, cmds: cmds, sessions: sessions, artwork: artwork,
			images: imageProxy, catalog: catalogSvc, catalogSessions: catalogSessions,
			telemetry: telemetry.AdminDeps{Reader: telemetry.NewReader(pool), State: caps.State, Vitals: vitals.Latest, Log: log},
			groups:    multiroom,
			log:       log,
		}),
		Plain:  []api.PlainRoute{feed.Route()},
		Device: []func(fiber.Router){func(r fiber.Router) { gateway.Mount(r, hub, enrolled.AuthDevice, link, log) }},
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Listener(ln, fiber.ListenConfig{DisableStartupMessage: true}) }()
	t.Cleanup(func() {
		feed.Close(int(websocket.StatusGoingAway), "")
		hub.Close(int(websocket.StatusGoingAway), "")
		_ = srv.Shutdown()
	})
	return &haRig{t: t, srv: srv, enrolled: enrolled, vitals: vitals, images: imageProxy, catalog: catalogSessions,
		catRef: catRef, groups: multiroom, caps: caps, addr: ln.Addr().String(), ids: map[string]string{}}
}

// enrol creates and enrols a device as cadenced enroll does, its id pinned to fixed
func (r *haRig) enrol(name, fixed string) (id, token string) {
	r.t.Helper()
	ctx := context.Background()
	_, code, err := r.enrolled.CreateDevice(ctx, name, "sim")
	if err != nil {
		r.t.Fatal(err)
	}
	id, token, err = r.enrolled.Enroll(ctx, code, enroll.Fingerprint{Board: "sim"})
	if err != nil {
		r.t.Fatal(err)
	}
	r.ids[id] = fixed
	return id, token
}

// call answers a request with an ha bearer, or an admin one
func (r *haRig) call(method, path, body string, ha bool) (int, []byte) {
	r.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	bearer := "admin-token"
	if ha {
		bearer = apitest.HABearer
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := r.srv.Test(req)
	if err != nil {
		r.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		r.t.Fatal(err)
	}
	return resp.StatusCode, b
}

func (r *haRig) get(path string, want int) []byte {
	r.t.Helper()
	code, b := r.call(http.MethodGet, path, "", true)
	if code != want {
		r.t.Fatalf("GET %s: %d %s", path, code, b)
	}
	return b
}

func (r *haRig) dial(path string, bearer string) *websocket.Conn {
	r.t.Helper()
	ctx, cancel := context.WithTimeout(r.t.Context(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws://"+r.addr+path, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + bearer}},
	})
	if err != nil {
		r.t.Fatal(err)
	}
	r.t.Cleanup(func() { _ = c.CloseNow() })
	return c
}

func (r *haRig) read(c *websocket.Conn) []byte {
	r.t.Helper()
	ctx, cancel := context.WithTimeout(r.t.Context(), 5*time.Second)
	defer cancel()
	_, b, err := c.Read(ctx)
	if err != nil {
		r.t.Fatal(err)
	}
	return b
}

func (r *haRig) write(c *websocket.Conn, typ websocket.MessageType, b []byte) {
	r.t.Helper()
	ctx, cancel := context.WithTimeout(r.t.Context(), 5*time.Second)
	defer cancel()
	if err := c.Write(ctx, typ, b); err != nil {
		r.t.Fatal(err)
	}
}

// send writes an envelope on a device's link
func (r *haRig) send(c *websocket.Conn, env *cadencev2.Envelope) {
	r.t.Helper()
	b, err := wire.Encode(env)
	if err != nil {
		r.t.Fatal(err)
	}
	r.write(c, websocket.MessageBinary, b)
}

// link opens a device's link and greets: it is online once its welcome is read
func (r *haRig) link(token string) *websocket.Conn {
	r.t.Helper()
	c := r.dial("/v2/device", token)
	r.send(c, &cadencev2.Envelope{Body: &cadencev2.Envelope_Hello{Hello: &cadencev2.Hello{Protocol: 2, Board: "sim", LanAddresses: []string{"192.0.2.20"}}}})
	if env, err := wire.Decode(r.read(c)); err != nil || env.GetWelcome() == nil {
		r.t.Fatalf("first frame %v %v", env, err)
	}
	return c
}

// refuseCommand answers the next command a device's link carries with code;
// it runs beside the request that sent the command, so it reports instead of
// failing the test
func (r *haRig) refuseCommand(c *websocket.Conn, code string) error {
	ctx, cancel := context.WithTimeout(r.t.Context(), 5*time.Second)
	defer cancel()
	for {
		_, b, err := c.Read(ctx)
		if err != nil {
			return err
		}
		env, err := wire.Decode(b)
		if err != nil {
			return err
		}
		if cmd := env.GetCapabilityCommand(); cmd != nil {
			out, err := wire.Encode(&cadencev2.Envelope{Body: &cadencev2.Envelope_CommandResult{CommandResult: &cadencev2.CommandResult{
				RequestId: cmd.GetRequestId(), Code: code,
			}}})
			if err != nil {
				return err
			}
			return c.Write(ctx, websocket.MessageBinary, out)
		}
	}
}

// event reads the feed's next frame, which must be of typ
func (r *haRig) event(feed *websocket.Conn, typ string) []byte {
	r.t.Helper()
	b := r.read(feed)
	var f struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(b, &f); err != nil || f.Type != typ {
		r.t.Fatalf("frame %s, want %s", b, typ)
	}
	return b
}

// pinned is b with the run's ids and times replaced by the fixtures' own, indented
func (r *haRig) pinned(b []byte) []byte {
	r.t.Helper()
	for real, fixed := range r.ids {
		b = bytes.ReplaceAll(b, []byte(real), []byte(fixed))
	}
	b = fixtureTimes.ReplaceAll(b, []byte(`"$1":"`+fixtureAt+`"`))
	var out bytes.Buffer
	if err := json.Indent(&out, b, "", "  "); err != nil {
		r.t.Fatalf("%s: %v", b, err)
	}
	out.WriteByte('\n')
	return out.Bytes()
}

func uuidOf(t *testing.T, id string) pgtype.UUID {
	t.Helper()
	var u pgtype.UUID
	if err := u.Scan(id); err != nil {
		t.Fatal(err)
	}
	return u
}

// the Home Assistant component's contract fixtures, from the real link,
// publishers, feed and admin operations an ha token reaches (M8-D10): each one
// as the server writes it, ids and times pinned; -update rewrites them
func TestHAContractFixtures(t *testing.T) {
	r := newHARig(t)
	got := map[string][]byte{}
	keep := func(name string, b []byte) { got[name] = r.pinned(b) }

	feed := r.dial("/api/v1/live", apitest.HABearer)
	keep("feed_open.json", r.event(feed, "subscribed"))
	r.write(feed, websocket.MessageText, []byte(`{"subscribe":["devices","media","vitals","groups","alarms"]}`))
	keep("feed_subscribed.json", r.event(feed, "subscribed"))

	kitchen, token := r.enrol("Kitchen", fixtureKitchen)
	keep("device_added.json", r.event(feed, events.DeviceAdded))
	hall, _ := r.enrol("Hall", fixtureHall)
	r.event(feed, events.DeviceAdded)

	dev := r.link(token)
	keep("device_online.json", r.event(feed, events.DeviceOnline))
	report := &cadencev2.CapabilityReport{Descriptors: append(slices.Clone(fixtureReport), fixtureAlarmsDesc)}
	report.Hash = caphash.Report(report)
	r.send(dev, &cadencev2.Envelope{Body: &cadencev2.Envelope_CapabilityReport{CapabilityReport: report}})
	r.send(dev, &cadencev2.Envelope{Body: &cadencev2.Envelope_StateDelta{StateDelta: &cadencev2.StateDelta{
		Capability: "audio.output", Json: `{"level":35,"channels":2,"maxBitrate":128000}`,
	}}})
	keep("device_volume.json", r.event(feed, events.DeviceVolume))

	volume := uint32(60)
	r.send(dev, &cadencev2.Envelope{Body: &cadencev2.Envelope_MediaSessionState{MediaSessionState: &cadencev2.MediaSessionState{
		SessionId: "url-7f3a", Source: cadencev2.MediaSource_MEDIA_SOURCE_URL, State: cadencev2.PlaybackState_PLAYBACK_STATE_PLAYING,
		PositionMs: 42000, PositionAtMs: time.Date(2026, 11, 12, 9, 59, 58, 250e6, time.UTC).UnixMilli(), Rate: 1,
		DurationMs: 215000, Volume: &volume,
		Metadata: &cadencev2.MediaMetadata{
			Title: "Night Drive", Artist: "The Examples", Album: "Fixtures", ArtworkUrl: "https://art.example.com/covers/night-drive.jpg",
		},
		Actions: []string{"pause", "stop", "seek", "next", "previous"},
	}}})
	keep("media_state.json", r.event(feed, events.MediaState))
	keep("media_playing.json", r.get("/api/v1/devices/"+kitchen+"/media", 200))
	keep("media_idle.json", r.get("/api/v1/devices/"+hall+"/media", 200))

	// a catalog session (an episode a screen plays through the server): its cover is the server's own proxied
	// image, which get-device-artwork answers from the proxy (ownImages), and its metadata names the show, the
	// episode and the provider's source
	coverID := r.images.Ref(fixtureArtURL, false)
	coverURL := fixtureOrigin + "/v2/images/" + coverID
	coverHash := fmt.Sprintf("%x", sha256.Sum256([]byte(coverURL)))
	r.ids[coverID] = fixtureCoverID
	r.ids[coverHash] = fmt.Sprintf("%x", sha256.Sum256([]byte(fixtureOrigin+"/v2/images/"+fixtureCoverID)))
	r.send(dev, &cadencev2.Envelope{Body: &cadencev2.Envelope_MediaSessionState{MediaSessionState: &cadencev2.MediaSessionState{
		SessionId: "video-31", Source: cadencev2.MediaSource_MEDIA_SOURCE_VIDEO, State: cadencev2.PlaybackState_PLAYBACK_STATE_PAUSED,
		PositionMs: 79000, PositionAtMs: time.Date(2026, 11, 12, 9, 59, 30, 0, time.UTC).UnixMilli(), Rate: 1,
		DurationMs: 1422000, Volume: &volume,
		Metadata: &cadencev2.MediaMetadata{
			Title: "Rebuilding a World", Series: "Rebuilding a World", Kind: cadencev2.MediaKind_MEDIA_KIND_EPISODE,
			Episode: "7", EpisodeName: "The Guild Advisor", ArtworkUrl: coverURL,
			Provider: &cadencev2.MediaProvider{Id: "torrplayer", Name: "torrplayer", Source: "anime365"},
		},
		Actions: []string{"pause", "resume", "stop", "seek", "next"},
	}}})
	keep("media_catalog_state.json", r.event(feed, events.MediaState))
	keep("media_catalog.json", r.get("/api/v1/devices/"+kitchen+"/media", 200))
	// the current title's resolved choices and the options Home Assistant's source and sound-mode selectors show
	r.ids[string(r.catRef)] = "c:series"
	r.catalog.current = catalog.SessionState{SessionID: "video-31", Ref: r.catRef, Episode: "7",
		Choices: catalog.Selection{catalog.GroupVoiceover: "AniLibria", catalog.GroupQuality: "1080"}}
	keep("catalog_session.json", r.get("/api/v1/devices/"+kitchen+"/catalog/session", 200))
	// the session's cover by its hash: the proxy's image, byte for byte
	code, art := r.call(http.MethodGet, "/api/v1/devices/"+kitchen+"/artwork?h="+coverHash, "", true)
	if code != http.StatusOK || !bytes.Equal(art, fixtureCover) {
		t.Fatalf("catalog cover: %d %d bytes, want the proxy's image", code, len(art))
	}

	at := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	id := uuidOf(t, kitchen)
	point := func(name, attrs string, v float64) telemetry.MetricRow {
		return telemetry.MetricRow{DeviceID: id, Name: name, Kind: telemetry.KindGauge, Attrs: []byte(attrs), Time: at, Value: v}
	}
	r.vitals.Observe([]telemetry.MetricRow{
		point("system.cpu.utilization", `{}`, 0.125),
		point("system.memory.used", `{}`, 412_000_000),
		point("system.memory.limit", `{}`, 1_024_000_000),
		point("system.filesystem.limit", `{"mountpoint":"/data"}`, 8_000_000_000),
		point("system.filesystem.free", `{"mountpoint":"/data"}`, 6_000_000_000),
		point("system.filesystem.limit", `{"mountpoint":"/tmp"}`, 256_000_000),
		point("system.filesystem.free", `{"mountpoint":"/tmp"}`, 200_000_000),
		point("system.temperature", `{}`, 48.5),
	})
	keep("device_vitals.json", r.event(feed, events.DeviceVitals))
	keep("vitals.json", r.get("/api/v1/devices/"+kitchen+"/vitals", 200))
	keep("vitals_none.json", r.get("/api/v1/devices/"+hall+"/vitals", 404))

	// the kitchen's alarms: its state, a ring and its snooze, the reminder's label the owner's own setting
	alarms := func(state string) {
		r.send(dev, &cadencev2.Envelope{Body: &cadencev2.Envelope_StateDelta{StateDelta: &cadencev2.StateDelta{
			Capability: alarmview.Capability, Json: state,
		}}})
	}
	alarms(fixtureAlarms)
	keep("alarms_state.json", r.event(feed, events.AlarmsState))
	alarms(strings.Replace(fixtureAlarms, `"ringing":null`, `"ringing":{"kind":"alarm","id":"a1"}`, 1))
	r.event(feed, events.AlarmsState)
	keep("alarm_ringing.json", r.event(feed, events.AlarmRinging))
	alarms(fixtureAlarmsSnoozed)
	r.event(feed, events.AlarmsState)
	keep("alarm_ring_ended.json", r.event(feed, events.AlarmRingEnded))

	keep("devices.json", r.get("/api/v1/devices", 200))
	keep("device_kitchen.json", r.get("/api/v1/devices/"+kitchen, 200))
	keep("device_hall.json", r.get("/api/v1/devices/"+hall, 200))

	// Home Assistant's join: the kitchen, now reporting media.group, leads a new group with the hall, named after it;
	// the hall is offline, so the kitchen is elected and streams to nobody yet
	grouped := &cadencev2.CapabilityReport{Descriptors: append(slices.Clone(fixtureReport), fixtureAlarmsDesc, fixtureGroupDesc)}
	grouped.Hash = caphash.Report(grouped)
	r.send(dev, &cadencev2.Envelope{Body: &cadencev2.Envelope_CapabilityReport{CapabilityReport: grouped}})
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if _, err := r.caps.Desired(context.Background(), kitchen, capschema.MediaGroupID); err == nil {
			break
		} else if time.Now().After(deadline) {
			t.Fatalf("media.group not reported: %v", err)
		}
	}
	code, b := r.call(http.MethodPost, "/api/v1/devices/"+kitchen+"/group", `{"members":["`+hall+`"]}`, true)
	if code != http.StatusOK {
		t.Fatalf("join %d %s", code, b)
	}
	var joined struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(b, &joined); err != nil {
		t.Fatal(err)
	}
	r.ids[joined.ID] = fixtureGroup
	keep("group_joined.json", b)
	keep("group_changed.json", r.event(feed, events.GroupChanged))
	r.send(dev, &cadencev2.Envelope{Body: &cadencev2.Envelope_StateDelta{StateDelta: &cadencev2.StateDelta{
		Capability: capschema.MediaGroupID,
		Json: `{"groupId":"` + joined.ID + `","role":"leader","state":"idle","synced":true,"clockErrorUs":0,"queueMs":0,` +
			`"steerPpm":0,"rejoins":0,"underruns":0,"placementErrorUs":0,"latencyMs":1000,"members":[{"deviceId":"` + hall +
			`","state":"connecting","connected":false,"synced":false,"clockErrorUs":0,"queueMs":0,"rejoins":0,"placementErrorUs":0}]}`,
	}}})
	keep("group_state.json", r.event(feed, events.GroupState))
	keep("groups.json", r.get("/api/v1/groups", 200))
	keep("group.json", r.get("/api/v1/groups/"+joined.ID, 200))
	keep("devices_grouped.json", r.get("/api/v1/devices", 200))
	// the hall leaves: one member left, the group is deleted
	if code, b := r.call(http.MethodDelete, "/api/v1/devices/"+hall+"/group", "", true); code != http.StatusNoContent {
		t.Fatalf("leave %d %s", code, b)
	}
	keep("group_deleted.json", r.event(feed, events.GroupChanged))

	r.send(dev, &cadencev2.Envelope{Body: &cadencev2.Envelope_MediaSessionState{MediaSessionState: &cadencev2.MediaSessionState{
		State: cadencev2.PlaybackState_PLAYBACK_STATE_IDLE,
	}}})
	keep("media_state_idle.json", r.event(feed, events.MediaState))

	// with no session the device refuses a skip with its own code, which the
	// error model carries as its detail
	answered := make(chan error, 1)
	go func() { answered <- r.refuseCommand(dev, "no_session") }()
	code, b = r.call(http.MethodPost, "/api/v1/devices/"+kitchen+"/capabilities/media.player/commands/next", `{}`, true)
	if err := <-answered; err != nil || code != http.StatusConflict {
		t.Fatalf("refused command %d %s %v", code, b, err)
	}
	keep("command_failed.json", b)

	if code, b := r.call(http.MethodPatch, "/api/v1/devices/"+kitchen, `{"name":"Living room"}`, false); code != 200 {
		t.Fatalf("rename %d %s", code, b)
	}
	keep("device_renamed.json", r.event(feed, events.DeviceRenamed))
	_ = dev.Close(websocket.StatusNormalClosure, "")
	keep("device_offline.json", r.event(feed, events.DeviceOffline))
	if code, b := r.call(http.MethodDelete, "/api/v1/devices/"+hall, "", false); code != 204 {
		t.Fatalf("delete %d %s", code, b)
	}
	keep("device_deleted.json", r.event(feed, events.DeviceDeleted))

	checkFixtures(t, got)
}

// checkFixtures holds the committed fixtures to got, or rewrites them with -update
func checkFixtures(t *testing.T, got map[string][]byte) {
	t.Helper()
	names := make([]string, 0, len(got))
	for name := range got {
		names = append(names, name)
	}
	slices.Sort(names)
	committed, err := filepath.Glob(filepath.Join(haFixtures, "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if *updateHA {
		if err := os.MkdirAll(haFixtures, 0o755); err != nil {
			t.Fatal(err)
		}
		for _, path := range committed {
			if _, ok := got[filepath.Base(path)]; !ok {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
		}
		for _, name := range names {
			if err := os.WriteFile(filepath.Join(haFixtures, name), got[name], 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return
	}
	for _, path := range committed {
		if _, ok := got[filepath.Base(path)]; !ok {
			t.Errorf("%s is no fixture the server makes: run go test ./server/internal/app -run TestHAContractFixtures -update", path)
		}
	}
	for _, name := range names {
		b, err := os.ReadFile(filepath.Join(haFixtures, name))
		if err != nil || !bytes.Equal(b, got[name]) {
			t.Errorf("%s drifted from what the server writes (%v): run go test ./server/internal/app -run TestHAContractFixtures -update\nserver:\n%s",
				name, err, got[name])
		}
	}
}
