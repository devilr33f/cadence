package agent_test

import (
	"context"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/agent"
)

// a multiroom group's name resolves to its leader, which plays for every member; a device's own name comes first, and
// a group that elected no leader is offline
func TestResolveTargetGroups(t *testing.T) {
	hall := agent.DeviceInfo{ID: "hall", Name: "Прихожая", Room: "hall", Kind: agent.KindSpeaker, Online: true,
		Caps: []string{"audio.output", "media.url", "media.player"}, RemoteTargets: "allow"}
	r := resolver(speakerInfo(), hall, tvInfo(livingTV, "Телевизор", "living_room"))
	r.Groups = func(context.Context) ([]agent.GroupName, error) {
		return []agent.GroupName{{Name: "Весь дом", LeaderID: "hall"}, {Name: "Прихожая", LeaderID: device},
			{Name: "Дача", LeaderID: ""}}, nil
	}
	cases := []struct {
		arg  string
		need agent.Need
		want string
	}{
		{"Весь дом", music, "hall"},
		{"весь дом", volume, "hall"},
		{"на весь дом", music, "hall"},
		// a device of that name wins
		{"Прихожая", music, "hall"},
		{"дача", music, "device_offline"},
	}
	for _, c := range cases {
		got, e := r.ResolveTarget(context.Background(), woken(), c.arg, c.need)
		id := got.ID
		if e != nil {
			id = e.Code
		}
		if id != c.want {
			t.Errorf("%q: %s %+v", c.arg, id, e)
		}
	}
}
