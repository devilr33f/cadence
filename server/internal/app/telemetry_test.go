package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"git.shino.boo/devilreef/cadence/server/internal/devices"
	"git.shino.boo/devilreef/cadence/server/internal/events"
	"git.shino.boo/devilreef/cadence/server/internal/settings"
	"git.shino.boo/devilreef/cadence/server/internal/telemetry"
)

func TestLogRetentionIsTheGlobalSetting(t *testing.T) {
	v := settings.Defaults()
	v.LogRetentionDays = 3
	days, err := logRetention(func(context.Context) (settings.Values, error) { return v, nil })(context.Background())
	if err != nil || days != 3 {
		t.Fatalf("%d %v", days, err)
	}
	down := errors.New("down")
	if _, err := logRetention(func(context.Context) (settings.Values, error) { return settings.Values{}, down })(context.Background()); !errors.Is(err, down) {
		t.Fatalf("err %v", err)
	}
}

// the self log is where its drop count is read, so it is built with the parts
func TestTelemetrySelfLogBuiltWithATee(t *testing.T) {
	deps := telemetryDeps{caps: &devices.Service{}, log: slog.New(slog.DiscardHandler)}
	if p := buildTelemetry(deps); p.self != nil {
		t.Fatal("a self log without a tee")
	}
	deps.tee = telemetry.NewTee(io.Discard)
	p := buildTelemetry(deps)
	if p.self == nil || p.self.Dropped() != 0 {
		t.Fatal("no self log with a tee")
	}
}

// the vitals of a stored round reach the bus and the admin route's reader
func TestTelemetryVitalsWired(t *testing.T) {
	bus := events.NewBus(4)
	sub := bus.Watch(nil)
	defer sub.Close()
	p := buildTelemetry(telemetryDeps{caps: &devices.Service{}, bus: bus, log: slog.New(slog.DiscardHandler)})
	var dev pgtype.UUID
	if err := dev.Scan("00000000-0000-0000-0000-000000000001"); err != nil {
		t.Fatal(err)
	}
	at := time.Now()
	p.vitals.Observe([]telemetry.MetricRow{
		{DeviceID: dev, Name: "system.cpu.utilization", Kind: telemetry.KindGauge, Attrs: []byte(`{}`), Time: at, Value: 0.5},
		{DeviceID: dev, Name: "system.memory.used", Kind: telemetry.KindGauge, Attrs: []byte(`{}`), Time: at, Value: 1},
		{DeviceID: dev, Name: "system.memory.limit", Kind: telemetry.KindGauge, Attrs: []byte(`{}`), Time: at, Value: 2},
	})
	select {
	case e := <-sub.C:
		if e.Type != events.DeviceVitals || e.DeviceID != dev.String() {
			t.Fatalf("event %+v", e)
		}
	default:
		t.Fatal("no vitals published")
	}
	if v, ok := p.admin.Vitals(dev.String()); !ok || v.CPU != 0.5 {
		t.Fatalf("route reads %+v %v", v, ok)
	}
}
