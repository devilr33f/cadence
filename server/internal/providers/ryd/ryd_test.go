package ryd

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func setup(t *testing.T, h http.HandlerFunc) (*Client, *clock, *bytes.Buffer) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	clk := &clock{t: time.Date(2026, 11, 16, 12, 0, 30, 0, time.UTC)}
	var logs bytes.Buffer
	return New(Config{BaseURL: srv.URL, Now: clk.now, Log: slog.New(slog.NewTextHandler(&logs, nil))}, srv.Client()), clk, &logs
}

// answer is the API's answer of 2026-11-16 for dQw4w9WgXcQ, its id swapped
func answer(id string) string {
	return fmt.Sprintf(`{"id":%q,"dateCreated":"2022-04-09T22:01:38.222268Z","likes":19507079,"rawDislikes":6727,`+
		`"rawLikes":130765,"dislikes":521964,"rating":4.89,"viewCount":1837530628,"deleted":false}`, id)
}

func TestVotesReadsAndCaches(t *testing.T) {
	var calls atomic.Int32
	c, _, _ := setup(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/votes" || r.Method != http.MethodGet {
			t.Errorf("%s %s", r.Method, r.URL)
		}
		id := r.URL.Query().Get("videoId")
		if id == "aaaaaaaaaaa" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(answer(id)))
	})
	got := c.Votes(context.Background(), []string{"dQw4w9WgXcQ", "aaaaaaaaaaa", "not an id", "dQw4w9WgXcQ"})
	if len(got) != 1 || got["dQw4w9WgXcQ"] != (Votes{Likes: 19507079, Dislikes: 521964, Views: 1837530628}) {
		t.Fatalf("%v", got)
	}
	if calls.Load() != 2 {
		t.Fatalf("%d calls", calls.Load())
	}
	c.Votes(context.Background(), []string{"dQw4w9WgXcQ"})
	if calls.Load() != 2 {
		t.Fatal("not cached")
	}
}

func TestVotesRefusesAnAnswerForAnotherID(t *testing.T) {
	c, _, _ := setup(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(answer("kJQP7kiw5Fk"))) })
	if got := c.Votes(context.Background(), []string{"dQw4w9WgXcQ"}); len(got) != 0 {
		t.Fatalf("%v", got)
	}
}

func TestVotesAsksAtMostMaxIDs(t *testing.T) {
	var calls atomic.Int32
	c, _, _ := setup(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(answer(r.URL.Query().Get("videoId"))))
	})
	var ids []string
	for i := range 12 {
		ids = append(ids, fmt.Sprintf("video%06d", i))
	}
	if got := c.Votes(context.Background(), ids); len(got) != MaxIDs || calls.Load() != MaxIDs {
		t.Fatalf("%d answers, %d calls", len(got), calls.Load())
	}
}

func TestVotesBacksOffOn429(t *testing.T) {
	var calls atomic.Int32
	c, clk, logs := setup(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(answer(r.URL.Query().Get("videoId"))))
	})
	if got := c.Votes(context.Background(), []string{"dQw4w9WgXcQ"}); len(got) != 0 {
		t.Fatalf("%v", got)
	}
	clk.add(time.Minute)
	if got := c.Votes(context.Background(), []string{"dQw4w9WgXcQ"}); len(got) != 0 || calls.Load() != 1 {
		t.Fatalf("asked while backing off: %v, %d calls", got, calls.Load())
	}
	clk.add(time.Minute)
	if got := c.Votes(context.Background(), []string{"dQw4w9WgXcQ"}); len(got) != 1 {
		t.Fatalf("not asked again after the back-off: %v", got)
	}
	if !strings.Contains(logs.String(), "ryd_rate_limited") || strings.Contains(logs.String(), "dQw4w9WgXcQ") {
		t.Fatalf("log %q", logs.String())
	}
}

func TestVotesKeepsUnderItsMinuteLimit(t *testing.T) {
	var calls atomic.Int32
	c, clk, _ := setup(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusNotFound)
	})
	for i := range 20 {
		var ids []string
		for j := range MaxIDs {
			ids = append(ids, fmt.Sprintf("v%02d%08d", i, j))
		}
		c.Votes(context.Background(), ids)
	}
	if calls.Load() != perMinute {
		t.Fatalf("%d calls in a minute", calls.Load())
	}
	clk.add(time.Minute)
	c.Votes(context.Background(), []string{"zzzzzzzzzzz"})
	if calls.Load() != perMinute+1 {
		t.Fatal("the next minute did not ask")
	}
}

func TestVotesWithinItsContext(t *testing.T) {
	c, _, _ := setup(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		_, _ = w.Write([]byte(answer(r.URL.Query().Get("videoId"))))
	})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if got := c.Votes(ctx, []string{"dQw4w9WgXcQ"}); len(got) != 0 || time.Since(start) > 250*time.Millisecond {
		t.Fatalf("%v after %v", got, time.Since(start))
	}
}
