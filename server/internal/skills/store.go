package skills

import (
	"context"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/store/db"
)

// DBStore keeps the owner's skills in Postgres.
type DBStore struct{ q *db.Queries }

var _ Store = DBStore{}

func NewDBStore(dbtx db.DBTX) DBStore { return DBStore{q: db.New(dbtx)} }

func (s DBStore) List(ctx context.Context) ([]Entry, error) {
	rows, err := s.q.ListAgentSkills(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(rows))
	for _, r := range rows {
		out = append(out, Entry{
			Skill: agent.Skill{
				Name: r.Name, Description: r.Description, Body: r.Body, Tools: r.Tools, Triggers: r.Triggers, Modes: r.Modes,
			},
			Enabled: r.Enabled, Source: SourceOwner, UpdatedAt: r.UpdatedAt.Time.UTC(),
		})
	}
	return out, nil
}

func (s DBStore) Put(ctx context.Context, e Entry) (bool, time.Time, error) {
	r, err := s.q.PutAgentSkill(ctx, db.PutAgentSkillParams{
		Name: e.Name, Description: e.Description, Body: e.Body, Tools: orEmpty(e.Tools), Triggers: orEmpty(e.Triggers),
		Modes: e.Modes, Enabled: e.Enabled,
	})
	if err != nil {
		return false, time.Time{}, err
	}
	return r.Created, r.UpdatedAt.Time.UTC(), nil
}

func (s DBStore) Delete(ctx context.Context, name string) (bool, error) {
	n, err := s.q.DeleteAgentSkill(ctx, name)
	return n > 0, err
}

// orEmpty keeps a NOT NULL array column from a nil list
func orEmpty(l []string) []string {
	if l == nil {
		return []string{}
	}
	return l
}
