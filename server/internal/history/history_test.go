package history_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"git.shino.boo/devilreef/cadence/server/internal/api"
	"git.shino.boo/devilreef/cadence/server/internal/api/apitest"
	"git.shino.boo/devilreef/cadence/server/internal/history"
	"git.shino.boo/devilreef/cadence/server/internal/media"
	"git.shino.boo/devilreef/cadence/server/internal/store/db"
	"git.shino.boo/devilreef/cadence/server/internal/storetest"
)

type rig struct {
	t    *testing.T
	pool *pgxpool.Pool
	s    *history.Service
	app  *fiber.App
	logs *bytes.Buffer
	dev  string
	now  time.Time
}

func newRig(t *testing.T) *rig {
	t.Helper()
	pool := storetest.Pool(t)
	q := db.New(pool)
	dev, err := q.CreateDevice(context.Background(), db.CreateDeviceParams{Name: "Kitchen", Model: "sim"})
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{t: t, pool: pool, logs: &bytes.Buffer{}, dev: uuid.UUID(dev.ID.Bytes).String(),
		now: time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)}
	r.s = history.New(q, slog.New(slog.NewTextHandler(r.logs, nil)), func() time.Time { return r.now })
	r.app = api.NewApp(api.Options{
		DB: noPing{}, Version: "t", VerifyToken: apitest.Verify,
		Admin: []func(huma.API){func(a huma.API) { history.RegisterAdmin(a, r.s, slog.New(slog.NewTextHandler(r.logs, nil))) }},
	})
	return r
}

type noPing struct{}

func (noPing) Ping(context.Context) error { return nil }

func session(id, title string) media.MediaSession {
	return media.MediaSession{SessionID: id, Source: "url", State: "playing", Provider: "YouTube Music",
		Metadata: media.MediaMetadata{Title: title, Artist: "Artist", Album: "Album", Kind: "track",
			ArtworkURL: "https://img.example/cover.jpg"}}
}

func (r *rig) observe(dev string, sess *media.MediaSession) {
	r.t.Helper()
	r.s.Observe(dev, nil, sess)
}

func (r *rig) play(dev, id, title string, duration time.Duration) history.HistoryEntry {
	r.t.Helper()
	sess := session(id, title)
	r.observe(dev, &sess)
	r.now = r.now.Add(duration)
	r.observe(dev, nil)
	page, err := r.s.List(context.Background(), dev, "", 1)
	if err != nil || len(page.Items) != 1 {
		r.t.Fatalf("list after play: %+v %v", page, err)
	}
	return page.Items[0]
}

func TestHistoryRecordsPlayingTimeAndCollapsesQuickRepeats(t *testing.T) {
	r := newRig(t)
	sess := session("s1", "Song")
	sess.ProviderSongID = "5qZQEq_C3vc"
	sess.ProviderInstance = "ytm"
	sess.WebURL = "https://music.youtube.com/watch?v=5qZQEq_C3vc&signature=do-not-store"
	sess.Metadata.ArtworkURL = "https://img.example/cover.jpg?sig=do-not-store"
	r.s.Attribute(r.dev, sess.SessionID, history.Attribution{TurnID: "turn-1", ItemID: "track-1", Source: "music",
		Provider: "ytmusic", Kind: "track", Title: "Song", Artist: "Artist", Album: "Album"})
	started := r.now
	r.observe(r.dev, &sess)
	r.now = r.now.Add(4 * time.Second)
	paused := media.MediaSession{SessionID: "s1", Source: "url", State: "paused", Provider: "YouTube Music"}
	r.observe(r.dev, &paused)
	r.now = r.now.Add(3 * time.Second)
	r.observe(r.dev, &sess)
	r.now = r.now.Add(2 * time.Second)
	r.observe(r.dev, nil)

	page, err := r.s.List(context.Background(), r.dev, "", 10)
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("page %+v %v", page, err)
	}
	got := page.Items[0]
	if got.PlayedMS != 6000 || !got.StartedAt.Equal(started) || got.EndedAt == nil || !got.EndedAt.Equal(r.now) ||
		got.TurnID != "turn-1" || got.CatalogID != "track-1" || got.Source != "music" || got.Provider != "ytmusic" ||
		got.ProviderSongID != "5qZQEq_C3vc" || got.WebURL != "https://music.youtube.com/watch?v=5qZQEq_C3vc" ||
		got.Feedback != "none" || got.Favorite || got.ArtworkURL != "" || got.ArtworkHash != "" {
		t.Fatalf("entry %+v", got)
	}

	// a durable like appears on the existing play without changing its timing
	if _, err := db.New(r.pool).UpsertMusicSongState(context.Background(), db.UpsertMusicSongStateParams{
		Provider: "ytmusic", ProviderSongID: "5qZQEq_C3vc", Feedback: "like",
		Now: pgtype.Timestamptz{Time: r.now, Valid: true},
	}); err != nil {
		t.Fatal(err)
	}
	page, err = r.s.List(context.Background(), r.dev, "", 1)
	if err != nil || len(page.Items) != 1 || page.Items[0].Feedback != "like" || !page.Items[0].Favorite ||
		page.Items[0].PlayedMS != 6000 {
		t.Fatalf("feedback on history %+v, err %v", page, err)
	}

	// five seconds later the same item continues the row, without paused time
	r.now = r.now.Add(5 * time.Second)
	repeat := session("s2", "Song")
	r.s.Attribute(r.dev, repeat.SessionID, history.Attribution{TurnID: "turn-1", ItemID: "track-1", Source: "music",
		Provider: "ytmusic", Kind: "track", Title: "Song", Artist: "Artist", Album: "Album"})
	r.observe(r.dev, &repeat)
	r.now = r.now.Add(2 * time.Second)
	r.observe(r.dev, nil)
	page, err = r.s.List(context.Background(), r.dev, "", 10)
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != got.ID || page.Items[0].PlayedMS != 8000 ||
		!page.Items[0].StartedAt.Equal(started) {
		t.Fatalf("collapsed page %+v %v", page, err)
	}

	// outside the 30-second window it is a new play
	r.now = r.now.Add(31 * time.Second)
	r.play(r.dev, "s3", "Song", time.Second)
	page, err = r.s.List(context.Background(), r.dev, "", 10)
	if err != nil || len(page.Items) != 2 {
		t.Fatalf("later repeat %+v %v", page, err)
	}
	if logs := r.logs.String(); strings.Contains(logs, "Song") || strings.Contains(logs, "do-not-store") {
		t.Fatalf("metadata reached logs: %s", logs)
	}
}

func TestHistoryFollowsTracksWithinAnAirPlaySession(t *testing.T) {
	r := newRig(t)
	hash := strings.Repeat("a", 64)
	first := media.MediaSession{SessionID: "cast", Source: "airplay", State: "playing",
		Metadata: media.MediaMetadata{Title: "One", Artist: "A", ArtworkURL: "https://img.example/one.jpg", ArtworkHash: hash}}
	r.observe(r.dev, &first)
	r.now = r.now.Add(time.Second)
	second := first
	second.Metadata.Title, second.Metadata.ArtworkURL = "Two", "https://img.example/two.jpg?token=do-not-store"
	r.observe(r.dev, &second)
	r.now = r.now.Add(2 * time.Second)
	r.observe(r.dev, nil)
	page, err := r.s.List(context.Background(), r.dev, "", 10)
	if err != nil || len(page.Items) != 2 {
		t.Fatalf("page %+v %v", page, err)
	}
	if page.Items[0].Title != "Two" || page.Items[1].Title != "One" || page.Items[0].PlayedMS != 2000 ||
		page.Items[0].ArtworkHash != hash || page.Items[0].ArtworkURL != "" || page.Items[0].Kind != "track" {
		t.Fatalf("entries %+v", page.Items)
	}
}

func TestHistoryAPIFiltersPagesAndDeletes(t *testing.T) {
	r := newRig(t)
	other, err := db.New(r.pool).CreateDevice(context.Background(), db.CreateDeviceParams{Name: "TV", Model: "sim-tv"})
	if err != nil {
		t.Fatal(err)
	}
	otherID := uuid.UUID(other.ID.Bytes).String()
	r.play(r.dev, "s1", "One", time.Second)
	r.now = r.now.Add(time.Second)
	r.play(r.dev, "s2", "Two", time.Second)
	r.now = r.now.Add(time.Second)
	third := r.play(otherID, "s3", "Three", time.Second)

	code, body := apitest.Do(t, r.app, "GET", "/api/v1/history?limit=2", "x")
	if code != 200 {
		t.Fatalf("list %d %s", code, body)
	}
	var page history.HistoryPage
	if err := json.Unmarshal([]byte(body), &page); err != nil || len(page.Items) != 2 || page.Next == "" ||
		page.Items[0].ID != third.ID || page.Items[0].DeviceName != "TV" {
		t.Fatalf("page %s %v", body, err)
	}
	code, body = apitest.Do(t, r.app, "GET", "/api/v1/history?deviceId="+r.dev+"&limit=1&before="+page.Next, "x")
	if code != 200 {
		t.Fatalf("next %d %s", code, body)
	}
	if err := json.Unmarshal([]byte(body), &page); err != nil || len(page.Items) != 1 || page.Items[0].Title != "One" {
		t.Fatalf("filtered next %s %v", body, err)
	}
	if code, _ := apitest.Do(t, r.app, "GET", "/api/v1/history?before=nope", "x"); code != 422 {
		t.Fatalf("cursor status %d", code)
	}
	if code, _ := apitest.Do(t, r.app, "GET", "/api/v1/history?deviceId=nope", "x"); code != 422 {
		t.Fatalf("device status %d", code)
	}
	if code, _ := apitest.Do(t, r.app, "DELETE", "/api/v1/history/"+strconv.FormatInt(third.ID, 10), "x"); code != 204 {
		t.Fatalf("delete status %d", code)
	}
	if code, _ := apitest.Do(t, r.app, "DELETE", "/api/v1/history/"+strconv.FormatInt(third.ID, 10), "x"); code != 404 {
		t.Fatalf("missing delete status %d", code)
	}
}

func TestHistoryRetention(t *testing.T) {
	r := newRig(t)
	old := r.play(r.dev, "old", "Old", time.Second)
	r.play(r.dev, "new", "New", time.Second)
	if _, err := r.pool.Exec(context.Background(), `UPDATE play_history SET started_at = $1 WHERE id = $2`,
		r.now.Add(-15*24*time.Hour), old.ID); err != nil {
		t.Fatal(err)
	}
	stop := r.s.RunRetention(context.Background(), slog.New(slog.NewTextHandler(r.logs, nil)), time.Hour)
	defer stop()
	deadline := time.Now().Add(2 * time.Second)
	for {
		page, err := r.s.List(context.Background(), "", "", 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) == 1 && page.Items[0].Title == "New" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("retention left %+v", page.Items)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// a multiroom member's session is its leader's stream: recorded once, on the leader
func TestHistorySkipsAMultiroomMembersStream(t *testing.T) {
	r := newRig(t)
	sess := session("g", "Song")
	sess.Source = "group"
	r.observe(r.dev, &sess)
	r.now = r.now.Add(time.Minute)
	r.observe(r.dev, nil)
	page, err := r.s.List(context.Background(), r.dev, "", 10)
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("page %+v %v", page, err)
	}
}
