package remote

import (
	"cmp"
	"context"
	"slices"
	"strings"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/media"
)

// the session states a screen is shown; idle and unspecified ones are not
var shownStates = map[string]bool{"playing": true, "paused": true, "buffering": true}

// Item is one entry of devices.media: a device's session, or a group's (its leader's). id is the group's id for a
// group and the device's for a lone device; room is omitted when the device has none (a group's when its members'
// differ); art is an img: ref of the image proxy.
type Item struct {
	ID           string `json:"id"`
	DeviceID     string `json:"deviceId"`
	Name         string `json:"name"`
	Room         string `json:"room,omitempty"`
	SameRoom     bool   `json:"sameRoom"`
	Source       string `json:"source"`
	State        string `json:"state"`
	Title        string `json:"title,omitempty"`
	Artist       string `json:"artist,omitempty"`
	Album        string `json:"album,omitempty"`
	Art          string `json:"art,omitempty"`
	PositionMs   uint64 `json:"positionMs,omitempty"`
	PositionAtMs int64  `json:"positionAtMs,omitempty"`
	DurationMs   uint64 `json:"durationMs,omitempty"`
	Here         bool   `json:"here"`
}

// MediaList is devices.media's answer and each event's.
type MediaList struct {
	Items []Item `json:"items"`
}

// entry is an item with what the panel and the change check read beside it
type entry struct {
	Item
	// the group's id, "" for a lone device
	group string
	kind  string
	rate  float32
	// what the panel's card shows beside the item: the session's metadata (an episode's series, its provider) and the
	// music queue's provider
	md       media.MediaMetadata
	provider string
	// the transport the session lists
	actions []string
	// the devices that play it: the group's members, or the device alone; each one's name and level (-1 unknown)
	members []member
}

type member struct {
	id, name string
	level    int
}

// entries is what the caller is shown: one entry per lone device with a session (its own never) and one per group
// whose leader has one; the caller's room first, then by name
func (s *Service) entries(ctx context.Context, caller string, levels bool) ([]entry, error) {
	devs, err := s.d.Devices(ctx)
	if err != nil {
		return nil, err
	}
	var grps []Group
	if s.d.Groups != nil {
		if grps, err = s.d.Groups(ctx); err != nil {
			return nil, err
		}
	}
	by := make(map[string]Device, len(devs))
	for _, d := range devs {
		by[d.ID] = d
	}
	room := by[caller].Room
	grouped := map[string]bool{}
	var out []entry
	for _, g := range grps {
		// a group without a leader streams to nobody: its members play on their own
		if g.Leader == "" {
			continue
		}
		for _, m := range g.Members {
			grouped[m] = true
		}
		sess, ok := s.session(g.Leader)
		if !ok {
			continue
		}
		e := s.entry(g.Leader, sess)
		e.ID, e.group, e.kind, e.Name = g.ID, g.ID, g.Kind, g.Name
		rooms := map[string]bool{}
		for _, m := range g.Members {
			d := by[m]
			rooms[d.Room] = true
			e.SameRoom = e.SameRoom || (room != "" && d.Room == room)
			e.Here = e.Here || m == caller
			e.members = append(e.members, s.member(ctx, d, m, levels))
		}
		if len(rooms) == 1 {
			e.Room = by[g.Leader].Room
		}
		out = append(out, e)
	}
	for _, d := range devs {
		if d.ID == caller || grouped[d.ID] {
			continue
		}
		sess, ok := s.session(d.ID)
		if !ok {
			continue
		}
		e := s.entry(d.ID, sess)
		e.ID, e.Name, e.Room = d.ID, d.Name, d.Room
		e.SameRoom = room != "" && d.Room == room
		e.members = []member{s.member(ctx, d, d.ID, levels)}
		out = append(out, e)
	}
	slices.SortFunc(out, func(a, b entry) int {
		if a.SameRoom != b.SameRoom {
			if a.SameRoom {
				return -1
			}
			return 1
		}
		return cmp.Or(cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)), cmp.Compare(a.ID, b.ID))
	})
	return out, nil
}

// session is the device's session while it is one a screen is shown
func (s *Service) session(dev string) (media.MediaSession, bool) {
	sess, ok := s.d.Session(dev)
	return sess, ok && shownStates[sess.State]
}

func (s *Service) entry(dev string, sess media.MediaSession) entry {
	e := entry{Item: Item{
		DeviceID: dev, Source: sess.Source, State: sess.State, Title: sess.Metadata.Title, Artist: sess.Metadata.Artist,
		Album: sess.Metadata.Album, PositionMs: sess.PositionMs, DurationMs: sess.DurationMs,
	}, rate: sess.Rate, md: sess.Metadata, provider: sess.Provider, actions: sess.Actions}
	if at, err := time.Parse(time.RFC3339Nano, sess.PositionAt); sess.PositionAt != "" && err == nil {
		e.PositionAtMs = at.UnixMilli()
	}
	if s.d.Art != nil {
		e.Art = s.d.Art(dev, sess)
	}
	return e
}

// member is a device that plays an entry, with its level when asked for: its session's own, else its audio.output's
func (s *Service) member(ctx context.Context, d Device, id string, levels bool) member {
	m := member{id: id, name: d.Name, level: -1}
	if !levels {
		return m
	}
	if sess, ok := s.d.Session(id); ok && sess.Volume != nil {
		m.level = int(min(*sess.Volume, 100))
	} else if s.d.Level != nil {
		if l, ok := s.d.Level(ctx, id); ok {
			m.level = min(max(l, 0), 100)
		}
	}
	return m
}

// a position this far from where the last one extrapolates to is a seek
const seekSlackMs = 1500

// differ says whether b is worth sending after a: anything but a position that moved as playback does (a seek
// counts); members (with their levels) and the metadata only the panel shows when it compares
func differ(a, b []entry, members bool) bool {
	if len(a) != len(b) {
		return true
	}
	for i := range a {
		x, y := a[i], b[i]
		if jumped(x, y) {
			return true
		}
		x.PositionMs, x.PositionAtMs, y.PositionMs, y.PositionAtMs = 0, 0, 0, 0
		if x.Item != y.Item || x.group != y.group || x.rate != y.rate || !slices.Equal(x.actions, y.actions) {
			return true
		}
		if members && (!slices.Equal(x.members, y.members) || x.md != y.md || x.provider != y.provider) {
			return true
		}
	}
	return false
}

// jumped says b's position is not where a's extrapolates to
func jumped(a, b entry) bool {
	if a.PositionAtMs == 0 || b.PositionAtMs == 0 {
		// unstamped positions cannot be extrapolated: only a paused one that moved is news
		return a.State == "paused" && b.State == "paused" && a.PositionMs != b.PositionMs
	}
	want := float64(a.PositionMs)
	if a.State == "playing" {
		rate := float64(a.rate)
		if rate == 0 {
			rate = 1
		}
		want += float64(b.PositionAtMs-a.PositionAtMs) * rate
	}
	d := float64(b.PositionMs) - want
	return d > seekSlackMs || d < -seekSlackMs
}

func items(es []entry) MediaList {
	out := MediaList{Items: make([]Item, len(es))}
	for i, e := range es {
		out.Items[i] = e.Item
	}
	return out
}
