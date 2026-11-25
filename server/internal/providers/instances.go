package providers

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"git.shino.boo/devilreef/cadence/server/internal/jsonschemax"
	"git.shino.boo/devilreef/cadence/server/internal/secrets"
	"git.shino.boo/devilreef/cadence/server/internal/settings"
	"git.shino.boo/devilreef/cadence/server/internal/store/db"
)

var (
	ErrUnknownModule = errors.New("providers: unknown module")
	ErrNameTaken     = errors.New("providers: name taken")
	ErrNotFound      = errors.New("providers: instance not found")
	ErrWrongKind     = errors.New("providers: wrong kind")
	ErrBuildFailed   = errors.New("providers: build failed")
	ErrInUse         = errors.New("providers: instance in use")
)

type ConfigInvalidError = jsonschemax.ConfigInvalidError

type SecretState struct {
	Set bool `json:"set"`
}

type Instance struct {
	ID        string                 `json:"id"`
	Name      string                 `json:"name"`
	Module    string                 `json:"module"`
	Config    json.RawMessage        `json:"config"`
	Secrets   map[string]SecretState `json:"secrets"`
	CreatedAt time.Time              `json:"createdAt"`
}

// Store is the database: queries, and transactions for a delete; *pgxpool.Pool
// is one.
type Store interface {
	db.DBTX
	Begin(ctx context.Context) (pgx.Tx, error)
}

type Instances struct {
	store Store
	q     *db.Queries
	box   *secrets.Box
	reg   *Registry

	mu      sync.Mutex
	cache   map[string]built
	changed []func()
	// saveMu orders the secret saves: each reads the row, changes one key and writes it whole
	saveMu sync.Mutex
}

// Ref is an instance's identity without building it: a delete and a create
// under the same name make a new ID.
type Ref struct {
	ID   string
	Kind Kind
}

type built struct {
	kind Kind
	v    any
}

func NewInstances(store Store, box *secrets.Box, reg *Registry) *Instances {
	return &Instances{store: store, q: db.New(store), box: box, reg: reg, cache: map[string]built{}}
}

func aad(name string) []byte { return []byte("provider:" + name) }

func (s *Instances) Create(ctx context.Context, name, module string, cfg json.RawMessage, sec map[string]string) (Instance, error) {
	mod, ok := s.reg.Module(module)
	if !ok {
		return Instance{}, ErrUnknownModule
	}
	cfg, err := checkedConfig(mod, cfg)
	if err != nil {
		return Instance{}, err
	}
	for k, v := range sec {
		if v == "" && slices.Contains(mod.OptionalSecrets, k) {
			delete(sec, k)
		}
	}
	if err := checkSecrets(mod, sec); err != nil {
		return Instance{}, err
	}
	sealed, err := s.sealSecrets(name, sec)
	if err != nil {
		return Instance{}, err
	}
	row, err := s.q.CreateProviderInstance(ctx, db.CreateProviderInstanceParams{
		Name: name, Module: module, Config: cfg, Secrets: sealed,
	})
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" {
		return Instance{}, ErrNameTaken
	}
	if err != nil {
		return Instance{}, err
	}
	s.notify()
	return s.toInstance(row)
}

// OnChange calls fn after each create, update and delete; fn must not block.
func (s *Instances) OnChange(fn func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.changed = append(s.changed, fn)
}

func (s *Instances) notify() {
	s.mu.Lock()
	changed := slices.Clone(s.changed)
	s.mu.Unlock()
	for _, fn := range changed {
		fn()
	}
}

// Ref names an instance's id and kind: ErrNotFound, or ErrUnknownModule for
// a module no longer registered.
func (s *Instances) Ref(ctx context.Context, name string) (Ref, error) {
	row, err := s.q.GetProviderInstance(ctx, name)
	if errors.Is(err, pgx.ErrNoRows) {
		return Ref{}, ErrNotFound
	}
	if err != nil {
		return Ref{}, err
	}
	mod, ok := s.reg.Module(row.Module)
	if !ok {
		return Ref{}, ErrUnknownModule
	}
	return Ref{ID: row.ID.String(), Kind: mod.Kind}, nil
}

func (s *Instances) List(ctx context.Context) ([]Instance, error) {
	rows, err := s.q.ListProviderInstances(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Instance, 0, len(rows))
	for _, r := range rows {
		inst, err := s.toInstance(r)
		if err != nil {
			return nil, err
		}
		out = append(out, inst)
	}
	return out, nil
}

// Delete removes the instance unless a setting (settings.NamesInstance) or a
// subscription's topic names it:
// ErrInUse, the instance kept. The delete and every settings write take turns
// under the settings lock, so a write naming the instance either lands first
// and the delete is refused, or comes after and is refused itself.
func (s *Instances) Delete(ctx context.Context, name string) error {
	err := pgx.BeginFunc(ctx, s.store, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		if err := settings.Lock(ctx, q); err != nil {
			return err
		}
		n, err := q.DeleteProviderInstance(ctx, name)
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrNotFound
		}
		named, err := settings.NamesInstance(ctx, q, name)
		if err != nil {
			return err
		}
		if !named {
			// a subscription's topic names its instance too; subscriptions are
			// created under the same lock
			if named, err = q.SubscriptionNamesInstance(ctx, name); err != nil {
				return err
			}
		}
		if named {
			return ErrInUse
		}
		return nil
	})
	if err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	s.mu.Lock()
	delete(s.cache, name)
	s.mu.Unlock()
	if err != nil {
		return err
	}
	s.notify()
	return nil
}

func (s *Instances) STT(ctx context.Context, name string) (STT, error) {
	return get[STT](ctx, s, name, KindSTT)
}

func (s *Instances) TTS(ctx context.Context, name string) (TTS, error) {
	return get[TTS](ctx, s, name, KindTTS)
}

func (s *Instances) LLM(ctx context.Context, name string) (LLM, error) {
	return get[LLM](ctx, s, name, KindLLM)
}

func (s *Instances) Weather(ctx context.Context, name string) (Weather, error) {
	return get[Weather](ctx, s, name, KindWeather)
}

func (s *Instances) Search(ctx context.Context, name string) (Search, error) {
	return get[Search](ctx, s, name, KindSearch)
}

func (s *Instances) Music(ctx context.Context, name string) (Music, error) {
	return get[Music](ctx, s, name, KindMusic)
}

func (s *Instances) Player(ctx context.Context, name string) (Player, error) {
	return get[Player](ctx, s, name, KindPlayer)
}

func (s *Instances) XSearch(ctx context.Context, name string) (XSearch, error) {
	return get[XSearch](ctx, s, name, KindXSearch)
}

func (s *Instances) Embed(ctx context.Context, name string) (Embedder, error) {
	return get[Embedder](ctx, s, name, KindEmbed)
}

// Get builds a named instance of a kind whose contract lives outside this
// package, such as KindHermes.
func Get[T any](ctx context.Context, s *Instances, name string, kind Kind) (T, error) {
	return get[T](ctx, s, name, kind)
}

func get[T any](ctx context.Context, s *Instances, name string, kind Kind) (T, error) {
	var zero T
	v, err := s.build(ctx, name, kind)
	if err != nil {
		return zero, err
	}
	t, ok := v.(T)
	if !ok {
		return zero, ErrBuildFailed
	}
	return t, nil
}

func (s *Instances) build(ctx context.Context, name string, kind Kind) (any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if b, ok := s.cache[name]; ok {
		if b.kind != kind {
			return nil, ErrWrongKind
		}
		return b.v, nil
	}
	row, err := s.q.GetProviderInstance(ctx, name)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	mod, ok := s.reg.Module(row.Module)
	if !ok {
		return nil, ErrUnknownModule
	}
	if mod.Kind != kind {
		return nil, ErrWrongKind
	}
	sec, err := s.openSecrets(name, row.Secrets)
	if err != nil {
		return nil, err
	}
	v, err := newOf(mod, row.Config, sec, s.saver(name, row.ID, mod))
	if err != nil || v == nil {
		return nil, ErrBuildFailed
	}
	s.cache[name] = built{kind: kind, v: v}
	return v, nil
}

// Modules are the registered modules by kind, then id.
func (s *Instances) Modules() []Module { return s.reg.Modules() }

// Probe builds name afresh from its stored row, as build does but outside the
// cache the runtime uses, and runs its module's Probe on that build under
// ctx: "" when it passed, else a probe code, or ErrNotFound. A module no
// longer registered, or one without a Probe, is unsupported; a panic in a
// probe is bad_response.
func (s *Instances) Probe(ctx context.Context, name string) (string, error) {
	row, err := s.q.GetProviderInstance(ctx, name)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	mod, ok := s.reg.Module(row.Module)
	if !ok || mod.Probe == nil {
		return string(ProbeUnsupported), nil
	}
	sec, err := s.openSecrets(name, row.Secrets)
	if err != nil {
		return string(ProbeBuildFailed), nil
	}
	v, err := newOf(mod, row.Config, sec, func(context.Context, string, string) error { return nil })
	if err != nil || v == nil {
		return string(ProbeBuildFailed), nil
	}
	if Guard(nil, mod.ID, string(ProbeBadResponse), func() { err = mod.Probe(ctx, v) }) {
		return string(ProbeBadResponse), nil
	}
	return ProbeCode(err), nil
}

func newOf(mod Module, cfg json.RawMessage, sec map[string]string, save SaveSecret) (any, error) {
	if mod.NewSaving != nil {
		return mod.NewSaving(cfg, sec, save)
	}
	return mod.New(cfg, sec)
}

// saver writes one secret of the instance row id under name: its other secrets are read again and kept, all sealed
// anew. It changes no build: the module that saves already holds the value
func (s *Instances) saver(name string, id pgtype.UUID, mod Module) SaveSecret {
	return func(ctx context.Context, key, value string) error {
		if !slices.Contains(mod.Secrets, key) && !slices.Contains(mod.OptionalSecrets, key) {
			return ErrBuildFailed
		}
		s.saveMu.Lock()
		defer s.saveMu.Unlock()
		row, err := s.q.GetProviderInstance(ctx, name)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && row.ID != id) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		sec, err := s.openSecrets(name, row.Secrets)
		if err != nil {
			return err
		}
		sec[key] = value
		plain, err := json.Marshal(sec)
		if err != nil {
			return err
		}
		sealed, err := s.box.Seal(plain, aad(name))
		if err != nil {
			return err
		}
		n, err := s.q.UpdateProviderSecrets(ctx, db.UpdateProviderSecretsParams{Secrets: sealed, Name: name, ID: id})
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrNotFound
		}
		return nil
	}
}

func (s *Instances) openSecrets(name string, sealed []byte) (map[string]string, error) {
	out := map[string]string{}
	if len(sealed) == 0 {
		return out, nil
	}
	plain, err := s.box.Open(sealed, aad(name))
	if err != nil {
		return nil, ErrBuildFailed
	}
	if err := json.Unmarshal(plain, &out); err != nil {
		return nil, ErrBuildFailed
	}
	return out, nil
}

func (s *Instances) toInstance(r db.ProviderInstance) (Instance, error) {
	sec, err := s.openSecrets(r.Name, r.Secrets)
	if err != nil {
		return Instance{}, err
	}
	state := make(map[string]SecretState, len(sec))
	for k := range sec {
		state[k] = SecretState{Set: true}
	}
	return Instance{
		ID: r.ID.String(), Name: r.Name, Module: r.Module, Config: r.Config,
		Secrets: state, CreatedAt: r.CreatedAt.Time,
	}, nil
}
