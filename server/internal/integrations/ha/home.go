package ha

import (
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"
)

// StaleAfter is how long after a disconnect the last view is still served.
const StaleAfter = 10 * time.Minute

// relistQuiet is how long the re-list worker waits after an event before it
// lists, so a burst of registry events (home assistant's startup) is one pass.
const relistQuiet = time.Second

// a failed re-list is tried again after relistRetryFirst, the wait doubling
// up to relistRetryMost while it keeps failing
const (
	relistRetryFirst = time.Second
	relistRetryMost  = 30 * time.Second
)

type HomeOptions struct {
	// Now nil is time.Now
	Now func() time.Time
	// Log nil is slog's default
	Log *slog.Logger
}

// View is an immutable snapshot of the home; never modify what it holds.
type View struct {
	Entities []Entity // sorted by id
	Areas    []Area   // sorted by id
	// Services maps domain -> service name -> what it takes
	Services map[string]map[string]Service
	// Fresh is true while the connection that loaded the view is up
	Fresh bool
}

// ByID finds an entity of the view.
func (v View) ByID(id string) (Entity, bool) {
	i, ok := slices.BinarySearchFunc(v.Entities, id, func(e Entity, id string) int { return strings.Compare(e.ID, id) })
	if !ok {
		return Entity{}, false
	}
	return v.Entities[i], true
}

// relist is a set of things to list again.
type relist uint8

const (
	relistServices relist = 1 << iota
	relistAreas
	relistDevices
	relistEntities
)

// Home is a live view of Home Assistant: every connection loads the states,
// the services and the area, device and entity registries, then state_changed
// events keep the states current and registry and service events list their
// part again, off the read goroutine.
type Home struct {
	c   *Client
	now func() time.Time
	log *slog.Logger

	mu     sync.Mutex
	states map[string]entityState // nil until the first load
	reg    registries
	svcs   map[string]map[string]Service
	// cached is the view of the current data; nil once anything changed
	cached *View
	// changed is closed and replaced on every state change, waking Wait
	changed chan struct{}
	up      bool
	lostAt  time.Time
	// gen counts loads; a re-list started under an older one is dropped
	gen uint64
	// overlay holds the states that change while a load runs: the last one
	// per entity, nil for a removal; nil when no load runs
	overlay map[string]*entityState
	pending relist
	// entityAdds is set by an entity registry event that may name an id the
	// list lacks, since the latest entity list began
	entityAdds bool
	// kick wakes the current connection's re-list worker; nil without one
	kick chan struct{}
	// quiet is how long the re-list worker waits after a kick
	quiet time.Duration
	// a failed re-list is tried again after retryFirst, doubling up to retryMost
	retryFirst, retryMost time.Duration
}

// NewHome subscribes to the events it follows and loads on every connection
// of c; call it before c runs.
func NewHome(c *Client, o HomeOptions) *Home {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	h := &Home{
		c: c, now: o.Now, log: o.Log, changed: make(chan struct{}),
		quiet: relistQuiet, retryFirst: relistRetryFirst, retryMost: relistRetryMost,
	}
	c.Subscribe("state_changed", h.stateChanged)
	c.Subscribe("service_registered", h.want(relistServices))
	c.Subscribe("service_removed", h.want(relistServices))
	c.Subscribe("area_registry_updated", h.want(relistAreas))
	c.Subscribe("device_registry_updated", h.want(relistDevices))
	c.Subscribe("entity_registry_updated", h.entityRegistryUpdated)
	c.OnConnect(h.load)
	c.OnDisconnect(h.lost)
	return h
}

// Client is the connection the home follows, for service calls and status.
func (h *Home) Client() *Client { return h.c }

// View is the current snapshot, or unavailable when Home Assistant was never
// loaded or has been disconnected for more than StaleAfter.
func (h *Home) View() (View, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.availableLocked() {
		return View{}, fail(CodeUnavailable)
	}
	v := *h.viewLocked()
	v.Fresh = h.up
	return v, nil
}

// Wait returns as soon as the entity id is in the view and satisfies cond,
// or false with what it last saw once d passed or ctx ended.
func (h *Home) Wait(ctx context.Context, id string, cond func(Entity) bool, d time.Duration) (Entity, bool) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	for {
		h.mu.Lock()
		e, ok := h.entityLocked(id)
		changed := h.changed
		h.mu.Unlock()
		if ok && cond(e) {
			return e, true
		}
		select {
		case <-changed:
		case <-timer.C:
			return e, false
		case <-ctx.Done():
			return e, false
		}
	}
}

func (h *Home) availableLocked() bool {
	return h.states != nil && (h.up || h.now().Sub(h.lostAt) <= StaleAfter)
}

func (h *Home) entityLocked(id string) (Entity, bool) {
	if !h.availableLocked() {
		return Entity{}, false
	}
	s, ok := h.states[id]
	if !ok {
		return Entity{}, false
	}
	return h.reg.derive(s)
}

func (h *Home) viewLocked() *View {
	if h.cached != nil {
		return h.cached
	}
	v := View{Entities: make([]Entity, 0, len(h.states)), Areas: h.reg.areas, Services: h.svcs}
	for _, s := range h.states {
		if e, ok := h.reg.derive(s); ok {
			v.Entities = append(v.Entities, e)
		}
	}
	slices.SortFunc(v.Entities, func(a, b Entity) int { return strings.Compare(a.ID, b.ID) })
	h.cached = &v
	return h.cached
}

// changedLocked drops the cached view and wakes every Wait.
func (h *Home) changedLocked() {
	h.cached = nil
	close(h.changed)
	h.changed = make(chan struct{})
}

// stateChanged runs on the read goroutine.
func (h *Home) stateChanged(data json.RawMessage) {
	id, s, removed, ok := parseStateChange(data)
	if !ok {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	switch {
	case h.overlay != nil:
		if _, seen := h.overlay[id]; !seen && len(h.overlay) >= maxEntities {
			return
		}
		if removed {
			h.overlay[id] = nil
		} else {
			h.overlay[id] = &s
		}
	case h.states != nil:
		if putState(h.states, id, s, removed) {
			h.changedLocked()
		}
	}
}

// putState applies one change; a new entity past the cap is left out. It
// reports whether states changed.
func putState(states map[string]entityState, id string, s entityState, removed bool) bool {
	_, known := states[id]
	switch {
	case removed:
		delete(states, id)
		return known
	case !known && len(states) >= maxEntities:
		return false
	}
	states[id] = s
	return true
}

// want marks part of the home to list again and wakes the worker; it runs on
// the read goroutine and never blocks.
func (h *Home) want(r relist) func(json.RawMessage) {
	return func(json.RawMessage) {
		h.mu.Lock()
		h.pending |= r
		h.kickLocked()
		h.mu.Unlock()
	}
}

// entityRegistryUpdated lists the entity registry again; an event that may
// name an entity id the list lacks also marks it behind until that list lands.
func (h *Home) entityRegistryUpdated(data json.RawMessage) {
	h.mu.Lock()
	h.pending |= relistEntities
	if mayAddEntity(data) {
		h.entityAdds = true
		h.behindLocked()
	}
	h.kickLocked()
	h.mu.Unlock()
}

// mayAddEntity says whether an entity_registry_updated event can name an
// entity id the cached list lacks: a create, an update that changed an id
// (old_entity_id), and one it cannot read. A removal or another update only
// changes or drops an entry the list has.
func mayAddEntity(data json.RawMessage) bool {
	var ev struct {
		Action      string  `json:"action"`
		EntityID    string  `json:"entity_id"`
		OldEntityID *string `json:"old_entity_id"`
	}
	if json.Unmarshal(data, &ev) != nil {
		return true
	}
	switch ev.Action {
	case "remove":
		return false
	case "update":
		return ev.OldEntityID != nil && *ev.OldEntityID != ev.EntityID
	}
	return true
}

// behindLocked marks the entity registry as possibly lacking entities that
// have a state, until a list of it lands: home assistant lists a new entity
// in its registry before its first state.
func (h *Home) behindLocked() {
	if !h.reg.entitiesBehind {
		h.reg.entitiesBehind = true
		h.changedLocked()
	}
}

func (h *Home) kickLocked() {
	select {
	case h.kick <- struct{}{}:
	default:
	}
}

func (h *Home) lost() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.up {
		h.up, h.lostAt = false, h.now()
		// a Watch sees the connection go
		h.changedLocked()
	}
	h.kick = nil
}

// Watch is the state of each of ids that has one while the connection is up
// (none while it is down or before its first load), with a channel closed at
// the next change, a disconnect included. The states are Home Assistant's
// text: never log them.
func (h *Home) Watch(ids []string) (up bool, states map[string]string, next <-chan struct{}) {
	h.mu.Lock()
	defer h.mu.Unlock()
	next = h.changed
	if !h.up || h.states == nil {
		return false, nil, next
	}
	states = map[string]string{}
	for _, id := range ids {
		if s, ok := h.states[id]; ok {
			states[id] = s.state
		}
	}
	return true, states, next
}

// load is the OnConnect hook: it fetches everything, swaps it in with the
// state changes that arrived meanwhile, and starts the connection's re-list
// worker. ctx ends with the connection.
func (h *Home) load(ctx context.Context) error {
	start := time.Now()
	h.mu.Lock()
	h.gen++
	gen := h.gen
	// what changed before these fetches is in their answers
	h.overlay, h.pending, h.kick, h.entityAdds = map[string]*entityState{}, 0, nil, false
	// the old registry may lack what was added while the connection was down;
	// a failed load leaves it so
	h.behindLocked()
	h.mu.Unlock()
	states, svcs, reg, err := h.fetchAll(ctx)
	h.mu.Lock()
	overlay := h.overlay
	h.overlay = nil
	if err != nil {
		// the old view may still be served: keep it as current as the events allow
		if h.states != nil && applyOverlay(h.states, overlay) {
			h.changedLocked()
		}
		h.mu.Unlock()
		return err
	}
	applyOverlay(states, overlay)
	h.states, h.svcs, h.reg, h.up = states, svcs, reg, true
	// an entity registry event during the load may name what its list missed
	h.reg.entitiesBehind = h.entityAdds
	h.changedLocked()
	// logged before the lock is let go, so whoever sees the new view sees the line
	h.log.Info("ha_loaded", "entities", len(h.viewLocked().Entities), "areas", len(reg.areas), "services", countServices(svcs),
		"ms", time.Since(start).Milliseconds())
	kick := make(chan struct{}, 1)
	h.kick = kick
	if h.pending != 0 {
		h.kickLocked()
	}
	h.mu.Unlock()
	go h.work(ctx, gen, kick)
	return nil
}

func (h *Home) fetchAll(ctx context.Context) (map[string]entityState, map[string]map[string]Service, registries, error) {
	var reg registries
	states, err := fetch(ctx, h, "states", "get_states", nil, parseStates)
	if err != nil {
		return nil, nil, reg, err
	}
	svcs, err := fetch(ctx, h, "services", "get_services", nil, parseServices)
	if err != nil {
		return nil, nil, reg, err
	}
	if reg.areas, err = fetch(ctx, h, "areas", "config/area_registry/list", nil, parseAreas); err != nil {
		return nil, nil, reg, err
	}
	if reg.devices, err = fetch(ctx, h, "devices", "config/device_registry/list", nil, parseDevices); err != nil {
		return nil, nil, reg, err
	}
	if reg.entities, err = h.fetchEntities(ctx, func(id string) bool { _, ok := states[id]; return ok }); err != nil {
		return nil, nil, reg, err
	}
	return states, svcs, reg, nil
}

// applyOverlay applies the state changes a load saw to states and reports
// whether any changed them.
func applyOverlay(states map[string]entityState, overlay map[string]*entityState) bool {
	changed := false
	for id, s := range overlay {
		if s == nil {
			changed = putState(states, id, entityState{}, true) || changed
		} else {
			changed = putState(states, id, *s, false) || changed
		}
	}
	return changed
}

// fetchEntities lists the entity registry, then asks for the aliases the list
// leaves out, of the entities withState says have a state. Aliases only help
// resolve names: when home assistant refuses them, the entities stay without.
func (h *Home) fetchEntities(ctx context.Context, withState func(id string) bool) (map[string]regEntry, error) {
	entities, err := fetch(ctx, h, "entities", "config/entity_registry/list", nil, parseEntityRegistry)
	if err != nil {
		return nil, err
	}
	ids := aliasIDs(entities, withState)
	if len(ids) == 0 {
		return entities, nil
	}
	_, _ = fetch(ctx, h, "aliases", "config/entity_registry/get_entries", map[string]any{"entity_ids": ids},
		func(raw json.RawMessage) (struct{}, error) { return struct{}{}, parseAliases(raw, entities) })
	return entities, nil
}

// stateIDs reports whether an entity has a state now.
func (h *Home) stateIDs() func(id string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	ids := make(map[string]bool, len(h.states))
	for id := range h.states {
		ids[id] = true
	}
	return func(id string) bool { return ids[id] }
}

// fetch runs one command and parses its result; a failure is logged with
// what it was for, unless the connection ended: its ctx, or unavailable,
// which only a dropped connection answers and the client reports itself.
func fetch[T any](ctx context.Context, h *Home, what, typ string, fields map[string]any, parse func(json.RawMessage) (T, error)) (T, error) {
	var zero T
	raw, err := h.c.Command(ctx, typ, fields)
	if err == nil {
		var v T
		if v, err = parse(raw); err == nil {
			return v, nil
		}
	}
	if code := codeOf(err); ctx.Err() == nil && code != CodeUnavailable {
		h.log.Warn("ha_reload_failed", "what", what, "code", code)
	}
	return zero, err
}

// codeOf is err's code; an error that is no *Error is unavailable.
func codeOf(err error) string {
	if code := Code(err); code != "" {
		return code
	}
	return CodeUnavailable
}

// work lists again what the events asked for, one part at a time, until the
// connection ends. It waits quiet after an event, so events that arrive
// meanwhile, or during a pass, coalesce into one pass. A part that failed is
// listed again after a backoff, or with the next event's pass.
func (h *Home) work(ctx context.Context, gen uint64, kick <-chan struct{}) {
	// retry is nil while the last pass failed nothing
	var retry <-chan time.Time
	var delay time.Duration
	for {
		select {
		case <-ctx.Done():
			return
		case <-retry:
		case <-kick:
			quiet := time.NewTimer(h.quiet)
			select {
			case <-ctx.Done():
				quiet.Stop()
				return
			case <-quiet.C:
			}
		}
		// the events of the quiet period are in this pass
		select {
		case <-kick:
		default:
		}
		h.mu.Lock()
		if h.gen != gen {
			h.mu.Unlock()
			return
		}
		todo := h.pending
		h.pending = 0
		if todo&relistEntities != 0 {
			// the list this pass begins covers the events before it
			h.entityAdds = false
		}
		h.mu.Unlock()
		failed := h.pass(ctx, gen, todo)
		if failed == 0 {
			retry, delay = nil, 0
			continue
		}
		h.mu.Lock()
		if h.gen == gen {
			h.pending |= failed
		}
		h.mu.Unlock()
		delay = h.nextRetry(delay)
		retry = time.After(delay)
	}
}

// nextRetry is the wait before a failed re-list is tried again, after prev
// (0 for its first failure).
func (h *Home) nextRetry(prev time.Duration) time.Duration {
	if prev == 0 {
		return h.retryFirst
	}
	return min(2*prev, h.retryMost)
}

// pass lists the parts todo names and swaps in each that lands; it returns
// the parts that failed.
func (h *Home) pass(ctx context.Context, gen uint64, todo relist) (failed relist) {
	if todo&relistServices != 0 {
		if svcs, err := fetch(ctx, h, "services", "get_services", nil, parseServices); err == nil {
			h.apply(gen, func() { h.svcs = svcs })
		} else {
			failed |= relistServices
		}
	}
	if todo&relistAreas != 0 {
		if areas, err := fetch(ctx, h, "areas", "config/area_registry/list", nil, parseAreas); err == nil {
			h.apply(gen, func() { h.reg.areas = areas })
		} else {
			failed |= relistAreas
		}
	}
	if todo&relistDevices != 0 {
		if devices, err := fetch(ctx, h, "devices", "config/device_registry/list", nil, parseDevices); err == nil {
			h.apply(gen, func() { h.reg.devices = devices })
		} else {
			failed |= relistDevices
		}
	}
	if todo&relistEntities != 0 {
		if entities, err := h.fetchEntities(ctx, h.stateIDs()); err == nil {
			h.apply(gen, func() {
				h.reg.entities = entities
				// an event since the list began may name what it missed
				h.reg.entitiesBehind = h.entityAdds
			})
		} else {
			failed |= relistEntities
		}
	}
	return failed
}

// apply swaps in a re-list's result unless a newer load replaced the data.
func (h *Home) apply(gen uint64, set func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.gen == gen {
		set()
		h.changedLocked()
	}
}

func countServices(svcs map[string]map[string]Service) int {
	n := 0
	for _, s := range svcs {
		n += len(s)
	}
	return n
}
