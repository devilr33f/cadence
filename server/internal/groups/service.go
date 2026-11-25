package groups

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"git.shino.boo/devilreef/cadence/pkg/capschema"
	"git.shino.boo/devilreef/cadence/server/internal/devices"
	"git.shino.boo/devilreef/cadence/server/internal/events"
	"git.shino.boo/devilreef/cadence/server/internal/secrets"
	"git.shino.boo/devilreef/cadence/server/internal/store/db"
)

var (
	ErrNotFound = errors.New("groups: not found")
	// ErrNameTaken: another group has the name, in any case
	ErrNameTaken = errors.New("groups: name taken")
	// ErrStereoPair: a join cannot extend a stereo pair
	ErrStereoPair = errors.New("groups: a stereo pair takes no more members")
)

// InGroupError refuses a member that is in another group (409 in_group): the JSON pointer of its device id.
type InGroupError struct{ Path string }

func (e *InGroupError) Error() string { return "in_group" }

const (
	keyBytes = 32
	// bounds one recompute: it reads the store and may set a config on every device
	recomputeTimeout = 15 * time.Second
	nameIndex        = "groups_name_lower"
)

// Store is the database; *pgxpool.Pool is one.
type Store interface {
	db.DBTX
	Begin(ctx context.Context) (pgx.Tx, error)
}

// Devices is the devices service as the groups use it.
type Devices interface {
	// Ensure sets a capability's config and one secret as one generation, when either differs (devices.Service)
	Ensure(ctx context.Context, deviceID, capability string, cfg json.RawMessage, name, value string) (bool, error)
	// Desired answers devices.ErrUnknownCapability for a device that does not report the capability
	Desired(ctx context.Context, deviceID, capability string) (json.RawMessage, error)
	State(ctx context.Context, deviceID, capability string) (json.RawMessage, bool)
}

// Device is an enrolled device as the plan reads it.
type Device struct {
	ID, Name, Model, Room string
}

type Deps struct {
	Store   Store
	Box     *secrets.Box
	Devices Devices
	// List is every enrolled device
	List   func(ctx context.Context) ([]Device, error)
	Online func(deviceID string) bool
	// Bus hears group_changed and group_state; nil publishes none
	Bus events.Publisher
	// Commands reaches a group's leader for a calibration; nil refuses every calibration
	Commands Commands
	Log      *slog.Logger
	// Now is the clock a listener calibration expires by; nil is time.Now
	Now func() time.Time
}

type Service struct {
	d Deps
	q *db.Queries

	// recompute runs one at a time
	recomputing sync.Mutex

	mu sync.Mutex
	// each linked device's lan addresses, from its hello
	addrs map[string][]string
	// from the last recompute: the group each device is in, each group's leader (its stream source), clock master and
	// members, the active groups
	memberOf map[string]string
	leaders  map[string]string
	clocks   map[string]string
	members  map[string][]string
	active   map[string]bool
	// the groups whose screens follow a video (followVideo)
	follow map[string]bool
	poke   chan struct{}
	// each device's localSource as last seen, each group's stream source, and the recompute a grace or a hold waits for
	seen  map[string]sourceSeen
	leads map[string]lead
	wake  *time.Timer
	// the listener calibrations scheduled and not yet applied, by id
	pending map[string]pendingCalibration
	// each group's last elected leader tracking report and correction counters, in memory only
	tracking map[string]trackingSnapshot
}

func New(d Deps) *Service {
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	return &Service{d: d, q: db.New(d.Store), addrs: map[string][]string{}, memberOf: map[string]string{},
		leaders: map[string]string{}, clocks: map[string]string{}, members: map[string][]string{}, active: map[string]bool{}, poke: make(chan struct{}, 1),
		pending: map[string]pendingCalibration{}, seen: map[string]sourceSeen{}, leads: map[string]lead{}, tracking: map[string]trackingSnapshot{}}
}

func parseID(s string) (pgtype.UUID, bool) {
	var id pgtype.UUID
	return id, id.Scan(s) == nil
}

func keyAAD(groupID string) []byte { return []byte("group-key:" + groupID) }

func (s *Service) newKey(groupID string) ([]byte, error) {
	if s.d.Box == nil {
		return nil, errors.New("groups: no secrets box")
	}
	key := make([]byte, keyBytes)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	return s.d.Box.Seal(key, keyAAD(groupID))
}

func toGroup(r db.Group, ms []db.GroupMember) Group {
	g := Group{ID: r.ID.String(), Name: r.Name, Kind: r.Kind, LatencyMs: int(r.LatencyMs), BitrateKbps: int(r.BitrateKbps),
		WaitMs: int(r.WaitMs), PCM: r.Pcm, AutoLatency: r.AutoLatency, AutoBitrate: r.AutoBitrate, AutoWait: r.AutoWait,
		FollowVideo: r.FollowVideo, TrackingEnabled: r.TrackingEnabled, CreatedAt: r.CreatedAt.Time, UpdatedAt: r.UpdatedAt.Time}
	if r.PreferredLeader.Valid {
		g.PreferredLeader = r.PreferredLeader.String()
	}
	for _, m := range ms {
		g.Members = append(g.Members, Member{DeviceID: m.DeviceID.String(), Channel: m.Channel, TrimMs: int(m.TrimMs),
			TrimUs: int(m.TrimUs)})
	}
	return g
}

func outputTrims(rows []db.GroupOutputTrim) map[string][]OutputTrim {
	out := map[string][]OutputTrim{}
	for _, r := range rows {
		id := r.DeviceID.String()
		out[id] = append(out[id], OutputTrim{OutputID: r.OutputID, TrimUs: int(r.TrimUs), Ultrasonic: r.Ultrasonic})
	}
	return out
}

func withOutputTrims(g Group, trims map[string][]OutputTrim) Group {
	for i := range g.Members {
		g.Members[i].Trims = trims[g.Members[i].DeviceID]
	}
	return g
}

// List answers every group, in the order they were made.
func (s *Service) List(ctx context.Context) ([]Group, error) {
	out, _, err := s.list(ctx)
	return out, err
}

// list is List with each group's sealed key, by id
func (s *Service) list(ctx context.Context) ([]Group, map[string][]byte, error) {
	rows, err := s.q.ListGroups(ctx)
	if err != nil {
		return nil, nil, err
	}
	all, err := s.q.ListAllGroupMembers(ctx)
	if err != nil {
		return nil, nil, err
	}
	trimRows, err := s.q.ListAllGroupOutputTrims(ctx)
	if err != nil {
		return nil, nil, err
	}
	trims := outputTrims(trimRows)
	by := map[pgtype.UUID][]db.GroupMember{}
	for _, m := range all {
		by[m.GroupID] = append(by[m.GroupID], m)
	}
	out := make([]Group, 0, len(rows))
	sealed := make(map[string][]byte, len(rows))
	for _, r := range rows {
		out = append(out, withOutputTrims(toGroup(r, by[r.ID]), trims))
		sealed[r.ID.String()] = r.KeySealed
	}
	return out, sealed, nil
}

// Get answers one group, ErrNotFound for none.
func (s *Service) Get(ctx context.Context, groupID string) (Group, error) {
	id, ok := parseID(groupID)
	if !ok {
		return Group{}, ErrNotFound
	}
	r, err := s.q.GetGroup(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Group{}, ErrNotFound
	}
	if err != nil {
		return Group{}, err
	}
	ms, err := s.q.ListGroupMembers(ctx, id)
	if err != nil {
		return Group{}, err
	}
	trims, err := s.q.ListGroupOutputTrims(ctx, id)
	if err != nil {
		return Group{}, err
	}
	return withOutputTrims(toGroup(r, ms), outputTrims(trims)), nil
}

// Create stores a new group with a new key and recomputes the devices' configs.
func (s *Service) Create(ctx context.Context, spec Spec) (Group, error) {
	g, err := normalize(spec)
	if err != nil {
		return Group{}, err
	}
	id, err := newID()
	if err != nil {
		return Group{}, err
	}
	err = pgx.BeginFunc(ctx, s.d.Store, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		if err := checkMembers(ctx, q, g.Members, pgtype.UUID{}); err != nil {
			return err
		}
		sealed, err := s.newKey(id.String())
		if err != nil {
			return err
		}
		if err := q.CreateGroup(ctx, db.CreateGroupParams{ID: id, Name: g.Name, Kind: g.Kind,
			PreferredLeader: uuidOrNull(g.PreferredLeader), LatencyMs: small(g.LatencyMs), BitrateKbps: small(g.BitrateKbps),
			Pcm: g.PCM, WaitMs: small(g.WaitMs), KeySealed: sealed, AutoLatency: g.AutoLatency, AutoBitrate: g.AutoBitrate,
			AutoWait: g.AutoWait, FollowVideo: g.FollowVideo, TrackingEnabled: g.TrackingEnabled}); err != nil {
			return err
		}
		return addMembers(ctx, q, id, g.Members)
	})
	if err != nil {
		return Group{}, storeError(err)
	}
	s.log("group_created", id.String(), len(g.Members))
	return s.changed(ctx, id.String())
}

// Update replaces the group's config and members; a changed membership takes a new key.
func (s *Service) Update(ctx context.Context, groupID string, spec Spec) (Group, error) {
	id, ok := parseID(groupID)
	if !ok {
		return Group{}, ErrNotFound
	}
	g, err := normalize(spec)
	if err != nil {
		return Group{}, err
	}
	err = pgx.BeginFunc(ctx, s.d.Store, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		cur, err := q.LockGroup(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if err := checkMembers(ctx, q, g.Members, id); err != nil {
			return err
		}
		old, err := q.ListGroupMembers(ctx, id)
		if err != nil {
			return err
		}
		sealed := cur.KeySealed
		if !sameSet(memberIDs(toGroup(cur, old).Members), memberIDs(g.Members)) {
			if sealed, err = s.newKey(id.String()); err != nil {
				return err
			}
		}
		if _, err := q.UpdateGroup(ctx, db.UpdateGroupParams{ID: id, Name: g.Name, Kind: g.Kind,
			PreferredLeader: uuidOrNull(g.PreferredLeader), LatencyMs: small(g.LatencyMs), BitrateKbps: small(g.BitrateKbps),
			Pcm: g.PCM, WaitMs: small(g.WaitMs), KeySealed: sealed, AutoLatency: g.AutoLatency, AutoBitrate: g.AutoBitrate,
			AutoWait: g.AutoWait, FollowVideo: g.FollowVideo, TrackingEnabled: g.TrackingEnabled}); err != nil {
			return err
		}
		ids := make([]pgtype.UUID, len(g.Members))
		for i, m := range g.Members {
			ids[i], _ = parseID(m.DeviceID)
		}
		if err := q.DeleteGroupMembers(ctx, db.DeleteGroupMembersParams{GroupID: id, DeviceIds: ids}); err != nil {
			return err
		}
		return addMembers(ctx, q, id, g.Members)
	})
	if err != nil {
		return Group{}, storeError(err)
	}
	s.log("group_updated", id.String(), len(g.Members))
	return s.changed(ctx, id.String())
}

// Delete removes the group; its members are disabled by the recompute that follows.
func (s *Service) Delete(ctx context.Context, groupID string) error {
	id, ok := parseID(groupID)
	if !ok {
		return ErrNotFound
	}
	n, err := s.q.DeleteGroup(ctx, id)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	s.log("group_deleted", id.String(), 0)
	s.publishDeleted(id.String())
	s.Recompute(ctx)
	return nil
}

// Join makes deviceID lead a group with the listed devices (Home Assistant's join): its own group, extended, or a new
// one named after it; the listed devices leave the groups they were in, and a group left with fewer than two members
// (or a broken stereo pair) is deleted.
func (s *Service) Join(ctx context.Context, deviceID string, others []string) (Group, error) {
	lead, ok := parseID(deviceID)
	if !ok {
		return Group{}, devices.ErrNotFound
	}
	var add []pgtype.UUID
	for i, o := range others {
		id, ok := parseID(o)
		if !ok {
			return Group{}, &InvalidError{Code: "unknown_device", Path: "/body/members/" + strconv.Itoa(i)}
		}
		if id != lead && !slices.Contains(add, id) {
			add = append(add, id)
		}
	}
	if len(add) == 0 {
		return Group{}, invalid("/body/members")
	}
	var groupID pgtype.UUID
	var emptied []string
	err := pgx.BeginFunc(ctx, s.d.Store, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		leader, err := q.DeviceByID(ctx, lead)
		if errors.Is(err, pgx.ErrNoRows) {
			return devices.ErrNotFound
		}
		if err != nil {
			return err
		}
		for i, id := range add {
			if _, err := q.DeviceByID(ctx, id); errors.Is(err, pgx.ErrNoRows) {
				return &InvalidError{Code: "unknown_device", Path: "/body/members/" + strconv.Itoa(i)}
			} else if err != nil {
				return err
			}
		}
		own, err := q.GroupOfDevice(ctx, lead)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			groupID, err = s.createFor(ctx, q, lead, leader.Name)
			if err != nil {
				return err
			}
		case err != nil:
			return err
		default:
			groupID = own
		}
		left, err := s.extend(ctx, q, groupID, add)
		if err != nil {
			return err
		}
		if _, err := q.SetGroupPreferredLeader(ctx, db.SetGroupPreferredLeaderParams{ID: groupID, PreferredLeader: lead}); err != nil {
			return err
		}
		emptied, err = s.shrink(ctx, q, left)
		return err
	})
	if err != nil {
		return Group{}, storeError(err)
	}
	for _, id := range emptied {
		s.publishDeleted(id)
	}
	s.log("group_joined", groupID.String(), len(add))
	return s.changed(ctx, groupID.String())
}

// extend adds the devices not yet in the group to it, each leaving the group it was in (answered, for shrink), and
// gives the group a new key when any moved; a stereo pair takes no one more
func (s *Service) extend(ctx context.Context, q *db.Queries, groupID pgtype.UUID, add []pgtype.UUID) ([]db.GroupsOfDevicesRow, error) {
	g, err := q.LockGroup(ctx, groupID)
	if err != nil {
		return nil, err
	}
	cur, err := q.ListGroupMembers(ctx, groupID)
	if err != nil {
		return nil, err
	}
	var moving []pgtype.UUID
	for _, id := range add {
		if !slices.ContainsFunc(cur, func(m db.GroupMember) bool { return m.DeviceID == id }) {
			moving = append(moving, id)
		}
	}
	if g.Kind == KindStereoPair && len(moving) > 0 {
		return nil, ErrStereoPair
	}
	if len(cur)+len(moving) > capschema.GroupMaxMembers {
		return nil, invalid("/body/members")
	}
	left, err := q.GroupsOfDevices(ctx, moving)
	if err != nil {
		return nil, err
	}
	if err := q.RemoveGroupMembers(ctx, moving); err != nil {
		return nil, err
	}
	next := int32(0)
	for _, m := range cur {
		next = max(next, m.Position+1)
	}
	for _, id := range moving {
		if err := q.AddGroupMember(ctx, db.AddGroupMemberParams{GroupID: groupID, DeviceID: id,
			Channel: capschema.GroupChannelBoth, Position: next}); err != nil {
			return nil, err
		}
		next++
	}
	if len(moving) > 0 {
		sealed, err := s.newKey(groupID.String())
		if err != nil {
			return nil, err
		}
		if _, err := q.SetGroupKey(ctx, db.SetGroupKeyParams{ID: groupID, KeySealed: sealed}); err != nil {
			return nil, err
		}
	}
	return left, nil
}

// Attach adds deviceID to the group member is in (a screen's «play here too»), its preferred leader left as it is:
// deviceID leaves the group it was in, which shrinks as Join's do, and the group takes a new key. ErrNotFound when
// member is in no group, ErrStereoPair for a stereo pair, devices.ErrNotFound for an unknown device.
func (s *Service) Attach(ctx context.Context, member, deviceID string) (Group, error) {
	of, ok := parseID(member)
	if !ok {
		return Group{}, ErrNotFound
	}
	id, ok := parseID(deviceID)
	if !ok {
		return Group{}, devices.ErrNotFound
	}
	var groupID pgtype.UUID
	var emptied []string
	err := pgx.BeginFunc(ctx, s.d.Store, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		if _, err := q.DeviceByID(ctx, id); errors.Is(err, pgx.ErrNoRows) {
			return devices.ErrNotFound
		} else if err != nil {
			return err
		}
		var err error
		groupID, err = q.GroupOfDevice(ctx, of)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		left, err := s.extend(ctx, q, groupID, []pgtype.UUID{id})
		if err != nil {
			return err
		}
		emptied, err = s.shrink(ctx, q, left)
		return err
	})
	if err != nil {
		return Group{}, storeError(err)
	}
	for _, e := range emptied {
		s.publishDeleted(e)
	}
	s.log("group_attached", groupID.String(), 1)
	return s.changed(ctx, groupID.String())
}

// Leave takes the device out of its group; a group left with fewer than two members, or a broken stereo pair, is
// deleted. A device in no group leaves nothing.
func (s *Service) Leave(ctx context.Context, deviceID string) error {
	id, ok := parseID(deviceID)
	if !ok {
		return devices.ErrNotFound
	}
	var emptied []string
	var groupID pgtype.UUID
	err := pgx.BeginFunc(ctx, s.d.Store, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		if _, err := q.DeviceByID(ctx, id); errors.Is(err, pgx.ErrNoRows) {
			return devices.ErrNotFound
		} else if err != nil {
			return err
		}
		left, err := q.GroupsOfDevices(ctx, []pgtype.UUID{id})
		if err != nil || len(left) == 0 {
			return err
		}
		groupID = left[0].GroupID
		if err := q.RemoveGroupMembers(ctx, []pgtype.UUID{id}); err != nil {
			return err
		}
		emptied, err = s.shrink(ctx, q, left)
		return err
	})
	if err != nil {
		return storeError(err)
	}
	if !groupID.Valid {
		return nil
	}
	s.log("group_left", groupID.String(), 1)
	if slices.Contains(emptied, groupID.String()) {
		s.publishDeleted(groupID.String())
		s.Recompute(ctx)
		return nil
	}
	_, err = s.changed(ctx, groupID.String())
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

// shrink handles the groups members left: one with fewer than two members left, or a stereo pair that lost one, is
// deleted; any other takes a new key. It answers the deleted ones.
func (s *Service) shrink(ctx context.Context, q *db.Queries, left []db.GroupsOfDevicesRow) ([]string, error) {
	var done []pgtype.UUID
	var deleted []string
	for _, l := range left {
		if slices.Contains(done, l.GroupID) {
			continue
		}
		done = append(done, l.GroupID)
		g, err := q.LockGroup(ctx, l.GroupID)
		if err != nil {
			return nil, err
		}
		ms, err := q.ListGroupMembers(ctx, l.GroupID)
		if err != nil {
			return nil, err
		}
		if len(ms) < minMembers || g.Kind == KindStereoPair {
			if _, err := q.DeleteGroup(ctx, l.GroupID); err != nil {
				return nil, err
			}
			deleted = append(deleted, l.GroupID.String())
			continue
		}
		sealed, err := s.newKey(l.GroupID.String())
		if err != nil {
			return nil, err
		}
		if _, err := q.SetGroupKey(ctx, db.SetGroupKeyParams{ID: l.GroupID, KeySealed: sealed}); err != nil {
			return nil, err
		}
	}
	return deleted, nil
}

// createFor makes an empty group the device leads, named after it (a number added while the name is taken)
func (s *Service) createFor(ctx context.Context, q *db.Queries, lead pgtype.UUID, name string) (pgtype.UUID, error) {
	id, err := newID()
	if err != nil {
		return id, err
	}
	sealed, err := s.newKey(id.String())
	if err != nil {
		return id, err
	}
	taken, err := q.ListGroups(ctx)
	if err != nil {
		return id, err
	}
	candidate := name
	for n := 2; slices.ContainsFunc(taken, func(g db.Group) bool { return strings.EqualFold(g.Name, candidate) }); n++ {
		candidate = name + " " + strconv.Itoa(n)
	}
	if err := q.CreateGroup(ctx, db.CreateGroupParams{ID: id, Name: candidate, Kind: KindGroup, PreferredLeader: lead,
		LatencyMs: capschema.GroupLatencyDefaultMs, BitrateKbps: defaultBitrateKbps, WaitMs: defaultWaitMs, KeySealed: sealed,
		AutoLatency: true, AutoBitrate: true, AutoWait: true, FollowVideo: true, TrackingEnabled: false}); err != nil {
		return id, err
	}
	return id, q.AddGroupMember(ctx, db.AddGroupMemberParams{GroupID: id, DeviceID: lead, Channel: capschema.GroupChannelBoth})
}

// checkMembers refuses a member that is no device, or one in another group than self
func checkMembers(ctx context.Context, q *db.Queries, ms []Member, self pgtype.UUID) error {
	ids := make([]pgtype.UUID, len(ms))
	for i, m := range ms {
		ids[i], _ = parseID(m.DeviceID)
		if _, err := q.DeviceByID(ctx, ids[i]); errors.Is(err, pgx.ErrNoRows) {
			return &InvalidError{Code: "unknown_device", Path: "/body/members/" + strconv.Itoa(i) + "/deviceId"}
		} else if err != nil {
			return err
		}
	}
	rows, err := q.GroupsOfDevices(ctx, ids)
	if err != nil {
		return err
	}
	for _, r := range rows {
		if r.GroupID != self {
			i := slices.Index(ids, r.DeviceID)
			return &InGroupError{Path: "/body/members/" + strconv.Itoa(i) + "/deviceId"}
		}
	}
	return nil
}

func addMembers(ctx context.Context, q *db.Queries, id pgtype.UUID, ms []Member) error {
	for i, m := range ms {
		dev, _ := parseID(m.DeviceID)
		if err := q.AddGroupMember(ctx, db.AddGroupMemberParams{GroupID: id, DeviceID: dev, Channel: m.Channel,
			TrimMs: small(m.TrimMs), TrimUs: small(m.TrimUs), Position: small(i)}); err != nil {
			return err
		}
	}
	return nil
}

// storeError maps the store's refusals to the service's; a member another transaction put in a group meanwhile
// fails the unique index, answered as in_group
func storeError(err error) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" {
		switch pg.ConstraintName {
		case nameIndex:
			return ErrNameTaken
		case "group_members_device_id_key":
			return &InGroupError{Path: "/body/members"}
		}
	}
	return err
}

func uuidOrNull(s string) pgtype.UUID {
	id, _ := parseID(s)
	return id
}

func newID() (pgtype.UUID, error) {
	var id pgtype.UUID
	if _, err := rand.Read(id.Bytes[:]); err != nil {
		return id, err
	}
	id.Bytes[6] = id.Bytes[6]&0x0f | 0x40
	id.Bytes[8] = id.Bytes[8]&0x3f | 0x80
	id.Valid = true
	return id, nil
}

func sameSet(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

func (s *Service) log(event, groupID string, members int) {
	s.d.Log.Info(event, "group_id", groupID, "members", members)
}

// changed recomputes after a group's change and answers it, publishing group_changed
func (s *Service) changed(ctx context.Context, groupID string) (Group, error) {
	s.Recompute(ctx)
	g, err := s.Get(ctx, groupID)
	if err != nil {
		return Group{}, err
	}
	s.publishGroup(g)
	return g, nil
}

func (s *Service) publishGroup(g Group) {
	if s.d.Bus == nil {
		return
	}
	s.d.Bus.Publish(events.Event{Type: events.GroupChanged, At: time.Now(), Data: events.GroupData{
		ID: g.ID, Name: g.Name, Kind: g.Kind, LeaderID: s.Leader(g.ID), Members: memberIDs(g.Members),
	}})
}

func (s *Service) publishDeleted(groupID string) {
	if s.d.Bus == nil {
		return
	}
	s.d.Bus.Publish(events.Event{Type: events.GroupChanged, At: time.Now(),
		Data: events.GroupData{ID: groupID, Members: []string{}, Deleted: true}})
}

// small is a validated group setting as its int4 column: every one is checked against its bounds (a few thousand at
// most) before it is stored, and the column's own check refuses anything else
func small(v int) int32 {
	return int32(min(max(v, math.MinInt32), math.MaxInt32)) //nolint:gosec // clamped to the int32 range
}
