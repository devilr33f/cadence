package input

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"

	"git.shino.boo/devilreef/cadence/device/internal/capability"
)

const (
	// CapabilityID reports the microphone's switch and whether the wake word works; nothing in it is configurable yet
	CapabilityID = "audio.input"

	configSchema = `{"type":"object","properties":{},"additionalProperties":false}`
	stateSchema  = `{"type":"object","properties":{"muted":{"type":"boolean"},"wake":{"type":"boolean"}},` +
		`"required":["muted","wake"],"additionalProperties":false}`
)

type provider struct{ in *Input }

// Capability is audio.input: {muted, wake}.
func (in *Input) Capability() capability.Provider { return in.provider }

func (*provider) Descriptor() capability.Descriptor {
	return capability.Descriptor{ID: CapabilityID, Version: 1, ConfigSchema: configSchema, StateSchema: stateSchema}
}

// Apply takes {} only.
func (*provider) Apply(_ context.Context, _ int64, cfg json.RawMessage) error {
	raw := bytes.TrimSpace(cfg)
	var c struct{}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if len(raw) == 0 || raw[0] != '{' || dec.Decode(&c) != nil {
		return &capability.ApplyError{Code: "invalid"}
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return &capability.ApplyError{Code: "invalid"}
	}
	return nil
}

func (*provider) Command(context.Context, string, json.RawMessage) (json.RawMessage, error) {
	return nil, capability.ErrUnsupported
}

func (p *provider) State() <-chan json.RawMessage { return p.in.state }

// publish offers the state, keeping only the latest; callers hold mu, so states go out in the order they were made
func (in *Input) publish() {
	b, err := json.Marshal(struct {
		Muted bool `json:"muted"`
		Wake  bool `json:"wake"`
	}{in.muted, in.wake})
	if err != nil {
		return
	}
	for {
		select {
		case in.state <- b:
			return
		default:
		}
		select {
		case <-in.state:
		default:
		}
	}
}
