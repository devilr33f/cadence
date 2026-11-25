package media

import (
	"encoding/json"
	"maps"
	"slices"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/humatest"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/server/internal/events"
)

// valid reports whether v, as json, fits the schema.
func valid(t *testing.T, reg huma.Registry, s *huma.Schema, v any) bool {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var doc any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	res := &huma.ValidateResult{}
	huma.Validate(reg, s, huma.NewPathBuffer([]byte{}, 0), huma.ModeReadFromServer, doc, res)
	return len(res.Errors) == 0
}

// the documented reply fits every answer the handler can give: no session,
// and a session in each state, from each source, with or without its optional
// fields; it refuses what the handler never says
func TestMediaReplySchemaFitsEveryReply(t *testing.T) {
	cfg := huma.DefaultConfig("t", "1")
	cfg.CreateHooks = nil
	_, a := humatest.New(t, cfg)
	RegisterAdmin(a, NewStore(events.NewBus(0), nil), nil, nil)
	reg := a.OpenAPI().Components.Schemas
	s := a.OpenAPI().Paths["/devices/{id}/media"].Get.Responses["200"].Content["application/json"].Schema
	replies := []any{none, view(&cadencev2.MediaSessionState{SessionId: "s"})}
	vol, pct := uint32(40), uint32(250)
	for st := range cadencev2.PlaybackState_name {
		for src := range cadencev2.MediaSource_name {
			replies = append(replies, view(&cadencev2.MediaSessionState{
				SessionId: "s", State: cadencev2.PlaybackState(st), Source: cadencev2.MediaSource(src),
				PositionMs: 1, PositionAtMs: 1_794_400_000_000, Rate: 1, DurationMs: 2, Volume: &vol,
				BufferedMs: 3, BufferingPercent: &pct,
				Metadata: &cadencev2.MediaMetadata{Title: "t", Artist: "a", Album: "b", ArtworkUrl: "https://x/y"},
				Actions:  append(append([]string{}, knownActions...), "bogus"),
			}))
		}
	}
	for _, r := range replies {
		if !valid(t, reg, s, r) {
			b, _ := json.Marshal(r)
			t.Errorf("reply %s does not fit the schema", b)
		}
	}
	unknown := view(&cadencev2.MediaSessionState{SessionId: "s"})
	unknown.State = "bogus"
	for _, bad := range []any{
		unknown,
		map[string]any{"state": "playing"},
		map[string]any{"state": "idle", "extra": 1},
		map[string]any{},
	} {
		if valid(t, reg, s, bad) {
			t.Errorf("%v fits the schema", bad)
		}
	}
}

// the art's answer is documented as an image of one of the four types, never
// json, with its type in the content and not as a header
func TestArtworkReplyIsAnImage(t *testing.T) {
	cfg := huma.DefaultConfig("t", "1")
	cfg.CreateHooks = nil
	_, a := humatest.New(t, cfg)
	RegisterAdmin(a, NewStore(events.NewBus(0), nil), nil, nil)
	ok := a.OpenAPI().Paths["/devices/{id}/artwork"].Get.Responses["200"]
	types := slices.Sorted(maps.Keys(ok.Content))
	if !slices.Equal(types, []string{"image/gif", "image/jpeg", "image/png", "image/webp"}) {
		t.Fatalf("content %v", types)
	}
	for typ, c := range ok.Content {
		if c.Schema == nil || c.Schema.Type != huma.TypeString || c.Schema.Format != "binary" {
			t.Errorf("%s: schema %+v", typ, c.Schema)
		}
	}
	if _, typed := ok.Headers["Content-Type"]; typed {
		t.Error("Content-Type documented as a header")
	}
}
