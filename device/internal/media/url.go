package media

import (
	"context"
	"encoding/json"

	"git.shino.boo/devilreef/cadence/device/internal/capability"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
)

// audiod's bounds (Ruling M4b-R4): longer fields fail its whole frame
const (
	maxURLBytes  = 2047
	maxTextBytes = 255
)

const (
	webURLSchema = `{"type":"string","pattern":"^[Hh][Tt][Tt][Pp][Ss]?://.","maxLength":2047}`
	playSchema   = `{"type":"object","properties":{` +
		`"url":` + webURLSchema + `,` +
		`"mode":{"enum":["session","overlay"]},` +
		`"metadata":{"type":"object","properties":{` +
		`"title":{"type":"string"},"artist":{"type":"string"},"album":{"type":"string"},"artworkUrl":` + webURLSchema +
		`},"additionalProperties":false},` +
		`"startMs":{"type":"integer","minimum":0}` +
		`},"required":["url"],"additionalProperties":false}`
)

var urlModes = map[string]audiodv1.UrlMode{"session": audiodv1.UrlMode_URL_MODE_SESSION, "overlay": audiodv1.UrlMode_URL_MODE_OVERLAY}

// urlPlayer is media.url: play an http or https url as the media session, or
// once over it as an overlay. It has no settings and no state.
type urlPlayer struct{ m *Media }

func (*urlPlayer) Descriptor() capability.Descriptor {
	return capability.Descriptor{ID: "media.url", Version: 1, ConfigSchema: noArgsSchema, Commands: map[string]string{"play": playSchema}}
}

func (*urlPlayer) Apply(_ context.Context, _ int64, cfg json.RawMessage) error {
	if decodeObject(cfg, &struct{}{}) != nil {
		return &capability.ApplyError{Code: codeInvalid}
	}
	return nil
}

type playArgs struct {
	URL      *string `json:"url"`
	Mode     *string `json:"mode"`
	Metadata *struct {
		Title      string  `json:"title"`
		Artist     string  `json:"artist"`
		Album      string  `json:"album"`
		ArtworkURL *string `json:"artworkUrl"`
	} `json:"metadata"`
	StartMs uint64 `json:"startMs"`
}

func (u *urlPlayer) Command(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, error) {
	if name != "play" {
		return nil, capability.ErrUnsupported
	}
	play, ok := urlPlay(args)
	if !ok {
		return nil, errInvalid
	}
	return nil, u.m.request(ctx, func(id string) *audiodv1.Envelope {
		play.RequestId = id
		return &audiodv1.Envelope{Body: &audiodv1.Envelope_UrlPlay{UrlPlay: play}}
	})
}

// urlPlay checks play's args and caps the metadata to audiod's bounds.
func urlPlay(args json.RawMessage) (*audiodv1.UrlPlay, bool) {
	var a playArgs
	if decodeObject(args, &a) != nil || a.URL == nil || !webURL(*a.URL) {
		return nil, false
	}
	mode := audiodv1.UrlMode_URL_MODE_SESSION
	if a.Mode != nil {
		var ok bool
		if mode, ok = urlModes[*a.Mode]; !ok {
			return nil, false
		}
	}
	p := &audiodv1.UrlPlay{Url: *a.URL, Mode: mode, StartMs: a.StartMs}
	if md := a.Metadata; md != nil {
		p.Metadata = &audiodv1.MediaMetadata{
			Title: capText(md.Title, maxTextBytes), Artist: capText(md.Artist, maxTextBytes), Album: capText(md.Album, maxTextBytes),
		}
		if md.ArtworkURL != nil {
			if !webURL(*md.ArtworkURL) {
				return nil, false
			}
			p.Metadata.ArtworkUrl = *md.ArtworkURL
		}
	}
	return p, true
}

func (*urlPlayer) State() <-chan json.RawMessage { return nil }
