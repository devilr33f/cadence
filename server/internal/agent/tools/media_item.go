package tools

import (
	"context"
	"encoding/json"
	"slices"
	"time"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/catalog"
)

const mediaSwitchInput = `{
	"type": "object",
	"properties": {
		"source": {"type": "string", "minLength": 1, "maxLength": 64},
		"voiceover": {"type": "string", "minLength": 1, "maxLength": 64},
		"quality": {"type": "string", "minLength": 1, "maxLength": 64},
		"audio": {"type": "string", "minLength": 1, "maxLength": 64},
		"subtitles": {"type": "string", "minLength": 1, "maxLength": 64},
		` + deviceProp + `
	},
	"additionalProperties": false
}`

const mediaItemActionInput = `{
	"type": "object",
	"properties": {
		"action": {"enum": ["like", "dislike", "save", "subscribe", "my_list", "watched"]},
		"on": {"type": "boolean"},
		"value": {"type": "string", "minLength": 1, "maxLength": 32},
		"ref": {"type": "string", "minLength": 1, "maxLength": 256},
		` + deviceProp + `
	},
	"required": ["action"],
	"additionalProperties": false
}`

// mediaSwitch switches inside what a screen plays: another source, voiceover or quality resolves the same episode
// again and starts it at the position the player reported; audio and subtitles pick one of the player's tracks.
func mediaSwitch(d ScreenDeps) agent.Tool {
	return agent.Tool{
		Name: "media_switch",
		Description: "Switch inside the episode a screen plays, keeping the position: source, voiceover or quality " +
			"(choice ids from catalog_item), or audio or subtitles (track ids from screen_state). " + deviceHint +
			"From a speaker, the default is its room's screen. Say nothing after it succeeds.",
		Input:   mediaSwitchInput,
		Effects: agent.Write, Output: agent.Trusted,
		TaintExempt: true, SilentOnSuccess: true, NoEventTurns: true, Reach: agent.ScreenReachable,
		Available: d.catalogAvailable,
		Timeout:   catalogPlayTimeout,
		Run: func(ctx context.Context, c agent.Call) (json.RawMessage, *agent.ToolError) {
			var a struct {
				playArgs
				Audio     string `json:"audio"`
				Subtitles string `json:"subtitles"`
			}
			if err := json.Unmarshal(c.Args, &a); err != nil {
				return nil, badArgs("The arguments are not a JSON object.")
			}
			sel := a.selection()
			if len(sel) == 0 && a.Audio == "" && a.Subtitles == "" {
				return nil, badArgs("Give what to switch: source, voiceover, quality, audio or subtitles.")
			}
			t, e := target(ctx, d.Target, c, a.Device, agent.Need{Capability: agent.CapMediaVideo, Screen: true, Write: true})
			if e != nil {
				return nil, e
			}
			var p *cadencev2.ScreenPlayer
			if d.Contexts != nil {
				if sc, ok := d.Contexts(t.ID); ok {
					p = sc.GetPlayer()
				}
			}
			if p.GetItemRef() == "" {
				return nil, &agent.ToolError{Code: "nothing_playing", Message: "Nothing from the catalog plays on that screen."}
			}
			for _, tr := range []struct{ kind, id string }{{"audio", a.Audio}, {"subtitle", a.Subtitles}} {
				if tr.id == "" {
					continue
				}
				if !slices.ContainsFunc(p.GetTracks(), func(x *cadencev2.MediaTrack) bool {
					return x.GetId() == tr.id && trackKind(x.GetKind()) == tr.kind
				}) {
					return nil, &agent.ToolError{Code: "unknown_track", Message: "The player has no " + tr.kind + " track with this id.",
						Hint: "Read the track ids with screen_state."}
				}
				raw, _ := json.Marshal(map[string]string{"kind": tr.kind, "id": tr.id})
				if _, err := d.Command(ctx, t.ID, agent.CapMediaVideo, "select_track", raw); err != nil {
					return nil, screenError(err)
				}
			}
			if len(sel) == 0 {
				return encode(map[string]bool{"ok": true})
			}
			ref := catalog.Ref(p.GetItemRef())
			episode := ""
			if it, ok := d.Catalog.Cached(ref); ok && it.Episode != nil {
				episode = it.Episode.Label
			}
			pos := int64(min(p.GetPositionMs(), 1<<40)) //nolint:gosec // bounded above
			return d.play(ctx, t.ID, ref, episode, sel, catalog.FromStart, pos)
		},
	}
}

// mediaItemAction runs an item action (like, save, subscribe, my list, watched) on a ref this server issued: the one
// given, else what the target screen shows (its player's item while it plays, else the focused item, then the
// page's). A follow-up may run one; event turns do not get it.
func mediaItemAction(d ScreenDeps) agent.Tool {
	return agent.Tool{
		Name: "media_item_action",
		Description: "Like, dislike, save, subscribe to, set the my-list status of (my_list with value: watching, " +
			"planned, completed, on_hold, dropped) or mark watched an item. on: false undoes it (unlike, unwatched). " +
			"ref names the item; leave it out for what the screen shows (" + deviceHint + "). Say nothing after it succeeds.",
		Input:   mediaItemActionInput,
		Effects: agent.Write, Output: agent.Trusted,
		TaintExempt: true, SilentOnSuccess: true, NoEventTurns: true,
		Reach: agent.ScreenReachable, Available: d.catalogAvailable,
		Timeout: 15 * time.Second,
		Run: func(ctx context.Context, c agent.Call) (json.RawMessage, *agent.ToolError) {
			var a struct {
				Action string `json:"action"`
				On     *bool  `json:"on"`
				Value  string `json:"value"`
				Ref    string `json:"ref"`
				Device string `json:"device"`
			}
			if err := json.Unmarshal(c.Args, &a); err != nil {
				return nil, badArgs("The arguments are not a JSON object.")
			}
			ref := a.Ref
			if ref == "" {
				t, e := target(ctx, d.Target, c, a.Device, screenNeed(false))
				if e != nil {
					return nil, e
				}
				if ref = d.shown(t.ID); ref == "" {
					return nil, noContext()
				}
			}
			det, err := d.Catalog.Item(ctx, agent.CatalogItem{Ref: catalog.Ref(ref), Limit: 1})
			if err != nil {
				return nil, catalogError(err)
			}
			i := slices.IndexFunc(det.Actions, func(x catalog.Action) bool { return string(x.Kind) == a.Action })
			if i < 0 {
				return nil, &agent.ToolError{Code: "unsupported", Message: "This item has no such action."}
			}
			on := a.On == nil || *a.On
			value := a.Value
			if a.Action == string(catalog.ActionMyList) && on && value == "" {
				return nil, badArgs("my_list needs value.")
			}
			if !on {
				value = ""
			}
			acts, err := d.Catalog.Act(ctx, catalog.Ref(ref), det.Actions[i].ID, on, value)
			if err != nil {
				return nil, catalogError(err)
			}
			out := make([]actionView, 0, len(acts))
			for _, x := range acts {
				out = append(out, actionView{Kind: string(x.Kind), Label: x.Label, Toggled: x.Toggled, Enabled: x.Enabled,
					Value: x.Value, Values: x.Values})
			}
			return encode(map[string]any{"ok": true, "actions": out})
		},
	}
}

// shown is the ref a screen's "this" means: the player's item while it plays, else the focused item, then the page's
func (d ScreenDeps) shown(deviceID string) string {
	if d.Contexts == nil {
		return ""
	}
	sc, ok := d.Contexts(deviceID)
	if !ok {
		return ""
	}
	switch sc.GetRoute() {
	case cadencev2.ScreenRoute_SCREEN_ROUTE_PLAYER, cadencev2.ScreenRoute_SCREEN_ROUTE_AUDIO:
		if r := sc.GetPlayer().GetItemRef(); r != "" {
			return r
		}
	}
	for _, r := range []string{sc.GetFocusedRef(), sc.GetPageRef(), sc.GetPlayer().GetItemRef()} {
		if r != "" {
			return r
		}
	}
	return ""
}
