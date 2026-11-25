package app

import (
	"context"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/config"
)

func TestYandexProxyDisabledNeedsNoDependencies(t *testing.T) {
	stop, err := startYandexProxy(context.Background(), config.YandexProxyConfig{}, nil, nil, nil)
	if err != nil || stop == nil {
		t.Fatalf("nil stop: %v", err)
	}
	stop()
}
