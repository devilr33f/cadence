package memory

import (
	"context"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/store/db"
)

func (s *Store) AddCompaction(ctx context.Context, n NewCompaction) (Compaction, error) {
	dev, err := parseUUID(n.DeviceID)
	if err != nil {
		return Compaction{}, err
	}
	threads, refs := n.OpenThreads, n.TaskRefs
	if threads == nil {
		threads = []byte("[]")
	}
	if refs == nil {
		refs = []byte("[]")
	}
	r, err := s.q.AddCompaction(ctx, db.AddCompactionParams{
		DeviceID: dev, UptoMessageID: n.UptoMessageID, Summary: n.Summary, OpenThreads: threads, TaskRefs: refs, Model: n.Model,
	})
	if err != nil {
		return Compaction{}, wrap("add compaction", err)
	}
	return fromCompaction(r), nil
}

// ConversationStart is the id the device's current prompt context starts after.
func (s *Store) ConversationStart(ctx context.Context, deviceID string) (int64, error) {
	dev, err := parseUUID(deviceID)
	if err != nil {
		return 0, err
	}
	id, err := s.q.ConversationStart(ctx, dev)
	return id, wrap("conversation start", err)
}

func (s *Store) LatestCompaction(ctx context.Context, deviceID string) (Compaction, error) {
	dev, err := parseUUID(deviceID)
	if err != nil {
		return Compaction{}, err
	}
	r, err := s.q.LatestCompaction(ctx, dev)
	if err != nil {
		return Compaction{}, wrap("latest compaction", err)
	}
	return fromCompaction(r), nil
}

// ListCompactions returns the newest first.
func (s *Store) ListCompactions(ctx context.Context, deviceID string, limit int) ([]Compaction, error) {
	dev, err := parseUUID(deviceID)
	if err != nil {
		return nil, err
	}
	rows, err := s.q.ListCompactions(ctx, db.ListCompactionsParams{DeviceID: dev, MaxRows: i32(limit)})
	if err != nil {
		return nil, wrap("list compactions", err)
	}
	out := make([]Compaction, len(rows))
	for i, r := range rows {
		out[i] = fromCompaction(r)
	}
	return out, nil
}

// PruneCompactions keeps the device's newest keep compactions and returns how many went.
func (s *Store) PruneCompactions(ctx context.Context, deviceID string, keep int) (int64, error) {
	dev, err := parseUUID(deviceID)
	if err != nil {
		return 0, err
	}
	n, err := s.q.PruneCompactions(ctx, db.PruneCompactionsParams{DeviceID: dev, Keep: i32(keep)})
	return n, wrap("prune compactions", err)
}

// Candidate is a device with messages committed after its latest compaction.
type Candidate struct {
	DeviceID string
	NewestID int64
	NewestAt time.Time
}

// Candidates lists the devices with messages after their latest compaction.
func (s *Store) Candidates(ctx context.Context) ([]Candidate, error) {
	rows, err := s.q.CompactionCandidates(ctx)
	if err != nil {
		return nil, wrap("candidates", err)
	}
	out := make([]Candidate, len(rows))
	for i, r := range rows {
		out[i] = Candidate{DeviceID: r.DeviceID.String(), NewestID: r.NewestID, NewestAt: r.NewestAt.Time}
	}
	return out, nil
}

// UncompactedChars is the length of the device's messages after its latest
// compaction, as stored.
func (s *Store) UncompactedChars(ctx context.Context, deviceID string) (int64, error) {
	dev, err := parseUUID(deviceID)
	if err != nil {
		return 0, err
	}
	n, err := s.q.UncompactedChars(ctx, dev)
	return n, wrap("uncompacted chars", err)
}

func (s *Store) AddRun(ctx context.Context, n NewRun) (Run, error) {
	comp, err := parseUUID(n.CompactionID)
	if err != nil {
		return Run{}, err
	}
	dev, err := parseUUID(n.DeviceID)
	if err != nil {
		return Run{}, err
	}
	ids, err := uuids(n.MemoryIDs)
	if err != nil {
		return Run{}, err
	}
	r, err := s.q.AddLearningRun(ctx, db.AddLearningRunParams{
		CompactionID: comp, DeviceID: dev, Applied: i32(n.Applied), Dropped: i32(n.Dropped), Pending: i32(n.Pending), MemoryIds: ids,
	})
	if err != nil {
		return Run{}, wrap("add run", err)
	}
	return fromRun(r), nil
}

// ListRuns returns the newest first.
func (s *Store) ListRuns(ctx context.Context, deviceID string, limit int) ([]Run, error) {
	dev, err := parseUUID(deviceID)
	if err != nil {
		return nil, err
	}
	rows, err := s.q.ListLearningRuns(ctx, db.ListLearningRunsParams{DeviceID: dev, MaxRows: i32(limit)})
	if err != nil {
		return nil, wrap("list runs", err)
	}
	out := make([]Run, len(rows))
	for i, r := range rows {
		out[i] = fromRun(r)
	}
	return out, nil
}

func fromCompaction(r db.ConversationCompaction) Compaction {
	return Compaction{ID: r.ID.String(), DeviceID: r.DeviceID.String(), UptoMessageID: r.UptoMessageID, Summary: r.Summary,
		OpenThreads: r.OpenThreads, TaskRefs: r.TaskRefs, Model: r.Model, CreatedAt: r.CreatedAt.Time}
}

func fromRun(r db.MemoryLearningRun) Run {
	ids := make([]string, len(r.MemoryIds))
	for i, id := range r.MemoryIds {
		ids[i] = id.String()
	}
	return Run{ID: r.ID.String(), CompactionID: r.CompactionID.String(), DeviceID: r.DeviceID.String(), Applied: int(r.Applied),
		Dropped: int(r.Dropped), Pending: int(r.Pending), MemoryIDs: ids, CreatedAt: r.CreatedAt.Time}
}

func fromRow(r db.ListMemoriesRow) Memory {
	m := Memory{
		ID: r.ID.String(), Scope: r.Scope, Kind: r.Kind, Text: r.Text, EmbedModel: r.EmbedModel.String,
		HasEmbedding: r.HasEmbedding, Pinned: r.Pinned, Hits: int(r.Hits), Confidence: r.Confidence, Evidence: r.Evidence,
		Source: r.Source, Status: r.Status, CreatedAt: r.CreatedAt.Time, UpdatedAt: r.UpdatedAt.Time,
	}
	if r.DeviceID.Valid {
		m.DeviceID = r.DeviceID.String()
	}
	if r.LastHitAt.Valid {
		t := r.LastHitAt.Time
		m.LastHitAt = &t
	}
	if r.Replaces.Valid {
		m.Replaces, m.Proposal = r.Replaces.String(), r.Proposal.String
	}
	return m
}

func fromRows(rows []db.ListMemoriesRow) []Memory {
	out := make([]Memory, len(rows))
	for i, r := range rows {
		out[i] = fromRow(r)
	}
	return out
}
