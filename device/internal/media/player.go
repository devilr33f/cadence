package media

import (
	"context"
	"encoding/json"

	"git.shino.boo/devilreef/cadence/device/internal/capability"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
)

const (
	duckDefault = "duck"

	noArgsSchema       = `{"type":"object","additionalProperties":false}`
	playerConfigSchema = `{"type":"object","properties":{"duckMode":{"enum":["duck","muffle"]}},"additionalProperties":false}`
	playerStateSchema  = `{"type":"object","properties":{"duckMode":{"enum":["duck","muffle"]}},"required":["duckMode"],"additionalProperties":false}`
	seekSchema         = `{"type":"object","properties":{"positionMs":{"type":"integer","minimum":0}},"required":["positionMs"],"additionalProperties":false}`
)

var (
	duckModes = map[string]audiodv1.DuckMode{"duck": audiodv1.DuckMode_DUCK_MODE_DUCK, "muffle": audiodv1.DuckMode_DUCK_MODE_MUFFLE}

	playerActions = map[string]audiodv1.MediaAction{
		"pause": audiodv1.MediaAction_MEDIA_ACTION_PAUSE, "resume": audiodv1.MediaAction_MEDIA_ACTION_RESUME,
		"stop": audiodv1.MediaAction_MEDIA_ACTION_STOP, "seek": audiodv1.MediaAction_MEDIA_ACTION_SEEK,
		// the session refuses a skip its actions do not list (unsupported), audiod one without a session (no_session)
		"next": audiodv1.MediaAction_MEDIA_ACTION_NEXT, "previous": audiodv1.MediaAction_MEDIA_ACTION_PREVIOUS,
	}
)

// player is media.player: the media session's controls, and how the music
// gives way to a voice session. Its state never carries the session.
type player struct {
	m     *Media
	state chan json.RawMessage
}

func (*player) Descriptor() capability.Descriptor {
	return capability.Descriptor{
		ID: "media.player", Version: 1, ConfigSchema: playerConfigSchema, StateSchema: playerStateSchema,
		Commands: map[string]string{
			"pause": noArgsSchema, "resume": noArgsSchema, "stop": noArgsSchema, "seek": seekSchema,
			"next": noArgsSchema, "previous": noArgsSchema,
		},
	}
}

func (p *player) Apply(_ context.Context, _ int64, cfg json.RawMessage) error {
	var c struct {
		DuckMode *string `json:"duckMode"`
	}
	if decodeObject(cfg, &c) != nil {
		return &capability.ApplyError{Code: codeInvalid}
	}
	mode := duckDefault
	if c.DuckMode != nil {
		mode = *c.DuckMode
	}
	if _, ok := duckModes[mode]; !ok {
		return &capability.ApplyError{Code: codeInvalid}
	}
	p.m.update(func() {
		p.m.duckMode = mode
		p.publish()
	})
	return nil
}

func (p *player) Command(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, error) {
	action, ok := playerActions[name]
	if !ok {
		return nil, capability.ErrUnsupported
	}
	var a struct {
		PositionMs *uint64 `json:"positionMs"`
	}
	// a seek needs its position, the others take nothing
	seek := action == audiodv1.MediaAction_MEDIA_ACTION_SEEK
	if decodeObject(args, &a) != nil || seek != (a.PositionMs != nil) {
		return nil, errInvalid
	}
	var position uint64
	if seek {
		position = *a.PositionMs
	}
	// a session another plane renders is its player's to move: audiod only arbitrates it
	if id, ext := p.m.currentExternal(); ext != nil {
		return nil, ext.ExternalCommand(ctx, id, name, position, "")
	}
	return nil, p.m.request(ctx, func(id string) *audiodv1.Envelope {
		mc := &audiodv1.MediaCommand{RequestId: id, Action: action, PositionMs: position}
		return &audiodv1.Envelope{Body: &audiodv1.Envelope_MediaCommand{MediaCommand: mc}}
	})
}

func (p *player) State() <-chan json.RawMessage { return p.state }

// Action runs one of the player's no-argument actions (pause, resume, stop, next, previous) on the current session:
// the board's own pause for the offline stop words, which takes the player command's own path, so a session another
// plane renders is that plane's to move.
func (m *Media) Action(ctx context.Context, name string) error {
	action, ok := playerActions[name]
	if !ok || action == audiodv1.MediaAction_MEDIA_ACTION_SEEK {
		return capability.ErrUnsupported
	}
	if id, ext := m.currentExternal(); ext != nil {
		return ext.ExternalCommand(ctx, id, name, 0, "")
	}
	return m.request(ctx, func(id string) *audiodv1.Envelope {
		return &audiodv1.Envelope{Body: &audiodv1.Envelope_MediaCommand{MediaCommand: &audiodv1.MediaCommand{
			RequestId: id, Action: action,
		}}}
	})
}

// callers hold setMu, or own p before it is shared
func (p *player) publish() {
	publishLatest(p.state, struct {
		DuckMode string `json:"duckMode"`
	}{p.m.duckMode})
}
