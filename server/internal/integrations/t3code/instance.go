package t3code

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/subscriptions"
)

const (
	// maxFollows bounds the thread streams one instance keeps open; past
	// it the shell alone tells a thread's news
	maxFollows = 64
	// maxTold bounds what one thread remembers having told
	maxTold   = 256
	outBuffer = 64
)

// instance follows one T3 Code server for the hub: the shell for every
// thread, a thread's own stream while a subscription could match it and it
// is active (or went idle less than Linger ago).
type instance struct {
	name string
	c    *Client
	d    *SourceDeps

	ctx    context.Context
	cancel context.CancelFunc
	runs   chan struct{} // closed once the client's Run returned
	wg     sync.WaitGroup
	out    chan subscriptions.Update
	// reading: the hub opened the stream, so updates are sent, not dropped
	reading atomic.Bool
	poke    chan struct{}

	mu       sync.Mutex
	shell    *ShellTracker
	synced   bool
	shellSub *Subscription
	follows  map[string]*follow
	told     map[string]map[string]bool // thread -> what was told of it
}

// follow is one thread's own stream; synced once its first snapshot was
// handled, from when the shell's news of the thread is left to it.
type follow struct {
	sub     *Subscription
	synced  atomic.Bool
	idleAt  time.Time
	project string
}

func newInstance(name string, c *Client, d *SourceDeps) *instance {
	return &instance{
		name: name, c: c, d: d, runs: make(chan struct{}), out: make(chan subscriptions.Update, outBuffer),
		poke: make(chan struct{}, 1), shell: NewShellTracker(), follows: map[string]*follow{}, told: map[string]map[string]bool{},
	}
}

func (in *instance) start() {
	in.ctx, in.cancel = context.WithCancel(context.Background())
	// a client that ran before (the instance named again) takes
	// subscriptions at once, not only once Run began
	in.c.mu.Lock()
	in.c.stopped = false
	in.c.mu.Unlock()
	go func() {
		defer close(in.runs)
		in.c.Run(in.ctx)
	}()
	in.mu.Lock()
	in.shellSub = in.c.SubscribeShell(in.onShell)
	in.mu.Unlock()
	in.wg.Add(1)
	go in.loop()
}

// stop ends the client and every stream, then closes the hub's stream.
func (in *instance) stop() {
	in.cancel()
	in.mu.Lock()
	subs := []*Subscription{in.shellSub}
	for id, f := range in.follows {
		subs = append(subs, f.sub)
		delete(in.follows, id)
	}
	in.mu.Unlock()
	for _, s := range subs {
		if s != nil {
			s.Close()
		}
	}
	in.wg.Wait()
	<-in.runs
	close(in.out)
}

func (in *instance) shellCopy() ([]Project, []ThreadShell, bool) {
	in.mu.Lock()
	defer in.mu.Unlock()
	if !in.synced {
		return nil, nil, false
	}
	projects := make([]Project, 0, len(in.shell.Projects))
	for _, p := range in.shell.Projects {
		projects = append(projects, p)
	}
	threads := make([]ThreadShell, 0, len(in.shell.Threads))
	for _, t := range in.shell.Threads {
		threads = append(threads, t)
	}
	return projects, threads, true
}

func (in *instance) status() InstanceStatus {
	st := in.c.Status()
	in.mu.Lock()
	defer in.mu.Unlock()
	out := InstanceStatus{Connected: st.Connected, LastError: st.LastError, Synced: in.synced, Followed: len(in.follows)}
	for _, t := range in.shell.Threads {
		out.Threads++
		if t.PendingRuntimeRequest != nil {
			out.Pending++
		}
	}
	return out
}

func (in *instance) kick() {
	select {
	case in.poke <- struct{}{}:
	default:
	}
}

// onShell takes one shell item: what it tells goes to the hub unless the
// thread's own stream tells it; a new pending request is read in full.
func (in *instance) onShell(it ShellItem) {
	in.mu.Lock()
	ups := in.shell.Apply(it)
	if it.Kind == ShellSnapshotKind && it.Snapshot != nil {
		in.synced = true
	}
	if it.Kind == ShellThreadRemoved {
		delete(in.told, it.ThreadID)
	}
	in.mu.Unlock()
	for _, su := range ups {
		if in.detailed(su.ThreadID) {
			continue
		}
		if su.NeedsDetail {
			in.wg.Add(1)
			go in.detail(su)
			continue
		}
		in.emit(su.ProjectID, su.Update)
	}
	in.kick()
}

// detailed reports a thread whose own stream is synced, so it tells.
func (in *instance) detailed(threadID string) bool {
	in.mu.Lock()
	defer in.mu.Unlock()
	f := in.follows[threadID]
	return f != nil && f.synced.Load()
}

// detail reads a thread's projection for the request the shell pointed at.
func (in *instance) detail(su ShellUpdate) {
	defer in.wg.Done()
	ctx, cancel := context.WithTimeout(in.ctx, 20*time.Second)
	defer cancel()
	p, err := in.c.GetThreadProjection(ctx, su.ThreadID)
	if err != nil {
		if in.ctx.Err() == nil {
			in.d.Log.Warn("t3code_detail_failed", "instance", in.name, "code", ErrorCode(err))
		}
		return
	}
	for _, u := range PendingFromProjection(*p) {
		if u.RequestID != su.RequestID {
			continue
		}
		if u.Title == "" {
			u.Title = su.Title
		}
		in.emit(su.ProjectID, u)
	}
}

func (in *instance) projectOf(threadID string) string {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.shell.Threads[threadID].ProjectID
}

// emit sends an update to the hub once: a stop per run and status, an
// error per run, a question per token.
func (in *instance) emit(projectID string, u Update) {
	key := ""
	switch u.Kind {
	case KindStopped:
		key = "s:" + u.RunID + ":" + u.Status
	case KindError:
		key = "e:" + u.RunID
	case KindQuestion, KindApproval:
		key = "q:" + u.Token
	case KindResolved:
		key = "r:" + u.RequestID
	default:
		return
	}
	in.mu.Lock()
	seen := in.told[u.ThreadID]
	if seen == nil || len(seen) >= maxTold {
		seen = map[string]bool{}
		in.told[u.ThreadID] = seen
	}
	if seen[key] {
		in.mu.Unlock()
		return
	}
	seen[key] = true
	if u.Title == "" {
		u.Title = in.shell.Threads[u.ThreadID].Title
	}
	in.mu.Unlock()
	hu, ok := hubUpdate(in.name, projectID, u, in.d.Now())
	if !ok || !in.reading.Load() {
		return
	}
	select {
	case in.out <- hu:
	case <-in.ctx.Done():
	}
}

// loop keeps the thread streams in line with the subscriptions, at each
// shell change and every Every.
func (in *instance) loop() {
	defer in.wg.Done()
	t := time.NewTicker(in.d.Every)
	defer t.Stop()
	for {
		select {
		case <-in.ctx.Done():
			return
		case <-in.poke:
		case <-t.C:
		}
		in.reconcile()
	}
}

// active reports a thread that may ask or stop soon: running, waiting, or
// with a pending request.
func active(t ThreadShell) bool {
	return t.PendingRuntimeRequest != nil || t.Status != "idle" && t.Status != "" && !Terminal(t.Status)
}

func (in *instance) reconcile() {
	ctx, cancel := context.WithTimeout(in.ctx, 10*time.Second)
	topics, err := in.d.Followed(ctx, in.name)
	cancel()
	if err != nil {
		return
	}
	now := in.d.Now()
	in.mu.Lock()
	var closing []*Subscription
	for id, f := range in.follows {
		th, ok := in.shell.Threads[id]
		switch {
		case !ok || !matches(topics, in.name, th):
		case active(th):
			f.idleAt = time.Time{}
			continue
		case f.idleAt.IsZero():
			f.idleAt = now
			continue
		case now.Sub(f.idleAt) < in.d.Linger:
			continue
		}
		closing = append(closing, f.sub)
		delete(in.follows, id)
	}
	for id, th := range in.shell.Threads {
		if len(in.follows) >= maxFollows {
			break
		}
		if _, ok := in.follows[id]; ok || !validID(id) || !active(th) || !matches(topics, in.name, th) {
			continue
		}
		in.follows[id] = in.followLocked(id, th.ProjectID)
	}
	in.mu.Unlock()
	for _, s := range closing {
		s.Close()
	}
}

// followLocked opens a thread's own stream; in.mu held.
func (in *instance) followLocked(threadID, projectID string) *follow {
	f := &follow{project: projectID}
	tr := NewTracker(threadID)
	f.sub = in.c.SubscribeThread(threadID, func(it ThreadItem) {
		ups := tr.Apply(it)
		if it.Kind == ThreadSnapshotKind {
			f.synced.Store(true)
		}
		project := in.projectOf(threadID)
		for _, u := range ups {
			in.emit(project, u)
		}
	})
	return f
}

// matches reports whether one of the followed topics covers the thread.
func matches(topics []subscriptions.Topic, instance string, th ThreadShell) bool {
	topic, also := topicsOf(instance, th.ProjectID, th.ID)
	for _, t := range topics {
		if t.Matches(topic) {
			return true
		}
		for _, a := range also {
			if t.Matches(a) {
				return true
			}
		}
	}
	return false
}
