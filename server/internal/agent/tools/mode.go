package tools

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"strings"
	"time"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/server/internal/agent"
)

const modeInput = `{
	"type": "object",
	"properties": {"mode": {"type": "string", "minLength": 1, "maxLength": 40}},
	"required": ["mode"],
	"additionalProperties": false
}`

type ModeDeps struct {
	Settings SettingsFunc
	State    *agent.ModeState
	// Modes resolves a device's allowed modes at call time.
	Modes interface {
		Get(ctx context.Context, name string) (agent.Mode, error)
	}
	// Described are the modes the description lists, read at each turn's
	// start without a query; nil lists none.
	Described func() []agent.Mode
	Now       func() time.Time
}

// AgentMode switches the device's mode by voice, from the next step on.
func AgentMode(d ModeDeps) agent.Tool {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Described == nil {
		d.Described = func() []agent.Mode { return nil }
	}
	return agent.Tool{
		Name:        "agent_mode",
		Description: modeDescription(d.Described()),
		Describe:    func() string { return modeDescription(d.Described()) },
		Input:       modeInput,
		Effects:     agent.Write, Output: agent.Trusted,
		Timeout: 2 * time.Second,
		Run: func(ctx context.Context, c agent.Call) (json.RawMessage, *agent.ToolError) {
			// a server-started turn has no user to ask for a mode
			if c.Trigger == cadencev2.Trigger_TRIGGER_EVENT {
				return nil, unavailable("A mode cannot be switched in this turn.")
			}
			var a struct {
				Mode string `json:"mode"`
			}
			if err := json.Unmarshal(c.Args, &a); err != nil {
				return nil, badArgs("The arguments are not a JSON object.")
			}
			v, err := d.Settings(ctx, c.DeviceID)
			if err != nil {
				return nil, internal()
			}
			name, ok := resolveMode(ctx, d.Modes, v.AllowedModes, a.Mode)
			if !ok {
				return nil, &agent.ToolError{
					Code: "mode_not_allowed", Message: "That mode is not allowed on this device.",
					Hint: "Pick one of the candidates.", Candidates: slices.Clone(v.AllowedModes),
				}
			}
			// an allowed name may have no row behind it; set, it would fail every later turn
			if _, err := d.Modes.Get(ctx, name); errors.Is(err, agent.ErrModeNotFound) {
				return nil, unavailable("That mode is not set up on the server.")
			} else if err != nil {
				return nil, internal()
			}
			d.State.Set(c.DeviceID, c.SessionID, name, d.Now())
			return encode(map[string]string{"mode": name})
		},
	}
}

// resolveMode finds the allowed mode the user's word names, by name or alias.
func resolveMode(ctx context.Context, modes interface {
	Get(ctx context.Context, name string) (agent.Mode, error)
}, allowed []string, in string) (string, bool) {
	in = agent.AliasKey(in)
	for _, n := range allowed {
		if strings.ToLower(n) == in {
			return n, true
		}
	}
	for _, n := range allowed {
		m, err := modes.Get(ctx, n)
		if err != nil {
			continue
		}
		for _, list := range m.Aliases {
			for _, al := range list {
				if agent.AliasKey(al) == in {
					return n, true
				}
			}
		}
	}
	return "", false
}

func modeDescription(modes []agent.Mode) string {
	var b strings.Builder
	b.WriteString("Switch how you think and answer, when the user asks for it (for example to think harder or to be quick). Applies from the next step. Modes:")
	for _, m := range modes {
		b.WriteString(" " + m.Name)
		var al []string
		for _, list := range m.Aliases {
			al = append(al, list...)
		}
		sort.Strings(al)
		if len(al) > 0 {
			b.WriteString(" (also: " + strings.Join(al, ", ") + ")")
		}
		b.WriteString(";")
	}
	b.WriteString(" Other modes may be allowed on this device: pass the user's word for them.")
	return b.String()
}
