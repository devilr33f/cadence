package agent

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"git.shino.boo/devilreef/cadence/server/internal/settings"
	"git.shino.boo/devilreef/cadence/server/internal/store/db"
)

const (
	InterimDrop  = "drop"
	InterimSpeak = "speak"
)

var (
	ErrModeNotFound = errors.New("agent: mode not found")
	// ErrModeInUse is a delete of a mode a setting names
	ErrModeInUse = errors.New("agent: mode in use")
)

// AliasTakenError is a put whose alias at Index of language Lang another mode
// has in any language, or names that mode, in any case; Lang "" is the put's
// new name, which another mode has as an alias.
type AliasTakenError struct {
	Lang  string
	Index int
}

func (e *AliasTakenError) Error() string { return "agent: alias taken" }

// Mode is a named preset for a turn: its model, reasoning and budgets, whether
// text that comes with tool calls is spoken, the tools it offers and the words
// that name it by voice, per language.
type Mode struct {
	Name        string
	Model       string // "" uses the device's model setting
	Reasoning   string // none, low, medium or high
	MaxSteps    int
	MaxDuration time.Duration
	// MaxTokens is the ceiling of a step's max_tokens; 0 keeps the built-in
	// defaults (reasoningMaxTokens with reasoning on, else maxTokens)
	MaxTokens int
	// FirstTokenTimeout is how long a step waits for the model's first chunk; 0 keeps
	// the built-in 10 s
	FirstTokenTimeout time.Duration
	Interim           string   // InterimDrop or InterimSpeak
	Tools             []string // nil: every tool
	Aliases           map[string][]string
}

// ModeStore is the database: queries, and transactions for a put or a
// delete; *pgxpool.Pool is one.
type ModeStore interface {
	db.DBTX
	Begin(ctx context.Context) (pgx.Tx, error)
}

// Modes reads the modes for each turn and keeps them for the admin api.
type Modes struct {
	store ModeStore
	q     *db.Queries
	log   *slog.Logger

	mu      sync.Mutex
	changed []func()
}

// NewModes keeps the modes in store; log nil discards the saved and deleted
// lines.
func NewModes(store ModeStore, log *slog.Logger) *Modes {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Modes{store: store, q: db.New(store), log: log}
}

// OnChange calls fn after each put or delete that was stored; fn must not
// block.
func (m *Modes) OnChange(fn func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.changed = append(m.changed, fn)
}

func (m *Modes) notify() {
	m.mu.Lock()
	changed := slices.Clone(m.changed)
	m.mu.Unlock()
	for _, fn := range changed {
		fn()
	}
}

func (m *Modes) Get(ctx context.Context, name string) (Mode, error) {
	row, err := m.q.GetAgentMode(ctx, name)
	if errors.Is(err, pgx.ErrNoRows) {
		return Mode{}, ErrModeNotFound
	}
	if err != nil {
		return Mode{}, err
	}
	return modeOf(row)
}

// List is every mode by name.
func (m *Modes) List(ctx context.Context) ([]Mode, error) {
	return listModes(ctx, m.q)
}

func listModes(ctx context.Context, q *db.Queries) ([]Mode, error) {
	rows, err := q.ListAgentModes(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Mode, 0, len(rows))
	for _, row := range rows {
		mode, err := modeOf(row)
		if err != nil {
			return nil, err
		}
		out = append(out, mode)
	}
	return out, nil
}

func modeOf(row db.AgentMode) (Mode, error) {
	mode := Mode{
		Name:              row.Name,
		Model:             row.Model,
		Reasoning:         row.Reasoning,
		MaxSteps:          int(row.MaxSteps),
		MaxDuration:       time.Duration(row.MaxSeconds) * time.Second,
		MaxTokens:         int(row.MaxTokens),
		FirstTokenTimeout: time.Duration(row.FirstTokenMs) * time.Millisecond,
		Interim:           row.Interim,
		Tools:             row.Tools,
	}
	if err := json.Unmarshal(row.Aliases, &mode.Aliases); err != nil {
		return Mode{}, errors.New("agent: mode aliases invalid")
	}
	return mode, nil
}

// Put creates mode or replaces the one of its name, which the next turn reads;
// created says which. The caller has checked its fields. The agent_mode tool
// matches a word against every mode's name and every language's aliases, so
// an alias another mode has in any language or that names another mode, and a
// new mode named as another mode's alias, compared as the tool compares them,
// are an *AliasTakenError; puts take turns under the settings lock, so two
// cannot take one free alias.
func (m *Modes) Put(ctx context.Context, mode Mode) (created bool, err error) {
	aliases := mode.Aliases
	if aliases == nil {
		aliases = map[string][]string{}
	}
	raw, err := json.Marshal(aliases)
	if err != nil {
		return false, err
	}
	err = pgx.BeginFunc(ctx, m.store, func(tx pgx.Tx) error {
		q := m.q.WithTx(tx)
		if err := settings.Lock(ctx, q); err != nil {
			return err
		}
		all, err := listModes(ctx, q)
		if err != nil {
			return err
		}
		created = !slices.ContainsFunc(all, func(other Mode) bool { return other.Name == mode.Name })
		for _, other := range all {
			if other.Name == mode.Name {
				continue
			}
			if err := aliasTaken(aliases, other); err != nil {
				return err
			}
			// a mode named before this rule keeps its name on a replace
			if created && hasAlias(other.Aliases, AliasKey(mode.Name)) {
				return &AliasTakenError{}
			}
		}
		// checked by the caller; the bounds only keep the conversion whole
		steps := min(max(mode.MaxSteps, 0), math.MaxInt32)
		secs := min(max(int64(mode.MaxDuration/time.Second), 0), math.MaxInt32)
		tokens := min(max(int64(mode.MaxTokens), 0), math.MaxInt32)
		firstMs := min(max(mode.FirstTokenTimeout.Milliseconds(), 0), math.MaxInt32)
		return q.PutAgentMode(ctx, db.PutAgentModeParams{
			Name: mode.Name, Model: mode.Model, Reasoning: mode.Reasoning, MaxSteps: int32(steps),
			MaxSeconds: int32(secs), MaxTokens: int32(tokens), FirstTokenMs: int32(firstMs), Interim: mode.Interim, Tools: mode.Tools, Aliases: raw,
		})
	})
	if err != nil {
		return false, err
	}
	m.log.Info("mode_saved", "name", mode.Name, "created", created)
	m.notify()
	return created, nil
}

// AliasKey is how the agent_mode tool matches a word against a mode's alias:
// two aliases with one key cannot be told apart.
func AliasKey(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// aliasTaken is the first of mine, by language then index, that other has as
// an alias in any language or as its name.
func aliasTaken(mine map[string][]string, other Mode) error {
	langs := make([]string, 0, len(mine))
	for lang := range mine {
		langs = append(langs, lang)
	}
	slices.Sort(langs)
	for _, lang := range langs {
		for i, a := range mine[lang] {
			key := AliasKey(a)
			if key == AliasKey(other.Name) || hasAlias(other.Aliases, key) {
				return &AliasTakenError{Lang: lang, Index: i}
			}
		}
	}
	return nil
}

// hasAlias says whether an alias of any language has key.
func hasAlias(aliases map[string][]string, key string) bool {
	for _, list := range aliases {
		if slices.ContainsFunc(list, func(a string) bool { return AliasKey(a) == key }) {
			return true
		}
	}
	return false
}

// Delete removes the mode unless a setting names it (settings.NamedModes):
// ErrModeInUse, the mode kept. The delete and every settings write take
// turns under the settings lock, so a write naming the mode either lands first
// and the delete is refused, or comes after and is refused itself.
func (m *Modes) Delete(ctx context.Context, name string) error {
	err := pgx.BeginFunc(ctx, m.store, func(tx pgx.Tx) error {
		q := m.q.WithTx(tx)
		if err := settings.Lock(ctx, q); err != nil {
			return err
		}
		n, err := q.DeleteAgentMode(ctx, name)
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrModeNotFound
		}
		named, err := settings.NamedModes(ctx, q)
		if err != nil {
			return err
		}
		if slices.Contains(named, name) {
			return ErrModeInUse
		}
		return nil
	})
	if err != nil {
		return err
	}
	m.log.Info("mode_deleted", "name", name)
	m.notify()
	return nil
}

// defaultContextWindow is the window, in tokens, of a model contextWindows lacks.
const defaultContextWindow = 32_768

// contextWindows are models' context windows in tokens (M5a-D4).
var contextWindows = map[string]int{
	"deepseek/deepseek-v4.1-flash-1017": 1_000_000,
}

// ContextWindow is model's context window in tokens.
func ContextWindow(model string) int {
	if w, ok := contextWindows[model]; ok {
		return w
	}
	return defaultContextWindow
}
