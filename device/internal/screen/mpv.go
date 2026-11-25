package screen

import (
	"context"
	"encoding/json"
	"sync"

	"git.shino.boo/devilreef/cadence/device/internal/capability"
	"git.shino.boo/devilreef/cadence/pkg/capschema"
	screenv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/screen/v1"
)

// mpv is media.mpv: the screen player's engine (mpv) as the owner tunes it. the config is an allowlisted set of mpv
// options (never an option name or filter string from the server), checked here and again on the screen; the screen
// applies a key live where mpv takes a runtime set and at the next file otherwise, and its state says which keys
// landed, which wait for the next file and which it refused
type mpv struct {
	s     *Server
	state chan json.RawMessage

	mu      sync.Mutex
	screen  bool
	applied []string
	pending []string
	refused []string
}

func newMpv(s *Server) *mpv {
	m := &mpv{s: s, state: make(chan json.RawMessage, 1)}
	m.publish()
	return m
}

func (*mpv) Descriptor() capability.Descriptor {
	return capability.Descriptor{
		ID: capschema.MediaMpvID, Version: 1, ConfigSchema: capschema.MediaMpvConfig,
		StateSchema: capschema.MediaMpvState,
	}
}

func (m *mpv) Apply(_ context.Context, _ int64, cfg json.RawMessage) error {
	return m.s.applyConfig(capschema.MediaMpvID, mpvCfg, cfg)
}

func (*mpv) Command(context.Context, string, json.RawMessage) (json.RawMessage, error) {
	return nil, capability.ErrUnsupported
}

func (m *mpv) State() <-chan json.RawMessage { return m.state }

// attached: a screen took the config it is sent and applies it from nothing, so nothing of the one before holds
func (m *mpv) attached() {
	m.mu.Lock()
	m.screen = true
	m.applied, m.pending, m.refused = nil, nil, nil
	m.mu.Unlock()
	m.publish()
}

// detached: no screen applies anything; the config stays for the next one
func (m *mpv) detached() {
	m.mu.Lock()
	m.screen = false
	m.applied, m.pending, m.refused = nil, nil, nil
	m.mu.Unlock()
	m.publish()
}

// take is the screen's report (screen/v1 CapabilityState): which keys it applied, which wait for the next file and
// which it refused. checked against the state's schema, as the screen's own word is not a guarantee
func (m *mpv) take(raw string) {
	var st struct {
		Screen  bool     `json:"screen"`
		Applied []string `json:"applied"`
		Pending []string `json:"pending"`
		Refused []string `json:"refused"`
	}
	if !mpvState.valid(json.RawMessage(raw), &st) {
		m.s.log.Warn("mpv_state_refused")
		return
	}
	m.mu.Lock()
	m.screen, m.applied, m.pending, m.refused = st.Screen, cap64(st.Applied), cap64(st.Pending), cap64(st.Refused)
	m.mu.Unlock()
	m.publish()
}

func (m *mpv) publish() {
	m.mu.Lock()
	st := struct {
		Screen  bool     `json:"screen"`
		Applied []string `json:"applied"`
		Pending []string `json:"pending"`
		Refused []string `json:"refused"`
	}{m.screen, cap64(m.applied), cap64(m.pending), cap64(m.refused)}
	m.mu.Unlock()
	offer(m.state, encode(st))
}

// cap64 keeps the state's schema bound (64 keys, more than the config has)
func cap64(in []string) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		if len(out) == 64 || len(v) == 0 || len(v) > 32 {
			break
		}
		out = append(out, v)
	}
	return out
}

// capabilityState is what a screen that applies a config itself reports of it. only media.mpv has one; another
// capability's report names a screen cadenced does not know
func (s *Server) capabilityState(cs *screenv1.CapabilityState) {
	if cs.GetCapability() != capschema.MediaMpvID {
		s.log.Info("screen_state_unhandled", "capability", cs.GetCapability())
		return
	}
	s.mpv.take(cs.GetJson())
}
