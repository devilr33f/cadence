package volume_test

import (
	"bytes"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"git.shino.boo/devilreef/cadence/device/internal/volume"
)

// ramp follows a setter's transient path: the levels it put on the hardware above the level in force
type ramp struct{ applied []int }

func (r *ramp) apply(level int) error {
	r.applied = append(r.applied, level)
	return nil
}

// an alarm's ramp raises the hardware above the level in force and nothing else: never stored, never heard, never
// below the level in force; a plain set or Reassert puts the level in force back (rule 7)
func TestTransientRaisesTheHardwareOnly(t *testing.T) {
	dir := t.TempDir()
	stored(t, dir, 30)
	logs := &bytes.Buffer{}
	s := volume.NewSetter(volume.NewStore(dir), 50, slog.New(slog.NewTextHandler(logs, nil)))
	r := follow(s)
	tr := &ramp{}
	s.FollowTransient(tr.apply)

	for _, c := range []struct {
		level int
		want  bool
	}{{20, false}, {30, false}, {35, true}, {150, true}} {
		if raised, err := s.Transient(c.level); err != nil || raised != c.want {
			t.Fatalf("transient %d = %t, %v", c.level, raised, err)
		}
	}
	if !slices.Equal(tr.applied, []int{35, 100}) {
		t.Fatalf("transient applied %v", tr.applied)
	}
	if level, known := s.Current(); level != 30 || !known {
		t.Fatalf("level in force %d, %t", level, known)
	}
	if level, _ := volume.NewStore(dir).Load(); level != 30 {
		t.Fatalf("stored %d", level)
	}
	if len(r.applied) != 0 || len(r.heard) != 0 {
		t.Fatalf("plain path applied %v, heard %v", r.applied, r.heard)
	}

	s.Reassert()
	if !slices.Equal(r.applied, []int{30}) || len(r.heard) != 0 {
		t.Fatalf("reassert applied %v, heard %v", r.applied, r.heard)
	}
	if !strings.Contains(logs.String(), "msg=volume_reapplied found=100 level=30") {
		t.Fatalf("log: %s", logs)
	}
	// nothing stands above the level in force: put back all the same, quietly
	s.Reassert()
	if !slices.Equal(r.applied, []int{30, 30}) || strings.Count(logs.String(), "volume_reapplied") != 1 {
		t.Fatalf("second reassert applied %v; log: %s", r.applied, logs)
	}

	// a plain change puts its own level on the hardware: the transient one is gone with it
	if _, err := s.Transient(60); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Set(40, volume.OriginCommand); err != nil {
		t.Fatal(err)
	}
	s.Reassert()
	if !slices.Equal(r.applied, []int{30, 30, 40, 40}) || strings.Count(logs.String(), "volume_reapplied") != 1 {
		t.Fatalf("after a plain set applied %v; log: %s", r.applied, logs)
	}
}

// a level that is the default, not the device's own, is never ramped from; a board without the path never ramps
func TestTransientRefusedUnknownOrWithoutAPath(t *testing.T) {
	s := volume.NewSetter(volume.NewStore(t.TempDir()), 50, nil)
	follow(s)
	tr := &ramp{}
	s.FollowTransient(tr.apply)
	if raised, err := s.Transient(80); !errors.Is(err, volume.ErrUnknown) || raised {
		t.Fatalf("unknown: %t, %v", raised, err)
	}
	if len(tr.applied) != 0 {
		t.Fatalf("applied %v", tr.applied)
	}

	known := volume.NewSetter(volume.NewStore(t.TempDir()), 50, nil)
	if _, err := known.Set(30, volume.OriginButton); err != nil {
		t.Fatal(err)
	}
	if raised, err := known.Transient(80); !errors.Is(err, volume.ErrNoTransient) || raised {
		t.Fatalf("no path: %t, %v", raised, err)
	}

	// a path that fails left nothing raised
	failing := volume.NewSetter(volume.NewStore(t.TempDir()), 50, nil)
	if _, err := failing.Set(30, volume.OriginButton); err != nil {
		t.Fatal(err)
	}
	down := errors.New("down")
	failing.FollowTransient(func(int) error { return down })
	if raised, err := failing.Transient(80); !errors.Is(err, down) || raised {
		t.Fatalf("failing path: %t, %v", raised, err)
	}
}

// a raise the hardware lost (its audiod restarted, its gate shut) is no longer one: putting the level back finds
// nothing above it
func TestADroppedRaiseIsNotFound(t *testing.T) {
	logs := &bytes.Buffer{}
	s := volume.NewSetter(volume.NewStore(t.TempDir()), 50, slog.New(slog.NewTextHandler(logs, nil)))
	if _, err := s.Set(30, volume.OriginButton); err != nil {
		t.Fatal(err)
	}
	r := follow(s)
	s.FollowTransient((&ramp{}).apply)
	if _, err := s.Transient(60); err != nil {
		t.Fatal(err)
	}
	s.DropTransient()
	s.Reassert()
	if !slices.Equal(r.applied, []int{30}) || strings.Contains(logs.String(), "volume_reapplied") {
		t.Fatalf("applied %v; log: %s", r.applied, logs)
	}
}

// a quiet ramp lowers the hardware at or below the level in force and never raises it; nothing is stored or heard,
// and Reassert puts the level in force back without a volume_reapplied line (nothing stood above it)
func TestQuietLowersTheHardwareOnly(t *testing.T) {
	dir := t.TempDir()
	stored(t, dir, 30)
	logs := &bytes.Buffer{}
	s := volume.NewSetter(volume.NewStore(dir), 50, slog.New(slog.NewTextHandler(logs, nil)))
	r := follow(s)
	tr := &ramp{}
	s.FollowTransient(tr.apply)
	for _, c := range []struct {
		level int
		want  bool
	}{{5, true}, {-3, true}, {30, true}, {31, false}} {
		if set, err := s.Quiet(c.level); err != nil || set != c.want {
			t.Fatalf("quiet %d = %t, %v", c.level, set, err)
		}
	}
	if !slices.Equal(tr.applied, []int{5, 0, 30}) {
		t.Fatalf("quiet applied %v", tr.applied)
	}
	if level, _ := s.Current(); level != 30 || len(r.applied) != 0 || len(r.heard) != 0 {
		t.Fatalf("level %d, plain %v, heard %v", level, r.applied, r.heard)
	}
	s.Reassert()
	if !slices.Equal(r.applied, []int{30}) || strings.Contains(logs.String(), "volume_reapplied") {
		t.Fatalf("reassert applied %v, log %s", r.applied, logs)
	}
	unknown := volume.NewSetter(volume.NewStore(t.TempDir()), 50, nil)
	unknown.FollowTransient(tr.apply)
	if _, err := unknown.Quiet(5); !errors.Is(err, volume.ErrUnknown) {
		t.Fatalf("quiet on an unknown level: %v", err)
	}
}
