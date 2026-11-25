//go:build cadence_notrace

package tracing_test

import (
	"context"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/tracing"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

// the notrace build records nothing: a turn carries no context and the provider ships nothing
func TestNotraceRecordsNothing(t *testing.T) {
	p := tracing.Setup(nil, "v")
	defer p.Run(context.Background())()
	if tracing.Linked || tracing.Enabled() {
		t.Fatal("notrace build records")
	}
	turn := tracing.StartTurn(context.Background(), time.Time{}, "s", cadencev2.Trigger_TRIGGER_WAKE, nil)
	if turn.Proto() != nil {
		t.Fatalf("turn carried %v", turn.Proto())
	}
	turn.End("ok")
}
