package ha

import (
	"context"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"git.shino.boo/devilreef/cadence/server/internal/api"
)

const (
	tag = "homeassistant"

	adminNameLen = 60
	maxQuery     = 100

	defaultEntities = 100
	maxEntityPage   = 500
	defaultUsage    = 50
	maxUsagePage    = 200
)

// AdminDeps are what the admin routes show of Home Assistant.
type AdminDeps struct {
	// Home is the running view; nil when no client was built at startup
	Home *Home
	// Usage ranks the entities the tools changed
	Usage *Usage
	// Configured reads the global homeAssistant setting now ("" is unset)
	Configured func(ctx context.Context) (string, error)
	// Instance is the instance name the running client was built with
	Instance string
	Log      *slog.Logger
}

// Admin is the dashboard's view of Home Assistant (M5c-D10). Its bodies carry
// Home Assistant's text, which is the dashboard's job; its logs never do.
type Admin struct{ d AdminDeps }

func NewAdmin(d AdminDeps) *Admin {
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	return &Admin{d: d}
}

func badInput(path string) error { return api.Error(http.StatusUnprocessableEntity, "invalid", path) }

// internal logs the operation only.
func (a *Admin) internal(op string) error {
	a.d.Log.Error("ha_admin_failed", "op", op)
	return api.Error(http.StatusInternalServerError, "internal")
}

// unknownParam names the first query parameter outside allowed.
func unknownParam(q url.Values, allowed ...string) string {
	for _, k := range slices.Sorted(maps.Keys(q)) {
		if !slices.Contains(allowed, k) {
			return k
		}
	}
	return ""
}

// limit reads the limit parameter, 1 to hi.
func limit(q url.Values, def, hi int) (int, bool) {
	v := q.Get("limit")
	if v == "" {
		return def, true
	}
	n, err := strconv.Atoi(v)
	return n, err == nil && n >= 1 && n <= hi
}

type statusJSON struct {
	Configured      *string    `json:"configured"`
	Instance        *string    `json:"instance"`
	Connected       bool       `json:"connected"`
	ConnectedAt     *time.Time `json:"connectedAt"`
	LastError       *string    `json:"lastError"`
	Entities        int        `json:"entities"`
	Areas           int        `json:"areas"`
	RestartRequired bool       `json:"restartRequired"`
	Version         *string    `json:"version" doc:"the Home Assistant version the latest authentication reported (its auth_ok); null until one did"`
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func (a *Admin) status(ctx context.Context, in *queryIn) (*statusOut, error) {
	if bad := unknownParam(in.Values()); bad != "" {
		return nil, badInput(bad)
	}
	configured, err := a.d.Configured(ctx)
	if err != nil {
		return nil, a.internal("status")
	}
	out := statusJSON{Configured: optional(configured), Instance: optional(a.d.Instance)}
	if h := a.d.Home; h != nil {
		st := h.Client().Status()
		out.Connected, out.LastError, out.Version = st.Connected, optional(st.LastError), optional(st.Version)
		if !st.ConnectedAt.IsZero() {
			out.ConnectedAt = &st.ConnectedAt
		}
		if v, err := h.View(); err == nil {
			out.Entities, out.Areas = len(v.Entities), len(v.Areas)
		}
	}
	// a rejected token is retried only after a restart
	out.RestartRequired = configured != a.d.Instance || st(out.LastError) == CodeAuthFailed
	return &statusOut{Body: out}, nil
}

func st(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// view is the home's view, or the status that says why there is none.
func (a *Admin) view() (View, int) {
	if a.d.Home == nil {
		return View{}, http.StatusNotFound
	}
	v, err := a.d.Home.View()
	if err != nil {
		return View{}, http.StatusServiceUnavailable
	}
	return v, 0
}

func refuse(status int) error {
	if status == http.StatusNotFound {
		return api.Error(status, "not_configured")
	}
	return api.Error(status, "unavailable")
}

type entityJSON struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Domain   string `json:"domain"`
	Area     string `json:"area"`
	AreaName string `json:"areaName"`
	State    string `json:"state"`
}

func normalize(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }

// matches is ha_search's match: the query within the name, id or an alias.
func matches(e Entity, q string) bool {
	return strings.Contains(normalize(e.Name), q) || strings.Contains(e.ID, q) ||
		slices.ContainsFunc(e.Aliases, func(a string) bool { return strings.Contains(normalize(a), q) })
}

func (a *Admin) entities(_ context.Context, in *queryIn) (*entitiesOut, error) {
	params := in.Values()
	if bad := unknownParam(params, "q", "domain", "area", "limit"); bad != "" {
		return nil, badInput(bad)
	}
	q, domain, area := normalize(params.Get("q")), params.Get("domain"), params.Get("area")
	switch {
	case utf8.RuneCountInString(params.Get("q")) > maxQuery:
		return nil, badInput("q")
	case domain != "" && (len(domain) > maxDomain || !domainRe.MatchString(domain)):
		return nil, badInput("domain")
	case area != "" && !validRegistryID(area):
		return nil, badInput("area")
	}
	n, ok := limit(params, defaultEntities, maxEntityPage)
	if !ok {
		return nil, badInput("limit")
	}
	v, status := a.view()
	if status != 0 {
		return nil, refuse(status)
	}
	areas := make(map[string]string, len(v.Areas))
	for _, ar := range v.Areas {
		areas[ar.ID] = Sanitize(ar.Name, adminNameLen)
	}
	out := []entityJSON{}
	for _, e := range v.Entities {
		if len(out) == n {
			break
		}
		if (domain != "" && e.Domain != domain) || (area != "" && e.Area != area) || (q != "" && !matches(e, q)) {
			continue
		}
		name := Sanitize(e.Name, adminNameLen)
		if name == "" {
			name = e.ID
		}
		out = append(out, entityJSON{
			ID: e.ID, Name: name, Domain: e.Domain, Area: e.Area, AreaName: areas[e.Area], State: ModelState(e),
		})
	}
	return &entitiesOut{Body: out}, nil
}

type areaJSON struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Aliases []string `json:"aliases"`
}

func (a *Admin) areas(_ context.Context, in *queryIn) (*areasOut, error) {
	if bad := unknownParam(in.Values()); bad != "" {
		return nil, badInput(bad)
	}
	v, status := a.view()
	if status != 0 {
		return nil, refuse(status)
	}
	out := make([]areaJSON, len(v.Areas))
	for i, ar := range v.Areas {
		out[i] = areaJSON{ID: ar.ID, Name: Sanitize(ar.Name, adminNameLen), Aliases: []string{}}
		for _, al := range ar.Aliases {
			if s := Sanitize(al, adminNameLen); s != "" {
				out[i].Aliases = append(out[i].Aliases, s)
			}
		}
	}
	return &areasOut{Body: out}, nil
}

type usageJSON struct {
	EntityID string  `json:"entityId"`
	Score    float64 `json:"score"`
}

func (a *Admin) usage(ctx context.Context, in *queryIn) (*usageOut, error) {
	params := in.Values()
	if bad := unknownParam(params, "device", "limit"); bad != "" {
		return nil, badInput(bad)
	}
	var device uuid.UUID
	if v := params.Get("device"); v != "" {
		d, err := uuid.Parse(v)
		if err != nil || d.String() != strings.ToLower(v) {
			return nil, badInput("device")
		}
		device = d
	}
	n, ok := limit(params, defaultUsage, maxUsagePage)
	if !ok {
		return nil, badInput("limit")
	}
	if a.d.Home == nil {
		return nil, api.Error(http.StatusNotFound, "not_configured")
	}
	var rows []Scored
	var err error
	if device != uuid.Nil {
		rows, err = a.d.Usage.Top(ctx, device, n)
	} else {
		rows, err = a.d.Usage.Household(ctx, n)
	}
	if err != nil {
		return nil, a.internal("usage")
	}
	out := make([]usageJSON, len(rows))
	for i, r := range rows {
		out[i] = usageJSON(r)
	}
	return &usageOut{Body: out}, nil
}

type (
	queryIn     struct{ api.RawQuery }
	statusOut   struct{ Body statusJSON }
	entitiesOut struct{ Body []entityJSON }
	areasOut    struct{ Body []areaJSON }
	usageOut    struct{ Body []usageJSON }
)

// RegisterAdmin registers the Home Assistant operations on the admin api.
func RegisterAdmin(h huma.API, a *Admin) {
	defer api.Describe(h, docs)
	op := func(id, path string) huma.Operation {
		return huma.Operation{OperationID: id, Method: http.MethodGet, Path: path, Tags: []string{tag}}
	}
	huma.Register(h, op("get-homeassistant", "/integrations/homeassistant"), a.status)
	huma.Register(h, op("list-homeassistant-entities", "/integrations/homeassistant/entities"), a.entities)
	huma.Register(h, op("list-homeassistant-areas", "/integrations/homeassistant/areas"), a.areas)
	huma.Register(h, op("list-homeassistant-usage", "/integrations/homeassistant/usage"), a.usage)
}
