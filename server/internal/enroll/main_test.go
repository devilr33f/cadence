package enroll_test

import (
	"os"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/storetest"
)

func TestMain(m *testing.M) { os.Exit(storetest.Main(m)) }
