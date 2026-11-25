package tools

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/commands"
	"git.shino.boo/devilreef/cadence/server/internal/devices"
	"git.shino.boo/devilreef/cadence/server/internal/ringsounds"
)

// the device's alarms capability (M6a-D2): it keeps the alarms and timers and rings them, the server only asks
const alarmsCapability = "alarms"

// alarmsTimeout is past the commands service's own 10 s, so its timeout is the one that answers
const alarmsTimeout = 12 * time.Second

// AlarmDeps reach the device's alarms capability: the state it last reported, and its commands.
type AlarmDeps struct {
	State   func(ctx context.Context, deviceID, capability string) (json.RawMessage, bool)
	Command func(ctx context.Context, deviceID, capability, name string, args []byte) (json.RawMessage, error)
	Now     func() time.Time
	// Log hears one line per call: its action and code, nothing the user said
	Log *slog.Logger
	// Target resolves the device argument; nil: this device alone
	Target TargetFunc
	// Sounds makes a reminder's speech and reads what a device's sounds are; nil: reminders chime and say the bundled
	// prompt, and alarms list no music
	Sounds RingSounds
	// Music makes an alarm's ring sound from a music_search result id or a query; nil: alarms ring their tone
	Music AlarmMusic
}

// AlarmMusic makes the ring sound of what sound names for the device: a music_search result id (r1) or a query whose
// first song it takes.
type AlarmMusic func(ctx context.Context, deviceID, sound string) (ringsounds.Sound, error)

func (d AlarmDeps) now() time.Time {
	if d.Now == nil {
		return time.Now()
	}
	return d.Now()
}

// logged runs a tool's call and logs its action and code (rule 8: no time, length or id)
func (d AlarmDeps) logged(event string, run func(ctx context.Context, c agent.Call) (string, json.RawMessage, *agent.ToolError)) func(ctx context.Context, c agent.Call) (json.RawMessage, *agent.ToolError) {
	return func(ctx context.Context, c agent.Call) (json.RawMessage, *agent.ToolError) {
		action, out, e := run(ctx, c)
		if d.Log != nil {
			code := "ok"
			if e != nil {
				code = e.Code
			}
			// action is one of the schema's, or none: the model's own value never reaches a log
			if action == "" {
				action = "none"
			}
			d.Log.Info(event, "device_id", c.DeviceID, "session_id", c.SessionID, "action", action, "code", code)
		}
		return out, e
	}
}

type alarmEntry struct {
	ID      string   `json:"id"`
	Time    string   `json:"time"`
	Days    []string `json:"days"`
	Date    string   `json:"date"`
	Level   int      `json:"level"`
	SoundID string   `json:"soundId"`
}

type timerEntry struct {
	ID      string `json:"id"`
	Seconds int    `json:"seconds"`
	EndsAt  string `json:"endsAt"`
}

type alarmsState struct {
	Alarms []alarmEntry `json:"alarms"`
	Timers []timerEntry `json:"timers"`
}

// target is the call as it acts on the device its device argument names, and the ctx its commands go under (vouched):
// a list reads it, anything else writes it
func (d AlarmDeps) target(ctx context.Context, c agent.Call) (context.Context, agent.Call, *agent.ToolError) {
	var a struct {
		Action string `json:"action"`
		Device string `json:"device"`
	}
	// the arguments' own faults are the parser's, which answers them later
	_ = json.Unmarshal(c.Args, &a)
	t, e := target(ctx, d.Target, c, a.Device, agent.Need{Capability: alarmsCapability, Write: a.Action != "list"})
	if e != nil {
		return ctx, c, e
	}
	return vouched(ctx, c, t), on(c, t), nil
}

// state is what the device last reported; none, or one this server cannot read, is a device without alarms (R5)
func (d AlarmDeps) state(ctx context.Context, deviceID string) (alarmsState, bool) {
	raw, ok := d.State(ctx, deviceID, alarmsCapability)
	var st alarmsState
	if !ok || json.Unmarshal(raw, &st) != nil || st.Alarms == nil || st.Timers == nil {
		return alarmsState{}, false
	}
	return st, true
}

func noAlarms() *agent.ToolError { return unavailable("This speaker cannot keep alarms or timers.") }

// command runs one of the capability's commands with args as its JSON
func (d AlarmDeps) command(ctx context.Context, c agent.Call, name string, args any) (json.RawMessage, error) {
	raw, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	return d.Command(ctx, c.DeviceID, alarmsCapability, name, raw)
}

// alarmsError is the model's view of a failed command, never device text. failed is the tool's own code for the
// rest; nothing is offered for a retry, since a set or cancel that timed out may have landed
func alarmsError(err error, failed, noun string) *agent.ToolError {
	var device *commands.DeviceError
	var invalid *commands.InvalidArgumentsError
	code := ""
	if errors.As(err, &device) {
		code = device.Code
	}
	switch {
	case code == "wake_required" || code == "follow_up":
		return agent.WakeRequired()
	case code == "full":
		return &agent.ToolError{Code: "full", Message: "The speaker holds as many " + noun + "s as it can; one must be cancelled first."}
	case code == "past":
		return &agent.ToolError{Code: "past", Message: "That time has passed, so the alarm would never ring."}
	case code == "store":
		return &agent.ToolError{Code: "store", Message: "The speaker could not save it."}
	case code == "not_found":
		return &agent.ToolError{Code: "not_found", Message: "The speaker has no " + noun + " like that."}
	case code == "unavailable":
		// the book holds alarms until the server set the zone (R2, R6)
		return unavailable("The speaker does not know its time zone yet, so it cannot set alarms; the owner sets timeZone in the settings.")
	case code == "invalid", errors.As(err, &invalid):
		return &agent.ToolError{Code: "invalid_arguments", Message: "The speaker refused these arguments."}
	case errors.Is(err, devices.ErrNotFound), errors.Is(err, devices.ErrUnknownCapability), errors.Is(err, devices.ErrUnknownCommand):
		return noAlarms()
	case errors.Is(err, commands.ErrOffline):
		return &agent.ToolError{Code: "device_offline", Message: "The speaker is offline."}
	case errors.Is(err, commands.ErrTimeout):
		return &agent.ToolError{Code: "timeout", Message: "The speaker did not answer."}
	}
	return failedAnswer(failed)
}

func failedAnswer(code string) *agent.ToolError {
	return &agent.ToolError{Code: code, Message: "The speaker did not answer as expected."}
}

// cancelled reads a cancel's answer: the ids the device removed
func cancelled(res json.RawMessage) ([]string, bool) {
	var out struct {
		Cancelled []string `json:"cancelled"`
	}
	if json.Unmarshal(res, &out) != nil || len(out.Cancelled) == 0 {
		return nil, false
	}
	return out.Cancelled, true
}

// remaining is a timer's seconds left as a voice says it: a timer about to ring still has one
func remaining(endsAt string, now time.Time) (int, bool) {
	end, err := time.Parse(time.RFC3339, endsAt)
	if err != nil {
		return 0, false
	}
	left := math.Ceil(end.Sub(now).Seconds())
	return int(max(1, min(left, math.MaxInt32))), true
}

const (
	idAlphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	idLength   = 8
	// the largest multiple of the alphabet's size a byte holds: higher bytes would favour the first letters
	idUnbiased = 252
)

// newID is a fresh id the device keeps an alarm or timer under, [a-z0-9]{8} (its own pattern is 1-16); never one it
// keeps already, which a set would replace
func newID(taken func(string) bool) string {
	for {
		id := make([]byte, 0, idLength)
		var b [idLength]byte
		for len(id) < idLength {
			_, _ = rand.Read(b[:])
			for _, c := range b {
				if c < idUnbiased && len(id) < idLength {
					id = append(id, idAlphabet[int(c)%len(idAlphabet)])
				}
			}
		}
		if !taken(string(id)) {
			return string(id)
		}
	}
}

// fields reads a call's arguments as an object; a field that is false or [] is absent, as models echo the empty
// values of fields they do not mean, and one that is null is refused, as the schema refuses it (R7)
func fields(args json.RawMessage) (map[string]json.RawMessage, bool) {
	var m map[string]json.RawMessage
	if json.Unmarshal(args, &m) != nil || m == nil {
		return nil, false
	}
	for k, raw := range m {
		t := bytes.TrimSpace(raw)
		var list []json.RawMessage
		switch {
		case string(t) == "null":
			return nil, false
		case string(t) == "false", len(t) > 0 && t[0] == '[' && json.Unmarshal(t, &list) == nil && len(list) == 0:
			delete(m, k)
		}
	}
	return m, true
}

// action takes the call's action and checks that every other field goes with it
func action(m map[string]json.RawMessage, allowed map[string][]string) (string, *agent.ToolError) {
	var name string
	if json.Unmarshal(m["action"], &name) != nil {
		return "", badArgs("Give action.")
	}
	ok, known := allowed[name]
	if !known {
		return "", badArgs("Unknown action.")
	}
	for k := range m {
		if k == "action" {
			continue
		}
		fits := false
		for _, a := range ok {
			fits = fits || a == k
		}
		if !fits {
			// a cancel that lost a field would remove what the user did not name
			return name, badArgs(k + " does not go with " + name + ".")
		}
	}
	return name, nil
}
