package cards

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	pcards "git.shino.boo/devilreef/cadence/pkg/cards"
	cardsv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/cards/v1"
)

// DefaultTTL is how long an issued card and its actions stay valid when the
// card sets no ttl of its own: a drawer's cards are minutes on screen.
const DefaultTTL = 10 * time.Minute

// Deps are the service's collaborators.
type Deps struct {
	// Lang is a screen's language; nil or "" is English
	Lang func(ctx context.Context, deviceID string) string
	// Messages nil is Builtin
	Messages Messages
	// Record, when set, keeps a card action in the device's conversation
	// history (record.go's Recorder)
	Record func(ctx context.Context, deviceID string, r Record)
	Now    func() time.Time
	Log    *slog.Logger
	// TTL zero is DefaultTTL
	TTL time.Duration
}

// Service keeps every card a device was sent, at its current revision, and
// the actions it issued for it (W0-C item 12): bound to the device, the card
// and its revision, with a ttl, single use unless idempotent. Everything is
// in memory: a restart refuses every earlier action as unknown.
type Service struct {
	d Deps

	mu   sync.Mutex
	devs map[string]*device
	// floors are the revisions a forgotten device's cards reached, so a card shown again after its link came back
	// (a live card's next state) still replaces the one on screen
	floors map[string]map[string]floor
}

type floor struct {
	rev uint32
	at  time.Time
}

type device struct {
	// render serializes the device's renders, so revisions only rise
	render  sync.Mutex
	cards   map[string]*shown
	actions map[string]*issued
}

type shown struct {
	subject Subject
	card    *cardsv1.Card
	expires time.Time
	actions []string
}

type issued struct {
	// cardID is "" for a page's next action, whose page is pageID
	cardID   string
	pageID   string
	revision uint32
	b        Binding
	expires  time.Time
}

// New builds the service.
func New(d Deps) *Service {
	if d.Messages == nil {
		d.Messages = Builtin
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Log == nil {
		d.Log = slog.Default()
	}
	if d.TTL <= 0 {
		d.TTL = DefaultTTL
	}
	return &Service{d: d, devs: map[string]*device{}, floors: map[string]map[string]floor{}}
}

func (s *Service) device(dev string) *device {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.devs[dev]
	if d == nil {
		d = &device{cards: map[string]*shown{}, actions: map[string]*issued{}}
		s.devs[dev] = d
	}
	return d
}

func (s *Service) lang(ctx context.Context, dev string) string {
	if s.d.Lang == nil {
		return ""
	}
	return s.d.Lang(ctx, dev)
}

// Show renders subj for dev at its next revision (1 the first time), checks
// it as the device will and issues its actions; the caller sends the card it
// answers and must not change it.
func (s *Service) Show(ctx context.Context, dev string, subj Subject) (*cardsv1.Card, error) {
	return s.show(ctx, dev, subj, s.lang(ctx, dev))
}

func (s *Service) show(ctx context.Context, dev string, subj Subject, lang string) (*cardsv1.Card, error) {
	d := s.device(dev)
	d.render.Lock()
	defer d.render.Unlock()
	id := subj.ID()
	s.mu.Lock()
	rev := uint32(1)
	if cur := d.cards[id]; cur != nil {
		rev = cur.card.GetRevision() + 1
	} else if f, ok := s.floors[dev][id]; ok {
		rev = f.rev + 1
	}
	s.mu.Unlock()
	bound := map[string]Binding{}
	r := &Render{DeviceID: dev, Revision: rev, Lang: lang, Messages: s.d.Messages, bind: func(b Binding) string {
		aid := newActionID()
		bound[aid] = b
		return aid
	}}
	c, err := subj.Render(ctx, r)
	if err != nil {
		return nil, err
	}
	c.Id, c.Revision, c.Version = id, rev, pcards.Version
	if _, err := pcards.EncodeCard(c); err != nil {
		var ce *pcards.Error
		code := "invalid"
		if errors.As(err, &ce) {
			code = string(ce.Code)
		}
		// the code and path alone: a reason may quote the card's text
		s.d.Log.Error("card_invalid", "device_id", dev, "card_id", id, "code", code)
		return nil, err
	}
	now := s.d.Now()
	expires := now.Add(s.ttl(c))
	s.mu.Lock()
	defer s.mu.Unlock()
	if old := d.cards[id]; old != nil {
		for _, a := range old.actions {
			delete(d.actions, a)
		}
	}
	sh := &shown{subject: subj, card: c, expires: expires}
	for aid, b := range bound {
		d.actions[aid] = &issued{cardID: id, revision: rev, b: b, expires: expires}
		sh.actions = append(sh.actions, aid)
	}
	d.cards[id] = sh
	return clone(c), nil
}

func (s *Service) ttl(c *cardsv1.Card) time.Duration {
	if t := c.GetTtlS(); t > 0 {
		return time.Duration(t) * time.Second
	}
	return s.d.TTL
}

// Page renders a panel page for dev: each subject as Show does, and the next
// action, which a device sends as card.action with the page's id.
func (s *Service) Page(ctx context.Context, dev string, p *Panel) (*cardsv1.Page, error) {
	lang := s.lang(ctx, dev)
	out := &cardsv1.Page{Version: pcards.Version, Id: newPageID()}
	for _, subj := range p.Cards {
		if len(out.Cards) == pcards.MaxPageCards {
			break
		}
		c, err := s.show(ctx, dev, subj, lang)
		if err != nil {
			return nil, err
		}
		out.Cards = append(out.Cards, c)
	}
	if p.Next != nil {
		d := s.device(dev)
		aid := newActionID()
		s.mu.Lock()
		d.actions[aid] = &issued{pageID: out.GetId(), b: *p.Next, expires: s.d.Now().Add(s.d.TTL)}
		s.mu.Unlock()
		out.Next = &cardsv1.Action{Id: aid}
	}
	return out, nil
}

// Card is the card dev holds by id, at its current revision.
func (s *Service) Card(dev, id string) (*cardsv1.Card, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.devs[dev]
	if d == nil {
		return nil, false
	}
	sh := d.cards[id]
	if sh == nil || !s.d.Now().Before(sh.expires) {
		return nil, false
	}
	return sh.card, true
}

// errors a take answers; their text is the refusal code
var (
	errUnknown = errors.New("unknown")
	errExpired = errors.New("expired")
)

type taken struct {
	b       Binding
	subject Subject
	cardID  string
}

// take finds the action a request names and checks it against the card and
// revision it was issued for, using it up unless it is idempotent.
func (s *Service) take(dev string, req *cardsv1.ActionRequest) (taken, error) {
	now := s.d.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.devs[dev]
	if d == nil {
		return taken{}, errUnknown
	}
	iss := d.actions[req.GetActionId()]
	if iss == nil {
		// an action of a revision the card has moved past is out of date, not unknown
		if sh := d.cards[req.GetCardId()]; sh != nil && req.GetRevision() < sh.card.GetRevision() {
			return taken{}, errExpired
		}
		return taken{}, errUnknown
	}
	if !now.Before(iss.expires) {
		delete(d.actions, req.GetActionId())
		return taken{}, errExpired
	}
	t := taken{b: iss.b, cardID: iss.cardID}
	if iss.cardID != "" {
		sh := d.cards[iss.cardID]
		if sh == nil || req.GetCardId() != iss.cardID {
			return taken{}, errUnknown
		}
		if _, err := pcards.CheckAction(sh.card, req); err != nil {
			var ce *pcards.Error
			if errors.As(err, &ce) && ce.Code == pcards.CodeStale {
				return taken{}, errExpired
			}
			return taken{}, err
		}
		t.subject = sh.subject
	} else if req.GetCardId() != "" || req.GetValue() != nil ||
		(req.GetPageId() != "" && req.GetPageId() != iss.pageID) {
		// a page's next action belongs to no card, names its own page (or none, from a screen before page ids) and
		// takes no value
		return taken{}, errUnknown
	}
	if !iss.b.Idempotent {
		delete(d.actions, req.GetActionId())
	}
	return t, nil
}

// Forget drops what a device was sent: its link closed.
func (s *Service) Forget(dev string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if d := s.devs[dev]; d != nil && len(d.cards) > 0 {
		fl := map[string]floor{}
		now := s.d.Now()
		for id, sh := range d.cards {
			fl[id] = floor{rev: sh.card.GetRevision(), at: now}
		}
		s.floors[dev] = fl
	}
	delete(s.devs, dev)
}

// Run sweeps expired cards and actions each minute until ctx ends.
func (s *Service) Run(ctx context.Context) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.sweep()
			}
		}
	}()
	return func() { cancel(); <-done }
}

func (s *Service) sweep() {
	now := s.d.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for dev, d := range s.devs {
		for id, a := range d.actions {
			if !now.Before(a.expires) {
				delete(d.actions, id)
			}
		}
		for id, c := range d.cards {
			if !now.Before(c.expires) {
				delete(d.cards, id)
			}
		}
		if len(d.cards) == 0 && len(d.actions) == 0 {
			delete(s.devs, dev)
		}
	}
	for dev, fl := range s.floors {
		for id, f := range fl {
			if now.Sub(f.at) > s.d.TTL {
				delete(fl, id)
			}
		}
		if len(fl) == 0 {
			delete(s.floors, dev)
		}
	}
}

// clone keeps a caller from changing what the registry checks requests against
func clone(c *cardsv1.Card) *cardsv1.Card { return proto.Clone(c).(*cardsv1.Card) }
