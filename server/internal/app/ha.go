package app

import (
	"context"
	"log/slog"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/agent/tools"
	"git.shino.boo/devilreef/cadence/server/internal/integrations/ha"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/settings"
)

// homeAssistant is the client of the instance the global `homeAssistant`
// setting names, read once at startup (a change takes a restart); nil when
// the setting is unset or the instance cannot be built. It also returns the
// instance name the client was built with.
func homeAssistant(
	ctx context.Context,
	global func(context.Context) (settings.Overrides, error),
	build func(ctx context.Context, name string) (*ha.Client, error),
	log *slog.Logger,
) (*ha.Client, string) {
	name, err := globalName(global, "homeAssistant")(ctx)
	if err != nil {
		log.Error("ha_disabled", "code", "settings_unreadable")
		return nil, ""
	}
	if name == "" {
		return nil, ""
	}
	c, err := build(ctx, name)
	if err != nil {
		log.Error("ha_disabled", "code", instanceCode(err))
		return nil, ""
	}
	return c, name
}

func haClient(instances *providers.Instances) func(context.Context, string) (*ha.Client, error) {
	return func(ctx context.Context, name string) (*ha.Client, error) {
		return providers.Get[*ha.Client](ctx, instances, name, providers.KindHomeAssistant)
	}
}

// watchHome builds the live view of the home on c, so it is registered before
// c runs; nil for a nil c.
func watchHome(c *ha.Client, log *slog.Logger) *ha.Home {
	if c == nil {
		return nil
	}
	return ha.NewHome(c, ha.HomeOptions{Now: time.Now, Log: log})
}

// runHomeAssistant keeps h's client connected until stop, which waits for the
// connection to close; a nil h runs nothing.
func runHomeAssistant(ctx context.Context, h *ha.Home) (stop func()) {
	if h == nil {
		return func() {}
	}
	rctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.Client().Run(rctx)
	}()
	return func() {
		cancel()
		<-done
	}
}

// homeContext is each turn's note of the home h shows, ranked by u; nil
// without a home, which leaves the note out.
func homeContext(h *ha.Home, u agent.HomeUsage) func(context.Context, string, settings.Values) (string, error) {
	if h == nil {
		return nil
	}
	return agent.HomeNotes(h, u)
}

// homeTools are the Home Assistant tools, offered only while h runs, with the
// sensitive-device policy and counting what they change in u. The shared
// confirmation tracker lets the cards finish what a pending answer started.
func homeTools(h *ha.Home, s tools.SettingsFunc, u tools.HAUsage, shared ...*tools.HAConfirmations) []agent.Tool {
	var confirmations *tools.HAConfirmations
	if len(shared) > 0 {
		confirmations = shared[0]
	}
	d := tools.HADeps{Home: h, Settings: s, Policy: tools.HASensitivePolicy(s), Usage: u, Confirmations: confirmations}
	return []agent.Tool{tools.HAControl(d), tools.HASearch(d), tools.HAInspect(d)}
}
