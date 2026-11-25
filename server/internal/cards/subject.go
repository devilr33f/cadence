package cards

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"strings"

	cardsv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/cards/v1"
)

// Subject is what a card shows: a Home Assistant entity, a search's results,
// a task. It renders the card afresh at each revision, binding every action
// it draws, so a card's actions always belong to the revision on screen.
type Subject interface {
	// ID is the card's id, stable per subject and opaque: never an entity id
	// or text (OpaqueID).
	ID() string
	Render(ctx context.Context, r *Render) (*cardsv1.Card, error)
}

// Live is a subject whose state can change while its card is on screen.
// Changed blocks until it may have changed (true) or ctx ends (false).
type Live interface {
	Changed(ctx context.Context) bool
}

// Render is what a subject renders with: the revision, the screen's language
// and catalog, and Bind, which issues an action id for a binding.
type Render struct {
	DeviceID string
	Revision uint32
	Lang     string
	Messages Messages
	bind     func(Binding) string
}

// Bind issues an action for this revision of the card.
func (r *Render) Bind(b Binding) *cardsv1.Action { return &cardsv1.Action{Id: r.bind(b)} }

// BindConfirm issues an action the device asks about first.
func (r *Render) BindConfirm(b Binding, question string) *cardsv1.Action {
	return &cardsv1.Action{Id: r.bind(b), Confirm: question}
}

// T formats a catalog message in the screen's language.
func (r *Render) T(key string, args ...string) string {
	return r.Messages.Format(r.Lang, key, pairs(args))
}

func pairs(kv []string) map[string]string {
	if len(kv) == 0 {
		return nil
	}
	m := make(map[string]string, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	return m
}

// Binding is what an issued action does: Run maps it straight to the call it
// was issued for, without the model. A binding that is not Idempotent runs
// once; any binding runs only until it expires and only for the device, card
// and revision it was issued to.
type Binding struct {
	Run        func(ctx context.Context, a Act) Outcome
	Idempotent bool
}

// Act is one press: the device that sent it and a slider's or choices'
// value, already checked against the primitive by pkg/cards.
type Act struct {
	DeviceID string
	Value    *cardsv1.ActionValue
}

// Outcome is what an action did. Refused is a refusal code the screen shows
// as a toast (refusal.<code> in the catalog); the card is drawn again either
// way, so the device's optimistic change rolls back to the server's. Panel,
// when set, answers a page (a link's cleaned text, a task panel). Record,
// when set, goes to the conversation history like a tool call.
type Outcome struct {
	Refused string
	Panel   *Panel
	Record  *Record
}

// Panel is a page to answer: its cards' subjects and the action loading the
// next page, nil on the last.
type Panel struct {
	Cards []Subject
	Next  *Binding
}

// Record is a card action as the history keeps it: a tool call by name with
// its arguments and result, as the model would have made it.
type Record struct {
	Tool   string
	Args   any
	Result any
}

// OpaqueID is a card id for a subject named by private text (an entity id, a
// url): a prefix and a hash, so the id the device holds and may log says
// nothing.
func OpaqueID(prefix, key string) string {
	sum := sha256.Sum256([]byte(key))
	return prefix + "-" + hex.EncodeToString(sum[:9])
}

var actionEnc = base32.StdEncoding.WithPadding(base32.NoPadding)

// newPageID is random, as an action id is.
func newPageID() string {
	var b [10]byte
	_, _ = rand.Read(b[:])
	return "page." + strings.ToLower(actionEnc.EncodeToString(b[:]))
}

// newActionID is random, so one device cannot guess another's.
func newActionID() string {
	var b [10]byte
	_, _ = rand.Read(b[:])
	return "a~" + strings.ToLower(actionEnc.EncodeToString(b[:]))
}
