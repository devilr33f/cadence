package tools_test

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/agent/tools"
	"git.shino.boo/devilreef/cadence/server/internal/catalog"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/providers/ytdlp"
	"git.shino.boo/devilreef/cadence/server/internal/settings"
)

// history is a device's stored conversation as the store hands it back: each call beside its result's envelope
type history struct {
	msgs []providers.Message
	err  error
}

func (h *history) fn(context.Context, string) ([]providers.Message, error) { return h.msgs, h.err }

// record stores a successful call of tool with its output, as the runner commits it
func (h *history) record(tool, args string, out json.RawMessage) {
	id := "call-" + strconv.Itoa(len(h.msgs))
	h.msgs = append(h.msgs,
		providers.Message{Role: "assistant", ToolCalls: []providers.ToolCall{{ID: id, Name: tool, Arguments: json.RawMessage(args)}}},
		providers.Message{Role: "tool", ToolCallID: id, Content: `{"ok":true,"data":` + string(out) + `}`})
}

func (f *musicFake) toolsWith(h tools.HistoryFunc) (search, play agent.Tool) {
	ts := tools.Music(tools.MusicDeps{
		Settings: func(context.Context, string) (settings.Values, error) { return f.values, nil },
		Music: func(_ context.Context, name string) (providers.Music, error) {
			if name != "ytm" && name != "sp" {
				return nil, errors.New("no such instance")
			}
			return fakeMusic{f, name}, nil
		},
		Player: f, History: h,
	})
	return ts[0], ts[1]
}

// «Да включи 1» after a redeploy: the search's ids were in the old process's memory alone. The new one finds the
// search in the stored conversation, runs it again and plays the same song, and its own ids start past the old ones
func TestMusicResultsSurviveARestart(t *testing.T) {
	f := newMusicFake()
	h := &history{}
	search, _ := f.toolsWith(h.fn)
	args := `{"query":"Linkin Park Numb"}`
	out, e := call(t, search, "kitchen", args)
	if e != nil {
		t.Fatal(e)
	}
	h.record("music_search", args, out)
	if got := searchItems(t, out); len(got) != 3 || got[1].ID != "r2" || got[1].Provider != "Spotify" {
		t.Fatalf("first search %+v", got)
	}

	// the restart: new results, the same history; the provider now answers in another order
	f.found["ytm"] = []providers.Track{f.found["ytm"][1], f.found["ytm"][0]}
	search, play := f.toolsWith(h.fn)
	if _, e := call(t, play, "kitchen", `{"id":"r2"}`); e != nil {
		t.Fatalf("r2 after the restart: %+v", e)
	}
	if _, e := call(t, play, "kitchen", `{"id":"r1"}`); e != nil {
		t.Fatalf("r1 after the restart: %+v", e)
	}
	// r2 was Spotify's Numb, r1 YouTube Music's, wherever the search put them now
	if len(f.plays) != 2 || !strings.HasPrefix(f.plays[0], "kitchen play 2nLt ") || !strings.HasPrefix(f.plays[1], "kitchen play 5qZQEq_C3vc ") {
		t.Fatalf("plays %v", f.plays)
	}
	out, e = call(t, search, "kitchen", `{"query":"другое"}`)
	if e != nil {
		t.Fatal(e)
	}
	for _, it := range searchItems(t, out) {
		if n, _ := strconv.Atoi(strings.TrimPrefix(it.ID, "r")); n <= 3 {
			t.Fatalf("an id after the restart names an old result: %+v", it)
		}
	}
}

// an id the history cannot find again, or whose song the search no longer finds, tells the model to search again
// now, never to give up
func TestAnUnknownMusicIDAsksForASearchNow(t *testing.T) {
	f := newMusicFake()
	h := &history{}
	search, _ := f.toolsWith(h.fn)
	args := `{"query":"Linkin Park Numb"}`
	out, _ := call(t, search, "kitchen", args)
	h.record("music_search", args, out)
	_, play := f.toolsWith(h.fn)
	if _, e := call(t, play, "kitchen", `{"id":"r9"}`); e == nil || e.Code != "unknown_result" || !strings.Contains(e.Hint, "music_search again") {
		t.Fatalf("never found: %+v", e)
	}
	delete(f.found, "sp")
	if _, e := call(t, play, "kitchen", `{"id":"r2"}`); e == nil || e.Code != "unknown_result" {
		t.Fatalf("gone from the provider: %+v", e)
	}
	h.err = errors.New("db down")
	if _, e := call(t, play, "kitchen", `{"id":"r1"}`); e == nil || e.Code != "unknown_result" {
		t.Fatalf("no history: %+v", e)
	}
	if len(f.plays) != 0 {
		t.Fatalf("played %v", f.plays)
	}
}

func TestVideoResultsSurviveARestart(t *testing.T) {
	f := newVideoFake()
	h := &history{}
	tools2 := func() (agent.Tool, agent.Tool) {
		ts := tools.Video(tools.VideoDeps{
			Settings: func(context.Context, string) (settings.Values, error) { return f.values, nil },
			Player:   func(context.Context, string) (providers.Player, error) { return f, nil },
			Videos:   f, Now: func() time.Time { return f.now }, History: h.fn,
		})
		return ts[0], ts[1]
	}
	search, _ := tools2()
	args := `{"query":"котики"}`
	out, e := call(t, search, "tv", args)
	if e != nil {
		t.Fatal(e)
	}
	h.record("video_search", args, out)
	f.found = []ytdlp.Video{f.found[1], f.found[0]}
	_, play := tools2()
	if _, e := call(t, play, "tv", `{"id":"v2"}`); e != nil {
		t.Fatalf("v2 after the restart: %+v", e)
	}
	if len(f.played) != 1 || f.played[0] != "tv:dQw4w9WgXcQ" {
		t.Fatalf("played %v", f.played)
	}
}

// a catalog ref lives in the ref table's memory (D11): after a restart the stored search finds the title again
func TestCatalogRefsSurviveARestart(t *testing.T) {
	h := &history{}
	before := newScreenFake()
	d := before.deps()
	d.History = h.fn
	args := `{"query":"фрирен"}`
	out, e := call(t, screenTool(d, "catalog_search"), "tv", args)
	if e != nil {
		t.Fatal(e)
	}
	h.record("catalog_search", args, out)
	var got struct{ Items []struct{ Ref, Title string } }
	if json.Unmarshal(out, &got) != nil || len(got.Items) == 0 {
		t.Fatalf("search %s", out)
	}

	after := newScreenFake()
	d = after.deps()
	if _, e := call(t, screenTool(d, "catalog_item"), "tv", `{"ref":"`+got.Items[0].Ref+`"}`); e == nil || e.Code != "unknown_ref" {
		t.Fatalf("without the history: %+v", e)
	}
	shown := 0
	d.History = h.fn
	d.Found = func(agent.Call, []catalog.Item) { shown++ }
	if _, e := call(t, screenTool(d, "catalog_item"), "tv", `{"ref":"`+got.Items[0].Ref+`"}`); e != nil {
		t.Fatalf("after the restart: %+v", e)
	}
	if shown != 0 {
		t.Fatal("the search run again showed its posters")
	}
}
