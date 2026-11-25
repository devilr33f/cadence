package media

import (
	"context"
	"encoding/json"

	"git.shino.boo/devilreef/cadence/device/internal/capability"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
)

const (
	vizMusic = "music"
	vizBands = "bands"

	vizConfigSchema = `{"type":"object","properties":{"enabled":{"type":"boolean"},"source":{"enum":["music","all"]},` +
		`"style":{"enum":["bands","pulse","spectrum","equalizer","orbit","beat"]}},"additionalProperties":false}`
	vizStateSchema = `{"type":"object","properties":{"enabled":{"type":"boolean"},"source":{"enum":["music","all"]},` +
		`"style":{"enum":["bands","pulse","spectrum","equalizer","orbit","beat"]}},"required":["enabled","source","style"],"additionalProperties":false}`
)

var vizSources = map[string]audiodv1.VizSource{vizMusic: audiodv1.VizSource_VIZ_SOURCE_MUSIC, "all": audiodv1.VizSource_VIZ_SOURCE_ALL}

var vizStyles = map[string]bool{vizBands: true, "pulse": true, "spectrum": true, "equalizer": true, "orbit": true, "beat": true}

// vizConfig is media.viz's config as applied, and its state: on by default, as v1's bands were
type vizConfig struct {
	Enabled bool   `json:"enabled"`
	Source  string `json:"source"`
	Style   string `json:"style"`
}

var vizDefault = vizConfig{Enabled: true, Source: vizMusic, Style: vizBands}

// VizFrame is one frame of spectrum bands, 0-255 each, and the style the lights show them in.
type VizFrame struct {
	Bands [16]byte
	Style string
}

func (c vizConfig) proto() *audiodv1.VizSettings {
	return &audiodv1.VizSettings{Enabled: c.Enabled, Source: vizSources[c.Source]}
}

// visualizer is media.viz: whether audiod sends the spectrum bands, and of what.
type visualizer struct {
	m     *Media
	state chan json.RawMessage
}

func (*visualizer) Descriptor() capability.Descriptor {
	return capability.Descriptor{ID: "media.viz", Version: 1, ConfigSchema: vizConfigSchema, StateSchema: vizStateSchema}
}

func (v *visualizer) Apply(_ context.Context, _ int64, cfg json.RawMessage) error {
	var c struct {
		Enabled *bool   `json:"enabled"`
		Source  *string `json:"source"`
		Style   *string `json:"style"`
	}
	if decodeObject(cfg, &c) != nil {
		return &capability.ApplyError{Code: codeInvalid}
	}
	applied := vizDefault
	if c.Enabled != nil {
		applied.Enabled = *c.Enabled
	}
	if c.Source != nil {
		applied.Source = *c.Source
	}
	if c.Style != nil {
		applied.Style = *c.Style
	}
	if _, ok := vizSources[applied.Source]; !ok || !vizStyles[applied.Style] {
		return &capability.ApplyError{Code: codeInvalid}
	}
	v.m.update(func() {
		v.m.viz = applied
		v.m.vizStyle.Store(&applied.Style)
		v.publish()
	})
	return nil
}

func (*visualizer) Command(context.Context, string, json.RawMessage) (json.RawMessage, error) {
	return nil, capability.ErrUnsupported
}

func (v *visualizer) State() <-chan json.RawMessage { return v.state }

// callers hold setMu, or own v before it is shared
func (v *visualizer) publish() { publishLatest(v.state, v.m.viz) }
