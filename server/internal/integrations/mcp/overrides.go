package mcp

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"git.shino.boo/devilreef/cadence/server/internal/store/db"
)

// Retention keeps the rows of an instance gone, their overrides with them,
// this long after a prune first found it gone.
const Retention = 30 * 24 * time.Hour

const (
	EffectsRead    = "read"
	EffectsWrite   = "write"
	TrustTrusted   = "trusted"
	TrustUntrusted = "untrusted"
)

var (
	ErrToolNotFound = errors.New("mcp: tool not found")
	// ErrStaleAck: the hash acknowledged is not the latest one listed
	ErrStaleAck = errors.New("mcp: acknowledged hash is not the latest")
	ErrBadPatch = errors.New("mcp: effects must be read or write, trust trusted or untrusted")
	errStore    = errors.New("mcp: tool store failed")
)

// Override is the owner's say over one tool, as stored: Write, Untrusted,
// off by default and woken-only until marked otherwise. Changed is a Hash
// past AckedHash: the tool is withdrawn until the owner acknowledges it.
type Override struct {
	Local                   string
	Effects, Trust          string
	EnabledDefault, AnyTurn bool
	Hash, AckedHash         string
	Changed                 bool
	FirstSeen, LastSeen     time.Time
}

// Patch changes the overrides it sets; a non-empty Ack acknowledges that
// hash, which must be the latest one listed.
type Patch struct {
	Effects, Trust          *string
	EnabledDefault, AnyTurn *bool
	Ack                     string
}

// Store keeps the overrides in mcp_tools. Its errors are fixed: a driver
// error can quote a tool's name.
type Store struct{ q *db.Queries }

func NewStore(q *db.Queries) *Store { return &Store{q: q} }

type seen struct{ remote, local, hash string }

// names is every stored local name, gone tools included: the instance's by
// remote name, and the set the other instances hold.
func (s *Store) names(ctx context.Context, instance string) (own map[string]string, others map[string]bool, err error) {
	rows, err := s.q.ListMCPToolNames(ctx)
	if err != nil {
		return nil, nil, errStore
	}
	own, others = map[string]string{}, map[string]bool{}
	for _, r := range rows {
		if r.Instance == instance {
			own[r.RemoteName] = r.LocalName
		} else {
			others[r.LocalName] = true
		}
	}
	return own, others, nil
}

// see records a listing: new tools get the defaults, known ones their latest
// hash and name. It returns the overrides of the tools seen.
func (s *Store) see(ctx context.Context, instance string, tools []seen) (map[string]Override, error) {
	out := make(map[string]Override, len(tools))
	if len(tools) == 0 {
		return out, nil
	}
	p := db.SeeMCPToolsParams{Instance: instance}
	for _, t := range tools {
		p.RemoteNames = append(p.RemoteNames, t.remote)
		p.LocalNames = append(p.LocalNames, t.local)
		p.SchemaHashes = append(p.SchemaHashes, t.hash)
	}
	rows, err := s.q.SeeMCPTools(ctx, p)
	if err != nil {
		return nil, errStore
	}
	for _, r := range rows {
		out[r.RemoteName] = override(r)
	}
	return out, nil
}

// Update applies p to a tool's overrides and returns them.
func (s *Store) Update(ctx context.Context, instance, remote string, p Patch) (Override, error) {
	if (p.Effects != nil && *p.Effects != EffectsRead && *p.Effects != EffectsWrite) ||
		(p.Trust != nil && *p.Trust != TrustTrusted && *p.Trust != TrustUntrusted) {
		return Override{}, ErrBadPatch
	}
	text := func(v *string) pgtype.Text {
		if v == nil {
			return pgtype.Text{}
		}
		return pgtype.Text{String: *v, Valid: true}
	}
	flag := func(v *bool) pgtype.Bool {
		if v == nil {
			return pgtype.Bool{}
		}
		return pgtype.Bool{Bool: *v, Valid: true}
	}
	r, err := s.q.UpdateMCPTool(ctx, db.UpdateMCPToolParams{
		Effects: text(p.Effects), Trust: text(p.Trust), EnabledDefault: flag(p.EnabledDefault), AnyTurn: flag(p.AnyTurn),
		Ack: p.Ack, Instance: instance, RemoteName: remote,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// no row, or one whose latest hash is not the one acknowledged
		if _, err := s.q.GetMCPTool(ctx, db.GetMCPToolParams{Instance: instance, RemoteName: remote}); errors.Is(err, pgx.ErrNoRows) {
			return Override{}, ErrToolNotFound
		} else if err != nil {
			return Override{}, errStore
		}
		return Override{}, ErrStaleAck
	}
	if err != nil {
		return Override{}, errStore
	}
	return override(r), nil
}

// ack acknowledges an instance's tools at the hashes given, remote name to
// hash, each or none: ErrStaleAck, with nothing changed, unless every hash is
// the latest one listed. It returns their overrides.
func (s *Store) ack(ctx context.Context, instance string, hashes map[string]string) (map[string]Override, error) {
	p := db.AckMCPToolsParams{Instance: instance}
	for remote, hash := range hashes {
		p.RemoteNames = append(p.RemoteNames, remote)
		p.Hashes = append(p.Hashes, hash)
	}
	rows, err := s.q.AckMCPTools(ctx, p)
	if err != nil {
		return nil, errStore
	}
	if len(rows) != len(hashes) {
		return nil, ErrStaleAck
	}
	out := make(map[string]Override, len(rows))
	for _, r := range rows {
		out[r.RemoteName] = override(r)
	}
	return out, nil
}

// Prune notes which instances are gone and deletes the rows of those gone
// keep. An instance that exists keeps every row, however long its tool or
// its server was away, so a tool never comes back seen afresh and
// acknowledged (Ruling M5d-R11).
func (s *Store) Prune(ctx context.Context, keep time.Duration) (int64, error) {
	if err := s.q.MarkGoneMCPTools(ctx); err != nil {
		return 0, errStore
	}
	n, err := s.q.PruneMCPTools(ctx, keep.Seconds())
	if err != nil {
		return 0, errStore
	}
	return n, nil
}

// RunRetention prunes at start and then every interval until ctx ends; stop
// waits for the loop.
func (s *Store) RunRetention(ctx context.Context, log *slog.Logger, every time.Duration) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	prune := func() {
		n, err := s.Prune(ctx, Retention)
		switch {
		case ctx.Err() != nil:
		case err != nil:
			log.Error("mcp_tools_prune_failed")
		default:
			log.Info("mcp_tools_pruned", "count", n)
		}
	}
	go func() {
		defer close(done)
		prune()
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				prune()
			}
		}
	}()
	return func() { cancel(); <-done }
}

func override(r db.McpTool) Override {
	return Override{
		Local: r.LocalName, Effects: r.Effects, Trust: r.Trust, EnabledDefault: r.EnabledDefault, AnyTurn: r.AnyTurn,
		Hash: r.SchemaHash, AckedHash: r.AckedHash, Changed: r.Changed, FirstSeen: r.FirstSeen.Time, LastSeen: r.LastSeen.Time,
	}
}
