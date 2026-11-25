package pickle_test

import (
	"os"
	"testing"

	"git.shino.boo/devilreef/cadence/device/internal/audiod/audiodtest"
)

// the audiod test runs this binary as the board's fake audiod
func TestMain(m *testing.M) {
	if audiodtest.IsFake() {
		audiodtest.Main()
	}
	os.Exit(m.Run())
}
