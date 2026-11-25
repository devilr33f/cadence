package remote_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	cardsv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/cards/v1"
	"git.shino.boo/devilreef/cadence/pkg/rpc"
	"git.shino.boo/devilreef/cadence/server/internal/remote"
)

// outline is a card's node tree on one line: what each node is and says, the ids of its actions left out
func outline(n *cardsv1.Node) string {
	kids := func(ns []*cardsv1.Node) string {
		out := make([]string, len(ns))
		for i, c := range ns {
			out[i] = outline(c)
		}
		return "[" + strings.Join(out, ", ") + "]"
	}
	switch k := n.GetKind().(type) {
	case *cardsv1.Node_Card:
		acts := make([]*cardsv1.Node, len(k.Card.GetActions()))
		for i, b := range k.Card.GetActions() {
			acts[i] = &cardsv1.Node{Kind: &cardsv1.Node_Button{Button: b}}
		}
		return fmt.Sprintf("card «%s» «%s» %s actions%s", k.Card.GetTitle(), k.Card.GetSubtitle(), kids(k.Card.GetChildren()), kids(acts))
	case *cardsv1.Node_Row:
		return "row" + kids(k.Row.GetChildren())
	case *cardsv1.Node_Column:
		return "column" + kids(k.Column.GetChildren())
	case *cardsv1.Node_Image:
		return "image " + strings.TrimPrefix(k.Image.GetAspect().String(), "ASPECT_")
	case *cardsv1.Node_Text:
		return strings.TrimPrefix(k.Text.GetStyle().String(), "TEXT_STYLE_") + " " + k.Text.GetText()
	case *cardsv1.Node_Button:
		return "button " + k.Button.GetIcon()
	case *cardsv1.Node_Slider:
		s := k.Slider
		out := fmt.Sprintf("slider %s %v/%v step %v", s.GetLabel(), s.GetValue(), s.GetMax(), s.GetStep())
		if tl := s.GetTimeline(); tl != nil {
			out += fmt.Sprintf(" timeline %d rate %v", tl.GetAnchorAtMs(), tl.GetRate())
		}
		if s.GetAction() == nil {
			out += " read-only"
		}
		return out
	}
	return "?"
}

func stamped(ms int64) string { return time.UnixMilli(ms).UTC().Format(time.RFC3339Nano) }

// the household of the layout tests: a track with its album that seeks in the kitchen, a paused video episode that
// does not in the living room, a radio stream in the hall's group
func layoutRig(t *testing.T) *rig {
	r := newRig(t)
	r.w.mu.Lock()
	defer r.w.mu.Unlock()
	r.w.features = []string{"card", "slider", "slider.timeline", "text"}
	k := playing("Song", "Artist", "pause", "resume", "next", "seek")
	k.Metadata.Album = "Album"
	r.w.sess["kitchen"] = *k
	ep := playing("Film", "Channel", "pause", "resume")
	ep.Source, ep.State, ep.Volume = "video", "paused", nil
	ep.Metadata.Kind, ep.Metadata.Series, ep.Metadata.Season, ep.Metadata.Episode = "episode", "Сериал", 1, "5"
	ep.Metadata.EpisodeName, ep.Metadata.Provider, ep.Metadata.ProviderSource = "Пилот", "TorrServer", "anime365"
	r.w.sess["lr"] = *ep
	radio := playing("Radio One", "", "pause")
	radio.Source, radio.DurationMs, radio.Metadata.ArtworkURL = "stream", 0, ""
	r.w.sess["a"] = *radio
	return r
}

func TestDevicesPanelLayout(t *testing.T) {
	r := layoutRig(t)
	p := r.call("p1", rpc.DevicesPanel, nil, false).GetPage()
	want := map[string]string{
		"remote-kitchen": "card «Кухня» «Кухня · играет» [row[image SQUARE, column[TITLE Song, BODY Artist, META Album, " +
			"META Spotify Connect]], slider Позиция 10/200 step 10 timeline 1800000000000 rate 1, row[button pause, " +
			"button next], slider Кухня 35/100 step 5] actions[button share_to_speaker]",
		"remote-lr": "card «Колонка» «Гостиная · пауза» [row[image SQUARE, column[TITLE Film, BODY Сериал · S1 · E5 · Пилот, " +
			"META TorrServer · anime365]], slider Позиция 10/200 step 10 timeline 1800000000000 rate 0 read-only, " +
			"row[button play]] actions[button share_to_speaker]",
		"remote-g1": "card «Зал» «Зал · играет» [row[column[TITLE Radio One, META Радио]], META В эфире, row[button pause], " +
			"slider Зал А 35/100 step 5, slider Зал Б 60/100 step 5] actions[button share_to_speaker]",
	}
	for id, w := range want {
		if got := outline(cardOf(t, p, id).GetRoot()); got != w {
			t.Errorf("%s\n got %s\nwant %s", id, got, w)
		}
	}
	// devices.media names the album too
	if items := list(t, r.call("m1", rpc.DevicesMedia, nil, false).GetJson()); items[2]["id"] != "kitchen" || items[2]["album"] != "Album" {
		t.Fatalf("items %v", items)
	}
	// a playing session the device never stamped runs from now; the step follows the length
	for _, c := range []struct {
		durMs uint64
		step  float64
	}{{90_000, 5}, {119_999, 5}, {120_000, 10}, {3_600_000, 10}, {3_600_001, 30}, {3_000, 3}} {
		k := playing("Song", "Artist", "seek")
		k.DurationMs, k.PositionMs, k.PositionAt = c.durMs, 1_000, ""
		r.w.set("kitchen", k)
		sl := parts(cardOf(t, r.call(fmt.Sprintf("p-%d", c.durMs), rpc.DevicesPanel, nil, false).GetPage(), "remote-kitchen")).sliders[0]
		if sl.GetStep() != c.step || sl.GetTimeline().GetAnchorAtMs() != 1_800_000_100_000 || sl.GetValue() != 1 {
			t.Fatalf("%d ms: %v", c.durMs, sl)
		}
	}
}

// the position slider seeks the leader's session to its value, in ms; once the session no longer lists seek, the
// press is refused and nothing is sent
func TestDevicesPanelSeek(t *testing.T) {
	r := layoutRig(t)
	k := cardOf(t, r.call("p1", rpc.DevicesPanel, nil, false).GetPage(), "remote-kitchen")
	pos := parts(k).sliders[0]
	res := r.act("a1", k, pos.GetAction(), 42.5).GetCardAction()
	if res.GetRefusal() != nil || res.GetCard() == nil {
		t.Fatalf("seek %+v", res)
	}
	if cmds, _ := r.w.done(); strings.Join(cmds, "|") != `kitchen media.player seek {"positionMs":42500}` {
		t.Fatalf("commands %q", cmds)
	}
	// past the slider's max: the value check refuses it before the binding
	k = cardOf(t, r.call("p2", rpc.DevicesPanel, nil, false).GetPage(), "remote-kitchen")
	if res := r.act("a2", k, parts(k).sliders[0].GetAction(), 201).GetCardAction(); res.GetRefusal() == nil {
		t.Fatalf("past the end %+v", res)
	}
	k = cardOf(t, r.call("p3", rpc.DevicesPanel, nil, false).GetPage(), "remote-kitchen")
	r.w.set("kitchen", playing("Song", "Artist", "pause"))
	res = r.act("a3", k, parts(k).sliders[0].GetAction(), 30).GetCardAction()
	if res.GetRefusal().GetReason() != "unsupported" {
		t.Fatalf("seek not listed %+v", res)
	}
	if cmds, _ := r.w.done(); len(cmds) != 1 {
		t.Fatalf("sent %q", cmds)
	}
	// redrawn read-only
	if sl := parts(res.GetRefusal().GetCard()).sliders[0]; sl.GetTimeline() == nil || sl.GetAction() != nil {
		t.Fatalf("redrawn %v", sl)
	}
}

// a screen whose hello did not list slider.timeline gets the cards without a position, never a fallback
func TestDevicesPanelTimelineNeedsTheFeature(t *testing.T) {
	for _, features := range [][]string{nil, {"card", "slider", "text"}} {
		r := layoutRig(t)
		r.w.mu.Lock()
		r.w.features = features
		r.w.mu.Unlock()
		p := r.call("p1", rpc.DevicesPanel, nil, false).GetPage()
		if len(p.GetCards()) != 3 {
			t.Fatalf("%v: %v", features, p)
		}
		for _, c := range p.GetCards() {
			for _, s := range parts(c).sliders {
				if s.GetTimeline() != nil {
					t.Fatalf("%v: a timeline in %s", features, c.GetId())
				}
			}
			if strings.Contains(strings.Join(c.GetFeatures(), ","), "slider.timeline") {
				t.Fatalf("%v: %v", features, c.GetFeatures())
			}
		}
		if got := outline(cardOf(t, p, "remote-kitchen").GetRoot()); !strings.Contains(got, "]], row[button pause") {
			t.Fatalf("%v: %s", features, got)
		}
	}
}

// the volume sliders give way to the seek: 3 transport buttons, the join and the seek leave 7 of 9 members' sliders
func TestDevicesPanelFocusLimit(t *testing.T) {
	r := layoutRig(t)
	r.w.mu.Lock()
	s := playing("Party", "DJ", "pause", "resume", "next", "previous", "seek")
	r.w.sess["a"] = *s
	for i := range 7 {
		id := fmt.Sprintf("m%d", i)
		r.w.devs = append(r.w.devs, remote.Device{ID: id, Name: "Колонка " + id, Room: "Зал"})
		r.w.grps[0].Members = append(r.w.grps[0].Members, id)
		r.w.levels[id] = 10 + i
	}
	r.w.mu.Unlock()
	g := cardOf(t, r.call("p1", rpc.DevicesPanel, nil, false).GetPage(), "remote-g1")
	sl := parts(g).sliders
	if len(sl) != 8 || sl[0].GetTimeline() == nil || sl[0].GetAction() == nil {
		t.Fatalf("sliders %v", sl)
	}
}

// the panel follows a seek, a pause and a resume with a new revision, never a position playing on
func TestDevicesPanelRevisesOnJumpsOnly(t *testing.T) {
	r := layoutRig(t)
	p := r.call("p1", rpc.DevicesPanel, nil, true).GetPage()
	rev := cardOf(t, p, "remote-kitchen").GetRevision()
	k := playing("Song", "Artist", "pause", "resume", "next", "seek")
	k.Metadata.Album = "Album"
	k.PositionMs, k.PositionAt = 15_000, stamped(1_800_000_005_000)
	r.w.set("kitchen", k)
	r.changed()
	time.Sleep(600 * time.Millisecond)
	if n := len(r.events("p1")); n != 0 {
		t.Fatalf("%d pages for a position playing on", n)
	}
	steps := []func(){
		func() { k.PositionMs, k.PositionAt = 90_000, stamped(1_800_000_006_000) },                    // a seek
		func() { k.State, k.PositionMs, k.PositionAt = "paused", 91_000, stamped(1_800_000_007_000) }, // a pause
		func() { k.State, k.PositionAt = "playing", stamped(1_800_000_060_000) },                      // a resume
	}
	for i, step := range steps {
		step()
		r.w.set("kitchen", k)
		r.changed()
		r.waitFor(fmt.Sprintf("page %d", i+1), func() bool { return len(r.events("p1")) == i+1 })
		c := cardOf(t, r.events("p1")[i].GetPage(), "remote-kitchen")
		if c.GetRevision() <= rev {
			t.Fatalf("step %d: revision %d after %d", i, c.GetRevision(), rev)
		}
		rev = c.GetRevision()
		tl := parts(c).sliders[0]
		if tl.GetTimeline().GetAnchorAtMs() != mustParse(t, k.PositionAt).UnixMilli() || tl.GetValue() != float64(k.PositionMs)/1000 {
			t.Fatalf("step %d: %v", i, tl)
		}
	}
}

func mustParse(t *testing.T, s string) time.Time {
	t.Helper()
	at, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t.Fatal(err)
	}
	return at
}

// rule 8 on the new lines: no album, series, episode or provider in a log, the seek's included
func TestDevicesPanelLogsNoNewMetadata(t *testing.T) {
	r := layoutRig(t)
	k := cardOf(t, r.call("p1", rpc.DevicesPanel, nil, false).GetPage(), "remote-kitchen")
	r.act("a1", k, parts(k).sliders[0].GetAction(), 20)
	logs := r.logs.String()
	if !strings.Contains(logs, "op=seek") {
		t.Fatalf("no seek line: %s", logs)
	}
	for _, secret := range []string{"Album", "Сериал", "Пилот", "TorrServer", "anime365", "Radio One", "Song", "Film"} {
		if strings.Contains(logs, secret) {
			t.Fatalf("%q logged: %s", secret, logs)
		}
	}
}
