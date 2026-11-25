package capability

import (
	"context"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

// Vouched is a session of another device the server vouched for, for the one command a provider runs now: core has
// checked the grant's target, capability, command, expiry and single use, and that the session was woken.
type Vouched struct {
	CallerDeviceID string
	SessionID      string
	Trigger        cadencev2.Trigger
}

type vouchedKey struct{}

// WithVouched is ctx carrying v for one command.
func WithVouched(ctx context.Context, v Vouched) context.Context {
	return context.WithValue(ctx, vouchedKey{}, v)
}

// VouchedFrom is the session the server vouched for in this command, false for none.
func VouchedFrom(ctx context.Context) (Vouched, bool) {
	v, ok := ctx.Value(vouchedKey{}).(Vouched)
	return v, ok
}

// Sessions tells how a recent session of this device began; an id it does not know is false.
type Sessions interface {
	Trigger(sessionID string) (cadencev2.Trigger, bool)
}

// SessionTrigger is how session began: this device's own record first, else the session the server vouched for in
// this command, when it is that one. A vouched session is never one of the device's: rule 7 on the target holds
// through the caller's session, which the server and the grant say was woken.
func SessionTrigger(ctx context.Context, sessions Sessions, session string) (cadencev2.Trigger, bool) {
	if session == "" {
		return cadencev2.Trigger_TRIGGER_UNSPECIFIED, false
	}
	if sessions != nil {
		if t, ok := sessions.Trigger(session); ok {
			return t, true
		}
	}
	if v, ok := VouchedFrom(ctx); ok && v.SessionID == session {
		return v.Trigger, true
	}
	return cadencev2.Trigger_TRIGGER_UNSPECIFIED, false
}
