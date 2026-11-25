package group_test

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"google.golang.org/protobuf/proto"

	"git.shino.boo/devilreef/cadence/device/internal/capability"
	"git.shino.boo/devilreef/cadence/device/internal/group"
	"git.shino.boo/devilreef/cadence/pkg/capschema"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
)

var (
	keyHex = strings.Repeat("0123456789abcdef", 4)
	secret = map[string]string{capschema.MediaGroupKeySecret: keyHex}
)

const (
	leaderCfg = `{"enabled":true,"groupId":"g-1","role":"leader","port":7702,"latencyMs":1000,"members":["m-1","m-2"],` +
		`"channel":"left","trimMs":-20,"budgetUs":100,"bitrateKbps":320,"pcm":true,"waitMs":2000}`
	memberCfg = `{"enabled":true,"groupId":"g-1","role":"member","leader":{"host":"192.168.1.20","port":7701},"latencyMs":800}`
)

type rig struct {
	g   *group.Group
	cap capability.Provider
	log *bytes.Buffer

	mu   sync.Mutex
	sent []*audiodv1.GroupConfig
}

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{log: &bytes.Buffer{}}
	r.g = group.New(slog.New(slog.NewJSONHandler(&lockedWriter{w: r.log}, nil)))
	r.cap = r.g.Capability()
	r.g.SetDeviceID("dev-self")
	return r
}

type lockedWriter struct {
	mu sync.Mutex
	w  *bytes.Buffer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

func (r *rig) send(env *audiodv1.Envelope) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if g := env.GetGroupConfig(); g != nil {
		r.sent = append(r.sent, g)
	}
	return nil
}

func (r *rig) configs() []*audiodv1.GroupConfig {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*audiodv1.GroupConfig(nil), r.sent...)
}

func (r *rig) apply(t *testing.T, cfg string, secrets map[string]string) error {
	t.Helper()
	return r.cap.(capability.SecretApplier).ApplySecrets(t.Context(), 1, json.RawMessage(cfg), secrets)
}

func (r *rig) state(t *testing.T) string {
	t.Helper()
	select {
	case v := <-r.cap.State():
		return string(v)
	case <-time.After(time.Second):
		t.Fatal("no state")
		return ""
	}
}

func compile(t *testing.T, src string) *jsonschema.Schema {
	t.Helper()
	doc, err := jsonschema.UnmarshalJSON(strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	if err := c.AddResource("s.json", doc); err != nil {
		t.Fatal(err)
	}
	s, err := c.Compile("s.json")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func validate(t *testing.T, s *jsonschema.Schema, doc string) {
	t.Helper()
	v, err := jsonschema.UnmarshalJSON(strings.NewReader(doc))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Validate(v); err != nil {
		t.Fatalf("%s: %v", doc, err)
	}
}

func TestDescriptorIsMediaGroup(t *testing.T) {
	d := newRig(t).cap.Descriptor()
	if d.ID != capschema.MediaGroupID || d.ConfigSchema != capschema.MediaGroupConfig || d.StateSchema != capschema.MediaGroupState {
		t.Fatalf("descriptor = %+v", d)
	}
	if len(d.Secrets) != 1 || d.Secrets[0].Name != capschema.MediaGroupKeySecret {
		t.Fatalf("secrets = %+v", d.Secrets)
	}
	if _, ok := newRig(t).cap.(capability.DeviceIdentified); !ok {
		t.Fatal("the provider takes no device id")
	}
}

// every config the device takes is one the schema takes too; what it refuses is refused with a code alone
func TestConfigIsCheckedStrictly(t *testing.T) {
	schema := compile(t, capschema.MediaGroupConfig)
	r := newRig(t)
	for _, cfg := range []string{
		`{"enabled":false}`, leaderCfg, memberCfg, `{"enabled":false,"groupId":"g"}`,
		`{"enabled":true,"groupId":"g","role":"leader","latencyMs":200}`,
		`{"enabled":true,"groupId":"g","role":"leader","latencyMs":5000,"channel":"mono","trimMs":500}`,
		`{"enabled":true,"groupId":"g","role":"leader","latencyMs":1000,"clock":{"host":"192.168.1.30","port":7701}}`,
		`{"enabled":true,"groupId":"g","role":"member","latencyMs":1000,"leader":{"host":"h","port":7702},"clockMaster":true}`,
	} {
		validate(t, schema, cfg)
		if err := r.apply(t, cfg, secret); err != nil {
			t.Fatalf("%s: %v", cfg, err)
		}
	}
	for _, cfg := range []string{
		``, `[]`, `{}`, `{"enabled":"yes"}`, `{"enabled":true}`, `{"enabled":null}`, `{"enabled":true,"other":1}`,
		`{"enabled":true,"groupId":"g","role":"leader"}`,
		`{"enabled":true,"groupId":"","role":"leader","latencyMs":1000}`,
		`{"enabled":true,"groupId":"` + strings.Repeat("g", 65) + `","role":"leader","latencyMs":1000}`,
		`{"enabled":true,"groupId":"g","role":"boss","latencyMs":1000}`,
		`{"enabled":true,"groupId":"g","role":"member","latencyMs":1000}`,
		`{"enabled":true,"groupId":"g","role":"member","latencyMs":1000,"leader":{"host":"a b","port":7701}}`,
		`{"enabled":true,"groupId":"g","role":"member","latencyMs":1000,"leader":{"host":"h","port":0}}`,
		`{"enabled":true,"groupId":"g","role":"member","latencyMs":1000,"leader":{"host":"h"}}`,
		`{"enabled":true,"groupId":"g","role":"leader","latencyMs":199}`,
		`{"enabled":true,"groupId":"g","role":"leader","latencyMs":1000,"trimMs":501}`,
		`{"enabled":true,"groupId":"g","role":"leader","latencyMs":1000,"channel":"centre"}`,
		`{"enabled":true,"groupId":"g","role":"leader","latencyMs":1000,"bitrateKbps":32}`,
		`{"enabled":true,"groupId":"g","role":"leader","latencyMs":1000,"budgetUs":20001}`,
		`{"enabled":true,"groupId":"g","role":"leader","latencyMs":1000,"waitMs":10001}`,
		`{"enabled":true,"groupId":"g","role":"leader","latencyMs":1000,"port":70000}`,
		`{"enabled":true,"groupId":"g","role":"leader","latencyMs":1000.5}`,
		`{"enabled":true,"groupId":"g","role":"leader","latencyMs":1000,"members":["1","2","3","4","5","6","7","8","9"]}`,
		`{"enabled":true,"groupId":"g","role":"leader","latencyMs":1000,"leader":null}`,
		`{"enabled":true,"groupId":"g","role":"leader","latencyMs":1000,"clock":null}`,
		`{"enabled":true,"groupId":"g","role":"leader","latencyMs":1000,"clock":{"host":"a b","port":7701}}`,
		`{"enabled":true,"groupId":"g","role":"leader","latencyMs":1000,"clock":{"host":"h"}}`,
		`{"enabled":true,"groupId":"g","role":"leader","latencyMs":1000,"clockMaster":"yes"}`,
		// the clock answers on the group's port alone
		`{"enabled":true,"groupId":"g","role":"leader","latencyMs":1000,"clock":{"host":"h","port":7702}}`,
		`{"enabled":true,"groupId":"g","role":"member","latencyMs":1000,"leader":{"host":"h","port":7702},` +
			`"clock":{"host":"k","port":7701}}`,
		`{"enabled":false} {}`,
	} {
		var ae *capability.ApplyError
		if err := r.apply(t, cfg, secret); !errors.As(err, &ae) || ae.Code != "invalid" {
			t.Fatalf("%q: err = %v", cfg, err)
		}
	}
}

// an enabled config needs the group key, 64 lowercase hex; one that is off needs none
func TestKeyValidation(t *testing.T) {
	r := newRig(t)
	for _, c := range []struct {
		secrets map[string]string
		code    string
	}{
		{nil, "key_missing"},
		{map[string]string{capschema.MediaGroupKeySecret: ""}, "key_missing"},
		{map[string]string{"other": keyHex}, "key_missing"},
		{map[string]string{capschema.MediaGroupKeySecret: strings.ToUpper(keyHex)}, "key_invalid"},
		{map[string]string{capschema.MediaGroupKeySecret: keyHex[:62]}, "key_invalid"},
		{map[string]string{capschema.MediaGroupKeySecret: keyHex + "00"}, "key_invalid"},
		{map[string]string{capschema.MediaGroupKeySecret: keyHex[:63] + "g"}, "key_invalid"},
		{map[string]string{capschema.MediaGroupKeySecret: keyHex + "\n"}, "key_invalid"},
	} {
		var ae *capability.ApplyError
		err := r.apply(t, leaderCfg, c.secrets)
		if !errors.As(err, &ae) || ae.Code != c.code {
			t.Fatalf("%v: err = %v, want %s", c.secrets, err, c.code)
		}
		if strings.Contains(err.Error(), keyHex[:16]) {
			t.Fatalf("the error carries the key: %v", err)
		}
	}
	if err := r.apply(t, `{"enabled":false}`, nil); err != nil {
		t.Fatalf("off without a key: %v", err)
	}
}

func TestOutputSelectsTrimAndRepublishesRoute(t *testing.T) {
	r := newRig(t)
	hdmi := capschema.MediaGroupOutput{ID: "hdmi", Kind: capschema.GroupOutputHDMI}
	r.g.SetOutput(hdmi)
	state := r.state(t)
	if !strings.Contains(state, `"output":{"id":"hdmi","kind":"hdmi"}`) {
		t.Fatalf("output state = %s", state)
	}
	validate(t, compile(t, capschema.MediaGroupState), state)
	cfg := `{"enabled":true,"groupId":"g-1","role":"leader","latencyMs":1000,"trimUs":21000,` +
		`"trims":[{"outputId":"hdmi","trimUs":12345},{"outputId":"bt:aa:bb:cc:dd:ee:10","trimUs":-50000}]}`
	validate(t, compile(t, capschema.MediaGroupConfig), cfg)
	if err := r.apply(t, cfg, secret); err != nil {
		t.Fatal(err)
	}
	r.g.GroupReady(r.send)
	got := r.configs()
	if len(got) != 1 || got[0].GetTrimUs() != 12345 || got[0].GetTrimMs() != 12 {
		t.Fatalf("hdmi trim = %v", got)
	}

	hint := 145000
	r.g.SetOutput(capschema.MediaGroupOutput{ID: "bt:aa:bb:cc:dd:ee:10", Kind: capschema.GroupOutputBluetooth, DelayHintUs: &hint})
	got = r.configs()
	if len(got) != 2 || got[1].GetTrimUs() != -50000 || got[1].GetTrimMs() != -50 ||
		got[1].GetOutputKind() != capschema.GroupOutputBluetooth {
		t.Fatalf("bluetooth trim = %v", got)
	}
	// a route change is a trim-only audiod config plus its live output kind: the links' settings stay unchanged
	before := proto.Clone(got[0]).(*audiodv1.GroupConfig)
	after := proto.Clone(got[1]).(*audiodv1.GroupConfig)
	before.TrimMs, before.TrimUs, after.TrimMs, after.TrimUs = 0, 0, 0, 0
	before.OutputKind, after.OutputKind = "", ""
	if !proto.Equal(before, after) {
		t.Fatalf("route reconfigured the group: %v -> %v", got[0], got[1])
	}
	if got := r.state(t); !strings.Contains(got, `"delayHintUs":145000`) {
		t.Fatalf("bluetooth state = %s", got)
	}

	r.g.SetOutput(capschema.MediaGroupOutput{ID: "other", Kind: capschema.GroupOutputOther})
	got = r.configs()
	if len(got) != 3 || got[2].GetTrimUs() != 21000 || got[2].GetTrimMs() != 0 {
		t.Fatalf("fallback trim = %v", got)
	}
}

func TestGroupConfigGoesToAudiod(t *testing.T) {
	key, _ := hex.DecodeString(keyHex)
	r := newRig(t)
	if err := r.apply(t, leaderCfg, secret); err != nil {
		t.Fatal(err)
	}
	r.g.GroupReady(r.send)
	want := &audiodv1.GroupConfig{
		Enabled: true, GroupId: "g-1", Role: audiodv1.GroupRole_GROUP_ROLE_LEADER, MemberId: "dev-self", Port: 7702, Key: key,
		Channel: audiodv1.GroupChannel_GROUP_CHANNEL_LEFT, LatencyMs: 1000, TrimMs: -20, BudgetUs: 100, BitrateKbps: 320,
		CodecPcm: true, Members: []string{"m-1", "m-2"}, WaitMs: 2000,
	}
	if got := r.configs(); len(got) != 1 || !proto.Equal(got[0], want) {
		t.Fatalf("sent %v\nwant %v", got, want)
	}

	// a member dials its leader at its port; the defaults fill in what the server left out
	if err := r.apply(t, memberCfg, secret); err != nil {
		t.Fatal(err)
	}
	want = &audiodv1.GroupConfig{
		Enabled: true, GroupId: "g-1", Role: audiodv1.GroupRole_GROUP_ROLE_MEMBER, MemberId: "dev-self", LeaderHost: "192.168.1.20",
		Port: 7701, Key: key, LatencyMs: 800, BudgetUs: 1000,
	}
	if got := r.configs(); len(got) != 2 || !proto.Equal(got[1], want) {
		t.Fatalf("sent %v\nwant %v", got, want)
	}
	// the same config again sends nothing; a new key does
	if err := r.apply(t, memberCfg, secret); err != nil {
		t.Fatal(err)
	}
	if got := r.configs(); len(got) != 2 {
		t.Fatalf("an unchanged config went out again: %v", got)
	}
	other := strings.Repeat("f", 64)
	if err := r.apply(t, memberCfg, map[string]string{capschema.MediaGroupKeySecret: other}); err != nil {
		t.Fatal(err)
	}
	if got := r.configs(); len(got) != 3 || got[2].GetKey()[0] != 0xff {
		t.Fatalf("a new key did not go out: %v", got)
	}
	// a refused config leaves the one in force
	if err := r.apply(t, memberCfg, nil); err == nil {
		t.Fatal("no key taken")
	}
	if got := r.configs(); len(got) != 3 {
		t.Fatalf("a refused config went out: %v", got)
	}
	// out of the group: enabled false alone, no key
	if err := r.apply(t, `{"enabled":false,"groupId":"g-1"}`, secret); err != nil {
		t.Fatal(err)
	}
	if got := r.configs(); len(got) != 4 || !proto.Equal(got[3], &audiodv1.GroupConfig{}) {
		t.Fatalf("disabled sent %v", got)
	}
	out := r.log.String()
	for _, leak := range []string{keyHex[:16], other[:16], "192.168.1.20"} {
		if strings.Contains(out, leak) {
			t.Fatalf("the log carries %q: %s", leak, out)
		}
	}
	if !strings.Contains(out, `"msg":"group_config_sent"`) {
		t.Fatalf("log: %s", out)
	}
}

// the clock master and the stream leader as two devices: a leader and a member dial the master's address, the master
// serves the clock beside its member's stream; no clock and no clockMaster is the one device that does both
func TestClockSplitGoesToAudiod(t *testing.T) {
	key, _ := hex.DecodeString(keyHex)
	r := newRig(t)
	r.g.GroupReady(r.send)
	base := len(r.configs())
	for i, c := range []struct {
		cfg  string
		want *audiodv1.GroupConfig
	}{{
		`{"enabled":true,"groupId":"g-1","role":"leader","latencyMs":1000,"members":["m-1"],"clock":{"host":"192.168.1.30","port":7701}}`,
		&audiodv1.GroupConfig{Enabled: true, GroupId: "g-1", Role: audiodv1.GroupRole_GROUP_ROLE_LEADER, MemberId: "dev-self",
			Port: 7701, Key: key, LatencyMs: 1000, BudgetUs: 1000, Members: []string{"m-1"}, ClockHost: "192.168.1.30"},
	}, {
		`{"enabled":true,"groupId":"g-1","role":"member","leader":{"host":"192.168.1.20","port":7701},"latencyMs":800,` +
			`"clock":{"host":"192.168.1.30","port":7701}}`,
		&audiodv1.GroupConfig{Enabled: true, GroupId: "g-1", Role: audiodv1.GroupRole_GROUP_ROLE_MEMBER, MemberId: "dev-self",
			LeaderHost: "192.168.1.20", Port: 7701, Key: key, LatencyMs: 800, BudgetUs: 1000, ClockHost: "192.168.1.30"},
	}, {
		// the master itself: it serves, whatever clock says
		`{"enabled":true,"groupId":"g-1","role":"member","leader":{"host":"192.168.1.20","port":7701},"latencyMs":800,` +
			`"clockMaster":true,"clock":{"host":"192.168.1.30","port":7701}}`,
		&audiodv1.GroupConfig{Enabled: true, GroupId: "g-1", Role: audiodv1.GroupRole_GROUP_ROLE_MEMBER, MemberId: "dev-self",
			LeaderHost: "192.168.1.20", Port: 7701, Key: key, LatencyMs: 800, BudgetUs: 1000, ClockMaster: true},
	}} {
		if err := r.apply(t, c.cfg, secret); err != nil {
			t.Fatalf("%d: %v", i, err)
		}
		if got := r.configs(); len(got) != base+i+1 || !proto.Equal(got[base+i], c.want) {
			t.Fatalf("%d: sent %v\nwant %v", i, got, c.want)
		}
	}
	if out := r.log.String(); strings.Contains(out, "192.168.1.30") {
		t.Fatalf("the log carries the clock's address: %s", out)
	}
}

// audiod forgets the config with its client: every Ready gets it again, and a config applied while audiod is down
// waits for the next
func TestConfigFollowsEveryReady(t *testing.T) {
	r := newRig(t)
	r.g.GroupReady(r.send)
	if got := r.configs(); len(got) != 1 || got[0].GetEnabled() {
		t.Fatalf("first ready sent %v", got)
	}
	r.g.GroupGone()
	if err := r.apply(t, memberCfg, secret); err != nil {
		t.Fatal(err)
	}
	if got := r.configs(); len(got) != 1 {
		t.Fatalf("sent while audiod was down: %v", got)
	}
	r.g.GroupReady(r.send)
	r.g.GroupGone()
	r.g.GroupReady(r.send)
	got := r.configs()
	if len(got) != 3 || !got[1].GetEnabled() || !proto.Equal(got[1], got[2]) || got[2].GetMemberId() != "dev-self" {
		t.Fatalf("readies sent %v", got)
	}
	// a new device id is the member's new name
	r.g.SetDeviceID("dev-other")
	if got := r.configs(); len(got) != 4 || got[3].GetMemberId() != "dev-other" {
		t.Fatalf("id change sent %v", r.configs())
	}
}

func TestStatusBecomesState(t *testing.T) {
	schema := compile(t, capschema.MediaGroupState)
	r := newRig(t)
	if got := r.state(t); got != `{"state":"off","synced":false,"clockErrorUs":0,"queueMs":0,"steerPpm":0,"rejoins":0,"underruns":0,"placementErrorUs":0,"latencyMs":0,"tracking":{"state":"off","members":[]}}` {
		t.Fatalf("first state = %s", got)
	}
	if err := r.apply(t, leaderCfg, secret); err != nil {
		t.Fatal(err)
	}
	got := r.state(t)
	validate(t, schema, got)
	var st capschema.MediaGroupStatus
	if json.Unmarshal([]byte(got), &st) != nil || st.State != "connecting" || st.GroupID != "g-1" || st.Role != "leader" {
		t.Fatalf("before audiod = %s", got)
	}
	r.g.GroupReady(r.send)
	status := func(gs *audiodv1.GroupStatus) {
		r.g.GroupEnvelope(&audiodv1.Envelope{Body: &audiodv1.Envelope_GroupStatus{GroupStatus: gs}})
	}
	status(&audiodv1.GroupStatus{
		GroupId: "g-1", Role: audiodv1.GroupRole_GROUP_ROLE_LEADER, State: audiodv1.GroupState_GROUP_STATE_PLAYING, Synced: true,
		ClockErrorUs: 12, QueueMs: 900, SteerPpm: -30, Rejoins: 2, Underruns: 1, PlacementErrorUs: -40, LatencyMs: 1500,
		BitrateKbps: 192, WaitMs: 1100, Error: "Not A Token: 10.0.0.1",
		Members: []*audiodv1.GroupMemberStatus{
			{MemberId: "m-1", State: audiodv1.GroupState_GROUP_STATE_REJOINING, Connected: true, ClockErrorUs: 300, QueueMs: 10, Rejoins: 3, PlacementErrorUs: 7},
			{MemberId: "", State: audiodv1.GroupState_GROUP_STATE_PLAYING},
			{MemberId: "m-2", State: audiodv1.GroupState(42)},
		},
	})
	got = r.state(t)
	validate(t, schema, got)
	want := capschema.MediaGroupStatus{
		GroupID: "g-1", Role: "leader", State: "playing", Synced: true, ClockErrorUs: 12, QueueMs: 900, SteerPpm: -30, Rejoins: 2,
		Underruns: 1, PlacementErrorUs: -40, LatencyMs: 1500, BitrateKbps: 192, WaitMs: 1100,
		Members: []capschema.MediaGroupMemberStatus{
			{DeviceID: "m-1", State: "rejoining", Connected: true, ClockErrorUs: 300, QueueMs: 10, Rejoins: 3, PlacementErrorUs: 7},
			{DeviceID: "m-2", State: "connecting"},
		},
		Tracking: capschema.MediaGroupTracking{State: "off", Members: []capschema.MediaGroupTrackingMember{}},
	}
	if b, _ := json.Marshal(want); string(b) != got {
		t.Fatalf("state = %s\nwant %s", got, b)
	}
	status(&audiodv1.GroupStatus{GroupId: "g-1", Role: audiodv1.GroupRole_GROUP_ROLE_LEADER, State: audiodv1.GroupState_GROUP_STATE_FAILED, Error: "bind_failed"})
	if got := r.state(t); !strings.Contains(got, `"state":"failed","error":"bind_failed"`) {
		t.Fatalf("failed state = %s", got)
	} else {
		validate(t, schema, got)
	}
	// a status of another membership is not this one's
	if err := r.apply(t, memberCfg, secret); err != nil {
		t.Fatal(err)
	}
	if got := r.state(t); !strings.Contains(got, `"role":"member","state":"connecting"`) {
		t.Fatalf("after a new role = %s", got)
	}
	// audiod gone: connecting again until it reports
	status(&audiodv1.GroupStatus{GroupId: "g-1", Role: audiodv1.GroupRole_GROUP_ROLE_MEMBER, State: audiodv1.GroupState_GROUP_STATE_SYNCING})
	if got := r.state(t); !strings.Contains(got, `"state":"syncing"`) {
		t.Fatalf("member state = %s", got)
	}
	r.g.GroupGone()
	if got := r.state(t); !strings.Contains(got, `"state":"connecting"`) {
		t.Fatalf("after gone = %s", got)
	}
	if err := r.apply(t, `{"enabled":false}`, nil); err != nil {
		t.Fatal(err)
	}
	if got := r.state(t); !strings.HasPrefix(got, `{"state":"off"`) {
		t.Fatalf("off = %s", got)
	}
	if strings.Contains(r.log.String(), "10.0.0.1") {
		t.Fatalf("log: %s", r.log.String())
	}
}
