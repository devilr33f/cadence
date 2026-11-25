package commands

import (
	"context"
	"crypto/rand"
	"time"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

// grantTTL is how long a session grant holds: past the 10 s a command waits, well inside the minute a device takes
const grantTTL = 30 * time.Second

// Vouch is the turn's session a command on another device runs for (TV plan item 14): the target does not know it,
// so the server vouches for it with a session grant bound to that one command.
type Vouch struct {
	DeviceID, SessionID string
	Trigger             cadencev2.Trigger
}

type vouchKey struct{}

// WithVouch has every command sent under ctx to another device than v's carry a grant for v's session, when it is one
// the user began with the wake word or a press; a command to v's own device, or for any other session, carries none.
func WithVouch(ctx context.Context, v Vouch) context.Context {
	return context.WithValue(ctx, vouchKey{}, v)
}

// VouchFrom is the session ctx vouches for, false for none.
func VouchFrom(ctx context.Context) (Vouch, bool) {
	v, ok := ctx.Value(vouchKey{}).(Vouch)
	return v, ok
}

// grant is a fresh single-use grant for the command, nil when ctx vouches for no woken session of another device
func (s *Service) grant(ctx context.Context, dev, capability, name string) *cadencev2.SessionGrant {
	v, ok := VouchFrom(ctx)
	woken := v.Trigger == cadencev2.Trigger_TRIGGER_WAKE || v.Trigger == cadencev2.Trigger_TRIGGER_BUTTON
	if !ok || !woken || v.DeviceID == "" || v.SessionID == "" || v.DeviceID == dev {
		return nil
	}
	return &cadencev2.SessionGrant{
		Id: rand.Text(), CallerDeviceId: v.DeviceID, CallerSessionId: v.SessionID, CallerTrigger: v.Trigger,
		TargetDeviceId: dev, Capability: capability, Command: name,
		ExpiresAtUnixMs: s.now().Add(grantTTL).UnixMilli(),
	}
}
