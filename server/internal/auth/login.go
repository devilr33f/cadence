package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/netip"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// a login reads at most this many of its known-browser cookie's values from
// the store
const maxBrowserLookups = 4

// RateLimitedError refuses a login while its bucket or the global one has too
// many recent failures; RetryAfter is when the oldest of them stops counting.
type RateLimitedError struct{ RetryAfter time.Duration }

func (e *RateLimitedError) Error() string { return "auth: rate limited" }

// Login is the owner's dashboard login: password checks under the rate limit,
// and the sessions and known browsers they open.
type Login struct {
	owner    *Owner
	sessions *Sessions
	limit    *limiter
	log      *slog.Logger
	// the ip hash's key, made at start and never written anywhere (Ruling M7a-R3)
	hashKey []byte
}

func NewLogin(owner *Owner, sessions *Sessions, log *slog.Logger, now func() time.Time) *Login {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	l := &Login{owner: owner, sessions: sessions, limit: newLimiter(now), log: log, hashKey: []byte(rand.Text())}
	l.limit.report = l.reportWindow
	return l
}

// bucketKey is a client's limit bucket: an ipv4 address whole, an ipv6 one by
// its /64 (a host usually holds the whole prefix).
func bucketKey(ip string) string {
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return ip
	}
	a = a.Unmap().WithZone("")
	if a.Is4() {
		return a.String()
	}
	p, err := a.Prefix(64)
	if err != nil {
		return ip
	}
	return p.String()
}

// ipHash names a client's bucket in a log without its address, linkable only
// within one run.
func (l *Login) ipHash(key string) string {
	m := hmac.New(sha256.New, l.hashKey)
	m.Write([]byte(key))
	return hex.EncodeToString(m.Sum(nil)[:4])
}

func bucketKind(key string) string {
	kind, _, _ := strings.Cut(key, ":")
	return kind
}

func (l *Login) reportWindow(key string, count int) {
	args := []any{"bucket", bucketKind(key)}
	if ip, ok := strings.CutPrefix(key, "ip:"); ok {
		args = append(args, "ip_hash", l.ipHash(ip))
	}
	l.log.Info("admin_login_refused", append(args, "count", count)...)
}

// Login opens a session for the owner's password: ErrUnauthorized for a wrong
// one, *RateLimitedError while its bucket or the global one is full. A live
// known-browser token, the first among browsers, puts the attempt in that
// browser's bucket alone (Ruling M7a-R2); its BrowserFailures-th failure over
// its life forgets the browser.
func (l *Login) Login(ctx context.Context, ip string, browsers []string, password string) (Grant, error) {
	client := bucketKey(ip)
	key, global, kind := "ip:"+client, true, "ip"
	known, browser, err := l.knownBrowser(ctx, browsers)
	if err != nil {
		l.log.Error("admin_login_error", "code", failCode(ctx))
		return Grant{}, err
	}
	if known.Valid {
		key, global, kind = "browser:"+known.String(), false, "browser"
	}
	hash := l.ipHash(client)
	t, r, ok := l.limit.begin(key, global)
	if !ok {
		if r.first {
			l.log.Warn("admin_login_failed", "reason", "rate_limited", "bucket", bucketKind(r.key), "ip_hash", hash)
		}
		return Grant{}, &RateLimitedError{RetryAfter: r.wait}
	}
	match, version, err := l.owner.Check(ctx, password)
	if err != nil {
		l.limit.abandon(t)
		code := failCode(ctx)
		if errors.Is(err, ErrBadHash) {
			code = "bad_hash"
		}
		l.log.Error("admin_login_error", "code", code)
		return Grant{}, err
	}
	if !match {
		l.log.Warn("admin_login_failed", "reason", "password", "bucket", kind, "ip_hash", hash)
		if known.Valid {
			switch forgot, err := l.sessions.failBrowser(ctx, known); {
			case err != nil:
				l.log.Error("admin_login_error", "code", failCode(ctx))
			case forgot:
				l.log.Warn("admin_browser_revoked", "reason", "failures", "ip_hash", hash)
			}
		}
		return Grant{}, ErrUnauthorized
	}
	g, err := l.sessions.Open(ctx, version, browser)
	if err != nil {
		l.limit.abandon(t)
		if errors.Is(err, ErrUnauthorized) {
			l.log.Warn("admin_login_failed", "reason", "password_changed", "bucket", kind, "ip_hash", hash)
			return Grant{}, err
		}
		l.log.Error("admin_login_error", "code", failCode(ctx))
		return Grant{}, err
	}
	l.limit.succeeded(t)
	l.log.Info("admin_login", "bucket", kind, "ip_hash", hash)
	return g, nil
}

// knownBrowser is the first live known browser among a login's cookie values,
// and its token; none is no error. A sibling host can set cookies of that
// name beside the owner's, so every value counts, but at most
// maxBrowserLookups token-shaped ones reach the store.
func (l *Login) knownBrowser(ctx context.Context, browsers []string) (pgtype.UUID, string, error) {
	lookups := 0
	for _, b := range browsers {
		if !tokenShaped(b) {
			continue
		}
		if lookups++; lookups > maxBrowserLookups {
			break
		}
		id, err := l.sessions.browser(ctx, b)
		switch {
		case err == nil:
			return id, b, nil
		case !errors.Is(err, ErrUnauthorized):
			return pgtype.UUID{}, "", err
		}
	}
	return pgtype.UUID{}, "", nil
}

// Sweep ends the limit windows that ran out, logging their refusal counts.
func (l *Login) Sweep() { l.limit.sweep() }

// Run sweeps every interval until ctx ends, so a window's count is logged
// soon after it ends even with no login; stop waits for the loop.
func (l *Login) Run(ctx context.Context, every time.Duration) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				l.Sweep()
			}
		}
	}()
	return func() { cancel(); <-done }
}

// Validate answers the live session of a cookie's token.
func (l *Login) Validate(ctx context.Context, token string) (Session, error) {
	return l.sessions.Validate(ctx, token)
}

// Logout revokes the session of a cookie's token; the known browser stays.
func (l *Login) Logout(ctx context.Context, token string) error { return l.sessions.Revoke(ctx, token) }

// failCode names a failed store call for the log: canceled once the login's
// context ended (its client went), else store.
func failCode(ctx context.Context) string {
	if ctx.Err() != nil {
		return "canceled"
	}
	return "store"
}
