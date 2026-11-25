package ringsounds_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"git.shino.boo/devilreef/cadence/server/internal/api"
	"git.shino.boo/devilreef/cadence/server/internal/audio"
	"git.shino.boo/devilreef/cadence/server/internal/auth"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/ringsounds"
	"git.shino.boo/devilreef/cadence/server/internal/store/db"
)

const (
	devA = "11111111-1111-4111-8111-111111111111"
	devB = "22222222-2222-4222-8222-222222222222"
)

// memQueries is the ring_sounds table in memory
type memQueries struct {
	mu   sync.Mutex
	rows []memRow
}

type memRow struct {
	id, kind, key, title string
	dev                  pgtype.UUID
	audio                []byte
	used                 time.Time
}

func (q *memQueries) PutRingSound(_ context.Context, a db.PutRingSoundParams) (string, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i, r := range q.rows {
		if r.dev == a.DeviceID && r.kind == a.Kind && r.key == a.SourceKey {
			q.rows[i].audio, q.rows[i].title, q.rows[i].used = a.Audio, a.Title, a.Now.Time
			return r.id, nil
		}
	}
	q.rows = append(q.rows, memRow{id: a.ID, kind: a.Kind, key: a.SourceKey, title: a.Title, dev: a.DeviceID, audio: a.Audio, used: a.Now.Time})
	return a.ID, nil
}

func (q *memQueries) RingSoundBySource(_ context.Context, a db.RingSoundBySourceParams) (db.RingSoundBySourceRow, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, r := range q.rows {
		if r.dev == a.DeviceID && r.kind == a.Kind && r.key == a.SourceKey {
			return db.RingSoundBySourceRow{ID: r.id, Title: r.title}, nil
		}
	}
	return db.RingSoundBySourceRow{}, pgx.ErrNoRows
}

func (q *memQueries) GetRingSound(_ context.Context, a db.GetRingSoundParams) (db.GetRingSoundRow, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, r := range q.rows {
		if r.id == a.ID && r.dev == a.DeviceID {
			return db.GetRingSoundRow{Sha256: strings.Repeat("a", 64), Audio: r.audio, Size: int32(len(r.audio))}, nil
		}
	}
	return db.GetRingSoundRow{}, pgx.ErrNoRows
}

func (q *memQueries) RingSoundInfo(_ context.Context, a db.RingSoundInfoParams) ([]db.RingSoundInfoRow, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	var out []db.RingSoundInfoRow
	for _, r := range q.rows {
		if r.dev == a.DeviceID && slices.Contains(a.Ids, r.id) {
			out = append(out, db.RingSoundInfoRow{ID: r.id, Kind: r.kind, Title: r.title, Size: int32(len(r.audio))})
		}
	}
	return out, nil
}

func (q *memQueries) ListRingSounds(context.Context) ([]db.ListRingSoundsRow, error) { return nil, nil }

func (q *memQueries) TouchRingSounds(_ context.Context, a db.TouchRingSoundsParams) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i, r := range q.rows {
		if r.dev == a.DeviceID && slices.Contains(a.Ids, r.id) && r.used.Before(a.Now.Time) {
			q.rows[i].used = a.Now.Time
		}
	}
	return nil
}

func (q *memQueries) DeleteStaleRingSounds(_ context.Context, before pgtype.Timestamptz) (int64, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	n := len(q.rows)
	q.rows = slices.DeleteFunc(q.rows, func(r memRow) bool { return r.used.Before(before.Time) })
	return int64(n - len(q.rows)), nil
}

type fakeTTS struct {
	mu     sync.Mutex
	opens  int
	spoken []string
	fail   bool
}

func (f *fakeTTS) Open(context.Context, providers.TTSOptions) (providers.TTSStream, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opens++
	if f.fail {
		return nil, errors.New(providers.CodeTTSFailed)
	}
	return &fakeStream{tts: f, audio: make(chan []int16, 1)}, nil
}

type fakeStream struct {
	tts   *fakeTTS
	audio chan []int16
}

func (s *fakeStream) Speak(text string) error {
	s.tts.mu.Lock()
	defer s.tts.mu.Unlock()
	s.tts.spoken = append(s.tts.spoken, text)
	return nil
}

func (s *fakeStream) Flush() error {
	pcm := make([]int16, 24000)
	for i := range pcm {
		pcm[i] = 4000
	}
	s.audio <- pcm
	close(s.audio)
	return nil
}
func (s *fakeStream) Audio() <-chan []int16 { return s.audio }
func (s *fakeStream) Err() error            { return nil }
func (s *fakeStream) Close() error          { return nil }

type fakeEncoder struct{}

func (fakeEncoder) Encode(pcm []int16) ([]byte, error) {
	if len(pcm) != 480 {
		return nil, audio.ErrFrameSize
	}
	return []byte{0xf8, 1, 2}, nil
}
func (fakeEncoder) FrameSamples() int { return 480 }

type rig struct {
	q    *memQueries
	tts  *fakeTTS
	s    *ringsounds.Service
	logs *bytes.Buffer
	now  time.Time
}

func newRig(t *testing.T, transcode ringsounds.Transcoder) *rig {
	t.Helper()
	g := &rig{q: &memQueries{}, tts: &fakeTTS{}, logs: &bytes.Buffer{}, now: time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)}
	g.s = ringsounds.New(ringsounds.Deps{
		Q: g.q,
		Voice: func(_ context.Context, deviceID string) (providers.TTS, string, string, error) {
			if deviceID == devB {
				return nil, "", "", ringsounds.ErrNoTTS
			}
			return g.tts, "alena", "ru", nil
		},
		Encoder:   func() (audio.Encoder, error) { return fakeEncoder{}, nil },
		Transcode: transcode,
		Now:       func() time.Time { return g.now },
		Log:       slog.New(slog.NewTextHandler(g.logs, nil)),
	})
	return g
}

func TestSpeechIsMadeOnceForTheSameTextAndNeverLogged(t *testing.T) {
	g := newRig(t, nil)
	text := "Напоминаю: выключить духовку"
	first, err := g.s.Speech(context.Background(), devA, text)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.ID) != 16 || first.Title != text || first.Kind != ringsounds.KindSpeech || first.Reused {
		t.Fatalf("first %+v", first)
	}
	again, err := g.s.Speech(context.Background(), devA, "  "+text+" ")
	if err != nil || again.ID != first.ID || !again.Reused || g.tts.opens != 1 {
		t.Fatalf("again %+v %v opens %d", again, err, g.tts.opens)
	}
	if info := g.s.Info(context.Background(), devA, []string{first.ID, "zzzzzzzzzzzzzzzz"}); len(info) != 1 ||
		info[first.ID].Title != text {
		t.Fatalf("info %+v", info)
	}
	if info := g.s.Info(context.Background(), devB, []string{first.ID}); len(info) != 0 {
		t.Fatalf("another device's info %+v", info)
	}
	if strings.Contains(g.logs.String(), "духовк") {
		t.Fatal("the text was logged")
	}
	if !g.s.CanMakeMusic() {
		_, err := g.s.Music(context.Background(), devA, "k", "t", nil)
		if !errors.Is(err, ringsounds.ErrNoTranscoder) {
			t.Fatalf("music without ffmpeg: %v", err)
		}
	}
}

func TestSpeechRefusesWhatItCannotSay(t *testing.T) {
	g := newRig(t, nil)
	if _, err := g.s.Speech(context.Background(), devA, strings.Repeat("я", ringsounds.MaxTextRunes+1)); !errors.Is(err, ringsounds.ErrTooLong) {
		t.Fatalf("long: %v", err)
	}
	if _, err := g.s.Speech(context.Background(), devA, " "); !errors.Is(err, ringsounds.ErrTooLong) {
		t.Fatalf("empty: %v", err)
	}
	if _, err := g.s.Speech(context.Background(), devB, "x"); !errors.Is(err, ringsounds.ErrNoTTS) {
		t.Fatalf("no tts: %v", err)
	}
	g.tts.fail = true
	if _, err := g.s.Speech(context.Background(), devA, "x"); !errors.Is(err, ringsounds.ErrTTS) {
		t.Fatalf("tts failure: %v", err)
	}
}

func TestMusicIsTranscodedOnceFromWhatTheResolverOpens(t *testing.T) {
	calls := 0
	transcode := func(ctx context.Context, open func(context.Context) (ringsounds.Body, error), maxSeconds int) ([]byte, error) {
		calls++
		body, err := open(ctx)
		if err != nil {
			return nil, err
		}
		defer func() { _ = body.Close() }()
		src, _ := io.ReadAll(body)
		if string(src) != "song bytes" || maxSeconds != 600 {
			return nil, errors.New("unexpected")
		}
		return []byte("OggS fake"), nil
	}
	g := newRig(t, transcode)
	open := func(context.Context) (ringsounds.Body, error) {
		return io.NopCloser(strings.NewReader("song bytes")), nil
	}
	s, err := g.s.Music(context.Background(), devA, "youtube:abc", "Song — Artist", open)
	if err != nil || s.Title != "Song — Artist" || s.Kind != ringsounds.KindMusic {
		t.Fatalf("music %+v %v", s, err)
	}
	again, err := g.s.Music(context.Background(), devA, "youtube:abc", "Song — Artist", open)
	if err != nil || again.ID != s.ID || calls != 1 {
		t.Fatalf("again %+v %v calls %d", again, err, calls)
	}
	failing := func(context.Context) (ringsounds.Body, error) { return nil, errors.New("upstream https://x/?sig=1") }
	if _, err := g.s.Music(context.Background(), devA, "youtube:other", "x", failing); !errors.Is(err, ringsounds.ErrTranscode) {
		t.Fatalf("failing: %v", err)
	}
	if strings.Contains(g.logs.String(), "sig=") {
		t.Fatal("an upstream url was logged")
	}
}

func TestUnusedSoundsGoAfterSevenDays(t *testing.T) {
	g := newRig(t, nil)
	kept, _ := g.s.Speech(context.Background(), devA, "keep")
	gone, _ := g.s.Speech(context.Background(), devA, "gone")
	g.now = g.now.Add(8 * 24 * time.Hour)
	g.s.Collect(context.Background(), func(context.Context) (map[string][]string, error) {
		return map[string][]string{devA: {kept.ID}}, nil
	})
	info := g.s.Info(context.Background(), devA, []string{kept.ID, gone.ID})
	if _, ok := info[kept.ID]; !ok || len(info) != 1 {
		t.Fatalf("after gc %+v", info)
	}
	// a failed read of the references deletes nothing
	g.now = g.now.Add(30 * 24 * time.Hour)
	g.s.Collect(context.Background(), func(context.Context) (map[string][]string, error) { return nil, errors.New("x") })
	if len(g.s.Info(context.Background(), devA, []string{kept.ID})) != 1 {
		t.Fatal("deleted on a failed read")
	}
}

type noPing struct{}

func (noPing) Ping(context.Context) error { return nil }

func get(t *testing.T, s *ringsounds.Service, bearer, path, rangeHeader string) (*http.Response, []byte) {
	t.Helper()
	app := api.NewApp(api.Options{DB: noPing{}, Version: "t", Device: []func(fiber.Router){
		func(r fiber.Router) {
			s.Mount(r, func(_ context.Context, token string) (string, error) {
				switch token {
				case "tok-a":
					return devA, nil
				case "tok-b":
					return devB, nil
				}
				return "", auth.ErrUnauthorized
			})
		},
	}})
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if rangeHeader != "" {
		req.Header.Set("Range", rangeHeader)
	}
	resp, err := app.Test(req, fiber.TestConfig{Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return resp, body
}

func TestTheRouteServesASoundToItsDeviceAloneWithRanges(t *testing.T) {
	g := newRig(t, nil)
	s, err := g.s.Speech(context.Background(), devA, "чай")
	if err != nil {
		t.Fatal(err)
	}
	path := "/v2/ring-sound/" + s.ID
	resp, whole := get(t, g.s, "tok-a", path, "")
	if resp.StatusCode != http.StatusOK || !bytes.HasPrefix(whole, []byte("OggS")) ||
		resp.Header.Get("Content-Type") != "audio/ogg" || resp.Header.Get("Accept-Ranges") != "bytes" {
		t.Fatalf("whole %d %q", resp.StatusCode, resp.Header)
	}
	resp, part := get(t, g.s, "tok-a", path, "bytes=4-9")
	if resp.StatusCode != http.StatusPartialContent || !bytes.Equal(part, whole[4:10]) ||
		resp.Header.Get("Content-Range") != "bytes 4-9/"+itoa(len(whole)) {
		t.Fatalf("range %d %q", resp.StatusCode, resp.Header.Get("Content-Range"))
	}
	resp, tail := get(t, g.s, "tok-a", path, "bytes=-5")
	if resp.StatusCode != http.StatusPartialContent || !bytes.Equal(tail, whole[len(whole)-5:]) {
		t.Fatalf("suffix %d", resp.StatusCode)
	}
	if resp, _ := get(t, g.s, "tok-a", path, "bytes="+itoa(len(whole))+"-"); resp.StatusCode != http.StatusRequestedRangeNotSatisfiable ||
		resp.Header.Get("Content-Range") != "bytes */"+itoa(len(whole)) {
		t.Fatalf("past the end %d", resp.StatusCode)
	}
	for _, c := range []struct {
		bearer, path string
		want         int
	}{
		{"tok-b", path, http.StatusNotFound},
		{"tok-a", "/v2/ring-sound/zzzzzzzzzzzzzzzz", http.StatusNotFound},
		{"tok-a", "/v2/ring-sound/..%2Fx", http.StatusNotFound},
		{"nope", path, http.StatusUnauthorized},
		{"", path, http.StatusUnauthorized},
	} {
		if resp, _ := get(t, g.s, c.bearer, c.path, ""); resp.StatusCode != c.want {
			t.Errorf("%s %s: %d, want %d", c.bearer, c.path, resp.StatusCode, c.want)
		}
	}
	if strings.Contains(g.logs.String(), "tok-") {
		t.Fatal("a token was logged")
	}
}

func itoa(n int) string { return strconv.Itoa(n) }
