package auth_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/auth"
)

const ownerPassword = "owner pass phrase"

type loginRig struct {
	*store
	l    *auth.Login
	logs *lockedBuffer
}

// newLogin is the owner's login over a fresh database, its password hashed
// with cheap params so a test can fail many logins quickly.
func newLogin(t *testing.T) *loginRig {
	t.Helper()
	r := &loginRig{store: newStore(t), logs: &lockedBuffer{}}
	owner := auth.NewOwner(r.pool, r.c.Now)
	auth.SetParams(owner, auth.Params{Memory: 8 * 1024, Time: 1, Threads: 1})
	if _, err := owner.SetPassword(context.Background(), ownerPassword); err != nil {
		t.Fatal(err)
	}
	r.l = auth.NewLogin(owner, r.s, slog.New(slog.NewJSONHandler(r.logs, nil)), r.c.Now)
	return r
}

func (r *loginRig) login(ip, browser, password string) (auth.Grant, error) {
	var browsers []string
	if browser != "" {
		browsers = []string{browser}
	}
	return r.l.Login(context.Background(), ip, browsers, password)
}

// fail logs in n times with a wrong password and wants each refused as wrong.
func (r *loginRig) fail(t *testing.T, n int, ip, browser string) {
	t.Helper()
	for i := range n {
		if _, err := r.login(ip, browser, "guess guess guess"); !errors.Is(err, auth.ErrUnauthorized) {
			t.Fatalf("failure %d from %s: %v", i+1, ip, err)
		}
	}
}

func limited(err error) bool {
	var rl *auth.RateLimitedError
	return errors.As(err, &rl)
}

var hashField = regexp.MustCompile(`"ip_hash":"([0-9a-f]+)"`)

// hashes are the ip_hash values the log carries, in order.
func hashes(out string) []string {
	var got []string
	for _, m := range hashField.FindAllStringSubmatch(out, -1) {
		got = append(got, m[1])
	}
	return got
}

func TestLoginOpensASession(t *testing.T) {
	ctx := context.Background()
	r := newLogin(t)
	g, err := r.login("198.51.100.7", "", ownerPassword)
	if err != nil || g.Session.Token == "" || g.Browser == "" {
		t.Fatalf("%+v %v", g, err)
	}
	if got, err := r.l.Validate(ctx, g.Session.Token); err != nil || !got.ExpiresAt.Equal(g.Session.ExpiresAt) {
		t.Fatalf("validate %+v %v", got, err)
	}
	if err := r.l.Logout(ctx, g.Session.Token); err != nil {
		t.Fatal(err)
	}
	if _, err := r.l.Validate(ctx, g.Session.Token); !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("after logout: %v", err)
	}
	if !strings.Contains(r.logs.String(), `"msg":"admin_login","bucket":"ip","ip_hash":"`) {
		t.Fatalf("log %s", r.logs.String())
	}
	noSecrets(t, r.logs.String(), ownerPassword, g.Session.Token, g.Browser, "198.51.100.7")
}

// the ip hash is an hmac under a key of the process: stable within one run,
// unlinkable to the address or to another run (Ruling M7a-R3)
func TestLoginIPHashIsKeyed(t *testing.T) {
	a, b := newLogin(t), newLogin(t)
	for range 2 {
		_, _ = a.login("198.51.100.7", "", "guess guess guess")
	}
	_, _ = b.login("198.51.100.7", "", "guess guess guess")
	ha, hb := hashes(a.logs.String()), hashes(b.logs.String())
	sum := sha256.Sum256([]byte("198.51.100.7"))
	plain := hex.EncodeToString(sum[:])[:8]
	if len(ha) != 2 || ha[0] != ha[1] || len(ha[0]) != 8 || len(hb) != 1 || hb[0] == ha[0] || ha[0] == plain || hb[0] == plain {
		t.Fatalf("hashes %q %q (plain sha256 %s)", ha, hb, plain)
	}
}

func TestLoginWrongPassword(t *testing.T) {
	r := newLogin(t)
	if _, err := r.login("198.51.100.7", "", "not the password"); !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("err %v", err)
	}
	if !strings.Contains(r.logs.String(), `"msg":"admin_login_failed","reason":"password","bucket":"ip","ip_hash":"`) {
		t.Fatalf("log %s", r.logs.String())
	}
	noSecrets(t, r.logs.String(), "not the password", ownerPassword, "198.51.100.7")
}

// a refusal is logged once per bucket and window, and the window's count when
// it ends
func TestLoginRateLimited(t *testing.T) {
	r := newLogin(t)
	r.fail(t, 5, "198.51.100.7", "")
	r.c.Add(time.Minute)
	// even the right password waits: the limit is checked before it
	for range 3 {
		_, err := r.login("198.51.100.7", "", ownerPassword)
		var rl *auth.RateLimitedError
		if !errors.As(err, &rl) || rl.RetryAfter != 14*time.Minute {
			t.Fatalf("err %v", err)
		}
	}
	if n := strings.Count(r.logs.String(), `"reason":"rate_limited","bucket":"ip"`); n != 1 {
		t.Fatalf("%d rate_limited lines: %s", n, r.logs.String())
	}
	if _, err := r.login("198.51.100.8", "", ownerPassword); err != nil {
		t.Fatalf("another ip: %v", err)
	}
	r.c.Add(14 * time.Minute)
	r.l.Sweep()
	if !strings.Contains(r.logs.String(), `"msg":"admin_login_refused","bucket":"ip","ip_hash":"`) ||
		!strings.Contains(r.logs.String(), `"count":3`) {
		t.Fatalf("log %s", r.logs.String())
	}
	noSecrets(t, r.logs.String(), ownerPassword, "guess guess", "198.51.100.7", "198.51.100.8")
}

// a success clears the ip's failures
func TestLoginSuccessResetsTheIP(t *testing.T) {
	r := newLogin(t)
	r.fail(t, 4, "198.51.100.7", "")
	if _, err := r.login("198.51.100.7", "", ownerPassword); err != nil {
		t.Fatal(err)
	}
	r.fail(t, 4, "198.51.100.7", "")
}

// an ipv6 client is its /64, an ipv4 one its address
func TestLoginKeysIPv6ByItsSlash64(t *testing.T) {
	r := newLogin(t)
	for i := range 5 {
		r.fail(t, 1, fmt.Sprintf("2001:db8:1:2::%x", i+1), "")
	}
	if _, err := r.login("2001:db8:1:2:ffff:ffff:ffff:ffff", "", ownerPassword); !limited(err) {
		t.Fatalf("same /64: %v", err)
	}
	if _, err := r.login("2001:db8:1:3::1", "", ownerPassword); err != nil {
		t.Fatalf("next /64: %v", err)
	}
	r.fail(t, 5, "198.51.100.1", "")
	if _, err := r.login("198.51.100.2", "", ownerPassword); err != nil {
		t.Fatalf("next ipv4 address: %v", err)
	}
	for ip, want := range map[string]string{
		"2001:db8:1:2::1": "2001:db8:1:2::/64", "::ffff:198.51.100.1": "198.51.100.1", "198.51.100.1": "198.51.100.1",
		"fe80::1%eth0": "fe80::/64", "not an ip": "not an ip",
	} {
		if got := auth.BucketKey(ip); got != want {
			t.Errorf("%s: %s, want %s", ip, got, want)
		}
	}
}

// the owner's known browser logs in while strangers have used up the global
// limit (Ruling M7a-R2)
func TestLoginKnownBrowserPassesTheGlobalLimit(t *testing.T) {
	r := newLogin(t)
	g, err := r.login("198.51.100.7", "", ownerPassword)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 4 {
		r.fail(t, 5, fmt.Sprintf("203.0.113.%d", i), "")
	}
	if _, err := r.login("192.0.2.1", "", ownerPassword); !limited(err) {
		t.Fatalf("a stranger after 20 failures: %v", err)
	}
	next, err := r.login("192.0.2.1", g.Browser, ownerPassword)
	if err != nil {
		t.Fatalf("known browser: %v", err)
	}
	if r.known(t, g.Browser) || !r.known(t, next.Browser) {
		t.Fatal("the login did not replace the browser token")
	}
	if !strings.Contains(r.logs.String(), `"msg":"admin_login","bucket":"browser"`) {
		t.Fatalf("log %s", r.logs.String())
	}
}

// a cookie a sibling host set under the same name, before the owner's, shadows
// nothing: the first live value is the browser, and it is the one replaced
func TestLoginKnownBrowserBehindAShadow(t *testing.T) {
	r := newLogin(t)
	g, err := r.login("198.51.100.7", "", ownerPassword)
	if err != nil {
		t.Fatal(err)
	}
	other, err := r.login("198.51.100.8", "", ownerPassword)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 4 {
		r.fail(t, 5, fmt.Sprintf("203.0.113.%d", i), "")
	}
	dead := strings.Repeat("A", len(g.Browser))
	next, err := r.l.Login(context.Background(), "192.0.2.1", []string{"sibling", dead, g.Browser, other.Browser}, ownerPassword)
	if err != nil {
		t.Fatalf("known browser behind a shadow: %v", err)
	}
	if r.known(t, g.Browser) || !r.known(t, other.Browser) || !r.known(t, next.Browser) {
		t.Fatal("the login replaced another browser than the first live one")
	}
	noSecrets(t, r.logs.String(), g.Browser, other.Browser, next.Browser, ownerPassword)
}

// a known browser has five failures of its own, and the fifth forgets it
func TestLoginKnownBrowserBucketRevokesIt(t *testing.T) {
	r := newLogin(t)
	g, _ := r.login("198.51.100.7", "", ownerPassword)
	r.fail(t, 4, "198.51.100.7", g.Browser)
	if !r.known(t, g.Browser) {
		t.Fatal("forgotten after 4 failures")
	}
	r.fail(t, 1, "198.51.100.7", g.Browser)
	if r.known(t, g.Browser) {
		t.Fatal("known after 5 failures")
	}
	if !strings.Contains(r.logs.String(), `"msg":"admin_browser_revoked","reason":"failures"`) {
		t.Fatalf("log %s", r.logs.String())
	}
	// the browser's failures left the ip's bucket alone; it now counts there
	r.fail(t, 4, "198.51.100.7", g.Browser)
	if _, err := r.login("198.51.100.7", g.Browser, ownerPassword); err != nil {
		t.Fatalf("fifth attempt on the ip bucket: %v", err)
	}
	noSecrets(t, r.logs.String(), g.Browser, ownerPassword)
}

// a known browser's failures count over its life, not per window, so a stolen
// cookie cannot keep guessing four at a time; a success replaces the browser
// and the count starts over, so the owner's own typos never add up
func TestLoginKnownBrowserFailuresCountOverItsLife(t *testing.T) {
	r := newLogin(t)
	const ip = "198.51.100.7"
	g, _ := r.login(ip, "", ownerPassword)
	r.fail(t, 4, ip, g.Browser)
	r.c.Add(auth.LoginWindow)
	r.fail(t, 1, ip, g.Browser)
	if r.known(t, g.Browser) {
		t.Fatal("known after 4 failures, a window's gap and one more")
	}
	next, err := r.login(ip, "", ownerPassword)
	if err != nil {
		t.Fatal(err)
	}
	r.fail(t, 4, ip, next.Browser)
	again, err := r.login(ip, next.Browser, ownerPassword)
	if err != nil {
		t.Fatal(err)
	}
	r.c.Add(auth.LoginWindow)
	r.fail(t, 4, ip, again.Browser)
	if !r.known(t, again.Browser) {
		t.Fatal("a rotated browser kept the failures of the one it replaced")
	}
	noSecrets(t, r.logs.String(), g.Browser, next.Browser, again.Browser, ownerPassword)
}

// a login whose client went (its context ended) fails with its own code, not
// as a store failure, and counts as no attempt; with a browser cookie too
func TestLoginCanceledIsNoStoreFailure(t *testing.T) {
	r := newLogin(t)
	g, err := r.login("198.51.100.7", "", ownerPassword)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, browsers := range [][]string{nil, {g.Browser}} {
		if _, err := r.l.Login(ctx, "198.51.100.7", browsers, ownerPassword); err == nil || errors.Is(err, auth.ErrUnauthorized) {
			t.Fatalf("err %v", err)
		}
	}
	out := r.logs.String()
	if strings.Count(out, `"msg":"admin_login_error","code":"canceled"`) != 2 || strings.Contains(out, `"code":"store"`) {
		t.Fatalf("log %s", out)
	}
	r.fail(t, 5, "198.51.100.7", "")
}

// a hash past the bounds fails the login closed, with a code
func TestLoginBadStoredHashFailsClosed(t *testing.T) {
	r := newLogin(t)
	bad := "$argon2id$v=19$m=65536,t=11,p=2$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	if _, err := r.pool.Exec(context.Background(), "UPDATE admin_users SET password_hash = $1", bad); err != nil {
		t.Fatal(err)
	}
	if _, err := r.login("198.51.100.7", "", ownerPassword); err == nil || errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("err %v", err)
	}
	if !strings.Contains(r.logs.String(), `"msg":"admin_login_error","code":"bad_hash"`) {
		t.Fatalf("log %s", r.logs.String())
	}
}

// parallel guesses from one ip get no more than five password checks
func TestLoginParallelGuessesHoldTheLimit(t *testing.T) {
	r := newLogin(t)
	errs := make(chan error, 10)
	for range 10 {
		go func() {
			_, err := r.login("198.51.100.7", "", "guess guess guess")
			errs <- err
		}()
	}
	var wrong, refused int
	for range 10 {
		switch err := <-errs; {
		case errors.Is(err, auth.ErrUnauthorized):
			wrong++
		case limited(err):
			refused++
		default:
			t.Fatalf("err %v", err)
		}
	}
	if wrong != 5 || refused != 5 {
		t.Fatalf("checked %d, limited %d", wrong, refused)
	}
}

func noSecrets(t *testing.T, out string, secrets ...string) {
	t.Helper()
	for _, s := range secrets {
		if strings.Contains(out, s) {
			t.Fatalf("output carries %q: %s", s, out)
		}
	}
}
