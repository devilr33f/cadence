package app

import (
	"context"
	"log/slog"

	"git.shino.boo/devilreef/cadence/server/internal/config"
	"git.shino.boo/devilreef/cadence/server/internal/telemetry"
	"git.shino.boo/devilreef/cadence/server/internal/yandex"
)

func startYandexProxy(ctx context.Context, cfg config.YandexProxyConfig, logs *telemetry.LogStore, secrets yandex.SecretLookup, log *slog.Logger) (func(), error) {
	service, err := yandex.New(cfg, logs, secrets, log)
	if err != nil {
		return nil, err
	}
	if service == nil {
		return func() {}, nil
	}
	if err := service.Start(ctx); err != nil {
		return nil, err
	}
	return service.Close, nil
}
