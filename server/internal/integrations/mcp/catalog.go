package mcp

import (
	"context"
	"log/slog"
	"maps"
	"sync"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/events"
)

const (
	// RefreshEvery re-lists a server after its last good listing
	RefreshEvery = 10 * time.Minute
	// RetryEvery re-lists a server after a failed refresh
	RetryEvery = time.Minute
	// KeepFor keeps a failing server's tools offered, then withdraws them
	KeepFor = 10 * time.Minute

	maxPerServer = 64
	maxOffered   = 128

	// CodeStore: the overrides could not be read or written
	CodeStore = "store"
)

// Entry is one listed tool as the catalog holds it. Its remote name,
// description, annotations and schema are the server's untrusted text: never
// logged.
type Entry struct {
	Instance, Remote string
	// Local is the name the model and the devices' tools settings know; empty
	// for a tool skipped for its remote name
	Local string
	// Description is what the model reads: sanitized, prefixed, capped
	Description string
	// Annotations are the server's hints, for the owner: never applied
	Annotations *sdk.ToolAnnotations
	// SchemaBytes is the inputSchema's size as listed
	SchemaBytes int
	// Tool has Name, Description and Input compiled once; the rest is the
	// caller's to set
	Tool     agent.Tool
	Override Override
	// Skip is why the tool is not offered; empty offers it
	Skip string
}

// Server is one MCP server the catalog should run.
type Server struct {
	Instance string
	// ID is the provider instance's id: a new one, as after a delete and a
	// create under the same name, restarts the server
	ID     string
	Client *Client
}

// State is a server as the catalog sees it.
type State struct {
	Instance, Prefix string
	// Code is the latest failure since the latest good listing: a session
	// that ended or never came up, a failed refresh or store; empty again
	// once a listing succeeds
	Code string
	// ListedAt is the latest good listing; zero before the first
	ListedAt time.Time
	// FailingSince is the first failure since then; zero while none
	FailingSince time.Time
	// Withdrawn: failing for KeepFor, its tools are not offered
	Withdrawn bool
}

type CatalogConfig struct {
	Store *Store
	// Log nil is slog's default
	Log *slog.Logger
	// Clock nil is the real one
	Clock Clock
	// Bus hears each server's changes; nil publishes nothing
	Bus *events.Bus
}

// Catalog runs one supervisor per server and keeps each server's tools under
// local names, checked against the caps and the owner's overrides. It logs
// and publishes instance names, counts, skip reasons and codes only.
type Catalog struct {
	store *Store
	log   *slog.Logger
	clock Clock
	bus   *events.Bus

	// apply serializes Apply
	apply sync.Mutex
	// listing serializes every server's listings and the owner's overrides:
	// local names are unique across servers, and a listing never puts back
	// overrides an owner's change replaced. A token in it holds the lock;
	// a waiter gives up when its ctx ends, so a stalled store holds no stop
	listing chan struct{}
	mu      sync.Mutex
	servers map[string]*server
	entries []Entry
	offered []Entry
}

type server struct {
	cfg    Server
	prefix string
	sup    *Supervisor
	poke   chan struct{}
	// slots holds one token per tool call in flight
	slots  chan struct{}
	cancel context.CancelFunc
	done   chan struct{}

	// guarded by Catalog.mu
	items     []Entry
	overrides map[string]Override
	state     State
	tick      Timer
	expiry    Timer
	logged    string
	// health and fed are what the live feed heard last
	health, fed string
}

func NewCatalog(c CatalogConfig) *Catalog {
	if c.Log == nil {
		c.Log = slog.Default()
	}
	if c.Clock == nil {
		c.Clock = realClock{}
	}
	return &Catalog{
		store: c.Store, log: c.Log, clock: c.Clock, bus: c.Bus, listing: make(chan struct{}, 1), servers: map[string]*server{},
	}
}

// lockListing takes the listing lock, false when ctx ends first.
func (c *Catalog) lockListing(ctx context.Context) bool {
	select {
	case c.listing <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

func (c *Catalog) unlockListing() { <-c.listing }

// Apply runs exactly the servers in want: it starts the new ones, stops the
// ones left out and restarts one whose id or client changed. Stopping waits
// for the server's goroutines; starting never waits for the server. A server
// left out is published as unavailable with code removed.
func (c *Catalog) Apply(want []Server) { c.reconcile(want, true) }

// Close stops every server, publishing nothing of them.
func (c *Catalog) Close() { c.reconcile(nil, false) }

func (c *Catalog) reconcile(want []Server, tell bool) {
	c.apply.Lock()
	defer c.apply.Unlock()
	// the first of an instance listed twice counts
	wanted := make(map[string]Server, len(want))
	var order []string
	for _, w := range want {
		if _, ok := wanted[w.Instance]; !ok && w.Client != nil {
			wanted[w.Instance] = w
			order = append(order, w.Instance)
		}
	}
	c.mu.Lock()
	var gone []*server
	for name, s := range c.servers {
		w, ok := wanted[name]
		if ok && w == s.cfg {
			continue
		}
		gone = append(gone, s)
		delete(c.servers, name)
		// once out of c.servers, nothing of s publishes again
		if !ok && tell {
			c.publish(events.MCPUnavailable, LiveUnavailable{Instance: name, Code: codeRemoved})
		}
	}
	if len(gone) > 0 {
		c.rebuild()
	}
	running := maps.Clone(c.servers)
	c.mu.Unlock()
	// side by side: a legacy session's DELETE may take its grace
	var wg sync.WaitGroup
	for _, s := range gone {
		wg.Go(func() { c.stop(s) })
	}
	wg.Wait()
	for _, name := range order {
		if running[name] == nil {
			c.start(wanted[name])
		}
	}
}

func (c *Catalog) start(w Server) {
	prefix := w.Client.ToolPrefix()
	if prefix == "" {
		prefix = DerivePrefix(w.Instance)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &server{
		cfg: w, prefix: prefix, poke: make(chan struct{}, 1), slots: make(chan struct{}, maxInFlight), cancel: cancel,
		done:  make(chan struct{}),
		state: State{Instance: w.Instance, Prefix: prefix}, fed: noCounts,
	}
	c.mu.Lock()
	c.servers[w.Instance] = s
	c.mu.Unlock()
	c.log.Info("mcp_server_started", "instance", w.Instance)
	s.sup = Supervise(SupervisorConfig{
		Instance: w.Instance, Client: w.Client, Log: c.log,
		OnTools: func(ctx context.Context, tools []RemoteTool) { c.take(ctx, s, tools) },
		OnDown:  func(code string) { c.failed(s, code, false) },
	})
	go c.refresher(ctx, s)
}

// stop ends both goroutines of s before it waits for either: a listing of
// one may wait for the listing lock the other's holds, and every store call
// of a listing ends with its goroutine's ctx.
func (c *Catalog) stop(s *server) {
	s.cancel()
	s.sup.cancel()
	<-s.done
	s.sup.Stop()
	c.mu.Lock()
	stopTimer(s.tick)
	stopTimer(s.expiry)
	c.mu.Unlock()
	c.log.Info("mcp_server_stopped", "instance", s.cfg.Instance)
}

// Offered is every tool offered now, by instance and local name. The slice
// is never changed.
func (c *Catalog) Offered() []Entry {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.offered
}

// Entries is every listed tool, offered or skipped, by instance and local
// name. The slice is never changed.
func (c *Catalog) Entries() []Entry {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.entries
}

// State is a running server's state, false when it is not running.
func (c *Catalog) State(instance string) (State, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.servers[instance]
	if s == nil {
		return State{}, false
	}
	return s.state, true
}

// Override applies p to a tool's overrides, and to the catalog at once
// when its server runs. It waits for a listing under way, so the listing's
// older overrides never replace these, until ctx ends.
func (c *Catalog) Override(ctx context.Context, instance, remote string, p Patch) (Override, error) {
	if !c.lockListing(ctx) {
		return Override{}, ctx.Err()
	}
	defer c.unlockListing()
	o, err := c.store.Update(ctx, instance, remote, p)
	if err != nil {
		return Override{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if s := c.servers[instance]; s != nil {
		if _, ok := s.overrides[remote]; ok {
			s.overrides[remote] = o
			c.rebuild()
		}
	}
	return o, nil
}
