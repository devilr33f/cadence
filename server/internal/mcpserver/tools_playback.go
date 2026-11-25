package mcpserver

import (
	"context"
	"encoding/json"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// playbackTools serves get_playback, playback_history and list_favorites.
func (s *Server) playbackTools(take func(definition)) {
	take(definition{
		name: "get_playback",
		description: "What the household's devices play right now: each one's media session (state, title, artist, " +
			"position, the actions it takes), the music queue behind a speaker's session and what a screen's catalog " +
			"session shows. One device, or every one when device is left out.",
		schema: object(map[string]any{
			"device": prop("One device's name or id; leave out for every device.", nil),
		}),
		call: func(ctx context.Context, args json.RawMessage) (*sdk.CallToolResult, error) {
			var one bool
			var ids []string
			if stringArg(args, "device") != "" {
				id, refused := s.deviceArg(ctx, args)
				if refused != nil {
					return refused, nil
				}
				ids, one = []string{id}, true
			} else if s.d.List != nil {
				list, err := s.d.List(ctx)
				if err != nil {
					return toolError("internal", "The devices could not be read."), nil
				}
				for _, d := range list {
					ids = append(ids, d.ID)
				}
			}
			out := make([]map[string]any, 0, len(ids))
			for _, id := range ids {
				entry := map[string]any{"deviceId": id}
				if s.d.Session != nil {
					if sess, ok := s.d.Session(id); ok {
						entry["session"] = sess
					}
				}
				if s.d.Queue != nil {
					if q := s.d.Queue(id); q.Active {
						entry["queue"] = q
					}
				}
				if len(entry) > 1 || one {
					out = append(out, entry)
				}
			}
			return textResult(map[string]any{"devices": out})
		},
	})
	take(definition{
		name: "playback_history",
		description: "The household's recently played items, kept 14 days: songs, videos and urls with their " +
			"device, source and how far they played. One device, or the whole household.",
		schema: object(map[string]any{
			"device": prop("One device's name or id; leave out for the whole household.", nil),
			"limit":  map[string]any{"type": "integer", "description": "At most this many items (default 25, at most 100).", "minimum": 1, "maximum": 100},
			"before": prop("List only items played before this history cursor (a playback_history page's nextBefore).", nil),
		}),
		call: func(ctx context.Context, args json.RawMessage) (*sdk.CallToolResult, error) {
			if s.d.History == nil {
				return toolError("unavailable", "No history store."), nil
			}
			var a struct {
				Device string `json:"device"`
				Before string `json:"before"`
				Limit  int    `json:"limit"`
			}
			_ = json.Unmarshal(args, &a)
			if a.Limit == 0 {
				a.Limit = 25
			}
			device := ""
			if a.Device != "" {
				id, refused := s.deviceArg(ctx, args)
				if refused != nil {
					return refused, nil
				}
				device = id
			}
			page, err := s.d.History(ctx, device, a.Before, a.Limit)
			if err != nil {
				return toolError("internal", "The history could not be read."), nil
			}
			return textResult(page)
		},
	})
	take(definition{
		name: "list_favorites",
		description: "The household's music favorites: the songs it liked, kept until cleared, with their " +
			"providers and web links.",
		schema: object(map[string]any{
			"q":     prop("Only favorites whose title, artist or album contains this.", nil),
			"limit": map[string]any{"type": "integer", "description": "At most this many (default 25, at most 100).", "minimum": 1, "maximum": 100},
		}),
		call: func(ctx context.Context, args json.RawMessage) (*sdk.CallToolResult, error) {
			if s.d.Favorite == nil {
				return toolError("unavailable", "No favorites store."), nil
			}
			var a struct {
				Q     string `json:"q"`
				Limit int    `json:"limit"`
			}
			_ = json.Unmarshal(args, &a)
			if a.Limit == 0 {
				a.Limit = 25
			}
			list, err := s.d.Favorite(ctx, a.Q, a.Limit)
			if err != nil {
				return toolError("internal", "The favorites could not be read."), nil
			}
			return textResult(map[string]any{"favorites": list})
		},
	})
}

// stringArg reads one string argument, "" when absent.
func stringArg(args json.RawMessage, name string) string {
	var m map[string]any
	if json.Unmarshal(args, &m) != nil {
		return ""
	}
	v, _ := m[name].(string)
	return v
}
