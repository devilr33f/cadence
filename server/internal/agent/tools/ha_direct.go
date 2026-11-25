package tools

import (
	"context"
	"slices"
	"strings"

	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/integrations/ha"
)

// DirectHomeControl turns one home device on or off without the model: a direct
// command's own path (voice's включи/выключи by name). It takes ha_control's
// resolution, guards and policy, so what the model may do and what a direct
// command may do cannot drift apart; it does not wait for the state to confirm,
// which the cards and the history show. The spoken light word is apart: it
// means the speaker's whole room, not one named device (roomLights). The name
// the spoken line says and "" is a call that landed; any other answer is a
// reason the caller hands the request to the agent for (not_found, ambiguous,
// unlisted, refused:<code>, unavailable, canceled, failed), never model text.
// room is the asking device's own room name, which names its HA area when the
// setting does not.
func DirectHomeControl(ctx context.Context, d HADeps, c agent.Call, on bool, target, room string) (string, string) {
	action := "turn_off"
	if on {
		action = "turn_on"
	}
	a := controlArgs{action: action, target: target}
	v, err := d.view()
	if err != nil {
		return "", "unavailable"
	}
	can := func(x ha.Entity) bool { return canTake(a, x, v.Services) }
	if spokenLight[normalize(target)] {
		ms, reason := roomLights(ctx, d, c, a, v, room, can)
		if reason != "" {
			return "", reason
		}
		return callEach(ctx, d, c, a, v, ms, target)
	}
	ms, err := d.narrow(ctx, c.DeviceID, matches(v.Entities, a.target), can)
	switch {
	case err != nil:
		return "", "unavailable"
	case len(ms) == 0:
		return "", "not_found"
	case len(ms) > 1:
		return "", "ambiguous"
	}
	ent := ms[0]
	switch {
	case ent.Platform == cadencePlatform:
		return "", "cadence"
	case ent.EntryUnreadable:
		return "", "unreadable"
	case ent.PlatformUnknown:
		return "", "unlisted"
	}
	return callEach(ctx, d, c, a, v, ms, displayName(ent))
}

// callEach makes a's call on every entity, with the model tool's own guards and
// policy per entity; name is what the spoken line says was done. A water heater
// without the on/off feature is already on in any operation mode.
func callEach(ctx context.Context, d HADeps, c agent.Call, a controlArgs, v ha.View, ms []ha.Entity, name string) (string, string) {
	for _, ent := range ms {
		if a.action == "turn_on" && ent.Domain == "water_heater" && !ent.HasFeature(ha.WaterHeaterOnOff) && heaterOn(ent) {
			continue
		}
		call, e := planCall(a, ent, v.Services)
		if e != nil {
			return "", "refused:" + e.Code
		}
		if d.Policy != nil {
			if e := d.Policy(ctx, c, ent, call.domain, call.service); e != nil {
				return "", "refused:" + e.Code
			}
		}
		// a barge-in ends the turn, and an ended turn must not change the home
		if ctx.Err() != nil {
			return "", "canceled"
		}
		if err := d.Home.Client().Call(ctx, call.domain, call.service, call.data, ent.ID); err != nil {
			return "", "failed"
		}
		d.record(ctx, c.DeviceID, ent.ID)
	}
	return name, ""
}

// spokenLight is the generic word for a room's lighting, not one named device:
// «включи свет» means every light of the room the speaker stands in, so the
// direct answer takes them together and never asks which one.
var spokenLight = map[string]bool{
	"свет": true, "освещение": true, "лампы": true, "лампочки": true,
	"light": true, "lights": true, "the light": true, "the lights": true, "lighting": true,
}

// roomLights are the lights of the asking device's room, whatever each is
// named: its HA area, else the area named as the device's own room. Entities
// the home cannot see are skipped; no area, or no light in it, leaves the turn
// to the model, which can ask and explain.
func roomLights(ctx context.Context, d HADeps, c agent.Call, _ controlArgs, v ha.View, room string, can func(ha.Entity) bool) ([]ha.Entity, string) {
	area := ""
	if s, err := d.Settings(ctx, c.DeviceID); err != nil {
		return nil, "unavailable"
	} else if s.HAArea != "" {
		area = s.HAArea
	} else if room != "" {
		for _, x := range v.Areas {
			if strings.EqualFold(x.Name, room) || slices.ContainsFunc(x.Aliases, func(al string) bool { return strings.EqualFold(al, room) }) {
				area = x.ID
				break
			}
		}
	}
	if area == "" {
		return nil, "no_area"
	}
	var ms []ha.Entity
	for _, e := range v.Entities {
		if e.Area != area || !isLight(e) || gone(e) || !can(e) {
			continue
		}
		switch {
		case e.Platform == cadencePlatform:
			return nil, "cadence"
		case e.EntryUnreadable:
			return nil, "unreadable"
		case e.PlatformUnknown:
			return nil, "unlisted"
		}
		ms = append(ms, e)
	}
	if len(ms) == 0 {
		return nil, "not_found"
	}
	return ms, ""
}

// isLight says e is lighting: the light domain, or another the home classes as
// a light (a switch that switches a dumb bulb).
func isLight(e ha.Entity) bool {
	return e.Domain == "light" || (e.Attrs.DeviceClass != nil && *e.Attrs.DeviceClass == "light")
}
