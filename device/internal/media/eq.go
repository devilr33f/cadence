package media

import (
	"context"
	"encoding/json"

	"git.shino.boo/devilreef/cadence/device/internal/capability"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
)

const (
	maxEqBands = 10

	eqProperties = `"preampDb":{"type":"number","minimum":-12,"maximum":12},` +
		`"bands":{"type":"array","maxItems":10,"items":{"type":"object","properties":{` +
		`"type":{"enum":["peaking","lowShelf","highShelf","lowPass","highPass"]},` +
		`"freqHz":{"type":"number","minimum":20,"maximum":20000},` +
		`"gainDb":{"type":"number","minimum":-12,"maximum":12},` +
		`"q":{"type":"number","minimum":0.1,"maximum":10}` +
		`},"required":["type","freqHz","gainDb","q"],"additionalProperties":false}}`
	eqConfigSchema = `{"type":"object","properties":{` + eqProperties + `},"additionalProperties":false}`
	eqStateSchema  = `{"type":"object","properties":{` + eqProperties + `},"required":["preampDb","bands"],"additionalProperties":false}`
)

var eqTypes = map[string]audiodv1.EqType{
	"peaking": audiodv1.EqType_EQ_TYPE_PEAKING, "lowShelf": audiodv1.EqType_EQ_TYPE_LOW_SHELF,
	"highShelf": audiodv1.EqType_EQ_TYPE_HIGH_SHELF, "lowPass": audiodv1.EqType_EQ_TYPE_LOW_PASS,
	"highPass": audiodv1.EqType_EQ_TYPE_HIGH_PASS,
}

// eqConfig is media.eq's config as applied, and its state.
type eqConfig struct {
	PreampDb float64  `json:"preampDb"`
	Bands    []eqBand `json:"bands"`
}

type eqBand struct {
	Type   string  `json:"type"`
	FreqHz float64 `json:"freqHz"`
	GainDb float64 `json:"gainDb"`
	Q      float64 `json:"q"`
}

func (c eqConfig) proto() *audiodv1.EqSettings {
	s := &audiodv1.EqSettings{PreampDb: float32(c.PreampDb)}
	for _, b := range c.Bands {
		s.Bands = append(s.Bands, &audiodv1.EqBand{Type: eqTypes[b.Type], FreqHz: float32(b.FreqHz), GainDb: float32(b.GainDb), Q: float32(b.Q)})
	}
	return s
}

func within(v *float64, lo, hi float64) bool { return v != nil && *v >= lo && *v <= hi }

// parseEq is the config with its defaults, a flat eq, or false when it is invalid.
func parseEq(raw json.RawMessage) (eqConfig, bool) {
	var c struct {
		PreampDb *float64 `json:"preampDb"`
		Bands    []struct {
			Type   *string  `json:"type"`
			FreqHz *float64 `json:"freqHz"`
			GainDb *float64 `json:"gainDb"`
			Q      *float64 `json:"q"`
		} `json:"bands"`
	}
	if decodeObject(raw, &c) != nil || len(c.Bands) > maxEqBands {
		return eqConfig{}, false
	}
	out := eqConfig{Bands: []eqBand{}}
	if c.PreampDb != nil {
		if !within(c.PreampDb, -12, 12) {
			return eqConfig{}, false
		}
		out.PreampDb = *c.PreampDb
	}
	for _, b := range c.Bands {
		if b.Type == nil || !within(b.FreqHz, 20, 20000) || !within(b.GainDb, -12, 12) || !within(b.Q, 0.1, 10) {
			return eqConfig{}, false
		}
		if _, ok := eqTypes[*b.Type]; !ok {
			return eqConfig{}, false
		}
		out.Bands = append(out.Bands, eqBand{Type: *b.Type, FreqHz: *b.FreqHz, GainDb: *b.GainDb, Q: *b.Q})
	}
	return out, true
}

// equalizer is media.eq: the user's eq over the board's speaker profile.
type equalizer struct {
	m     *Media
	state chan json.RawMessage
}

func (*equalizer) Descriptor() capability.Descriptor {
	return capability.Descriptor{ID: "media.eq", Version: 1, ConfigSchema: eqConfigSchema, StateSchema: eqStateSchema}
}

func (e *equalizer) Apply(_ context.Context, _ int64, cfg json.RawMessage) error {
	c, ok := parseEq(cfg)
	if !ok {
		return &capability.ApplyError{Code: codeInvalid}
	}
	e.m.update(func() {
		e.m.eq = c
		e.publish()
	})
	return nil
}

func (*equalizer) Command(context.Context, string, json.RawMessage) (json.RawMessage, error) {
	return nil, capability.ErrUnsupported
}

func (e *equalizer) State() <-chan json.RawMessage { return e.state }

// callers hold setMu, or own e before it is shared
func (e *equalizer) publish() { publishLatest(e.state, e.m.eq) }
