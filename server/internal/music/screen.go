package music

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strconv"
	"strings"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/pkg/rpc"
	"git.shino.boo/devilreef/cadence/server/internal/screens/calls"
)

const (
	// queuePage is the most items a media.queue page holds
	queuePage = 50
	// refPrefix marks a queue item's ref: the item's place in the queue in the order songs were added, which a skip
	// or a shuffle leaves alone
	refPrefix = "mq:"
)

// QueueItem is one song of a media.queue page; Ref plays it with media.control's jump.
type QueueItem struct {
	Ref        string `json:"ref"`
	Title      string `json:"title"`
	Subtitle   string `json:"subtitle,omitempty"`
	DurationMs int    `json:"durationMs,omitempty"`
	ArtworkURL string `json:"artworkUrl,omitempty"`
}

// QueuePage is media.queue's answer: by default the songs after the playing one in play order, from the cursor; with
// all, every song, at most MaxQueue. Position is the playing song's place in play order and Total the queue's size.
type QueuePage struct {
	Name       string      `json:"name,omitempty"`
	ArtworkURL string      `json:"artworkUrl,omitempty"`
	Total      int         `json:"total"`
	Items      []QueueItem `json:"items"`
	Position   int         `json:"position"`
	// Next is the cursor of the page after this one, absent at the end
	Next string `json:"next,omitempty"`
}

// RegisterScreen adds media.queue to r: the screen's queue page. media.control goes through the app's dispatcher,
// which hands ControlMethod the sessions the queue plays (OwnsSession). Titles and refs are never logged.
func (s *Service) RegisterScreen(r *calls.Router) {
	r.Register(rpc.MediaQueue, s.queueMethod)
}

// ControlMethod is media.control for a session the queue plays.
func (s *Service) ControlMethod() calls.Method { return s.controlMethod }

// OwnsSession is whether the device's session is sessionID and a song of its queue.
func (s *Service) OwnsSession(deviceID, sessionID string) bool {
	return s.ownsSession(deviceID, sessionID)
}

func strict(raw []byte, into any) bool {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(into) != nil {
		return false
	}
	var rest json.RawMessage
	return dec.Decode(&rest) == io.EOF
}

// ownsSession is whether the device's session is sessionID and a song of its queue
func (s *Service) ownsSession(deviceID, sessionID string) bool {
	sess, ok := s.d.Session(deviceID)
	return ok && sessionID != "" && sess.SessionID == sessionID && s.Owns(deviceID)
}

func (s *Service) queueMethod(_ context.Context, c calls.Call) calls.Reply {
	var a struct {
		SessionID string `json:"sessionId"`
		Cursor    string `json:"cursor,omitempty"`
		All       bool   `json:"all,omitempty"`
	}
	if !strict(c.Args, &a) || a.SessionID == "" || len(a.SessionID) > 64 || (a.All && a.Cursor != "") {
		return calls.Fail(rpc.StatusInvalid, "invalid")
	}
	from := 0
	if a.Cursor != "" {
		n, err := strconv.Atoi(a.Cursor)
		if err != nil || n < 0 || n > MaxQueue {
			return calls.Fail(rpc.StatusInvalid, "invalid")
		}
		from = n
	}
	if !s.ownsSession(c.DeviceID, a.SessionID) {
		return calls.Fail(rpc.StatusNotFound, "no_queue")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	q := s.queues[c.DeviceID]
	if q == nil {
		return calls.Fail(rpc.StatusNotFound, "no_queue")
	}
	page := QueuePage{
		Items: []QueueItem{}, Position: q.pos, Total: len(q.order), Name: q.name, ArtworkURL: s.image(q.artworkURL),
	}
	from += q.pos + 1
	limit := queuePage
	if a.All {
		from, limit = 0, MaxQueue
	}
	for i := from; i < len(q.order); i++ {
		if len(page.Items) == limit {
			if !a.All {
				page.Next = strconv.Itoa(i - q.pos - 1)
			}
			break
		}
		t := q.items[q.order[i]]
		page.Items = append(page.Items, QueueItem{
			Ref: refPrefix + strconv.Itoa(q.order[i]), Title: t.Title, Subtitle: t.Artist, DurationMs: t.DurationS * 1000,
			ArtworkURL: s.image(t.ArtworkURL),
		})
	}
	return calls.OK(page)
}

func (s *Service) image(raw string) string {
	if s.d.Image != nil {
		return s.d.Image(raw)
	}
	return raw
}

// controlMethod serves media.control {sessionId, action, ref?} for a session the queue plays: next and previous skip
// through it, jump plays a queue item by its ref, play_next moves one right after the playing song and remove takes
// one out (never the playing one)
func (s *Service) controlMethod(ctx context.Context, c calls.Call) calls.Reply {
	var a struct {
		SessionID string `json:"sessionId"`
		Action    string `json:"action"`
		Ref       string `json:"ref,omitempty"`
	}
	if !strict(c.Args, &a) || a.SessionID == "" || len(a.SessionID) > 64 || len(a.Ref) > 256 {
		return calls.Fail(rpc.StatusInvalid, "invalid")
	}
	if !s.ownsSession(c.DeviceID, a.SessionID) {
		return calls.Fail(rpc.StatusNotFound, "no_queue")
	}
	var err error
	switch a.Action {
	case "next":
		err = s.Skip(ctx, c.DeviceID, 1)
	case "previous":
		err = s.Skip(ctx, c.DeviceID, -1)
	case "jump":
		err = s.jumpRef(ctx, c.DeviceID, a.Ref)
	case "play_next":
		err = s.moveRef(c.DeviceID, a.Ref, true)
	case "remove":
		err = s.moveRef(c.DeviceID, a.Ref, false)
	default:
		return calls.Fail(rpc.StatusInvalid, "invalid")
	}
	switch {
	case err == nil:
		return calls.OK(struct{}{})
	case errors.Is(err, errBadRef):
		return calls.Fail(rpc.StatusExpired, "unknown_ref")
	case errors.Is(err, ErrNoQueue):
		return calls.Fail(rpc.StatusNotFound, "no_queue")
	case errors.Is(err, ErrQueueEnd):
		return calls.Fail(rpc.StatusNotFound, "queue_end")
	}
	s.d.Log.Info("music_control_failed", "device_id", c.DeviceID, "rpc_id", c.ID, "action", a.Action)
	return calls.Fail(rpc.StatusUnavailable, "unavailable")
}

var errBadRef = errors.New("music: unknown queue ref")

// moveRef puts the queue item ref names right after the playing song (next), or takes it out of the play order; the
// playing song itself is neither. The item keeps its index, so the other refs stay valid
func (s *Service) moveRef(deviceID, ref string, next bool) error {
	item, err := strconv.Atoi(strings.TrimPrefix(ref, refPrefix))
	if !strings.HasPrefix(ref, refPrefix) || err != nil {
		return errBadRef
	}
	s.mu.Lock()
	q := s.queues[deviceID]
	if q == nil {
		s.mu.Unlock()
		return ErrNoQueue
	}
	at := -1
	for i := q.pos + 1; i < len(q.order); i++ {
		if q.order[i] == item {
			at = i
			break
		}
	}
	if at < 0 {
		s.mu.Unlock()
		return errBadRef
	}
	q.order = slices.Delete(q.order, at, at+1)
	if next {
		q.order = slices.Insert(q.order, q.pos+1, item)
	}
	s.mu.Unlock()
	s.fieldsChanged(deviceID)
	return nil
}

// jumpRef plays the queue item ref names, ahead of or behind the playing one; the playing one itself is a no-op
func (s *Service) jumpRef(ctx context.Context, deviceID, ref string) error {
	defer s.playLock(deviceID)()
	item, err := strconv.Atoi(strings.TrimPrefix(ref, refPrefix))
	if !strings.HasPrefix(ref, refPrefix) || err != nil {
		return errBadRef
	}
	s.mu.Lock()
	q := s.queues[deviceID]
	at, was := -1, 0
	if q != nil {
		was = q.pos
		for i, itemIndex := range q.order {
			if itemIndex == item {
				at = i
				break
			}
		}
		if at >= 0 {
			q.pos = at
		}
	}
	s.mu.Unlock()
	if q == nil {
		return ErrNoQueue
	}
	if at < 0 {
		return errBadRef
	}
	if at == was {
		return nil
	}
	dir := 1
	if at < was {
		dir = -1
	}
	return s.playSkipping(ctx, deviceID, q, dir)
}

// upNext is the most queue items a session's fields carry; media.queue pages the rest
const upNext = 20

// SessionFields are the server's own fields of the session the queue plays on the device (the link's
// media_session_state, server -> device): its transport, the next songs as queue items whose refs media.control's jump
// takes, and the playing song's place. false while the device plays none of the queue's songs. Memory-only (D11).
func (s *Service) SessionFields(deviceID string) (*cadencev2.MediaSessionState, bool) {
	sess, ok := s.d.Session(deviceID)
	if !ok || !s.Owns(deviceID) {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	q := s.queues[deviceID]
	if q == nil {
		return nil, false
	}
	// a queue holds at most MaxQueue songs
	pos := uint32(min(max(q.pos, 0), MaxQueue)) //nolint:gosec // bounded above
	st := &cadencev2.MediaSessionState{
		SessionId: sess.SessionID, Actions: []string{"next", "previous"}, QueuePosition: &pos,
		Playable: cadencev2.PlayableKind_PLAYABLE_KIND_STREAM,
	}
	for i := q.pos + 1; i < len(q.order) && len(st.UpNext) < upNext; i++ {
		t := q.items[q.order[i]]
		st.UpNext = append(st.UpNext, &cadencev2.MediaQueueItem{
			Ref: refPrefix + strconv.Itoa(q.order[i]), Title: t.Title, Subtitle: t.Artist,
			DurationMs: uint64(max(t.DurationS, 0)) * 1000,
		})
	}
	return st, true
}
