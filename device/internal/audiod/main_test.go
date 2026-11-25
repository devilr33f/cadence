package audiod_test

import (
	"os"
	"testing"

	"git.shino.boo/devilreef/cadence/device/internal/audiod/audiodtest"
)

// the supervisor tests run this binary as their fake audiod
func TestMain(m *testing.M) {
	if audiodtest.IsFake() {
		audiodtest.Main()
	}
	os.Exit(m.Run())
}
