package commands

import (
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/auth"
)

// ha stops and snoozes a ring and sets a timer or a reminder without a level; an alarm, or a reminder with a level,
// needs admin (rule 7)
func TestHAMayCallAlarms(t *testing.T) {
	ha := auth.Scopes{auth.ScopeHA}
	for _, c := range []struct {
		name, args string
		want       bool
	}{
		{"stop_ring", `{}`, true},
		{"snooze", `{"minutes":5}`, true},
		{"set_timer", `{"id":"t1","seconds":60}`, true},
		{"set_reminder", `{"id":"r1","time":"09:00","label":"x"}`, true},
		{"set_reminder", `{"id":"r1","time":"09:00","level":80}`, false},
		{"set_alarm", `{"id":"a","time":"07:00"}`, false},
		{"cancel_alarm", `{"all":true}`, false},
	} {
		if got := mayCall(ha, "alarms", c.name, []byte(c.args)); got != c.want {
			t.Errorf("%s %s = %v", c.name, c.args, got)
		}
	}
	if !mayCall(auth.AdminScopes, "alarms", "set_reminder", []byte(`{"level":80}`)) {
		t.Error("admin refused")
	}
}
