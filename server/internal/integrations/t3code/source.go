package t3code

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sort"
	"sync"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/subscriptions"
)

// ErrNotConfigured is an instance the t3code setting does not name now.
var ErrNotConfigured = errors.New("t3code: instance not configured")

// SourceDeps are what the module needs of the hub.
type SourceDeps struct {
	// Followed is the topics the subscriptions naming an instance follow
	// (the hub's Followed for this module): a thread is followed in detail
	// only while one of them could match it
	Followed func(ctx context.Context, instance string) ([]subscriptions.Topic, error)
	// Log nil discards
	Log *slog.Logger
	// Now nil is time.Now
	Now func() time.Time
	// Every is how often the followed topics are read again besides each
	// shell change; zero is 10 s
	Every time.Duration
	// Linger keeps following a thread that went idle, so its stop arrives in
	// detail; zero is 30 s
	Linger time.Duration
}

// Want is one instance the setting names, with its client.
type Want struct {
	Instance string
	Client   *Client
}

// Source is the t3code module of the subscriptions hub: one follower per
// instance the t3code setting names, each running its client, following the
// shell for every thread (new ones too) and a thread's own stream while a
// subscription could match it. It never logs a title, a text or a token.
type Source struct {
	d SourceDeps

	apply sync.Mutex // one Apply at a time
	mu    sync.Mutex
	insts map[string]*instance
	// changed hears an instance started or stopped (the hub's Refresh)
	changed []func()
}

func NewSource(d SourceDeps) *Source {
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Every <= 0 {
		d.Every = 10 * time.Second
	}
	if d.Linger <= 0 {
		d.Linger = 30 * time.Second
	}
	if d.Followed == nil {
		d.Followed = func(context.Context, string) ([]subscriptions.Topic, error) { return nil, nil }
	}
	return &Source{d: d, insts: map[string]*instance{}}
}

// OnChange calls fn after Apply started or stopped an instance; fn must not
// block.
func (s *Source) OnChange(fn func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.changed = append(s.changed, fn)
}

// Apply runs the instances want names, each with its client: one whose
// client changed (its config or token) starts again, one no longer named
// stops. The first of a name listed twice counts.
func (s *Source) Apply(want []Want) {
	s.apply.Lock()
	defer s.apply.Unlock()
	wanted := map[string]*Client{}
	for _, w := range want {
		if _, ok := wanted[w.Instance]; !ok && w.Client != nil {
			wanted[w.Instance] = w.Client
		}
	}
	s.mu.Lock()
	var gone []*instance
	for name, in := range s.insts {
		if c, ok := wanted[name]; ok && c == in.c {
			delete(wanted, name)
			continue
		}
		gone = append(gone, in)
		delete(s.insts, name)
	}
	s.mu.Unlock()
	for _, in := range gone {
		in.stop()
		s.d.Log.Info("t3code_follow_stopped", "instance", in.name)
	}
	names := make([]string, 0, len(wanted))
	for name := range wanted {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		in := newInstance(name, wanted[name], &s.d)
		s.mu.Lock()
		s.insts[name] = in
		s.mu.Unlock()
		in.start()
		s.d.Log.Info("t3code_follow_started", "instance", name)
	}
	if len(gone) > 0 || len(names) > 0 {
		s.mu.Lock()
		fns := slices.Clone(s.changed)
		s.mu.Unlock()
		for _, fn := range fns {
			fn()
		}
	}
}

// Close stops every instance.
func (s *Source) Close() { s.Apply(nil) }

func (s *Source) get(name string) *instance {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.insts[name]
}

// Instances are the names running now, sorted.
func (s *Source) Instances() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.insts))
	for n := range s.insts {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Client is a running instance's client.
func (s *Source) Client(instance string) (*Client, bool) {
	in := s.get(instance)
	if in == nil {
		return nil, false
	}
	return in.c, true
}

// Shell is a running instance's projects and active threads as its shell
// last said; false while it is not running or has no snapshot yet. The
// titles are outside text.
func (s *Source) Shell(instance string) ([]Project, []ThreadShell, bool) {
	in := s.get(instance)
	if in == nil {
		return nil, nil, false
	}
	return in.shellCopy()
}

// InstanceStatus is one instance as the admin view shows it: counts and
// codes, never a title.
type InstanceStatus struct {
	Connected bool
	LastError string
	Synced    bool
	Threads   int
	Pending   int
	Followed  int
}

// Status is the instance's, false when it is not running.
func (s *Source) Status(instance string) (InstanceStatus, bool) {
	in := s.get(instance)
	if in == nil {
		return InstanceStatus{}, false
	}
	return in.status(), true
}

// Module is the provider catalog's name.
func (s *Source) Module() string { return ModuleName }

// Events are the shared events the module emits.
func (s *Source) Events() []string {
	return []string{subscriptions.EventStopped, subscriptions.EventQuestion, subscriptions.EventApproval, subscriptions.EventError}
}

// Validate takes the paths of the module's topics on an instance the
// setting names: the root (with *), thread[:<id>], project[:<id>] and
// project:<id>:thread:<id>.
func (s *Source) Validate(instance string, path []string) error {
	if s.get(instance) == nil {
		return ErrNotConfigured
	}
	switch {
	case len(path) == 0:
		return nil
	case len(path) == 1 && (path[0] == "thread" || path[0] == "project"):
		return nil
	case len(path) == 2 && (path[0] == "thread" || path[0] == "project") && validID(path[1]):
		return nil
	case len(path) == 4 && path[0] == "project" && validID(path[1]) && path[2] == "thread" && validID(path[3]):
		return nil
	}
	return subscriptions.ErrTopic
}

// List shows what can be followed under a prefix: the projects (as their
// subtree topics) and the threads, of a project alone or one thread.
func (s *Source) List(_ context.Context, instance string, prefix []string) ([]subscriptions.Entry, error) {
	in := s.get(instance)
	if in == nil {
		return nil, ErrNotConfigured
	}
	projects, threads, ok := in.shellCopy()
	if !ok {
		return nil, fail(CodeUnavailable)
	}
	var out []subscriptions.Entry
	wantProjects := len(prefix) == 0 || len(prefix) == 1 && prefix[0] == "project"
	if wantProjects {
		for _, p := range projects {
			if !validID(p.ID) {
				continue
			}
			t := subscriptions.NewTopic(ModuleName, instance, "project", p.ID)
			t.Wild = true
			out = append(out, subscriptions.Entry{Topic: t.String(), Kind: "project", Title: p.Title})
		}
	}
	for _, th := range threads {
		if !validID(th.ID) || !threadUnder(th, prefix) {
			continue
		}
		e := subscriptions.Entry{Topic: ThreadTopic(instance, th.ID).String(), Kind: KindThread, Title: th.Title, Status: th.Status}
		if at, err := time.Parse(time.RFC3339Nano, th.UpdatedAt); err == nil {
			e.Updated = at.UTC()
		}
		out = append(out, e)
	}
	return out, nil
}

// threadUnder reports whether a thread is listed under a prefix.
func threadUnder(th ThreadShell, prefix []string) bool {
	switch {
	case len(prefix) == 0, len(prefix) == 1 && prefix[0] == "thread":
		return true
	case len(prefix) == 1:
		return false
	case prefix[0] == "thread":
		return th.ID == prefix[1]
	case prefix[0] == "project" && th.ProjectID == prefix[1]:
		return len(prefix) < 4 || th.ID == prefix[3]
	}
	return false
}

// Updates is a running instance's stream, nil for one the setting does not
// name; it closes when the instance stops.
func (s *Source) Updates(instance string) <-chan subscriptions.Update {
	in := s.get(instance)
	if in == nil {
		return nil
	}
	in.reading.Store(true)
	return in.out
}

// Answer replies to the request a hub token names on a thread, read again
// from the thread's projection: ErrNoAnswer when it is no longer pending or
// its prompt changed since. An approval sends runtime-request.respond with
// accept (once), acceptForSession (session) or decline (deny); a question
// its answers keyed by question id, or a message to the thread for a
// request answered by message.
func (s *Source) Answer(ctx context.Context, ref subscriptions.Ref, a subscriptions.Answer) error {
	in := s.get(ref.Instance)
	if in == nil {
		return fail(CodeUnavailable)
	}
	if !validID(ref.ID) {
		return subscriptions.ErrNoAnswer
	}
	return in.answer(ctx, ref.ID, a)
}
