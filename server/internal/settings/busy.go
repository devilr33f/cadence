package settings

import (
	"bytes"
	"encoding/json"
	"slices"
	"unicode/utf8"
)

// BusyEntity is one Home Assistant state that makes every device busy while
// the entity holds it: a phone's call state, a computer's microphone in use.
type BusyEntity struct {
	Entity string `json:"entity" maxLength:"128" pattern:"^[a-z][a-z0-9_]*\\.[a-z0-9_]+$"`
	State  string `json:"state" minLength:"1" maxLength:"255"`
	Reason string `json:"reason" enum:"call,meeting,screenshare,dnd"`
}

// BusyReasons are the reasons a busy entity may give, as presence takes them.
var BusyReasons = []string{"call", "meeting", "screenshare", "dnd"}

const (
	maxBusyEntities  = 32
	maxBusyStateLen  = 255
	busyEntityFields = 3
)

// validBusyEntities checks a busyEntities value: at most maxBusyEntities
// objects of exactly entity, state and reason, each pair of entity and state
// once (an entity may list several states).
func validBusyEntities(raw json.RawMessage) bool {
	var items []map[string]json.RawMessage
	if json.Unmarshal(raw, &items) != nil || items == nil || len(items) > maxBusyEntities {
		return false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var list []BusyEntity
	if dec.Decode(&list) != nil {
		return false
	}
	for i, e := range list {
		if len(items[i]) != busyEntityFields {
			return false
		}
		n := utf8.RuneCountInString(e.State)
		if len(e.Entity) > maxHAEntityID || !haEntityRe.MatchString(e.Entity) || n < 1 || n > maxBusyStateLen ||
			!slices.Contains(BusyReasons, e.Reason) {
			return false
		}
		for _, prev := range list[:i] {
			if prev.Entity == e.Entity && prev.State == e.State {
				return false
			}
		}
	}
	return true
}
