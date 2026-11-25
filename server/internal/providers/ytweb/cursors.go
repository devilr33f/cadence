package ytweb

import (
	"crypto/rand"
	"encoding/base64"
	"sync"
	"time"
)

const (
	cursorTTL  = 30 * time.Minute
	maxCursors = 4096
)

// cursor is where a listing goes on: YouTube's token (often past 256
// characters) stays here, and callers hold a short id for it. Fill is the
// owner a channel's own rows leave out.
type cursor struct {
	endpoint string
	token    string
	fill     Item
}

type cursors struct {
	m *ttlMap[cursor]
}

func newCursors(now func() time.Time) *cursors {
	return &cursors{m: newTTLMap[cursor](now, cursorTTL, maxCursors)}
}

func (c *cursors) add(cur cursor) string {
	if cur.token == "" {
		return ""
	}
	var b [12]byte
	_, _ = rand.Read(b[:])
	id := base64.RawURLEncoding.EncodeToString(b[:])
	c.m.put(id, cur)
	return id
}

func (c *cursors) get(id string) (cursor, bool) { return c.m.get(id) }

// ttlMap is a small bounded map whose entries expire; at its bound the
// oldest entries go first
type ttlMap[V any] struct {
	mu    sync.Mutex
	now   func() time.Time
	ttl   time.Duration
	limit int
	m     map[string]ttlEntry[V]
}

type ttlEntry[V any] struct {
	v   V
	exp time.Time
}

func newTTLMap[V any](now func() time.Time, ttl time.Duration, limit int) *ttlMap[V] {
	return &ttlMap[V]{now: now, ttl: ttl, limit: limit, m: map[string]ttlEntry[V]{}}
}

func (t *ttlMap[V]) put(k string, v V) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	if len(t.m) >= t.limit {
		var oldest string
		var at time.Time
		for key, e := range t.m {
			if !e.exp.After(now) {
				delete(t.m, key)
				continue
			}
			if oldest == "" || e.exp.Before(at) {
				oldest, at = key, e.exp
			}
		}
		if len(t.m) >= t.limit {
			delete(t.m, oldest)
		}
	}
	t.m[k] = ttlEntry[V]{v: v, exp: now.Add(t.ttl)}
}

func (t *ttlMap[V]) get(k string) (V, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	e, ok := t.m[k]
	if !ok || !e.exp.After(t.now()) {
		delete(t.m, k)
		var zero V
		return zero, false
	}
	return e.v, true
}
