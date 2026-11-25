package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"sync"

	"github.com/jackc/pgx/v5"

	"git.shino.boo/devilreef/cadence/server/internal/store/db"
)

// ChoiceStore keeps the service's own remembered choices, by an opaque title
// id (choiceID): a title group's source, and every choice of a provider
// that is no ChoiceMemory itself.
type ChoiceStore interface {
	Recall(ctx context.Context, title []byte) (Selection, error)
	Remember(ctx context.Context, title []byte, s Selection) error
}

// choiceID hides the household and key behind a hash
func choiceID(household string, k Key) []byte {
	h := sha256.Sum256([]byte(household + "\x1e" + k.String()))
	return h[:]
}

// MemoryChoices is a ChoiceStore in memory, for tests and a server without
// a database.
type MemoryChoices struct {
	mu sync.Mutex
	m  map[string]Selection
}

func (c *MemoryChoices) Recall(_ context.Context, title []byte) (Selection, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return Selection{}.Merge(c.m[string(title)]), nil
}

func (c *MemoryChoices) Remember(_ context.Context, title []byte, s Selection) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]Selection{}
	}
	c.m[string(title)] = Selection{}.Merge(s)
	return nil
}

// DBChoices is the ChoiceStore in catalog_choices.
type DBChoices struct{ q *db.Queries }

// NewDBChoices keeps choices through q.
func NewDBChoices(q *db.Queries) *DBChoices { return &DBChoices{q: q} }

func (c *DBChoices) Recall(ctx context.Context, title []byte) (Selection, error) {
	b, err := c.q.GetCatalogChoice(ctx, title)
	if errors.Is(err, pgx.ErrNoRows) {
		return Selection{}, nil
	}
	if err != nil {
		return nil, err
	}
	s := Selection{}
	if err := json.Unmarshal(b, &s); err != nil {
		return Selection{}, nil
	}
	return s, nil
}

func (c *DBChoices) Remember(ctx context.Context, title []byte, s Selection) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return c.q.PutCatalogChoice(ctx, db.PutCatalogChoiceParams{Title: title, Selection: b})
}
