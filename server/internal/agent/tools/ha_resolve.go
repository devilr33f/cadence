package tools

import (
	"context"
	"slices"
	"strings"

	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/integrations/ha"
	"git.shino.boo/devilreef/cadence/server/internal/settings"
)

// what the home tools show of Home Assistant's text, in code points
const (
	haNameLen = 60
	haDescLen = 120
	haAttrLen = 32
)

// HAPolicy decides whether ha_control may call domain.service on e; it runs
// after the entity resolved, before the call. A refusal goes to the model as
// it is, and nothing is called.
type HAPolicy func(ctx context.Context, c agent.Call, e ha.Entity, domain, service string) *agent.ToolError

// HAUsage counts the entities ha_control changed.
type HAUsage interface {
	Record(ctx context.Context, deviceID, entityID string)
}

// HADeps are the home tools' collaborators. The tools log nothing: entity
// names, states and arguments reach only the model, the conversation store
// and the live feed.
type HADeps struct {
	// Home nil: Home Assistant is not set up, and the tools are not offered
	Home     *ha.Home
	Settings SettingsFunc
	// Policy nil allows every control
	Policy HAPolicy
	// Usage nil counts nothing
	Usage HAUsage
	// Confirmations is shared with the cards, so they see a pending command's
	// late outcome. HAControl makes a private one when it is nil.
	Confirmations *HAConfirmations
}

// available offers the tools while Home Assistant is set up: the instance
// the server started with, still named by the setting.
func (d HADeps) available(v settings.Values) bool { return d.Home != nil && v.HomeAssistant != "" }

func (d HADeps) view() (ha.View, *agent.ToolError) {
	if d.Home == nil {
		return ha.View{}, haUnavailable()
	}
	v, err := d.Home.View()
	if err != nil {
		return ha.View{}, haUnavailable()
	}
	return v, nil
}

// narrow settles several matches: to those can takes (all of them when none
// can; nil can keeps all), then to those in the device's area (all of them
// when none is there).
func (d HADeps) narrow(ctx context.Context, deviceID string, ms []ha.Entity, can func(ha.Entity) bool) ([]ha.Entity, *agent.ToolError) {
	if len(ms) > 1 && can != nil {
		ms = keep(ms, can)
	}
	if len(ms) > 1 {
		v, err := d.Settings(ctx, deviceID)
		if err != nil {
			return nil, internal()
		}
		if v.HAArea != "" {
			ms = keep(ms, func(e ha.Entity) bool { return e.Area == v.HAArea })
		}
	}
	return ms, nil
}

// matches are the entities target names, by id: the exact id, else those
// with the exact alias, else those with the exact name, else every one whose
// name, id or an alias contains it. Case and runs of whitespace do not count.
func matches(entities []ha.Entity, target string) []ha.Entity {
	want := normalize(target)
	if want == "" {
		return nil
	}
	if i := slices.IndexFunc(entities, func(e ha.Entity) bool { return e.ID == want }); i >= 0 {
		return entities[i : i+1]
	}
	stages := []func(ha.Entity) bool{
		func(e ha.Entity) bool {
			return slices.ContainsFunc(e.Aliases, func(a string) bool { return normalize(a) == want })
		},
		func(e ha.Entity) bool { return normalize(e.Name) == want },
		func(e ha.Entity) bool {
			return strings.Contains(normalize(e.Name), want) || strings.Contains(e.ID, want) ||
				slices.ContainsFunc(e.Aliases, func(a string) bool { return strings.Contains(normalize(a), want) })
		},
	}
	for _, match := range stages {
		if ms := filter(entities, match); len(ms) > 0 {
			return ms
		}
	}
	return nil
}

func normalize(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }

func filter(es []ha.Entity, ok func(ha.Entity) bool) []ha.Entity {
	var out []ha.Entity
	for _, e := range es {
		if ok(e) {
			out = append(out, e)
		}
	}
	return out
}

// keep is the entities ok keeps, or all of them when it keeps none.
func keep(es []ha.Entity, ok func(ha.Entity) bool) []ha.Entity {
	if kept := filter(es, ok); len(kept) > 0 {
		return kept
	}
	return es
}

// displayName is e's name as the model may see it.
func displayName(e ha.Entity) string {
	if n := ha.Sanitize(e.Name, haNameLen); n != "" {
		return n
	}
	return e.ID
}

// ambiguous names at most three of the matches, by id, for the model to ask
// the user which one: never a silent pick. A name two of them share carries
// the area, else (no area, or the same one) the id.
func ambiguous(ms []ha.Entity, areas []ha.Area) *agent.ToolError {
	listed := ms[:min(len(ms), 3)]
	plain := make([]string, len(listed))
	for i, e := range listed {
		plain[i] = displayName(e)
	}
	names := slices.Clone(plain)
	for i, e := range listed {
		shared, sameArea := false, false
		for j, o := range listed {
			if j != i && plain[j] == plain[i] {
				shared, sameArea = true, sameArea || o.Area == e.Area
			}
		}
		if !shared {
			continue
		}
		which := areaName(areas, e.Area)
		if which == "" || sameArea {
			which = e.ID
		}
		names[i] = plain[i] + " (" + which + ")"
	}
	return &agent.ToolError{
		Code: "ambiguous", Message: "Several devices match: " + strings.Join(names, ", ") + ".",
		Hint: "Ask the user which one they mean.", Retryable: true, Candidates: names,
	}
}

func haNotFound(msg, hint string) *agent.ToolError {
	return &agent.ToolError{Code: "not_found", Message: msg, Hint: hint, Retryable: true}
}

func haUnavailable() *agent.ToolError {
	return unavailable("Home Assistant is not reachable right now.")
}
