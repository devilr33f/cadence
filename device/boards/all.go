package boards

import (
	"log/slog"
	"os"

	androidtv "git.shino.boo/devilreef/cadence/device/boards/android-tv"
	rpi "git.shino.boo/devilreef/cadence/device/boards/rpi"
	"git.shino.boo/devilreef/cadence/device/boards/sim"
	simtv "git.shino.boo/devilreef/cadence/device/boards/sim-tv"
	vkcapsula "git.shino.boo/devilreef/cadence/device/boards/vk-capsula-mini"
	micro "git.shino.boo/devilreef/cadence/device/boards/yandex-micro"
	midi "git.shino.boo/devilreef/cadence/device/boards/yandex-midi"
	pickle "git.shino.boo/devilreef/cadence/device/boards/yandex-pickle"
	"git.shino.boo/devilreef/cadence/device/internal/board"
	"git.shino.boo/devilreef/cadence/device/internal/config"
	"git.shino.boo/devilreef/cadence/device/internal/telemetry"
)

// All are the boards cadenced knows; each collects its logs into env's pipeline, if there is one, and reports its
// system metrics and counts through menv.
func All(cfg config.Config, log *slog.Logger, env telemetry.LogsEnv, menv telemetry.MetricsEnv) []board.Board {
	simOpts := []sim.Option{sim.WithLogs(env), sim.WithMetrics(menv)}
	if cfg.Audiod {
		simOpts = append(simOpts, sim.WithAudiod(cfg.AudiodExecutable()))
	}
	// the e2e's fake cdn: the sim never downloads spotify's build
	if cdn, ok := sim.SoloistCDNFromEnv(os.Getenv); ok {
		simOpts = append(simOpts, sim.WithSoloistCDN(cdn))
	}
	// the e2e's timers time out within seconds
	if d, ok := sim.TimerLimitFromEnv(os.Getenv); ok {
		simOpts = append(simOpts, sim.WithTimerLimit(d))
	}
	simTVOpts := []simtv.Option{simtv.WithLogs(env), simtv.WithMetrics(menv)}
	if cfg.Audiod {
		simTVOpts = append(simTVOpts, simtv.WithAudiod(cfg.AudiodExecutable()))
	}
	return []board.Board{
		sim.New(os.Getenv, cfg.DataDir, log, simOpts...),
		pickle.New(cfg.DataDir, log, pickle.WithAudiod(cfg.AudiodBinary), pickle.WithLogs(env), pickle.WithMetrics(menv)),
		midi.New(cfg.DataDir, log, midi.WithAudiod(cfg.AudiodBinary), midi.WithLogs(env), midi.WithMetrics(menv)),
		micro.New(cfg.DataDir, log, micro.WithAudiod(cfg.AudiodBinary), micro.WithLogs(env), micro.WithMetrics(menv)),
		simtv.New(os.Getenv, cfg.DataDir, log, simTVOpts...),
		// the deb ships audiod at the default path
		rpi.New(os.Getenv, cfg.DataDir, log, rpi.WithAudiod(cfg.AudiodExecutable()), rpi.WithLogs(env),
			rpi.WithMetrics(menv)),
		androidtv.New(os.Getenv, cfg.DataDir, log, androidtv.WithLogs(env), androidtv.WithMetrics(menv)),
		vkcapsula.New(cfg.DataDir, log, vkcapsula.WithAudiod(cfg.AudiodBinary), vkcapsula.WithLogs(env), vkcapsula.WithMetrics(menv)),
	}
}
