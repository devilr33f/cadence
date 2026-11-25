// Package screenplay keeps the catalog sessions the server played on screens (catalog_play, media.play, a switch, the
// next episode): their item, episode and resolved choices, and from the catalog the episodes around the playing one,
// the item's actions and its panels. From them it answers the session's server-owned fields (the link's
// media_session_state, server -> device), media.control's next, previous and jump (the next episode keeps the
// voiceover), media.item_action and the ref media.panel reads. Memory-only (D11): refs, titles and choices are never
// logged.
package screenplay

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/catalog"
	"git.shino.boo/devilreef/cadence/server/internal/media"
)

const (
	// upNext is the most episodes a session's fields carry
	upNext = 20
	// a play whose session the device never reported is forgotten after this
	unseenTTL = 30 * time.Second
	// the catalog read behind a session's fields
	readWait = 10 * time.Second
	// episodeRef marks an episode's ref in up next: the label after it
	episodeRef = "ce:"
)

// Played is a catalog play the device accepted.
type Played struct {
	SessionID string
	// Ref is the resolved variant the item read and episode navigation use.
	Ref catalog.Ref
	// TitleRef is the title group a source switch resolves, when the play began at one.
	TitleRef catalog.Ref
	Episode  string
	// Choices are the choices the play resolved (the voiceover, the source), kept for the next episode
	Choices catalog.Selection
}

// PlayFunc plays a ref's episode with choices on the device, as media.play does (the catalog's resolve, then
// media.video/play); from start unless startMS >= 0. It answers the episode label and the new session's id.
type PlayFunc func(ctx context.Context, deviceID string, ref catalog.Ref, episode string, sel catalog.Selection,
	from catalog.ResolveFrom, startMS int64) (string, string, *agent.ToolError)

// Deps are the tracker's collaborators.
type Deps struct {
	Catalog agent.Catalog
	// Session is the device's session as it last reported it (media.Store.Raw)
	Session func(deviceID string) (media.MediaSession, bool)
	// Changed hears that a device's session fields changed; it must not block
	Changed func(deviceID string)
	Log     *slog.Logger
	Now     func() time.Time
}

// Tracker is the catalog sessions, one per device.
type Tracker struct {
	d Deps

	mu       sync.Mutex
	sessions map[string]*entry
	play     PlayFunc
	// moving counts the Control plays running per device
	moving map[string]int
}

type entry struct {
	Played
	at   time.Time
	seen bool
	// what the catalog answered; read is false until it did
	read     bool
	episodes []catalog.Item
	actions  []catalog.Action
	panels   []catalog.Panel
}

func New(d Deps) *Tracker {
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Changed == nil {
		d.Changed = func(string) {}
	}
	return &Tracker{d: d, sessions: map[string]*entry{}, moving: map[string]int{}}
}

// Moving is whether the device is moving to another catalog item of the server's: a next, previous or jump runs, or
// the device has not yet reported the last play it accepted. A multiroom group's followers are kept meanwhile.
func (t *Tracker) Moving(deviceID string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	e := t.sessions[deviceID]
	return t.moving[deviceID] > 0 || e != nil && !e.seen && t.d.Now().Sub(e.at) <= unseenTTL
}

// UsePlay sets how the tracker plays the next episode; set before the links open.
func (t *Tracker) UsePlay(p PlayFunc) { t.play = p }

// Played records a play the device accepted and reads its item from the catalog in the background; the fields go out
// once it answered. ctx bounds nothing past the call.
func (t *Tracker) Played(deviceID string, p Played) {
	if p.SessionID == "" || p.Ref == "" {
		return
	}
	e := &entry{Played: p, at: t.d.Now()}
	t.mu.Lock()
	t.sessions[deviceID] = e
	t.mu.Unlock()
	go t.read(deviceID, e)
}

func (t *Tracker) read(deviceID string, e *entry) {
	if t.d.Catalog == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), readWait)
	defer cancel()
	det, err := t.d.Catalog.Item(ctx, agent.CatalogItem{Ref: e.Ref})
	if err != nil {
		t.d.Log.Info("screenplay_read_failed", "device_id", deviceID)
		return
	}
	var eps []catalog.Item
	for _, c := range det.Children {
		if c.Episode != nil && c.Episode.Label != "" {
			eps = append(eps, c)
		}
	}
	sort.SliceStable(eps, func(i, j int) bool { return eps[i].Episode.Ordinal < eps[j].Episode.Ordinal })
	t.mu.Lock()
	if t.sessions[deviceID] != e {
		t.mu.Unlock()
		return
	}
	e.read, e.episodes, e.actions, e.panels = true, eps, det.Actions, det.Panels
	t.mu.Unlock()
	t.d.Changed(deviceID)
}

// SessionChanged follows the device's session (media.Store's OnChange): a session of another id once the play's own
// was seen ends the play's; one never seen is dropped after a while. No session at all keeps it: a link that drops
// and comes back reports the same session again, and an ended episode's fields go unread.
func (t *Tracker) SessionChanged(deviceID string) {
	sess, ok := t.d.Session(deviceID)
	t.mu.Lock()
	defer t.mu.Unlock()
	e := t.sessions[deviceID]
	switch {
	case e == nil:
	case ok && sess.SessionID == e.SessionID:
		e.seen = true
	case ok && e.seen, !e.seen && t.d.Now().Sub(e.at) > unseenTTL:
		delete(t.sessions, deviceID)
	}
}

// Owns is whether sessionID is the device's catalog session.
func (t *Tracker) Owns(deviceID, sessionID string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	e := t.sessions[deviceID]
	return e != nil && sessionID != "" && e.SessionID == sessionID
}

// Ref is the item the device's catalog session plays (media.panel reads its panels), false for another session.
func (t *Tracker) Ref(deviceID, sessionID string) (catalog.Ref, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	e := t.sessions[deviceID]
	if e == nil || e.SessionID != sessionID {
		return "", false
	}
	return e.Ref, true
}

// Current is the device's playing catalog session, with its title-group ref for source switching. False when another
// session (or none) is on the device now, even if an earlier catalog play is still tracked until its link settles.
func (t *Tracker) Current(deviceID string) (catalog.SessionState, bool) {
	sess, ok := t.d.Session(deviceID)
	t.mu.Lock()
	defer t.mu.Unlock()
	e := t.sessions[deviceID]
	if !ok || e == nil || sess.SessionID == "" || sess.SessionID != e.SessionID {
		return catalog.SessionState{}, false
	}
	ref := e.TitleRef
	if ref == "" {
		ref = e.Ref
	}
	return catalog.SessionState{SessionID: e.SessionID, Ref: ref, Episode: e.Episode,
		Choices: catalog.Selection{}.Merge(e.Choices)}, true
}

// Forget drops the device's catalog session when it is sessionID (a play that replaces it).
func (t *Tracker) Forget(deviceID, sessionID string) {
	t.mu.Lock()
	if e := t.sessions[deviceID]; e != nil && e.SessionID == sessionID {
		delete(t.sessions, deviceID)
	}
	t.mu.Unlock()
}

// index is the playing episode's place, -1 for none; under mu
func (e *entry) index() int {
	return slices.IndexFunc(e.episodes, func(it catalog.Item) bool { return it.Episode.Label == e.Episode })
}

// Fields are the session's server-owned fields, false while the device plays no catalog session of the server's or
// the catalog has not answered yet.
func (t *Tracker) Fields(deviceID string) (*cadencev2.MediaSessionState, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	e := t.sessions[deviceID]
	if e == nil || !e.read {
		return nil, false
	}
	st := &cadencev2.MediaSessionState{SessionId: e.SessionID, Playable: cadencev2.PlayableKind_PLAYABLE_KIND_STREAM}
	if i := e.index(); i >= 0 {
		if i+1 < len(e.episodes) {
			st.Actions = append(st.Actions, "next")
		}
		if i > 0 {
			st.Actions = append(st.Actions, "previous")
		}
		pos := uint32(min(i, 1<<16)) //nolint:gosec // bounded above
		st.QueuePosition = &pos
		for _, it := range e.episodes[i+1 : min(len(e.episodes), i+1+upNext)] {
			st.UpNext = append(st.UpNext, &cadencev2.MediaQueueItem{
				Ref: episodeRef + it.Episode.Label, Title: clip(it.Title, 256), Subtitle: clip(it.Subtitle, 256),
				DurationMs: uint64(max(it.DurationS, 0)) * 1000,
			})
		}
	}
	for _, a := range e.actions {
		if v, ok := actionView(a); ok {
			st.ItemActions = append(st.ItemActions, v)
		}
	}
	for _, p := range e.panels {
		st.Panels = append(st.Panels, &cadencev2.MediaPanel{
			Id: p.ID, Kind: panelKind(p.Kind), Title: clip(p.Title, 128), CountLabel: clip(p.CountLabel, 64),
		})
	}
	return st, true
}

var actionKinds = map[catalog.ActionKind]cadencev2.MediaItemActionKind{
	catalog.ActionLike: cadencev2.MediaItemActionKind_MEDIA_ITEM_ACTION_KIND_LIKE, catalog.ActionDislike: cadencev2.MediaItemActionKind_MEDIA_ITEM_ACTION_KIND_DISLIKE,
	catalog.ActionSave: cadencev2.MediaItemActionKind_MEDIA_ITEM_ACTION_KIND_SAVE, catalog.ActionSubscribe: cadencev2.MediaItemActionKind_MEDIA_ITEM_ACTION_KIND_SUBSCRIBE,
	catalog.ActionAddToQueue: cadencev2.MediaItemActionKind_MEDIA_ITEM_ACTION_KIND_ADD_TO_QUEUE,
	// a my-list status shows as saved or not; watched and the provider's own are custom
	catalog.ActionMyList: cadencev2.MediaItemActionKind_MEDIA_ITEM_ACTION_KIND_SAVE, catalog.ActionWatched: cadencev2.MediaItemActionKind_MEDIA_ITEM_ACTION_KIND_CUSTOM,
	catalog.ActionCustom: cadencev2.MediaItemActionKind_MEDIA_ITEM_ACTION_KIND_CUSTOM,
}

var panelKinds = map[catalog.PanelKind]cadencev2.MediaPanelKind{
	catalog.PanelDescription: cadencev2.MediaPanelKind_MEDIA_PANEL_KIND_DESCRIPTION,
	catalog.PanelComments:    cadencev2.MediaPanelKind_MEDIA_PANEL_KIND_COMMENTS,
	catalog.PanelChannel:     cadencev2.MediaPanelKind_MEDIA_PANEL_KIND_CHANNEL,
	catalog.PanelLyrics:      cadencev2.MediaPanelKind_MEDIA_PANEL_KIND_LYRICS,
	catalog.PanelRelated:     cadencev2.MediaPanelKind_MEDIA_PANEL_KIND_RELATED,
}

func panelKind(k catalog.PanelKind) cadencev2.MediaPanelKind {
	if v, ok := panelKinds[k]; ok {
		return v
	}
	return cadencev2.MediaPanelKind_MEDIA_PANEL_KIND_CUSTOM
}

func actionView(a catalog.Action) (*cadencev2.MediaItemAction, bool) {
	k, ok := actionKinds[a.Kind]
	if !ok || a.ID == "" {
		return nil, false
	}
	toggled := a.Toggled
	if a.Kind == catalog.ActionMyList {
		toggled = a.Value != ""
	}
	return &cadencev2.MediaItemAction{Id: a.ID, Kind: k, Label: clip(a.Label, 64), Icon: a.Icon, Toggled: toggled, Enabled: a.Enabled}, true
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && n < len(s) && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n]
}

// errors of Control and ItemAction, for media.control's and media.item_action's answers
var (
	ErrNotOwned      = errors.New("screenplay: not the device's catalog session")
	ErrUnknownRef    = errors.New("screenplay: unknown ref")
	ErrNoEpisode     = errors.New("screenplay: no such episode")
	ErrUnknownAction = errors.New("screenplay: unknown item action")
	ErrUnsupported   = errors.New("screenplay: not supported")
)

// Control runs media.control's action on the device's catalog session: next and previous play the episode after or
// before the playing one with its choices (the same voiceover), jump the episode an up next ref names, from its
// start. A failed play's tool error is returned as it is.
func (t *Tracker) Control(ctx context.Context, deviceID, sessionID, action, ref string) (*agent.ToolError, error) {
	t.mu.Lock()
	e := t.sessions[deviceID]
	if e == nil || e.SessionID != sessionID {
		t.mu.Unlock()
		return nil, ErrNotOwned
	}
	i := e.index()
	label := ""
	switch action {
	case "next", "previous":
		dir := 1
		if action == "previous" {
			dir = -1
		}
		if i < 0 || i+dir < 0 || i+dir >= len(e.episodes) {
			t.mu.Unlock()
			return nil, ErrNoEpisode
		}
		label = e.episodes[i+dir].Episode.Label
	case "jump":
		want, ok := strings.CutPrefix(ref, episodeRef)
		if !ok || !slices.ContainsFunc(e.episodes, func(it catalog.Item) bool { return it.Episode.Label == want }) {
			t.mu.Unlock()
			return nil, ErrUnknownRef
		}
		label = want
	default:
		t.mu.Unlock()
		return nil, ErrUnsupported
	}
	p := e.Played
	play := t.play
	if play != nil {
		t.moving[deviceID]++
	}
	t.mu.Unlock()
	if play == nil {
		return nil, ErrUnsupported
	}
	defer func() {
		t.mu.Lock()
		if t.moving[deviceID]--; t.moving[deviceID] <= 0 {
			delete(t.moving, deviceID)
		}
		t.mu.Unlock()
	}()
	playRef := p.Ref
	if p.TitleRef != "" {
		playRef = p.TitleRef
	}
	_, _, te := play(ctx, deviceID, playRef, label, p.Choices, catalog.FromStart, -1)
	if te != nil {
		return te, nil
	}
	t.d.Log.Info("screenplay_control", "device_id", deviceID, "action", action)
	return nil, nil
}

// ItemAction runs one of the session's item actions on the catalog (toggled off when it is on), keeps the item's
// actions after it and sends the fields again.
func (t *Tracker) ItemAction(ctx context.Context, deviceID, sessionID, actionID string) error {
	t.mu.Lock()
	e := t.sessions[deviceID]
	if e == nil || e.SessionID != sessionID {
		t.mu.Unlock()
		return ErrNotOwned
	}
	i := slices.IndexFunc(e.actions, func(a catalog.Action) bool { return a.ID == actionID })
	if i < 0 {
		t.mu.Unlock()
		return ErrUnknownAction
	}
	a, ref := e.actions[i], e.Ref
	t.mu.Unlock()
	on, value := !a.Toggled, ""
	if a.Kind == catalog.ActionMyList {
		on = a.Value == ""
		if on {
			value = "planned"
		}
	}
	acts, err := t.d.Catalog.Act(ctx, ref, a.ID, on, value)
	if err != nil {
		return err
	}
	t.mu.Lock()
	if t.sessions[deviceID] == e {
		e.actions = acts
	}
	t.mu.Unlock()
	t.d.Changed(deviceID)
	return nil
}
