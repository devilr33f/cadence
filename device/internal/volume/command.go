package volume

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"git.shino.boo/devilreef/cadence/device/internal/capability"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

const maxSessionID = 63

const (
	sessionIDSchema   = `{"type":"string","minLength":1,"maxLength":63}`
	setLevelSchema    = `{"type":"object","properties":{"level":{"type":"integer","minimum":0,"maximum":100},"sessionId":` + sessionIDSchema + `},"required":["level"],"additionalProperties":false}`
	changeLevelSchema = `{"type":"object","properties":{"delta":{"type":"integer","minimum":-100,"maximum":100},"sessionId":` + sessionIDSchema + `},"required":["delta"],"additionalProperties":false}`
)

var (
	errInvalid      = &capability.CommandError{Code: "invalid"}
	errWakeRequired = &capability.CommandError{Code: "wake_required"}
	errStoreFailed  = &capability.CommandError{Code: "store_failed"}
)

// Sessions tells how a recent session began; an id it does not know is false.
type Sessions interface {
	Trigger(sessionID string) (cadencev2.Trigger, bool)
}

// Commands are the schemas of the audio.output commands, by name.
func Commands() map[string]string {
	return map[string]string{"set_level": setLevelSchema, "change_level": changeLevelSchema}
}

// Command runs set_level or change_level on s and answers {"level": N}, the
// level now in force. A raise in a session needs one the user began with the
// wake word or a press (rule 7), on this device or on the one whose session
// the server vouched for in ctx (capability.SessionTrigger); a raise with no
// session is an authenticated api call; a lower or equal level always goes.
func Command(ctx context.Context, s *Setter, sessions Sessions, name string, raw json.RawMessage) (json.RawMessage, error) {
	var target func(cur int) int
	var rawSession json.RawMessage
	switch name {
	case "set_level":
		var a struct {
			Level     *int            `json:"level"`
			SessionID json.RawMessage `json:"sessionId"`
		}
		if decodeObject(raw, &a) != nil || a.Level == nil || *a.Level < minLevel || *a.Level > maxLevel {
			return nil, errInvalid
		}
		target, rawSession = func(int) int { return *a.Level }, a.SessionID
	case "change_level":
		var a struct {
			Delta     *int            `json:"delta"`
			SessionID json.RawMessage `json:"sessionId"`
		}
		if decodeObject(raw, &a) != nil || a.Delta == nil || *a.Delta < -maxLevel || *a.Delta > maxLevel {
			return nil, errInvalid
		}
		target, rawSession = func(cur int) int { return clamp(cur + *a.Delta) }, a.SessionID
	default:
		return nil, capability.ErrUnsupported
	}
	session, ok := sessionID(rawSession)
	if !ok {
		return nil, errInvalid
	}
	level, err := s.update(OriginCommand, func(cur int) (int, error) {
		next := target(cur)
		if next > cur && !mayRaise(ctx, sessions, session) {
			return cur, errWakeRequired
		}
		return next, nil
	})
	switch {
	case errors.Is(err, ErrStore):
		return nil, errStoreFailed
	case err != nil:
		return nil, err
	}
	return fmt.Appendf(nil, `{"level":%d}`, level), nil
}

// sessionID is "" when the key is absent; present, it must be a string of
// 1-63 bytes: an explicit null is refused, never taken for an api call.
func sessionID(raw json.RawMessage) (string, bool) {
	if raw == nil {
		return "", true
	}
	if raw[0] != '"' {
		return "", false
	}
	var id string
	if json.Unmarshal(raw, &id) != nil || id == "" || len(id) > maxSessionID {
		return "", false
	}
	return id, true
}

func mayRaise(ctx context.Context, sessions Sessions, session string) bool {
	if session == "" {
		return true
	}
	t, ok := capability.SessionTrigger(ctx, sessions, session)
	return ok && (t == cadencev2.Trigger_TRIGGER_WAKE || t == cadencev2.Trigger_TRIGGER_BUTTON)
}

// decodeObject decodes one JSON object into v and refuses unknown fields,
// wrong types and anything after it.
func decodeObject(raw []byte, v any) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		return errInvalid
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errInvalid
	}
	return nil
}
