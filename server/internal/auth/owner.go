package auth

import (
	"context"
	"errors"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"git.shino.boo/devilreef/cadence/server/internal/store/db"
)

// MinPasswordLen is the shortest owner password, in characters.
const MinPasswordLen = 12

// ErrPasswordShort refuses a password under MinPasswordLen characters.
var ErrPasswordShort = errors.New("auth: password too short")

// argon2 at 64 MiB each: checks beyond this many wait, so a burst of logins
// cannot exhaust memory
const checkSlots = 2

// Owner is the one dashboard account (M7a-D4).
type Owner struct {
	pool   *pgxpool.Pool
	q      *db.Queries
	now    func() time.Time
	slots  chan struct{}
	params Params
	verify func(encoded, password string) (bool, error)
}

func NewOwner(pool *pgxpool.Pool, now func() time.Time) *Owner {
	return &Owner{pool: pool, q: db.New(pool), now: now, slots: make(chan struct{}, checkSlots), params: DefaultParams, verify: VerifyPassword}
}

// Revoked counts what a password change ended.
type Revoked struct{ Sessions, Browsers int64 }

// SetPassword stores the password's hash and revokes every session and known
// browser, in one transaction.
func (o *Owner) SetPassword(ctx context.Context, password string) (Revoked, error) {
	if utf8.RuneCountInString(password) < MinPasswordLen {
		return Revoked{}, ErrPasswordShort
	}
	hash, err := o.params.Hash(password)
	if err != nil {
		return Revoked{}, err
	}
	var r Revoked
	err = pgx.BeginFunc(ctx, o.pool, func(tx pgx.Tx) error {
		q := o.q.WithTx(tx)
		now := ts(o.now())
		if err := q.SetAdminPassword(ctx, db.SetAdminPasswordParams{PasswordHash: hash, Now: now}); err != nil {
			return err
		}
		if r.Sessions, err = q.RevokeAdminSessions(ctx, now); err != nil {
			return err
		}
		r.Browsers, err = q.RevokeAdminBrowsers(ctx, now)
		return err
	})
	if err != nil {
		return Revoked{}, errStore
	}
	return r, nil
}

// Check reports whether password is the owner's, and the version (its
// updated_at) it checked, which a session opens against. With no password set
// it runs the same argon2 work against a fixed hash and answers false, so
// neither the answer nor its timing tells whether one is set. A stored hash it
// cannot read or will not run is ErrBadHash.
func (o *Owner) Check(ctx context.Context, password string) (bool, time.Time, error) {
	row, err := o.q.GetAdminPassword(ctx)
	set := true
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		row.PasswordHash, set = dummyHash, false
	case err != nil:
		return false, time.Time{}, errStore
	}
	select {
	case o.slots <- struct{}{}:
	case <-ctx.Done():
		return false, time.Time{}, ctx.Err()
	}
	ok, err := o.verify(row.PasswordHash, password)
	<-o.slots
	return ok && set, row.UpdatedAt.Time, err
}
