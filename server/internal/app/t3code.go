package app

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/integrations/t3code"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/settings"
	"git.shino.boo/devilreef/cadence/server/internal/subscriptions"
)

// t3Every reconciles even without a poke, so a failed read is tried again.
const t3Every = time.Minute

type t3Deps struct {
	Global func(context.Context) (settings.Values, error)
	Ref    func(ctx context.Context, name string) (providers.Ref, error)
	Build  func(ctx context.Context, name string) (*t3code.Client, error)
	Source *t3code.Source
	// Registry takes the source the first time the setting names an
	// instance, so the module is unknown to the hub until then
	Registry *subscriptions.Registry
	Log      *slog.Logger
	// Every zero is t3Every
	Every time.Duration
	// Disabled hears each name left out; nil keeps it to the loop
	Disabled *mcpDisabled
}

// runT3Code keeps the source following the instances the global `t3code`
// setting names, of kind t3code: at start, at each poke (a settings or
// instance change) and every minute, without a restart. A failed read keeps
// what runs. stop waits for the loop, then stops every instance.
func runT3Code(ctx context.Context, d t3Deps) (poke, stop func()) {
	if d.Every <= 0 {
		d.Every = t3Every
	}
	if d.Disabled == nil {
		d.Disabled = newMCPDisabled()
	}
	ctx, cancel := context.WithCancel(ctx)
	pokes := make(chan struct{}, 1)
	done := make(chan struct{})
	var register sync.Once
	go func() {
		defer close(done)
		t := time.NewTicker(d.Every)
		defer t.Stop()
		for {
			reconcileT3(ctx, d, &register)
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
		d.Source.Close()
	}
}

func reconcileT3(ctx context.Context, d t3Deps, register *sync.Once) {
	v, err := d.Global(ctx)
	if err != nil {
		if ctx.Err() == nil {
			d.Log.Warn("t3code_reconcile_failed", "code", "settings_unreadable")
		}
		return
	}
	var want []t3code.Want
	for _, name := range v.T3Code {
		ref, err := d.Ref(ctx, name)
		if err == nil && ref.Kind != providers.KindT3Code {
			err = providers.ErrWrongKind
		}
		var c *t3code.Client
		if err == nil {
			c, err = d.Build(ctx, name)
		}
		switch {
		case err == nil:
			d.Disabled.clear(name)
			want = append(want, t3code.Want{Instance: name, Client: c})
		case errors.Is(err, providers.ErrNotFound), errors.Is(err, providers.ErrWrongKind),
			errors.Is(err, providers.ErrUnknownModule), errors.Is(err, providers.ErrBuildFailed):
			if code := instanceCode(err); d.Disabled.set(name, code) {
				d.Log.Warn("t3code_disabled", "instance", name, "code", code)
			}
		default:
			if ctx.Err() == nil {
				d.Log.Warn("t3code_reconcile_failed", "code", "store")
			}
			return
		}
	}
	if len(v.T3Code) > 0 && d.Registry != nil {
		register.Do(func() {
			if err := d.Registry.Register(d.Source); err != nil {
				d.Log.Error("t3code_register_failed")
			}
		})
	}
	d.Source.Apply(want)
}

func t3Client(instances *providers.Instances) func(context.Context, string) (*t3code.Client, error) {
	return func(ctx context.Context, name string) (*t3code.Client, error) {
		return providers.Get[*t3code.Client](ctx, instances, name, providers.KindT3Code)
	}
}

// t3InstanceCheck refuses a name added to the `t3code` setting unless it
// names an instance of kind t3code.
func t3InstanceCheck(ref func(ctx context.Context, name string) (providers.Ref, error)) func(context.Context, string) error {
	return func(ctx context.Context, name string) error {
		r, err := ref(ctx, name)
		switch {
		case errors.Is(err, providers.ErrNotFound), errors.Is(err, providers.ErrUnknownModule):
			return settings.ErrInvalid
		case err != nil:
			return err
		case r.Kind != providers.KindT3Code:
			return settings.ErrInvalid
		}
		return nil
	}
}

// t3Listed reads the global t3code setting.
func t3Listed(global func(context.Context) (settings.Values, error)) func(context.Context) ([]string, error) {
	return func(ctx context.Context) ([]string, error) {
		v, err := global(ctx)
		return v.T3Code, err
	}
}
