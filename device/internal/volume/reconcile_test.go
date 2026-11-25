package volume_test

import (
	"slices"
	"testing"

	"git.shino.boo/devilreef/cadence/device/internal/volume"
)

// the system volume's level at start (android): a stored level is lowered to a quieter system, never raised to a
// louder one (rule 7), and kept
func TestReconcileNeverRaises(t *testing.T) {
	for _, c := range []struct {
		name           string
		stored, system int
		want           int
		heard          []change
	}{
		{"system quieter: the level follows it down", 30, 9, 9, []change{{9, volume.OriginSystem}}},
		{"system louder: the level stays, the system comes down", 30, 80, 30, nil},
		{"equal: nothing moves", 30, 30, 30, nil},
		{"system muted to 0", 30, 0, 0, []change{{0, volume.OriginSystem}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			stored(t, dir, c.stored)
			s := volume.NewSetter(volume.NewStore(dir), 50, nil)
			r := follow(s)
			if got := s.Reconcile(c.system); got != c.want || s.Level() != c.want {
				t.Fatalf("reconcile = %d, level %d, want %d", got, s.Level(), c.want)
			}
			if !slices.Equal(r.heard, c.heard) {
				t.Fatalf("heard %v, want %v", r.heard, c.heard)
			}
			if level, ok := volume.NewStore(dir).Load(); !ok || level != c.want {
				t.Fatalf("stored %d %v, want %d", level, ok, c.want)
			}
		})
	}
}

// with no level of its own (a fresh install), the device takes the system's: the system already plays at it, so
// nothing moves, up or down
func TestReconcileAdoptsTheSystemWhenUnknown(t *testing.T) {
	for _, system := range []int{9, 80} {
		dir := t.TempDir()
		s := volume.NewSetter(volume.NewStore(dir), 30, nil)
		r := follow(s)
		if got := s.Reconcile(system); got != system {
			t.Fatalf("reconcile(%d) = %d", system, got)
		}
		if level, known := s.Current(); level != system || !known {
			t.Fatalf("current %d %v", level, known)
		}
		if !slices.Equal(r.applied, []int{system}) {
			t.Fatalf("applied %v", r.applied)
		}
	}
}

// a store that cannot keep it: a quieter system still lowers the level in memory, a louder one leaves the default
func TestReconcileThatCannotBeKept(t *testing.T) {
	s := volume.NewSetter(volume.NewStore(blockedDir(t)), 30, nil)
	if got := s.Reconcile(80); got != 30 {
		t.Fatalf("louder = %d", got)
	}
	if got := s.Reconcile(10); got != 10 {
		t.Fatalf("quieter = %d", got)
	}
	if _, known := s.Current(); known {
		t.Fatal("known")
	}
}
