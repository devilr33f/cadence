package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"

	"git.shino.boo/devilreef/cadence/pkg/rpc"
	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/catalog"
	"git.shino.boo/devilreef/cadence/server/internal/screenplay"
	"git.shino.boo/devilreef/cadence/server/internal/screens/calls"
)

// mediaMethods put media.control and media.item_action in front of the services that own a screen's session: the
// music queue's songs and the catalog sessions the server played. A session neither owns is not_found. A multiroom
// follower screen's session (P4) acts on its leader's session instead: its next moves the group's video, which the
// followers then get.
type mediaMethods struct {
	music interface {
		OwnsSession(deviceID, sessionID string) bool
		ControlMethod() calls.Method
	}
	catalog interface {
		Owns(deviceID, sessionID string) bool
		Control(ctx context.Context, deviceID, sessionID, action, ref string) (*agent.ToolError, error)
		ItemAction(ctx context.Context, deviceID, sessionID, actionID string) error
	}
	// follow maps a follower screen's session to its leader and the session the leader plays now; nil for none
	follow func(deviceID, sessionID string) (leaderID, leaderSession string, ok bool)
	log    *slog.Logger
}

// toLeader is the call as the leader's session takes it when it came from a follower's session, with its arguments
// carrying the leader's session id; ok is false for a follower whose leader plays nothing now
func (m mediaMethods) toLeader(c calls.Call, session string, args func(leaderSession string) any) (calls.Call, bool) {
	if m.follow == nil {
		return c, true
	}
	leader, ls, ok := m.follow(c.DeviceID, session)
	if !ok {
		return c, true
	}
	raw, err := json.Marshal(args(ls))
	if ls == "" || err != nil {
		return c, false
	}
	m.log.Info("follow_control", "device_id", c.DeviceID, "leader_id", leader, "method", c.Method, "rpc_id", c.ID)
	c.DeviceID, c.Args = leader, raw
	return c, true
}

func (m mediaMethods) register(register func(name string, mm calls.Method)) {
	register(rpc.MediaControl, m.control)
	register(rpc.MediaItemAction, m.itemAction)
}

type controlArgs struct {
	SessionID string `json:"sessionId"`
	Action    string `json:"action"`
	Ref       string `json:"ref,omitempty"`
}

func (m mediaMethods) control(ctx context.Context, c calls.Call) calls.Reply {
	var a controlArgs
	if !strictArgs(c.Args, &a) || a.SessionID == "" || len(a.SessionID) > 64 || len(a.Ref) > 256 {
		return calls.Fail(rpc.StatusInvalid, "invalid")
	}
	c, ok := m.toLeader(c, a.SessionID, func(ls string) any { a.SessionID = ls; return a })
	if !ok {
		return calls.Fail(rpc.StatusNotFound, "no_session")
	}
	switch {
	case m.music != nil && m.music.OwnsSession(c.DeviceID, a.SessionID):
		return m.music.ControlMethod()(ctx, c)
	case m.catalog == nil || !m.catalog.Owns(c.DeviceID, a.SessionID):
		return calls.Fail(rpc.StatusNotFound, "no_session")
	}
	switch a.Action {
	case "next", "previous", "jump":
	case "play_next", "remove":
		// a catalog session's up next is its episodes, in their order
		return calls.Fail(rpc.StatusInvalid, "not_supported")
	default:
		return calls.Fail(rpc.StatusInvalid, "invalid")
	}
	te, err := m.catalog.Control(ctx, c.DeviceID, a.SessionID, a.Action, a.Ref)
	switch {
	case te != nil:
		return calls.Fail(playStatusOf(te.Code), te.Code)
	case errors.Is(err, screenplay.ErrNotOwned):
		return calls.Fail(rpc.StatusNotFound, "no_session")
	case errors.Is(err, screenplay.ErrUnknownRef):
		return calls.Fail(rpc.StatusExpired, "unknown_ref")
	case errors.Is(err, screenplay.ErrNoEpisode):
		return calls.Fail(rpc.StatusNotFound, "queue_end")
	case err != nil:
		return calls.Fail(rpc.StatusUnavailable, "unavailable")
	}
	return calls.OK(struct{}{})
}

func (m mediaMethods) itemAction(ctx context.Context, c calls.Call) calls.Reply {
	var a struct {
		SessionID string `json:"sessionId"`
		ActionID  string `json:"actionId"`
	}
	if !strictArgs(c.Args, &a) || a.SessionID == "" || len(a.SessionID) > 64 || a.ActionID == "" || len(a.ActionID) > 128 {
		return calls.Fail(rpc.StatusInvalid, "invalid")
	}
	c, ok := m.toLeader(c, a.SessionID, func(ls string) any { a.SessionID = ls; return a })
	if !ok {
		return calls.Fail(rpc.StatusNotFound, "no_session")
	}
	switch {
	case m.music != nil && m.music.OwnsSession(c.DeviceID, a.SessionID):
		// a song of the queue has no item actions of its own
		return calls.Fail(rpc.StatusExpired, "unknown_action")
	case m.catalog == nil || !m.catalog.Owns(c.DeviceID, a.SessionID):
		return calls.Fail(rpc.StatusNotFound, "no_session")
	}
	err := m.catalog.ItemAction(ctx, c.DeviceID, a.SessionID, a.ActionID)
	switch {
	case err == nil:
		m.log.Info("media_item_action", "device_id", c.DeviceID, "rpc_id", c.ID)
		return calls.OK(struct{}{})
	case errors.Is(err, screenplay.ErrNotOwned):
		return calls.Fail(rpc.StatusNotFound, "no_session")
	case errors.Is(err, screenplay.ErrUnknownAction):
		return calls.Fail(rpc.StatusExpired, "unknown_action")
	}
	why, code := catalog.RefusalOf(err)
	m.log.Info("media_item_action_refused", "device_id", c.DeviceID, "rpc_id", c.ID, "code", string(why), "failure", string(code))
	switch why {
	case catalog.RefusalUnknownRef, catalog.RefusalUnknownAct:
		return calls.Fail(rpc.StatusExpired, "unknown_action")
	case catalog.RefusalRefused, catalog.RefusalSignIn:
		return calls.Fail(rpc.StatusRefused, string(why))
	}
	return calls.Fail(rpc.StatusUnavailable, "unavailable")
}

// playStatusOf is media.play's status for a play's tool error code
func playStatusOf(code string) string {
	switch code {
	case "unknown_ref":
		return rpc.StatusExpired
	case "not_found":
		return rpc.StatusNotFound
	case "unplayable", "refused", "sign_in_required", "unsupported":
		return rpc.StatusRefused
	case "no_screen":
		return rpc.StatusInvalid
	}
	return rpc.StatusUnavailable
}

func strictArgs(raw []byte, into any) bool {
	if len(raw) == 0 {
		return false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(into) != nil {
		return false
	}
	return !dec.More()
}
