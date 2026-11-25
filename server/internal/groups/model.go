// Package groups keeps the multiroom playback groups: their store, the leader each one elects among its online
// members, and every device's media.group config computed from them and pushed through the devices service with the
// group's key. Lines carry ids, codes and numbers only: never the key or an address (rule 8).
package groups

import (
	"strconv"
	"strings"
	"time"

	"git.shino.boo/devilreef/cadence/pkg/capschema"
	"git.shino.boo/devilreef/cadence/server/internal/devicename"
)

// group kinds as the store and the api spell them
const (
	KindGroup      = "group"
	KindStereoPair = "stereo_pair"
)

const (
	defaultBitrateKbps = 256
	defaultWaitMs      = 3000
	maxWaitMs          = 10000
	minMembers         = 2
)

// OutputTrim is one output's calibrated trim and whether its last applied calibration proved ultrasonic reachability.
type OutputTrim struct {
	OutputID   string
	TrimUs     int
	Ultrasonic string
}

// Member is one device of a group, in the group's order. TrimUs is its trim; TrimMs is TrimUs rounded to the
// millisecond, kept for what reads milliseconds.
type Member struct {
	DeviceID string
	Channel  string
	TrimMs   int
	TrimUs   int
	// Trims are calibrated output ids; TrimUs stays the fallback for every other output
	Trims []OutputTrim
}

// trimMs is a trim in us rounded to the millisecond, halves away from zero
func trimMs(us int) int {
	if us < 0 {
		return -((-us + 500) / 1000)
	}
	return (us + 500) / 1000
}

// Group is a stored group.
type Group struct {
	ID, Name, Kind string
	// PreferredLeader is the member that leads while it can; "" for none
	PreferredLeader        string
	LatencyMs, BitrateKbps int
	WaitMs                 int
	PCM                    bool
	// the leader tunes these itself: LatencyMs is where its latency starts, BitrateKbps and WaitMs the ceilings
	AutoLatency, AutoBitrate, AutoWait bool
	// FollowVideo: a video started on one of its screens opens on its other screens as followers (multiroom P4)
	FollowVideo bool
	// TrackingEnabled opts into ultrasonic live tracking; actual tracking still needs two proven active outputs
	TrackingEnabled      bool
	Members              []Member
	CreatedAt, UpdatedAt time.Time
}

// Spec is a group as create and update take it: nil latency, bitrate and wait take their defaults, nil auto flags and
// followVideo are on, an empty channel is both.
type Spec struct {
	Name, Kind, PreferredLeader        string
	LatencyMs, BitrateKbps             *int
	WaitMs                             *int
	PCM                                bool
	AutoLatency, AutoBitrate, AutoWait *bool
	FollowVideo                        *bool
	TrackingEnabled                    bool
	Members                            []Member
}

func orTrue(b *bool) bool { return b == nil || *b }

// InvalidError refuses a spec: a code and the JSON pointer of the field, as the api answers it (422).
type InvalidError struct{ Code, Path string }

func (e *InvalidError) Error() string { return e.Code }

func invalid(path string) *InvalidError { return &InvalidError{Code: "invalid", Path: path} }

// normalize checks s and returns the group it describes, defaults filled in; the members' existence and other groups
// are the store's to check.
func normalize(s Spec) (Group, error) {
	name, ok := devicename.Clean(s.Name)
	if !ok {
		return Group{}, invalid("/body/name")
	}
	g := Group{Name: name, Kind: s.Kind, PreferredLeader: s.PreferredLeader, PCM: s.PCM,
		LatencyMs: capschema.GroupLatencyDefaultMs, BitrateKbps: defaultBitrateKbps, WaitMs: defaultWaitMs,
		AutoLatency: orTrue(s.AutoLatency), AutoBitrate: orTrue(s.AutoBitrate), AutoWait: orTrue(s.AutoWait),
		FollowVideo: orTrue(s.FollowVideo), TrackingEnabled: s.TrackingEnabled}
	if g.Kind != KindGroup && g.Kind != KindStereoPair {
		return Group{}, invalid("/body/kind")
	}
	bounded := func(v *int, lo, hi int, dst *int, path string) error {
		if v == nil {
			return nil
		}
		if *v < lo || *v > hi {
			return invalid(path)
		}
		*dst = *v
		return nil
	}
	if err := bounded(s.LatencyMs, capschema.GroupLatencyMinMs, capschema.GroupLatencyMaxMs, &g.LatencyMs, "/body/latencyMs"); err != nil {
		return Group{}, err
	}
	if err := bounded(s.BitrateKbps, capschema.GroupBitrateMinKbps, capschema.GroupBitrateMaxKbps, &g.BitrateKbps, "/body/bitrateKbps"); err != nil {
		return Group{}, err
	}
	if err := bounded(s.WaitMs, 0, maxWaitMs, &g.WaitMs, "/body/waitMs"); err != nil {
		return Group{}, err
	}
	seen := map[string]bool{}
	for i, m := range s.Members {
		at := "/body/members/" + strconv.Itoa(i)
		id, ok := canonicalID(m.DeviceID)
		if !ok {
			return Group{}, &InvalidError{Code: "unknown_device", Path: at + "/deviceId"}
		}
		if seen[id] {
			return Group{}, invalid(at + "/deviceId")
		}
		seen[id] = true
		if m.Channel == "" {
			m.Channel = capschema.GroupChannelBoth
		}
		switch m.Channel {
		case capschema.GroupChannelBoth, capschema.GroupChannelLeft, capschema.GroupChannelRight, capschema.GroupChannelMono:
		default:
			return Group{}, invalid(at + "/channel")
		}
		if m.TrimMs < capschema.GroupTrimMinMs || m.TrimMs > capschema.GroupTrimMaxMs {
			return Group{}, invalid(at + "/trimMs")
		}
		// a trim to the microsecond (a calibration's, sent back as the group read) wins over its milliseconds
		us := m.TrimMs * 1000
		if m.TrimUs != 0 {
			if m.TrimUs < capschema.GroupTrimMinUs || m.TrimUs > capschema.GroupTrimMaxUs {
				return Group{}, invalid(at + "/trimUs")
			}
			us = m.TrimUs
		}
		g.Members = append(g.Members, Member{DeviceID: id, Channel: m.Channel, TrimMs: trimMs(us), TrimUs: us})
	}
	switch {
	case g.Kind == KindStereoPair && len(g.Members) != 2:
		return Group{}, invalid("/body/members")
	case g.Kind == KindStereoPair && !pairChannels(g.Members):
		return Group{}, invalid("/body/members/1/channel")
	case len(g.Members) < minMembers || len(g.Members) > capschema.GroupMaxMembers:
		return Group{}, invalid("/body/members")
	}
	if g.PreferredLeader != "" {
		id, ok := canonicalID(g.PreferredLeader)
		if !ok || !seen[id] {
			return Group{}, invalid("/body/preferredLeader")
		}
		g.PreferredLeader = id
	}
	return g, nil
}

// a stereo pair is one left speaker and one right
func pairChannels(ms []Member) bool {
	a, b := ms[0].Channel, ms[1].Channel
	return a == capschema.GroupChannelLeft && b == capschema.GroupChannelRight ||
		a == capschema.GroupChannelRight && b == capschema.GroupChannelLeft
}

// sameRoom says every room is the same named one (case and spacing aside)
func sameRoom(rooms []string) bool {
	if len(rooms) == 0 {
		return false
	}
	first := strings.ToLower(strings.TrimSpace(rooms[0]))
	if first == "" {
		return false
	}
	for _, r := range rooms[1:] {
		if strings.ToLower(strings.TrimSpace(r)) != first {
			return false
		}
	}
	return true
}

func memberIDs(ms []Member) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.DeviceID
	}
	return out
}
