package soloist

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/capability"
	"git.shino.boo/devilreef/cadence/device/internal/soloist/updater"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
)

const (
	maxName = 32

	configSchema = `{"type":"object","properties":{"enabled":{"type":"boolean"},` +
		`"deviceName":{"type":"string","minLength":1,"maxLength":32,"pattern":"^[A-Za-z0-9._-]([A-Za-z0-9._ -]*[A-Za-z0-9._-])?$"}},` +
		`"additionalProperties":false}`
	stateSchema = `{"type":"object","properties":{` +
		`"state":{"enum":["off","key_missing","no_build","starting","ready","expired","failed"]},` +
		`"code":{"type":"string","pattern":"^[a-z0-9_]{1,64}$"},"build":{"type":"string","pattern":"^[0-9]{8}$"},` +
		`"expiresAt":{"type":"string","format":"date-time"}},"required":["state"],"additionalProperties":false}`
)

var (
	// letters, digits, dot, underscore, hyphen and inner spaces
	deviceName = regexp.MustCompile(`^[A-Za-z0-9._-]([A-Za-z0-9._ -]*[A-Za-z0-9._-])?$`)
	codeToken  = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)
)

type config struct {
	Enabled bool
	Name    string // "" for the device's own (SetDeviceName)
}

// state never carries the key, nor what soloist plays
type state struct {
	State     string `json:"state"`
	Code      string `json:"code,omitempty"`
	Build     string `json:"build,omitempty"`
	ExpiresAt string `json:"expiresAt,omitempty"`
}

// provider is media.soloist: soloist on or off, the name phones see, and the
// owner's api key as its one secret.
type provider struct {
	s     *Soloist
	state chan json.RawMessage
}

func (*provider) Descriptor() capability.Descriptor {
	return capability.Descriptor{
		ID: "media.soloist", Version: 1, ConfigSchema: configSchema, StateSchema: stateSchema,
		Secrets: []capability.SecretSpec{{Name: secretKey, Description: "Spotify Soloist API key, from the Spotify for Developers dashboard"}},
	}
}

// Apply takes a config with no key.
func (p *provider) Apply(ctx context.Context, gen int64, cfg json.RawMessage) error {
	return p.ApplySecrets(ctx, gen, cfg, nil)
}

// ApplySecrets takes {enabled, deviceName} and the key that came with it; a
// key that is not one leaves soloist off, as key_missing.
func (p *provider) ApplySecrets(_ context.Context, _ int64, raw json.RawMessage, secrets map[string]string) error {
	cfg, ok := parseConfig(raw)
	if !ok {
		return &capability.ApplyError{Code: "invalid"}
	}
	key, problem := checkKey(secrets)
	p.s.configure(cfg, key, problem)
	return nil
}

func (*provider) Command(context.Context, string, json.RawMessage) (json.RawMessage, error) {
	return nil, capability.ErrUnsupported
}

func (p *provider) State() <-chan json.RawMessage { return p.state }

func parseConfig(raw json.RawMessage) (config, bool) {
	var c struct {
		Enabled    json.RawMessage `json:"enabled"`
		DeviceName json.RawMessage `json:"deviceName"`
	}
	if decodeObject(raw, &c) != nil {
		return config{}, false
	}
	var out config
	if !field(c.Enabled, &out.Enabled) || !field(c.DeviceName, &out.Name) || len(out.Name) > maxName ||
		c.DeviceName != nil && !deviceName.MatchString(out.Name) {
		return config{}, false
	}
	return out, true
}

// field decodes a field that is present: an explicit null is no value of its type
func field(raw json.RawMessage, v any) bool {
	return raw == nil || !bytes.Equal(raw, []byte("null")) && json.Unmarshal(raw, v) == nil
}

// decodeObject decodes one JSON object into v and refuses unknown fields and
// anything after it
func decodeObject(raw []byte, v any) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		return errors.New("not an object")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing data")
	}
	return nil
}

// publishLocked offers the state when it changed, under mu so that states go
// out in the order they were made. callers hold mu
func (s *Soloist) publishLocked() {
	st := s.stateLocked()
	if st == s.shown && s.shown.State != "" {
		return
	}
	if st.State != s.shown.State || st.Code != s.shown.Code {
		s.log.Info("soloist_state", "state", st.State, "code", st.Code)
	}
	s.shown = st
	b, err := json.Marshal(st)
	if err != nil {
		return
	}
	offer(s.cap.state, json.RawMessage(b))
}

// callers hold mu
func (s *Soloist) stateLocked() state {
	var out state
	switch {
	case !s.cfg.Enabled:
		out.State = "off"
	case s.key == "":
		out.State, out.Code = "key_missing", s.problem
	case s.status == nil:
		out.State = "starting"
	default:
		out.State, out.Code = fromStatus(s.status)
	}
	if b, ok := s.buildLocked(); ok {
		out.Build, out.ExpiresAt = b.Date(), b.ExpiresAt().Format(time.RFC3339)
	}
	return out
}

// buildLocked is the installed build as the updater last read it, else the
// one soloist runs as audiod read it. callers hold mu
func (s *Soloist) buildLocked() (updater.Build, bool) {
	if s.installedKnown {
		if s.installed == nil {
			return updater.Build{}, false
		}
		return *s.installed, true
	}
	if s.status == nil || s.status.GetBuildEpoch() <= 0 {
		return updater.Build{}, false
	}
	return updater.Build{Version: s.status.GetVersion(), Epoch: s.status.GetBuildEpoch()}, true
}

// fromStatus maps audiod's state; a code is kept only as a token
func fromStatus(st *audiodv1.SoloistStatus) (name, code string) {
	if codeToken.MatchString(st.GetCode()) {
		code = st.GetCode()
	}
	switch st.GetState() {
	case audiodv1.SoloistState_SOLOIST_STATE_NO_BUILD:
		return "no_build", ""
	case audiodv1.SoloistState_SOLOIST_STATE_READY:
		return "ready", ""
	case audiodv1.SoloistState_SOLOIST_STATE_EXPIRED:
		return "expired", ""
	case audiodv1.SoloistState_SOLOIST_STATE_FAILED:
		return "failed", code
	case audiodv1.SoloistState_SOLOIST_STATE_KEY_MISSING:
		return "key_missing", code
	default:
		// starting, or off from before audiod took the config that enabled it
		return "starting", ""
	}
}

// offer puts v in a channel of one that keeps the latest value; it never blocks.
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
