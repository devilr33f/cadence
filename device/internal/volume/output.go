package volume

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync/atomic"

	"git.shino.boo/devilreef/cadence/device/internal/capability"
	"git.shino.boo/devilreef/cadence/pkg/capschema"
)

const outputStateSchema = `{"type":"object","properties":{"level":{"type":"integer"},"channels":{"type":"integer"},"maxBitrate":{"type":"integer"}},"required":["level","channels","maxBitrate"]}`

// output is audio.output on a board whose level is its hardware volume, which audiod applies to the mixer.
type output struct {
	vol      *Setter
	sessions Sessions
	// what the server may send: channels, and the most it encodes at
	channels, maxBitrate int
	state                chan json.RawMessage
	opts                 outputOptions
	// volume.mode as the config set it, "" when unset
	mode atomic.Pointer[string]
}

type outputOptions struct {
	screen       bool
	defaultDelay int
	delay        func(ms int)
}

// OutputOption shapes audio.output's config.
type OutputOption func(*outputOptions)

// WithDelay has audio.output's delayMs set the board's audio delay trim through apply (media.Media's
// SetOutputDelay): the config's value, or def, the board's own (its Configure's), when the config sets none.
func WithDelay(def int, apply func(ms int)) OutputOption {
	return func(o *outputOptions) { o.defaultDelay, o.delay = def, apply }
}

// ScreenOutput is a screen board's audio.output: its config takes volume.mode as well
// (capschema.AudioOutputScreenConfig).
func ScreenOutput() OutputOption { return func(o *outputOptions) { o.screen = true } }

// NewOutput is audio.output for a speaker of channels channels that takes at most maxBitrate, its level set through
// vol.
func NewOutput(vol *Setter, sessions Sessions, channels, maxBitrate int, opts ...OutputOption) capability.Provider {
	o := &output{vol: vol, sessions: sessions, channels: channels, maxBitrate: maxBitrate, state: make(chan json.RawMessage, 1)}
	for _, f := range opts {
		f(&o.opts)
	}
	o.publish(vol.Level())
	vol.Listen(func(level int, _ Origin) { o.publish(level) })
	return o
}

func (o *output) Descriptor() capability.Descriptor {
	cfg := capschema.AudioOutputConfig
	if o.opts.screen {
		cfg = capschema.AudioOutputScreenConfig
	}
	return capability.Descriptor{
		ID: capschema.AudioOutputID, Version: 1, ConfigSchema: cfg, StateSchema: outputStateSchema,
		Commands: Commands(),
	}
}

type outputConfig struct {
	DelayMs *int `json:"delayMs"`
	Volume  *struct {
		Mode string `json:"mode"`
	} `json:"volume"`
}

// Apply takes the delay trim (and on a screen board volume.mode); the level is the device's own, set by command.
func (o *output) Apply(_ context.Context, _ int64, cfg json.RawMessage) error {
	raw := bytes.TrimSpace(cfg)
	var c outputConfig
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if len(raw) == 0 || raw[0] != '{' || dec.Decode(&c) != nil {
		return &capability.ApplyError{Code: "invalid"}
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return &capability.ApplyError{Code: "invalid"}
	}
	if c.DelayMs != nil && (*c.DelayMs < capschema.AudioDelayMinMs || *c.DelayMs > capschema.AudioDelayMaxMs) {
		return &capability.ApplyError{Code: "invalid"}
	}
	mode := ""
	if c.Volume != nil {
		if !o.opts.screen || (c.Volume.Mode != "" && c.Volume.Mode != "software" && c.Volume.Mode != "system") {
			return &capability.ApplyError{Code: "invalid"}
		}
		mode = c.Volume.Mode
	}
	o.mode.Store(&mode)
	if o.opts.delay != nil {
		ms := o.opts.defaultDelay
		if c.DelayMs != nil {
			ms = *c.DelayMs
		}
		o.opts.delay(ms)
	}
	return nil
}

// VolumeMode is a screen board's volume.mode as audio.output's config set it: software (also when unset) or system.
func VolumeMode(p capability.Provider) string {
	if o, ok := p.(*output); ok {
		if m := o.mode.Load(); m != nil && *m != "" {
			return *m
		}
	}
	return "software"
}

func (o *output) Command(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, error) {
	return Command(ctx, o.vol, o.sessions, name, args)
}

func (o *output) State() <-chan json.RawMessage { return o.state }

// runs under the setter's lock, or before the output is shared; the channel keeps only the latest state
func (o *output) publish(level int) {
	s := json.RawMessage(fmt.Sprintf(`{"level":%d,"channels":%d,"maxBitrate":%d}`, level, o.channels, o.maxBitrate))
	select {
	case <-o.state:
	default:
	}
	o.state <- s
}
