package telemetry_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/events"
	"git.shino.boo/devilreef/cadence/server/internal/store/db"
	"git.shino.boo/devilreef/cadence/server/internal/storetest"
	"git.shino.boo/devilreef/cadence/server/internal/telemetry"
)

// every stored row, a device's or the server's, reaches the live feed while
// a feed subscribed to logs; a batch the store refused reaches nothing, and
// nothing is published while no feed tails
func TestStoredLinesPublished(t *testing.T) {
	pool := storetest.Pool(t)
	q := db.New(pool)
	dev, err := q.CreateDevice(context.Background(), db.CreateDeviceParams{Name: "kitchen", Model: "sim"})
	if err != nil {
		t.Fatal(err)
	}
	store := telemetry.NewLogStore(q)
	bus := events.NewBus(16)
	sub := bus.Watch(nil)
	defer sub.Close()
	var tailing atomic.Bool
	tailing.Store(true)
	store.OnInsert(telemetry.PublishLogs(bus, tailing.Load))

	at := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	rows := []telemetry.LogRow{
		{Time: at, Observed: at, DeviceID: dev.ID, Source: "soloist", Severity: 13, Body: "third-party line", Truncated: true},
		{Time: at.Add(time.Second), Observed: at, Source: "server", Severity: 9, Body: `{"msg":"x"}`},
	}
	if err := store.Insert(context.Background(), rows); err != nil {
		t.Fatal(err)
	}
	for i, want := range []struct {
		device string
		data   telemetry.LogLine
	}{
		{dev.ID.String(), telemetry.LogLine{DeviceID: dev.ID.String(), Source: "soloist", Severity: 13, Time: at, Body: "third-party line"}},
		{"", telemetry.LogLine{Source: "server", Severity: 9, Time: at.Add(time.Second), Body: `{"msg":"x"}`}},
	} {
		select {
		case e := <-sub.C:
			if e.Type != events.LogLine || e.DeviceID != want.device || !e.At.Equal(want.data.Time) || e.Data != want.data {
				t.Fatalf("event %d: %+v", i, e)
			}
		case <-time.After(time.Second):
			t.Fatalf("event %d missing", i)
		}
	}

	// a row the store refuses (postgres text holds no NUL) fails the insert: nothing is published
	if err := store.Insert(context.Background(), []telemetry.LogRow{{Time: at, Observed: at, DeviceID: dev.ID, Source: "x", Body: "y\x00"}}); err == nil {
		t.Fatal("a refused insert passed")
	}
	select {
	case e := <-sub.C:
		t.Fatalf("published %+v", e)
	case <-time.After(50 * time.Millisecond):
	}

	tailing.Store(false)
	if err := store.Insert(context.Background(), rows[1:]); err != nil {
		t.Fatal(err)
	}
	select {
	case e := <-sub.C:
		t.Fatalf("published with no feed tailing: %+v", e)
	case <-time.After(50 * time.Millisecond):
	}
}
