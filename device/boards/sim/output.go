package sim

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"git.shino.boo/devilreef/cadence/device/internal/capability"
	"git.shino.boo/devilreef/cadence/device/internal/volume"
)

const (
	// the sim's level before it has one of its own
	defaultLevel = 50

	outputConfigSchema = `{"type":"object","properties":{},"additionalProperties":false}`
	outputStateSchema  = `{"type":"object","properties":{"level":{"type":"integer"},"channels":{"type":"integer"},"maxBitrate":{"type":"integer"}},"required":["level","channels","maxBitrate"]}`
)

// output is audio.output: the level is the device's own, set by command.
type output struct {
	vol      *volume.Setter
	sessions volume.Sessions
	state    chan json.RawMessage
}

func newOutput(vol *volume.Setter, sessions volume.Sessions) *output {
	o := &output{vol: vol, sessions: sessions, state: make(chan json.RawMessage, 1)}
	o.publish(vol.Level())
	vol.Listen(func(level int, _ volume.Origin) { o.publish(level) })
	return o
}

func (*output) Descriptor() capability.Descriptor {
	return capability.Descriptor{
		ID: "audio.output", Version: 1, ConfigSchema: outputConfigSchema, StateSchema: outputStateSchema,
		Commands: volume.Commands(),
	}
}

// Apply takes {}. A {"level": N} stored before the device owned its level only
// seeds a level the device does not have yet, so such configs keep restoring.
func (o *output) Apply(_ context.Context, _ int64, cfg json.RawMessage) error {
	var c struct {
		Level *int `json:"level"`
	}
	dec := json.NewDecoder(bytes.NewReader(cfg))
	dec.DisallowUnknownFields()
	if dec.Decode(&c) != nil || !atEnd(dec) || c.Level != nil && (*c.Level < 0 || *c.Level > 100) {
		return &capability.ApplyError{Code: "invalid"}
	}
	if c.Level != nil {
		o.vol.Seed(*c.Level)
	}
	return nil
}

func atEnd(dec *json.Decoder) bool {
	_, err := dec.Token()
	return errors.Is(err, io.EOF)
}

func (o *output) Command(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, error) {
	return volume.Command(ctx, o.vol, o.sessions, name, args)
}

func (o *output) State() <-chan json.RawMessage { return o.state }

// runs under the setter's lock, or before the output is shared; the channel keeps only the latest state
func (o *output) publish(level int) {
	s := json.RawMessage(fmt.Sprintf(`{"level":%d,"channels":1,"maxBitrate":64000}`, level))
	select {
	case <-o.state:
	default:
	}
	o.state <- s
}
