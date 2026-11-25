package app

import (
	"context"
	"encoding/json"
	"errors"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/settings"
)

var errNoEmbedder = errors.New("app: no embed instance in the global settings")

// globalEmbedName is the instance the global `embed` setting names, empty
// when unset.
func globalEmbedName(global func(context.Context) (settings.Overrides, error)) func(context.Context) (string, error) {
	return globalName(global, "embed")
}

// globalName is the instance a global setting names, empty when unset.
func globalName(global func(context.Context) (settings.Overrides, error), key string) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		o, err := global(ctx)
		if err != nil {
			return "", err
		}
		var name string
		if json.Unmarshal(o[key], &name) != nil {
			return "", nil
		}
		return name, nil
	}
}

// globalEmbedder is the instance the global `embed` setting names: the
// re-embed job serves every device's memories, so a device's own override
// has no say in which model they carry.
func globalEmbedder(
	name func(context.Context) (string, error),
	embed func(ctx context.Context, name string) (providers.Embedder, error),
) func(context.Context) (providers.Embedder, error) {
	return func(ctx context.Context) (providers.Embedder, error) {
		n, err := name(ctx)
		if err != nil {
			return nil, err
		}
		if n == "" {
			return nil, errNoEmbedder
		}
		return embed(ctx, n)
	}
}
