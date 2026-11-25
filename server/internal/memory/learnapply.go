package memory

import (
	"context"
	"errors"
	"slices"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

const (
	// activeCap and pendingCap bound a scope's memories, per device for the
	// device scope
	activeCap  = 300
	pendingCap = 100
	// an add this close to an active memory of its scope updates that one
	dedupSimilarity = 0.9
)

type scopeKey struct{ scope, deviceID string }

// run is one learning run's state and counts.
type run struct {
	l   *Learner
	dev string
	// the device's memory scopes (memoryScopes): what the model sees and
	// what ops may touch
	allowGlobal, allowDevice bool
	// memoryApproval for each scope: the global setting for global memories,
	// the device's effective one for its own
	approveGlobal, approveDevice bool
	emb                          providers.Embedder // nil without an embed instance
	seg                          map[int64]string   // role of each message the model saw, by id

	applied, pending, evicted, pruned int
	drops                             map[string]int
	ids                               []string
	pendingIn                         map[scopeKey]bool
}

// newRun takes the device's memoryScopes: nil is both, an empty list none.
func newRun(l *Learner, dev string, scopes []string) *run {
	return &run{
		l: l, dev: dev, drops: map[string]int{}, pendingIn: map[scopeKey]bool{},
		allowGlobal: scopes == nil || slices.Contains(scopes, ScopeGlobal),
		allowDevice: scopes == nil || slices.Contains(scopes, ScopeDevice),
	}
}

func (r *run) scopes() Scopes {
	sc := Scopes{Global: r.allowGlobal}
	if r.allowDevice {
		sc.DeviceID = r.dev
	}
	return sc
}

func (r *run) allowed(scope string) bool {
	return (scope == ScopeGlobal && r.allowGlobal) || (scope == ScopeDevice && r.allowDevice)
}

func (r *run) approval(scope string) bool {
	if scope == ScopeGlobal {
		return r.approveGlobal
	}
	return r.approveDevice
}

func (r *run) drop(reason string) { r.drops[reason]++ }

func (r *run) dropped() int {
	n := 0
	for _, c := range r.drops {
		n += c
	}
	return n
}

// vectors embeds the texts of the adds and updates in one request; without
// an embedder every vector is nil.
func (r *run) vectors(ctx context.Context, plan []plannedOp) ([][]float32, error) {
	out := make([][]float32, len(plan))
	var texts []string
	var at []int
	for i, p := range plan {
		if p.op != "delete" {
			texts, at = append(texts, p.text), append(at, i)
		}
	}
	if r.emb == nil || len(texts) == 0 {
		return out, nil
	}
	vecs, err := r.embed(ctx, texts)
	if err != nil {
		return nil, err
	}
	for j, i := range at {
		out[i] = vecs[j]
	}
	return out, nil
}

// result is what one op came to: the memory it wrote and how, or why it was
// dropped.
type result struct {
	op       string
	m        Memory
	awaiting bool
	evicted  int
	reason   string
}

func dropped(reason string) result { return result{reason: reason} }

// errDropped rolls back an add that did not land.
var errDropped = errors.New("memory: op dropped")

func (r *run) apply(ctx context.Context, p plannedOp, vec []float32) {
	var res result
	switch p.op {
	case "add":
		res = r.add(ctx, p, vec)
	case "update":
		res = r.update(ctx, r.l.d.Store, *p.target, p, vec, p.conf)
	case "delete":
		res = r.remove(ctx, *p.target, p)
	}
	if res.reason != "" {
		r.drop(res.reason)
		return
	}
	r.evicted += res.evicted
	r.done(res.op, res.m, res.awaiting)
}

// add stores a new memory under its scope's lock, so that concurrent runs
// can neither pass the cap nor store one memory twice. A memory of its scope
// within dedupSimilarity, an active one or, while the scope awaits approval,
// a pending one too, is updated instead, keeping the higher confidence.
func (r *run) add(ctx context.Context, p plannedOp, vec []float32) result {
	dev := ""
	if p.scope == ScopeDevice {
		dev = r.dev
	}
	var res result
	err := r.l.d.Store.Locked(ctx, p.scope, dev, func(s *Store) error {
		if res = r.addLocked(ctx, s, p, dev, vec); res.reason != "" {
			return errDropped
		}
		return nil
	})
	switch {
	case errors.Is(err, errDropped):
		return dropped(res.reason)
	case err != nil:
		return dropped(dropStore)
	}
	return res
}

func (r *run) addLocked(ctx context.Context, s *Store, p plannedOp, dev string, vec []float32) result {
	approval := r.approval(p.scope)
	if vec != nil {
		sc := Scopes{Global: p.scope == ScopeGlobal, DeviceID: dev}
		nearest := s.Nearest
		if approval {
			nearest = s.NearestWithPending
		}
		near, err := nearest(ctx, vec, r.emb.Model(), sc, 1, dedupSimilarity)
		if err != nil {
			return dropped(dropStore)
		}
		if len(near) > 0 {
			n := near[0].Memory
			if reason := untouchable(n); reason != "" {
				return dropped(reason)
			}
			return r.update(ctx, s, n, p, vec, max(n.Confidence, p.conf))
		}
	}
	nm := NewMemory{
		Scope: p.scope, DeviceID: dev, Kind: p.kind, Text: p.text, Confidence: p.conf, Evidence: p.evidence,
		Source: SourceLearned, Status: StatusActive,
	}
	if vec != nil {
		nm.Vector, nm.EmbedModel = vec, r.emb.Model()
	}
	evicted := 0
	if approval {
		nm.Status = StatusPending
	} else {
		// the learned memories that score lowest go, never pinned, manual or this run's own
		n, fits, err := s.MakeRoom(ctx, p.scope, dev, r.ids)
		switch {
		case err != nil:
			return dropped(dropStore)
		case !fits:
			return dropped(dropCap)
		}
		evicted = n
	}
	m, err := s.Add(ctx, nm)
	if err != nil {
		return dropped(dropStore)
	}
	return result{op: "add", m: m, awaiting: approval, evicted: evicted}
}

// untouchable says why learning may not change a memory: manual memories and
// pinned ones change in the dashboard only.
func untouchable(m Memory) string {
	switch {
	case m.Source != SourceLearned:
		return dropManual
	case m.Pinned:
		return dropPinned
	}
	return ""
}

// update replaces a memory's text, kind and confidence and merges the
// evidence; hits stay. In approval mode an active memory gets a proposal
// instead, while a pending one is edited in place. s is the store, or an
// add's locked transaction.
func (r *run) update(ctx context.Context, s *Store, t Memory, p plannedOp, vec []float32, conf float32) result {
	kind := p.kind
	if kind == "" {
		kind = t.Kind
	}
	ev := mergeEvidence(t.Evidence, p.evidence)
	if r.approval(t.Scope) && t.Status == StatusActive {
		return r.propose(ctx, s, t, NewMemory{Kind: kind, Text: p.text, Confidence: conf, Evidence: ev, Proposal: ProposalUpdate}, vec)
	}
	m, err := s.UpdateLearned(ctx, t, p.text, kind, conf, ev)
	if err != nil {
		return dropped(dropFor(err))
	}
	if vec != nil {
		// a vector that fails to land is the re-embed job's to make
		_ = s.SetEmbeddingIfText(ctx, m.ID, p.text, vec, r.emb.Model())
	}
	if m.Status == StatusActive {
		// an older proposal, approved later, would overwrite the newer text
		if _, err := s.DeleteProposals(ctx, m.ID); err != nil {
			r.l.d.Log.Warn("learning_proposals_failed", "device_id", r.dev, "memory_id", m.ID)
		}
	}
	return result{op: "update", m: m, awaiting: m.Status == StatusPending}
}

// remove deletes a learned memory; in approval mode an active one gets a
// proposal instead.
func (r *run) remove(ctx context.Context, t Memory, p plannedOp) result {
	s := r.l.d.Store
	if r.approval(t.Scope) && t.Status == StatusActive {
		return r.propose(ctx, s, t, NewMemory{Kind: t.Kind, Text: t.Text, Confidence: t.Confidence, Evidence: p.evidence, Proposal: ProposalDelete}, nil)
	}
	if err := s.DeleteLearned(ctx, t); err != nil {
		return dropped(dropFor(err))
	}
	return result{op: "delete", m: t}
}

// propose stores a pending proposal for the active memory t, in t's scope;
// the latest proposal for a memory replaces the earlier ones.
func (r *run) propose(ctx context.Context, s *Store, t Memory, nm NewMemory, vec []float32) result {
	nm.Scope, nm.DeviceID, nm.Source, nm.Status, nm.Replaces = t.Scope, t.DeviceID, SourceLearned, StatusPending, t.ID
	if vec != nil {
		nm.Vector, nm.EmbedModel = vec, r.emb.Model()
	}
	if _, err := s.DeleteProposals(ctx, t.ID); err != nil {
		return dropped(dropStore)
	}
	m, err := s.Add(ctx, nm)
	if err != nil {
		return dropped(dropStore)
	}
	return result{op: nm.Proposal, m: m, awaiting: true}
}

// dropFor names why a write failed: a memory changed or gone since the model
// saw it (an edit, a pin or an approval in the dashboard meanwhile) is
// "changed".
func dropFor(err error) string {
	if errors.Is(err, ErrChanged) {
		return dropChanged
	}
	return dropStore
}

// prunePending drops the oldest pending memories past pendingCap in each
// scope this run proposed in.
func (r *run) prunePending(ctx context.Context) {
	for k := range r.pendingIn {
		n, err := r.l.d.Store.PrunePending(ctx, k.scope, k.deviceID, pendingCap)
		if err != nil {
			r.l.d.Log.Warn("learning_prune_failed", "device_id", r.dev, "scope", k.scope)
			continue
		}
		r.pruned += int(n)
	}
}

// done counts an op that landed, awaiting approval or not, and publishes it
// without its text.
func (r *run) done(op string, m Memory, awaiting bool) {
	if awaiting {
		r.pending++
		r.pendingIn[scopeKey{m.Scope, m.DeviceID}] = true
	} else {
		r.applied++
	}
	r.ids = append(r.ids, m.ID)
	data := map[string]any{"op": op, "scope": m.Scope, "kind": m.Kind, "memory_id": m.ID, "status": m.Status}
	if m.Replaces != "" {
		data["replaces"] = m.Replaces
	}
	r.l.publish("learning_op", r.dev, data)
}
