// Package airplay is cadenced's side of the AirPlay 2 receiver under audiod
// (M4d-D7): the media.airplay capability. Sender volume goes through the
// soloist package's policy, which is sender-generic.
package airplay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"regexp"
	"sync"

	"git.shino.boo/devilreef/cadence/device/internal/capability"
	"git.shino.boo/devilreef/cadence/device/internal/receivername"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
)

const (
	maxName = 32

	configSchema = `{"type":"object","properties":{"enabled":{"type":"boolean"},` +
		`"name":{"type":"string","minLength":1,"maxLength":32,"pattern":"^[A-Za-z0-9._-]([A-Za-z0-9._ -]*[A-Za-z0-9._-])?$"}},` +
		`"additionalProperties":false}`
	stateSchema = `{"type":"object","properties":{` +
		`"state":{"enum":["off","starting","ready","failed"]},` +
		`"code":{"type":"string","pattern":"^[a-z0-9_]{1,64}$"}},"required":["state"],"additionalProperties":false}`
)

var name = regexp.MustCompile(`^[A-Za-z0-9._-]([A-Za-z0-9._ -]*[A-Za-z0-9._-])?$`)

// codes audiod may report; anything else is dropped, so nothing from a phone
// or the receiver's own text can reach the state
var codes = map[string]bool{
	"runtime_missing": true, "foreign_service": true, "ports_busy": true, "spawn_failed": true, "timeout": true,
	"no_interface": true, "scratch_failed": true, "invalid_name": true, "bus_failed": true, "exited": true,
	"session_lost": true, "wrong_protocol": true, "invalid_state": true, "backend_unavailable": true,
	"invalid_config": true,
}

// Options are the board's.
type Options struct {
	// Dir is the board's airplay runtime: <data dir>/airplay.
	Dir string
	// ScratchDir is audiod's scratch for it: the board's runtime dir + /airplay.
	ScratchDir string
	// Backend is the receiver audiod runs: unspecified (shairport, the speakers) or libairplay (the screens, whose
	// config comes from media.screencast through Configure)
	Backend audiodv1.AirplayBackend
	// OnState hears each change of the receiver's state (off, starting, ready, failed) and its code; it must never
	// block. The screens show it in media.screencast.
	OnState func(state, code string)
}

// Config is the receiver's: on or off, the name phones see ("" for the device's own, see SetDeviceName) and, with
// libairplay, what it offers.
type Config struct {
	Enabled bool
	Name    string
	// libairplay only: screen mirroring, the largest size offered (0 = the screen's), h265 and a pin
	Mirroring  bool
	MaxWidth   uint32
	MaxHeight  uint32
	H265       bool
	RequirePin bool
}

type config = Config

type state struct {
	State string `json:"state"`
	Code  string `json:"code,omitempty"`
}

// Airplay keeps the receiver's config for audiod, which forgets it with its
// client, and the state audiod reports.
type Airplay struct {
	opts  Options
	log   *slog.Logger
	cap   *provider
	state chan json.RawMessage

	// held while a config goes out, so audiod gets them in order
	sendMu sync.Mutex

	mu     sync.Mutex
	cfg    config
	device string                         // the device's name, "" while it has none
	send   func(*audiodv1.Envelope) error // the live connection's, nil while audiod is down
	status *audiodv1.AirplayStatus
	shown  state
}

// New starts with the receiver off. A nil log discards.
func New(opts Options, log *slog.Logger) *Airplay {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	a := &Airplay{opts: opts, log: log, state: make(chan json.RawMessage, 1)}
	a.cap = &provider{a: a}
	a.mu.Lock()
	a.publishLocked()
	a.mu.Unlock()
	return a
}

// Capability is media.airplay.
func (a *Airplay) Capability() capability.Provider { return a.cap }

// AirplayReady sends a new audiod connection the config and uses the connection.
func (a *Airplay) AirplayReady(send func(*audiodv1.Envelope) error) {
	a.sendMu.Lock()
	defer a.sendMu.Unlock()
	_ = send(a.configEnvelope())
	a.mu.Lock()
	a.send, a.status = send, nil
	a.publishLocked()
	a.mu.Unlock()
}

// AirplayEnvelope takes an AirplayStatus from audiod; it never blocks.
func (a *Airplay) AirplayEnvelope(env *audiodv1.Envelope) {
	st, ok := env.GetBody().(*audiodv1.Envelope_AirplayStatus)
	if !ok {
		return
	}
	a.mu.Lock()
	a.status = st.AirplayStatus
	a.publishLocked()
	a.mu.Unlock()
}

// AirplayGone follows the end of the connection AirplayReady took.
func (a *Airplay) AirplayGone() {
	a.mu.Lock()
	a.send, a.status = nil, nil
	a.publishLocked()
	a.mu.Unlock()
}

// Configure sets the receiver's config, as media.airplay's Apply does; the screens' media.screencast calls it.
func (a *Airplay) Configure(cfg Config) { a.configure(cfg) }

// SetDeviceName is the device's name, as the server last named it ("" for none yet): the receiver's name while its
// config sets none. A change of the name it advertises goes to audiod at once; it never blocks on audiod's answer.
func (a *Airplay) SetDeviceName(device string) {
	a.sendMu.Lock()
	defer a.sendMu.Unlock()
	a.mu.Lock()
	was := a.nameLocked()
	a.device = device
	send := a.send
	changed := a.nameLocked() != was
	a.mu.Unlock()
	if changed && send != nil {
		_ = send(a.configEnvelope())
	}
}

// the name phones see; callers hold mu
func (a *Airplay) nameLocked() string { return receivername.Pick(a.cfg.Name, a.device, maxName) }

func (a *Airplay) configure(cfg config) {
	a.sendMu.Lock()
	defer a.sendMu.Unlock()
	a.mu.Lock()
	a.cfg = cfg
	send := a.send
	a.publishLocked()
	a.mu.Unlock()
	if send != nil {
		_ = send(a.configEnvelope())
	}
}

func (a *Airplay) configEnvelope() *audiodv1.Envelope {
	a.mu.Lock()
	defer a.mu.Unlock()
	c := &audiodv1.AirplayConfig{
		Enabled: a.cfg.Enabled, Name: a.nameLocked(), Dir: a.opts.Dir, ScratchDir: a.opts.ScratchDir, Backend: a.opts.Backend,
	}
	if a.opts.Backend == audiodv1.AirplayBackend_AIRPLAY_BACKEND_LIBAIRPLAY {
		c.Mirroring, c.MaxWidth, c.MaxHeight, c.H265, c.RequirePin = a.cfg.Mirroring, a.cfg.MaxWidth, a.cfg.MaxHeight,
			a.cfg.H265, a.cfg.RequirePin
	}
	return &audiodv1.Envelope{Body: &audiodv1.Envelope_AirplayConfig{AirplayConfig: c}}
}

// callers hold mu
func (a *Airplay) publishLocked() {
	st := a.stateLocked()
	// only a change goes out, but for the first publish (New's, nothing shown
	// yet), so the capability has a state from the start
	if st == a.shown && a.shown.State != "" {
		return
	}
	if st != a.shown {
		a.log.Info("airplay_state", "state", st.State, "code", st.Code)
	}
	a.shown = st
	if a.opts.OnState != nil {
		a.opts.OnState(st.State, st.Code)
	}
	b, err := json.Marshal(st)
	if err != nil {
		return
	}
	offer(a.state, json.RawMessage(b))
}

func (a *Airplay) stateLocked() state {
	if !a.cfg.Enabled {
		return state{State: "off"}
	}
	if a.status == nil {
		return state{State: "starting"}
	}
	switch a.status.GetState() {
	case audiodv1.AirplayState_AIRPLAY_STATE_READY:
		return state{State: "ready"}
	case audiodv1.AirplayState_AIRPLAY_STATE_FAILED:
		out := state{State: "failed"}
		if code := a.status.GetCode(); codes[code] {
			out.Code = code
		}
		return out
	default:
		// starting, or off from before audiod took the config that enabled it
		return state{State: "starting"}
	}
}

// provider is media.airplay: the receiver on or off and the name phones see.
type provider struct{ a *Airplay }

func (*provider) Descriptor() capability.Descriptor {
	return capability.Descriptor{ID: "media.airplay", Version: 1, ConfigSchema: configSchema, StateSchema: stateSchema}
}

func (p *provider) Apply(_ context.Context, _ int64, raw json.RawMessage) error {
	cfg, ok := parseConfig(raw)
	if !ok {
		return &capability.ApplyError{Code: "invalid"}
	}
	p.a.configure(cfg)
	return nil
}

func (*provider) Command(context.Context, string, json.RawMessage) (json.RawMessage, error) {
	return nil, capability.ErrUnsupported
}

func (p *provider) State() <-chan json.RawMessage { return p.a.state }

func parseConfig(raw json.RawMessage) (config, bool) {
	var c struct {
		Enabled json.RawMessage `json:"enabled"`
		Name    json.RawMessage `json:"name"`
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		return config{}, false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(&c) != nil {
		return config{}, false
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return config{}, false
	}
	var out config
	if !field(c.Enabled, &out.Enabled) || !field(c.Name, &out.Name) || len(out.Name) > maxName ||
		c.Name != nil && !name.MatchString(out.Name) {
		return config{}, false
	}
	return out, true
}

// an explicit null is no value of its type
func field(raw json.RawMessage, v any) bool {
	return raw == nil || !bytes.Equal(raw, []byte("null")) && json.Unmarshal(raw, v) == nil
}

func offer[T any](ch chan T, v T) {
	for {
		select {
		case ch <- v:
			return
		default:
		}
		select {
		case <-ch:
		default:
		}
	}
}
