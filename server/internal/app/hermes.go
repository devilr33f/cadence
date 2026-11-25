package app

import (
	"context"
	"errors"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"git.shino.boo/devilreef/cadence/server/internal/integrations/hermes"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/settings"
)

// hermesKind is the hermes task kind on the instance the global `hermes`
// setting names, read once at startup (a change takes a restart); nil when
// the setting is unset or the instance cannot be built. It also returns the
// instance name the kind was built with.
func hermesKind(
	ctx context.Context,
	global func(context.Context) (settings.Overrides, error),
	build func(ctx context.Context, name string) (*hermes.Client, error),
	pool *pgxpool.Pool,
	log *slog.Logger,
) (*hermes.Kind, string) {
	name, err := globalName(global, "hermes")(ctx)
	if err != nil {
		log.Error("hermes_disabled", "code", "settings_unreadable")
		return nil, ""
	}
	if name == "" {
		return nil, ""
	}
	c, err := build(ctx, name)
	if err != nil {
		log.Error("hermes_disabled", "code", instanceCode(err))
		return nil, ""
	}
	return hermes.NewKind(hermes.Deps{Client: c, Pool: pool, Log: log}), name
}

func hermesClient(instances *providers.Instances) func(context.Context, string) (*hermes.Client, error) {
	return func(ctx context.Context, name string) (*hermes.Client, error) {
		return providers.Get[*hermes.Client](ctx, instances, name, providers.KindHermes)
	}
}

func instanceCode(err error) string {
	switch {
	case errors.Is(err, providers.ErrNotFound):
		return "instance_not_found"
	case errors.Is(err, providers.ErrWrongKind):
		return "instance_wrong_kind"
	case errors.Is(err, providers.ErrUnknownModule):
		return "instance_unknown_module"
	}
	return "instance_build_failed"
}
