package memory

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"git.shino.boo/devilreef/cadence/server/internal/store/db"
)

type Store struct {
	q    *db.Queries
	pool *pgxpool.Pool // nil in a Locked transaction
}

func New(pool *pgxpool.Pool) *Store { return &Store{q: db.New(pool), pool: pool} }

var errNested = errors.New("memory: locked inside a locked transaction")

// Locked runs fn in one transaction that holds the lock of a scope (deviceID
// empty for global), with a Store that works in it: concurrent adds to one
// scope take turns, so its count, the dedup check and the insert never
// interleave (the cap holds, a household fact is stored once). fn's error
// rolls everything back and is returned. fn must use only the Store it gets.
func (s *Store) Locked(ctx context.Context, scope, deviceID string, fn func(tx *Store) error) error {
	if s.pool == nil {
		return errNested
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return wrap("begin", err)
	}
	// a no-op once committed
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.q.WithTx(tx)
	if err := q.LockMemoryScope(ctx, "memory/"+scope+"/"+deviceID); err != nil {
		return wrap("lock", err)
	}
	if err := fn(&Store{q: q}); err != nil {
		return err
	}
	return wrap("commit", tx.Commit(ctx))
}

// wrap keeps row content out of errors: postgres detail lines quote the failing row.
func wrap(op string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("memory: %s: %w", op, ErrNotFound)
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		if pg.Code == "22000" {
			return fmt.Errorf("memory: %s: %w", op, ErrDimension)
		}
		switch {
		case len(pg.Code) == 5 && pg.Code[:2] == "23", pg.Code == "22P02":
			return fmt.Errorf("memory: %s: %w (%s)", op, ErrInvalid, pg.ConstraintName)
		}
		return fmt.Errorf("memory: %s: db error %s", op, pg.Code)
	}
	return fmt.Errorf("memory: %s: %w", op, err)
}

func (s *Store) Add(ctx context.Context, n NewMemory) (Memory, error) {
	if (len(n.Vector) > 0) != (n.EmbedModel != "") {
		return Memory{}, fmt.Errorf("%w: vector and model go together", ErrInvalid)
	}
	dev, err := optUUID(n.DeviceID)
	if err != nil {
		return Memory{}, err
	}
	replaces, err := optUUID(n.Replaces)
	if err != nil {
		return Memory{}, err
	}
	var vec pgtype.Text
	if len(n.Vector) > 0 {
		t, err := vectorText(n.Vector)
		if err != nil {
			return Memory{}, err
		}
		vec = pgtype.Text{String: t, Valid: true}
	}
	ev := n.Evidence
	if ev == nil {
		ev = []int64{}
	}
	r, err := s.q.AddMemory(ctx, db.AddMemoryParams{
		Scope: n.Scope, DeviceID: dev, Kind: n.Kind, Text: n.Text, Embedding: vec, EmbedModel: optText(n.EmbedModel),
		Pinned: n.Pinned, Confidence: n.Confidence, Evidence: ev, Source: n.Source, Status: n.Status,
		Replaces: replaces, Proposal: optText(n.Proposal),
	})
	if err != nil {
		return Memory{}, wrap("add", err)
	}
	return fromRow(db.ListMemoriesRow(r)), nil
}

func (s *Store) Get(ctx context.Context, id string) (Memory, error) {
	u, err := parseUUID(id)
	if err != nil {
		return Memory{}, fmt.Errorf("memory: get: %w", ErrNotFound)
	}
	r, err := s.q.GetMemory(ctx, u)
	if err != nil {
		return Memory{}, wrap("get", err)
	}
	return fromRow(db.ListMemoriesRow(r)), nil
}

func (s *Store) Update(ctx context.Context, id string, p Patch) (Memory, error) {
	u, err := parseUUID(id)
	if err != nil {
		return Memory{}, fmt.Errorf("memory: update: %w", ErrNotFound)
	}
	if p.Scope == nil && p.DeviceID != "" {
		return Memory{}, fmt.Errorf("%w: device id without scope", ErrInvalid)
	}
	dev, err := optUUID(p.DeviceID)
	if err != nil {
		return Memory{}, err
	}
	arg := db.UpdateMemoryParams{
		ID: u, Text: ptrText(p.Text), Kind: ptrText(p.Kind), Scope: ptrText(p.Scope), DeviceID: dev,
		Status: ptrText(p.Status), Source: ptrText(p.Source), Evidence: p.Evidence,
	}
	if p.Pinned != nil {
		arg.Pinned = pgtype.Bool{Bool: *p.Pinned, Valid: true}
	}
	if p.Confidence != nil {
		arg.Confidence = pgtype.Float4{Float32: *p.Confidence, Valid: true}
	}
	r, err := s.q.UpdateMemory(ctx, arg)
	if err != nil {
		return Memory{}, wrap("update", err)
	}
	return fromRow(db.ListMemoriesRow(r)), nil
}

// UpdateLearned is learning's update of a memory it read as seen: it lands
// only while that memory is still learned, unpinned and of the same status
// and updated_at, and is ErrChanged otherwise. A changed text drops the
// embedding.
func (s *Store) UpdateLearned(ctx context.Context, seen Memory, text, kind string, conf float32, evidence []int64) (Memory, error) {
	u, err := parseUUID(seen.ID)
	if err != nil {
		return Memory{}, fmt.Errorf("memory: update learned: %w", ErrChanged)
	}
	if evidence == nil {
		evidence = []int64{}
	}
	r, err := s.q.UpdateLearnedMemory(ctx, db.UpdateLearnedMemoryParams{
		ID: u, Text: text, Kind: kind, Confidence: conf, Evidence: evidence, Status: seen.Status, Seen: timestamp(seen.UpdatedAt),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Memory{}, fmt.Errorf("memory: update learned: %w", ErrChanged)
	}
	if err != nil {
		return Memory{}, wrap("update learned", err)
	}
	return fromRow(db.ListMemoriesRow(r)), nil
}

// DeleteLearned is learning's delete of a memory it read as seen, under the
// same guard as UpdateLearned.
func (s *Store) DeleteLearned(ctx context.Context, seen Memory) error {
	u, err := parseUUID(seen.ID)
	if err != nil {
		return fmt.Errorf("memory: delete learned: %w", ErrChanged)
	}
	n, err := s.q.DeleteLearnedMemory(ctx, db.DeleteLearnedMemoryParams{ID: u, Status: seen.Status, Seen: timestamp(seen.UpdatedAt)})
	if err != nil {
		return wrap("delete learned", err)
	}
	if n == 0 {
		return fmt.Errorf("memory: delete learned: %w", ErrChanged)
	}
	return nil
}

func (s *Store) SetEmbedding(ctx context.Context, id string, vec []float32, model string) error {
	u, err := parseUUID(id)
	if err != nil {
		return fmt.Errorf("memory: set embedding: %w", ErrNotFound)
	}
	if model == "" {
		return fmt.Errorf("%w: empty model", ErrInvalid)
	}
	t, err := vectorText(vec)
	if err != nil {
		return err
	}
	n, err := s.q.SetMemoryEmbedding(ctx, db.SetMemoryEmbeddingParams{ID: u, Embedding: t, EmbedModel: pgtype.Text{String: model, Valid: true}})
	if err != nil {
		return wrap("set embedding", err)
	}
	if n == 0 {
		return fmt.Errorf("memory: set embedding: %w", ErrNotFound)
	}
	return nil
}

func (s *Store) Delete(ctx context.Context, id string) error {
	u, err := parseUUID(id)
	if err != nil {
		return fmt.Errorf("memory: delete: %w", ErrNotFound)
	}
	n, err := s.q.DeleteMemory(ctx, u)
	if err != nil {
		return wrap("delete", err)
	}
	if n == 0 {
		return fmt.Errorf("memory: delete: %w", ErrNotFound)
	}
	return nil
}

func (s *Store) List(ctx context.Context, f Filter) ([]Memory, error) {
	dev, err := optUUID(f.DeviceID)
	if err != nil {
		return nil, err
	}
	arg := db.ListMemoriesParams{Scope: optText(f.Scope), DeviceID: dev, Kind: optText(f.Kind), Status: optText(f.Status)}
	if f.Pinned != nil {
		arg.Pinned = pgtype.Bool{Bool: *f.Pinned, Valid: true}
	}
	rows, err := s.q.ListMemories(ctx, arg)
	if err != nil {
		return nil, wrap("list", err)
	}
	return fromRows(rows), nil
}

// ListPage is List newest first, one page of it.
func (s *Store) ListPage(ctx context.Context, f Filter, limit, offset int) ([]Memory, error) {
	dev, err := optUUID(f.DeviceID)
	if err != nil {
		return nil, err
	}
	arg := db.ListMemoriesPageParams{
		Scope: optText(f.Scope), DeviceID: dev, Kind: optText(f.Kind), Status: optText(f.Status), MaxRows: i32(limit), Skip: i32(offset),
	}
	if f.Pinned != nil {
		arg.Pinned = pgtype.Bool{Bool: *f.Pinned, Valid: true}
	}
	rows, err := s.q.ListMemoriesPage(ctx, arg)
	if err != nil {
		return nil, wrap("list page", err)
	}
	out := make([]db.ListMemoriesRow, len(rows))
	for i, r := range rows {
		out[i] = db.ListMemoriesRow(r)
	}
	return fromRows(out), nil
}

// DeviceExists reports whether a device has this id; a malformed id has none.
func (s *Store) DeviceExists(ctx context.Context, id string) (bool, error) {
	u, err := parseUUID(id)
	if err != nil {
		return false, nil
	}
	if _, err := s.q.DeviceByID(ctx, u); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, wrap("device", err)
	}
	return true, nil
}

// Nearest returns active memories of one embedding model, most similar first
// (cosine similarity, at least minSimilarity).
func (s *Store) Nearest(ctx context.Context, vec []float32, model string, sc Scopes, limit int, minSimilarity float64) ([]Scored, error) {
	return s.nearest(ctx, vec, model, sc, limit, minSimilarity, false)
}

// NearestWithPending is Nearest with the pending memories too, proposals left out.
func (s *Store) NearestWithPending(ctx context.Context, vec []float32, model string, sc Scopes, limit int, minSimilarity float64) ([]Scored, error) {
	return s.nearest(ctx, vec, model, sc, limit, minSimilarity, true)
}

func (s *Store) nearest(ctx context.Context, vec []float32, model string, sc Scopes, limit int, minSimilarity float64, withPending bool) ([]Scored, error) {
	t, err := vectorText(vec)
	if err != nil {
		return nil, err
	}
	dev, err := optUUID(sc.DeviceID)
	if err != nil {
		return nil, err
	}
	rows, err := s.q.NearestMemories(ctx, db.NearestMemoriesParams{
		Vec: t, WithPending: withPending, Model: pgtype.Text{String: model, Valid: true}, IncludeGlobal: sc.Global, DeviceID: dev,
		MinSimilarity: minSimilarity, MaxRows: i32(limit),
	})
	if err != nil {
		return nil, wrap("nearest", err)
	}
	out := make([]Scored, len(rows))
	for i, r := range rows {
		out[i] = Scored{Memory: fromRow(db.ListMemoriesRow{
			ID: r.ID, Scope: r.Scope, DeviceID: r.DeviceID, Kind: r.Kind, Text: r.Text, EmbedModel: r.EmbedModel,
			HasEmbedding: r.HasEmbedding, Pinned: r.Pinned, Hits: r.Hits, LastHitAt: r.LastHitAt, Confidence: r.Confidence,
			Evidence: r.Evidence, Source: r.Source, Status: r.Status, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
			Replaces: r.Replaces, Proposal: r.Proposal,
		}), Similarity: r.Similarity}
	}
	return out, nil
}

func (s *Store) Pinned(ctx context.Context, sc Scopes) ([]Memory, error) {
	dev, err := optUUID(sc.DeviceID)
	if err != nil {
		return nil, err
	}
	rows, err := s.q.PinnedMemories(ctx, db.PinnedMemoriesParams{IncludeGlobal: sc.Global, DeviceID: dev})
	if err != nil {
		return nil, wrap("pinned", err)
	}
	out := make([]db.ListMemoriesRow, len(rows))
	for i, r := range rows {
		out[i] = db.ListMemoriesRow(r)
	}
	return fromRows(out), nil
}

func (s *Store) Hit(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	us, err := uuids(ids)
	if err != nil {
		return err
	}
	_, err = s.q.HitMemories(ctx, us)
	return wrap("hit", err)
}

// PrunePending keeps the newest keep pending learned memories of a scope
// (deviceID empty for global) and returns how many went.
func (s *Store) PrunePending(ctx context.Context, scope, deviceID string, keep int) (int64, error) {
	dev, err := optUUID(deviceID)
	if err != nil {
		return 0, err
	}
	n, err := s.q.PrunePendingMemories(ctx, db.PrunePendingMemoriesParams{Scope: scope, DeviceID: dev, Keep: i32(keep)})
	return n, wrap("prune pending", err)
}

// DeleteProposals deletes the pending proposals for a memory.
func (s *Store) DeleteProposals(ctx context.Context, id string) (int64, error) {
	u, err := parseUUID(id)
	if err != nil {
		return 0, err
	}
	n, err := s.q.DeleteMemoryProposals(ctx, u)
	return n, wrap("delete proposals", err)
}

// CountByScope counts active memories; deviceID is empty for global.
func (s *Store) CountByScope(ctx context.Context, scope, deviceID string) (int, error) {
	dev, err := optUUID(deviceID)
	if err != nil {
		return 0, err
	}
	n, err := s.q.CountMemoriesByScope(ctx, db.CountMemoriesByScopeParams{Scope: scope, DeviceID: dev})
	return int(n), wrap("count", err)
}

// EvictionCandidates lists non-pinned active learned memories other than
// keep, lowest hits x 0.5^(age_days/14) first.
func (s *Store) EvictionCandidates(ctx context.Context, scope, deviceID string, n int, keep []string) ([]Memory, error) {
	dev, err := optUUID(deviceID)
	if err != nil {
		return nil, err
	}
	ks, err := uuids(keep)
	if err != nil {
		return nil, err
	}
	rows, err := s.q.MemoryEvictionCandidates(ctx, db.MemoryEvictionCandidatesParams{Keep: ks, Scope: scope, DeviceID: dev, MaxRows: i32(n)})
	if err != nil {
		return nil, wrap("eviction candidates", err)
	}
	out := make([]db.ListMemoriesRow, len(rows))
	for i, r := range rows {
		out[i] = db.ListMemoriesRow(r)
	}
	return fromRows(out), nil
}

// StaleModel lists memories not embedded with model (including none), for re-embedding.
func (s *Store) StaleModel(ctx context.Context, model string, limit int) ([]Memory, error) {
	rows, err := s.q.StaleModelMemories(ctx, db.StaleModelMemoriesParams{Model: model, MaxRows: i32(limit)})
	if err != nil {
		return nil, wrap("stale model", err)
	}
	out := make([]db.ListMemoriesRow, len(rows))
	for i, r := range rows {
		out[i] = db.ListMemoriesRow(r)
	}
	return fromRows(out), nil
}

// SetEmbeddingIfText is SetEmbedding for a vector made from text: a memory
// edited since (its text differs) is left alone and reported ErrNotFound, so
// a stale vector never lands under the current model.
func (s *Store) SetEmbeddingIfText(ctx context.Context, id, text string, vec []float32, model string) error {
	u, err := parseUUID(id)
	if err != nil {
		return fmt.Errorf("memory: set embedding: %w", ErrNotFound)
	}
	if model == "" {
		return fmt.Errorf("%w: empty model", ErrInvalid)
	}
	t, err := vectorText(vec)
	if err != nil {
		return err
	}
	n, err := s.q.SetMemoryEmbeddingIfText(ctx, db.SetMemoryEmbeddingIfTextParams{
		ID: u, Embedding: t, EmbedModel: pgtype.Text{String: model, Valid: true}, Text: text,
	})
	if err != nil {
		return wrap("set embedding", err)
	}
	if n == 0 {
		return fmt.Errorf("memory: set embedding: %w", ErrNotFound)
	}
	return nil
}
