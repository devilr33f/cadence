package tools

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/presence"
)

// one of the two, which Run checks: minutes turns it on, off turns it off
const dndInput = `{
	"type": "object",
	"properties": {
		"minutes": {"type": "integer", "minimum": 1, "maximum": 1440},
		"off": {"type": "boolean"}
	},
	"additionalProperties": false
}`

// maxDNDMinutes is presence's MaxTTL in the tool's unit
const maxDNDMinutes = int(presence.MaxTTL / time.Minute)

// Busy sets and clears a busy signal; *presence.Service is one.
type Busy interface {
	Put(ctx context.Context, source, reason string, devices []string, ttl time.Duration) (time.Time, error)
	Delete(ctx context.Context, source string) error
}

type DNDDeps struct {
	Busy     Busy
	Settings SettingsFunc
}

// DoNotDisturb turns do-not-disturb on for every speaker for some minutes, or
// off (busy mode, source voice). It holds only updates nobody asked for:
// alarms, timers and what the user starts still go. A follow-up may ask for
// it (it never raises the volume or sets an alarm); event turns do not get it.
func DoNotDisturb(d DNDDeps) agent.Tool {
	return agent.Tool{
		Name: "do_not_disturb",
		Description: "Turn do-not-disturb on for every speaker for some minutes (1-1440, a day at most; an hour is 60), " +
			"or off (which also ends one set in the dashboard). While it is on, the speakers keep task updates " +
			"and other news nobody asked for until it ends; alarms, timers and anything the user asks still go. Give minutes to turn it on, off true to turn it off. " +
			"With no length given, ask how long.",
		Input:   dndInput,
		Effects: agent.Write, Output: agent.Trusted,
		Timeout: 5 * time.Second,
		Run: func(ctx context.Context, c agent.Call) (json.RawMessage, *agent.ToolError) {
			var a struct {
				Minutes *int  `json:"minutes"`
				Off     *bool `json:"off"`
			}
			if err := json.Unmarshal(c.Args, &a); err != nil {
				return nil, badArgs("The arguments are not a JSON object.")
			}
			off := a.Off != nil && *a.Off
			switch {
			case off && a.Minutes != nil:
				return nil, badArgs("Give minutes or off, not both.")
			case off:
				// the dashboard's toggle is the owner's do-not-disturb too
				was := false
				for _, src := range []string{presence.SourceVoice, presence.SourceDashboard} {
					err := d.Busy.Delete(ctx, src)
					if err != nil && !errors.Is(err, presence.ErrNotFound) {
						return nil, internal()
					}
					was = was || err == nil
				}
				return encode(map[string]bool{"on": false, "wasOn": was})
			case a.Minutes == nil:
				return nil, badArgs("Give minutes to turn it on, or off true.")
			case *a.Minutes < 1 || *a.Minutes > maxDNDMinutes:
				return nil, badArgs("minutes is 1-1440.")
			}
			at, err := d.Busy.Put(ctx, presence.SourceVoice, presence.ReasonDND, nil, time.Duration(*a.Minutes)*time.Minute)
			if err != nil {
				return nil, internal()
			}
			// the end on the household's clock, so the reply can name it
			loc := time.UTC
			if d.Settings != nil {
				if v, err := d.Settings(ctx, c.DeviceID); err == nil {
					if l, err := time.LoadLocation(v.TimeZone); err == nil {
						loc = l
					}
				}
			}
			return encode(map[string]any{"on": true, "minutes": *a.Minutes, "until": at.In(loc).Format("15:04")})
		},
	}
}
