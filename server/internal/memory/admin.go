package memory

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"git.shino.boo/devilreef/cadence/server/internal/events"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

const (
	maxTextChars = 500

	codeInvalid = "invalid"
	codeSecret  = "looks_like_secret" //nolint:gosec // an error code, no credential
)

// ErrConflict: the memory is not in a state the action works on, or doing it
// would break a rule (the cap, a manual or pinned target).
var ErrConflict = errors.New("memory: conflict")

// InvalidError names the field of a request that is wrong; Code is "invalid"
// or "looks_like_secret". It never carries the value.
type InvalidError struct{ Code, Path string }

func (e *InvalidError) Error() string { return "memory: " + e.Code + ": " + e.Path }

func invalid(path string) error { return &InvalidError{Code: codeInvalid, Path: path} }

type AdminDeps struct {
	Store *Store
	// Embedder is the instance memories are embedded with, or an error while
	// there is none: a memory then waits for the re-embed job.
	Embedder func(ctx context.Context) (providers.Embedder, error)
	Bus      *events.Bus
	Log      *slog.Logger
	Now      func() time.Time
}

// Admin is the dashboard's way to read and change memories (M5a-D6).
type Admin struct {
	d      AdminDeps
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func NewAdmin(d AdminDeps) *Admin {
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Admin{d: d, ctx: ctx, cancel: cancel}
}

// Close stops the embeddings in flight and waits for them.
func (a *Admin) Close() {
	a.cancel()
	a.wg.Wait()
}

// Create is a new manual memory.
type Create struct{ Scope, DeviceID, Kind, Text string }

// Edit changes only the fields set. A Scope change takes DeviceID with it.
type Edit struct {
	Text     *string
	Kind     *string
	Scope    *string
	DeviceID *string
	Pinned   *bool
}

func (a *Admin) validScope(ctx context.Context, scope, deviceID string) error {
	switch scope {
	case ScopeGlobal:
		if deviceID != "" {
			return invalid("deviceId")
		}
	case ScopeDevice:
		ok, err := a.d.Store.DeviceExists(ctx, deviceID)
		if err != nil {
			return err
		}
		if !ok {
			return invalid("deviceId")
		}
	default:
		return invalid("scope")
	}
	return nil
}

func validKind(kind string) error {
	switch kind {
	case KindPreference, KindFact, KindAlias, KindLesson:
		return nil
	}
	return invalid("kind")
}

func validText(text string) error {
	// postgres refuses NUL and invalid utf-8 in text with an error that is no client mistake
	if strings.TrimSpace(text) == "" || utf8.RuneCountInString(text) > maxTextChars || !utf8.ValidString(text) || strings.ContainsRune(text, 0) {
		return invalid("text")
	}
	if looksSecret(text) {
		return &InvalidError{Code: codeSecret, Path: "text"}
	}
	return nil
}

// Create stores a manual, active memory and embeds it in the background:
// until then recall sees it only if it is pinned.
func (a *Admin) Create(ctx context.Context, c Create) (Memory, error) {
	if err := a.validScope(ctx, c.Scope, c.DeviceID); err != nil {
		return Memory{}, err
	}
	if err := validKind(c.Kind); err != nil {
		return Memory{}, err
	}
	if err := validText(c.Text); err != nil {
		return Memory{}, err
	}
	var m Memory
	err := a.d.Store.Locked(ctx, c.Scope, c.DeviceID, func(s *Store) error {
		if err := room(ctx, s, c.Scope, c.DeviceID, nil); err != nil {
			return err
		}
		var err error
		m, err = s.Add(ctx, NewMemory{
			Scope: c.Scope, DeviceID: c.DeviceID, Kind: c.Kind, Text: c.Text, Confidence: 1, Source: SourceManual, Status: StatusActive,
		})
		return err
	})
	if err != nil {
		return Memory{}, err
	}
	a.embedAsync(m)
	a.changed(m, "created")
	return m, nil
}

// room evicts learned memories until the scope has space, in a transaction
// that holds the scope's lock; ErrConflict when only protected ones are left.
func room(ctx context.Context, s *Store, scope, deviceID string, keep []string) error {
	_, fits, err := s.MakeRoom(ctx, scope, deviceID, keep)
	if err != nil {
		return err
	}
	if !fits {
		return ErrConflict
	}
	return nil
}

// Edit applies a patch. An edit of the text, kind or scope of a learned
// memory makes it manual, so learning never overwrites it; one that changes
// the text re-queues its embedding.
func (a *Admin) Edit(ctx context.Context, id string, e Edit) (Memory, error) {
	cur, err := a.d.Store.Get(ctx, id)
	if err != nil {
		return Memory{}, err
	}
	if cur.Replaces != "" {
		return Memory{}, ErrConflict
	}
	p := Patch{Pinned: e.Pinned}
	newScope, newDev := cur.Scope, cur.DeviceID
	switch {
	case e.Scope != nil:
		dev := ""
		if e.DeviceID != nil {
			dev = *e.DeviceID
		}
		if err := a.validScope(ctx, *e.Scope, dev); err != nil {
			return Memory{}, err
		}
		newScope, newDev = *e.Scope, dev
	case e.DeviceID != nil:
		return Memory{}, invalid("deviceId")
	}
	scopeChanged := newScope != cur.Scope || newDev != cur.DeviceID
	if scopeChanged {
		p.Scope, p.DeviceID = &newScope, newDev
	}
	if e.Kind != nil {
		if err := validKind(*e.Kind); err != nil {
			return Memory{}, err
		}
		if *e.Kind != cur.Kind {
			p.Kind = e.Kind
		}
	}
	textChanged := false
	if e.Text != nil {
		if err := validText(*e.Text); err != nil {
			return Memory{}, err
		}
		if textChanged = *e.Text != cur.Text; textChanged {
			p.Text = e.Text
		}
	}
	if cur.Source == SourceLearned && (scopeChanged || textChanged || p.Kind != nil) {
		manual := SourceManual
		p.Source = &manual
	}
	var m Memory
	if scopeChanged && cur.Status == StatusActive {
		// a move adds an active memory to its new scope
		err = a.d.Store.Locked(ctx, newScope, newDev, func(s *Store) error {
			if err := room(ctx, s, newScope, newDev, []string{cur.ID}); err != nil {
				return err
			}
			var err error
			m, err = s.Update(ctx, id, p)
			return err
		})
	} else {
		m, err = a.d.Store.Update(ctx, id, p)
	}
	if err != nil {
		return Memory{}, err
	}
	if m.Status == StatusActive && (textChanged || scopeChanged) {
		// an older proposal, approved later, would overwrite the new text
		if _, err := a.d.Store.DeleteProposals(ctx, m.ID); err != nil {
			a.d.Log.Warn("memory_proposals_failed", "memory_id", m.ID)
		}
	}
	if textChanged {
		a.embedAsync(m)
	}
	a.changed(m, "updated")
	return m, nil
}

func (a *Admin) Delete(ctx context.Context, id string) error {
	m, err := a.d.Store.Get(ctx, id)
	if err != nil {
		return err
	}
	if err := a.d.Store.Delete(ctx, id); err != nil {
		return err
	}
	a.changed(m, "deleted")
	return nil
}

// Approve makes a pending add active, applies an update proposal to the
// memory it names, or carries out a delete proposal. The bool says whether
// the returned memory is there: it is not after a delete.
func (a *Admin) Approve(ctx context.Context, id string) (Memory, bool, error) {
	m, err := a.d.Store.Get(ctx, id)
	if err != nil {
		return Memory{}, false, err
	}
	if m.Status != StatusPending {
		return Memory{}, false, ErrConflict
	}
	// learned text is checked again at approval: the filter may have grown since it was stored
	if m.Proposal != ProposalDelete {
		if err := validText(m.Text); err != nil {
			return Memory{}, false, err
		}
	}
	switch m.Proposal {
	case "":
		var out Memory
		err := a.d.Store.Locked(ctx, m.Scope, m.DeviceID, func(s *Store) error {
			if err := room(ctx, s, m.Scope, m.DeviceID, []string{m.ID}); err != nil {
				return err
			}
			active := StatusActive
			var err error
			out, err = s.Update(ctx, id, Patch{Status: &active})
			return err
		})
		if err != nil {
			return Memory{}, false, err
		}
		a.changed(out, "approved")
		return out, true, nil
	case ProposalUpdate:
		t, err := a.target(ctx, m)
		if err != nil {
			return Memory{}, false, err
		}
		text, kind, conf := m.Text, m.Kind, m.Confidence
		out, err := a.d.Store.Update(ctx, t.ID, Patch{Text: &text, Kind: &kind, Confidence: &conf, Evidence: m.Evidence})
		if err != nil {
			return Memory{}, false, err
		}
		if _, err := a.d.Store.DeleteProposals(ctx, t.ID); err != nil {
			a.d.Log.Warn("memory_proposals_failed", "memory_id", t.ID)
		}
		a.embedAsync(out)
		a.changed(out, "approved")
		return out, true, nil
	default:
		t, err := a.target(ctx, m)
		if err != nil {
			return Memory{}, false, err
		}
		// the proposal goes with its target (cascade)
		if err := a.d.Store.Delete(ctx, t.ID); err != nil {
			return Memory{}, false, err
		}
		a.changed(t, "approved")
		return Memory{}, false, nil
	}
}

// target is the memory a proposal names, if learning may still change it.
func (a *Admin) target(ctx context.Context, proposal Memory) (Memory, error) {
	t, err := a.d.Store.Get(ctx, proposal.Replaces)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return Memory{}, ErrConflict
		}
		return Memory{}, err
	}
	if untouchable(t) != "" {
		return Memory{}, ErrConflict
	}
	return t, nil
}

// Reject removes a pending memory or proposal.
func (a *Admin) Reject(ctx context.Context, id string) error {
	m, err := a.d.Store.Get(ctx, id)
	if err != nil {
		return err
	}
	if m.Status != StatusPending {
		return ErrConflict
	}
	if err := a.d.Store.Delete(ctx, id); err != nil {
		return err
	}
	a.changed(m, "rejected")
	return nil
}

func (a *Admin) changed(m Memory, action string) {
	a.d.Log.Info("memory_admin", "memory_id", m.ID, "action", action)
	if a.d.Bus != nil {
		a.d.Bus.Publish(events.Event{
			Type: events.MemoryChanged, DeviceID: m.DeviceID, At: a.d.Now(),
			Data: map[string]any{"id": m.ID, "action": action},
		})
	}
}

// embedAsync gives m a vector in the background; a failure is the re-embed
// job's to make good, so it is only logged, by code.
func (a *Admin) embedAsync(m Memory) {
	if a.d.Embedder == nil {
		return
	}
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		ctx, cancel := context.WithTimeout(a.ctx, reembedTimeout)
		defer cancel()
		emb, err := a.d.Embedder(ctx)
		if err != nil || emb == nil {
			return
		}
		vecs, err := emb.Embed(ctx, []string{m.Text})
		if err == nil && len(vecs) != 1 {
			err = errors.New(providers.CodeEmbedFailed)
		}
		if err == nil {
			err = a.d.Store.SetEmbeddingIfText(ctx, m.ID, m.Text, vecs[0], emb.Model())
		}
		if err != nil && !errors.Is(err, ErrNotFound) && a.ctx.Err() == nil {
			a.d.Log.Warn("memory_embed_failed", "memory_id", m.ID, "code", reembedCode(err))
		}
	}()
}

// Compactions lists a device's newest first; ErrNotFound for an unknown device.
func (a *Admin) Compactions(ctx context.Context, deviceID string, limit int) ([]Compaction, error) {
	if err := a.deviceKnown(ctx, deviceID); err != nil {
		return nil, err
	}
	return a.d.Store.ListCompactions(ctx, deviceID, limit)
}

// Runs lists a device's learning runs newest first; ErrNotFound for an unknown device.
func (a *Admin) Runs(ctx context.Context, deviceID string, limit int) ([]Run, error) {
	if err := a.deviceKnown(ctx, deviceID); err != nil {
		return nil, err
	}
	return a.d.Store.ListRuns(ctx, deviceID, limit)
}

func (a *Admin) deviceKnown(ctx context.Context, id string) error {
	ok, err := a.d.Store.DeviceExists(ctx, id)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotFound
	}
	return nil
}

// List is one page of memories, newest first.
func (a *Admin) List(ctx context.Context, f Filter, limit, offset int) ([]Memory, error) {
	return a.d.Store.ListPage(ctx, f, limit, offset)
}
