package tools

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/commands"
	"git.shino.boo/devilreef/cadence/server/internal/music"
)

const mediaControlInput = `{
	"type": "object",
	"properties": {
		"action": {"enum": ["pause", "resume", "stop", "next", "previous", "seek",
			"shuffle_on", "shuffle_off", "repeat_off", "repeat_all", "repeat_one"]},
		"positionS": {"type": "integer", "minimum": 0},
		` + deviceProp + `
	},
	"required": ["action"],
	"additionalProperties": false
}`

// Queue is the music service's queue controls (music.Service).
type Queue interface {
	SetShuffle(deviceID string, on bool)
	SetRepeat(deviceID string, r music.Repeat)
	Restart(ctx context.Context, deviceID string) error
}

type MediaDeps struct {
	// Command sends a capability command; the music queue answers the skips
	// of its own songs
	Command func(ctx context.Context, deviceID, capability, name string, args []byte) (json.RawMessage, error)
	Queue   Queue
	// Target resolves the device argument; nil: this device alone
	Target TargetFunc
}

// MediaControl drives what a device plays (music, Spotify through Soloist,
// AirPlay, a screen's video): it never moves the volume (device_volume does),
// and its own bound, the household's playback, keeps it offered after
// untrusted output; another device needs a woken session.
func MediaControl(d MediaDeps) agent.Tool {
	return agent.Tool{
		Name: "media_control",
		Description: "Control what the speaker is playing: pause, resume, stop, next, previous, seek (positionS), " +
			"shuffle_on/off, repeat_off/all/one (repeat_one loops the song, repeat_all the queue). " + deviceHint +
			"Say nothing after it succeeds. Use it only for playback, never for the volume.",
		Input:   mediaControlInput,
		Effects: agent.Write, Output: agent.Trusted,
		TaintExempt: true, SilentOnSuccess: true,
		Timeout: 60 * time.Second,
		Run: func(ctx context.Context, c agent.Call) (json.RawMessage, *agent.ToolError) {
			var a struct {
				Action    string `json:"action"`
				PositionS *int   `json:"positionS"`
				Device    string `json:"device"`
			}
			if err := json.Unmarshal(c.Args, &a); err != nil {
				return nil, badArgs("The arguments are not a JSON object.")
			}
			t, terr := target(ctx, d.Target, c, a.Device, agent.Need{Capability: agent.CapMediaPlayer, Write: true})
			if terr != nil {
				return nil, terr
			}
			c = on(c, t)
			switch a.Action {
			case "shuffle_on", "shuffle_off":
				d.Queue.SetShuffle(c.DeviceID, a.Action == "shuffle_on")
				return encode(map[string]bool{"ok": true})
			case "repeat_off", "repeat_all", "repeat_one":
				d.Queue.SetRepeat(c.DeviceID, music.Repeat(a.Action[len("repeat_"):]))
				return encode(map[string]bool{"ok": true})
			}
			args := []byte("{}")
			if a.Action == "seek" {
				if a.PositionS == nil {
					return nil, badArgs("seek needs positionS.")
				}
				args, _ = json.Marshal(map[string]int{"positionMs": *a.PositionS * 1000})
			}
			_, err := d.Command(ctx, c.DeviceID, "media.player", a.Action, args)
			var device *commands.DeviceError
			// resume once a queued song ended or stopped: play it again
			if a.Action == "resume" && errors.As(err, &device) && device.Code == "no_session" {
				err = d.Queue.Restart(ctx, c.DeviceID)
			}
			if err != nil {
				return nil, musicError(err)
			}
			return encode(map[string]bool{"ok": true})
		},
	}
}
