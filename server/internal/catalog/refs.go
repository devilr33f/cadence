package catalog

import (
	"container/list"
	"context"
	"crypto/rand"
	"encoding/base64"
	"sync"
	"time"
)

const (
	// refTTL is how long a ref lives since it was last issued or looked up
	refTTL     = 12 * time.Hour
	maxRefs    = 200_000
	refBytes   = 12
	refsPrefix = "c:"
)

// RefTable is the service's in-memory RefIssuer (D11): random refs per
// household and Key, alive for refTTL since their last use, at most maxRefs
// (the least recently used go first).
type RefTable struct {
	now func() time.Time

	mu    sync.Mutex
	byRef map[Ref]*list.Element
	byKey map[string]Ref
	lru   *list.List
}

type refEntry struct {
	ref       Ref
	household string
	key       Key
	used      time.Time
}

var _ RefIssuer = (*RefTable)(nil)

// NewRefTable makes an empty table; now nil is time.Now.
func NewRefTable(now func() time.Time) *RefTable {
	if now == nil {
		now = time.Now
	}
	return &RefTable{now: now, byRef: map[Ref]*list.Element{}, byKey: map[string]Ref{}, lru: list.New()}
}

func refID(household string, k Key) string { return household + "\x1e" + k.String() }

func (t *RefTable) Issue(_ context.Context, household string, k Key) (Ref, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	id := refID(household, k)
	if r, ok := t.byKey[id]; ok {
		el := t.byRef[r]
		if e := el.Value.(*refEntry); now.Sub(e.used) < refTTL {
			e.used = now
			t.lru.MoveToFront(el)
			return r, nil
		}
		t.drop(el)
	}
	b := make([]byte, refBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	r := Ref(refsPrefix + base64.RawURLEncoding.EncodeToString(b))
	t.byRef[r] = t.lru.PushFront(&refEntry{ref: r, household: household, key: k, used: now})
	t.byKey[id] = r
	for t.lru.Len() > maxRefs {
		t.drop(t.lru.Back())
	}
	return r, nil
}

func (t *RefTable) Lookup(_ context.Context, household string, r Ref) (Key, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	el, ok := t.byRef[r]
	if !ok {
		return Key{}, ErrUnknownRef
	}
	e := el.Value.(*refEntry)
	now := t.now()
	if e.household != household {
		return Key{}, ErrUnknownRef
	}
	if now.Sub(e.used) >= refTTL {
		t.drop(el)
		return Key{}, ErrUnknownRef
	}
	e.used = now
	t.lru.MoveToFront(el)
	return e.key, nil
}

func (t *RefTable) drop(el *list.Element) {
	e := t.lru.Remove(el).(*refEntry)
	delete(t.byRef, e.ref)
	delete(t.byKey, refID(e.household, e.key))
}
