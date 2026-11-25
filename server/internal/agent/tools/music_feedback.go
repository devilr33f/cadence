package tools

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/music"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

const musicFeedbackInput = `{
	"type":"object",
	"properties":{
		"id":{"type":"string","pattern":"^(r[0-9]{1,9}|current)$"},
		"feedback":{"enum":["none","like","dislike"]}
	},
	"required":["id","feedback"],
	"additionalProperties":false
}`

// musicFeedback rates the current song or a bounded result, never an id or URL
// the model supplies. The provider is synchronized only through its stored instance.
func musicFeedback(d MusicDeps, r *musicResults) agent.Tool {
	return agent.Tool{
		Name: "music_feedback",
		Description: "Like, dislike or clear the rating of a song. Use id \"current\" for what plays now or a song's " +
			"music_search result id. like adds it to Cadence favorites and to the provider's library when supported; " +
			"dislike removes it from favorites and tells a supporting provider not to recommend it; none clears both " +
			"marks. It does not skip or change playback. The answer's providerSync says whether the provider was updated.",
		Input: musicFeedbackInput, Effects: agent.Write, Output: agent.Trusted, TaintExempt: true,
		Available: musicAvailable, Timeout: 15 * time.Second,
		Run: func(ctx context.Context, c agent.Call) (json.RawMessage, *agent.ToolError) {
			var a struct {
				ID       string                `json:"id"`
				Feedback providers.TrackRating `json:"feedback"`
			}
			if err := json.Unmarshal(c.Args, &a); err != nil {
				return nil, badArgs("The arguments are not a JSON object.")
			}
			if d.Feedback == nil {
				return nil, unavailable("Cadence favorites are not set up.")
			}
			var track providers.Track
			if a.ID == "current" {
				var ok bool
				track, ok = d.Player.CurrentTrack(c.DeviceID)
				if !ok {
					return nil, &agent.ToolError{Code: "nothing_playing", Message: "There is no current song to rate."}
				}
			} else {
				f, ok := r.get(c.DeviceID, a.ID)
				if !ok {
					return nil, &agent.ToolError{Code: "unknown_result", Message: "No recent search result has this id.", Hint: "Search again, then rate its result id."}
				}
				if f.coll != nil {
					return nil, badArgs("Ratings apply to one song, not a collection.")
				}
				track = f.track
			}
			got, err := d.Feedback.Set(ctx, track, a.Feedback)
			if err != nil {
				return nil, musicError(err)
			}
			return encode(map[string]any{
				"feedback": got.Entry.Feedback, "favorite": got.Entry.Favorite,
				"provider": got.Entry.Provider, "providerSync": got.ProviderSync,
			})
		},
	}
}

const musicFavoritesInput = `{
	"type":"object",
	"properties":{"query":{"type":"string","maxLength":200}},
	"additionalProperties":false
}`

// musicFavorites returns Cadence's own collection, playable by music_play.
func musicFavorites(d MusicDeps, r *musicResults) agent.Tool {
	return agent.Tool{
		Name: "music_favorites",
		Description: "Read Cadence favorites as one collection result, optionally filtered by title, artist or album. " +
			"Use when the user asks for their own favorites/liked songs, then call music_play with its id to play all " +
			"of them now or add them next/later. To rate a song instead, use music_feedback.",
		Input: musicFavoritesInput, Effects: agent.Read, Output: agent.Untrusted,
		Available: musicAvailable, Timeout: 5 * time.Second,
		Run: func(ctx context.Context, c agent.Call) (json.RawMessage, *agent.ToolError) {
			var a struct {
				Query string `json:"query"`
			}
			if err := json.Unmarshal(c.Args, &a); err != nil {
				return nil, badArgs("The arguments are not a JSON object.")
			}
			if d.Feedback == nil {
				return nil, unavailable("Cadence favorites are not set up.")
			}
			a.Query = strings.TrimSpace(a.Query)
			entries, err := d.Feedback.List(ctx, a.Query, music.MaxQueue)
			if err != nil {
				return nil, failed(providers.CodeMusicFailed)
			}
			if len(entries) == 0 {
				return encode(map[string]any{"results": []musicItem{}, "count": 0})
			}
			col := providers.Collection{
				Source: providers.SourceCadence, Kind: providers.KindFavorites, ID: "all",
				Title: "Cadence favorites", Tracks: len(entries), ArtworkURL: entries[0].ArtworkURL,
			}
			if !r.known(c.DeviceID) {
				r.seed(c.DeviceID, lastID(ctx, d.History, c.DeviceID, "music_search", musicSearchList, musicIDPrefix))
			}
			ids := r.add(c.DeviceID, []found{{coll: &col, favoriteQuery: a.Query}})
			return encode(map[string]any{"results": []musicItem{resultItem(ids[0], found{coll: &col})}, "count": len(entries)})
		},
	}
}
