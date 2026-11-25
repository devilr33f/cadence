package screenplay_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/catalog"
	"git.shino.boo/devilreef/cadence/server/internal/media"
	"git.shino.boo/devilreef/cadence/server/internal/screenplay"
)

// fakeCatalog answers a series of three episodes with a like and a my-list action
type fakeCatalog struct {
	agent.Catalog
	mu   sync.Mutex
	acts []string
}

func ep(label string, ord float64) catalog.Item {
	return catalog.Item{Ref: catalog.Ref("e" + label), Kind: catalog.KindEpisode, Title: "Серия " + label, DurationS: 1440,
		Episode: &catalog.EpisodeInfo{Label: label, Ordinal: ord}}
}

func (f *fakeCatalog) Item(context.Context, agent.CatalogItem) (catalog.ItemDetail, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return catalog.ItemDetail{
		Children: []catalog.Item{ep("2", 2), ep("1", 1), ep("3", 3)},
		Actions: []catalog.Action{
			{ID: "a1", Kind: catalog.ActionLike, Label: "Like", Enabled: true},
			{ID: "a2", Kind: catalog.ActionMyList, Label: "My list", Enabled: true, Values: []string{"planned"}},
		},
		Panels: []catalog.Panel{{ID: "p1", Kind: catalog.PanelComments, Title: "Comments", CountLabel: "12"}, {ID: "p2", Kind: "chapters"}},
	}, nil
}

func (f *fakeCatalog) Act(_ context.Context, ref catalog.Ref, action string, on bool, value string) ([]catalog.Action, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.acts = append(f.acts, string(ref)+"/"+action+"/"+map[bool]string{true: "on", false: "off"}[on]+"/"+value)
	return []catalog.Action{{ID: "a3", Kind: catalog.ActionLike, Label: "Like", Toggled: true, Enabled: true}}, nil
}

type rig struct {
	t       *testing.T
	tr      *screenplay.Tracker
	cat     *fakeCatalog
	mu      sync.Mutex
	session media.MediaSession
	changed int
	plays   []string
	now     time.Time
}

func newRig(t *testing.T) *rig {
	r := &rig{t: t, cat: &fakeCatalog{}, now: time.Unix(1_800_000_000, 0)}
	r.tr = screenplay.New(screenplay.Deps{
		Catalog: r.cat,
		Session: func(string) (media.MediaSession, bool) {
			r.mu.Lock()
			defer r.mu.Unlock()
			return r.session, r.session.SessionID != ""
		},
		Changed: func(string) { r.mu.Lock(); r.changed++; r.mu.Unlock() },
		Now:     func() time.Time { r.mu.Lock(); defer r.mu.Unlock(); return r.now },
	})
	r.tr.UsePlay(func(_ context.Context, dev string, ref catalog.Ref, episode string, sel catalog.Selection,
		from catalog.ResolveFrom, _ int64) (string, string, *agent.ToolError) {
		r.mu.Lock()
		r.plays = append(r.plays, string(ref)+"/"+episode+"/"+sel[catalog.GroupVoiceover]+"/"+string(from))
		r.mu.Unlock()
		if episode == "fail" {
			return "", "", &agent.ToolError{Code: "unplayable"}
		}
		r.tr.Played(dev, screenplay.Played{SessionID: "v-" + episode, Ref: ref, Episode: episode, Choices: sel})
		return episode, "v-" + episode, nil
	})
	return r
}

func (r *rig) fields(dev string) *cadencev2.MediaSessionState {
	r.t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if st, ok := r.tr.Fields(dev); ok {
			return st
		}
		time.Sleep(5 * time.Millisecond)
	}
	r.t.Fatal("no fields")
	return nil
}

// a played episode's fields: the transport the server serves, the episodes ahead as refs jump takes, its place, the
// item's actions and panels
func TestFieldsAndControl(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	sel := catalog.Selection{catalog.GroupVoiceover: "anilibria"}
	r.tr.Played("tv", screenplay.Played{SessionID: "v-1", Ref: "title", Episode: "1", Choices: sel})
	st := r.fields("tv")
	if st.GetSessionId() != "v-1" || len(st.GetActions()) != 1 || st.GetActions()[0] != "next" || st.GetQueuePosition() != 0 ||
		len(st.GetUpNext()) != 2 || st.GetUpNext()[0].GetRef() != "ce:2" || st.GetUpNext()[0].GetDurationMs() != 1_440_000 {
		t.Fatalf("fields %v", st)
	}
	if len(st.GetItemActions()) != 2 || st.GetItemActions()[1].GetKind() != cadencev2.MediaItemActionKind_MEDIA_ITEM_ACTION_KIND_SAVE ||
		len(st.GetPanels()) != 2 || st.GetPanels()[1].GetKind() != cadencev2.MediaPanelKind_MEDIA_PANEL_KIND_CUSTOM ||
		st.GetState() != cadencev2.PlaybackState_PLAYBACK_STATE_UNSPECIFIED {
		t.Fatalf("fields %v", st)
	}
	if !r.tr.Owns("tv", "v-1") || r.tr.Owns("tv", "other") {
		t.Fatal("owns")
	}
	// next keeps the voiceover and starts the episode from its start
	if te, err := r.tr.Control(ctx, "tv", "v-1", "next", ""); te != nil || err != nil {
		t.Fatalf("next %v %v", te, err)
	}
	if st := r.fields("tv"); st.GetSessionId() != "v-2" || len(st.GetActions()) != 2 {
		t.Fatalf("after next %v", st)
	}
	if te, err := r.tr.Control(ctx, "tv", "v-2", "jump", "ce:1"); te != nil || err != nil {
		t.Fatalf("jump %v %v", te, err)
	}
	if _, err := r.tr.Control(ctx, "tv", "v-1", "previous", ""); !errors.Is(err, screenplay.ErrNoEpisode) {
		t.Fatalf("previous of the first: %v", err)
	}
	if _, err := r.tr.Control(ctx, "tv", "v-1", "jump", "ce:9"); !errors.Is(err, screenplay.ErrUnknownRef) {
		t.Fatalf("jump to none: %v", err)
	}
	if _, err := r.tr.Control(ctx, "tv", "v-2", "next", ""); !errors.Is(err, screenplay.ErrNotOwned) {
		t.Fatalf("a replaced session: %v", err)
	}
	r.mu.Lock()
	plays := r.plays
	r.mu.Unlock()
	if len(plays) != 2 || plays[0] != "title/2/anilibria/start" || plays[1] != "title/1/anilibria/start" {
		t.Fatalf("plays %v", plays)
	}
}

// an item action toggles on the catalog and its new state goes out; a my-list one sets a status
func TestItemAction(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	r.tr.Played("tv", screenplay.Played{SessionID: "v-1", Ref: "title", Episode: "1"})
	r.fields("tv")
	if err := r.tr.ItemAction(ctx, "tv", "v-1", "a2"); err != nil {
		t.Fatal(err)
	}
	if err := r.tr.ItemAction(ctx, "tv", "v-1", "a1"); !errors.Is(err, screenplay.ErrUnknownAction) {
		t.Fatalf("an action id the act replaced: %v", err)
	}
	if err := r.tr.ItemAction(ctx, "tv", "v-1", "a3"); err != nil {
		t.Fatal(err)
	}
	if st := r.fields("tv"); len(st.GetItemActions()) != 1 || !st.GetItemActions()[0].GetToggled() {
		t.Fatalf("after %v", st)
	}
	if got := r.cat.acts; len(got) != 2 || got[0] != "title/a2/on/planned" || got[1] != "title/a3/off/" {
		t.Fatalf("acts %v", got)
	}
	if err := r.tr.ItemAction(ctx, "tv", "other", "a3"); !errors.Is(err, screenplay.ErrNotOwned) {
		t.Fatal(err)
	}
}

// the play's session ends once the device plays another after it was seen (none keeps it: a link that came back);
// one never seen goes after a while
func TestSessionChanges(t *testing.T) {
	r := newRig(t)
	r.tr.Played("tv", screenplay.Played{SessionID: "v-1", Ref: "title", Episode: "1"})
	r.mu.Lock()
	r.session = media.MediaSession{SessionID: "old"}
	r.mu.Unlock()
	r.tr.SessionChanged("tv")
	if !r.tr.Owns("tv", "v-1") {
		t.Fatal("dropped before its session was seen")
	}
	r.mu.Lock()
	r.session = media.MediaSession{SessionID: "v-1"}
	r.mu.Unlock()
	r.tr.SessionChanged("tv")
	r.mu.Lock()
	r.session = media.MediaSession{}
	r.mu.Unlock()
	r.tr.SessionChanged("tv")
	if !r.tr.Owns("tv", "v-1") {
		t.Fatal("dropped with the link")
	}
	r.mu.Lock()
	r.session = media.MediaSession{SessionID: "url-1"}
	r.mu.Unlock()
	r.tr.SessionChanged("tv")
	if r.tr.Owns("tv", "v-1") {
		t.Fatal("kept past another session")
	}
	r.tr.Played("tv", screenplay.Played{SessionID: "v-2", Ref: "title", Episode: "2"})
	r.mu.Lock()
	r.now = r.now.Add(time.Minute)
	r.mu.Unlock()
	r.tr.SessionChanged("tv")
	if r.tr.Owns("tv", "v-2") {
		t.Fatal("an unseen play kept")
	}
}

func TestCurrentIsTheMatchingSessionWithItsTitleGroup(t *testing.T) {
	r := newRig(t)
	r.tr.Played("tv", screenplay.Played{SessionID: "v-1", Ref: "variant", TitleRef: "group", Episode: "7",
		Choices: catalog.Selection{catalog.GroupVoiceover: "AniLibria"}})
	if _, ok := r.tr.Current("tv"); ok {
		t.Fatal("current before the device reported the session")
	}
	r.mu.Lock()
	r.session = media.MediaSession{SessionID: "v-1"}
	r.mu.Unlock()
	cur, ok := r.tr.Current("tv")
	if !ok || cur.Ref != "group" || cur.Episode != "7" || cur.Choices[catalog.GroupVoiceover] != "AniLibria" {
		t.Fatalf("current %+v %v", cur, ok)
	}
	r.mu.Lock()
	r.session = media.MediaSession{SessionID: "url-1"}
	r.mu.Unlock()
	if _, ok := r.tr.Current("tv"); ok {
		t.Fatal("another session exposed the old catalog choices")
	}
}

// a device is moving while its next runs and until it reports the play it accepted; a play never seen ends it
func TestMovingCoversANextUntilItsSessionIsSeen(t *testing.T) {
	r := newRig(t)
	r.tr.Played("tv", screenplay.Played{SessionID: "v-1", Ref: "title", Episode: "1"})
	r.fields("tv")
	r.mu.Lock()
	r.session = media.MediaSession{SessionID: "v-1"}
	r.mu.Unlock()
	r.tr.SessionChanged("tv")
	if r.tr.Moving("tv") {
		t.Fatal("moving with its play seen")
	}
	during := make(chan bool, 1)
	r.tr.UsePlay(func(_ context.Context, dev string, ref catalog.Ref, episode string, sel catalog.Selection,
		_ catalog.ResolveFrom, _ int64) (string, string, *agent.ToolError) {
		during <- r.tr.Moving(dev)
		r.tr.Played(dev, screenplay.Played{SessionID: "v-" + episode, Ref: ref, Episode: episode, Choices: sel})
		return episode, "v-" + episode, nil
	})
	if te, err := r.tr.Control(context.Background(), "tv", "v-1", "next", ""); te != nil || err != nil {
		t.Fatalf("next %v %v", te, err)
	}
	if !<-during || !r.tr.Moving("tv") {
		t.Fatal("not moving while the next runs or before its session is seen")
	}
	r.mu.Lock()
	r.session = media.MediaSession{SessionID: "v-2"}
	r.mu.Unlock()
	r.tr.SessionChanged("tv")
	if r.tr.Moving("tv") {
		t.Fatal("moving once the next's session is seen")
	}
	r.tr.Played("tv", screenplay.Played{SessionID: "v-3", Ref: "title", Episode: "3"})
	r.mu.Lock()
	r.now = r.now.Add(time.Minute)
	r.mu.Unlock()
	if r.tr.Moving("tv") {
		t.Fatal("moving past an unseen play's time")
	}
}
