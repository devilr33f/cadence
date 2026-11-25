// Package devices stores what each device can do and keeps its config in sync.
package devices

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"google.golang.org/protobuf/encoding/protojson"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/pkg/wire"
	"git.shino.boo/devilreef/cadence/server/internal/events"
	"git.shino.boo/devilreef/cadence/server/internal/gateway"
	"git.shino.boo/devilreef/cadence/server/internal/secrets"
	"git.shino.boo/devilreef/cadence/server/internal/store/db"
)

var ErrUnknownCapability = errors.New("unknown capability")

// ErrConfigTooLarge: the config would not fit in one ConfigSet frame.
var ErrConfigTooLarge = errors.New("config too large")

type Sender interface {
	Send(deviceID string, env *cadencev2.Envelope) error
	Online(deviceID string) bool
	// Remove ends a deleted device's link and refuses its later ones
	Remove(deviceID string)
}

// Store is the database: queries, and transactions for a rename or a delete;
// *pgxpool.Pool is one.
type Store interface {
	db.DBTX
	Begin(ctx context.Context) (pgx.Tx, error)
}

// linkDBTimeout bounds each store call made for a device's link: its reader
// holds the device's lifecycle lock meanwhile, and a newer link waits on it.
const linkDBTimeout = 3 * time.Second

type Service struct {
	store     Store
	q         *db.Queries
	hub       Sender
	box       *secrets.Box
	log       *slog.Logger
	dbTimeout time.Duration
	now       func() time.Time
	// bus hears the devices topic's events; nil publishes none
	bus  events.Publisher
	pres presence

	mu         sync.Mutex
	assemblies map[string]*assembly   // per device, the report arriving in parts
	pushLocks  map[string]*sync.Mutex // per device, held while its configs are numbered and sent
	changed    []func()
	states     []func(deviceID, capability string, state json.RawMessage)
	// groupRefs, when set, names each device's multiroom group in its detail
	groupRefs GroupRefs
}

func NewService(store Store, hub Sender, box *secrets.Box, log *slog.Logger) *Service {
	return &Service{store: store, q: db.New(store), hub: hub, box: box, log: log, dbTimeout: linkDBTimeout, now: time.Now,
		assemblies: map[string]*assembly{}, pushLocks: map[string]*sync.Mutex{},
		pres: presence{known: map[string]named{}, gone: map[string]struct{}{}}}
}

func parseID(s string) (pgtype.UUID, bool) {
	var id pgtype.UUID
	return id, id.Scan(s) == nil
}

// linkCtx is the context of one store call made for a device's link.
func (s *Service) linkCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, s.dbTimeout)
}

// Hello answers a device's hello: it asks for the full report when the stored
// hash is missing or differs, else it pushes the configs the device lacks.
func (s *Service) Hello(ctx context.Context, deviceID string, h *cadencev2.Hello) *cadencev2.Welcome {
	s.dropAssembly(deviceID)
	id, ok := parseID(deviceID)
	if !ok {
		return &cadencev2.Welcome{SendFullReport: true}
	}
	cctx, cancel := s.linkCtx(ctx)
	stored, err := s.q.GetCapabilitiesHash(cctx, id)
	cancel()
	if err != nil {
		s.log.Error("hello_failed", "device_id", deviceID)
		return &cadencev2.Welcome{SendFullReport: true}
	}
	if len(stored) == 0 || !bytes.Equal(stored, h.GetCapabilitiesHash()) {
		return &cadencev2.Welcome{SendFullReport: true}
	}
	s.pushPending(ctx, deviceID, id)
	return &cadencev2.Welcome{}
}

// Handle consumes the capability messages and reports whether env was one.
func (s *Service) Handle(ctx context.Context, deviceID string, env *cadencev2.Envelope) bool {
	switch b := env.GetBody().(type) {
	case *cadencev2.Envelope_CapabilityReport:
		s.report(ctx, deviceID, b.CapabilityReport)
	case *cadencev2.Envelope_ConfigApplied:
		s.applied(ctx, deviceID, b.ConfigApplied)
	case *cadencev2.Envelope_StateDelta:
		s.state(ctx, deviceID, b.StateDelta)
	default:
		return false
	}
	return true
}

// Closed forgets what the device's ended link left half sent.
func (s *Service) Closed(deviceID string) { s.dropAssembly(deviceID) }

func (s *Service) pushPending(ctx context.Context, deviceID string, id pgtype.UUID) {
	defer s.lockPush(id.String())()
	cctx, cancel := s.linkCtx(ctx)
	rows, err := s.q.PendingConfigs(cctx, id)
	cancel()
	if err != nil {
		s.log.Error("config_push_failed", "device_id", deviceID, "reason", "store")
		return
	}
	for _, r := range rows {
		s.push(ctx, deviceID, id, r.Capability, r.DesiredGen, r.Desired)
	}
}

// push sends the config with all the capability's stored secrets: the device
// clears the ones a ConfigSet lacks, so one that cannot be read stops the send.
func (s *Service) push(ctx context.Context, deviceID string, id pgtype.UUID, capability string, gen int64, cfg []byte) {
	sec, err := s.loadSecrets(ctx, id, capability, "")
	if err != nil {
		s.log.Error("config_push_failed", "device_id", deviceID, "capability", capability, "reason", "secrets")
		return
	}
	var compact bytes.Buffer
	if len(cfg) == 0 {
		cfg = []byte("{}")
	}
	// jsonb renders with extra spaces; compact form is what SetConfig size-checked
	if err := json.Compact(&compact, cfg); err != nil {
		s.log.Warn("config_push_failed", "device_id", deviceID, "capability", capability, "reason", "send_failed")
		return
	}
	err = s.hub.Send(deviceID, &cadencev2.Envelope{Body: &cadencev2.Envelope_ConfigSet{
		ConfigSet: &cadencev2.ConfigSet{Capability: capability, Generation: gen, Json: compact.String(), Secrets: sec},
	}})
	if err == nil {
		return
	}
	reason := "send_failed"
	switch {
	case errors.Is(err, gateway.ErrOffline):
		// pending stays pending: the next hello pushes it
		reason = "offline"
	case errors.Is(err, gateway.ErrBackpressure):
		reason = "backpressure"
	case errors.Is(err, wire.ErrFrameTooLarge):
		reason = "too_large"
	}
	s.log.Warn("config_push_failed", "device_id", deviceID, "capability", capability, "reason", reason)
}

func (s *Service) applied(ctx context.Context, deviceID string, a *cadencev2.ConfigApplied) {
	id, ok := parseID(deviceID)
	if !ok {
		return
	}
	cctx, cancel := s.linkCtx(ctx)
	n, err := s.q.SetApplied(cctx, db.SetAppliedParams{
		DeviceID: id, Capability: a.GetCapability(), Generation: a.GetGeneration(), AppliedError: a.GetErrorCode(),
	})
	cancel()
	if err != nil {
		s.log.Error("config_applied_failed", "device_id", deviceID)
		return
	}
	if n == 0 {
		s.log.Warn("config_applied_ignored", "device_id", deviceID, "capability", a.GetCapability(),
			"generation", a.GetGeneration())
	}
}

func (s *Service) state(ctx context.Context, deviceID string, d *cadencev2.StateDelta) {
	id, ok := parseID(deviceID)
	if !ok {
		return
	}
	drop := func(event string) {
		s.log.Warn(event, "device_id", deviceID, "capability", d.GetCapability())
	}
	cctx, cancel := s.linkCtx(ctx)
	row, err := s.q.GetCapability(cctx, db.GetCapabilityParams{DeviceID: id, Capability: d.GetCapability()})
	cancel()
	if errors.Is(err, pgx.ErrNoRows) {
		drop("state_unknown_capability")
		return
	}
	if err != nil {
		s.log.Error("state_failed", "device_id", deviceID)
		return
	}
	available := !d.GetUnavailable()
	// an empty json is availability alone: the device had no state to send with it
	if d.GetJson() == "" {
		s.setAvailable(ctx, deviceID, id, d.GetCapability(), row, available)
		return
	}
	sch, err := schemaOf(row.Descriptor, false)
	if err != nil {
		s.log.Error("state_failed", "device_id", deviceID)
		return
	}
	if _, ok := validateJSON(sch, []byte(d.GetJson())); !ok {
		drop("state_invalid")
		return
	}
	cctx, cancel = s.linkCtx(ctx)
	n, err := s.q.SetCapabilityState(cctx, db.SetCapabilityStateParams{
		DeviceID: id, Capability: d.GetCapability(), State: []byte(d.GetJson()), Available: available,
	})
	cancel()
	if err != nil {
		s.log.Error("state_failed", "device_id", deviceID)
		return
	}
	if n > 0 && d.GetCapability() == outputCapability {
		s.publishVolume(id.String(), row.State, []byte(d.GetJson()))
	}
	if n > 0 && row.Available != available {
		s.log.Info("capability_availability", "device_id", deviceID, "capability", d.GetCapability(), "available", available)
	}
	if n > 0 && (!sameJSON(row.State, []byte(d.GetJson())) || row.Available != available) {
		s.stateChanged(id.String(), d.GetCapability(), []byte(d.GetJson()))
	}
}

// setAvailable stores a capability's availability alone; its state stays as it was
func (s *Service) setAvailable(ctx context.Context, deviceID string, id pgtype.UUID, capability string, row db.GetCapabilityRow, available bool) {
	if row.Available == available {
		return
	}
	cctx, cancel := s.linkCtx(ctx)
	n, err := s.q.SetCapabilityAvailable(cctx, db.SetCapabilityAvailableParams{DeviceID: id, Capability: capability, Available: available})
	cancel()
	if err != nil {
		s.log.Error("state_failed", "device_id", deviceID)
		return
	}
	if n > 0 {
		s.log.Info("capability_availability", "device_id", deviceID, "capability", capability, "available", available)
		s.stateChanged(id.String(), capability, row.State)
	}
}

// SetConfig validates cfg against the capability's config schema, stores it
// as the next generation and pushes it when the device is online.
func (s *Service) SetConfig(ctx context.Context, deviceID, capability string, cfg json.RawMessage) (int64, error) {
	id, ok := parseID(deviceID)
	if !ok {
		return 0, ErrUnknownCapability
	}
	cctx, cancel := s.linkCtx(ctx)
	row, err := s.q.GetCapability(cctx, db.GetCapabilityParams{DeviceID: id, Capability: capability})
	cancel()
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrUnknownCapability
	}
	if err != nil {
		return 0, err
	}
	if err := checkConfig(row.Descriptor, cfg); err != nil {
		return 0, err
	}
	// the hub keys links by the canonical spelling, which the path id need not be
	dev := id.String()
	defer s.lockPush(dev)()
	have, err := s.loadSecrets(ctx, id, capability, "")
	if err != nil {
		return 0, err
	}
	if ok, err := fits(capability, cfg, have); err != nil {
		return 0, err
	} else if !ok {
		return 0, ErrConfigTooLarge
	}
	cctx, cancel = s.linkCtx(ctx)
	gen, err := s.q.SetDesiredConfig(cctx, db.SetDesiredConfigParams{DeviceID: id, Capability: capability, Desired: cfg})
	cancel()
	if err != nil {
		return 0, err
	}
	if s.hub.Online(dev) {
		s.push(ctx, dev, id, capability, gen, cfg)
	}
	return gen, nil
}

// checkConfig validates cfg, a JSON object, against the descriptor's config schema
func checkConfig(descriptor []byte, cfg json.RawMessage) error {
	sch, err := schemaOf(descriptor, true)
	if err != nil {
		return err
	}
	if t := bytes.TrimSpace(cfg); len(t) == 0 || t[0] != '{' {
		return &ConfigInvalidError{}
	}
	if path, ok := validateJSON(sch, cfg); !ok {
		return &ConfigInvalidError{Path: path}
	}
	if !json.Valid(cfg) {
		return &ConfigInvalidError{}
	}
	return nil
}

func schemaOf(descriptor []byte, config bool) (*jsonschema.Schema, error) {
	var d cadencev2.Descriptor
	if err := protojson.Unmarshal(descriptor, &d); err != nil {
		return nil, err
	}
	if config {
		return compileSchema(d.GetConfigSchema())
	}
	return compileSchema(d.GetStateSchema())
}

// Desired returns the config last set for the capability, nil when none was;
// ErrUnknownCapability when the device does not report it.
func (s *Service) Desired(ctx context.Context, deviceID, capability string) (json.RawMessage, error) {
	id, ok := parseID(deviceID)
	if !ok {
		return nil, ErrUnknownCapability
	}
	cctx, cancel := s.linkCtx(ctx)
	row, err := s.q.GetCapability(cctx, db.GetCapabilityParams{DeviceID: id, Capability: capability})
	cancel()
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrUnknownCapability
	}
	if err != nil {
		return nil, err
	}
	return row.Desired, nil
}

// State returns the last state the device reported for the capability; none while the device says the capability's
// hardware is absent (an unplugged screen), so every caller that asks for a screen's state finds none.
func (s *Service) State(ctx context.Context, deviceID, capability string) (json.RawMessage, bool) {
	id, ok := parseID(deviceID)
	if !ok {
		return nil, false
	}
	row, err := s.q.GetCapability(ctx, db.GetCapabilityParams{DeviceID: id, Capability: capability})
	if err != nil || len(row.State) == 0 || !row.Available {
		return nil, false
	}
	return row.State, true
}

// lockPush serializes numbering and sending a device's configs: the hub keeps
// send order, so generations only ever rise on a link. Taken after the
// gateway's lifecycle lock, never before it.
func (s *Service) lockPush(dev string) func() {
	s.mu.Lock()
	m := s.pushLocks[dev]
	if m == nil {
		m = &sync.Mutex{}
		s.pushLocks[dev] = m
	}
	s.mu.Unlock()
	m.Lock()
	return m.Unlock
}

// OnChange calls fn after each rename and delete; fn must not block.
func (s *Service) OnChange(fn func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.changed = append(s.changed, fn)
}

// OnState calls fn after each reported state that differs from the stored one; fn must not block.
func (s *Service) OnState(fn func(deviceID, capability string, state json.RawMessage)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.states = append(s.states, fn)
}

func (s *Service) stateChanged(dev, capability string, state json.RawMessage) {
	s.mu.Lock()
	fns := slices.Clone(s.states)
	s.mu.Unlock()
	for _, fn := range fns {
		fn(dev, capability, state)
	}
}

func (s *Service) notify() {
	s.mu.Lock()
	changed := slices.Clone(s.changed)
	s.mu.Unlock()
	for _, fn := range changed {
		fn()
	}
}

// ConfigKeys names the keys the config schema the device reported for the capability declares under properties, so a
// caller can leave out keys an older device would refuse; ErrUnknownCapability when the device does not report it.
func (s *Service) ConfigKeys(ctx context.Context, deviceID, capability string) ([]string, error) {
	id, ok := parseID(deviceID)
	if !ok {
		return nil, ErrUnknownCapability
	}
	cctx, cancel := s.linkCtx(ctx)
	row, err := s.q.GetCapability(cctx, db.GetCapabilityParams{DeviceID: id, Capability: capability})
	cancel()
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrUnknownCapability
	}
	if err != nil {
		return nil, err
	}
	var d cadencev2.Descriptor
	if err := protojson.Unmarshal(row.Descriptor, &d); err != nil {
		return nil, err
	}
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal([]byte(d.GetConfigSchema()), &schema); err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(schema.Properties))
	for k := range schema.Properties {
		keys = append(keys, k)
	}
	return keys, nil
}
