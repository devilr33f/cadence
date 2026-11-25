package devices

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/encoding/protojson"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

var ErrNotFound = errors.New("device not found")

type CapabilityView struct {
	ID           string          `json:"id"`
	Version      int32           `json:"version"`
	Desired      json.RawMessage `json:"desired"`
	DesiredGen   int64           `json:"desiredGen"`
	AppliedGen   int64           `json:"appliedGen"`
	AppliedError string          `json:"appliedError"`
	State        json.RawMessage `json:"state"`
	Available    bool            `json:"available" doc:"false while the device says the capability's hardware is absent (a screen's hdmi sink unplugged): it stays reported and configured, its commands are refused"`
	ConfigSchema JSONSchema      `json:"configSchema" doc:"the JSON Schema a config is validated against, as the device reported it"`
	StateSchema  JSONSchema      `json:"stateSchema" doc:"the JSON Schema of the state the device reports"`
	Commands     []CommandView   `json:"commands" nullable:"false"`
	Secrets      []SecretView    `json:"secrets" nullable:"false"`
	SecretSpecs  []SecretSpec    `json:"secretSpecs" nullable:"false" doc:"the secrets the device declares, in its order"`
}

// CommandView is a command the capability takes, by name.
type CommandView struct {
	Name       string     `json:"name"`
	ArgsSchema JSONSchema `json:"argsSchema" doc:"the JSON Schema of its args"`
}

// SecretView says whether a declared secret has a value; never the value.
type SecretView struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Set         bool   `json:"set"`
}

// SecretSpec is a secret the device declares.
type SecretSpec struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// JSONSchema is a schema of a stored descriptor: a JSON object sent as the
// device wrote it, never generated again; null where it declared none, or one
// that is no object (a boolean schema).
type JSONSchema json.RawMessage

func (s JSONSchema) MarshalJSON() ([]byte, error) {
	if len(s) == 0 {
		return []byte("null"), nil
	}
	return s, nil
}

func (JSONSchema) Schema(huma.Registry) *huma.Schema {
	return &huma.Schema{Type: huma.TypeObject, Nullable: true}
}

// schemaOfSource is the stored schema src as a JSONSchema.
func schemaOfSource(src string) JSONSchema {
	b := bytes.TrimSpace([]byte(src))
	if len(b) == 0 || b[0] != '{' || !json.Valid(b) {
		return nil
	}
	return JSONSchema(b)
}

// GroupRef is the multiroom group a device is in, as the device list and detail show it.
type GroupRef struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Kind     string   `json:"kind" enum:"group,stereo_pair"`
	LeaderID string   `json:"leaderId" doc:"the member elected to lead now; empty while none can"`
	Members  []string `json:"members" nullable:"false" doc:"the members' device ids, in the group's order"`
}

type Detail struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Model      string     `json:"model"`
	Status     string     `json:"status"`
	Room       string     `json:"room" doc:"the room the device is in, the owner's name for it; empty for none"`
	Online     bool       `json:"online"`
	LastSeenAt *time.Time `json:"lastSeenAt"`
	// DaemonVersion is the build the device last linked with (hello), so the dashboard can say
	// what an update would replace
	DaemonVersion string           `json:"daemonVersion"`
	Capabilities  []CapabilityView `json:"capabilities"`
	Group         *GroupRef        `json:"group,omitempty" doc:"the multiroom group the device is in; none when in none"`
}

// GroupRefs answers each grouped device's group, by device id (the groups service's Refs).
type GroupRefs func(ctx context.Context) (map[string]GroupRef, error)

// ShowGroups has the device detail name each device's group; set it before the api serves.
func (s *Service) ShowGroups(refs GroupRefs) { s.groupRefs = refs }

// withGroup names the device's group in its detail; a failed read leaves it out
func (s *Service) withGroup(ctx context.Context, d Detail) Detail {
	if s == nil || s.groupRefs == nil {
		return d
	}
	if refs, err := s.groupRefs(ctx); err == nil {
		if g, ok := refs[d.ID]; ok {
			d.Group = &g
		}
	}
	return d
}

func (s *Service) Detail(ctx context.Context, deviceID string) (Detail, error) {
	id, ok := parseID(deviceID)
	if !ok {
		return Detail{}, ErrNotFound
	}
	d, err := s.q.DeviceByID(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Detail{}, ErrNotFound
	}
	if err != nil {
		return Detail{}, err
	}
	rows, err := s.q.ListCapabilities(ctx, id)
	if err != nil {
		return Detail{}, err
	}
	setNames, err := s.q.ListDeviceSecretNames(ctx, id)
	if err != nil {
		return Detail{}, err
	}
	isSet := map[[2]string]bool{}
	for _, r := range setNames {
		isSet[[2]string{r.Capability, r.Name}] = true
	}
	out := Detail{
		ID: d.ID.String(), Name: d.Name, Model: d.Model, Status: d.Status, Room: d.Room,
		Online: s.hub.Online(d.ID.String()), DaemonVersion: d.DaemonVersion,
		Capabilities: make([]CapabilityView, 0, len(rows)),
	}
	if d.LastSeenAt.Valid {
		t := d.LastSeenAt.Time
		out.LastSeenAt = &t
	}
	for _, r := range rows {
		v := CapabilityView{
			ID: r.Capability, Version: r.Version, Desired: r.Desired, DesiredGen: r.DesiredGen,
			AppliedGen: r.AppliedGen, AppliedError: r.AppliedError, State: r.State, Available: r.Available,
		}
		describe(&v, r.Descriptor, func(n string) bool { return isSet[[2]string{r.Capability, n}] })
		out.Capabilities = append(out.Capabilities, v)
	}
	return out, nil
}

// describe fills in what the stored descriptor declares: its schemas, its
// commands sorted by name, its secrets in declared order. One this server
// cannot read declares nothing.
func describe(v *CapabilityView, descriptor []byte, set func(string) bool) {
	v.Commands, v.Secrets, v.SecretSpecs = []CommandView{}, []SecretView{}, []SecretSpec{}
	var d cadencev2.Descriptor
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(descriptor, &d); err != nil {
		return
	}
	v.ConfigSchema, v.StateSchema = schemaOfSource(d.GetConfigSchema()), schemaOfSource(d.GetStateSchema())
	for _, name := range slices.Sorted(maps.Keys(d.GetCommands())) {
		v.Commands = append(v.Commands, CommandView{Name: name, ArgsSchema: schemaOfSource(d.GetCommands()[name])})
	}
	for _, sp := range d.GetSecrets() {
		v.Secrets = append(v.Secrets, SecretView{Name: sp.GetName(), Description: sp.GetDescription(), Set: set(sp.GetName())})
		v.SecretSpecs = append(v.SecretSpecs, SecretSpec{Name: sp.GetName(), Description: sp.GetDescription()})
	}
}
