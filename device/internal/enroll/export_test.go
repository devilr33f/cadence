package enroll

import "git.shino.boo/devilreef/cadence/device/internal/config"

func SetStageConfig(f func(string, config.Config) (func() error, func(), error)) (restore func()) {
	old := stageConfig
	stageConfig = f
	return func() { stageConfig = old }
}
