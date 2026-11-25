package auth

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"git.shino.boo/devilreef/cadence/server/internal/store/db"
)

// TokenNamePattern is what an API token's name looks like; names are unique.
const TokenNamePattern = `^[a-z0-9][a-z0-9._-]{0,63}$` //nolint:gosec // a name's pattern, no credential

var tokenName = regexp.MustCompile(TokenNamePattern)

var (
	ErrUnauthorized   = errors.New("unauthorized")
	ErrTokenName      = errors.New("auth: token name invalid")
	ErrTokenNameTaken = errors.New("auth: token name taken")
	ErrTokenNotFound  = errors.New("auth: token not found")
)

// ValidTokenName reports whether name may name an API token.
func ValidTokenName(name string) bool { return tokenName.MatchString(name) }

// TokenInfo is an API token as listed: never the token or its hash.
type TokenInfo struct {
	Name   string
	Scopes Scopes
	// MCPTools names the MCP tools the token sees, in the order stored;
	// empty: every tool (the mcp scope's own grant)
	MCPTools   []string
	CreatedAt  time.Time
	LastUsedAt *time.Time
}

// Grants are what a verified API token may do.
type Grants struct {
	Scopes Scopes
	// MCPTools, as on the token: empty means every MCP tool
	MCPTools []string
}

// AllTools says whether g's token sees every MCP tool, which an admin
// caller's empty list and no list both mean.
func (g Grants) AllTools() bool { return len(g.MCPTools) == 0 }

// Sees says whether the MCP tool named is inside g's list.
func (g Grants) Sees(tool string) bool {
	return g.AllTools() || slices.Contains(g.MCPTools, tool)
}

type Tokens struct {
	q   *db.Queries
	now func() time.Time
}

func NewTokens(q *db.Queries, now func() time.Time) *Tokens { return &Tokens{q: q, now: now} }

// Create stores a new API token under name with scopes, and the MCP tools it
// sees, and returns it, the only time it is shown, with its scopes as stored.
func (t *Tokens) Create(ctx context.Context, name string, scopes, mcpTools []string) (string, Scopes, error) {
	if !ValidTokenName(name) {
		return "", nil, ErrTokenName
	}
	s, err := ParseScopes(scopes)
	if err != nil {
		return "", nil, err
	}
	tools, err := ParseMCPTools(mcpTools)
	if err != nil {
		return "", nil, err
	}
	tok, hash, err := NewToken(APIPrefix)
	if err != nil {
		return "", nil, err
	}
	// an empty list is every tool, stored as {} rather than null
	if tools == nil {
		tools = []string{}
	}
	n, err := t.q.CreateAPIToken(ctx, db.CreateAPITokenParams{Name: name, Hash: hash, Scopes: s.Strings(), McpTools: tools, Now: ts(t.now())})
	switch {
	case err != nil:
		return "", nil, errStore
	case n == 0:
		return "", nil, ErrTokenNameTaken
	}
	return tok, s, nil
}

// ParseMCPTools reads an MCP tool list: each name once, in order, empty for
// every tool; an unknown shape of name is ErrToolInvalid.
func ParseMCPTools(names []string) ([]string, error) {
	if len(names) > maxMCPTools {
		return nil, fmt.Errorf("auth: at most %d mcp tools", maxMCPTools)
	}
	var out []string
	for _, n := range names {
		if !mcpToolName.MatchString(n) || slices.Contains(out, n) {
			return nil, ErrToolInvalid
		}
		out = append(out, n)
	}
	return out, nil
}

// maxMCPTools is the most tools one token may name, as the column's check holds.
const maxMCPTools = 128

// ErrToolInvalid refuses an MCP tool name that cannot be one.
var ErrToolInvalid = errors.New("auth: mcp tool name invalid")

var mcpToolName = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,63}$`) //nolint:gosec // a name's pattern, no credential

// Verify answers a bearer's grants and marks it used, at most once a minute.
func (t *Tokens) Verify(ctx context.Context, bearer string) (Grants, error) {
	if !strings.HasPrefix(bearer, APIPrefix) {
		return Grants{}, ErrUnauthorized
	}
	row, err := t.q.GetAPIToken(ctx, Hash(bearer))
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return Grants{}, ErrUnauthorized
	case err != nil:
		return Grants{}, errStore
	}
	// the column takes known scopes only; anything else fails closed
	s, err := ParseScopes(row.Scopes)
	if err != nil {
		return Grants{}, ErrUnauthorized
	}
	tools, err := ParseMCPTools(row.McpTools)
	if err != nil {
		return Grants{}, ErrUnauthorized
	}
	now := t.now()
	if !row.LastUsedAt.Valid || now.Sub(row.LastUsedAt.Time) >= touchEvery {
		if err := t.q.TouchAPIToken(ctx, db.TouchAPITokenParams{ID: row.ID, Now: ts(now), StaleBefore: ts(now.Add(-touchEvery))}); err != nil {
			return Grants{}, errStore
		}
	}
	return Grants{Scopes: s, MCPTools: tools}, nil
}

// List answers every API token by name.
func (t *Tokens) List(ctx context.Context) ([]TokenInfo, error) {
	rows, err := t.q.ListAPITokens(ctx)
	if err != nil {
		return nil, errStore
	}
	out := make([]TokenInfo, 0, len(rows))
	for _, r := range rows {
		s, err := ParseScopes(r.Scopes)
		if err != nil {
			return nil, errStore
		}
		tools, err := ParseMCPTools(r.McpTools)
		if err != nil {
			return nil, errStore
		}
		info := TokenInfo{Name: r.Name, Scopes: s, MCPTools: tools, CreatedAt: r.CreatedAt.Time}
		if r.LastUsedAt.Valid {
			used := r.LastUsedAt.Time
			info.LastUsedAt = &used
		}
		out = append(out, info)
	}
	return out, nil
}

// SetMCPTools changes which MCP tools the token named name sees; an empty
// list is every tool.
func (t *Tokens) SetMCPTools(ctx context.Context, name string, tools []string) error {
	list, err := ParseMCPTools(tools)
	if err != nil {
		return err
	}
	if list == nil {
		list = []string{}
	}
	n, err := t.q.SetAPITokenTools(ctx, db.SetAPITokenToolsParams{Name: name, McpTools: list})
	switch {
	case err != nil:
		return errStore
	case n == 0:
		return ErrTokenNotFound
	}
	return nil
}

// Delete removes the token named name; ErrTokenNotFound when none is.
func (t *Tokens) Delete(ctx context.Context, name string) error {
	n, err := t.q.DeleteAPIToken(ctx, name)
	switch {
	case err != nil:
		return errStore
	case n == 0:
		return ErrTokenNotFound
	}
	return nil
}
