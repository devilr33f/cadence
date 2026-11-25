package commands_test

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"git.shino.boo/devilreef/cadence/pkg/caphash"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/server/internal/api"
	"git.shino.boo/devilreef/cadence/server/internal/api/apitest"
	"git.shino.boo/devilreef/cadence/server/internal/commands"
	"git.shino.boo/devilreef/cadence/server/internal/devices"
	"git.shino.boo/devilreef/cadence/server/internal/events"
	"git.shino.boo/devilreef/cadence/server/internal/gateway"
	"git.shino.boo/devilreef/cadence/server/internal/media"
	"git.shino.boo/devilreef/cadence/server/internal/store/db"
	"git.shino.boo/devilreef/cadence/server/internal/storetest"
)

// the device's media.url play schema
const (
	webURLSchema  = `{"type":"string","pattern":"^[Hh][Tt][Tt][Pp][Ss]?://.","maxLength":2047}`
	urlPlaySchema = `{"type":"object","properties":{"url":` + webURLSchema + `,"mode":{"enum":["session","overlay"]},` +
		`"metadata":{"type":"object","properties":{"title":{"type":"string"},"artist":{"type":"string"},` +
		`"album":{"type":"string"},"artworkUrl":` + webURLSchema + `},"additionalProperties":false},` +
		`"startMs":{"type":"integer","minimum":0}},"required":["url"],"additionalProperties":false}`
)

var private = []string{
	"https://media.example/private-track.mp3", "Private Title", "Private Artist", "Private Album",
	"https://art.example/private-cover.jpg",
}

// guard (rule 8, D11): a play command's url and metadata reach the device
// only, and a media state's metadata the admin api only: never a log line or a row
func TestPlayAndMediaStateLeaveNoURLOrMetadata(t *testing.T) {
	pool := storetest.Pool(t)
	q := db.New(pool)
	ctx := context.Background()
	row, err := q.CreateDevice(ctx, db.CreateDeviceParams{Name: "kitchen", Model: "sim"})
	if err != nil {
		t.Fatal(err)
	}
	dev := row.ID.String()
	logs := &lockedBuffer{}
	log := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	caps := devices.NewService(pool, gateway.NewHub(), nil, log)
	r := &cadencev2.CapabilityReport{Descriptors: []*cadencev2.Descriptor{{
		Id: "media.url", Version: 1, ConfigSchema: noArgs, Commands: map[string]string{"play": urlPlaySchema},
	}}}
	r.Hash = caphash.Report(r)
	caps.Handle(ctx, dev, &cadencev2.Envelope{Body: &cadencev2.Envelope_CapabilityReport{CapabilityReport: r}})

	svc := commands.New(caps, log)
	sessions := media.NewStore(events.NewBus(0), log)
	conn := &linkConn{id: dev, svc: svc}
	svc.Linked(ctx, conn)
	app := api.NewApp(api.Options{
		DB: noPing{}, Version: "t", Log: log,
		VerifyToken: apitest.Verify,
		Admin: []func(huma.API){
			func(a huma.API) { commands.RegisterAdmin(a, svc) },
			func(a huma.API) { media.RegisterAdmin(a, sessions, caps, nil) },
		},
	})
	e := &env{app: app}
	body := `{"url":"` + private[0] + `","metadata":{"title":"` + private[1] + `","artist":"` + private[2] +
		`","album":"` + private[3] + `","artworkUrl":"` + private[4] + `"}}`
	code, got := e.post(t, "/api/v1/devices/"+dev+"/capabilities/media.url/commands/play", body)
	if code != 200 || got != `{"result":null}` {
		t.Fatalf("%d %s", code, got)
	}
	if conn.json == "" || !strings.Contains(conn.json, private[0]) {
		t.Fatal("the device did not get the url")
	}
	// a failing play is answered by code only
	conn.code = "http_error"
	if code, got := e.post(t, "/api/v1/devices/"+dev+"/capabilities/media.url/commands/play", body); code != 409 {
		t.Fatalf("%d %s", code, got)
	}

	state := &cadencev2.MediaSessionState{
		SessionId: "ms-1", Source: cadencev2.MediaSource_MEDIA_SOURCE_URL, State: cadencev2.PlaybackState_PLAYBACK_STATE_PLAYING,
		Metadata: &cadencev2.MediaMetadata{Title: private[1], Artist: private[2], Album: private[3], ArtworkUrl: private[4]},
	}
	sessions.Handle(dev, &cadencev2.Envelope{Body: &cadencev2.Envelope_MediaSessionState{MediaSessionState: state}})
	if code, got := e.get(t, "/api/v1/devices/"+dev+"/media"); code != 200 || !strings.Contains(got, private[1]) {
		t.Fatalf("%d %s", code, got)
	}
	sessions.Closed(dev)
	assertNoLeak(t, pool, logs.String())
}

// linkConn answers each command at once with code; a failure carries the args
// back as its json, which the server must not pass on
type linkConn struct {
	id   string
	svc  *commands.Service
	json string
	code string
}

func (c *linkConn) DeviceID() string { return c.id }

func (c *linkConn) Send(env *cadencev2.Envelope) error {
	cmd := env.GetCapabilityCommand()
	c.json = cmd.GetJson()
	res := &cadencev2.CommandResult{RequestId: cmd.GetRequestId(), Code: c.code}
	if c.code != "" {
		res.Json = cmd.GetJson()
	}
	go c.svc.Handle(c, result(res))
	return nil
}

// assertNoLeak fails when a private string is in the log or in any row of any table.
func assertNoLeak(t *testing.T, pool *pgxpool.Pool, logs string) {
	t.Helper()
	for _, s := range private {
		if strings.Contains(logs, s) {
			t.Fatalf("log carries a private string: %s", logs)
		}
	}
	ctx := context.Background()
	rows, err := pool.Query(ctx, `SELECT table_name FROM information_schema.tables WHERE table_schema = 'public' AND table_type = 'BASE TABLE'`)
	if err != nil {
		t.Fatal(err)
	}
	tables, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil || len(tables) == 0 {
		t.Fatal(tables, err)
	}
	for _, table := range tables {
		var dump string
		q := `SELECT coalesce(string_agg(t::text, E'\n'), '') FROM ` + pgx.Identifier{table}.Sanitize() + ` t`
		if err := pool.QueryRow(ctx, q).Scan(&dump); err != nil {
			t.Fatal(table, err)
		}
		for _, s := range private {
			if strings.Contains(dump, s) {
				t.Fatalf("table %s carries a private string", table)
			}
		}
	}
}
