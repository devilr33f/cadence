package cards

import (
	"context"
	"encoding/json"
	"time"

	pcards "git.shino.boo/devilreef/cadence/pkg/cards"
	cardsv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/cards/v1"
)

// ToolResult is one tool call of a turn as the turn's screens see it: the
// agent's tool_call and tool_result events, joined by the call's id.
type ToolResult struct {
	// Key is the turn's and the call's ids: each call's card has its own id
	Key  string
	Tool string
	OK   bool
	// Code is the tool's error code when it failed
	Code string
	// Args are the model's arguments: builders read enumerated fields alone
	Args json.RawMessage
	// Data is the tool's own output, the data of the agent's result envelope
	Data json.RawMessage
	// At is when the result came, for a countdown's end
	At time.Time
	// DeviceID is the turn's device, whose search results the call read; Target the device the call acted on, ""
	// for the turn's own
	DeviceID, Target string
}

// ResultData is the data of the agent's result envelope ({ok, data, error,
// target}): the tool's own output, nil when there is none.
func ResultData(envelope json.RawMessage) json.RawMessage {
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if json.Unmarshal(envelope, &env) != nil || len(env.Data) == 0 || string(env.Data) == "null" {
		return nil
	}
	return env.Data
}

// ToolCards answers a tool call's cards. Every built-in tool has its builder
// in toolBuilders, or none by design (its effect is the screen itself, or its
// tool attaches its own cards); any other tool, an MCP server's, gets the
// generic card: its name and how it ended, never its arguments or result.
type ToolCards struct {
	HA HADeps
	// Task is a background task's live card by its short id; nil shows none.
	// Set before the first turn, once the task cards exist.
	Task func(shortID string) Subject
	// Music reads the music results a call names and plays one a tile is pressed on; nil shows lists without
	// covers or presses
	Music MusicResults
	// Videos reads the video tools' results and plays one a tile is pressed on; nil shows video_search's list
	// without covers or presses
	Videos VideoResults
	// Images issues the image proxy's id of a cover; nil shows none
	Images ImageRefs
}

// toolBuilder answers a successful call's cards; a nil builder shows none.
type toolBuilder func(t *ToolCards, r ToolResult) []Subject

// Subjects are the cards of one call, fresh subjects each call (one per
// screen). A built-in tool that failed shows none: its step says so.
func (t *ToolCards) Subjects(r ToolResult) []Subject {
	b, known := toolBuilders[r.Tool]
	if !known {
		return []Subject{genericCard(r)}
	}
	if b == nil || !r.OK {
		return nil
	}
	return b(t, r)
}

// Known says whether a tool is one of the built-in tools the table decides.
func Known(tool string) bool {
	_, ok := toolBuilders[tool]
	return ok
}

// toolBuilders holds every built-in tool, so a new one cannot pass unnoticed
// (TestEveryBuiltinToolHasACardDecision)
var toolBuilders map[string]toolBuilder

func init() {
	toolBuilders = map[string]toolBuilder{
		"weather":         weatherCards,
		"timer":           timerCards,
		"alarm":           alarmCards,
		"reminder":        reminderCards,
		"do_not_disturb":  dndCards,
		"device_volume":   volumeCards,
		"agent_mode":      modeCards,
		"ha_control":      haControlCards,
		"ha_search":       haSearchCards,
		"ha_inspect":      haInspectCards,
		"music_search":    musicSearchCards,
		"music_favorites": musicSearchCards,
		"music_play":      musicPlayCards,
		// a rating's step says what changed; no extra play card
		"music_feedback":       nil,
		"video_search":         videoSearchCards,
		"video_play":           videoPlayCards,
		"media_control":        mediaControlCards,
		"catalog_item":         catalogItemCards,
		"catalog_alternatives": catalogAlternativesCards,
		"media_switch":         mediaSwitchCards,
		"media_item_action":    mediaItemActionCards,
		"task_start":           taskCards,
		"task_cancel":          taskCards,
		"task_reply":           taskCards,
		"task_inspect":         taskCards,
		"task_list":            taskListCards,
		"subscribe":            subscribeCards,
		"unsubscribe":          subscribeCards,
		"subscriptions":        subscriptionListCards,
		"subscription_inspect": subscriptionUpdatesCards,
		"subscription_reply":   subscriptionReplyCards,
		"t3_threads":           t3ThreadsCards,
		"t3_launch":            t3DoneCards,
		"t3_send":              t3DoneCards,
		"t3_stop":              t3DoneCards,
		// their tools attach their own cards with the provider's whole results
		"web_search": nil,
		"x_search":   nil,
		// what only YouTube answers, as the screen's list (tool_youtube.go)
		"youtube_search": youtubeSearchCards,
		"youtube_browse": youtubeBrowseCards,
		// the catalog's poster tiles, and a play's live card (CatalogFound, CatalogPlay)
		"catalog_search": nil,
		"catalog_play":   nil,
		// the agent's own seeded call of an event turn (agent.updateTool), no tool's
		"subscription_updates": nil,
		// what a screen_state read (the turn's record of it); the screen itself shows what navigate did
		"screen_state":    screenStateCards,
		"screen_navigate": nil,
		// the skill's name is the step line; its text stays out of every card
		"load_skill": nil,
	}
}

// toolCard is a card drawn once from a call's result; it never changes.
type toolCard struct {
	id    string
	build func(r *Render) (block *cardsv1.CardBlock, fallback string)
}

func (c *toolCard) ID() string { return c.id }

func (c *toolCard) Render(_ context.Context, r *Render) (*cardsv1.Card, error) {
	block, fallback := c.build(r)
	return pcards.NewCard("", 0, clip(fallback, 280), block), nil
}

// card is one call's card, its id hashed from the call's key
func card(prefix string, r ToolResult, build func(r *Render) (*cardsv1.CardBlock, string)) []Subject {
	return []Subject{&toolCard{id: OpaqueID(prefix, r.Key), build: build}}
}

// decode reads a call's output, false when it is not what the tool writes
func decode(r ToolResult, into any) bool {
	return len(r.Data) > 0 && json.Unmarshal(r.Data, into) == nil
}

// done is a card of one thing a call did: a title, a state word and a line
func done(_ *Render, title, icon, state string, kind cardsv1.StateKind, line string) *cardsv1.CardBlock {
	b := &cardsv1.CardBlock{Title: clip(title, 80), Icon: icon, Tone: cardsv1.Tone_TONE_NEUTRAL}
	if state != "" {
		b.Children = append(b.Children, pcards.N(&cardsv1.State{Label: clip(state, 40), Kind: kind}))
	}
	if line = clip(line, 280); line != "" {
		b.Children = append(b.Children, pcards.N(&cardsv1.Text{Text: line, Style: cardsv1.TextStyle_TEXT_STYLE_BODY}))
	}
	return b
}

// rows is a key-value block of at most 12 rows, nil without any
func rows(items [][2]string) *cardsv1.KeyValue {
	kv := &cardsv1.KeyValue{}
	for _, it := range items {
		k, v := clip(it[0], 40), clip(it[1], 80)
		if k == "" || v == "" {
			continue
		}
		kv.Items = append(kv.Items, &cardsv1.KeyValueItem{Key: k, Value: v})
		if len(kv.Items) == 12 {
			break
		}
	}
	if len(kv.Items) == 0 {
		return nil
	}
	return kv
}

// listed is a column of up to n entries, each a title line and a meta line
func listed(entries [][2]string, n int) *cardsv1.Column {
	col := &cardsv1.Column{Gap: cardsv1.Gap_GAP_M}
	for _, e := range entries {
		title := clip(e[0], 280)
		if title == "" {
			continue
		}
		item := &cardsv1.Column{Gap: cardsv1.Gap_GAP_S, Children: pcards.Nodes(
			&cardsv1.Text{Text: title, Style: cardsv1.TextStyle_TEXT_STYLE_BODY})}
		if meta := clip(e[1], 280); meta != "" {
			item.Children = append(item.Children, pcards.N(&cardsv1.Text{Text: meta, Style: cardsv1.TextStyle_TEXT_STYLE_META}))
		}
		col.Children = append(col.Children, pcards.N(item))
		if len(col.Children) == n {
			break
		}
	}
	return col
}

// joined is the non-empty parts with sep between
func joined(sep string, parts ...string) string {
	out := ""
	for _, p := range parts {
		if p == "" {
			continue
		}
		if out != "" {
			out += sep
		}
		out += p
	}
	return out
}
