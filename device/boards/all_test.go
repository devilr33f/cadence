package boards_test

import (
	"slices"
	"testing"

	"git.shino.boo/devilreef/cadence/device/boards"
	"git.shino.boo/devilreef/cadence/device/internal/config"
	"git.shino.boo/devilreef/cadence/device/internal/telemetry"
)

func TestAllBoards(t *testing.T) {
	var got []string
	for _, b := range boards.All(config.Config{Board: "sim", DataDir: t.TempDir()}, nil, telemetry.LogsEnv{}, telemetry.MetricsEnv{}) {
		got = append(got, b.ID())
	}
	if !slices.Equal(got, []string{"sim", "yandex-pickle", "yandex-midi", "yandex-micro", "sim-tv", "rpi", "android-tv", "vk-capsula-mini"}) {
		t.Fatalf("boards = %v", got)
	}
}
