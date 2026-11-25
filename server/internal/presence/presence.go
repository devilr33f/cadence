// Package presence is busy mode: signals from outside (an API caller, Home
// Assistant, a voice request) that the owner is in a call, screensharing, in
// a meeting or does not want to be disturbed. While an unexpired signal
// covers a device, unasked speech there waits; alarms, timers, sessions the
// user began and command words never look at it.
package presence

import (
	"context"
	"errors"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"git.shino.boo/devilreef/cadence/server/internal/events"
	"git.shino.boo/devilreef/cadence/server/internal/store/db"
)

// the reasons a source may give, in the order Busy prefers them when several
// signals cover a device
const (
	ReasonCall        = "call"
	ReasonMeeting     = "meeting"
	ReasonScreenshare = "screenshare"
	ReasonDND         = "dnd"
)

// Reasons are every reason, the most pressing first.
var Reasons = []string{ReasonCall, ReasonMeeting, ReasonScreenshare, ReasonDND}

const (
	// MaxTTL bounds every stored signal: a source that stops sending lets the
	// device speak again within a day
	MaxTTL = 24 * time.Hour
	// MaxDevices bounds the devices one signal names
	MaxDevices = 64

	// SourceVoice is the do_not_disturb tool's source, SourceDashboard the
	// dashboard's toggle; haPrefix starts each Home Assistant entity's
	SourceVoice     = "voice"
	SourceDashboard = "dashboard"
	haPrefix        = "ha:"
)

// sourceRe is a source's name: an API caller's own, or one of the above.
var sourceRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._:-]{0,63}$`)

var (
	ErrInvalid  = errors.New("presence: invalid signal")
	ErrReserved = errors.New("presence: source reserved")
	ErrNotFound = errors.New("presence: no such signal")
)

// Signal is one source's claim that the owner is busy. Devices nil covers
// every device. ExpiresAt is zero only for a live signal (Home Assistant's),
// which lasts while its source holds it.
type Signal struct {
	Source    string
	Reason    string
	Devices   []string
	ExpiresAt time.Time
}

func (s Signal) covers(deviceID string) bool {
	return s.Devices == nil || slices.Contains(s.Devices, deviceID)
}

func (s Signal) live(now time.Time) bool { return s.ExpiresAt.IsZero() || s.ExpiresAt.After(now) }

// State is one device's busy state: the reason Busy gives, "" when not busy.
type State struct {
	DeviceID string
	Reason   string
}

// BusyChanged is the presence topic's event, with a BusyData.
const BusyChanged = events.BusyChanged

// BusyData is a BusyChanged's data.
type BusyData struct {
	Busy   bool   `json:"busy"`
	Reason string `json:"reason,omitempty"`
}

type Deps struct {
	Queries *db.Queries
	Bus     events.Publisher
	Log     *slog.Logger
	Now     func() time.Time
}

// Service keeps every signal in memory, the stored ones loaded at Run, and
// tells listeners of each device whose busy state changed.
type Service struct {
	d Deps

	// writes serializes each store write with its memory change, so the two
	// never disagree
	writes sync.Mutex

	mu sync.Mutex
	// stored are the API's and the voice's signals, as the store has them
	stored map[string]Signal
	// live are Home Assistant's, never stored
	live map[string]Signal
	// known are the devices whose state was last computed, with that state
	known     map[string]string
	listeners []func(deviceID string)
	// wake has Run's loop look at the next expiry again
	wake chan struct{}
}

func New(d Deps) *Service {
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	return &Service{
		d: d, stored: map[string]Signal{}, live: map[string]Signal{}, known: map[string]string{},
		wake: make(chan struct{}, 1),
	}
}

// OnChange calls fn with each device whose busy state (busy or not, and its
// reason) changed, a signal's expiry included; fn must not block. Register
// before Run.
func (s *Service) OnChange(fn func(deviceID string)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.listeners = append(s.listeners, fn)
}

// Busy reports whether a signal covers the device now, with the most
// pressing reason among them. It reads memory only.
func (s *Service) Busy(_ context.Context, deviceID string) (bool, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.reasonLocked(canonical(deviceID), s.d.Now())
	return r != "", r
}

func (s *Service) reasonLocked(deviceID string, now time.Time) string {
	best := -1
	for _, set := range []map[string]Signal{s.stored, s.live} {
		for _, sig := range set {
			if !sig.live(now) || !sig.covers(deviceID) {
				continue
			}
			if i := slices.Index(Reasons, sig.Reason); i >= 0 && (best < 0 || i < best) {
				best = i
			}
		}
	}
	if best < 0 {
		return ""
	}
	return Reasons[best]
}

// Signals are every signal in force, by source.
func (s *Service) Signals() []Signal {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.d.Now()
	var out []Signal
	for _, set := range []map[string]Signal{s.stored, s.live} {
		for _, sig := range set {
			if sig.live(now) {
				sig.Devices = slices.Clone(sig.Devices)
				out = append(out, sig)
			}
		}
	}
	slices.SortFunc(out, func(a, b Signal) int { return strings.Compare(a.Source, b.Source) })
	return out
}

// States are the busy devices among every enrolled one, by id.
func (s *Service) States(ctx context.Context) ([]State, error) {
	ids, err := s.deviceIDs(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.d.Now()
	out := []State{}
	for _, id := range ids {
		if r := s.reasonLocked(id, now); r != "" {
			out = append(out, State{DeviceID: id, Reason: r})
		}
	}
	return out, nil
}

// Put stores the source's signal for ttl (1 s to MaxTTL), replacing the one it
// had, and returns when it expires. devices nil covers every device; each one
// named must be enrolled. A Home Assistant source is ErrReserved.
func (s *Service) Put(ctx context.Context, source, reason string, devices []string, ttl time.Duration) (time.Time, error) {
	if strings.HasPrefix(source, haPrefix) {
		return time.Time{}, ErrReserved
	}
	if !sourceRe.MatchString(source) || !slices.Contains(Reasons, reason) || ttl < time.Second || ttl > MaxTTL {
		return time.Time{}, ErrInvalid
	}
	ids, err := s.checkDevices(ctx, devices)
	if err != nil {
		return time.Time{}, err
	}
	s.writes.Lock()
	defer s.writes.Unlock()
	now := s.d.Now()
	at := now.Add(ttl)
	params := db.PutBusySignalParams{
		Source: source, Reason: reason, ExpiresAt: ts(at), UpdatedAt: ts(now),
	}
	for _, id := range ids {
		params.Devices = append(params.Devices, pgtype.UUID{Bytes: uuid.MustParse(id), Valid: true})
	}
	if _, err := s.d.Queries.PutBusySignal(ctx, params); err != nil {
		return time.Time{}, err
	}
	s.d.Log.Info("busy_set", "source", logSource(source), "reason", reason, "devices", len(ids), "ttl_s", int(ttl.Seconds()))
	s.apply(ctx, func() { s.stored[source] = Signal{Source: source, Reason: reason, Devices: ids, ExpiresAt: at} })
	return at, nil
}

// Delete clears the source's signal; ErrNotFound when it had none.
func (s *Service) Delete(ctx context.Context, source string) error {
	if strings.HasPrefix(source, haPrefix) {
		return ErrReserved
	}
	if !sourceRe.MatchString(source) {
		return ErrInvalid
	}
	s.writes.Lock()
	defer s.writes.Unlock()
	n, err := s.d.Queries.DeleteBusySignal(ctx, source)
	if err != nil {
		return err
	}
	s.mu.Lock()
	_, had := s.stored[source]
	s.mu.Unlock()
	if n == 0 && !had {
		return ErrNotFound
	}
	s.d.Log.Info("busy_cleared", "source", logSource(source))
	s.apply(ctx, func() { delete(s.stored, source) })
	return nil
}

// setLive replaces every Home Assistant signal with sigs, keyed by source.
func (s *Service) setLive(ctx context.Context, sigs map[string]Signal) {
	s.mu.Lock()
	same := len(sigs) == len(s.live)
	for k, v := range sigs {
		if cur, ok := s.live[k]; !ok || cur.Reason != v.Reason || !slices.Equal(cur.Devices, v.Devices) {
			same = false
		}
	}
	s.mu.Unlock()
	if same {
		return
	}
	s.d.Log.Info("busy_ha_changed", "signals", len(sigs))
	s.apply(ctx, func() { s.live = sigs })
}

// checkDevices canonicalizes the ids and refuses one that is not enrolled, a
// repeat, an empty list or one past MaxDevices.
func (s *Service) checkDevices(ctx context.Context, devices []string) ([]string, error) {
	if devices == nil {
		return nil, nil
	}
	if len(devices) == 0 || len(devices) > MaxDevices {
		return nil, ErrInvalid
	}
	enrolled, err := s.deviceIDs(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(devices))
	for _, d := range devices {
		u, err := uuid.Parse(d)
		id := u.String()
		if err != nil || slices.Contains(out, id) || !slices.Contains(enrolled, id) {
			return nil, ErrInvalid
		}
		out = append(out, id)
	}
	slices.Sort(out)
	return out, nil
}

func (s *Service) deviceIDs(ctx context.Context) ([]string, error) {
	rows, err := s.d.Queries.BusyDeviceIDs(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = uuid.UUID(r.Bytes).String()
	}
	return out, nil
}

// apply changes the signals, then tells the listeners and the feed of every
// device whose state that changed. The devices looked at are the enrolled
// ones and any a signal names; with the device list unreadable, the ones
// known from before.
func (s *Service) apply(ctx context.Context, change func()) {
	ids, err := s.deviceIDs(ctx)
	if err != nil && ctx.Err() == nil {
		s.d.Log.Error("busy_failed", "op", "devices")
	}
	s.mu.Lock()
	change()
	now := s.d.Now()
	seen := map[string]bool{}
	for _, id := range ids {
		seen[id] = true
	}
	for id := range s.known {
		seen[id] = true
	}
	for _, set := range []map[string]Signal{s.stored, s.live} {
		for _, sig := range set {
			for _, id := range sig.Devices {
				seen[id] = true
			}
		}
	}
	var changed []State
	for id := range seen {
		r := s.reasonLocked(id, now)
		if old, ok := s.known[id]; (ok && old != r) || (!ok && r != "") {
			changed = append(changed, State{DeviceID: id, Reason: r})
		}
		if r == "" && !slices.Contains(ids, id) {
			// a device no longer enrolled and not busy is forgotten
			delete(s.known, id)
		} else {
			s.known[id] = r
		}
	}
	listeners := slices.Clone(s.listeners)
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
	slices.SortFunc(changed, func(a, b State) int { return strings.Compare(a.DeviceID, b.DeviceID) })
	for _, c := range changed {
		if s.d.Bus != nil {
			s.d.Bus.Publish(events.Event{
				Type: BusyChanged, DeviceID: c.DeviceID, At: now, Data: BusyData{Busy: c.Reason != "", Reason: c.Reason},
			})
		}
		for _, fn := range listeners {
			fn(c.DeviceID)
		}
	}
}

// Run loads the stored signals still in force, dropping the expired ones,
// then expires each signal at its time until ctx ends. It returns once the
// load is done (a failed one is logged, and nothing holds), with a stop that
// waits for the loop.
func (s *Service) Run(ctx context.Context) (stop func()) {
	if err := s.load(ctx); err != nil && ctx.Err() == nil {
		s.d.Log.Error("busy_failed", "op", "load")
	}
	rctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.loop(rctx)
	}()
	return func() {
		cancel()
		<-done
	}
}

func (s *Service) load(ctx context.Context) error {
	now := s.d.Now()
	if _, err := s.d.Queries.DeleteExpiredBusySignals(ctx, ts(now)); err != nil {
		return err
	}
	rows, err := s.d.Queries.ListBusySignals(ctx, ts(now))
	if err != nil {
		return err
	}
	loaded := map[string]Signal{}
	for _, r := range rows {
		sig := Signal{Source: r.Source, Reason: r.Reason, ExpiresAt: r.ExpiresAt.Time}
		for _, d := range r.Devices {
			sig.Devices = append(sig.Devices, uuid.UUID(d.Bytes).String())
		}
		slices.Sort(sig.Devices)
		loaded[r.Source] = sig
	}
	s.d.Log.Info("busy_loaded", "signals", len(loaded))
	s.apply(ctx, func() { s.stored = loaded })
	return nil
}

func (s *Service) loop(ctx context.Context) {
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	for {
		if next, ok := s.nextExpiry(); ok {
			timer.Reset(max(next.Sub(s.d.Now()), 0))
		} else {
			timer.Stop()
		}
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
		case <-timer.C:
			s.expire(ctx)
		}
	}
}

func (s *Service) nextExpiry() (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var next time.Time
	for _, sig := range s.stored {
		if next.IsZero() || sig.ExpiresAt.Before(next) {
			next = sig.ExpiresAt
		}
	}
	return next, !next.IsZero()
}

// expire drops the stored signals whose time came, from memory and the store.
func (s *Service) expire(ctx context.Context) {
	s.writes.Lock()
	defer s.writes.Unlock()
	now := s.d.Now()
	s.mu.Lock()
	var gone []string
	for k, sig := range s.stored {
		if !sig.live(now) {
			gone = append(gone, k)
		}
	}
	s.mu.Unlock()
	if len(gone) == 0 {
		return
	}
	// the rows are cleared too; one left by a failed delete is dropped at the
	// next load, and never read before that
	if _, err := s.d.Queries.DeleteExpiredBusySignals(ctx, ts(now)); err != nil && ctx.Err() == nil {
		s.d.Log.Warn("busy_failed", "op", "expire")
	}
	slices.Sort(gone)
	for _, k := range gone {
		s.d.Log.Info("busy_expired", "source", logSource(k))
	}
	s.apply(ctx, func() {
		for _, k := range gone {
			delete(s.stored, k)
		}
	})
}

// logSource is a source as a log line may carry it: a Home Assistant one
// names an entity, which stays out of the logs (rule 8).
func logSource(source string) string {
	if strings.HasPrefix(source, haPrefix) {
		return "ha"
	}
	return source
}

func canonical(id string) string {
	if u, err := uuid.Parse(id); err == nil {
		return u.String()
	}
	return id
}

func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }
