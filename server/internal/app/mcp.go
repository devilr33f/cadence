package app

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/integrations/mcp"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/settings"
)

// mcpEvery reconciles even without a poke, so a failed read is tried again.
const mcpEvery = time.Minute

type mcpDeps struct {
	Global func(context.Context) (settings.Values, error)
	Ref    func(ctx context.Context, name string) (providers.Ref, error)
	Build  func(ctx context.Context, name string) (*mcp.Client, error)
	// Servers is the catalog; Close stops every server at shutdown, publishing
	// none as removed
	Servers interface {
		Apply([]mcp.Server)
		Close()
	}
	Log *slog.Logger
	// Every zero is mcpEvery
	Every time.Duration
	// Disabled hears each name left out; nil keeps it to the loop
	Disabled *mcpDisabled
}

// mcpDisabled is the code each name the reconcile left out was logged with,
// for the admin API.
type mcpDisabled struct {
	mu    sync.Mutex
	codes map[string]string
}

func newMCPDisabled() *mcpDisabled { return &mcpDisabled{codes: map[string]string{}} }

// Code is why name was left out, "" when it runs or never was.
func (d *mcpDisabled) Code(name string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.codes[name]
}

// set records code for name, false when it was recorded already.
func (d *mcpDisabled) set(name, code string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.codes[name] == code {
		return false
	}
	d.codes[name] = code
	return true
}

func (d *mcpDisabled) clear(name string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.codes, name)
}

// runMCP keeps the catalog running the servers the global `mcp` setting
// names, of instances of kind mcp: at start, at each poke (a settings or
// instance change) and every minute, without a restart. A failed read keeps
// what runs. stop waits for the loop, then stops every server.
func runMCP(ctx context.Context, d mcpDeps) (poke, stop func()) {
	if d.Every <= 0 {
		d.Every = mcpEvery
	}
	if d.Disabled == nil {
		d.Disabled = newMCPDisabled()
	}
	ctx, cancel := context.WithCancel(ctx)
	pokes := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(d.Every)
		defer t.Stop()
		for {
			reconcileMCP(ctx, d)
			select {
			case <-ctx.Done():
				return
			case <-pokes:
			case <-t.C:
			}
		}
	}()
	poke = func() {
		select {
		case pokes <- struct{}{}:
		default:
		}
	}
	return poke, func() {
		cancel()
		<-done
		d.Servers.Close()
	}
}

// reconcileMCP applies the servers the setting names now; d.Disabled holds
// the code last logged for each name left out, so each is logged once.
func reconcileMCP(ctx context.Context, d mcpDeps) {
	v, err := d.Global(ctx)
	if err != nil {
		if ctx.Err() == nil {
			d.Log.Warn("mcp_reconcile_failed", "code", "settings_unreadable")
		}
		return
	}
	var want []mcp.Server
	for _, name := range v.MCP {
		ref, err := d.Ref(ctx, name)
		if err == nil && ref.Kind != providers.KindMCP {
			err = providers.ErrWrongKind
		}
		var c *mcp.Client
		if err == nil {
			c, err = d.Build(ctx, name)
		}
		switch {
		case err == nil:
			d.Disabled.clear(name)
			want = append(want, mcp.Server{Instance: name, ID: ref.ID, Client: c})
		case errors.Is(err, providers.ErrNotFound), errors.Is(err, providers.ErrWrongKind),
			errors.Is(err, providers.ErrUnknownModule), errors.Is(err, providers.ErrBuildFailed):
			if code := instanceCode(err); d.Disabled.set(name, code) {
				d.Log.Warn("mcp_disabled", "instance", name, "code", code)
			}
		default:
			if ctx.Err() == nil {
				d.Log.Warn("mcp_reconcile_failed", "code", "store")
			}
			return
		}
	}
	d.Servers.Apply(want)
}

func mcpClient(instances *providers.Instances) func(context.Context, string) (*mcp.Client, error) {
	return func(ctx context.Context, name string) (*mcp.Client, error) {
		return providers.Get[*mcp.Client](ctx, instances, name, providers.KindMCP)
	}
}

// mcpIsInstance says whether check takes name: an instance of kind mcp.
func mcpIsInstance(check func(context.Context, string) error) func(context.Context, string) (bool, error) {
	return func(ctx context.Context, name string) (bool, error) {
		err := check(ctx, name)
		if errors.Is(err, settings.ErrInvalid) {
			return false, nil
		}
		return err == nil, err
	}
}

// mcpListed reads the global mcp setting.
func mcpListed(global func(context.Context) (settings.Values, error)) func(context.Context) ([]string, error) {
	return func(ctx context.Context) ([]string, error) {
		v, err := global(ctx)
		return v.MCP, err
	}
}

// mcpInstanceCheck refuses a name added to the `mcp` setting unless it names
// an instance of kind mcp.
func mcpInstanceCheck(ref func(ctx context.Context, name string) (providers.Ref, error)) func(context.Context, string) error {
	return func(ctx context.Context, name string) error {
		r, err := ref(ctx, name)
		switch {
		case errors.Is(err, providers.ErrNotFound), errors.Is(err, providers.ErrUnknownModule):
			return settings.ErrInvalid
		case err != nil:
			return err
		case r.Kind != providers.KindMCP:
			return settings.ErrInvalid
		}
		return nil
	}
}
