package catalog

import (
	"context"
	"errors"
	"strings"
)

// Key is a provider's own identity of an item: what it needs to find it
// again. ID is opaque to everyone but the provider (a torrplayer title id, a
// YouTube video id, a TorrServer hash with a file index). Episode is set for
// an episode of a variant, so a variant's ID and an episode label name it.
type Key struct {
	Provider ProviderID
	Source   SourceID
	Kind     Kind
	ID       string
	Episode  string
}

// String is a stable map key. It holds the provider's ids, so it never
// reaches a log.
func (k Key) String() string {
	return strings.Join([]string{string(k.Provider), string(k.Source), string(k.Kind), k.ID, k.Episode}, "\x1f")
}

// Ref is an opaque, server-issued name of a Key ("c:…"), the only name of an
// item that leaves the server. Refs are bound to a household and expire.
type Ref string

// ErrUnknownRef is a ref this server did not issue to the caller, or one
// that expired. It names no item, so it may answer anyone.
var ErrUnknownRef = errors.New("catalog: unknown ref")

// RefIssuer is the service's ref table. Issue answers the same Ref for the
// same Key and household while it lives; Lookup answers ErrUnknownRef for a
// ref of another household. Implementations keep refs in memory (D11).
type RefIssuer interface {
	Issue(ctx context.Context, household string, k Key) (Ref, error)
	Lookup(ctx context.Context, household string, r Ref) (Key, error)
}

// GroupKey is the key that merges one title across sources into a title
// group: its Shikimori id, else its MAL id. ok is false for an item with
// neither, which stays a group of its own.
func GroupKey(it Item) (key string, ok bool) {
	if it.Show == nil {
		return "", false
	}
	switch {
	case it.Show.IDs.Shikimori != "":
		return "shikimori:" + it.Show.IDs.Shikimori, true
	case it.Show.IDs.MAL != "":
		return "mal:" + it.Show.IDs.MAL, true
	}
	return "", false
}
