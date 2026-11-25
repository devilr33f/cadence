package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"git.shino.boo/devilreef/cadence/server/internal/store/db"
)

const (
	// SessionLifetime is a session's absolute expiry and its cookie's Max-Age.
	SessionLifetime = 30 * 24 * time.Hour
	// SessionIdle ends a session nobody used for that long.
	SessionIdle = 7 * 24 * time.Hour
	// last_seen is written at most this often
	touchEvery = time.Minute
	tokenBytes = 32
)

// errStore hides the driver's error, which can quote a row.
var errStore = errors.New("auth: store failed")

// Session is an owner's dashboard session. Token is set only when it opens:
// it is the cookie's value and is stored as its sha256 alone.
type Session struct {
	Token     string
	ExpiresAt time.Time
}

// Grant is what a login opens: a session, and the known-browser token that
// replaces the one the login came with (Ruling M7a-R2).
type Grant struct {
	Session Session
	Browser string
}

// Sessions stores the owner's sessions and known browsers.
type Sessions struct {
	pool *pgxpool.Pool
	q    *db.Queries
	now  func() time.Time
}

func NewSessions(pool *pgxpool.Pool, now func() time.Time) *Sessions {
	return &Sessions{pool: pool, q: db.New(pool), now: now}
}

func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

// newToken is 256 random bits, base64url.
func newToken() (string, error) {
	b := make([]byte, tokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// tokenShaped holds for a string newToken could have made.
func tokenShaped(token string) bool {
	return base64.RawURLEncoding.DecodedLen(len(token)) == tokenBytes
}

// Open opens a session and a known browser for a password check of the
// version passwordAt, replacing the known browser oldBrowser. It is
// ErrUnauthorized, and opens nothing, when the password changed since that
// check.
func (s *Sessions) Open(ctx context.Context, passwordAt time.Time, oldBrowser string) (Grant, error) {
	session, err := newToken()
	if err != nil {
		return Grant{}, err
	}
	browser, err := newToken()
	if err != nil {
		return Grant{}, err
	}
	now := s.now()
	g := Grant{Session: Session{Token: session, ExpiresAt: now.Add(SessionLifetime)}, Browser: browser}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		n, err := q.CreateAdminSession(ctx, db.CreateAdminSessionParams{
			TokenHash: Hash(session), Now: ts(now), ExpiresAt: ts(g.Session.ExpiresAt), PasswordAt: ts(passwordAt),
		})
		switch {
		case err != nil:
			return err
		case n == 0:
			return ErrUnauthorized
		}
		if err := q.CreateAdminBrowser(ctx, db.CreateAdminBrowserParams{TokenHash: Hash(browser), Now: ts(now), ExpiresAt: ts(now.Add(BrowserLifetime))}); err != nil {
			return err
		}
		if oldBrowser == "" {
			return nil
		}
		return q.RevokeAdminBrowserToken(ctx, db.RevokeAdminBrowserTokenParams{TokenHash: Hash(oldBrowser), Now: ts(now)})
	})
	switch {
	case errors.Is(err, ErrUnauthorized):
		return Grant{}, ErrUnauthorized
	case err != nil:
		return Grant{}, errStore
	}
	return g, nil
}

// Validate answers the live session of token, ErrUnauthorized for none (unknown,
// revoked, expired or idle), and marks it seen.
func (s *Sessions) Validate(ctx context.Context, token string) (Session, error) {
	if !tokenShaped(token) {
		return Session{}, ErrUnauthorized
	}
	now := s.now()
	row, err := s.q.GetAdminSession(ctx, db.GetAdminSessionParams{TokenHash: Hash(token), Now: ts(now), IdleSince: ts(now.Add(-SessionIdle))})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return Session{}, ErrUnauthorized
	case err != nil:
		return Session{}, errStore
	}
	if now.Sub(row.LastSeenAt.Time) >= touchEvery {
		if err := s.q.TouchAdminSession(ctx, db.TouchAdminSessionParams{ID: row.ID, Now: ts(now)}); err != nil {
			return Session{}, errStore
		}
	}
	return Session{ExpiresAt: row.ExpiresAt.Time}, nil
}

// Revoke ends the session of token; an unknown or ended one is no error. The
// known browser stays.
func (s *Sessions) Revoke(ctx context.Context, token string) error {
	if err := s.q.RevokeAdminSession(ctx, db.RevokeAdminSessionParams{TokenHash: Hash(token), Now: ts(s.now())}); err != nil {
		return errStore
	}
	return nil
}

// Prune deletes revoked, expired and idle sessions and revoked or expired
// known browsers, and answers how many.
func (s *Sessions) Prune(ctx context.Context) (int64, error) {
	now := s.now()
	n, err := s.q.PruneAdminSessions(ctx, db.PruneAdminSessionsParams{Now: ts(now), IdleSince: ts(now.Add(-SessionIdle))})
	if err != nil {
		return 0, errStore
	}
	m, err := s.q.PruneAdminBrowsers(ctx, ts(now))
	if err != nil {
		return 0, errStore
	}
	return n + m, nil
}

// RunRetention prunes at start and then every interval until ctx ends; stop
// waits for the loop.
func (s *Sessions) RunRetention(ctx context.Context, log *slog.Logger, every time.Duration) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	prune := func() {
		n, err := s.Prune(ctx)
		switch {
		case ctx.Err() != nil:
		case err != nil:
			log.Error("admin_sessions_prune_failed")
		default:
			log.Info("admin_sessions_pruned", "count", n)
		}
	}
	go func() {
		defer close(done)
		prune()
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				prune()
			}
		}
	}()
	return func() { cancel(); <-done }
}
