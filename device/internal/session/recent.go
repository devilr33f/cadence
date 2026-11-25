package session

import (
	"sync"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

const recentSessions = 16

// Recent remembers how the last 16 sessions began, so a capability can tell a
// session the user woke from any other (rule 7). The zero value is ready.
type Recent struct {
	mu       sync.Mutex
	ids      [recentSessions]string
	triggers [recentSessions]cadencev2.Trigger
	next     int
}

// Record keeps id, dropping the oldest of 16.
func (r *Recent) Record(id string, t cadencev2.Trigger) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ids[r.next], r.triggers[r.next] = id, t
	r.next = (r.next + 1) % recentSessions
}

// Trigger is how session id began, false when it is not among the last 16.
func (r *Recent) Trigger(id string) (cadencev2.Trigger, bool) {
	if id == "" {
		return cadencev2.Trigger_TRIGGER_UNSPECIFIED, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range recentSessions {
		// newest first, should an id ever come twice
		j := (r.next - 1 - i + recentSessions) % recentSessions
		if r.ids[j] == id {
			return r.triggers[j], true
		}
	}
	return cadencev2.Trigger_TRIGGER_UNSPECIFIED, false
}
