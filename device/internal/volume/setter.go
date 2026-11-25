package volume

import (
	"errors"
	"io"
	"log/slog"
	"sync"
)

// Origin says what set a level.
type Origin string

const (
	OriginCommand Origin = "command"
	OriginSender  Origin = "sender"
	OriginRestore Origin = "restore"
	// a volume button on the device: the owner's own press, which may raise (rule 7)
	OriginButton Origin = "button"
	// a command word the speaker acted on, which only lowers
	OriginWord Origin = "word"
	// the host's system volume, where the level is the system's (android's STREAM_MUSIC): the remote's keys android
	// handled, another app, the tv's hdmi-cec volume. the system already plays at it, so following it raises nothing
	OriginSystem Origin = "system"
	// a bluetooth speaker active for the first time, which only lowers (rule 7: a new speaker starts at most at 40)
	OriginSpeaker Origin = "speaker"
)

var (
	// ErrStore refuses a change the store could not keep: a level that would not
	// survive a restart is never applied.
	ErrStore = errors.New("volume: store failed")
	// ErrUnknown refuses a sender's change while the level is the default, not
	// the device's own.
	ErrUnknown = errors.New("volume: level unknown")
)

// Setter owns the current level. Every change is persisted, applied and then
// heard by the listeners, one change at a time.
type Setter struct {
	store *Store
	log   *slog.Logger

	mu    sync.Mutex
	level int
	// the level is the device's own: loaded from the store or set since
	known     bool
	apply     func(int)
	listeners []func(int, Origin)
	// an alarm's ramp: the path to the hardware, and the level it last raised the hardware to above the level in
	// force, 0 when the hardware holds the level in force
	transient func(int) error
	raised    int
}

// NewSetter starts at the stored level, else at def. A nil log discards.
func NewSetter(store *Store, def int, log *slog.Logger) *Setter {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	s := &Setter{store: store, log: log, level: clamp(def)}
	source := "default"
	if level, ok := store.Load(); ok {
		s.level, s.known, source = level, true, "local"
	}
	log.Info("volume_loaded", "volume", s.level, "source", source)
	return s
}

func (s *Setter) Level() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.level
}

// Current is the level in force and whether it is the device's own: loaded
// from the store or set since, not the default.
func (s *Setter) Current() (level int, known bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.level, s.known
}

// Set clamps level to 0-100 and returns the level now in force.
func (s *Setter) Set(level int, origin Origin) (int, error) {
	return s.update(origin, func(int) (int, error) { return level, nil })
}

// Step moves the level by delta from the level in force, clamped to 0-100, and returns the level now in force.
func (s *Setter) Step(delta int, origin Origin) (int, error) {
	return s.update(origin, func(cur int) (int, error) { return cur + delta, nil })
}

// Cap lowers the level in force to top when it is above it, never raising it, and returns the level now in force. A
// level that is already at or below top, the device's own or the default, is left as it is.
func (s *Setter) Cap(top int, origin Origin) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.level <= top {
		return s.level, nil
	}
	return s.setLocked(clamp(top), origin)
}

// SetKnown is Set for a media sender: refused with ErrUnknown while the level
// is not the device's own, in the same step as the change (rule 7).
func (s *Setter) SetKnown(level int, origin Origin) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.known {
		return s.level, ErrUnknown
	}
	return s.setLocked(clamp(level), origin)
}

// Seed takes a level kept elsewhere before the device owned it, only while the
// device has none of its own. One the store cannot keep may still lower the
// level, in memory only (rule 7), and leaves it unknown so the next seed tries
// the store again.
func (s *Setter) Seed(level int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.known {
		return
	}
	level = clamp(level)
	if _, err := s.setLocked(level, OriginRestore); err != nil && level < s.level {
		s.changeLocked(level, OriginRestore)
	}
}

// Reconcile takes the system's level once as audiod starts, where the level is the system volume, before the first
// level is applied: a level of the device's own above the system's is lowered to it and one below it stays (the
// system is then lowered to it), so neither moves up (rule 7); with none of its own the system's level becomes the
// device's (nothing moves). A level the store cannot keep may still lower the level, in memory only, as Seed does.
// It returns the level in force.
func (s *Setter) Reconcile(system int) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	system = clamp(system)
	if s.known && system >= s.level {
		return s.level
	}
	if _, err := s.setLocked(system, OriginSystem); err != nil && system < s.level {
		s.changeLocked(system, OriginSystem)
	}
	return s.level
}

// Follow hands the current level to start and applies every later one through
// the func start returns, with no level slipping in between.
func (s *Setter) Follow(start func(level int) (apply func(int))) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.apply = start(s.level)
}

// Listen hears every change after it. Listeners run under the setter's lock, in
// order: they must not block or call the setter.
func (s *Setter) Listen(f func(level int, origin Origin)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.listeners = append(s.listeners, f)
}

// update sets the level next picks from the current one, under the lock: no
// change can slip in between the decision and the write.
func (s *Setter) update(origin Origin, next func(cur int) (int, error)) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	level, err := next(s.level)
	if err != nil {
		return s.level, err
	}
	return s.setLocked(clamp(level), origin)
}

// callers hold mu
func (s *Setter) setLocked(level int, origin Origin) (int, error) {
	if s.known && level == s.level {
		return level, nil
	}
	if err := s.store.Save(level); err != nil {
		s.log.Error("volume_store_failed", "origin", string(origin))
		return s.level, ErrStore
	}
	s.known = true
	s.changeLocked(level, origin)
	return level, nil
}

// changeLocked puts level in force: applied, then heard. callers hold mu
func (s *Setter) changeLocked(level int, origin Origin) {
	s.level = level
	// the plain set puts the level in force on the hardware, over any ramp's
	s.raised = 0
	if s.apply != nil {
		s.apply(level)
	}
	for _, f := range s.listeners {
		f(level, origin)
	}
	s.log.Info("volume_set", "volume", level, "origin", string(origin))
}

func clamp(level int) int { return min(max(level, minLevel), maxLevel) }
