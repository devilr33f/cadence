package tools

import (
	"context"

	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/commands"
)

// TargetFunc resolves a tool's device argument (a device name, a room, or "here"; "" is here) to the device that
// has what need asks for, applying the cross-device policy, or answers the model's error with its candidates: the
// shared resolver of TV plan item 14 (agent.Resolver.ResolveTarget).
type TargetFunc func(ctx context.Context, c agent.Call, arg string, need agent.Need) (agent.Target, *agent.ToolError)

// deviceProp is the device argument every device tool takes
const deviceProp = `"device": {"type": "string", "minLength": 1, "maxLength": 64}`

// deviceHint is what a description says of it
const deviceHint = "device names another device (its name or its room, as the devices note lists them); leave it " +
	"out for this device. "

// target resolves arg through f; without f only this device resolves.
func target(ctx context.Context, f TargetFunc, c agent.Call, arg string, need agent.Need) (agent.Target, *agent.ToolError) {
	if f != nil {
		return f(ctx, c, arg, need)
	}
	if arg == "" || arg == "here" {
		return agent.Target{ID: c.DeviceID, Self: true}, nil
	}
	return agent.Target{}, &agent.ToolError{
		Code: "unknown_device", Message: "Acting on another device is not available.",
		Hint: "Leave device out to act on this device.",
	}
}

// on is the call as it acts on t: its device is the target's, its session still the turn's
func on(c agent.Call, t agent.Target) agent.Call {
	c.DeviceID = t.ID
	return c
}

// vouched is ctx as a command on t runs under it: on another device than the turn's, the server vouches for the
// turn's session with a grant (commands.WithVouch), as the target does not know it; the target's own rules still
// apply (rule 7 per target), and the grant goes only for a woken session. c is the turn's call, before on
func vouched(ctx context.Context, c agent.Call, t agent.Target) context.Context {
	if t.Self || t.ID == c.DeviceID || !agent.Woken(c.Trigger) {
		return ctx
	}
	return commands.WithVouch(ctx, commands.Vouch{DeviceID: c.DeviceID, SessionID: c.SessionID, Trigger: c.Trigger})
}
