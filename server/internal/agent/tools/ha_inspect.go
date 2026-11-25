package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/integrations/ha"
)

const haInspectInput = `{
	"type": "object",
	"properties": {
		"target": {
			"type": "string", "minLength": 1, "maxLength": 128, "pattern": "\\S",
			"description": "Entity id such as \"vacuum.cleaner\", the device name as the user said it, or a domain such as \"light\"."
		}
	},
	"required": ["target"],
	"additionalProperties": false
}`

const (
	maxInspectServices = 15
	maxInspectFields   = 12
	maxInspectChars    = 4000
)

var haDomainRe = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

type inspectEntity struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Domain string `json:"domain"`
	State  string `json:"state"`
}

type inspectField struct {
	Name     string `json:"name"`
	Required bool   `json:"required,omitempty"`
	Kind     string `json:"kind,omitempty"`
}

type inspectService struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Fields      []inspectField `json:"fields"`
}

type inspectOut struct {
	Entity    *inspectEntity   `json:"entity,omitempty"`
	Domain    string           `json:"domain"`
	Services  []inspectService `json:"services"`
	Truncated bool             `json:"truncated,omitempty"`
	// Stale: the view is the one kept while Home Assistant reconnects
	Stale bool `json:"stale,omitempty"`
}

// HAInspect lists what a device's domain can do (M5c-D4), from the services
// the view keeps.
func HAInspect(d HADeps) agent.Tool {
	return agent.Tool{
		Name: "ha_inspect",
		Description: "List what a home device can do through Home Assistant: every service its kind supports, with each field of " +
			"each service. Use before call_service, and to answer what a device can do. Never for changing anything: use " +
			"ha_control for that. Never for current state: use ha_search for that. What it lists may be old while Home " +
			"Assistant reconnects: a result with \"stale\": true says so.",
		Input:   haInspectInput,
		Effects: agent.Read, Output: agent.Trusted,
		Timeout:   5 * time.Second,
		Available: d.available,
		Run: func(ctx context.Context, c agent.Call) (json.RawMessage, *agent.ToolError) {
			var a struct {
				Target string `json:"target"`
			}
			if err := json.Unmarshal(c.Args, &a); err != nil {
				return nil, badArgs("The arguments are not a JSON object.")
			}
			target := strings.TrimSpace(a.Target)
			v, e := d.view()
			if e != nil {
				return nil, e
			}
			ms := matches(v.Entities, target)
			switch {
			case len(ms) == 1:
				return listing(v, ms[0].Domain, &ms[0])
			case haDomainRe.MatchString(target) && hasDomain(v, target):
				// a target that is no one entity may be a bare domain, such as
				// "light": that lists the domain, never one entity the area leaves
				return listing(v, target, nil)
			case len(ms) == 0:
				return nil, haNotFound(fmt.Sprintf("no device or domain matched %q", target),
					"Search with ha_search, or tell the user no such device was found.")
			}
			ms, e = d.narrow(ctx, c.DeviceID, ms, nil)
			switch {
			case e != nil:
				return nil, e
			case len(ms) == 1:
				return listing(v, ms[0].Domain, &ms[0])
			}
			return nil, ambiguous(ms, v.Areas)
		},
	}
}

func hasDomain(v ha.View, domain string) bool {
	if _, ok := v.Services[domain]; ok {
		return true
	}
	return slices.ContainsFunc(v.Entities, func(e ha.Entity) bool { return e.Domain == domain })
}

// listing is the domain's services by name, at most 15 of 12 fields each,
// cut to 4000 code points; truncated says some were left out.
func listing(v ha.View, domain string, e *ha.Entity) (json.RawMessage, *agent.ToolError) {
	base := inspectOut{Domain: domain, Stale: !v.Fresh}
	if e != nil {
		base.Entity = &inspectEntity{ID: e.ID, Name: displayName(*e), Domain: e.Domain, State: ha.ModelState(*e)}
	}
	names := slices.Sorted(maps.Keys(v.Services[domain]))
	services := make([]inspectService, 0, min(len(names), maxInspectServices))
	for _, name := range names[:min(len(names), maxInspectServices)] {
		svc := v.Services[domain][name]
		out := inspectService{Name: name, Description: ha.Sanitize(svc.Description, haDescLen), Fields: []inspectField{}}
		for _, f := range svc.Fields[:min(len(svc.Fields), maxInspectFields)] {
			out.Fields = append(out.Fields, inspectField{Name: f.Name, Required: f.Required, Kind: f.Kind})
		}
		services = append(services, out)
	}
	return capped(services, maxInspectChars, func(kept []inspectService) any {
		o := base
		o.Services = kept
		o.Truncated = len(kept) < len(names)
		return o
	})
}
