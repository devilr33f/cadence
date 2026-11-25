package agent

import (
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"sync"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/server/internal/jsonschemax"
)

// Source supplies tools that come and go, such as an MCP server's. A turn
// calls Tools once, at its start, from any goroutine, so it must be quick and
// safe for concurrent use; the slice it returns is not changed. A source that
// keeps its tools compiles them once with Compile, or each snapshot does.
type Source interface{ Tools() []Tool }

// SourceFunc lets a func be a Source.
type SourceFunc func() []Tool

func (f SourceFunc) Tools() []Tool { return f() }

// dynamic tool names as OpenAI-compatible providers take them
var dynamicName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// why a snapshot left a dynamic tool out
const (
	SkipName      = "name"
	SkipDuplicate = "duplicate"
	SkipRun       = "run"
	SkipSchema    = "schema"
	SkipPanic     = "panic"
)

// Skip is a dynamic tool a snapshot left out, or with SkipPanic and no Name
// a whole source. Name is the tool's name as its source gave it, for the
// admin API: logs carry only Source and Reason.
type Skip struct{ Source, Name, Reason string }

type source struct {
	name string
	s    Source
}

// Registry holds the static tools in registration order and the sources
// after them. A turn reads them as one snapshot taken at its start, so a
// source that changes mid-turn changes the next turn only.
type Registry struct {
	tools []Tool
	live  bool // a tool has a Describe

	mu      sync.Mutex
	sources []source
	logged  map[Skip]bool // the last logged snapshot's skips
}

// NewRegistry compiles each tool's schema once. A tool without a name, a name
// registered twice or an invalid schema is a programming error and panics.
func NewRegistry(tools ...Tool) *Registry {
	r := &Registry{}
	seen := map[string]bool{}
	for _, t := range tools {
		if t.Name == "" || seen[t.Name] {
			panic(`agent: tool name empty or registered twice: "` + t.Name + `"`)
		}
		seen[t.Name] = true
		sch, err := jsonschemax.Compile(t.Input)
		if err != nil {
			panic(`agent: tool "` + t.Name + `" has an invalid input schema`)
		}
		t.schema = sch
		r.tools = append(r.tools, t)
		r.live = r.live || t.Describe != nil
	}
	return r
}

// static is the registered tools, each live description read now.
func (r *Registry) static() []Tool {
	if !r.live {
		return slices.Clip(r.tools)
	}
	tools := slices.Clone(r.tools)
	for i, t := range tools {
		if t.Describe != nil {
			tools[i].Description = t.Describe()
		}
	}
	return slices.Clip(tools)
}

// Static is the registered tools with their live descriptions, the sources'
// never among them: a list built from it alone cannot serve another MCP
// server's tools back through Cadence's own MCP endpoint.
func (r *Registry) Static() []Tool { return r.static() }

// AddSource adds a source of tools after the static ones and the sources
// added before it; name stands for it in the log. An empty or repeated name
// or a nil source is a programming error and panics.
func (r *Registry) AddSource(name string, s Source) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if name == "" || s == nil || slices.ContainsFunc(r.sources, func(src source) bool { return src.name == name }) {
		panic(`agent: tool source nil, unnamed or added twice: "` + name + `"`)
	}
	r.sources = append(r.sources, source{name, s})
}

// Offer returns the tools as they are now that a device whose tools setting
// is enabled (nil: every tool but the DefaultOff ones) may call in a turn of
// trigger: a tainted turn gets only Read and taint-exempt tools, an event
// turn Read tools and the ones marked for it.
func (r *Registry) Offer(enabled []string, tainted bool, trigger cadencev2.Trigger) []Tool {
	return offer(r.snapshot(nil), nil, enabled, tainted, trigger)
}

// offer keeps the order of tools, which stays stable for prompt caching. A
// built-in tool must be in the mode's list intersected with the device's; a
// mode is written before the names of tools that come and go are known, so
// the device's list alone decides on a source's tool, and a DefaultOff tool
// only where it names it (Ruling M5d-R5).
func offer(tools []Tool, mode, device []string, tainted bool, trigger cadencev2.Trigger) []Tool {
	enabled := enabledTools(mode, device)
	var out []Tool
	for _, t := range tools {
		var on bool
		switch {
		case t.turnScoped:
			on = true
		case t.DefaultOff:
			on = slices.Contains(device, t.Name)
		case t.dynamic:
			on = device == nil || slices.Contains(device, t.Name)
		default:
			on = enabled == nil || slices.Contains(enabled, t.Name)
		}
		if on && offered(t, tainted, trigger) {
			out = append(out, t)
		}
	}
	return out
}

// Skips is what a snapshot taken now leaves out, in snapshot order.
func (r *Registry) Skips() []Skip {
	_, skips := r.collect()
	return skips
}

// snapshot is collect's tools; with log set, each skip not in the last
// logged snapshot is logged by source and reason, never the tool's name.
func (r *Registry) snapshot(log *slog.Logger) []Tool {
	tools, skips := r.collect()
	if log != nil {
		r.logSkips(log, skips)
	}
	return tools
}

// collect is the static tools, then each source's current tools sorted by
// name, source by source, so the list stays the same while nothing changed.
// A dynamic tool with a bad name, a name taken before it, no Run or an
// invalid schema is left out, and so is a source that panics.
func (r *Registry) collect() ([]Tool, []Skip) {
	r.mu.Lock()
	sources := slices.Clone(r.sources)
	r.mu.Unlock()
	tools := r.static()
	if len(sources) == 0 {
		return tools, nil
	}
	seen := map[string]bool{}
	for _, t := range tools {
		seen[t.Name] = true
	}
	var skips []Skip
	for _, src := range sources {
		got, ok := sourceTools(src.s)
		if !ok {
			skips = append(skips, Skip{Source: src.name, Reason: SkipPanic})
			continue
		}
		slices.SortStableFunc(got, func(a, b Tool) int { return strings.Compare(a.Name, b.Name) })
		for _, t := range got {
			reason := ""
			switch {
			case !dynamicName.MatchString(t.Name):
				reason = SkipName
			case seen[t.Name]:
				reason = SkipDuplicate
			case t.Run == nil:
				reason = SkipRun
			case t.schema == nil:
				var err error
				if t, err = Compile(t); err != nil {
					reason = SkipSchema
				}
			}
			if reason != "" {
				skips = append(skips, Skip{src.name, t.Name, reason})
				continue
			}
			seen[t.Name] = true
			t.dynamic = true
			tools = append(tools, t)
		}
	}
	return slices.Clip(tools), skips
}

// sourceTools copies a source's tools, false when it panics: the value can
// carry the tools' text, so it is dropped.
func sourceTools(s Source) (tools []Tool, ok bool) {
	defer func() {
		if recover() != nil {
			tools, ok = nil, false
		}
	}()
	return slices.Clone(s.Tools()), true
}

func (r *Registry) logSkips(log *slog.Logger, skips []Skip) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := make(map[Skip]bool, len(skips))
	for _, s := range skips {
		if !r.logged[s] && !now[s] {
			log.Warn("agent_tool_skipped", "source", s.Source, "reason", s.Reason)
		}
		now[s] = true
	}
	r.logged = now
}
