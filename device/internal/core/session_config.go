package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"

	"git.shino.boo/devilreef/cadence/device/internal/capability"
	"git.shino.boo/devilreef/cadence/device/internal/session"
)

// sessionCapabilityID is the voice sessions' own settings, which the server keeps equal to the device's settings
const sessionCapabilityID = "session"

const (
	sessionConfigSchema = `{"type":"object","properties":{` +
		`"errorSound":{"enum":["prompt","beep","off"],"description":"what a failed request plays: a spoken prompt, the error beep, or nothing"},` +
		`"language":{"type":"string","pattern":"^[a-z]{2,3}$","description":"the language of the spoken prompts"}` +
		`},"additionalProperties":false}`
	sessionStateSchema = `{"type":"object","additionalProperties":false}`
)

// sessionCapability is the session capability: {errorSound, language}, applied to the device's error sounds. The
// sync store keeps it, so it holds from boot and without a link.
type sessionCapability struct {
	sounds *session.ErrorSounds
}

func (*sessionCapability) Descriptor() capability.Descriptor {
	return capability.Descriptor{ID: sessionCapabilityID, Version: 1, ConfigSchema: sessionConfigSchema, StateSchema: sessionStateSchema}
}

func (c *sessionCapability) Apply(_ context.Context, _ int64, cfg json.RawMessage) error {
	var f struct {
		ErrorSound string `json:"errorSound"`
		Language   string `json:"language"`
	}
	dec := json.NewDecoder(bytes.NewReader(cfg))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return &capability.ApplyError{Code: "invalid"}
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return &capability.ApplyError{Code: "invalid"}
	}
	switch f.ErrorSound {
	case "", session.ErrorSoundPrompt, session.ErrorSoundBeep, session.ErrorSoundOff:
	default:
		return &capability.ApplyError{Code: "invalid"}
	}
	if f.Language != "" && !promptLanguage(f.Language) {
		return &capability.ApplyError{Code: "invalid"}
	}
	c.sounds.Set(f.ErrorSound, f.Language)
	return nil
}

// promptLanguage is [a-z]{2,3}, as audiod takes it
func promptLanguage(s string) bool {
	if len(s) < 2 || len(s) > 3 {
		return false
	}
	for _, r := range s {
		if r < 'a' || r > 'z' {
			return false
		}
	}
	return true
}

func (*sessionCapability) Command(context.Context, string, json.RawMessage) (json.RawMessage, error) {
	return nil, capability.ErrUnsupported
}

func (*sessionCapability) State() <-chan json.RawMessage { return nil }

// withSessionSettings reports the session capability, which sets how the device tells a failed request
func withSessionSettings() SyncOption {
	return func(s *Sync) { s.extra = append(s.extra, &sessionCapability{sounds: s.errors}) }
}
