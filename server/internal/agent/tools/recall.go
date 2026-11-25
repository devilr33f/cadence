package tools

import (
	"context"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"

	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

// HistoryFunc is the device's stored conversation, oldest first: the calls and results of its recent turns
// (conversation.Store.After). The search tools' ids live in memory, so after a restart, or once they expired, the
// history is where a follow-up's id is found again.
type HistoryFunc func(ctx context.Context, deviceID string) ([]providers.Message, error)

// recall is how a play or item tool finds a search result its id no longer names: the search the history shows
// answering it is run again with its own arguments, and the same item, matched by its fields, plays under its new id
type recall struct {
	history HistoryFunc
	// search is the tool whose results carry the ids, list the key of its results, field the id's key in an item
	// and in the play tool's arguments, code the play tool's answer to an unknown id
	search, list, field, code string
	// rerun runs the search again with the arguments the history holds, with no side effect beyond its results'
	// ids (no cards on a screen)
	rerun func(ctx context.Context, c agent.Call) (json.RawMessage, *agent.ToolError)
}

// recalling is t answering an unknown id from rc: the id is found again before t gives up, else t's refusal tells
// the model to search again now rather than stop
func recalling(t agent.Tool, rc recall) agent.Tool {
	run := t.Run
	t.Run = func(ctx context.Context, c agent.Call) (json.RawMessage, *agent.ToolError) {
		out, terr := run(ctx, c)
		if terr == nil || terr.Code != rc.code {
			return out, terr
		}
		if args, ok := rc.find(ctx, c); ok {
			retry := c
			retry.Args = args
			return run(ctx, retry)
		}
		terr.Hint = "That id is from an older search. Call " + rc.search + " again now for what the user asked, then " +
			"use an id from its results in this same turn; never tell the user it cannot be done without trying."
		return nil, terr
	}
	return t
}

// pastSearch is a search call of the history and its results
type pastSearch struct {
	args  json.RawMessage
	items []map[string]any
}

// find answers c's arguments with its lost id replaced by the id the search run again gave the same item
func (rc recall) find(ctx context.Context, c agent.Call) (json.RawMessage, bool) {
	if rc.history == nil || rc.rerun == nil {
		return nil, false
	}
	var args map[string]any
	if json.Unmarshal(c.Args, &args) != nil {
		return nil, false
	}
	lost, _ := args[rc.field].(string)
	if lost == "" {
		return nil, false
	}
	msgs, err := rc.history(ctx, c.DeviceID)
	if err != nil {
		return nil, false
	}
	searches := rc.searches(msgs)
	for i := len(searches) - 1; i >= 0; i-- {
		s := searches[i]
		at := -1
		for j, it := range s.items {
			if it[rc.field] == lost {
				at = j
				break
			}
		}
		if at < 0 {
			continue
		}
		again := c
		again.Args = s.args
		out, terr := rc.rerun(ctx, again)
		if terr != nil {
			return nil, false
		}
		fresh := rc.results(out)
		id, ok := sameItem(s.items[at], at, fresh, rc.field)
		if !ok {
			return nil, false
		}
		args[rc.field] = id
		raw, err := json.Marshal(args)
		return raw, err == nil
	}
	return nil, false
}

// searches are the history's successful calls of the search tool with their results, oldest first
func (rc recall) searches(msgs []providers.Message) []pastSearch {
	calls := map[string]json.RawMessage{}
	var out []pastSearch
	for _, m := range msgs {
		for _, tc := range m.ToolCalls {
			if tc.Name == rc.search {
				calls[tc.ID] = tc.Arguments
			}
		}
		if m.Role != "tool" {
			continue
		}
		args, ok := calls[m.ToolCallID]
		if !ok {
			continue
		}
		if items := rc.results(json.RawMessage(m.Content)); len(items) > 0 {
			out = append(out, pastSearch{args: args, items: items})
		}
	}
	return out
}

// results reads a search's items from its answer: the tool's own output, or the envelope the history stores it in
func (rc recall) results(raw json.RawMessage) []map[string]any {
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if json.Unmarshal(raw, &env) == nil && len(env.Data) > 0 {
		raw = env.Data
	}
	var body map[string][]map[string]any
	if json.Unmarshal(raw, &body) != nil {
		return nil
	}
	return body[rc.list]
}

// matchFields are the fields that tell an item from another: an item found again agrees on each it had
var matchFields = []string{"kind", "provider", "title", "artist", "owner", "album", "channel", "subtitle"}

// sameItem is the id of the fresh item that is old: at old's place first, then anywhere
func sameItem(old map[string]any, at int, fresh []map[string]any, field string) (string, bool) {
	same := func(it map[string]any) bool {
		for _, f := range matchFields {
			if v, ok := old[f].(string); ok && v != "" && !strings.EqualFold(v, str(it[f])) {
				return false
			}
		}
		return str(old["title"]) != ""
	}
	if at < len(fresh) && same(fresh[at]) {
		return str(fresh[at][field]), str(fresh[at][field]) != ""
	}
	for _, it := range fresh {
		if same(it) {
			return str(it[field]), str(it[field]) != ""
		}
	}
	return "", false
}

func str(v any) string { s, _ := v.(string); return s }

// lastID is the highest number of the ids "<prefix><n>" the history's searches answered, so ids made after a
// restart never name an older result the model still reads in its context
func lastID(ctx context.Context, h HistoryFunc, deviceID, search, list, prefix string) int {
	if h == nil {
		return 0
	}
	msgs, err := h(ctx, deviceID)
	if err != nil {
		return 0
	}
	rc := recall{search: search, list: list, field: "id"}
	re := regexp.MustCompile("^" + regexp.QuoteMeta(prefix) + "([0-9]{1,9})$")
	n := 0
	for _, s := range rc.searches(msgs) {
		for _, it := range s.items {
			if m := re.FindStringSubmatch(str(it["id"])); m != nil {
				v, _ := strconv.Atoi(m[1])
				n = max(n, v)
			}
		}
	}
	return n
}
