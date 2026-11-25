package mcp_test

import (
	"os"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/storetest"
)

func TestMain(m *testing.M) {
	// the hash test's child needs no database
	if os.Getenv(hashChild) != "" {
		os.Exit(m.Run())
	}
	os.Exit(storetest.Main(m))
}
