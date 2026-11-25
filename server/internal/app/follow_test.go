package app

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/pkg/rpc"
	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/catalog"
	"git.shino.boo/devilreef/cadence/server/internal/follow"
	"git.shino.boo/devilreef/cadence/server/internal/media"
	"git.shino.boo/devilreef/cadence/server/internal/screenplay"
	"git.shino.boo/devilreef/cadence/server/internal/screens/calls"
)

const followGroupID = "0b6f3a52-6a1e-4d2b-9c51-3c1d2a4e5f60"

// threeEpisodes is a catalog of a series of three
type threeEpisodes struct{ agent.Catalog }

func (threeEpisodes) Item(context.Context, agent.CatalogItem) (catalog.ItemDetail, error) {
	var eps []catalog.Item
	for i := 1; i <= 3; i++ {
		l := fmt.Sprint(i)
		eps = append(eps, catalog.Item{Ref: catalog.Ref("e" + l), Kind: catalog.KindEpisode, Title: "Серия " + l,
			Episode: &catalog.EpisodeInfo{Label: l, Ordinal: float64(i)}})
	}
	return catalog.ItemDetail{Children: eps}, nil
}

// followRig is a group of a leading screen tv1 and a follower tv2 over the real tracker, fan-out and media methods
type followRig struct {
	t       *testing.T
	store   *media.Store
	tracker *screenplay.Tracker
	fanout  *follow.Fanout
	methods map[string]calls.Method

	mu      sync.Mutex
	n       int
	leader  []string // the leader's plays, as episodes
	follows []string // the followers' plays: device/episode
	videos  map[string]follow.Video
}

func (r *followRig) report(dev, session string) {
	r.store.Handle(dev, &cadencev2.Envelope{Body: &cadencev2.Envelope_MediaSessionState{MediaSessionState: &cadencev2.MediaSessionState{
		SessionId: session, Source: cadencev2.MediaSource_MEDIA_SOURCE_VIDEO, State: cadencev2.PlaybackState_PLAYBACK_STATE_PLAYING,
	}}})
	r.tracker.SessionChanged(dev)
}

func newFollowRig(t *testing.T) *followRig {
	r := &followRig{t: t, videos: map[string]follow.Video{}}
	r.store = media.NewStore(nil, slog.New(slog.DiscardHandler))
	r.tracker = screenplay.New(screenplay.Deps{Catalog: threeEpisodes{}, Session: r.store.Raw})
	r.tracker.UsePlay(func(_ context.Context, dev string, ref catalog.Ref, episode string, sel catalog.Selection,
		_ catalog.ResolveFrom, _ int64) (string, string, *agent.ToolError) {
		r.mu.Lock()
		r.n++
		session := fmt.Sprintf("video-L%d", r.n)
		r.leader = append(r.leader, dev+"/"+episode)
		r.mu.Unlock()
		r.tracker.Played(dev, screenplay.Played{SessionID: session, Ref: ref, Episode: episode, Choices: sel})
		// the screen reports its new session a moment later
		r.report(dev, session)
		return episode, session, nil
	})
	d := followDeps{tracker: r.tracker, sessions: r.store}
	r.fanout = follow.New(follow.Deps{
		Groups: func() []follow.Group {
			return []follow.Group{{ID: followGroupID, Leader: "tv1", Members: []string{"tv1", "tv2"}}}
		},
		Online:  func(string) bool { return true },
		Screen:  func(context.Context, string) bool { return true },
		Item:    func(dev string) (follow.Item, bool) { return followItem(d, dev) },
		Holding: r.tracker.Moving,
		Video: func(_ context.Context, dev string) (follow.Video, bool) {
			r.mu.Lock()
			defer r.mu.Unlock()
			v, ok := r.videos[dev]
			return v, ok
		},
		Play: func(_ context.Context, dev string, it follow.Item, g string) (string, error) {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.n++
			session := fmt.Sprintf("video-F%d", r.n)
			r.follows = append(r.follows, dev+"/"+it.Episode)
			r.videos[dev] = follow.Video{SessionID: session, GroupID: g}
			return session, nil
		},
		Stop: func(context.Context, string, string) error { t.Error("a follower was stopped"); return nil },
	})
	r.methods = map[string]calls.Method{}
	mediaMethods{catalog: r.tracker, follow: followRoute(r.fanout, r.store), log: slog.New(slog.DiscardHandler)}.register(
		func(name string, mm calls.Method) { r.methods[name] = mm })
	return r
}

func (r *followRig) step() {
	for range 2 {
		r.fanout.Reconcile(context.Background())
		r.fanout.Settle()
	}
}

func (r *followRig) fields(dev string) *cadencev2.MediaSessionState {
	r.t.Helper()
	get := followFields(r.fanout, r.tracker.Fields)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if st, ok := get(dev); ok && len(st.GetActions()) > 0 {
			return st
		}
		time.Sleep(5 * time.Millisecond)
	}
	r.t.Fatal("no follower fields")
	return nil
}

// a follower screen is offered its leader's next under its own session; its next moves the leader to the next
// episode, which the follower then gets; the leader's own next does the same
func TestAFollowersNextMovesTheGroup(t *testing.T) {
	r := newFollowRig(t)
	r.tracker.Played("tv1", screenplay.Played{SessionID: "video-L0", Ref: "title", Episode: "1"})
	r.report("tv1", "video-L0")
	r.step()
	session, leader, ok := r.fanout.Following("tv2")
	if !ok || leader != "tv1" {
		t.Fatalf("tv2 follows %q of %q, %v", session, leader, ok)
	}
	st := r.fields("tv2")
	if st.GetSessionId() != session || len(st.GetActions()) != 1 || st.GetActions()[0] != "next" {
		t.Fatalf("follower fields %v", st)
	}
	if rep := callOn(r.methods, "tv2", rpc.MediaControl, `{"sessionId":"`+session+`","action":"next"}`); rep.Status != rpc.StatusOK {
		t.Fatalf("a follower's next: %+v", rep)
	}
	r.step()
	r.mu.Lock()
	leaderPlays, follows := append([]string(nil), r.leader...), append([]string(nil), r.follows...)
	r.mu.Unlock()
	if len(leaderPlays) != 1 || leaderPlays[0] != "tv1/2" {
		t.Fatalf("leader plays %v", leaderPlays)
	}
	if len(follows) != 2 || follows[0] != "tv2/1" || follows[1] != "tv2/2" {
		t.Fatalf("follower plays %v", follows)
	}
	// its previous is now offered too, and goes back through the leader
	session, _, _ = r.fanout.Following("tv2")
	if st := r.fields("tv2"); st.GetSessionId() != session || len(st.GetActions()) != 2 {
		t.Fatalf("follower fields after next %v", st)
	}
	if rep := callOn(r.methods, "tv2", rpc.MediaControl, `{"sessionId":"`+session+`","action":"previous"}`); rep.Status != rpc.StatusOK {
		t.Fatalf("a follower's previous: %+v", rep)
	}
	r.step()
	// the leader's own next, once the catalog answered for its episode
	r.fields("tv2")
	cur, _ := r.store.Raw("tv1")
	if rep := callOn(r.methods, "tv1", rpc.MediaControl, `{"sessionId":"`+cur.SessionID+`","action":"next"}`); rep.Status != rpc.StatusOK {
		t.Fatalf("the leader's next: %+v", rep)
	}
	r.step()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.leader) != 3 || r.leader[1] != "tv1/1" || r.leader[2] != "tv1/2" {
		t.Fatalf("leader plays %v", r.leader)
	}
	if len(r.follows) != 4 || r.follows[2] != "tv2/1" || r.follows[3] != "tv2/2" {
		t.Fatalf("follower plays %v", r.follows)
	}
	// a session that is no follower's of tv2 is still not found
	if rep := callOn(r.methods, "tv2", rpc.MediaControl, `{"sessionId":"video-old","action":"next"}`); rep.Status != rpc.StatusNotFound {
		t.Fatalf("another session: %+v", rep)
	}
}

// a follower's panel reads its leader's item
func TestAFollowersPanelsAreItsLeaders(t *testing.T) {
	r := newFollowRig(t)
	r.tracker.Played("tv1", screenplay.Played{SessionID: "video-L0", Ref: "title", Episode: "1"})
	r.report("tv1", "video-L0")
	r.step()
	session, _, _ := r.fanout.Following("tv2")
	ref := followRef(r.tracker.Ref, followRoute(r.fanout, r.store))
	if got, ok := ref("tv2", session); !ok || got != "title" {
		t.Fatalf("follower ref %q, %v", got, ok)
	}
	if _, ok := ref("tv2", "video-other"); ok {
		t.Fatal("another session has a ref")
	}
}
