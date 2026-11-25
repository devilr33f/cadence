package app

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"

	"google.golang.org/protobuf/proto"

	"git.shino.boo/devilreef/cadence/pkg/capschema"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/server/internal/agent/tools"
	"git.shino.boo/devilreef/cadence/server/internal/catalog"
	"git.shino.boo/devilreef/cadence/server/internal/follow"
	"git.shino.boo/devilreef/cadence/server/internal/groups"
	"git.shino.boo/devilreef/cadence/server/internal/media"
	"git.shino.boo/devilreef/cadence/server/internal/screenplay"
	"git.shino.boo/devilreef/cadence/server/internal/video"
)

// followDeps are what the video fan-out of a group's screens (multiroom P4) reads and sends through
type followDeps struct {
	groups   *groups.Service
	online   func(string) bool
	screen   func(ctx context.Context, deviceID string) bool
	tracker  *screenplay.Tracker
	sessions *media.Store
	state    func(ctx context.Context, deviceID, capability string) (json.RawMessage, bool)
	screens  tools.ScreenDeps
	// videos are the YouTube videos the server plays on screens, which followers play too
	videos *video.Service
	send   func(ctx context.Context, deviceID, capability, name string, args []byte) (json.RawMessage, error)
	// changed hears that a follower's session came or went: its server fields follow it
	changed func(deviceID string)
	log     *slog.Logger
}

func newFollow(d followDeps) *follow.Fanout {
	return follow.New(follow.Deps{
		Log: d.log,
		Groups: func() []follow.Group {
			var out []follow.Group
			for _, g := range d.groups.VideoGroups() {
				if g.Active {
					out = append(out, follow.Group{ID: g.ID, Leader: g.Leader, Members: g.Members})
				}
			}
			return out
		},
		Online:  d.online,
		Screen:  d.screen,
		Item:    func(deviceID string) (follow.Item, bool) { return followItem(d, deviceID) },
		Holding: d.tracker.Moving,
		Changed: d.changed,
		Video: func(ctx context.Context, deviceID string) (follow.Video, bool) {
			raw, ok := d.state(ctx, deviceID, follow.CapVideo)
			if !ok {
				return follow.Video{}, false
			}
			var st struct {
				SessionID string `json:"sessionId"`
				Follow    *struct {
					GroupID string `json:"groupId"`
				} `json:"follow"`
			}
			if json.Unmarshal(raw, &st) != nil {
				return follow.Video{}, false
			}
			v := follow.Video{SessionID: st.SessionID}
			if st.Follow != nil {
				v.GroupID = st.Follow.GroupID
			}
			return v, true
		},
		Play: func(ctx context.Context, deviceID string, it follow.Item, groupID string) (string, error) {
			if it.Video != nil {
				if d.videos == nil {
					return "", video.ErrNotSetUp
				}
				return d.videos.Follow(ctx, deviceID, *it.Video, groupID)
			}
			session, te := d.screens.Follow(ctx, deviceID, it.Ref, it.Episode, it.Choices, groupID)
			if te != nil {
				return "", errors.New(te.Code)
			}
			return session, nil
		},
		Stop: func(ctx context.Context, deviceID, sessionID string) error {
			args, err := json.Marshal(map[string]string{"sessionId": sessionID})
			if err != nil {
				return err
			}
			_, err = d.send(ctx, deviceID, follow.CapVideo, "stop", args)
			return err
		},
	})
}

// followStates pokes the fan-out on a screen's media.video state (a follower that left) and a media.group one (a lead
// that moved)
func followStates(f *follow.Fanout) func(deviceID, capability string, state json.RawMessage) {
	return func(_, capability string, _ json.RawMessage) {
		if capability == follow.CapVideo || capability == capschema.MediaGroupID {
			f.Poke()
		}
	}
}

// followItem is the video a group's leading screen plays, as its followers play it: the catalog item the server
// played, else the YouTube video it played; a video or cast of another kind is skipped (no follower can open it)
func followItem(d followDeps, deviceID string) (follow.Item, bool) {
	sess, _ := d.sessions.Raw(deviceID)
	playing := sess.State == "playing" || sess.State == "buffering"
	if cur, ok := d.tracker.Current(deviceID); ok {
		return follow.Item{SessionID: cur.SessionID, Ref: cur.Ref, Episode: cur.Episode, Choices: cur.Choices,
			Playing: playing}, true
	}
	if d.videos != nil {
		if r, ok := d.videos.Current(deviceID, sess.SessionID); ok {
			// a quality or audio switch is a new session of the same video: the followers keep theirs
			return follow.Item{SessionID: sess.SessionID, Key: "yt:" + r.VideoID + "/" + r.View, Video: &r,
				Playing: playing}, true
		}
	}
	if !playing || sess.SessionID == "" || d.tracker.Moving(deviceID) {
		return follow.Item{}, false
	}
	switch sess.Source {
	case "screencast":
		return follow.Item{SessionID: sess.SessionID, Skip: "cast", Playing: true}, true
	case "video":
		// a play the server holds nothing of (gone from its memory, or not its own)
		return follow.Item{SessionID: sess.SessionID, Skip: "not_reissuable", Playing: true}, true
	}
	return follow.Item{}, false
}

// followRoute maps a follower screen's session to its leader's session now (multiroom P4): its transport, item
// actions and panels act on the leader's item
func followRoute(f *follow.Fanout, sessions *media.Store) func(deviceID, sessionID string) (string, string, bool) {
	return func(deviceID, sessionID string) (string, string, bool) {
		leader, ok := f.Leader(deviceID, sessionID)
		if !ok {
			return "", "", false
		}
		cur, _ := sessions.Raw(leader)
		return leader, cur.SessionID, true
	}
}

// followRef is the catalog item a session's panels read: the device's own, else its leader's for a follower's
func followRef(own func(deviceID, sessionID string) (catalog.Ref, bool),
	route func(deviceID, sessionID string) (string, string, bool)) func(deviceID, sessionID string) (catalog.Ref, bool) {
	return func(deviceID, sessionID string) (catalog.Ref, bool) {
		if ref, ok := own(deviceID, sessionID); ok {
			return ref, true
		}
		if leader, ls, ok := route(deviceID, sessionID); ok {
			return own(leader, ls)
		}
		return "", false
	}
}

// followFields are a follower screen's server fields: its leader's (next, previous, up next, item actions,
// panels) under the follower's own session id, so its player offers the group's transport
func followFields(f *follow.Fanout, sources ...func(deviceID string) (*cadencev2.MediaSessionState, bool)) func(string) (*cadencev2.MediaSessionState, bool) {
	return func(deviceID string) (*cadencev2.MediaSessionState, bool) {
		session, leader, ok := f.Following(deviceID)
		if !ok {
			return nil, false
		}
		for _, src := range sources {
			if st, ok := src(leader); ok {
				st = proto.CloneOf(st)
				st.SessionId = session
				return st, true
			}
		}
		return nil, false
	}
}

// fieldsAndFollowers sends a device's server fields, and its followers' (theirs are its own)
func fieldsAndFollowers(push func(string), f func() *follow.Fanout) func(string) {
	return func(deviceID string) {
		push(deviceID)
		if fo := f(); fo != nil {
			for _, id := range fo.Followers(deviceID) {
				push(id)
			}
		}
	}
}
