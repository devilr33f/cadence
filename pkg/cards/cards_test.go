package cards

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	cardsv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/cards/v1"
)

func files(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("testdata", dir, "*.json"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no testdata in %s: %v", dir, err)
	}
	out := map[string][]byte{}
	for _, p := range paths {
		b, err := os.ReadFile(p) //nolint:gosec // fixed testdata path
		if err != nil {
			t.Fatal(err)
		}
		out[strings.TrimSuffix(filepath.Base(p), ".json")] = b
	}
	return out
}

func reason(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return string(e.Code) + " at " + e.Path + ": " + e.Reason
	}
	return err.Error()
}

func example(t *testing.T, name string) *cardsv1.Card {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "cards", name+".json")) //nolint:gosec // fixed testdata path
	if err != nil {
		t.Fatal(err)
	}
	c := &cardsv1.Card{}
	if err := protojson.Unmarshal(raw, c); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestExampleCardsValidateAndRoundTrip(t *testing.T) {
	for name, raw := range files(t, "cards") {
		t.Run(name, func(t *testing.T) {
			c := &cardsv1.Card{}
			if err := protojson.Unmarshal(raw, c); err != nil {
				t.Fatal(err)
			}
			if err := ValidateCard(c); err != nil {
				t.Fatalf("refused: %s", reason(err))
			}
			b, err := EncodeCard(proto.Clone(c).(*cardsv1.Card))
			if err != nil {
				t.Fatalf("encode refused: %s", reason(err))
			}
			back, err := DecodeCard(b)
			if err != nil {
				t.Fatalf("decode refused: %s", reason(err))
			}
			if !proto.Equal(back, c) {
				t.Fatal("round trip differs")
			}
		})
	}
}

func TestExamplePagesValidateAndRoundTrip(t *testing.T) {
	for name, raw := range files(t, "pages") {
		t.Run(name, func(t *testing.T) {
			p := &cardsv1.Page{}
			if err := protojson.Unmarshal(raw, p); err != nil {
				t.Fatal(err)
			}
			b, err := EncodePage(proto.Clone(p).(*cardsv1.Page))
			if err != nil {
				t.Fatalf("refused: %s", reason(err))
			}
			back, err := DecodePage(b)
			if err != nil {
				t.Fatalf("decode refused: %s", reason(err))
			}
			if !proto.Equal(back, p) {
				t.Fatal("round trip differs")
			}
		})
	}
}

// every primitive and every field feature appears in at least one example
func TestExamplesCoverTheVocabulary(t *testing.T) {
	seen := map[string]bool{}
	for _, raw := range files(t, "cards") {
		c := &cardsv1.Card{}
		if err := protojson.Unmarshal(raw, c); err != nil {
			t.Fatal(err)
		}
		for _, f := range c.GetFeatures() {
			seen[f] = true
		}
	}
	for _, raw := range files(t, "pages") {
		p := &cardsv1.Page{}
		if err := protojson.Unmarshal(raw, p); err != nil {
			t.Fatal(err)
		}
		for _, c := range p.GetCards() {
			for _, f := range c.GetFeatures() {
				seen[f] = true
			}
		}
	}
	for _, f := range append(Primitives(), FieldFeatures...) {
		if !seen[f] {
			t.Errorf("no example uses %s", f)
		}
	}
}

func TestPrimitives(t *testing.T) {
	p := Primitives()
	// the design's 27 primitives, card included, row and column as one
	if p[0] != "card" || len(p)-1 != 27 {
		t.Fatalf("primitives %v", p)
	}
	for _, f := range FieldFeatures {
		if !featurePattern.MatchString(f) {
			t.Fatalf("feature %q", f)
		}
	}
}

// every string, enum, message and repeated field of the vocabulary has a rule,
// so a new field cannot slip in unbounded
func TestEveryFieldHasARule(t *testing.T) {
	fd := cardsv1.File_cadence_cards_v1_cards_proto
	msgs := fd.Messages()
	for i := range msgs.Len() {
		fields := msgs.Get(i).Fields()
		for j := range fields.Len() {
			f := fields.Get(j)
			switch {
			case f.IsList(), f.Kind() == protoreflect.StringKind, f.Kind() == protoreflect.EnumKind,
				f.Kind() == protoreflect.MessageKind:
				if _, ok := rules[f.FullName()]; !ok {
					t.Errorf("no rule for %s", f.FullName())
				}
			}
		}
	}
	for name := range rules {
		d := fd.Messages().ByName(name.Parent().Name())
		if d == nil || d.Fields().ByName(name.Name()) == nil {
			t.Errorf("rule for a field that does not exist: %s", name)
		}
	}
}

func card(children ...any) *cardsv1.Card {
	return NewCard("test.card", 1, "Тест", &cardsv1.CardBlock{Title: "Тест", Children: Nodes(children...)})
}

func refusedWith(t *testing.T, err error, code ErrorCode) {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) || e.Code != code {
		t.Fatalf("want %s, got %v", code, err)
	}
	if e.Reason == "" {
		t.Fatalf("%s carries no reason", code)
	}
	if strings.Contains(err.Error(), e.Reason) {
		t.Fatalf("Error() quotes the reason: %q", err.Error())
	}
}

func body(s string) *cardsv1.Text {
	return &cardsv1.Text{Text: s, Style: cardsv1.TextStyle_TEXT_STYLE_BODY}
}

func TestLimits(t *testing.T) {
	many := func(n int) []*cardsv1.Node {
		out := make([]*cardsv1.Node, n)
		for i := range out {
			out[i] = N(body("x"))
		}
		return out
	}
	buttons := func(n int) []any {
		out := []any{}
		for i := range n {
			out = append(out, &cardsv1.Button{Label: "b", Action: Act("a:" + string(rune('a'+i)))})
		}
		return out
	}
	nest := func(levels int) any {
		var n any = body("deep")
		for range levels - 1 {
			n = &cardsv1.Row{Children: Nodes(n)}
		}
		return n
	}
	opts := func(n int) []*cardsv1.Choice {
		out := []*cardsv1.Choice{}
		for i := range n {
			out = append(out, &cardsv1.Choice{Id: string(rune('a' + i)), Label: "o"})
		}
		return out
	}
	withRoot := func(f func(*cardsv1.Card)) *cardsv1.Card { c := card(); f(c); return c }
	cases := []struct {
		name string
		card *cardsv1.Card
		code ErrorCode
	}{
		{"64 nodes pass", card(&cardsv1.Column{Children: many(62)}), ""},
		{"65 nodes refused", card(&cardsv1.Column{Children: many(62)}, body("x")), CodeNodes},
		{"12 focusables pass", card(buttons(12)...), ""},
		{"13 focusables refused", card(buttons(13)...), CodeFocusables},
		{"choices count each option", card(append(buttons(5), &cardsv1.Choices{Action: Act("a:z"), Multiple: true,
			Options: opts(7)})...), CodeFocusables},
		{"a paged list is one region", card(&cardsv1.PagedList{Items: Nodes(func() []any {
			out := []any{}
			for i := range 20 {
				out = append(out, &cardsv1.Link{Domain: "a.example", Title: "t", Action: Act("a:l" + string(rune('a'+i)))})
			}
			return out
		}()...)}), ""},
		{"depth 4 passes", card(nest(4)), ""},
		{"depth 5 refused", card(nest(5)), CodeDepth},
		{"quote counts as a level", card(&cardsv1.Row{Children: Nodes(&cardsv1.Column{Children: Nodes(&cardsv1.Row{
			Children: Nodes(&cardsv1.Post{Author: "a", Text: "t", Quote: &cardsv1.Post{Author: "b", Text: "q"}})})})}),
			CodeDepth},
		{"quote in quote refused", card(&cardsv1.Post{Author: "a", Text: "t", Quote: &cardsv1.Post{Author: "b", Text: "q",
			Quote: &cardsv1.Post{Author: "c", Text: "r"}}}), CodeQuote},
		{"text of 280 passes", card(body(strings.Repeat("я", 280))), ""},
		{"text over 280 refused", card(body(strings.Repeat("я", 281))), CodeLength},
		{"title over 80 refused", NewCard("t", 1, "f", &cardsv1.CardBlock{Title: strings.Repeat("т", 81)}), CodeLength},
		{"label over 40 refused", card(&cardsv1.Button{Label: strings.Repeat("л", 41), Action: Act("a:1")}), CodeLength},
		{"long text of 8000 passes", card(&cardsv1.LongText{Text: strings.Repeat("a", 8000)}), ""},
		{"long text over 8000 refused", card(&cardsv1.LongText{Text: strings.Repeat("a", 8001)}), CodeLength},
		{"card over 16 KB refused", card(&cardsv1.LongText{Text: strings.Repeat("a", 8000)},
			&cardsv1.LongText{Text: strings.Repeat("b", 8000)}, &cardsv1.LongText{Text: strings.Repeat("c", 500)}), CodeSize},
		{"steps over 12 refused", card(&cardsv1.Steps{Items: slices.Repeat([]*cardsv1.Step{{Label: "s",
			Status: cardsv1.StepStatus_STEP_STATUS_DONE}}, 13)}), CodeLength},
		{"no steps refused", card(&cardsv1.Steps{}), CodeLength},
		{"sparkline over 64 refused", card(&cardsv1.Sparkline{Points: make([]float64, 65)}), CodeLength},
		{"text without style", card(&cardsv1.Text{Text: "x"}), CodeRequired},
		{"button without action", card(&cardsv1.Button{Label: "b"}), CodeRequired},
		{"unknown enum value", card(&cardsv1.Text{Text: "x", Style: 99}), CodeEnum},
		{"image url refused", card(&cardsv1.Image{Ref: "https://example.com/a.png", Alt: "a",
			Aspect: cardsv1.Aspect_ASPECT_SQUARE}), CodePattern},
		{"bad action id", card(&cardsv1.Button{Label: "b", Action: Act("a b")}), CodePattern},
		{"tile without aspect", card(&cardsv1.MediaTile{Image: &cardsv1.ImageRef{Ref: "img:a1"}, Title: "t",
			Action: Act("a:1")}), CodeRequired},
		{"empty node refused", card(&cardsv1.Node{}), CodeEmptyNode},
		{"card below root refused", card(&cardsv1.CardBlock{Title: "inner"}), CodeRoot},
		{"root not a card", withRoot(func(c *cardsv1.Card) { c.Root = N(body("x")) }), CodeRoot},
		{"version 2 refused", withRoot(func(c *cardsv1.Card) { c.Version = 2 }), CodeVersion},
		{"ttl over a day", withRoot(func(c *cardsv1.Card) { c.TtlS = MaxTTLSeconds + 1 }), CodeRange},
		{"no fallback", withRoot(func(c *cardsv1.Card) { c.FallbackText = "" }), CodeRequired},
		{"text in a paged list", card(&cardsv1.PagedList{Items: Nodes(body("x"))}), CodeListItem},
		{"grid of 5 columns", card(&cardsv1.Grid{Columns: 5}), CodeRange},
		{"accent on text", card(&cardsv1.Text{Text: "x", Style: cardsv1.TextStyle_TEXT_STYLE_BODY,
			Tone: cardsv1.Tone_TONE_ACCENT}), CodeTone},
		{"two primaries", withRoot(func(c *cardsv1.Card) {
			c.Root.GetCard().Children = Nodes(&cardsv1.Button{Label: "a", Action: Act("a:1"), Primary: true})
			c.Root.GetCard().Actions = []*cardsv1.Button{{Label: "b", Action: Act("a:2"), Primary: true}}
		}), CodePrimary},
		{"action twice", card(&cardsv1.Button{Label: "a", Action: Act("a:1")},
			&cardsv1.Toggle{Label: "t", Action: Act("a:1")}), CodeDuplicate},
		{"cite twice", card(&cardsv1.Link{Domain: "a", Title: "a", Action: Act("a:1"), Cite: 1},
			&cardsv1.Link{Domain: "b", Title: "b", Action: Act("a:2"), Cite: 1}), CodeDuplicate},
		{"meter bounds unordered", card(&cardsv1.Meter{Value: 1, Min: 5, Max: 5, Label: "m", ValueLabel: "m"}), CodeRange},
		{"slider out of range", card(&cardsv1.Slider{Value: 120, Min: 0, Max: 100, Step: 5, Label: "s",
			Action: Act("a:1")}), CodeRange},
		{"slider without step", card(&cardsv1.Slider{Value: 1, Min: 0, Max: 100, Label: "s", Action: Act("a:1")}),
			CodeRange},
		{"timeline without action passes", card(timeline(30, 200, 1)), ""},
		{"timeline with action passes", card(withAction(timeline(30, 200, 0))), ""},
		{"timeline without anchor", card(func() *cardsv1.Slider {
			s := timeline(30, 200, 1)
			s.Timeline.AnchorAtMs = 0
			return s
		}()), CodeRange},
		{"timeline rate over 4", card(timeline(30, 200, 4.5)), CodeRange},
		{"timeline rate below 0", card(timeline(30, 200, -1)), CodeRange},
		{"timeline rate NaN", card(timeline(30, 200, math.NaN())), CodeRange},
		{"timeline max not above 0", card(func() *cardsv1.Slider {
			s := timeline(-5, 0, 1)
			s.Min = -10
			return s
		}()), CodeRange},
		{"timeline value past max", card(timeline(201, 200, 1)), CodeRange},
		{"slider without action or timeline", card(&cardsv1.Slider{Value: 1, Min: 0, Max: 100, Step: 5, Label: "s"}),
			CodeRequired},
		// a timeline without an action is not focusable: 12 buttons beside it pass, an action makes 13
		{"timeline display only is not focusable", card(append(buttons(12), timeline(1, 200, 1))...), ""},
		{"timeline with action counts", card(append(buttons(12), withAction(timeline(1, 200, 1)))...), CodeFocusables},
		{"choices duplicate", card(&cardsv1.Choices{Action: Act("a:1"), Options: []*cardsv1.Choice{{Id: "a", Label: "A"},
			{Id: "a", Label: "B"}}}), CodeChoices},
		{"multiple max over options", card(&cardsv1.Choices{Action: Act("a:1"), Multiple: true, Max: 3,
			Options: opts(2)}), CodeChoices},
		{"single with a min", card(&cardsv1.Choices{Action: Act("a:1"), Min: 1, Options: opts(1)}), CodeChoices},
		{"progress without amount", card(&cardsv1.Progress{}), CodeRequired},
		{"fraction over 1", card(&cardsv1.Progress{Amount: &cardsv1.Progress_Fraction{Fraction: 1.5}}), CodeRange},
		{"indeterminate passes", card(&cardsv1.Progress{Amount: &cardsv1.Progress_Indeterminate{Indeterminate: true}}), ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := EncodeCard(c.card)
			if c.code == "" {
				if err != nil {
					t.Fatalf("refused: %s", reason(err))
				}
				return
			}
			refusedWith(t, err, c.code)
		})
	}
}

func timeline(value, maxS, rate float64) *cardsv1.Slider {
	return &cardsv1.Slider{Value: value, Min: 0, Max: maxS, Step: 10, Label: "Позиция",
		Timeline: &cardsv1.SliderTimeline{AnchorAtMs: 1_800_000_000_000, Rate: rate}}
}

func withAction(s *cardsv1.Slider) *cardsv1.Slider {
	s.Action = Act("a:seek")
	return s
}

func TestSliderTimelineFeature(t *testing.T) {
	if f := Features(card(timeline(30, 200, 1))); !slices.Contains(f, "slider.timeline") || !slices.Contains(f, "slider") {
		t.Fatalf("features %v", f)
	}
	if f := Features(card(withAction(&cardsv1.Slider{Value: 1, Max: 100, Step: 5, Label: "s"}))); slices.Contains(f, "slider.timeline") {
		t.Fatalf("a plain slider names the timeline: %v", f)
	}
	c := example(t, "remote_playback")
	c.Features = slices.DeleteFunc(slices.Clone(c.Features), func(f string) bool { return f == "slider.timeline" })
	refusedWith(t, ValidateCard(c), CodeFeatures)
}

func TestFeaturesMustMatch(t *testing.T) {
	c := example(t, "x_post")
	c.Features = slices.DeleteFunc(slices.Clone(c.Features), func(f string) bool { return f == "post.quote" })
	refusedWith(t, ValidateCard(c), CodeFeatures)
}

func TestUnknownFieldsRefused(t *testing.T) {
	c := card(body("x"))
	b, err := EncodeCard(c)
	if err != nil {
		t.Fatal(reason(err))
	}
	// field 99 of the envelope, as a newer vocabulary might send
	b = append(b, 0x98, 0x06, 0x01)
	_, err = DecodeCard(b)
	refusedWith(t, err, CodeUnknownField)
	// a node whose primitive this vocabulary lacks decodes as an empty node
	c = card(body("x"))
	c.Root.GetCard().Children[0] = &cardsv1.Node{}
	c.Features = Features(c)
	refusedWith(t, ValidateCard(c), CodeEmptyNode)
}

func TestPageLimits(t *testing.T) {
	big := func(id string) *cardsv1.Card {
		c := card(&cardsv1.LongText{Text: strings.Repeat("a", 7500)}, &cardsv1.LongText{Text: strings.Repeat("b", 7500)})
		c.Id = id
		return c
	}
	var cs []*cardsv1.Card
	for i := range 10 {
		cs = append(cs, big("c"+string(rune('a'+i))))
	}
	_, err := EncodePage(&cardsv1.Page{Version: 1, Cards: cs})
	refusedWith(t, err, CodeSize)

	var small []*cardsv1.Card
	for i := range MaxPageCards + 1 {
		c := card()
		c.Id = "c" + string(rune('a'+i))
		small = append(small, c)
	}
	_, err = EncodePage(&cardsv1.Page{Version: 1, Cards: small})
	refusedWith(t, err, CodeLength)
	if _, err := EncodePage(&cardsv1.Page{Version: 1, Cards: small[:MaxPageCards]}); err != nil {
		t.Fatalf("20 cards refused: %s", reason(err))
	}
	_, err = EncodePage(&cardsv1.Page{Version: 1, Cards: []*cardsv1.Card{card(), card()}})
	refusedWith(t, err, CodeDuplicate)
	_, err = EncodePage(&cardsv1.Page{Version: 1, Cards: []*cardsv1.Card{card(&cardsv1.Node{})}})
	refusedWith(t, err, CodeEmptyNode)
	var e *Error
	if errors.As(err, &e); e.Path != "/cards/0/root/card/children/0" {
		t.Fatalf("path %q", e.Path)
	}
	_, err = EncodePage(&cardsv1.Page{Version: 1, Next: Act("bad id")})
	refusedWith(t, err, CodePattern)
}

func TestPathsUseProtojsonNames(t *testing.T) {
	c := card(&cardsv1.Row{Children: Nodes(body("x"), &cardsv1.LongText{Text: strings.Repeat("a", 8001)})})
	_, err := EncodeCard(c)
	var e *Error
	if !errors.As(err, &e) || e.Path != "/root/card/children/0/row/children/1/longText/text" {
		t.Fatalf("got %v", err)
	}
}

func TestDecodeRefusesGarbage(t *testing.T) {
	_, err := DecodeCard([]byte{0xff, 0xff, 0xff})
	refusedWith(t, err, CodeDecode)
	_, err = DecodeCard(make([]byte, MaxCardBytes+1))
	refusedWith(t, err, CodeSize)
}
