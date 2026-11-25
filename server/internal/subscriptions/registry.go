package subscriptions

import (
	"errors"
	"slices"
	"sort"
	"sync"
)

var (
	// ErrUnknownModule is a topic whose module no Subscribable registered.
	ErrUnknownModule = errors.New("subscriptions: unknown module")
	// ErrEvent is an event the topic's module does not emit.
	ErrEvent = errors.New("subscriptions: unsupported event")
)

// Registry holds the modules that can be followed, by name.
type Registry struct {
	mu   sync.RWMutex
	mods map[string]Subscribable
	// added is called after each Register, so the hub opens the new module's
	// streams
	added func()
}

func NewRegistry() *Registry { return &Registry{mods: map[string]Subscribable{}} }

// Register adds a module; a second one of the same name is refused.
func (r *Registry) Register(m Subscribable) error {
	r.mu.Lock()
	name := m.Module()
	if !moduleRe.MatchString(name) {
		r.mu.Unlock()
		return ErrTopic
	}
	if _, ok := r.mods[name]; ok {
		r.mu.Unlock()
		return errors.New("subscriptions: module registered twice")
	}
	r.mods[name] = m
	added := r.added
	r.mu.Unlock()
	if added != nil {
		added()
	}
	return nil
}

// Get is the module of that name.
func (r *Registry) Get(name string) (Subscribable, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	m, ok := r.mods[name]
	return m, ok
}

// Modules are the registered modules' names, sorted.
func (r *Registry) Modules() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.mods))
	for n := range r.mods {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func (r *Registry) onAdd(fn func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.added = fn
}

// check validates a topic and events against its module: known, a path the
// module takes, events it emits (none: all of them). It returns the events
// to store.
func (r *Registry) check(t Topic, events []string) ([]string, error) {
	m, ok := r.Get(t.Module)
	if !ok {
		return nil, ErrUnknownModule
	}
	if err := m.Validate(t.Instance, t.Path); err != nil {
		return nil, ErrTopic
	}
	emits := m.Events()
	if len(events) == 0 {
		return slices.Clone(emits), nil
	}
	out := []string{}
	for _, e := range events {
		if !slices.Contains(emits, e) {
			return nil, ErrEvent
		}
		if !slices.Contains(out, e) {
			out = append(out, e)
		}
	}
	return out, nil
}
