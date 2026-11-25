package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/integrations/ha"
)

const haSearchInput = `{
	"type": "object",
	"properties": {
		"query": {
			"type": "string", "minLength": 1, "maxLength": 100, "pattern": "\\S",
			"description": "Words to match against entity ids, names and aliases, such as \"bedroom\" or \"boiler\"."
		},
		"domain": {
			"type": "string", "maxLength": 32, "pattern": "^[a-z][a-z0-9_]*$",
			"description": "Optional Home Assistant domain filter such as light, switch, climate, sensor or water_heater."
		}
	},
	"required": ["query"],
	"additionalProperties": false
}`

const (
	maxSearchMatches = 5
	maxSearchChars   = 2000
)

type searchMatch struct {
	ID     string         `json:"id"`
	Name   string         `json:"name"`
	Domain string         `json:"domain"`
	Area   string         `json:"area,omitempty"`
	State  string         `json:"state"`
	Attrs  map[string]any `json:"attrs,omitempty"`
}

// HASearch answers state questions from the live view (M5c-D4): it fetches
// nothing, since state_changed events keep the view current.
func HASearch(d HADeps) agent.Tool {
	return agent.Tool{
		Name: "ha_search",
		Description: "Answer a question about the current state of home devices through Home Assistant: whether a light or switch " +
			"is on, a sensor reading, a climate or water heater temperature. Never for changing anything: use ha_control for that. " +
			"Never for what a device can do: use ha_inspect for that. States may be old while Home Assistant reconnects: " +
			"a result with \"stale\": true says so.",
		Input:   haSearchInput,
		Effects: agent.Read, Output: agent.Trusted,
		Timeout:   4 * time.Second,
		Available: d.available,
		Run: func(_ context.Context, c agent.Call) (json.RawMessage, *agent.ToolError) {
			var a struct {
				Query  string `json:"query"`
				Domain string `json:"domain"`
			}
			if err := json.Unmarshal(c.Args, &a); err != nil {
				return nil, badArgs("The arguments are not a JSON object.")
			}
			v, e := d.view()
			if e != nil {
				return nil, e
			}
			hits := search(v.Entities, normalize(a.Query), a.Domain)
			if len(hits) == 0 {
				msg := fmt.Sprintf("nothing matched %q", strings.TrimSpace(a.Query))
				if a.Domain != "" {
					msg += " in domain " + a.Domain
				}
				return nil, haNotFound(msg, "Try other words or no domain, or tell the user nothing matched.")
			}
			out := make([]searchMatch, len(hits))
			for i, x := range hits {
				out[i] = searchMatch{
					ID: x.ID, Name: displayName(x), Domain: x.Domain, Area: areaName(v.Areas, x.Area),
					State: ha.ModelState(x), Attrs: attrsOut(x.Attrs),
				}
			}
			return capped(out, maxSearchChars, func(ms []searchMatch) any {
				res := map[string]any{"matches": ms}
				if !v.Fresh {
					res["stale"] = true
				}
				return res
			})
		},
	}
}

// search is v1's match: every entity whose name, id or an alias contains
// the query, of the domain when given; an exact name first, then by id.
func search(entities []ha.Entity, query, domain string) []ha.Entity {
	if query == "" {
		return nil
	}
	hits := filter(entities, func(e ha.Entity) bool {
		return (domain == "" || e.Domain == domain) && (strings.Contains(normalize(e.Name), query) ||
			strings.Contains(e.ID, query) || slices.ContainsFunc(e.Aliases, func(a string) bool { return strings.Contains(normalize(a), query) }))
	})
	// entities come by id, so a stable sort keeps that order within each rank
	slices.SortStableFunc(hits, func(a, b ha.Entity) int {
		return rank(a, query) - rank(b, query)
	})
	return hits[:min(len(hits), maxSearchMatches)]
}

func rank(e ha.Entity, query string) int {
	if normalize(e.Name) == query {
		return 0
	}
	return 1
}

func areaName(areas []ha.Area, id string) string {
	if id == "" {
		return ""
	}
	i, ok := slices.BinarySearchFunc(areas, id, func(a ha.Area, id string) int { return strings.Compare(a.ID, id) })
	if !ok {
		return ""
	}
	return ha.Sanitize(areas[i].Name, haNameLen)
}

// attrsOut are the whitelisted attributes under Home Assistant's names;
// nil without any.
func attrsOut(at ha.Attrs) map[string]any {
	out := map[string]any{}
	for name, n := range map[string]*float64{
		"brightness": at.Brightness, "color_temp_kelvin": at.ColorTempKelvin, "temperature": at.Temperature,
		"current_temperature": at.CurrentTemperature, "percentage": at.Percentage,
	} {
		if n != nil {
			out[name] = *n
		}
	}
	for name, s := range map[string]*string{
		"hvac_action": at.HVACAction, "operation_mode": at.OperationMode,
		"unit_of_measurement": at.UnitOfMeasurement, "device_class": at.DeviceClass,
	} {
		if s != nil {
			out[name] = ha.Sanitize(*s, haAttrLen)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// capped encodes wrap(items), dropping trailing items (one stays) while the
// result is over limit code points.
func capped[T any](items []T, limit int, wrap func([]T) any) (json.RawMessage, *agent.ToolError) {
	for {
		out, e := encode(wrap(items))
		if e != nil || len(items) <= 1 || utf8.RuneCount(out) <= limit {
			return out, e
		}
		items = items[:len(items)-1]
	}
}
