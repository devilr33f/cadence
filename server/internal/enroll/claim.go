package enroll

import (
	"context"
	"errors"
	"time"
)

// The claim flow (device-first enrollment for screens, D17; design §22). Shapes only: no handler serves them yet.
//
//	POST /v2/claim {"board": "android-tv"}
//	  -> 201 {"id", "code", "url", "expiresAt", "pollAfterMs"}: a 6-character code (no 0/O or 1/I), valid 10 min, and
//	     the dashboard url its qr encodes; 429 {"error":"rate_limited"} past the limits per source address or of open
//	     claims; 400 {"error":"unknown_board"}
//	GET /v2/claim/{id}?wait=25s (long poll, the claim's id as its bearer: it is unguessable and never shown)
//	  -> 200 {"state":"pending"} when the wait ends first; {"state":"approved","deviceId","token"} once, exactly as
//	     /v2/enroll answers, after which the claim is gone; {"state":"declined"}, {"state":"expired"}, or
//	     {"state":"removed"} (approved, then the device was deleted before this poll); 404 for an unknown id
//	POST /api/v1/claims/{code}/approve {"name", "room"} (admin) -> 201 the created device; 404 an unknown or expired
//	     code; 409 a name another device has
//	POST /api/v1/claims/{code}/decline (admin) -> 204
//	GET /api/v1/claims (admin) -> the open claims: code, board, source address, created and expiry times
//
// The 8-character dashboard-first code flow (/v2/enroll) stays as it is. ClaimService serves it (claims.go).

// ClaimTTL is how long a claim's code is valid.
const ClaimTTL = 10 * time.Minute

// ClaimCodeAlphabet is the claim code's: no 0/O and no 1/I.
const ClaimCodeAlphabet = "23456789ABCDEFGHJKLMNPQRSTUVWXYZ"

// ClaimCodeLength is the claim code's length, shown as two groups of three.
const ClaimCodeLength = 6

var (
	// ErrClaimRateLimited is a new claim past a source address's limit or the cap on open claims
	ErrClaimRateLimited = errors.New("claim rate limited")
	// ErrClaimNotFound is an unknown, expired or finished claim
	ErrClaimNotFound = errors.New("claim not found")
)

// ClaimState is a claim's phase.
type ClaimState string

const (
	ClaimPending  ClaimState = "pending"
	ClaimApproved ClaimState = "approved"
	ClaimDeclined ClaimState = "declined"
	ClaimExpired  ClaimState = "expired"
	// ClaimRemoved is an approved claim whose device was deleted before the screen took its token
	ClaimRemoved ClaimState = "removed"
)

// Claim is an open claim a screen holds.
type Claim struct {
	// ID is the device's bearer for its polls; never shown, never logged
	ID    string
	Code  string
	Board string
	// Source is the address the claim came from, for the dashboard's list
	Source    string
	CreatedAt time.Time
	ExpiresAt time.Time
}

// ClaimResult is what a poll answers; DeviceID and Token are set once, when State is ClaimApproved.
type ClaimResult struct {
	State    ClaimState
	DeviceID string
	Token    string
}

// Approval is the owner's answer to a claim in the dashboard.
type Approval struct {
	Name string
	// Room is the room's name (the device's room field), empty for none
	Room string
}

// Claims is the claim flow's service. Claims live in memory or a table with expiry; either way an approved claim's
// token is handed out once.
type Claims interface {
	// Start opens a claim for board from source (the caller's address, for its rate limit)
	Start(ctx context.Context, board, source string) (Claim, error)
	// Wait answers once the claim leaves pending or ctx is done (pending)
	Wait(ctx context.Context, id string) (ClaimResult, error)
	// Approve creates the device as CreateDevice would and hands its token to the claim's next poll
	Approve(ctx context.Context, code string, a Approval) (Device, error)
	Decline(ctx context.Context, code string) error
	// Open lists the claims still pending, without their ids
	Open(ctx context.Context) ([]Claim, error)
}
