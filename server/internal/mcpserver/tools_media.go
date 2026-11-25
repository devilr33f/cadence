package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"git.shino.boo/devilreef/cadence/server/internal/assistant"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/voice"
)

// mediaTools serves say, assistant_turn, play_url and get_media_link.
func (s *Server) mediaTools(take func(definition)) {
	take(definition{
		name: "say",
		description: "Speak a text announcement on a device over its music (its TTS voice, ducking what plays). " +
			"The same announcement the dashboard and Home Assistant send; at most 500 characters.",
		schema: object(map[string]any{
			"device": prop("The device's name or id.", nil),
			"text":   prop("The text to speak.", map[string]any{"minLength": 1, "maxLength": 500}),
			"language": prop("The text's language (ru or en); the device's default when left out.",
				map[string]any{"enum": []string{"ru", "en"}}),
		}, "device", "text"),
		call: func(ctx context.Context, args json.RawMessage) (*sdk.CallToolResult, error) {
			if s.d.Announce == nil {
				return toolError("unavailable", "No voice coordinator."), nil
			}
			device, refused := s.deviceArg(ctx, args)
			if refused != nil {
				return refused, nil
			}
			var a struct {
				Text     string `json:"text"`
				Language string `json:"language"`
			}
			_ = json.Unmarshal(args, &a)
			start := s.now()
			_, err := s.d.Announce(ctx, device, voice.Announcement{Text: a.Text, Language: a.Language})
			code := announceCode(err)
			s.logTool("say", start, code)
			if code != "ok" {
				return toolError(code, "The announcement did not play."), nil
			}
			return textResult(map[string]any{"said": true})
		},
	})
	take(definition{
		name: "assistant_turn",
		description: "Run the device's own assistant on a typed request, as the dashboard's typed turn does, and " +
			"answer its reply: the full agent with its tools, its memory and its home. speak also plays the reply " +
			"on the device. One turn a device at a time; it can take up to a minute.",
		schema: object(map[string]any{
			"device": prop("The device whose assistant answers.", nil),
			"text":   prop("The request, as if spoken to the device.", map[string]any{"minLength": 1, "maxLength": 2000}),
			"speak":  map[string]any{"type": "boolean", "description": "Also play the reply on the device (default false)."},
			"language": prop("The request's language; the device's default when left out.",
				map[string]any{"enum": []string{"ru", "en"}}),
		}, "device", "text"),
		call: func(ctx context.Context, args json.RawMessage) (*sdk.CallToolResult, error) {
			if s.d.Ask == nil {
				return toolError("unavailable", "No assistant service."), nil
			}
			device, refused := s.deviceArg(ctx, args)
			if refused != nil {
				return refused, nil
			}
			var a struct {
				Text     string `json:"text"`
				Language string `json:"language"`
				Speak    bool   `json:"speak"`
			}
			_ = json.Unmarshal(args, &a)
			start := s.now()
			res, spoken, err := s.d.Ask(ctx, assistant.Request{DeviceID: device, Text: a.Text, Language: a.Language, Speak: a.Speak})
			switch {
			case errors.Is(err, assistant.ErrBusy):
				s.logTool("assistant_turn", start, "busy")
				return toolError("busy", "A typed turn is already running on that device."), nil
			case err != nil:
				s.logTool("assistant_turn", start, "internal")
				return toolError("internal", "The turn could not run."), nil
			case res.Code != "":
				s.logTool("assistant_turn", start, res.Code)
				return toolError(res.Code, "The turn failed."), nil
			}
			s.logTool("assistant_turn", start, "ok")
			return textResult(map[string]any{"reply": spoken})
		},
	})
	take(definition{
		name: "play_url",
		description: "Play a media url on a device: an audio stream or file (or a video url on a screen), the same " +
			"play Home Assistant's play_media sends. mode session replaces what plays (a media session of its " +
			"own); mode overlay speaks over what plays.",
		schema: object(map[string]any{
			"device": prop("The device's name or id.", nil),
			"url":    prop("The media url: a stream, an audio file, a video the device can play.", map[string]any{"minLength": 8, "maxLength": 2048}),
			"mode":   prop("session replaces what plays (default); overlay ducks it.", map[string]any{"enum": []string{"session", "overlay"}}),
			"title":  prop("A title to show for the url.", nil),
			"artist": prop("An artist to show for the url.", nil),
		}, "device", "url"),
		call: func(ctx context.Context, args json.RawMessage) (*sdk.CallToolResult, error) {
			if s.d.Send == nil {
				return toolError("unavailable", "No command service."), nil
			}
			device, refused := s.deviceArg(ctx, args)
			if refused != nil {
				return refused, nil
			}
			var a struct {
				URL    string `json:"url"`
				Mode   string `json:"mode"`
				Title  string `json:"title"`
				Artist string `json:"artist"`
			}
			_ = json.Unmarshal(args, &a)
			if a.Mode == "" {
				a.Mode = "session"
			}
			play := map[string]any{"url": a.URL, "mode": a.Mode}
			if a.Title != "" || a.Artist != "" {
				meta := map[string]any{}
				if a.Title != "" {
					meta["title"] = a.Title
				}
				if a.Artist != "" {
					meta["artist"] = a.Artist
				}
				play["metadata"] = meta
			}
			payload, _ := json.Marshal(play)
			start := s.now()
			_, err := s.d.Send(ctx, device, "media.url", "play", payload)
			if err != nil {
				s.logTool("play_url", start, commandCode(err))
				return toolError(commandCode(err), "The url did not play."), nil
			}
			s.logTool("play_url", start, "ok")
			return textResult(map[string]any{"playing": true})
		},
	})
	take(definition{
		name: "get_media_link",
		description: "Answer a playable, token-sealed link for media the server can stream: a music_search result " +
			"by its id (its provider's own stream, or YouTube's audio), any YouTube video id, or a catalog item's " +
			"episode by its ref. The link streams and downloads (http ranges), works without further " +
			"authentication and expires after some hours; ask for it again then.",
		schema: object(map[string]any{
			"device":  prop("The device whose search found the result (music ids are per device); the link plays anywhere.", nil),
			"musicId": prop("A music_search result id, e.g. r12.", nil),
			"videoId": prop("A YouTube video id, for its audio stream.", nil),
			"ref":     prop("A catalog item ref, with episode for one episode.", nil),
			"episode": prop("The episode label inside a catalog item, e.g. \"5\".", nil),
		}),
		call: func(ctx context.Context, args json.RawMessage) (*sdk.CallToolResult, error) {
			var a struct {
				Device  string `json:"device"`
				MusicID string `json:"musicId"`
				VideoID string `json:"videoId"`
				Ref     string `json:"ref"`
				Episode string `json:"episode"`
			}
			_ = json.Unmarshal(args, &a)
			switch {
			case a.MusicID != "":
				if a.Device == "" {
					return toolError("device_required", "A music result needs the device whose search found it."), nil
				}
				device, refused := s.deviceArg(ctx, args)
				if refused != nil {
					return refused, nil
				}
				return s.musicLink(ctx, device, a.MusicID)
			case a.VideoID != "":
				return s.ytLink(ctx, a.Device, a.VideoID)
			case a.Ref != "":
				return s.catalogLink(ctx, a.Ref, a.Episode)
			}
			return toolError("bad_arguments", "Name one of musicId, videoId or ref."), nil
		},
	})
}

// announceCode is a refused announcement's class, the assistant's own.
func announceCode(err error) string {
	switch {
	case err == nil:
		return "ok"
	case errors.Is(err, voice.ErrOffline):
		return "device_offline"
	case errors.Is(err, voice.ErrNoAudio):
		return "no_audio"
	case errors.Is(err, voice.ErrBusy):
		return "busy"
	case errors.Is(err, voice.ErrTTSUnconfigured):
		return "tts_unconfigured"
	case errors.Is(err, voice.ErrSuperseded):
		return "superseded"
	case errors.Is(err, voice.ErrTTSFailed):
		return "tts_failed"
	case errors.Is(err, voice.ErrTTSTimeout):
		return "tts_timeout"
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return "canceled"
	}
	return "internal"
}

// musicLink relays the song a device's search result names: its provider's
// own stream, or YouTube's audio for a song without one.
func (s *Server) musicLink(ctx context.Context, device, id string) (*sdk.CallToolResult, error) {
	if s.d.Track == nil {
		return toolError("unavailable", "No music service."), nil
	}
	track, ok := s.d.Track(device, id)
	if !ok {
		return toolError("unknown_result", "No recent music result of that device has this id."), nil
	}
	if stream, ok, err := s.nativeStream(ctx, track); ok {
		return s.link(device, stream, "native")
	} else if err != nil && track.VideoID == "" {
		return toolError("unplayable", "The song's provider would not stream it."), nil
	}
	if track.VideoID == "" {
		return toolError("unplayable", "The song has no stream this server can relay."), nil
	}
	return s.ytLink(ctx, device, track.VideoID)
}

// nativeStream resolves a track's own provider stream.
func (s *Server) nativeStream(ctx context.Context, track providers.Track) (providers.Stream, bool, error) {
	if track.Instance == "" || s.d.Music == nil {
		return providers.Stream{}, false, nil
	}
	m, err := s.d.Music(ctx, track.Instance)
	if err != nil {
		return providers.Stream{}, false, nil
	}
	st, ok := m.(providers.Streamer)
	if !ok {
		return providers.Stream{}, false, nil
	}
	stream, err := st.StreamTrack(ctx, track)
	if err == nil && stream.URL != "" {
		return stream, true, nil
	}
	return providers.Stream{}, false, err
}

// ytLink relays a YouTube video's audio stream.
func (s *Server) ytLink(ctx context.Context, device, videoID string) (*sdk.CallToolResult, error) {
	if s.d.Player == nil {
		return toolError("unavailable", "No player resolves YouTube streams."), nil
	}
	p, err := s.d.Player(ctx)
	if err != nil {
		return toolError("unavailable", "No player resolves YouTube streams."), nil
	}
	stream, err := p.Resolve(ctx, videoID)
	if err != nil {
		return toolError("unplayable", "The video would not resolve."), nil
	}
	return s.link(device, stream, "youtube")
}

// catalogLink resolves a catalog item's episode to its playable stream: the
// url the device itself would play (a provider's own, or one the resolver
// already relayed), with the expiry the resolver reported.
func (s *Server) catalogLink(ctx context.Context, ref, episode string) (*sdk.CallToolResult, error) {
	if s.d.ResolveCatalog == nil {
		return toolError("unavailable", "No catalog service."), nil
	}
	view, err := s.d.ResolveCatalog(ctx, ref, episode)
	if err != nil {
		return toolError("unplayable", "The item would not resolve."), nil
	}
	if view.Stream == nil || view.Stream.URL == "" {
		return toolError("unplayable", "The item has no stream behind this choice."), nil
	}
	out := map[string]any{"url": view.Stream.URL, "kind": string(view.Kind),
		"durationS": view.Stream.DurationMS / 1000}
	if view.Stream.Expires != nil {
		out["expiresAt"] = view.Stream.Expires.UTC().Format(time.RFC3339)
	}
	return textResult(out)
}

// link relays a resolved stream and answers its url with its expiry.
func (s *Server) link(device string, stream providers.Stream, kind string) (*sdk.CallToolResult, error) {
	if s.d.Stream == nil {
		return textResult(map[string]any{"url": stream.URL, "kind": kind, "relayed": false,
			"durationS": stream.DurationS})
	}
	url, err := s.d.Stream(device, stream)
	if err != nil {
		return toolError("failed", "The stream could not be relayed."), nil
	}
	out := map[string]any{"url": url, "kind": kind, "relayed": true, "durationS": stream.DurationS}
	if exp := s.d.RelayExpiry(); !exp.IsZero() {
		out["expiresAt"] = exp.UTC().Format(time.RFC3339)
	}
	return textResult(out)
}
