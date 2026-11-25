package catalog

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"log/slog"
	"sync"
	"time"
)

// Household is the one household a server serves; refs are bound to it.
const Household = "home"

const (
	// itemTTL bounds how long an item issued stays readable for overlays
	itemTTL      = 12 * time.Hour
	maxItems     = 50_000
	maxGroups    = 10_000
	actionTTL    = time.Hour
	maxActions   = 20_000
	callTimeout  = 20 * time.Second
	sourceWait   = 3 * time.Second
	actionPrefix = "a:"
)

// Configured is one provider the catalog asks, with the instance it is
// built from.
type Configured struct {
	Instance string
	Provider Provider
}

// Images issues the image proxy's ids (images.Proxy).
type Images interface {
	Ref(rawURL string, local bool) string
}

// LocalArtwork is a provider whose artwork urls on its own LAN host the
// image proxy fetches without the public guard.
type LocalArtwork interface {
	OwnsArtwork(rawURL string) bool
}

// Deps are the service's parts.
type Deps struct {
	// Providers are the configured providers, in the owner's order
	Providers func(ctx context.Context) []Configured
	// Refs nil is a RefTable
	Refs RefIssuer
	// Images nil leaves artwork without ids
	Images Images
	// Choices nil keeps them in memory
	Choices ChoiceStore
	// Profile is a device's, for resolve; nil is a speaker's
	Profile func(ctx context.Context, deviceID string) DeviceProfile
	// ImageURL is an image id's url a screen fetches (ambient's backdrops)
	ImageURL func(id string) string
	// Info, when set, enriches titles with Shikimori's and MyAnimeList's facts
	Info TitleInfo
	Log  *slog.Logger
	Now  func() time.Time
}

// Service is the one catalog API over every configured provider.
type Service struct {
	d Deps

	mu      sync.Mutex
	items   map[string]issued
	groups  map[string]*group
	gorder  []string
	actions map[string]action
}

type issued struct {
	item Item
	at   time.Time
}

type action struct {
	key Key
	id  string
	at  time.Time
}

// New makes the service.
func New(d Deps) *Service {
	if d.Refs == nil {
		d.Refs = NewRefTable(d.Now)
	}
	if d.Choices == nil {
		d.Choices = &MemoryChoices{}
	}
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Providers == nil {
		d.Providers = func(context.Context) []Configured { return nil }
	}
	return &Service{d: d, items: map[string]issued{}, groups: map[string]*group{}, actions: map[string]action{}}
}

type deviceKey struct{}

// WithDevice names the device a call is for: a provider that relays streams
// (YouTube's) binds them to it.
func WithDevice(ctx context.Context, deviceID string) context.Context {
	return context.WithValue(ctx, deviceKey{}, deviceID)
}

// DeviceFrom is the device WithDevice named, "" for none.
func DeviceFrom(ctx context.Context) string {
	s, _ := ctx.Value(deviceKey{}).(string)
	return s
}

func (s *Service) provider(ctx context.Context, id ProviderID) (Provider, bool) {
	for _, c := range s.d.Providers(ctx) {
		if c.Provider.ID() == id {
			return c.Provider, true
		}
	}
	return nil, false
}

// Providers lists the configured providers and their sources.
func (s *Service) Providers(ctx context.Context) []ProviderView {
	cs := s.d.Providers(ctx)
	out := make([]ProviderView, len(cs))
	var wg sync.WaitGroup
	for i, c := range cs {
		wg.Go(func() {
			v := ProviderView{ID: c.Provider.ID(), Instance: c.Instance, Capabilities: c.Provider.Capabilities(),
				Sources: []SourceView{}}
			if v.Capabilities == nil {
				v.Capabilities = []Capability{}
			}
			sctx, cancel := context.WithTimeout(ctx, sourceWait)
			defer cancel()
			srcs, err := c.Provider.Sources(sctx)
			if err != nil {
				v.Failure = codeOf(err)
			}
			for _, src := range srcs {
				v.Sources = append(v.Sources, SourceView{Provider: v.ID, Source: src.ID, Name: src.Name,
					Enabled: src.Enabled, Torrents: src.Torrents})
			}
			out[i] = v
		})
	}
	wg.Wait()
	return out
}

// codeOf is an error's failure code
func codeOf(err error) FailureCode {
	var f *Failure
	switch {
	case errors.As(err, &f):
		return f.Code
	case errors.Is(err, context.DeadlineExceeded):
		return FailTimeout
	case errors.Is(err, ErrSignInRequired):
		return FailAuth
	case errors.Is(err, ErrUnplayable):
		return FailRemoved
	}
	return FailFailed
}

// out swaps an item's key for a ref and its artwork url for an image id,
// and keeps it for the overlay of a later read
func (s *Service) out(ctx context.Context, household string, p Provider, it Item) (Item, error) {
	ref, err := s.d.Refs.Issue(ctx, household, it.Key)
	if err != nil {
		return Item{}, err
	}
	it.Ref = ref
	it.Provider, it.Source, it.Kind = it.Key.Provider, it.Key.Source, it.Key.Kind
	if it.Owner != nil {
		o := *it.Owner
		if o.Key.ID != "" {
			if o.Ref, err = s.d.Refs.Issue(ctx, household, o.Key); err != nil {
				return Item{}, err
			}
		}
		it.Owner = &o
	}
	if it.Artwork.URL != "" && s.d.Images != nil {
		local := false
		if la, ok := p.(LocalArtwork); ok {
			local = la.OwnsArtwork(it.Artwork.URL)
		}
		it.Artwork.Hash = s.d.Images.Ref(it.Artwork.URL, local)
	}
	s.keep(household, it)
	return it, nil
}

func (s *Service) outAll(ctx context.Context, household string, p Provider, its []Item) ([]Item, error) {
	out := make([]Item, 0, len(its))
	for _, it := range its {
		o, err := s.out(ctx, household, p, it)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, nil
}

func (s *Service) keep(household string, it Item) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.d.Now()
	s.items[household+"\x1e"+it.Key.String()] = issued{item: it, at: now}
	if len(s.items) > maxItems {
		for k, v := range s.items {
			if now.Sub(v.at) > itemTTL || len(s.items) > maxItems {
				delete(s.items, k)
			}
		}
	}
}

// kept is an item issued before, by its key
func (s *Service) kept(household string, k Key) (Item, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.items[household+"\x1e"+k.String()]
	if !ok || s.d.Now().Sub(v.at) > itemTTL {
		return Item{}, false
	}
	return v.item, true
}

// Lookup is a ref's key, for the agent's tools and the screen note.
func (s *Service) Lookup(ctx context.Context, household string, r Ref) (Key, error) {
	return s.d.Refs.Lookup(ctx, household, r)
}

// Peek is the item a ref names as it was last handed out, without asking
// its provider: the screen note's titles.
func (s *Service) Peek(ctx context.Context, household string, r Ref) (Item, bool) {
	k, err := s.d.Refs.Lookup(ctx, household, r)
	if err != nil {
		return Item{}, false
	}
	return s.kept(household, k)
}

// Series is the series an episode's ref belongs to, as it was last handed out: an episode alone names only its
// number and its own name, its cards and the screen's player name the show. False for anything but an episode, or
// a series not read since.
func (s *Service) Series(ctx context.Context, household string, r Ref) (Item, bool) {
	k, err := s.d.Refs.Lookup(ctx, household, r)
	if err != nil || (k.Kind != KindEpisode && k.Episode == "") {
		return Item{}, false
	}
	k.Kind, k.Episode = KindSeries, ""
	return s.kept(household, k)
}

// issueAction keeps a provider action under an id of the service's
func (s *Service) issueAction(k Key, a Action) ActionView {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	id := actionPrefix + base64.RawURLEncoding.EncodeToString(b)
	s.mu.Lock()
	now := s.d.Now()
	s.actions[id] = action{key: k, id: a.ID, at: now}
	if len(s.actions) > maxActions {
		for k, v := range s.actions {
			if now.Sub(v.at) > actionTTL || len(s.actions) > maxActions {
				delete(s.actions, k)
			}
		}
	}
	s.mu.Unlock()
	return ActionView{ID: id, Kind: a.Kind, Label: a.Label, Icon: a.Icon, Toggled: a.Toggled, Enabled: a.Enabled,
		Value: a.Value, Values: a.Values}
}

func (s *Service) actionsOut(k Key, as []Action) []ActionView {
	out := make([]ActionView, 0, len(as))
	for _, a := range as {
		out = append(out, s.issueAction(k, a))
	}
	return out
}

func (s *Service) takeAction(id string) (action, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.actions[id]
	if !ok || s.d.Now().Sub(a.at) > actionTTL {
		return action{}, false
	}
	return a, true
}

// Kind of refusal a caller sees, for both the RPC and the admin API: never
// upstream text.
type Refusal string

const (
	RefusalUnknownRef  Refusal = "unknown_ref"
	RefusalUnknownAct  Refusal = "unknown_action"
	RefusalNotFound    Refusal = "not_found"
	RefusalUnsupported Refusal = "unsupported"
	RefusalRefused     Refusal = "refused"
	RefusalSignIn      Refusal = "sign_in_required"
	RefusalUnavailable Refusal = "unavailable"
	RefusalCanceled    Refusal = "canceled"
	RefusalInternal    Refusal = "internal"
)

// RefusalOf classifies a service error; code is the failure code of an
// unavailable one ("" else).
// FailureOf is the *Failure in err's chain, if any (which provider and
// source failed, and how)
func FailureOf(err error) *Failure {
	var f *Failure
	if errors.As(err, &f) {
		return f
	}
	return nil
}

func RefusalOf(err error) (Refusal, FailureCode) {
	var f *Failure
	switch {
	case errors.Is(err, ErrUnknownRef):
		return RefusalUnknownRef, ""
	case errors.Is(err, ErrUnknownAction):
		return RefusalUnknownAct, ""
	case errors.Is(err, ErrNotFound):
		return RefusalNotFound, ""
	case errors.Is(err, ErrUnsupported):
		return RefusalUnsupported, ""
	case errors.Is(err, ErrSignInRequired):
		return RefusalSignIn, ""
	case errors.Is(err, ErrRefused):
		return RefusalRefused, ""
	case errors.As(err, &f):
		return RefusalUnavailable, f.Code
	case errors.Is(err, ErrUnplayable):
		return RefusalUnavailable, FailRemoved
	case errors.Is(err, context.DeadlineExceeded):
		return RefusalUnavailable, FailTimeout
	case errors.Is(err, context.Canceled):
		return RefusalCanceled, ""
	}
	return RefusalInternal, ""
}
