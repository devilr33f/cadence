package leds

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"git.shino.boo/devilreef/cadence/device/internal/atomicfile"
	"git.shino.boo/devilreef/cadence/device/internal/capability"
)

const (
	// CapabilityID is the one capability of the lights: the owner's two switches. the server's voice control of them
	// (M6) sets this config; nothing else reaches the renderer from the server.
	CapabilityID = "display.lights"

	configSchema = `{"type":"object","properties":{"lights":{"type":"boolean"},"clock":{"type":"boolean"}},` +
		`"additionalProperties":false}`
	stateSchema = `{"type":"object","properties":{"lights":{"type":"boolean"},"clock":{"type":"boolean"}},` +
		`"required":["lights","clock"],"additionalProperties":false}`
)

// switches are the owner's: on by default, and kept on the device so they hold before the server's config arrives
type switches struct {
	Lights bool `json:"lights"`
	Clock  bool `json:"clock"`
}

var on = switches{Lights: true, Clock: true}

var errInvalid = errors.New("leds: invalid switches")

// decode takes one JSON object of optional booleans over base; an omitted key keeps base. unknown fields, null and trailing data
// are refused.
func decode(raw []byte, base switches) (switches, error) {
	var f struct {
		Lights json.RawMessage `json:"lights"`
		Clock  json.RawMessage `json:"clock"`
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		return switches{}, errInvalid
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return switches{}, errInvalid
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return switches{}, errInvalid
	}
	s := base
	for _, p := range []struct {
		raw json.RawMessage
		to  *bool
	}{{f.Lights, &s.Lights}, {f.Clock, &s.Clock}} {
		if p.raw != nil && (bytes.Equal(p.raw, []byte("null")) || json.Unmarshal(p.raw, p.to) != nil) {
			return switches{}, errInvalid
		}
	}
	return s, nil
}

type provider struct {
	r    *Renderer
	path string
	log  *slog.Logger

	mu    sync.Mutex
	cur   switches
	state chan json.RawMessage
}

// NewCapability is display.lights: {lights, clock}, both optional; an omitted key keeps the persisted value (on at first boot). it reads the persisted
// switches at once (a missing file is the defaults; an invalid one is the defaults and one settings_invalid), applies
// them to the renderer, and keeps every applied config at path.
func NewCapability(r *Renderer, path string, log *slog.Logger) capability.Provider {
	p := &provider{r: r, path: path, log: log, state: make(chan json.RawMessage, 1)}
	s := on
	raw, err := os.ReadFile(path) //nolint:gosec // the board's own settings path
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		log.Warn("settings_invalid", "reason", "read")
	default:
		if s, err = decode(raw, on); err != nil {
			s = on
			log.Warn("settings_invalid", "reason", "format")
		}
	}
	p.mu.Lock()
	p.set(s)
	p.mu.Unlock()
	return p
}

func (*provider) Descriptor() capability.Descriptor {
	return capability.Descriptor{ID: CapabilityID, Version: 1, ConfigSchema: configSchema, StateSchema: stateSchema}
}

func (p *provider) Apply(_ context.Context, _ int64, cfg json.RawMessage) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	s, err := decode(cfg, p.cur)
	if err != nil {
		return &capability.ApplyError{Code: "invalid"}
	}
	p.set(s)
	if err := p.save(s); err != nil {
		// the switch is live; only its keeping failed
		p.log.Warn("settings_failed", "reason", "write")
	}
	return nil
}

func (*provider) Command(context.Context, string, json.RawMessage) (json.RawMessage, error) {
	return nil, capability.ErrUnsupported
}

func (p *provider) State() <-chan json.RawMessage { return p.state }

// set applies s and offers it as the state; callers hold mu so states go out in the order they were made
func (p *provider) set(s switches) {
	p.cur = s
	p.r.SetSwitches(s.Lights, s.Clock)
	b, err := json.Marshal(s)
	if err != nil {
		return
	}
	for {
		select {
		case p.state <- b:
			return
		default:
		}
		select {
		case <-p.state:
		default:
		}
	}
}

func (p *provider) save(s switches) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p.path), 0o700); err != nil {
		return err
	}
	return atomicfile.Write(p.path, b)
}
