package ha_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"git.shino.boo/devilreef/cadence/server/internal/integrations/ha"
	"git.shino.boo/devilreef/cadence/server/internal/store/db"
	"git.shino.boo/devilreef/cadence/server/internal/storetest"
)

// usageTx runs a test in one transaction, where now() stands still: a shift
// of updated_at by SQL is then an exact span of time.
type usageTx struct {
	t     *testing.T
	tx    pgx.Tx
	usage *ha.Usage
	logs  *bytes.Buffer
}

func newUsageTx(t *testing.T) *usageTx {
	t.Helper()
	ctx := context.Background()
	tx, err := storetest.Pool(t).Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(ctx) })
	logs := &bytes.Buffer{}
	return &usageTx{t: t, tx: tx, usage: ha.NewUsage(db.New(tx), slog.New(slog.NewTextHandler(logs, nil))), logs: logs}
}

func (u *usageTx) device() uuid.UUID {
	u.t.Helper()
	d, err := db.New(u.tx).CreateDevice(context.Background(), db.CreateDeviceParams{Name: rand.Text(), Model: "sim"})
	if err != nil {
		u.t.Fatal(err)
	}
	return d.ID.Bytes
}

func (u *usageTx) record(device uuid.UUID, entities ...string) {
	u.t.Helper()
	for _, e := range entities {
		u.usage.Record(context.Background(), device.String(), e)
	}
	if u.logs.Len() != 0 {
		u.t.Fatalf("logged %s", u.logs)
	}
}

// ago moves the device's uses d into the past.
func (u *usageTx) ago(device uuid.UUID, d time.Duration) {
	u.t.Helper()
	if _, err := u.tx.Exec(context.Background(), "UPDATE ha_usage SET updated_at = updated_at - make_interval(secs => $2) WHERE device_id = $1::uuid",
		device.String(), d.Seconds()); err != nil {
		u.t.Fatal(err)
	}
}

func (u *usageTx) top(device uuid.UUID, n int) []ha.Scored {
	u.t.Helper()
	got, err := u.usage.Top(context.Background(), device, n)
	if err != nil {
		u.t.Fatal(err)
	}
	return got
}

func (u *usageTx) household(n int) []ha.Scored {
	u.t.Helper()
	got, err := u.usage.Household(context.Background(), n)
	if err != nil {
		u.t.Fatal(err)
	}
	return got
}

const fortnight = 14 * 24 * time.Hour

func sc(id string, score float64) ha.Scored { return ha.Scored{EntityID: id, Score: score} }

func TestUsageHalvesEveryFortnight(t *testing.T) {
	u := newUsageTx(t)
	dev := u.device()
	u.record(dev, "light.kitchen")
	if got := u.top(dev, 5); !reflect.DeepEqual(got, []ha.Scored{sc("light.kitchen", 1)}) {
		t.Fatalf("first use %+v", got)
	}
	u.ago(dev, fortnight)
	if got := u.top(dev, 5); !reflect.DeepEqual(got, []ha.Scored{sc("light.kitchen", 0.5)}) {
		t.Fatalf("after 14 days %+v", got)
	}
	// a use decays the score to now, then counts one
	u.record(dev, "light.kitchen")
	if got := u.top(dev, 5); !reflect.DeepEqual(got, []ha.Scored{sc("light.kitchen", 1.5)}) {
		t.Fatalf("used again %+v", got)
	}
	u.ago(dev, 2*fortnight)
	if got := u.top(dev, 5); !reflect.DeepEqual(got, []ha.Scored{sc("light.kitchen", 0.375)}) {
		t.Fatalf("after 28 more days %+v", got)
	}
	// a clock stepped back never grows a score
	u.ago(dev, -fortnight)
	u.ago(dev, -3*fortnight)
	if got := u.top(dev, 5); !reflect.DeepEqual(got, []ha.Scored{sc("light.kitchen", 1.5)}) {
		t.Fatalf("updated in the future %+v", got)
	}
}

func TestUsageRanksByScoreThenID(t *testing.T) {
	u := newUsageTx(t)
	dev, other := u.device(), u.device()
	u.record(dev, "switch.fan", "light.b", "light.a", "light.c", "switch.fan")
	u.record(other, "light.z", "light.z", "light.z")
	want := []ha.Scored{sc("switch.fan", 2), sc("light.a", 1), sc("light.b", 1), sc("light.c", 1)}
	if got := u.top(dev, 10); !reflect.DeepEqual(got, want) {
		t.Fatalf("top %+v", got)
	}
	if got := u.top(dev, 2); !reflect.DeepEqual(got, want[:2]) {
		t.Fatalf("top 2 %+v", got)
	}
	if got := u.top(uuid.New(), 10); len(got) != 0 {
		t.Fatalf("a device never seen %+v", got)
	}
	for _, n := range []int{0, -1} {
		if got := u.top(dev, n); len(got) != 0 {
			t.Fatalf("top %d %+v", n, got)
		}
	}
	// what decays falls behind what was used since
	u.ago(dev, fortnight)
	u.record(dev, "light.c")
	want = []ha.Scored{sc("light.c", 1.5), sc("switch.fan", 1), sc("light.a", 0.5), sc("light.b", 0.5)}
	if got := u.top(dev, 10); !reflect.DeepEqual(got, want) {
		t.Fatalf("after a fortnight %+v", got)
	}
}

func TestUsageHouseholdSumsDevices(t *testing.T) {
	u := newUsageTx(t)
	a, b, gone := u.device(), u.device(), u.device()
	u.record(a, "light.hall", "light.hall", "switch.fan")
	u.record(b, "light.hall", "switch.fan", "climate.ac", "climate.ac")
	u.ago(b, fortnight)
	u.record(gone, "lock.door", "lock.door", "lock.door", "lock.door", "lock.door")
	if _, err := u.tx.Exec(context.Background(), "DELETE FROM devices WHERE id = $1::uuid", gone.String()); err != nil {
		t.Fatal(err)
	}
	// a device's uses go with it
	want := []ha.Scored{sc("light.hall", 2.5), sc("switch.fan", 1.5), sc("climate.ac", 1)}
	if got := u.household(10); !reflect.DeepEqual(got, want) {
		t.Fatalf("household %+v", got)
	}
	if got := u.household(1); !reflect.DeepEqual(got, want[:1]) {
		t.Fatalf("household 1 %+v", got)
	}
	if got := u.household(0); len(got) != 0 {
		t.Fatalf("household 0 %+v", got)
	}
	if got := u.top(gone, 10); len(got) != 0 {
		t.Fatalf("a deleted device %+v", got)
	}
}

// a ranking hint never fails the control that counted it: Record logs a code
// and never an id
func TestUsageRecordFailuresAreCodes(t *testing.T) {
	pool := storetest.Pool(t)
	dev, err := db.New(pool).CreateDevice(context.Background(), db.CreateDeviceParams{Name: rand.Text(), Model: "sim"})
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))
	usage := ha.NewUsage(db.New(pool), log)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, cancel2 := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel2()
	bg := context.Background()
	for _, c := range []struct {
		ctx            context.Context
		device, entity string
		code           string
	}{
		{bg, "d1", "light.secret_kitchen", "bad_device"},
		{bg, "", "light.secret_kitchen", "bad_device"},
		{bg, dev.ID.String(), "Light.Secret_kitchen", "bad_entity"},
		{bg, dev.ID.String(), "secret_kitchen", "bad_entity"},
		{bg, dev.ID.String(), "light.secret kitchen", "bad_entity"},
		{bg, dev.ID.String(), "light." + strings.Repeat("s", 123), "bad_entity"},
		// a device the store does not know
		{bg, uuid.New().String(), "light.secret_kitchen", "store"},
		{canceled, dev.ID.String(), "light.secret_kitchen", "canceled"},
		{expired, dev.ID.String(), "light.secret_kitchen", "timeout"},
	} {
		logs.Reset()
		usage.Record(c.ctx, c.device, c.entity)
		if got := strings.TrimSpace(logs.String()); !strings.HasSuffix(got, "msg=ha_usage_failed code="+c.code) {
			t.Fatalf("%q %q: logged %s", c.device, c.entity, got)
		}
	}
	// the longest id passes
	logs.Reset()
	usage.Record(bg, dev.ID.String(), "light."+strings.Repeat("s", 122))
	if logs.Len() != 0 {
		t.Fatalf("128 characters: %s", logs.String())
	}
	if got, err := usage.Top(bg, dev.ID.Bytes, 10); err != nil || len(got) != 1 {
		t.Fatalf("recorded %+v %v", got, err)
	}
	// a store that panics: the value can carry the id, so it is dropped
	logs.Reset()
	ha.NewUsage(nil, log).Record(bg, dev.ID.String(), "light.secret_kitchen")
	if got := strings.TrimSpace(logs.String()); !strings.HasSuffix(got, "msg=ha_usage_failed code=panic") {
		t.Fatalf("panic logged %s", got)
	}
	if _, err := usage.Top(canceled, dev.ID.Bytes, 10); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("top on a canceled ctx: %v", err)
	}
	if _, err := usage.Household(canceled, 10); err == nil {
		t.Fatal("household on a canceled ctx")
	}
}
