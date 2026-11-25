package tools_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"git.shino.boo/devilreef/cadence/pkg/rpc"
	"git.shino.boo/devilreef/cadence/server/internal/agent/agenttest"
	"git.shino.boo/devilreef/cadence/server/internal/agent/tools"
	"git.shino.boo/devilreef/cadence/server/internal/catalog"
	"git.shino.boo/devilreef/cadence/server/internal/catalog/catalogtest"
	"git.shino.boo/devilreef/cadence/server/internal/commands"
	"git.shino.boo/devilreef/cadence/server/internal/screens/calls"
)

// a screen's own play is resolved as catalog_play's and sent as media.video/play with the playable's skips, so the
// session is cadenced's; the answer names it
func TestMediaPlayFromTheScreen(t *testing.T) {
	f := newScreenFake()
	ep := f.cat.Ref(t, catalogtest.FrierenEpisode5)
	d := f.deps()
	d.Command = func(_ context.Context, id, capability, name string, args []byte) (json.RawMessage, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.sent = append(f.sent, id+" "+capability+"/"+name+" "+string(args))
		return json.RawMessage(`{"sessionId":"video-7"}`), nil
	}
	m := d.PlayMethod()
	play := func(args string) calls.Reply {
		return m(context.Background(), calls.Call{DeviceID: "tv", ID: "p1", Method: rpc.MediaPlay, Args: json.RawMessage(args)})
	}
	rep := play(`{"ref":"` + ep + `","choices":{"voiceover":"anilibria"},"startMs":61500,"replaces":"video-6","reason":"switch"}`)
	ans, _ := rep.JSON.(tools.MediaPlayAnswer)
	if rep.Status != rpc.StatusOK || ans.SessionID != "video-7" {
		t.Fatalf("play %+v", rep)
	}
	sent := f.commands()
	if len(sent) != 1 || !strings.HasPrefix(sent[0], "tv media.video/play ") || !strings.Contains(sent[0], `"startMs":61500`) ||
		!strings.Contains(sent[0], `"skips":[{"kind":"opening","startMs":90000,"endMs":180000}]`) {
		t.Fatalf("sent %q", sent)
	}
	if res := f.cat.Resolved(); len(res) != 1 || res[0].Selection[catalog.GroupVoiceover] != "anilibria" {
		t.Fatalf("resolved %+v", res)
	}
	for args, status := range map[string]string{
		`{"ref":"c:nope"}`:                                    rpc.StatusExpired,
		`{"ref":"` + ep + `","url":"http://x"}`:               rpc.StatusInvalid,
		`{"ref":"` + ep + `","reason":"model"}`:               rpc.StatusInvalid,
		`{"ref":"` + ep + `","startMs":-1}`:                   rpc.StatusInvalid,
		`{}`:                                                  rpc.StatusInvalid,
		`{"ref":"` + ep + `","choices":{"voiceover":"dead"}}`: rpc.StatusRefused,
	} {
		if got := play(args).Status; got != status {
			t.Errorf("%s: %s, want %s", args, got, status)
		}
	}
}

// replaces is checked against the device's session: a switch keeps its position, an auto-continue starts the next
// episode at its start, a session that something else replaced is not displaced, and the play is heard with its choices
func TestMediaPlayReplaces(t *testing.T) {
	f := newScreenFake()
	ep := f.cat.Ref(t, catalogtest.FrierenEpisode5)
	d := f.deps()
	d.Command = func(_ context.Context, _, _, _ string, args []byte) (json.RawMessage, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.sent = append(f.sent, string(args))
		return json.RawMessage(`{"sessionId":"video-8"}`), nil
	}
	current := "video-7"
	d.Current = func(string) (string, int64, bool) { return current, 754_000, current != "" }
	var replaced []string
	var played []tools.PlayedVideo
	d.Replaced = func(_, id string) { replaced = append(replaced, id) }
	d.Played = func(_ string, p tools.PlayedVideo) { played = append(played, p) }
	play := func(args string) calls.Reply {
		return d.PlayMethod()(context.Background(), calls.Call{DeviceID: "tv", Method: rpc.MediaPlay, Args: json.RawMessage(args)})
	}
	if rep := play(`{"ref":"` + ep + `","choices":{"voiceover":"anilibria"},"replaces":"video-7","reason":"switch"}`); rep.Status != rpc.StatusOK {
		t.Fatalf("switch %+v", rep)
	}
	if sent := f.commands(); len(sent) != 1 || !strings.Contains(sent[0], `"startMs":754000`) {
		t.Fatalf("switch sent %q", sent)
	}
	if len(replaced) != 1 || replaced[0] != "video-7" || len(played) != 1 || played[0].SessionID != "video-8" ||
		played[0].Choices[catalog.GroupVoiceover] != "anilibria" || played[0].Episode == "" {
		t.Fatalf("replaced %v, played %+v", replaced, played)
	}
	if rep := play(`{"ref":"` + ep + `","replaces":"video-7","reason":"continue"}`); rep.Status != rpc.StatusOK {
		t.Fatalf("continue %+v", rep)
	}
	if res := f.cat.Resolved(); res[len(res)-1].From != catalog.FromStart {
		t.Fatalf("continue resolved %+v", res[len(res)-1])
	}
	if rep := play(`{"ref":"` + ep + `","replaces":"video-1","reason":"continue"}`); rep.Status != rpc.StatusExpired {
		t.Fatalf("a superseded continue %+v", rep)
	}
	// the episode ended: nothing plays, the next one may start
	current = ""
	if rep := play(`{"ref":"` + ep + `","replaces":"video-7","reason":"continue"}`); rep.Status != rpc.StatusOK {
		t.Fatalf("continue after the end %+v", rep)
	}
}

// what the screen shows of a play (the osd's title, the channel under it, the provider's badge, the art) comes from
// the item the catalog handed out, and from a fresh read of it when the server no longer holds it (a restart): a
// YouTube video played from the screen's catalog is never nameless
func TestMediaPlayCarriesWhatTheScreenShows(t *testing.T) {
	yt := &catalogtest.Fake{Provider: catalog.ProviderYouTubeMusic, Caps: catalog.Capabilities{catalog.CapItem, catalog.CapResolve}}
	video := yt.Add(catalogtest.Entry{
		Item: catalog.Item{Key: catalog.Key{Source: "video", Kind: catalog.KindVideo, ID: "dQw4w9WgXcQ"},
			Title: "Как устроен звук в кино", Subtitle: "Лекторий", Artwork: catalog.Artwork{Hash: "AbCdEfGhIjKlMnOpQrStUvWx"}},
		Play: &catalog.Playable{Kind: catalog.PlayStream, Stream: &catalog.Stream{URL: "https://cadence.example/v2/stream/t/1"}},
	})
	for _, forget := range []bool{false, true} {
		cat := agenttest.NewCatalogOf(yt)
		if forget {
			cat.Forget()
		}
		ref := cat.Ref(t, video)
		var sent []string
		d := tools.ScreenDeps{Catalog: cat, ImageURL: func(id string) string { return "https://cadence.example/v2/images/" + id },
			Command: func(_ context.Context, _, _, _ string, args []byte) (json.RawMessage, error) {
				sent = append(sent, string(args))
				return json.RawMessage(`{"sessionId":"video-1"}`), nil
			}}
		rep := d.PlayMethod()(context.Background(), calls.Call{DeviceID: "tv", Method: rpc.MediaPlay, Args: json.RawMessage(`{"ref":"` + ref + `"}`)})
		if rep.Status != rpc.StatusOK || len(sent) != 1 {
			t.Fatalf("forget %v: %+v %q", forget, rep, sent)
		}
		var args struct {
			Metadata map[string]string `json:"metadata"`
		}
		if err := json.Unmarshal([]byte(sent[0]), &args); err != nil {
			t.Fatal(err)
		}
		want := map[string]string{
			"title": "Как устроен звук в кино", "channel": "Лекторий", "provider": "youtube", "providerName": "YouTube",
			"artworkUrl": "https://cadence.example/v2/images/AbCdEfGhIjKlMnOpQrStUvWx", "itemRef": ref,
		}
		for k, v := range want {
			if args.Metadata[k] != v {
				t.Errorf("forget %v: metadata %s = %q, want %q", forget, k, args.Metadata[k], v)
			}
		}
		if _, ok := args.Metadata["series"]; ok {
			t.Errorf("forget %v: a video has no series: %v", forget, args.Metadata)
		}
	}
}

// a series' episode reads its show's russian name, the episode label and torrplayer's badge
func TestMediaPlayNamesAnEpisode(t *testing.T) {
	f := newScreenFake()
	series := f.cat.Ref(t, catalogtest.FrierenAniLiberty)
	d := f.deps()
	d.Command = func(_ context.Context, _, _, _ string, args []byte) (json.RawMessage, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.sent = append(f.sent, string(args))
		return json.RawMessage(`{"sessionId":"video-2"}`), nil
	}
	rep := d.PlayMethod()(context.Background(), calls.Call{DeviceID: "tv", Method: rpc.MediaPlay, Args: json.RawMessage(`{"ref":"` + series + `","episode":"5"}`)})
	if rep.Status != rpc.StatusOK {
		t.Fatalf("play %+v", rep)
	}
	sent := f.commands()
	if len(sent) != 1 || !strings.Contains(sent[0], `"title":"Провожающая в последний путь Фрирен"`) || !strings.Contains(sent[0], `"episode":"5"`) ||
		!strings.Contains(sent[0], `"provider":"torrplayer"`) || strings.Contains(sent[0], `"channel"`) {
		t.Fatalf("sent %q", sent)
	}
}

// a cadenced from before the channel and the provider refuses them by its schema: the play goes again with the
// metadata it knows, so an older screen still plays and still shows the title
func TestMediaPlayOnAnOlderScreen(t *testing.T) {
	f := newScreenFake()
	ep := f.cat.Ref(t, catalogtest.FrierenEpisode5)
	d := f.deps()
	d.Command = func(_ context.Context, _, _, _ string, args []byte) (json.RawMessage, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.sent = append(f.sent, string(args))
		if strings.Contains(string(args), `"provider"`) {
			return nil, &commands.InvalidArgumentsError{Path: "/metadata"}
		}
		return json.RawMessage(`{"sessionId":"video-3"}`), nil
	}
	rep := d.PlayMethod()(context.Background(), calls.Call{DeviceID: "tv", Method: rpc.MediaPlay, Args: json.RawMessage(`{"ref":"` + ep + `"}`)})
	if rep.Status != rpc.StatusOK {
		t.Fatalf("play %+v", rep)
	}
	sent := f.commands()
	if len(sent) != 2 || strings.Contains(sent[1], `"provider"`) || !strings.Contains(sent[1], `"title":"Фрирен, серия 5"`) {
		t.Fatalf("sent %q", sent)
	}
}
