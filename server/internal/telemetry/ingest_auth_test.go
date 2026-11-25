package telemetry_test

import (
	"strings"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/telemetry"
)

// a token lookup that hangs answers 503 once its time is up, so a device's
// request never waits past its own timeout for it
func TestAuthLookupTimesOut(t *testing.T) {
	r := newRig(t)
	telemetry.SetAuthTimeout(r.in, 50*time.Millisecond)
	r.set(func() { r.authDelay = 5 * time.Second })
	began := time.Now()
	if a := r.post(logsPath, oneLine(), headers(tokenA)); a.status != 503 || len(a.body) != 0 {
		t.Fatalf("hung lookup: %d %q", a.status, a.body)
	}
	if d := time.Since(began); d > 2*time.Second {
		t.Fatalf("answered after %v", d)
	}
	if got := r.storedLogs(); len(got) != 0 {
		t.Fatalf("stored %d", len(got))
	}
	r.set(func() { r.authDelay = 0 })
	if a := r.post(logsPath, oneLine(), headers(tokenA)); a.status != 200 {
		t.Fatalf("lookup back: %d", a.status)
	}
}

// the token store down fails every device's every request: the warning goes
// out once a window, server-wide, and the window's end counts those it held
// back; a window with none held back closes, and the next failure is logged at once
func TestAuthFailureWarnedOncePerWindow(t *testing.T) {
	r := newRig(t)
	const window = 300 * time.Millisecond
	telemetry.SetAuthWarnEvery(r.in, window)
	r.set(func() { r.authErr = errDown })
	warnings := func() int { return strings.Count(r.logs.String(), `"code":"auth"`) }
	for i, path := range []string{logsPath, logsPath, metricsPath, logsPath, metricsPath} {
		token := tokenA
		if i%2 == 1 {
			token = tokenB
		}
		if a := r.post(path, oneLine(), headers(token)); a.status != 503 {
			t.Fatalf("request %d: %d", i, a.status)
		}
	}
	if n := warnings(); n != 1 {
		t.Fatalf("%d warnings in the window:\n%s", n, r.logs.String())
	}
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(r.logs.String(), `"msg":"otlp_ingest_failed","code":"auth","suppressed":4`) {
		if time.Now().After(deadline) {
			t.Fatalf("no count at the window's end:\n%s", r.logs.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	// nothing held back in the next window: it ends without a line
	time.Sleep(3 * window)
	if n := warnings(); n != 2 {
		t.Fatalf("%d warnings:\n%s", n, r.logs.String())
	}
	if a := r.post(logsPath, oneLine(), headers(tokenA)); a.status != 503 {
		t.Fatalf("after the windows: %d", a.status)
	}
	if n := warnings(); n != 3 {
		t.Fatalf("the failure after a quiet window was not logged at once:\n%s", r.logs.String())
	}
	if l := r.logs.String(); strings.Contains(l, "hunter2") || strings.Contains(l, r.devA) || strings.Contains(l, tokenA) {
		t.Fatalf("log %s", l)
	}
}
