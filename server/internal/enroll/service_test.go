package enroll_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"git.shino.boo/devilreef/cadence/server/internal/auth"
	"git.shino.boo/devilreef/cadence/server/internal/enroll"
	"git.shino.boo/devilreef/cadence/server/internal/store/db"
	"git.shino.boo/devilreef/cadence/server/internal/storetest"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newSvc(t *testing.T) (*enroll.Service, *clock, *pgxpool.Pool) {
	t.Helper()
	pool := storetest.Pool(t)
	clk := &clock{t: time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)}
	return enroll.NewService(db.New(pool), clk.now), clk, pool
}

func TestEnrollOnce(t *testing.T) {
	ctx := context.Background()
	s, _, _ := newSvc(t)
	d, code, err := s.CreateDevice(ctx, "kitchen", "sim")
	if err != nil {
		t.Fatal(err)
	}
	id, tok, err := s.Enroll(ctx, code, enroll.Fingerprint{Board: "sim"})
	if err != nil || id != d.ID || !strings.HasPrefix(tok, auth.DevicePrefix) {
		t.Fatalf("id=%q err=%v", id, err)
	}
	if _, _, err := s.Enroll(ctx, code, enroll.Fingerprint{Board: "sim"}); !errors.Is(err, enroll.ErrRefused) {
		t.Fatalf("second use: %v", err)
	}
}

func TestEnrollConcurrentUsesOnce(t *testing.T) {
	ctx := context.Background()
	s, _, _ := newSvc(t)
	_, code, _ := s.CreateDevice(ctx, "kitchen", "sim")
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok := 0
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, err := s.Enroll(ctx, code, enroll.Fingerprint{Board: "sim"}); err == nil {
				mu.Lock()
				ok++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if ok != 1 {
		t.Fatalf("%d enrolled", ok)
	}
}

func TestEnrollExpired(t *testing.T) {
	ctx := context.Background()
	s, clk, _ := newSvc(t)
	_, code, _ := s.CreateDevice(ctx, "kitchen", "sim")
	clk.t = clk.t.Add(31 * time.Minute)
	if _, _, err := s.Enroll(ctx, code, enroll.Fingerprint{Board: "sim"}); !errors.Is(err, enroll.ErrRefused) {
		t.Fatalf("err = %v", err)
	}
}

func TestEnrollModelMismatch(t *testing.T) {
	ctx := context.Background()
	s, _, _ := newSvc(t)
	_, code, _ := s.CreateDevice(ctx, "kitchen", "sim")
	if _, _, err := s.Enroll(ctx, code, enroll.Fingerprint{Board: "pickle"}); !errors.Is(err, enroll.ErrRefused) {
		t.Fatalf("err = %v", err)
	}
	if _, _, err := s.Enroll(ctx, "garbage", enroll.Fingerprint{Board: "sim"}); !errors.Is(err, enroll.ErrRefused) {
		t.Fatalf("garbage: %v", err)
	}
	// a refused attempt must not burn the code
	if _, _, err := s.Enroll(ctx, code, enroll.Fingerprint{Board: "sim"}); err != nil {
		t.Fatalf("code burnt: %v", err)
	}
}

// the speakers' and the screens' boards can be created and enrolled, each by the board id its package reports, and by
// no other
func TestServiceEnrollsTheSpeakerBoards(t *testing.T) {
	ctx := context.Background()
	s, _, _ := newSvc(t)
	for _, board := range []string{"yandex-pickle", "yandex-midi", "yandex-micro", "vk-capsula-mini", "sim-tv", "rpi", "android-tv"} {
		_, code, err := s.CreateDevice(ctx, board, board)
		if err != nil {
			t.Fatalf("%s: create: %v", board, err)
		}
		if _, _, err := s.Enroll(ctx, code, enroll.Fingerprint{Board: "sim"}); !errors.Is(err, enroll.ErrRefused) {
			t.Fatalf("%s: a sim enrolled with its code: %v", board, err)
		}
		if _, _, err := s.Enroll(ctx, code, enroll.Fingerprint{Board: board}); err != nil {
			t.Fatalf("%s: enroll: %v", board, err)
		}
	}
}

func TestServiceUnknownModel(t *testing.T) {
	s, _, _ := newSvc(t)
	if _, _, err := s.CreateDevice(context.Background(), "x", "toaster"); !errors.Is(err, enroll.ErrUnknownModel) {
		t.Fatalf("err = %v", err)
	}
}

func TestAuthDevice(t *testing.T) {
	ctx := context.Background()
	s, _, pool := newSvc(t)
	d, code, _ := s.CreateDevice(ctx, "kitchen", "sim")
	_, tok, _ := s.Enroll(ctx, code, enroll.Fingerprint{Board: "sim"})
	id, err := s.AuthDevice(ctx, tok)
	if err != nil || id != d.ID {
		t.Fatalf("id=%q err=%v", id, err)
	}

	// pending device with a planted hash must still be refused
	pending, _, _ := s.CreateDevice(ctx, "hall", "sim")
	planted, hash, _ := auth.NewToken(auth.DevicePrefix)
	if _, err := pool.Exec(ctx, `UPDATE devices SET token_hash = $1 WHERE id = $2`, hash, pending.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AuthDevice(ctx, planted); !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("pending: %v", err)
	}
	apiTok, _, _ := auth.NewToken(auth.APIPrefix)
	for _, bad := range []string{apiTok, "cdd_nope", ""} {
		if _, err := s.AuthDevice(ctx, bad); !errors.Is(err, auth.ErrUnauthorized) {
			t.Fatalf("%q: %v", bad, err)
		}
	}
}

func TestCodeStoredHashed(t *testing.T) {
	ctx := context.Background()
	s, _, pool := newSvc(t)
	_, code, _ := s.CreateDevice(ctx, "kitchen", "sim")
	norm, _ := enroll.NormalizeCode(code)
	var n int
	err := pool.QueryRow(ctx, `SELECT count(*) FROM enroll_codes WHERE code_hash = $1`, []byte(norm)).Scan(&n)
	if err != nil || n != 0 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	var l int
	if err := pool.QueryRow(ctx, `SELECT length(code_hash) FROM enroll_codes`).Scan(&l); err != nil || l != 32 {
		t.Fatalf("hash len %d err=%v", l, err)
	}
}

// a link's last-seen write answers the device's name and model, which its
// presence events carry (M8-D2); a device deleted meanwhile is unknown
func TestSeenSetsLastSeen(t *testing.T) {
	ctx := context.Background()
	s, clk, _ := newSvc(t)
	d, code, _ := s.CreateDevice(ctx, "kitchen", "sim")
	if _, _, err := s.Enroll(ctx, code, enroll.Fingerprint{Board: "sim"}); err != nil {
		t.Fatal(err)
	}
	clk.t = clk.t.Add(time.Hour)
	if name, model, err := s.Seen(ctx, d.ID); err != nil || name != "kitchen" || model != "sim" {
		t.Fatalf("%q %q %v", name, model, err)
	}
	if _, _, err := s.Seen(ctx, "00000000-0000-0000-0000-000000000009"); !errors.Is(err, enroll.ErrUnknownDevice) {
		t.Fatalf("unknown device: %v", err)
	}
	rows, err := s.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].LastSeenAt == nil || !rows[0].LastSeenAt.Equal(clk.t) {
		t.Fatalf("rows %+v", rows)
	}
}
