// Package follow opens a video on a multiroom group's other screens (multiroom P4, D3): when a group with followVideo
// has a screen leading it (its stream source) that plays a video the server issued (a catalog item, a YouTube video),
// every other online screen of the group gets the same play as a follower's media.video play (follow {groupId}): each
// resolves its own stream, plays it with no sound of its own (the group's stream carries it) and slaves its picture
// to the group's timeline. A follower that leaves the item (its own play, the player closed) is left out of it; the
// item's end, a stop or a leader that no longer plays it stops the followers, unless the leader is moving to its next
// item (Holding), which the followers then get. A video the server cannot issue again (a cast) is skipped with a
// follow_skipped line. Leader maps a follower's session to its leader, so its transport (next, previous) acts on the
// leader's item. Memory only; lines carry ids and codes alone (rule 8).
package follow

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/catalog"
	"git.shino.boo/devilreef/cadence/server/internal/video"
)

// CapVideo is the screens' player, whose plays and stops the fan-out sends and whose state it reads
const CapVideo = "media.video"

const (
	// a follower whose state never named its play this long after it was sent is given up on, for that item
	unseenTTL = 30 * time.Second
	// how often the fan-out looks again with nothing poked
	every = 2 * time.Second
	// one play or stop's bound: the catalog's resolve and the screen's answer
	playTimeout = 90 * time.Second
)

// Group is a multiroom group whose screens follow a video: its stream source and members.
type Group struct {
	ID, Leader string
	Members    []string
}

// Item is the video a screen plays: its session, what it resolves from (a catalog item, or a YouTube video when Video
// is set), and whether it plays now (playing or buffering; a paused one keeps its followers but starts none). Key is
// what the followers follow ("" for the session): a change of it is a new play for them, so a leader's quality
// switch, which makes a new session of the same video, is not one. Skip names why no follower can open the leader's
// session (a cast, a play the server did not issue): it is logged once and followed by none.
type Item struct {
	SessionID string
	Key       string
	Ref       catalog.Ref
	Episode   string
	Choices   catalog.Selection
	Video     *video.Request
	Playing   bool
	Skip      string
}

// key is the item's identity for its followers
func (it Item) key() string {
	if it.Key != "" {
		return it.Key
	}
	return it.SessionID
}

// Video is a screen's media.video state as the fan-out reads it: its session and the group a follower's play follows
// ("" for its own).
type Video struct {
	SessionID, GroupID string
}

type Deps struct {
	// Groups are the groups with followVideo on
	Groups func() []Group
	Online func(deviceID string) bool
	// Screen: the device has a screen that plays media.video
	Screen func(ctx context.Context, deviceID string) bool
	// Item is the device's catalog item now, false for none
	Item func(deviceID string) (Item, bool)
	// Video is the device's media.video state, false for none
	Video func(ctx context.Context, deviceID string) (Video, bool)
	// Play sends the device a follower's play of the item and answers its session id
	Play func(ctx context.Context, deviceID string, it Item, groupID string) (string, error)
	// Stop ends the device's session (media.video stop)
	Stop func(ctx context.Context, deviceID, sessionID string) error
	// Holding is whether the leader is moving to another item of the server's (a next, a jump): its followers are
	// kept while its item is gone, and get the new one once it plays. nil: never
	Holding func(leaderID string) bool
	// Changed hears that a follower's session became known or ended (its server fields follow it); it must not block
	Changed func(deviceID string)
	Log     *slog.Logger
	Now     func() time.Time
}

// Fanout keeps the followers of each group's screen leader.
type Fanout struct {
	d    Deps
	poke chan struct{}
	runs sync.WaitGroup

	mu        sync.Mutex
	followers map[string]*follower
	// the item each device left, which it is not sent again
	left map[string]string
	// the leader session each skip was logged for
	skipped map[string]string
}

// follower is a device sent an item to follow
type follower struct {
	group, leader, key string
	// the session its play answered; "" while the play runs
	session string
	sent    time.Time
	// its state named the play
	seen bool
	busy bool
}

func New(d Deps) *Fanout {
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Holding == nil {
		d.Holding = func(string) bool { return false }
	}
	if d.Changed == nil {
		d.Changed = func(string) {}
	}
	return &Fanout{d: d, poke: make(chan struct{}, 1), followers: map[string]*follower{}, left: map[string]string{},
		skipped: map[string]string{}}
}

// Leader is the leader whose item the device follows in sessionID, false when that is no follower's session of its.
func (f *Fanout) Leader(deviceID, sessionID string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fl := f.followers[deviceID]
	if fl == nil || sessionID == "" || fl.session != sessionID {
		return "", false
	}
	return fl.leader, true
}

// Following is the device's follower session and its leader, false when it follows none now.
func (f *Fanout) Following(deviceID string) (session, leader string, ok bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fl := f.followers[deviceID]
	if fl == nil || fl.session == "" {
		return "", "", false
	}
	return fl.session, fl.leader, true
}

// Followers are the devices that follow the leader's item now.
func (f *Fanout) Followers(leaderID string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for id, fl := range f.followers {
		if fl.leader == leaderID && fl.session != "" {
			out = append(out, id)
		}
	}
	return out
}

// Poke asks for a look on the fan-out's goroutine; it never blocks. A session or a state that changed pokes it.
func (f *Fanout) Poke() {
	select {
	case f.poke <- struct{}{}:
	default:
	}
}

// Run looks at once, on every poke and every couple of seconds, until ctx ends or stop is called.
func (f *Fanout) Run(ctx context.Context) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			f.Reconcile(ctx)
			select {
			case <-ctx.Done():
				return
			case <-f.poke:
			case <-t.C:
			}
		}
	}()
	return func() {
		cancel()
		<-done
		f.runs.Wait()
	}
}

// want is the item a device should follow
type want struct {
	group, leader, key string
	item               Item
}

// Reconcile sends the plays and stops the groups need now; they run on goroutines of their own.
func (f *Fanout) Reconcile(ctx context.Context) {
	wants, held := f.wants(ctx)
	now := f.d.Now()
	var changed []string
	f.mu.Lock()
	defer func() {
		f.mu.Unlock()
		for _, id := range changed {
			f.d.Changed(id)
		}
	}()
	for id, fl := range f.followers {
		if w, ok := wants[id]; ok && w.key == fl.key || fl.busy {
			continue
		}
		if _, ok := wants[id]; !ok && !held[fl.leader] {
			// the item ended, stopped, or its screen no longer leads: the follower stops too
			delete(f.followers, id)
			if fl.session != "" {
				changed = append(changed, id)
				f.stop(ctx, id, fl)
			}
		}
	}
	for id, key := range f.left {
		if w, ok := wants[id]; !ok || w.key != key {
			delete(f.left, id)
		}
	}
	for id, w := range wants {
		if f.left[id] == w.key {
			continue
		}
		fl := f.followers[id]
		if fl != nil && fl.key == w.key {
			if !fl.busy && !f.check(ctx, id, fl, now) {
				changed = append(changed, id)
			}
			continue
		}
		if fl != nil && fl.busy || !w.item.Playing && fl == nil {
			continue
		}
		f.play(ctx, id, w, now)
	}
}

// wants is every follower's item: the other online screens of each group whose screen leader plays a video the
// server issued; held are the leaders moving to their next item
func (f *Fanout) wants(ctx context.Context) (map[string]want, map[string]bool) {
	out, held := map[string]want{}, map[string]bool{}
	for _, g := range f.d.Groups() {
		if g.Leader == "" || !f.d.Online(g.Leader) {
			continue
		}
		it, ok := f.d.Item(g.Leader)
		if ok && it.Skip != "" {
			f.skip(g, it)
			ok = false
		}
		if !ok || it.SessionID == "" || !f.d.Screen(ctx, g.Leader) {
			if f.d.Holding(g.Leader) {
				held[g.Leader] = true
			}
			continue
		}
		key := g.Leader + "/" + it.key()
		for _, m := range g.Members {
			if m == g.Leader || !f.d.Online(m) || !f.d.Screen(ctx, m) {
				continue
			}
			out[m] = want{group: g.ID, leader: g.Leader, key: key, item: it}
		}
	}
	return out, held
}

// skip logs once per leader session that its play is followed by none
func (f *Fanout) skip(g Group, it Item) {
	f.mu.Lock()
	logged := f.skipped[g.Leader] == it.SessionID
	f.skipped[g.Leader] = it.SessionID
	f.mu.Unlock()
	if !logged {
		f.d.Log.Info("follow_skipped", "device_id", g.Leader, "group_id", g.ID, "reason", it.Skip)
	}
}

// check reads a follower's state: one that named the play and names another session now left the item. It answers
// whether the device still follows. callers hold mu
func (f *Fanout) check(ctx context.Context, id string, fl *follower, now time.Time) bool {
	st, ok := f.d.Video(ctx, id)
	switch {
	case ok && st.SessionID == fl.session && st.GroupID == fl.group:
		fl.seen = true
	case fl.seen:
		f.d.Log.Info("follow_left", "device_id", id, "group_id", fl.group)
		f.left[id] = fl.key
		delete(f.followers, id)
		return false
	case now.Sub(fl.sent) > unseenTTL:
		f.d.Log.Warn("follow_failed", "device_id", id, "group_id", fl.group, "code", "unseen")
		f.left[id] = fl.key
		delete(f.followers, id)
		return false
	}
	return true
}

// play sends the device a follower's play; a failed one is not tried again for the item. callers hold mu
func (f *Fanout) play(ctx context.Context, id string, w want, now time.Time) {
	fl := &follower{group: w.group, leader: w.leader, key: w.key, sent: now, busy: true}
	f.followers[id] = fl
	f.runs.Add(1)
	go func() {
		defer f.runs.Done()
		pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), playTimeout)
		defer cancel()
		session, err := f.d.Play(pctx, id, w.item, w.group)
		f.mu.Lock()
		fl.busy = false
		fl.sent = f.d.Now()
		if err != nil || session == "" {
			f.d.Log.Warn("follow_failed", "device_id", id, "group_id", w.group, "code", "play")
			if f.followers[id] == fl {
				delete(f.followers, id)
				f.left[id] = w.key
			}
			f.mu.Unlock()
			return
		}
		fl.session = session
		f.mu.Unlock()
		f.d.Log.Info("follow_play", "device_id", id, "group_id", w.group)
		f.d.Changed(id)
		f.Poke()
	}()
}

// stop ends a follower's session. callers hold mu
func (f *Fanout) stop(ctx context.Context, id string, fl *follower) {
	f.runs.Add(1)
	go func() {
		defer f.runs.Done()
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), playTimeout)
		defer cancel()
		if err := f.d.Stop(sctx, id, fl.session); err != nil {
			f.d.Log.Warn("follow_stop_failed", "device_id", id, "group_id", fl.group)
			return
		}
		f.d.Log.Info("follow_stop", "device_id", id, "group_id", fl.group)
	}()
}

// Settle waits for the plays and stops a Reconcile started (tests).
func (f *Fanout) Settle() { f.runs.Wait() }
