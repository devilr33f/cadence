package enroll

import (
	"context"
	"crypto/rand"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"git.shino.boo/devilreef/cadence/server/internal/auth"
	"git.shino.boo/devilreef/cadence/server/internal/devicename"
	"git.shino.boo/devilreef/cadence/server/internal/events"
	"git.shino.boo/devilreef/cadence/server/internal/store/db"
)

// the limits a claim starts within: open claims in all and per source address, and claims started per source address
// in a window. a screen opens one every 10 min at most (a fresh code after an expiry), and again after a decline
const (
	maxOpenClaims       = 20
	maxOpenPerSource    = 2
	maxStartsPerSource  = 10
	startWindow         = 10 * time.Minute
	claimPruneAfter     = time.Hour
	claimRecheck        = 2 * time.Second
	claimIDPrefix       = "cdc_"
	claimCodeCollisions = 5
)

// ErrUnknownBoard is a claim for a board no device can be.
var ErrUnknownBoard = errors.New("unknown board")

// ClaimStore is what the claims keep their rows in: the pool.
type ClaimStore interface {
	db.DBTX
	Begin(ctx context.Context) (pgx.Tx, error)
}

// ClaimService is Claims in a table with expiry. The claim's id (its bearer) is kept only as a hash; an approval
// creates the device pending and the screen's next poll enrols it with a token made there, so no token is stored.
type ClaimService struct {
	store ClaimStore
	q     *db.Queries
	now   func() time.Time
	bus   events.Publisher
	log   *slog.Logger

	mu      sync.Mutex
	starts  map[string][]time.Time
	changed chan struct{}
}

// NewClaims makes the claim service on the pool.
func NewClaims(store ClaimStore, now func() time.Time, log *slog.Logger) *ClaimService {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &ClaimService{store: store, q: db.New(store), now: now, log: log, starts: map[string][]time.Time{},
		changed: make(chan struct{})}
}

// PublishTo makes each claim a screen takes up a device_added event on bus.
func (s *ClaimService) PublishTo(bus events.Publisher) { s.bus = bus }

// NewClaimCode is a random claim code: 6 characters of ClaimCodeAlphabet.
func NewClaimCode() (string, error) {
	b := make([]byte, ClaimCodeLength)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	for i := range b {
		b[i] = ClaimCodeAlphabet[int(b[i])%len(ClaimCodeAlphabet)] // 32 letters: 256 is a multiple, no bias
	}
	return string(b), nil
}

// NormalizeClaimCode maps a typed code ("k7m 4qx", "K7M-4QX") to its stored form; false for one that cannot be a
// claim code.
func NormalizeClaimCode(in string) (string, bool) {
	out := make([]byte, 0, ClaimCodeLength)
	for _, r := range in {
		switch {
		case r == ' ' || r == '-' || r == '·':
			continue
		case r >= 'a' && r <= 'z':
			r -= 'a' - 'A'
		}
		if r < 0 || r > 0x7f || len(out) == ClaimCodeLength {
			return "", false
		}
		out = append(out, byte(r)) //nolint:gosec // ascii, checked above
	}
	if len(out) != ClaimCodeLength {
		return "", false
	}
	for _, c := range out {
		if !containsByte(ClaimCodeAlphabet, c) {
			return "", false
		}
	}
	return string(out), true
}

func containsByte(s string, c byte) bool {
	for i := range len(s) {
		if s[i] == c {
			return true
		}
	}
	return false
}

// allow records a start from source and says whether it is within the window's limit
func (s *ClaimService) allow(source string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.starts[source][:0]
	for _, t := range s.starts[source] {
		if now.Sub(t) < startWindow {
			kept = append(kept, t)
		}
	}
	if len(kept) >= maxStartsPerSource {
		s.starts[source] = kept
		return false
	}
	s.starts[source] = append(kept, now)
	// the map forgets sources whose window ended, so it holds the last 10 minutes' callers alone
	for src, ts := range s.starts {
		if len(ts) == 0 || now.Sub(ts[len(ts)-1]) >= startWindow {
			delete(s.starts, src)
		}
	}
	return true
}

// Start opens a claim for board from source.
func (s *ClaimService) Start(ctx context.Context, board, source string) (Claim, error) {
	if !knownModel(board) {
		return Claim{}, ErrUnknownBoard
	}
	if len(source) > 64 {
		source = source[:64]
	}
	now := s.now()
	open, err := s.q.CountOpenClaims(ctx, db.CountOpenClaimsParams{Source: source, Now: ts(now)})
	if err != nil {
		return Claim{}, err
	}
	if open.Total >= maxOpenClaims || open.FromSource >= maxOpenPerSource || !s.allow(source, now) {
		return Claim{}, ErrClaimRateLimited
	}
	id, idHash, err := auth.NewToken(claimIDPrefix)
	if err != nil {
		return Claim{}, err
	}
	c := Claim{ID: id, Board: board, Source: source, CreatedAt: now, ExpiresAt: now.Add(ClaimTTL)}
	for range claimCodeCollisions {
		if c.Code, err = NewClaimCode(); err != nil {
			return Claim{}, err
		}
		err = s.q.CreateClaim(ctx, db.CreateClaimParams{
			IDHash: idHash, Code: c.Code, Board: board, Source: source, CreatedAt: ts(now), ExpiresAt: ts(c.ExpiresAt),
		})
		if !codeTaken(err) {
			break
		}
	}
	if err != nil {
		return Claim{}, err
	}
	s.log.Info("claim_started", "board", board)
	s.poke()
	return c, nil
}

func codeTaken(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23505" && pg.ConstraintName == "device_claims_open_code"
}

// poke wakes every waiting poll to read its claim again
func (s *ClaimService) poke() {
	s.mu.Lock()
	close(s.changed)
	s.changed = make(chan struct{})
	s.mu.Unlock()
}

func (s *ClaimService) wake() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.changed
}

// Wait answers once the claim leaves pending or ctx is done (pending). An approved claim's first answer enrols its
// device and carries the token; the claim is gone after it (ErrClaimNotFound).
func (s *ClaimService) Wait(ctx context.Context, id string) (ClaimResult, error) {
	for {
		wake := s.wake()
		// the wait's end never cuts a read short, nor the enrolment of a claim just approved
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		r, err := s.check(cctx, id)
		cancel()
		if err != nil || r.State != ClaimPending {
			return r, err
		}
		t := time.NewTimer(claimRecheck)
		select {
		case <-ctx.Done():
			t.Stop()
			return ClaimResult{State: ClaimPending}, nil
		case <-wake:
			t.Stop()
		case <-t.C:
		}
	}
}

func (s *ClaimService) check(ctx context.Context, id string) (ClaimResult, error) {
	idHash := auth.Hash(id)
	row, err := s.q.ClaimByID(ctx, idHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return ClaimResult{}, ErrClaimNotFound
	}
	if err != nil {
		return ClaimResult{}, err
	}
	now := s.now()
	switch row.State {
	case "declined":
		return ClaimResult{State: ClaimDeclined}, nil
	case "done":
		return ClaimResult{}, ErrClaimNotFound
	case "approved":
		if !row.DeviceID.Valid {
			return ClaimResult{State: ClaimRemoved}, nil
		}
		return s.take(ctx, idHash, now)
	}
	if !row.ExpiresAt.Time.After(now) {
		return ClaimResult{State: ClaimExpired}, nil
	}
	return ClaimResult{State: ClaimPending}, nil
}

// take enrols an approved claim's device with a token made now, once
func (s *ClaimService) take(ctx context.Context, idHash []byte, now time.Time) (ClaimResult, error) {
	token, hash, err := auth.NewToken(auth.DevicePrefix)
	if err != nil {
		return ClaimResult{}, err
	}
	row, err := s.q.TakeClaim(ctx, db.TakeClaimParams{TokenHash: hash, Now: ts(now), IDHash: idHash})
	if errors.Is(err, pgx.ErrNoRows) {
		// another poll took it first, or the device left pending meanwhile
		return ClaimResult{}, ErrClaimNotFound
	}
	if err != nil {
		return ClaimResult{}, err
	}
	dev := row.ID.String()
	s.log.Info("claim_enrolled", "device_id", dev)
	if s.bus != nil {
		s.bus.Publish(events.Event{Type: events.DeviceAdded, DeviceID: dev, At: now,
			Data: events.DeviceData{Name: row.Name, Model: row.Model}})
	}
	return ClaimResult{State: ClaimApproved, DeviceID: dev, Token: token}, nil
}

// Approve creates the device, pending, with its name and room, and hands it to the claim's next poll.
func (s *ClaimService) Approve(ctx context.Context, code string, a Approval) (Device, error) {
	norm, ok := NormalizeClaimCode(code)
	if !ok {
		return Device{}, ErrClaimNotFound
	}
	name, ok := devicename.Clean(a.Name)
	if !ok {
		return Device{}, ErrNameInvalid
	}
	room, ok := devicename.CleanRoom(a.Room)
	if !ok {
		return Device{}, ErrRoomInvalid
	}
	now := s.now()
	var d Device
	err := pgx.BeginFunc(ctx, s.store, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		c, err := q.OpenClaimByCode(ctx, db.OpenClaimByCodeParams{Code: norm, Now: ts(now)})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrClaimNotFound
		}
		if err != nil {
			return err
		}
		row, err := q.CreateDevice(ctx, db.CreateDeviceParams{Name: name, Model: c.Board})
		if devicename.Taken(err) {
			return ErrNameTaken
		}
		if err != nil {
			return err
		}
		if _, err := q.SetDeviceRoom(ctx, db.SetDeviceRoomParams{ID: row.ID, Room: room}); err != nil {
			return err
		}
		if err := q.ApproveClaim(ctx, db.ApproveClaimParams{DeviceID: row.ID, Now: ts(now), IDHash: c.IDHash}); err != nil {
			return err
		}
		d = Device{ID: row.ID.String(), Name: row.Name, Model: row.Model, Status: row.Status, Room: room}
		return nil
	})
	if err != nil {
		return Device{}, err
	}
	s.log.Info("claim_approved", "device_id", d.ID)
	s.poke()
	return d, nil
}

// Decline ends the claim; its screen's poll answers declined.
func (s *ClaimService) Decline(ctx context.Context, code string) error {
	norm, ok := NormalizeClaimCode(code)
	if !ok {
		return ErrClaimNotFound
	}
	now := s.now()
	err := pgx.BeginFunc(ctx, s.store, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		c, err := q.OpenClaimByCode(ctx, db.OpenClaimByCodeParams{Code: norm, Now: ts(now)})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrClaimNotFound
		}
		if err != nil {
			return err
		}
		return q.DeclineClaim(ctx, db.DeclineClaimParams{Now: ts(now), IDHash: c.IDHash})
	})
	if err != nil {
		return err
	}
	s.log.Info("claim_declined")
	s.poke()
	return nil
}

// Open lists the claims still pending, oldest first.
func (s *ClaimService) Open(ctx context.Context) ([]Claim, error) {
	rows, err := s.q.ListOpenClaims(ctx, ts(s.now()))
	if err != nil {
		return nil, err
	}
	out := make([]Claim, len(rows))
	for i, r := range rows {
		out[i] = Claim{Code: r.Code, Board: r.Board, Source: r.Source, CreatedAt: r.CreatedAt.Time,
			ExpiresAt: r.ExpiresAt.Time}
	}
	return out, nil
}

// Run prunes the claims that ended an hour ago, now and every few minutes, until ctx ends; the returned func waits
// for it.
func (s *ClaimService) Run(ctx context.Context) func() {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(5 * time.Minute)
		defer t.Stop()
		for {
			if n, err := s.q.PruneClaims(ctx, pgtype.Timestamptz{Time: s.now().Add(-claimPruneAfter), Valid: true}); err != nil && ctx.Err() == nil {
				s.log.Warn("claims_prune_failed")
			} else if n > 0 {
				s.log.Info("claims_pruned", "count", n)
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}

var _ Claims = (*ClaimService)(nil)
