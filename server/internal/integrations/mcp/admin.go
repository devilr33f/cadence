package mcp

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"
	"unicode"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"git.shino.boo/devilreef/cadence/server/internal/agent"
)

// SourceName is the name the catalog's Source goes by in the agent's
// registry.
const SourceName = "mcp"

// CodeNotConfigured: a name in the mcp setting that names no instance of
// kind mcp
const CodeNotConfigured = "not_configured"

// AdminDeps are what the admin routes read.
type AdminDeps struct {
	Catalog *Catalog
	// Listed reads the global mcp setting now
	Listed func(ctx context.Context) ([]string, error)
	// IsMCP says whether an instance of kind mcp has that name
	IsMCP func(ctx context.Context, name string) (bool, error)
	// Disabled is the code a listed instance that could not be built was
	// left out with, "" when none; nil is never
	Disabled func(name string) string
	// Skips is what the agent's registry leaves out now (Registry.Skips);
	// nil is nothing
	Skips func() []agent.Skip
	Log   *slog.Logger
}

// Admin is the dashboard's view of the MCP servers and the owner's say over
// their tools (M5d-D8). Its bodies carry the tools' names and sanitized
// descriptions, the dashboard's job; never the URL beyond its host, a header
// or a session id. Its logs carry codes only.
type Admin struct{ d AdminDeps }

func NewAdmin(d AdminDeps) *Admin {
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	if d.Disabled == nil {
		d.Disabled = func(string) string { return "" }
	}
	if d.Skips == nil {
		d.Skips = func() []agent.Skip { return nil }
	}
	return &Admin{d: d}
}

type counts struct {
	Total   int `json:"total"`
	Offered int `json:"offered"`
	Changed int `json:"changed"`
	Skipped int `json:"skipped"`
}

type serverView struct {
	Instance     string     `json:"instance"`
	Status       string     `json:"status"`
	Host         string     `json:"host"`
	Era          string     `json:"era"`
	LastCode     string     `json:"lastCode"`
	FailingSince *time.Time `json:"failingSince,omitempty"`
	LastRefresh  *time.Time `json:"lastRefresh,omitempty"`
	Tools        counts     `json:"tools"`
}

type hints struct {
	ReadOnly    *bool `json:"readOnly,omitempty"`
	Destructive *bool `json:"destructive,omitempty"`
	Idempotent  *bool `json:"idempotent,omitempty"`
	OpenWorld   *bool `json:"openWorld,omitempty"`
}

type toolView struct {
	Remote         string     `json:"remote"`
	Local          string     `json:"local"`
	Description    string     `json:"description"`
	Hints          hints      `json:"hints"`
	SchemaBytes    int        `json:"schemaBytes"`
	Effects        string     `json:"effects"`
	Trust          string     `json:"trust"`
	EnabledDefault bool       `json:"enabledDefault"`
	AnyTurn        bool       `json:"anyTurn"`
	Changed        bool       `json:"changed"`
	Hash           string     `json:"hash"`
	AckedHash      string     `json:"ackedHash,omitempty"`
	FirstSeen      *time.Time `json:"firstSeen"`
	LastSeen       *time.Time `json:"lastSeen"`
	Skip           string     `json:"skip,omitempty"`
}

// refused is the agent's refusals of the catalog's tools, local name to reason.
func (a *Admin) refused() map[string]string {
	out := map[string]string{}
	for _, s := range a.d.Skips() {
		if s.Source == SourceName && s.Name != "" {
			out[s.Name] = s.Reason
		}
	}
	return out
}

// server is a listed name as it runs: its client's status and the catalog's
// state, or why it does not run. A busy server keeps its session and shows
// its failed refresh in lastCode and failingSince.
func (a *Admin) server(ctx context.Context, name string, refused map[string]string) (serverView, error) {
	v := serverView{Instance: name}
	ok, err := a.d.IsMCP(ctx, name)
	switch {
	case err != nil:
		return v, err
	case !ok:
		v.Status, v.LastCode = StateUnavailable, CodeNotConfigured
		return v, nil
	}
	st, conn, running := a.d.Catalog.Health(name)
	if !running {
		// not built (yet): the reconcile left it out, or has still to apply it
		v.Status = StateConnecting
		if code := a.d.Disabled(name); code != "" {
			v.Status, v.LastCode = StateUnavailable, code
		}
		return v, nil
	}
	v.Status, v.Host, v.Era, v.LastCode = conn.State, conn.Host, conn.Era, st.Code
	// the client keeps its code across reconnects: it names a failure only
	// while the session is down
	if v.LastCode == "" && conn.State != StateConnected {
		v.LastCode = conn.LastCode
	}
	if !st.FailingSince.IsZero() {
		v.FailingSince = &st.FailingSince
	}
	if !st.ListedAt.IsZero() {
		v.LastRefresh = &st.ListedAt
	}
	for _, t := range a.toolViews(name, refused) {
		v.Tools.Total++
		switch t.Skip {
		case "":
			v.Tools.Offered++
		case SkipChanged:
			v.Tools.Changed++
		default:
			v.Tools.Skipped++
		}
	}
	return v, nil
}

// toolViews is every tool instance listed, by local name, those without one
// last.
func (a *Admin) toolViews(instance string, refused map[string]string) []toolView {
	var entries []Entry
	for _, e := range a.d.Catalog.Entries() {
		if e.Instance == instance {
			entries = append(entries, e)
		}
	}
	slices.SortStableFunc(entries, func(x, y Entry) int {
		return cmp.Or(cmp.Compare(unnamed(x), unnamed(y)), cmp.Compare(x.Local, y.Local), cmp.Compare(x.Remote, y.Remote))
	})
	out := make([]toolView, 0, len(entries))
	for _, e := range entries {
		out = append(out, view(e, refused))
	}
	return out
}

// entry is instance's listed tool of that remote name with a local name, and
// so overrides of its own.
func (a *Admin) entry(instance, remote string) (Entry, bool) {
	for _, e := range a.d.Catalog.Entries() {
		if e.Instance == instance && e.Remote == remote && e.Local != "" {
			return e, true
		}
	}
	return Entry{}, false
}

// view is e as the admin API shows it. A tool without a local name has no
// row and so no overrides: it shows the defaults a row would start with.
func view(e Entry, refused map[string]string) toolView {
	v := toolView{
		Remote: shownRemote(e.Remote), Local: e.Local, Description: e.Description, Hints: hintsOf(e.Annotations),
		SchemaBytes: e.SchemaBytes, Effects: EffectsWrite, Trust: TrustUntrusted, Skip: e.Skip,
	}
	if o := e.Override; e.Local != "" {
		v.Effects, v.Trust, v.EnabledDefault, v.AnyTurn = o.Effects, o.Trust, o.EnabledDefault, o.AnyTurn
		v.Changed, v.Hash, v.FirstSeen, v.LastSeen = o.Changed, o.Hash, &o.FirstSeen, &o.LastSeen
		if o.AckedHash != o.Hash {
			v.AckedHash = o.AckedHash
		}
	}
	if v.Skip == "" {
		v.Skip = refused[e.Local]
	}
	return v
}

func hintsOf(t *sdk.ToolAnnotations) hints {
	if t == nil {
		return hints{}
	}
	readOnly, idempotent := t.ReadOnlyHint, t.IdempotentHint
	return hints{ReadOnly: &readOnly, Destructive: t.DestructiveHint, Idempotent: &idempotent, OpenWorld: t.OpenWorldHint}
}

// shownRemote is a remote name as the admin API shows it: one the catalog
// took, as is; another cut to 128 code points, its control and format
// characters written as \u escapes.
func shownRemote(name string) string {
	if validRemote(name) {
		return name
	}
	var b strings.Builder
	n := 0
	for _, r := range name {
		if n == maxRemoteName {
			break
		}
		n++
		switch {
		case !unicode.IsControl(r) && !unicode.Is(unicode.Cf, r):
			b.WriteRune(r)
		case r > 0xffff:
			fmt.Fprintf(&b, `\U%08x`, r)
		default:
			fmt.Fprintf(&b, `\u%04x`, r)
		}
	}
	return b.String()
}
