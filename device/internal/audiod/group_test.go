package audiod_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/audiod"
	"git.shino.boo/devilreef/cadence/device/internal/audiod/audiodtest"
	"git.shino.boo/devilreef/cadence/device/internal/capability"
	"git.shino.boo/devilreef/cadence/device/internal/group"
	"git.shino.boo/devilreef/cadence/pkg/capschema"
)

// GroupConfig follows Ready after the level, with the key as 32 bytes, and audiod's GroupStatus becomes
// media.group's state
func TestGroupConfigAndStatusThroughAudiod(t *testing.T) {
	g := group.New(nil)
	g.SetDeviceID("dev-self")
	cfg := `{"enabled":true,"groupId":"g-1","role":"leader","latencyMs":1000,"members":["member-1"]}`
	key := map[string]string{capschema.MediaGroupKeySecret: strings.Repeat("ab", 32)}
	if err := g.Capability().(capability.SecretApplier).ApplySecrets(t.Context(), 1, json.RawMessage(cfg), key); err != nil {
		t.Fatal(err)
	}
	h := startSupervisorWith(t, 40, audiod.Timings{BackoffMin: 50 * time.Millisecond},
		[]audiod.SupervisorOption{audiod.WithAirplay(&handlers{}), audiod.WithSoloist(&handlers{}), audiod.WithGroup(g)},
		"--group", "playing")
	want := `enabled=true group="g-1" role=GROUP_ROLE_LEADER member="dev-self" host="" port=7701 key_bytes=32 ` +
		`channel=GROUP_CHANNEL_UNSPECIFIED latency=1000 trim=0 budget=1000 bitrate=0 pcm=false members=["member-1"] wait=0`
	h.waitRecord(t, 1, "group_config", want)
	if got := afterConfigure(audiodtest.Records(h.dir), 1); len(got) < 2 || got[0] != "volume_set 40" ||
		got[len(got)-1] != "group_config "+want {
		t.Fatalf("run 1 after configure: %q", got)
	}
	deadline := time.After(10 * time.Second)
	for {
		select {
		case v := <-g.Capability().State():
			var st capschema.MediaGroupStatus
			if json.Unmarshal(v, &st) != nil {
				t.Fatalf("state %s", v)
			}
			if st.State == "playing" {
				if st.GroupID != "g-1" || st.Role != "leader" || !st.Synced || st.ClockErrorUs != 42 || len(st.Members) != 1 ||
					st.Members[0].DeviceID != "member-1" {
					t.Fatalf("state %s", v)
				}
				return
			}
		case <-deadline:
			t.Fatalf("no playing state; records %v", audiodtest.Records(h.dir))
		}
	}
}
