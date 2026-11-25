package commands

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"git.shino.boo/devilreef/cadence/server/internal/auth"
)

// haCommands are the capability commands an ha token may send (Ruling
// M7a-R4, M8-D5): the level, playback with its skips and URL play; on a
// screen, a video's play and the remote's keys (tv plan, stream H). A key
// moves the level only as the screen's own remote would (input.remote's
// rule 7 note); an ha caller may set the volume anyway. Every other command,
// an alarm's for one, needs admin. A ring's stop and snooze, a timer and a
// reminder without a level (it rings at the level in force) are ha's too
// (owner, 2026-11-16); setting an alarm never is (rule 7).
var haCommands = map[string][]string{
	"alarms":       {"stop_ring", "snooze", "set_timer", "set_reminder"},
	"audio.output": {"set_level", "change_level"},
	"input.remote": {"key"},
	"media.player": {"pause", "resume", "stop", "seek", "next", "previous"},
	"media.url":    {"play"},
	"media.video":  {"play"},
}

// HACommands is a copy of the ha allow-list, by capability.
func HACommands() map[string][]string {
	out := make(map[string][]string, len(haCommands))
	for c, names := range haCommands {
		out[c] = slices.Clone(names)
	}
	return out
}

// haDeniedArgs are the args an ha caller may not give a command it may send: a reminder with a level ramps like an
// alarm, so it needs a woken session (rule 7)
var haDeniedArgs = map[string][]string{"alarms.set_reminder": {"level"}}

// mayCall says whether a caller of these scopes may send the command with these args.
func mayCall(s auth.Scopes, capability, name string, args []byte) bool {
	if s.Allows(auth.ScopeAdmin) {
		return true
	}
	if !s.Allows(auth.ScopeHA) || !slices.Contains(haCommands[capability], name) {
		return false
	}
	denied := haDeniedArgs[capability+"."+name]
	if len(denied) == 0 {
		return true
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(args, &m) != nil {
		// the command's schema refuses it anyway
		return true
	}
	for _, k := range denied {
		if _, ok := m[k]; ok {
			return false
		}
	}
	return true
}

// haNote lists the allow-list for the document.
func haNote() string {
	var parts []string
	for _, c := range slices.Sorted(maps.Keys(haCommands)) {
		parts = append(parts, fmt.Sprintf("%s (%s)", c, strings.Join(haCommands[c], ", ")))
	}
	return "An ha token may send only " + strings.Join(parts, "; ") + ", alarms.set_reminder without a level" +
		"; any other command needs admin (403 forbidden)."
}
