package providers_test

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

func TestGuardRecovers(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	if providers.Guard(log, "m", "stt_failed", func() {}) {
		t.Fatal("clean run reported failed")
	}
	if !providers.Guard(log, "m", "stt_failed", func() { panic("secret-xyz") }) {
		t.Fatal("panic not reported")
	}
	out := buf.String()
	if strings.Contains(out, "secret-xyz") || !strings.Contains(out, "provider_panic") ||
		!strings.Contains(out, "module=m") || !strings.Contains(out, "code=stt_failed") {
		t.Fatalf("log = %q", out)
	}
}
