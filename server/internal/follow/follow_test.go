package follow_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/catalog"
	"git.shino.boo/devilreef/cadence/server/internal/follow"
	"git.shino.boo/devilreef/cadence/server/internal/video"
)

const group = "0b6f3a52-6a1e-4d2b-9c51-3c1d2a4e5f60"

type sent struct {
	device, session, group string
	ref                    catalog.Ref
	episode                string
	video                  string
}

// world is the fan-out's fakes: the group, which devices are online screens, each one's catalog item and media.video
// state, and what was sent
type world struct {
	mu      sync.Mutex
	groups  []follow.Group
	online  map[string]bool
	screens map[string]bool
	items   map[string]follow.Item
	videos  map[string]follow.Video
	plays   []sent
	stops   []sent
	fail    bool
	now     time.Time
	n       int
	holding map[string]bool
	changed []string
}

func newWorld() *world {
	return &world{
		groups:  []follow.Group{{ID: group, Leader: "tv1", Members: []string{"tv1", "tv2", "tv3", "speaker", "tv4"}}},
		online:  map[string]bool{"tv1": true, "tv2": true, "tv3": true, "speaker": true},
		screens: map[string]bool{"tv1": true, "tv2": true, "tv3": true, "tv4": true},
		items:   map[string]follow.Item{},
		videos:  map[string]follow.Video{},
		holding: map[string]bool{},
		now:     time.Unix(1_800_000_000, 0),
	}
}

func (w *world) fanout() *follow.Fanout {
	return follow.New(follow.Deps{
		Groups: func() []follow.Group {
			w.mu.Lock()
			defer w.mu.Unlock()
			return slices.Clone(w.groups)
		},
		Online: func(id string) bool { w.mu.Lock(); defer w.mu.Unlock(); return w.online[id] },
		Screen: func(_ context.Context, id string) bool { w.mu.Lock(); defer w.mu.Unlock(); return w.screens[id] },
		Item: func(id string) (follow.Item, bool) {
			w.mu.Lock()
			defer w.mu.Unlock()
			it, ok := w.items[id]
			return it, ok
		},
		Video: func(_ context.Context, id string) (follow.Video, bool) {
			w.mu.Lock()
			defer w.mu.Unlock()
			v, ok := w.videos[id]
			return v, ok
		},
		Play: func(_ context.Context, id string, it follow.Item, g string) (string, error) {
			w.mu.Lock()
			defer w.mu.Unlock()
			if w.fail {
				return "", errors.New("unplayable")
			}
			w.n++
			s := fmt.Sprintf("video-%d", w.n)
			p := sent{device: id, session: s, group: g, ref: it.Ref, episode: it.Episode}
			if it.Video != nil {
				p.video = it.Video.VideoID
			}
			w.plays = append(w.plays, p)
			// the screen reports its follower's session
			w.videos[id] = follow.Video{SessionID: s, GroupID: g}
			return s, nil
		},
		Stop: func(_ context.Context, id, session string) error {
			w.mu.Lock()
			defer w.mu.Unlock()
			w.stops = append(w.stops, sent{device: id, session: session})
			return nil
		},
		Holding: func(id string) bool { w.mu.Lock(); defer w.mu.Unlock(); return w.holding[id] },
		Changed: func(id string) { w.mu.Lock(); defer w.mu.Unlock(); w.changed = append(w.changed, id) },
		Now:     func() time.Time { w.mu.Lock(); defer w.mu.Unlock(); return w.now },
	})
}

func (w *world) step(f *follow.Fanout) {
	f.Reconcile(context.Background())
	f.Settle()
	// the second look sees the states the plays made
	f.Reconcile(context.Background())
	f.Settle()
}

func (w *world) played() []sent {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.plays)
}

func (w *world) stopped() []sent {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.stops)
}

func (w *world) set(fn func()) {
	w.mu.Lock()
	defer w.mu.Unlock()
	fn()
}

func episode(session, ep string, playing bool) follow.Item {
	return follow.Item{SessionID: session, Ref: "tp:title", Episode: ep, Choices: catalog.Selection{catalog.GroupVoiceover: "v1"},
		Playing: playing}
}

// the other online screens of the group get the leader's item with the group; the leader, a speaker, an offline
// screen and a group without followVideo (not listed) get nothing
func TestFollowersAreTheOtherOnlineScreens(t *testing.T) {
	w := newWorld()
	f := w.fanout()
	w.step(f)
	if p := w.played(); len(p) != 0 {
		t.Fatalf("plays with no item = %v", p)
	}
	// a paused item starts no follower
	w.set(func() { w.items["tv1"] = episode("video-L", "3", false) })
	w.step(f)
	if p := w.played(); len(p) != 0 {
		t.Fatalf("plays of a paused item = %v", p)
	}
	w.set(func() { w.items["tv1"] = episode("video-L", "3", true) })
	w.step(f)
	got := w.played()
	var devices []string
	for _, p := range got {
		devices = append(devices, p.device)
		if p.group != group || p.ref != "tp:title" || p.episode != "3" {
			t.Fatalf("play = %+v", p)
		}
	}
	slices.Sort(devices)
	if !slices.Equal(devices, []string{"tv2", "tv3"}) {
		t.Fatalf("followers = %v", devices)
	}
	// nothing more while they follow, paused or not
	w.set(func() { w.items["tv1"] = episode("video-L", "3", false) })
	w.step(f)
	if len(w.played()) != 2 || len(w.stopped()) != 0 {
		t.Fatalf("plays %v, stops %v", w.played(), w.stopped())
	}
}

// a follower that left the item (its own play) is not sent it again; the next item it follows again
func TestAFollowerThatLeftIsLeftOut(t *testing.T) {
	w := newWorld()
	f := w.fanout()
	w.set(func() { w.items["tv1"] = episode("video-L", "3", true) })
	w.step(f)
	if len(w.played()) != 2 {
		t.Fatalf("plays = %v", w.played())
	}
	w.set(func() { w.videos["tv2"] = follow.Video{SessionID: "video-own"} })
	w.step(f)
	w.step(f)
	if p := w.played(); len(p) != 2 {
		t.Fatalf("a follower that left was sent the item again: %v", p)
	}
	if s := w.stopped(); len(s) != 0 {
		t.Fatalf("a follower that left was stopped: %v", s)
	}
	// the next episode is a new item: both follow it, the old sessions replaced
	w.set(func() { w.items["tv1"] = episode("video-L2", "4", true) })
	w.step(f)
	p := w.played()
	if len(p) != 4 || p[2].episode != "4" || p[3].episode != "4" {
		t.Fatalf("plays = %v", p)
	}
}

// the leader's item ending stops the followers; a leader that is no screen's or no longer leads stops them too
func TestTheItemsEndStopsTheFollowers(t *testing.T) {
	w := newWorld()
	f := w.fanout()
	w.set(func() { w.items["tv1"] = episode("video-L", "3", true) })
	w.step(f)
	sessions := map[string]string{}
	for _, p := range w.played() {
		sessions[p.device] = p.session
	}
	w.set(func() { delete(w.items, "tv1") })
	w.step(f)
	s := w.stopped()
	if len(s) != 2 {
		t.Fatalf("stops = %v", s)
	}
	for _, st := range s {
		if sessions[st.device] != st.session {
			t.Fatalf("stopped %+v, its play was %s", st, sessions[st.device])
		}
	}
	// the leader moved to a speaker: its item is nobody's to follow
	w.set(func() {
		w.items["tv1"] = episode("video-L3", "5", true)
	})
	w.step(f)
	w.set(func() { w.groups[0].Leader = "speaker" })
	w.step(f)
	if s := w.stopped(); len(s) != 4 {
		t.Fatalf("stops after the lead moved = %v", s)
	}
}

// a play the screen refused, or one whose state never names it, is not tried again for the item
func TestAFailedFollowIsNotRetried(t *testing.T) {
	w := newWorld()
	f := w.fanout()
	w.set(func() {
		w.fail = true
		w.items["tv1"] = episode("video-L", "3", true)
	})
	w.step(f)
	w.set(func() { w.fail = false })
	w.step(f)
	if p := w.played(); len(p) != 0 {
		t.Fatalf("a failed follow was retried: %v", p)
	}
	// unseen: the screen never reports the play
	w.set(func() { w.items["tv1"] = episode("video-L2", "4", true) })
	f.Reconcile(context.Background())
	f.Settle()
	w.set(func() {
		w.videos = map[string]follow.Video{}
		w.now = w.now.Add(time.Minute)
	})
	w.step(f)
	w.step(f)
	if p := w.played(); len(p) != 2 {
		t.Fatalf("plays = %v", p)
	}
}

// a YouTube video the leader plays is followed as a catalog item is: each follower gets the video's play (and
// resolves its own stream); a quality switch on the leader, a new session of the same key, sends nothing new
func TestAYouTubeVideoIsFollowed(t *testing.T) {
	w := newWorld()
	f := w.fanout()
	yt := func(session string) follow.Item {
		return follow.Item{SessionID: session, Key: "yt:dQw4w9WgXcQ/video", Video: &video.Request{VideoID: "dQw4w9WgXcQ"},
			Playing: true}
	}
	w.set(func() { w.items["tv1"] = yt("video-L") })
	w.step(f)
	p := w.played()
	if len(p) != 2 {
		t.Fatalf("plays = %v", p)
	}
	for _, s := range p {
		if s.video != "dQw4w9WgXcQ" || s.group != group || s.ref != "" {
			t.Fatalf("play = %+v", s)
		}
	}
	w.set(func() { w.items["tv1"] = yt("video-L2") })
	w.step(f)
	if len(w.played()) != 2 || len(w.stopped()) != 0 {
		t.Fatalf("a quality switch re-sent the video: plays %v, stops %v", w.played(), w.stopped())
	}
}

// a leader's play no follower can open is skipped: no play, and its followers of an earlier item stop
func TestASkippedPlayIsFollowedByNone(t *testing.T) {
	w := newWorld()
	f := w.fanout()
	w.set(func() { w.items["tv1"] = episode("video-L", "3", true) })
	w.step(f)
	w.set(func() { w.items["tv1"] = follow.Item{SessionID: "cast-1", Skip: "cast", Playing: true} })
	w.step(f)
	w.step(f)
	if len(w.played()) != 2 || len(w.stopped()) != 2 {
		t.Fatalf("plays %v, stops %v", w.played(), w.stopped())
	}
}

// a follower's session maps to its leader; while the leader moves to its next item (a next from a follower) the
// followers are kept, then get the new item, and each change of a follower's session is heard
func TestALeaderMovingToTheNextItemKeepsItsFollowers(t *testing.T) {
	w := newWorld()
	f := w.fanout()
	w.set(func() { w.items["tv1"] = episode("video-L", "3", true) })
	w.step(f)
	sessions := map[string]string{}
	for _, p := range w.played() {
		sessions[p.device] = p.session
	}
	if l, ok := f.Leader("tv2", sessions["tv2"]); !ok || l != "tv1" {
		t.Fatalf("leader of tv2 = %q, %v", l, ok)
	}
	if _, ok := f.Leader("tv2", "video-other"); ok {
		t.Fatal("another session of tv2 mapped to the leader")
	}
	if s, l, ok := f.Following("tv3"); !ok || l != "tv1" || s != sessions["tv3"] {
		t.Fatalf("tv3 follows %q of %q, %v", s, l, ok)
	}
	if got := f.Followers("tv1"); len(got) != 2 {
		t.Fatalf("followers = %v", got)
	}
	w.mu.Lock()
	heard := slices.Clone(w.changed)
	w.mu.Unlock()
	slices.Sort(heard)
	if !slices.Equal(heard, []string{"tv2", "tv3"}) {
		t.Fatalf("changed = %v", heard)
	}
	// the next episode is on its way: the old item is gone from the leader, but its followers stay
	w.set(func() {
		delete(w.items, "tv1")
		w.holding["tv1"] = true
	})
	w.step(f)
	if s := w.stopped(); len(s) != 0 {
		t.Fatalf("followers stopped while the leader moved: %v", s)
	}
	w.set(func() {
		w.items["tv1"] = episode("video-L2", "4", true)
		w.holding["tv1"] = false
	})
	w.step(f)
	p := w.played()
	if len(p) != 4 || p[2].episode != "4" || p[3].episode != "4" || len(w.stopped()) != 0 {
		t.Fatalf("plays %v, stops %v", p, w.stopped())
	}
	if _, ok := f.Leader("tv2", sessions["tv2"]); ok {
		t.Fatal("the old session still maps to the leader")
	}
	// a leader that stops for good stops them
	w.set(func() { delete(w.items, "tv1") })
	w.step(f)
	if s := w.stopped(); len(s) != 2 {
		t.Fatalf("stops = %v", s)
	}
	if got := f.Followers("tv1"); len(got) != 0 {
		t.Fatalf("followers after the stop = %v", got)
	}
}
