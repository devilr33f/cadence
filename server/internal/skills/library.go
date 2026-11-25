// Package skills holds the agent's skills: named instruction packs a turn
// loads only when its input calls for them. The server ships some (Builtins);
// the owner adds more, or replaces or switches off a built-in by its name,
// through the admin api. A skill's text is never logged: lines carry names,
// counts and codes.
package skills

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/memory"
)

// where a skill comes from
const (
	SourceBuiltin = "builtin"
	SourceOwner   = "owner"
)

var (
	ErrNotFound = errors.New("skills: not found")
	// ErrBuiltin is a delete of a built-in skill the owner never replaced
	ErrBuiltin = errors.New("skills: built-in")
)

// Entry is one skill as the library holds it: Source owner for the owner's
// row (one that replaces a built-in has Overrides), Enabled false for one
// switched off.
type Entry struct {
	agent.Skill
	Enabled   bool
	Source    string
	Overrides bool
	UpdatedAt time.Time
}

// Store keeps the owner's skills.
type Store interface {
	List(ctx context.Context) ([]Entry, error)
	// Put stores the skill, created says whether it is new
	Put(ctx context.Context, e Entry) (created bool, at time.Time, err error)
	// Delete removes it, false when there was none
	Delete(ctx context.Context, name string) (bool, error)
}

// Deps are the library's collaborators. Embeds, when set, makes the skills'
// vectors for the pick's similarity; nil picks by trigger words alone.
type Deps struct {
	Store    Store
	Builtins []agent.Skill
	Embeds   memory.Embeds
	Log      *slog.Logger
}

// Library merges the built-ins and the owner's skills and answers turns from
// memory: it reads the store at Load and after each change it makes.
type Library struct {
	d   Deps
	cur atomic.Pointer[[]Entry]
	vec *vectors

	mu      sync.Mutex
	changed []func()
}

var _ agent.Skills = (*Library)(nil)

func New(d Deps) *Library {
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	l := &Library{d: d, vec: newVectors(d.Embeds, d.Log)}
	l.set(merge(d.Builtins, nil))
	return l
}

// Load reads the owner's skills; a failed read keeps what the library had
// (the built-ins at first).
func (l *Library) Load(ctx context.Context) error {
	if l.d.Store == nil {
		return nil
	}
	owned, err := l.d.Store.List(ctx)
	if err != nil {
		l.d.Log.Warn("skills_unread")
		return err
	}
	l.set(merge(l.d.Builtins, owned))
	return nil
}

func (l *Library) set(all []Entry) { l.cur.Store(&all) }

// All is every skill by name, the ones switched off too.
func (l *Library) All() []Entry { return *l.cur.Load() }

// Get is one skill by name.
func (l *Library) Get(name string) (Entry, bool) {
	all := l.All()
	i := slices.IndexFunc(all, func(e Entry) bool { return e.Name == name })
	if i < 0 {
		return Entry{}, false
	}
	return all[i], true
}

// For is the enabled skills listed in mode.
func (l *Library) For(mode string) []agent.Skill {
	var out []agent.Skill
	for _, e := range l.All() {
		if e.Enabled && (e.Modes == nil || slices.Contains(e.Modes, mode)) {
			out = append(out, e.Skill)
		}
	}
	return out
}

// OnChange calls fn after each stored put or delete; fn must not block.
func (l *Library) OnChange(fn func()) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.changed = append(l.changed, fn)
}

// Put stores the owner's skill (replacing a built-in of its name) and reloads.
func (l *Library) Put(ctx context.Context, e Entry) (Entry, bool, error) {
	created, at, err := l.d.Store.Put(ctx, e)
	if err != nil {
		return Entry{}, false, err
	}
	l.reload(ctx)
	l.d.Log.Info("skill_saved", "name", e.Name, "created", created)
	got, ok := l.Get(e.Name)
	if !ok {
		e.Source, e.UpdatedAt = SourceOwner, at
		got = e
	}
	return got, created, nil
}

// Delete removes the owner's skill of name; a built-in it replaced is back.
func (l *Library) Delete(ctx context.Context, name string) error {
	ok, err := l.d.Store.Delete(ctx, name)
	switch {
	case err != nil:
		return err
	case !ok && slices.ContainsFunc(l.d.Builtins, func(s agent.Skill) bool { return s.Name == name }):
		return ErrBuiltin
	case !ok:
		return ErrNotFound
	}
	l.reload(ctx)
	l.d.Log.Info("skill_deleted", "name", name)
	return nil
}

// reload reads the store after a change; a failed read leaves the list as
// it was until the next change or Load
func (l *Library) reload(ctx context.Context) {
	_ = l.Load(context.WithoutCancel(ctx))
	l.mu.Lock()
	fns := slices.Clone(l.changed)
	l.mu.Unlock()
	for _, fn := range fns {
		fn()
	}
}

// merge is the built-ins with the owner's rows over them, by name
func merge(builtins []agent.Skill, owned []Entry) []Entry {
	out := make([]Entry, 0, len(builtins)+len(owned))
	for _, s := range builtins {
		if !slices.ContainsFunc(owned, func(e Entry) bool { return e.Name == s.Name }) {
			out = append(out, Entry{Skill: s, Enabled: true, Source: SourceBuiltin})
		}
	}
	for _, e := range owned {
		e.Source = SourceOwner
		e.Overrides = slices.ContainsFunc(builtins, func(s agent.Skill) bool { return s.Name == e.Name })
		out = append(out, e)
	}
	slices.SortFunc(out, func(a, b Entry) int {
		switch {
		case a.Name < b.Name:
			return -1
		case a.Name > b.Name:
			return 1
		}
		return 0
	})
	return out
}
