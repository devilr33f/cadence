package tools

import (
	"context"

	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/integrations/ha"
	"git.shino.boo/devilreef/cadence/server/internal/settings"
)

// sensitiveDomains are where a control can open the house, disarm it, flood
// it or run whatever the owner scripted (M5c-D7, Ruling M5c-R5); a scene or
// an old-style group fans out to entities the target check cannot see, a
// lock among them (Ruling M5c-R6), and so do a helper button's automations
// and a conversation agent, which acts on whatever its text asks.
var sensitiveDomains = map[string]bool{
	"lock": true, "alarm_control_panel": true, "valve": true, "siren": true,
	"script": true, "automation": true, "update": true, "button": true,
	"input_button": true, "scene": true, "group": true, "conversation": true,
}

// sensitiveCovers are the cover classes that are a way in.
var sensitiveCovers = map[string]bool{"garage": true, "gate": true, "door": true}

// sensitive reports an entity whose control needs the owner's consent: the
// target decides, whatever service reaches it (homeassistant.turn_off on a
// lock is still the lock).
func sensitive(e ha.Entity) bool {
	if sensitiveDomains[e.Domain] {
		return true
	}
	return e.Domain == "cover" && e.Attrs.DeviceClass != nil && sensitiveCovers[*e.Attrs.DeviceClass]
}

// HASensitivePolicy guards ha_control (rule-7 style consent, Ruling M5c-R3):
// a sensitive entity needs a session the user began with the wake word or a
// press, or is refused outright when the global haSensitive setting is deny;
// the others take any microphone turn. An event turn, which is never offered
// ha_control, changes nothing.
func HASensitivePolicy(s SettingsFunc) HAPolicy {
	return func(ctx context.Context, c agent.Call, e ha.Entity, _, _ string) *agent.ToolError {
		if agent.EventTurn(c.Trigger) {
			return agent.NotInEventTurn()
		}
		if !sensitive(e) {
			return nil
		}
		v, err := s(ctx, c.DeviceID)
		switch {
		case err != nil:
			return internal()
		case v.HASensitive == settings.HASensitiveDeny:
			return &agent.ToolError{
				Code:    "not_supported",
				Message: "Voice control of locks, alarms, gates and other sensitive devices is turned off in the settings.",
				Hint:    "Tell the user it is turned off in the settings.",
			}
		case !agent.Woken(c.Trigger):
			return agent.WakeRequired()
		}
		return nil
	}
}

// Sensitive reports an entity HASensitivePolicy guards: a screen card asks the
// user to confirm any control of one (W0-C item 12).
func Sensitive(e ha.Entity) bool { return sensitive(e) }
