package auth

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"git.shino.boo/devilreef/cadence/server/internal/store/db"
)

const (
	// BrowserLifetime is a known browser's expiry and its cookie's Max-Age.
	BrowserLifetime = 365 * 24 * time.Hour
	// BrowserFailures failed logins over a known browser's life forget it;
	// a success replaces it with a fresh one.
	BrowserFailures = 5
)

// browser is the live known browser of token, ErrUnauthorized for none.
func (s *Sessions) browser(ctx context.Context, token string) (pgtype.UUID, error) {
	if !tokenShaped(token) {
		return pgtype.UUID{}, ErrUnauthorized
	}
	id, err := s.q.GetAdminBrowser(ctx, db.GetAdminBrowserParams{TokenHash: Hash(token), Now: ts(s.now())})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return pgtype.UUID{}, ErrUnauthorized
	case err != nil:
		return pgtype.UUID{}, errStore
	}
	return id, nil
}

// KnownBrowser reports whether token is a live known browser.
func (s *Sessions) KnownBrowser(ctx context.Context, token string) (bool, error) {
	_, err := s.browser(ctx, token)
	if errors.Is(err, ErrUnauthorized) {
		return false, nil
	}
	return err == nil, err
}

// failBrowser counts a failed login with a known browser and reports whether
// this one forgot it, at BrowserFailures; a browser a parallel failure forgot
// first counts no more.
func (s *Sessions) failBrowser(ctx context.Context, id pgtype.UUID) (bool, error) {
	n, err := s.q.FailAdminBrowser(ctx, db.FailAdminBrowserParams{ID: id, MaxFailures: BrowserFailures, Now: ts(s.now())})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return false, nil
	case err != nil:
		return false, errStore
	}
	return n >= BrowserFailures, nil
}
