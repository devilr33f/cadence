package exa_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/providers/exa"
	"git.shino.boo/devilreef/cadence/server/internal/providers/probetest"
)

// exa has no free authenticated call: the test answers unsupported and never
// runs a billed search
func TestProbeIsUnsupportedAndSendsNothing(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer srv.Close()
	s := exa.New(exa.Config{BaseURL: srv.URL}, probetest.Key, probetest.Client(srv))
	probetest.Want(t, exa.Module.Probe(context.Background(), s), "unsupported")
	if hits.Load() != 0 {
		t.Fatalf("%d requests", hits.Load())
	}
}
