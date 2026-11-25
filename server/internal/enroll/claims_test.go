package enroll_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5/pgxpool"

	"git.shino.boo/devilreef/cadence/server/internal/api"
	"git.shino.boo/devilreef/cadence/server/internal/api/apitest"
	"git.shino.boo/devilreef/cadence/server/internal/auth"
	"git.shino.boo/devilreef/cadence/server/internal/enroll"
)

// syncClock is a clock the tests move while polls read it
type syncClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *syncClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *syncClock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type claimRig struct {
	claims *enroll.ClaimService
	svc    *enroll.Service
	clk    *syncClock
	pool   *pgxpool.Pool
}

func newClaims(t *testing.T) claimRig {
	t.Helper()
	svc, _, pool := newSvc(t)
	clk := &syncClock{t: time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)}
	return claimRig{claims: enroll.NewClaims(pool, clk.now, nil), svc: svc, clk: clk, pool: pool}
}

var claimCode = regexp.MustCompile(`^[2-9A-HJ-NP-Z]{6}$`)

// wait polls once, giving up after d
func (r claimRig) wait(t *testing.T, id string, d time.Duration) (enroll.ClaimResult, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	return r.claims.Wait(ctx, id)
}

func TestClaimApprovedOnce(t *testing.T) {
	ctx := context.Background()
	r := newClaims(t)
	c, err := r.claims.Start(ctx, "sim-tv", "10.0.0.5")
	if err != nil {
		t.Fatal(err)
	}
	if !claimCode.MatchString(c.Code) || !strings.HasPrefix(c.ID, "cdc_") || !c.ExpiresAt.Equal(r.clk.now().Add(enroll.ClaimTTL)) {
		t.Fatalf("claim %+v", c)
	}
	open, err := r.claims.Open(ctx)
	if err != nil || len(open) != 1 || open[0].Code != c.Code || open[0].Board != "sim-tv" || open[0].Source != "10.0.0.5" || open[0].ID != "" {
		t.Fatalf("open %+v %v", open, err)
	}
	if res, err := r.wait(t, c.ID, 50*time.Millisecond); err != nil || res.State != enroll.ClaimPending {
		t.Fatalf("pending: %+v %v", res, err)
	}
	// a typed code in any case and grouping
	d, err := r.claims.Approve(ctx, strings.ToLower(c.Code[:3])+" · "+c.Code[3:], enroll.Approval{Name: " Living TV ", Room: " Гостиная "})
	if err != nil || d.Name != "Living TV" || d.Room != "Гостиная" || d.Model != "sim-tv" || d.Status != "pending" {
		t.Fatalf("approve %+v %v", d, err)
	}
	res, err := r.wait(t, c.ID, time.Second)
	if err != nil || res.State != enroll.ClaimApproved || res.DeviceID != d.ID || !strings.HasPrefix(res.Token, auth.DevicePrefix) {
		t.Fatalf("approved %+v %v", res, err)
	}
	if id, err := r.svc.AuthDevice(ctx, res.Token); err != nil || id != d.ID {
		t.Fatalf("token %q %v", id, err)
	}
	if room := r.svc.Room(ctx, d.ID); room != "Гостиная" {
		t.Fatalf("room %q", room)
	}
	if _, err := r.wait(t, c.ID, time.Second); !errors.Is(err, enroll.ErrClaimNotFound) {
		t.Fatalf("second poll: %v", err)
	}
	if open, _ := r.claims.Open(ctx); len(open) != 0 {
		t.Fatalf("still open %+v", open)
	}
	if _, err := r.claims.Approve(ctx, c.Code, enroll.Approval{Name: "again"}); !errors.Is(err, enroll.ErrClaimNotFound) {
		t.Fatalf("approve again: %v", err)
	}
}

func TestClaimPollWakesOnApproval(t *testing.T) {
	ctx := context.Background()
	r := newClaims(t)
	c, _ := r.claims.Start(ctx, "android-tv", "a")
	got := make(chan enroll.ClaimResult, 1)
	go func() {
		res, _ := r.wait(t, c.ID, 10*time.Second)
		got <- res
	}()
	time.Sleep(100 * time.Millisecond)
	start := time.Now()
	if _, err := r.claims.Approve(ctx, c.Code, enroll.Approval{Name: "tv"}); err != nil {
		t.Fatal(err)
	}
	select {
	case res := <-got:
		if res.State != enroll.ClaimApproved || time.Since(start) > time.Second {
			t.Fatalf("%+v after %v", res, time.Since(start))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the poll did not wake")
	}
}

func TestClaimDeclinedAndExpired(t *testing.T) {
	ctx := context.Background()
	r := newClaims(t)
	a, _ := r.claims.Start(ctx, "sim-tv", "a")
	b, _ := r.claims.Start(ctx, "sim-tv", "b")
	if err := r.claims.Decline(ctx, a.Code); err != nil {
		t.Fatal(err)
	}
	if res, err := r.wait(t, a.ID, time.Second); err != nil || res.State != enroll.ClaimDeclined {
		t.Fatalf("declined %+v %v", res, err)
	}
	if err := r.claims.Decline(ctx, a.Code); !errors.Is(err, enroll.ErrClaimNotFound) {
		t.Fatalf("decline again: %v", err)
	}
	r.clk.add(enroll.ClaimTTL)
	if res, err := r.wait(t, b.ID, time.Second); err != nil || res.State != enroll.ClaimExpired {
		t.Fatalf("expired %+v %v", res, err)
	}
	if _, err := r.claims.Approve(ctx, b.Code, enroll.Approval{Name: "late"}); !errors.Is(err, enroll.ErrClaimNotFound) {
		t.Fatalf("approve expired: %v", err)
	}
	if _, err := r.wait(t, "cdc_unknown", time.Second); !errors.Is(err, enroll.ErrClaimNotFound) {
		t.Fatalf("unknown: %v", err)
	}
}

func TestClaimRemovedBeforeTaken(t *testing.T) {
	ctx := context.Background()
	r := newClaims(t)
	c, _ := r.claims.Start(ctx, "sim-tv", "a")
	d, err := r.claims.Approve(ctx, c.Code, enroll.Approval{Name: "tv"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.pool.Exec(ctx, "DELETE FROM devices WHERE id = $1", d.ID); err != nil {
		t.Fatal(err)
	}
	if res, err := r.wait(t, c.ID, time.Second); err != nil || res.State != enroll.ClaimRemoved {
		t.Fatalf("removed %+v %v", res, err)
	}
}

func TestClaimApproveRefusals(t *testing.T) {
	ctx := context.Background()
	r := newClaims(t)
	if _, _, err := r.svc.CreateDevice(ctx, "Kitchen", "sim"); err != nil {
		t.Fatal(err)
	}
	c, _ := r.claims.Start(ctx, "sim-tv", "a")
	for _, tc := range []struct {
		a    enroll.Approval
		want error
	}{
		{enroll.Approval{Name: "kitchen"}, enroll.ErrNameTaken},
		{enroll.Approval{Name: " "}, enroll.ErrNameInvalid},
		{enroll.Approval{Name: "tv", Room: "a\nb"}, enroll.ErrRoomInvalid},
	} {
		if _, err := r.claims.Approve(ctx, c.Code, tc.a); !errors.Is(err, tc.want) {
			t.Fatalf("%+v: %v", tc.a, err)
		}
	}
	// the claim stays open for a better answer
	if _, err := r.claims.Approve(ctx, c.Code, enroll.Approval{Name: "tv"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.claims.Start(ctx, "speaker-x", "a"); !errors.Is(err, enroll.ErrUnknownBoard) {
		t.Fatalf("board: %v", err)
	}
}

func TestClaimLimits(t *testing.T) {
	ctx := context.Background()
	r := newClaims(t)
	first, _ := r.claims.Start(ctx, "sim-tv", "a")
	if _, err := r.claims.Start(ctx, "sim-tv", "a"); err != nil {
		t.Fatal(err)
	}
	// two open claims a source
	if _, err := r.claims.Start(ctx, "sim-tv", "a"); !errors.Is(err, enroll.ErrClaimRateLimited) {
		t.Fatalf("third open: %v", err)
	}
	if _, err := r.claims.Start(ctx, "sim-tv", "b"); err != nil {
		t.Fatalf("other source: %v", err)
	}
	// ten starts a source in ten minutes, declined or not
	if err := r.claims.Decline(ctx, first.Code); err != nil {
		t.Fatal(err)
	}
	starts := 2
	for ; starts < 10; starts++ {
		c, err := r.claims.Start(ctx, "sim-tv", "a")
		if err != nil {
			t.Fatalf("start %d: %v", starts+1, err)
		}
		if err := r.claims.Decline(ctx, c.Code); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.claims.Start(ctx, "sim-tv", "a"); !errors.Is(err, enroll.ErrClaimRateLimited) {
		t.Fatalf("eleventh start: %v", err)
	}
	r.clk.add(11 * time.Minute)
	if _, err := r.claims.Start(ctx, "sim-tv", "a"); err != nil {
		t.Fatalf("after the window: %v", err)
	}
	// and twenty open claims in all
	for i := range 30 {
		_, err := r.claims.Start(ctx, "sim-tv", "src-"+string(rune('A'+i)))
		if errors.Is(err, enroll.ErrClaimRateLimited) {
			open, _ := r.claims.Open(ctx)
			if len(open) != 20 {
				t.Fatalf("refused at %d open", len(open))
			}
			return
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	t.Fatal("no cap on open claims")
}

func TestNormalizeClaimCode(t *testing.T) {
	for in, want := range map[string]string{"k7m4qx": "K7M4QX", "K7M · 4QX": "K7M4QX", "k7m-4qx": "K7M4QX"} {
		if got, ok := enroll.NormalizeClaimCode(in); !ok || got != want {
			t.Errorf("%q: %q %v", in, got, ok)
		}
	}
	for _, in := range []string{"", "K7M4Q", "K7M4QXX", "K0M4QX", "KIM4QX", "K1M4QX", "KOM4QX", "К7М4QX"} {
		if got, ok := enroll.NormalizeClaimCode(in); ok {
			t.Errorf("%q: %q", in, got)
		}
	}
	for range 200 {
		c, err := enroll.NewClaimCode()
		if err != nil || !claimCode.MatchString(c) {
			t.Fatalf("%q %v", c, err)
		}
	}
}

func newClaimsApp(t *testing.T) (*fiber.App, claimRig) {
	t.Helper()
	r := newClaims(t)
	r.clk = &syncClock{t: time.Now()}
	r.claims = enroll.NewClaims(r.pool, r.clk.now, nil)
	return api.NewApp(api.Options{
		DB: noPing{}, Version: "t",
		VerifyToken: apitest.Verify,
		Admin: []func(huma.API){
			func(a huma.API) { enroll.RegisterClaimsAdmin(a, r.claims) },
			func(a huma.API) { enroll.RegisterAdmin(a, r.svc, func(string) bool { return false }) },
		},
		Device: []func(fiber.Router){func(rt fiber.Router) { enroll.MountClaims(rt, r.claims, "https://cadence.example") }},
	}), r
}

func TestClaimHTTPFlow(t *testing.T) {
	app, _ := newClaimsApp(t)
	code, body := send(t, app, http.MethodPost, "/v2/claim", `{"board":"sim-tv"}`)
	if code != 201 {
		t.Fatalf("%d %s", code, body)
	}
	var started struct {
		ID, Code, URL, ExpiresAt string
		PollAfterMs              int
	}
	if err := json.Unmarshal([]byte(body), &started); err != nil || !claimCode.MatchString(started.Code) ||
		started.URL != "https://cadence.example/claims/"+started.Code || started.PollAfterMs <= 0 || started.ExpiresAt == "" {
		t.Fatalf("%v %s", err, body)
	}
	if code, body := send(t, app, http.MethodGet, "/v2/claim/"+started.ID+"?wait=0s", ""); code != 200 || body != `{"state":"pending"}` {
		t.Fatalf("%d %s", code, body)
	}
	if code, body := send(t, app, http.MethodGet, "/api/v1/claims", ""); code != 200 || !strings.Contains(body, `"code":"`+started.Code+`"`) || strings.Contains(body, started.ID) {
		t.Fatalf("%d %s", code, body)
	}
	if code, _ := send(t, app, http.MethodGet, "/api/v1/claims/"+strings.ToLower(started.Code), ""); code != 200 {
		t.Fatalf("get: %d", code)
	}
	code, body = send(t, app, http.MethodPost, "/api/v1/claims/"+started.Code+"/approve", `{"name":"Living TV","room":"Living"}`)
	if code != 201 || !strings.Contains(body, `"room":"Living"`) || !strings.Contains(body, `"status":"pending"`) {
		t.Fatalf("%d %s", code, body)
	}
	code, body = send(t, app, http.MethodGet, "/v2/claim/"+started.ID+"?wait=1s", "")
	var took struct{ State, DeviceID, Token string }
	if code != 200 || json.Unmarshal([]byte(body), &took) != nil || took.State != "approved" || took.DeviceID == "" || !strings.HasPrefix(took.Token, "cdd_") {
		t.Fatalf("%d %s", code, body)
	}
	if code, _ := send(t, app, http.MethodGet, "/v2/claim/"+started.ID+"?wait=0s", ""); code != 404 {
		t.Fatalf("after: %d", code)
	}
	if _, list := send(t, app, http.MethodGet, "/api/v1/devices", ""); !strings.Contains(list, `"room":"Living"`) || !strings.Contains(list, `"status":"enrolled"`) {
		t.Fatalf("list %s", list)
	}
}

func TestClaimHTTPRefusals(t *testing.T) {
	app, _ := newClaimsApp(t)
	for _, c := range []struct {
		method, path, body string
		status             int
		want               string
	}{
		{http.MethodPost, "/v2/claim", `{"board":"nope"}`, 400, `{"error":"unknown_board"}`},
		{http.MethodPost, "/v2/claim", `not json`, 400, `{"error":"invalid"}`},
		{http.MethodGet, "/v2/claim/cdc_nothing?wait=0s", "", 404, `{"error":"not_found"}`},
		{http.MethodGet, "/v2/claim/other", "", 404, `{"error":"not_found"}`},
		{http.MethodGet, "/v2/claim/cdc_x?wait=soon", "", 400, `{"error":"invalid"}`},
		{http.MethodPost, "/api/v1/claims/ABCDEF/approve", `{"name":"tv"}`, 404, ""},
		{http.MethodPost, "/api/v1/claims/ABCDEF/decline", ``, 404, ""},
		{http.MethodGet, "/api/v1/claims/ABCDEF", ``, 404, ""},
	} {
		status, body := send(t, app, c.method, c.path, c.body)
		if status != c.status || (c.want != "" && body != c.want) {
			t.Errorf("%s %s: %d %s", c.method, c.path, status, body)
		}
	}
	// one source: two open claims, then 429
	for i := range 3 {
		status, body := send(t, app, http.MethodPost, "/v2/claim", `{"board":"sim-tv"}`)
		if (i < 2 && status != 201) || (i == 2 && (status != 429 || body != `{"error":"rate_limited"}`)) {
			t.Fatalf("start %d: %d %s", i, status, body)
		}
	}
}

func TestClaimPrune(t *testing.T) {
	ctx := context.Background()
	r := newClaims(t)
	c, _ := r.claims.Start(ctx, "sim-tv", "a")
	r.clk.add(enroll.ClaimTTL + 2*time.Hour)
	runCtx, cancel := context.WithCancel(ctx)
	stop := r.claims.Run(runCtx)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := r.wait(t, c.ID, time.Second); errors.Is(err, enroll.ErrClaimNotFound) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("not pruned")
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	stop()
}
