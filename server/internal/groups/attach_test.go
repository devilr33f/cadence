package groups_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/groups"
)

// attach (a screen's «play here too») adds a device to the group another member is in without moving its preferred
// leader; the device leaves its old group, which shrinks; a stereo pair and a device in no group refuse
func TestAttach(t *testing.T) {
	r := newRig(t, 5)
	ctx := context.Background()
	a, err := r.svc.Join(ctx, r.ids[0], []string{r.ids[1]})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.Join(ctx, r.ids[2], []string{r.ids[3]}); err != nil {
		t.Fatal(err)
	}
	g, err := r.svc.Attach(ctx, r.ids[1], r.ids[2])
	if err != nil {
		t.Fatal(err)
	}
	if g.ID != a.ID || g.PreferredLeader != r.ids[0] || len(g.Members) != 3 || g.Members[2].DeviceID != r.ids[2] {
		t.Fatalf("attached %+v", g)
	}
	// dev2's old group kept dev3 alone: deleted
	if list, _ := r.svc.List(ctx); len(list) != 1 {
		t.Fatalf("groups %+v", list)
	}
	// again: nothing moves
	if g, err := r.svc.Attach(ctx, r.ids[0], r.ids[2]); err != nil || len(g.Members) != 3 {
		t.Fatalf("again %+v %v", g, err)
	}
	if _, err := r.svc.Attach(ctx, r.ids[3], r.ids[4]); !errors.Is(err, groups.ErrNotFound) {
		t.Fatalf("no group: %v", err)
	}
	pair := fmt.Sprintf(`{"name":"Pair","kind":"stereo_pair","members":[{"deviceId":%q,"channel":"left"},{"deviceId":%q,"channel":"right"}]}`, r.ids[3], r.ids[4])
	r.must(201, http.MethodPost, "/groups", pair)
	if _, err := r.svc.Attach(ctx, r.ids[3], r.ids[0]); !errors.Is(err, groups.ErrStereoPair) {
		t.Fatalf("pair: %v", err)
	}
	if !strings.Contains(r.logs.String(), `"msg":"group_attached"`) {
		t.Fatal("no group_attached line")
	}
}
