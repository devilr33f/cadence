package alarms

import (
	"slices"
	"time"
)

// the most snoozes the book keeps: one per alarm, timer and reminder it can hold
const maxSnoozed = maxRings

// SetSettings sets the ramp, ring limits and snooze the config gives; the rings that start from now on take them.
func (b *Book) SetSettings(s Settings) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.settings == s {
		return
	}
	b.settings = s
	b.notify()
}

func (b *Book) Settings() Settings {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.settings
}

// SnoozeRing puts r off for minutes from now (0: the config's), in place of a snooze of the same ring: it rings again
// then as it rang, at its own level, its schedule untouched. ErrInvalid for minutes out of range, ErrFull past the
// limit, ErrStore when it cannot be saved.
func (b *Book) SnoozeRing(r Ring, minutes int) (Snooze, error) {
	now := wall(b.clock.Now())
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.snooze(r, minutes, now)
}

// SnoozeLast snoozes the ring that stopped within SnoozeWindow, when kind and id are empty or name it.
func (b *Book) SnoozeLast(kind Kind, id string, minutes int) (Snooze, error) {
	now := wall(b.clock.Now())
	b.mu.Lock()
	defer b.mu.Unlock()
	l := b.last
	if l == nil || now.Sub(l.StoppedAt) > SnoozeWindow || now.Before(l.StoppedAt.Add(-time.Minute)) ||
		kind != "" && kind != l.Kind || id != "" && id != l.ID {
		return Snooze{}, b.refuse(kindOr(kind), ErrNothing, id)
	}
	r := l.Ring
	r.AutoSnoozes = 0
	return b.snooze(r, minutes, now)
}

// Stopped records the ring that stopped, for a snooze and the state.
func (b *Book) Stopped(r Ring, by StopReason, at time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.last = &LastRing{Ring: r, StoppedAt: wall(at).UTC(), StoppedBy: by}
	_ = b.persist()
	b.notify()
}

// Snoozed lists the snoozes, soonest first.
func (b *Book) Snoozed() []Snooze {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := slices.Clone(b.snoozed)
	slices.SortStableFunc(out, func(x, y Snooze) int { return x.Until.Compare(y.Until) })
	return out
}

// Last is the ring that last stopped.
func (b *Book) Last() (LastRing, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.last == nil {
		return LastRing{}, false
	}
	return *b.last, true
}

// NextOf is when the alarm with id next rings, a snooze of it included; false while its calendar is unknown (no zone,
// a clock not yet set) and it has no snooze.
func (b *Book) NextOf(a Alarm) (time.Time, bool) {
	now := wall(b.clock.Now())
	b.mu.Lock()
	defer b.mu.Unlock()
	var next time.Time
	found := false
	if b.alarmsLive(now) {
		next, found = nextRing(a, now, b.loc)
		if i := slices.IndexFunc(b.alarms, func(e storedAlarm) bool { return e.alarm.ID == a.ID }); i >= 0 {
			// an occurrence owed a ring now is the next one
			if due, ok := b.dueOccurrence(b.alarms[i], now); ok {
				next, found = due, true
			}
		}
	}
	for _, s := range b.snoozed {
		if s.Kind == KindAlarm && s.ID == a.ID && (!found || s.Until.Before(next)) {
			next, found = s.Until, true
		}
	}
	return next, found
}

// callers hold mu
func (b *Book) snooze(r Ring, minutes int, now time.Time) (Snooze, error) {
	if minutes == 0 {
		minutes = b.settings.SnoozeMinutes
	}
	if minutes < MinSnoozeMinutes || minutes > MaxSnoozeMinutes || !r.Kind.valid() || !ValidID(r.ID) {
		return Snooze{}, b.refuse(kindOr(r.Kind), ErrInvalid, r.ID)
	}
	s := Snooze{Ring: r, Until: now.Add(time.Duration(minutes) * time.Minute).UTC()}
	s.At = time.Time{}
	previous := slices.Clone(b.snoozed)
	i := slices.IndexFunc(b.snoozed, func(e Snooze) bool { return e.Kind == r.Kind && e.ID == r.ID })
	switch {
	case i >= 0:
		b.snoozed[i] = s
	case len(b.snoozed) >= maxSnoozed:
		return Snooze{}, b.refuse(r.Kind, ErrFull, r.ID)
	default:
		b.snoozed = append(b.snoozed, s)
	}
	if b.persist() != nil {
		b.snoozed = previous
		return Snooze{}, b.refuse(r.Kind, ErrStore, r.ID)
	}
	b.log.Info("ring_snoozed", "kind", string(r.Kind), "id", r.ID, "minutes", minutes, "auto", r.AutoSnoozes)
	b.notify()
	return s, nil
}

func kindOr(k Kind) Kind {
	if k == "" {
		return KindAlarm
	}
	return k
}
