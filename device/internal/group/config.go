package group

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"regexp"

	"git.shino.boo/devilreef/cadence/pkg/capschema"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
)

// the codes an apply may fail with: never a config value or the key
const (
	codeInvalid    = "invalid"
	codeKeyMissing = "key_missing"
	codeKeyInvalid = "key_invalid"
)

const (
	keyBytes   = 32
	defaultBud = capschema.GroupBudgetRoomUs
)

var (
	// a lan address or a host name: nothing audiod could read as anything else
	hostChars = regexp.MustCompile(`^[A-Za-z0-9.:-]{1,64}$`)
	keyChars  = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// parseConfig decodes media.group's config strictly: one object, no unknown field, no null, every bound the schema
// sets. a config that is off needs nothing else, and what else it carries is dropped
func parseConfig(raw json.RawMessage) (capschema.MediaGroup, bool) {
	var fields map[string]json.RawMessage
	if decodeObject(raw, &fields) != nil {
		return capschema.MediaGroup{}, false
	}
	for _, v := range fields {
		if bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			return capschema.MediaGroup{}, false
		}
	}
	var c capschema.MediaGroup
	if decodeObject(raw, &c) != nil || fields["enabled"] == nil {
		return capschema.MediaGroup{}, false
	}
	if !c.Enabled {
		return capschema.MediaGroup{}, true
	}
	return c, valid(c)
}

func valid(c capschema.MediaGroup) bool {
	if n := len(c.GroupID); n < 1 || n > 64 {
		return false
	}
	switch c.Role {
	case capschema.GroupRoleLeader:
	case capschema.GroupRoleMember:
		if c.Leader == nil {
			return false
		}
	default:
		return false
	}
	if l := c.Leader; l != nil && (!hostChars.MatchString(l.Host) || l.Port < 1 || l.Port > 65535) {
		return false
	}
	// the clock master answers on the group's port number: a clock on another port is a config audiod cannot run
	if k := c.Clock; k != nil && (!hostChars.MatchString(k.Host) || k.Port != groupPort(c)) {
		return false
	}
	if _, ok := channels[c.Channel]; !ok {
		return false
	}
	if len(c.Members) > capschema.GroupMaxMembers {
		return false
	}
	for _, m := range c.Members {
		if len(m) < 1 || len(m) > 64 {
			return false
		}
	}
	if c.Tracking != nil {
		if len(c.Tracking.Members) > capschema.GroupMaxMembers {
			return false
		}
		seen := map[string]bool{}
		for _, m := range c.Tracking.Members {
			if len(m) < 1 || len(m) > 64 || seen[m] {
				return false
			}
			seen[m] = true
		}
	}
	seenOutputs := map[string]bool{}
	if len(c.Trims) > 16 {
		return false
	}
	for _, t := range c.Trims {
		if len(t.OutputID) < 1 || len(t.OutputID) > 64 || seenOutputs[t.OutputID] ||
			!within(t.TrimUs, capschema.GroupTrimMinUs, capschema.GroupTrimMaxUs) {
			return false
		}
		seenOutputs[t.OutputID] = true
	}
	return within(c.Port, 0, 65535) &&
		within(c.LatencyMs, capschema.GroupLatencyMinMs, capschema.GroupLatencyMaxMs) &&
		within(c.TrimMs, capschema.GroupTrimMinMs, capschema.GroupTrimMaxMs) &&
		within(c.TrimUs, capschema.GroupTrimMinUs, capschema.GroupTrimMaxUs) &&
		(c.BudgetUs == 0 || within(c.BudgetUs, 50, 20000)) &&
		(c.BitrateKbps == 0 || within(c.BitrateKbps, capschema.GroupBitrateMinKbps, capschema.GroupBitrateMaxKbps)) &&
		within(c.WaitMs, 0, 10000)
}

func within(v, lo, hi int) bool { return v >= lo && v <= hi }

var channels = map[string]audiodv1.GroupChannel{
	"": audiodv1.GroupChannel_GROUP_CHANNEL_UNSPECIFIED, capschema.GroupChannelBoth: audiodv1.GroupChannel_GROUP_CHANNEL_UNSPECIFIED,
	capschema.GroupChannelLeft: audiodv1.GroupChannel_GROUP_CHANNEL_LEFT, capschema.GroupChannelRight: audiodv1.GroupChannel_GROUP_CHANNEL_RIGHT,
	capschema.GroupChannelMono: audiodv1.GroupChannel_GROUP_CHANNEL_MONO,
}

var roles = map[string]audiodv1.GroupRole{
	capschema.GroupRoleLeader: audiodv1.GroupRole_GROUP_ROLE_LEADER,
	capschema.GroupRoleMember: audiodv1.GroupRole_GROUP_ROLE_MEMBER,
}

// parseKey is the group key as audiod takes it: 64 lowercase hex characters, 32 bytes. the problem is a code alone
func parseKey(secrets map[string]string) ([]byte, string) {
	raw, ok := secrets[capschema.MediaGroupKeySecret]
	if !ok || raw == "" {
		return nil, codeKeyMissing
	}
	if !keyChars.MatchString(raw) {
		return nil, codeKeyInvalid
	}
	key, err := hex.DecodeString(raw)
	if err != nil || len(key) != keyBytes {
		return nil, codeKeyInvalid
	}
	return key, ""
}

// groupConfig is what audiod gets for c, with member as this device's id and output its active output
func groupConfig(c capschema.MediaGroup, member string, key []byte, output capschema.MediaGroupOutput) *audiodv1.GroupConfig {
	if !c.Enabled {
		return &audiodv1.GroupConfig{}
	}
	port := groupPort(c)
	budget := c.BudgetUs
	if budget == 0 {
		budget = defaultBud
	}
	trimMs, trimUs := c.TrimMs, c.TrimUs
	for _, t := range c.Trims {
		if t.OutputID == output.ID {
			trimUs = t.TrimUs
			trimMs = outputTrimMs(trimUs)
			break
		}
	}
	g := &audiodv1.GroupConfig{
		Enabled: true, GroupId: c.GroupID, Role: roles[c.Role], MemberId: member,
		Port: uint32(port), Key: bytes.Clone(key), Channel: channels[c.Channel], //nolint:gosec // bounded by valid
		LatencyMs: uint32(c.LatencyMs), TrimMs: int32(trimMs), BudgetUs: uint32(budget), //nolint:gosec // bounded by valid
		BitrateKbps: uint32(c.BitrateKbps), CodecPcm: c.PCM, WaitMs: uint32(c.WaitMs), //nolint:gosec // bounded by valid
		TrimUs:     int32(trimUs), //nolint:gosec // bounded by valid
		OutputKind: output.Kind,
	}
	if c.Role == capschema.GroupRoleLeader {
		g.AutoLatency, g.AutoBitrate, g.AutoWait = c.AutoLatency, c.AutoBitrate, c.AutoWait
		if c.Tracking != nil {
			g.TrackingMembers = append([]string(nil), c.Tracking.Members...)
		}
	}
	if c.Tracking != nil {
		g.TrackingEnabled = c.Tracking.Enabled
	}
	if c.Role == capschema.GroupRoleMember {
		g.LeaderHost = c.Leader.Host
	} else {
		g.Members = append([]string(nil), c.Members...)
	}
	g.ClockMaster = c.ClockMaster
	if c.Clock != nil && !c.ClockMaster {
		g.ClockHost = c.Clock.Host
	}
	return g
}

func outputTrimMs(us int) int {
	if us < 0 {
		return -((-us + 500) / 1000)
	}
	return (us + 500) / 1000
}

// groupPort is the port the group's stream and clock use: a leader's own, a member's leader's, 7701 when unset
func groupPort(c capschema.MediaGroup) int {
	port := c.Port
	if c.Role == capschema.GroupRoleMember && c.Leader != nil {
		port = c.Leader.Port
	}
	if port == 0 {
		port = capschema.GroupDefaultPort
	}
	return port
}

// decodeObject decodes one JSON object into v and refuses unknown fields and anything after it
func decodeObject(raw []byte, v any) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		return errors.New("not an object")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing data")
	}
	return nil
}
