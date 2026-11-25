package enroll_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/enroll"
	"git.shino.boo/devilreef/cadence/server/internal/events"
)

type recorder struct {
	mu     sync.Mutex
	events []events.Event
}

func (r *recorder) Publish(e events.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

// a completed enrolment publishes device_added, offline: its token opened no
// link yet; creating the device and a refused code publish nothing (M8-D2)
func TestEnrollPublishesDeviceAdded(t *testing.T) {
	ctx := context.Background()
	s, clk, _ := newSvc(t)
	rec := &recorder{}
	s.PublishTo(rec)
	_, code, err := s.CreateDevice(ctx, "kitchen", "sim")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Enroll(ctx, code, enroll.Fingerprint{Board: "other"}); err == nil {
		t.Fatal("enrolled another board")
	}
	id, _, err := s.Enroll(ctx, code, enroll.Fingerprint{Board: "sim"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Enroll(ctx, code, enroll.Fingerprint{Board: "sim"}); err == nil {
		t.Fatal("enrolled twice")
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.events) != 1 {
		t.Fatalf("published %v", rec.events)
	}
	e := rec.events[0]
	data, _ := json.Marshal(e.Data)
	if e.Type != events.DeviceAdded || e.DeviceID != id || !e.At.Equal(clk.now()) ||
		string(data) != `{"name":"kitchen","model":"sim","online":false}` {
		t.Fatalf("published %+v %s", e, data)
	}
}
