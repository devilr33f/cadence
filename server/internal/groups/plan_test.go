package groups

import (
	"errors"
	"reflect"
	"testing"

	"git.shino.boo/devilreef/cadence/pkg/capschema"
)

const (
	devA = "00000000-0000-0000-0000-00000000000a"
	devB = "00000000-0000-0000-0000-00000000000b"
	devC = "00000000-0000-0000-0000-00000000000c"
	devD = "00000000-0000-0000-0000-00000000000d"
	gid  = "00000000-0000-0000-0000-0000000000f1"
)

func up(model, room string, addrs ...string) Facts {
	if addrs == nil {
		addrs = []string{"10.0.0.1"}
	}
	return Facts{Model: model, Room: room, Online: true, Capable: true, Addresses: addrs}
}

func down(model, room string) Facts { return Facts{Model: model, Room: room, Capable: true} }

func members(ids ...string) []Member {
	out := make([]Member, len(ids))
	for i, id := range ids {
		out[i] = Member{DeviceID: id, Channel: capschema.GroupChannelBoth}
	}
	return out
}

func TestElect(t *testing.T) {
	cases := []struct {
		name      string
		preferred string
		facts     map[string]Facts
		want      string
	}{
		{"preferred online", devB, map[string]Facts{devA: up("sim", ""), devB: up("sim", "")}, devB},
		{"preferred offline: first by position", devB, map[string]Facts{devA: up("sim", ""), devB: down("sim", "")}, devA},
		{"preferred without the capability", devB, map[string]Facts{devA: up("sim", ""), devB: {Online: true, Addresses: []string{"x"}}}, devA},
		{"preferred without an address", devB, map[string]Facts{devA: up("sim", ""), devB: {Online: true, Capable: true}}, devA},
		{"speakers before screens", "", map[string]Facts{devA: up("android-tv", ""), devB: up("yandex-pickle", "")}, devB},
		{"a screen when no speaker can", "", map[string]Facts{devA: up("sim-tv", ""), devB: down("sim", "")}, devA},
		{"a preferred screen leads", devA, map[string]Facts{devA: up("rpi", ""), devB: up("sim", "")}, devA},
		{"nobody online", "", map[string]Facts{devA: down("sim", ""), devB: down("sim", "")}, ""},
		{"an unknown device", "", map[string]Facts{devB: up("sim", "")}, devB},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g := Group{ID: gid, Kind: KindGroup, PreferredLeader: c.preferred, Members: members(devA, devB)}
			if got := elect(g, c.facts); got != c.want {
				t.Fatalf("elected %q, want %q", got, c.want)
			}
		})
	}
}

func TestComputeConfigs(t *testing.T) {
	key := []byte{0xab, 0xcd}
	group := func(kind string, ms []Member) []keyed {
		return []keyed{{Group: Group{ID: gid, Kind: kind, LatencyMs: 1500, BitrateKbps: 320, WaitMs: 2000, PCM: true, Members: ms}, Key: key}}
	}
	leaderCfg := func(budget int, ms ...string) Desired {
		return Desired{Key: "abcd", Config: capschema.MediaGroup{
			Enabled: true, GroupID: gid, Role: "leader", Port: 7701, Channel: "both", LatencyMs: 1500, BudgetUs: budget,
			BitrateKbps: 320, PCM: true, WaitMs: 2000, Members: ms, Tracking: &capschema.MediaGroupTrackingConfig{Enabled: false},
		}}
	}
	memberCfg := func(budget int, host, channel string, trim int) Desired {
		return Desired{Key: "abcd", Config: capschema.MediaGroup{
			Enabled: true, GroupID: gid, Role: "member", Leader: &capschema.MediaGroupLeader{Host: host, Port: 7701},
			Channel: channel, LatencyMs: 1500, TrimMs: trim, BudgetUs: budget, BitrateKbps: 320, PCM: true, WaitMs: 2000,
			Tracking: &capschema.MediaGroupTrackingConfig{Enabled: false},
		}}
	}
	pair := []Member{{DeviceID: devA, Channel: "left"}, {DeviceID: devB, Channel: "right", TrimMs: -20}}
	cases := []struct {
		name    string
		groups  []keyed
		facts   map[string]Facts
		want    map[string]Desired
		leader  string
		active  bool
		sources map[string]string
		clock   string
	}{
		{
			name:   "one room: the room budget, the leader's first address",
			groups: group(KindGroup, members(devA, devB, devC)),
			facts: map[string]Facts{devA: up("sim", "Kitchen", "10.0.0.5", "10.0.0.6"), devB: up("sim", " kitchen"),
				devC: up("sim", "KITCHEN"), devD: up("sim", "")},
			want: map[string]Desired{
				devA: leaderCfg(1000, devB, devC), devB: memberCfg(1000, "10.0.0.5", "both", 0),
				devC: memberCfg(1000, "10.0.0.5", "both", 0), devD: disabled,
			},
			leader: devA, active: true,
		},
		{
			name:   "rooms differ: the house budget; an offline member keeps its config, the leader leaves it out",
			groups: group(KindGroup, members(devA, devB, devC)),
			facts:  map[string]Facts{devA: up("sim", "Kitchen"), devB: up("sim", "Hall"), devC: down("sim", "Kitchen")},
			want: map[string]Desired{
				devA: leaderCfg(5000, devB), devB: memberCfg(5000, "10.0.0.1", "both", 0), devC: memberCfg(5000, "10.0.0.1", "both", 0),
			},
			leader: devA, active: true,
		},
		{
			name:   "no room named: the house budget",
			groups: group(KindGroup, members(devA, devB)),
			facts:  map[string]Facts{devA: up("sim", ""), devB: up("sim", "")},
			want:   map[string]Desired{devA: leaderCfg(5000, devB), devB: memberCfg(5000, "10.0.0.1", "both", 0)},
			leader: devA, active: true,
		},
		{
			name:   "a stereo pair: the pair budget, each member's channel and trim",
			groups: group(KindStereoPair, pair),
			facts:  map[string]Facts{devA: up("sim", "Hall"), devB: up("sim", "Hall")},
			want: map[string]Desired{
				devA: {Key: "abcd", Config: func() capschema.MediaGroup {
					c := leaderCfg(100, devB).Config
					c.Channel = "left"
					return c
				}()},
				devB: memberCfg(100, "10.0.0.1", "right", -20),
			},
			leader: devA, active: true,
		},
		{
			name:   "a leader with no online member is disabled",
			groups: group(KindGroup, members(devA, devB)),
			facts:  map[string]Facts{devA: up("sim", ""), devB: down("sim", "")},
			want:   map[string]Desired{devA: disabled, devB: disabled},
			leader: devA,
		},
		{
			name:   "no leader: every member disabled",
			groups: group(KindGroup, members(devA, devB)),
			facts:  map[string]Facts{devA: down("sim", ""), devB: {Online: true, Capable: true}},
			want:   map[string]Desired{devA: disabled, devB: disabled},
		},
		{
			name:   "a member without the capability gets nothing and is not streamed to",
			groups: group(KindGroup, members(devA, devB, devC)),
			facts:  map[string]Facts{devA: up("sim", ""), devB: {Online: true}, devC: up("sim", "")},
			want:   map[string]Desired{devA: leaderCfg(5000, devC), devC: memberCfg(5000, "10.0.0.1", "both", 0)},
			leader: devA, active: true,
		},
		{
			name:    "a source apart from the clock master: the source leads on the master's clock, the master serves it",
			groups:  group(KindGroup, members(devA, devB, devC)),
			facts:   map[string]Facts{devA: up("sim", "", "10.0.0.5"), devB: up("sim-tv", "", "10.0.0.6"), devC: up("sim", "", "10.0.0.7")},
			sources: map[string]string{gid: devB},
			want: map[string]Desired{
				devB: {Key: "abcd", Config: func() capschema.MediaGroup {
					c := leaderCfg(5000, devA, devC).Config
					c.Clock = &capschema.MediaGroupLeader{Host: "10.0.0.5", Port: 7701}
					return c
				}()},
				devA: {Key: "abcd", Config: func() capschema.MediaGroup {
					c := memberCfg(5000, "10.0.0.6", "both", 0).Config
					c.ClockMaster = true
					return c
				}()},
				devC: {Key: "abcd", Config: func() capschema.MediaGroup {
					c := memberCfg(5000, "10.0.0.6", "both", 0).Config
					c.Clock = &capschema.MediaGroupLeader{Host: "10.0.0.5", Port: 7701}
					return c
				}()},
			},
			leader: devB, clock: devA, active: true,
		},
		{
			name:    "a source that is the clock master sends neither clock nor clockMaster",
			groups:  group(KindGroup, members(devA, devB)),
			facts:   map[string]Facts{devA: up("sim", "", "10.0.0.5"), devB: up("sim", "")},
			sources: map[string]string{gid: devA},
			want:    map[string]Desired{devA: leaderCfg(5000, devB), devB: memberCfg(5000, "10.0.0.5", "both", 0)},
			leader:  devA, clock: devA, active: true,
		},
		{
			name:    "a source that cannot lead: the clock master leads",
			groups:  group(KindGroup, members(devA, devB)),
			facts:   map[string]Facts{devA: up("sim", "", "10.0.0.5"), devB: {Online: true, Capable: true}},
			sources: map[string]string{gid: devB},
			want:    map[string]Desired{devA: leaderCfg(5000, devB), devB: memberCfg(5000, "10.0.0.5", "both", 0)},
			leader:  devA, clock: devA, active: true,
		},
		{
			name:    "a source outside the group is not its leader",
			groups:  group(KindGroup, members(devA, devB)),
			facts:   map[string]Facts{devA: up("sim", "", "10.0.0.5"), devB: up("sim", ""), devC: up("sim", "")},
			sources: map[string]string{gid: devC},
			want:    map[string]Desired{devA: leaderCfg(5000, devB), devB: memberCfg(5000, "10.0.0.5", "both", 0), devC: disabled},
			leader:  devA, clock: devA, active: true,
		},
		{
			name:  "no group: disabled",
			facts: map[string]Facts{devA: up("sim", ""), devB: {Online: true}},
			want:  map[string]Desired{devA: disabled},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := compute(c.groups, c.facts, c.sources)
			if !reflect.DeepEqual(p.Devices, c.want) {
				t.Fatalf("configs\n got %+v\nwant %+v", p.Devices, c.want)
			}
			if c.groups != nil && (p.Leaders[gid] != c.leader || p.Active[gid] != c.active) {
				t.Fatalf("leader %q active %v", p.Leaders[gid], p.Active[gid])
			}
			if c.clock != "" && p.Clocks[gid] != c.clock {
				t.Fatalf("clock master %q, want %q", p.Clocks[gid], c.clock)
			}
		})
	}
}

func intp(v int) *int { return &v }

func TestNormalize(t *testing.T) {
	ok := func(s Spec) Spec {
		if s.Name == "" {
			s.Name = "Home"
		}
		if s.Kind == "" {
			s.Kind = KindGroup
		}
		if s.Members == nil {
			s.Members = members(devA, devB)
		}
		return s
	}
	g, err := normalize(ok(Spec{Name: "  Home ", PreferredLeader: "00000000-0000-0000-0000-00000000000B"}))
	if err != nil {
		t.Fatal(err)
	}
	if g.Name != "Home" || g.LatencyMs != 1000 || g.BitrateKbps != 256 || g.WaitMs != 3000 || g.PreferredLeader != devB ||
		g.Members[0].Channel != "both" {
		t.Fatalf("%+v", g)
	}
	eight := members(devA, devB, devC, devD, "00000000-0000-0000-0000-000000000001", "00000000-0000-0000-0000-000000000002",
		"00000000-0000-0000-0000-000000000003", "00000000-0000-0000-0000-000000000004")
	if _, err := normalize(ok(Spec{Members: eight, WaitMs: intp(0), LatencyMs: intp(5000), BitrateKbps: intp(64)})); err != nil {
		t.Fatalf("eight members: %v", err)
	}
	if _, err := normalize(ok(Spec{Kind: KindStereoPair, Members: []Member{{DeviceID: devA, Channel: "right"}, {DeviceID: devB, Channel: "left"}}})); err != nil {
		t.Fatalf("a pair: %v", err)
	}
	refusals := []struct {
		spec Spec
		code string
		path string
	}{
		{Spec{Name: "\u200b"}, "invalid", "/body/name"},
		{Spec{Kind: "party"}, "invalid", "/body/kind"},
		{Spec{LatencyMs: intp(199)}, "invalid", "/body/latencyMs"},
		{Spec{LatencyMs: intp(5001)}, "invalid", "/body/latencyMs"},
		{Spec{BitrateKbps: intp(511)}, "invalid", "/body/bitrateKbps"},
		{Spec{WaitMs: intp(-1)}, "invalid", "/body/waitMs"},
		{Spec{Members: members(devA)}, "invalid", "/body/members"},
		{Spec{Members: append(eight, Member{DeviceID: "00000000-0000-0000-0000-000000000005"})}, "invalid", "/body/members"},
		{Spec{Members: members(devA, devA)}, "invalid", "/body/members/1/deviceId"},
		{Spec{Members: members(devA, "kitchen")}, "unknown_device", "/body/members/1/deviceId"},
		{Spec{Members: []Member{{DeviceID: devA, Channel: "center"}, {DeviceID: devB}}}, "invalid", "/body/members/0/channel"},
		{Spec{Members: []Member{{DeviceID: devA, TrimMs: 501}, {DeviceID: devB}}}, "invalid", "/body/members/0/trimMs"},
		{Spec{Kind: KindStereoPair, Members: members(devA, devB, devC)}, "invalid", "/body/members"},
		{Spec{Kind: KindStereoPair, Members: []Member{{DeviceID: devA, Channel: "left"}, {DeviceID: devB, Channel: "left"}}}, "invalid", "/body/members/1/channel"},
		{Spec{Kind: KindStereoPair, Members: members(devA, devB)}, "invalid", "/body/members/1/channel"},
		{Spec{PreferredLeader: devC}, "invalid", "/body/preferredLeader"},
	}
	for _, r := range refusals {
		_, err := normalize(ok(r.spec))
		var inv *InvalidError
		if !errors.As(err, &inv) || inv.Code != r.code || inv.Path != r.path {
			t.Errorf("%+v: %v, want %s %s", r.spec, err, r.code, r.path)
		}
	}
}
