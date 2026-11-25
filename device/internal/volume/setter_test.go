package volume_test

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.shino.boo/devilreef/cadence/device/internal/volume"
)

type change struct {
	level  int
	origin volume.Origin
}

// recorder follows a setter: the levels applied and the changes heard.
type recorder struct {
	start   int
	applied []int
	heard   []change
}

func follow(s *volume.Setter) *recorder {
	r := &recorder{}
	s.Follow(func(level int) func(int) {
		r.start = level
		return func(l int) { r.applied = append(r.applied, l) }
	})
	s.Listen(func(level int, origin volume.Origin) { r.heard = append(r.heard, change{level, origin}) })
	return r
}

func stored(t *testing.T, dir string, level int) {
	t.Helper()
	if err := volume.NewStore(dir).Save(level); err != nil {
		t.Fatal(err)
	}
}

func TestSetterStartsAtStoredLevelElseDefault(t *testing.T) {
	logs := &bytes.Buffer{}
	log := slog.New(slog.NewTextHandler(logs, nil))
	if got := volume.NewSetter(volume.NewStore(t.TempDir()), 50, log).Level(); got != 50 {
		t.Fatalf("default = %d", got)
	}
	dir := t.TempDir()
	stored(t, dir, 20)
	if got := volume.NewSetter(volume.NewStore(dir), 50, log).Level(); got != 20 {
		t.Fatalf("stored = %d", got)
	}
	if !strings.Contains(logs.String(), "msg=volume_loaded volume=50 source=default") ||
		!strings.Contains(logs.String(), "msg=volume_loaded volume=20 source=local") {
		t.Fatalf("log: %s", logs)
	}
}

func TestSetPersistsAppliesAndNotifies(t *testing.T) {
	dir := t.TempDir()
	s := volume.NewSetter(volume.NewStore(dir), 50, nil)
	r := follow(s)
	if r.start != 50 {
		t.Fatalf("start = %d", r.start)
	}
	for _, c := range []struct {
		in, want int
		origin   volume.Origin
	}{{30, 30, volume.OriginCommand}, {150, 100, volume.OriginSender}, {-5, 0, volume.OriginCommand}} {
		got, err := s.Set(c.in, c.origin)
		if err != nil || got != c.want || s.Level() != c.want {
			t.Fatalf("set %d = %d, %v (level %d)", c.in, got, err, s.Level())
		}
		if level, ok := volume.NewStore(dir).Load(); !ok || level != c.want {
			t.Fatalf("stored %d, %v", level, ok)
		}
	}
	want := []change{{30, volume.OriginCommand}, {100, volume.OriginSender}, {0, volume.OriginCommand}}
	if len(r.heard) != 3 || r.heard[0] != want[0] || r.heard[1] != want[1] || r.heard[2] != want[2] {
		t.Fatalf("heard %v", r.heard)
	}
	if len(r.applied) != 3 || r.applied[0] != 30 || r.applied[1] != 100 || r.applied[2] != 0 {
		t.Fatalf("applied %v", r.applied)
	}
	// the stored level again changes nothing
	if got, err := s.Set(0, volume.OriginCommand); err != nil || got != 0 || len(r.heard) != 3 || len(r.applied) != 3 {
		t.Fatalf("equal set: %d, %v, heard %v, applied %v", got, err, r.heard, r.applied)
	}
}

// blockedDir is a data dir with a file where the state dir goes: nothing can be saved
func blockedDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "state"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestPersistFailureRefusesTheChange(t *testing.T) {
	logs := &bytes.Buffer{}
	s := volume.NewSetter(volume.NewStore(blockedDir(t)), 50, slog.New(slog.NewTextHandler(logs, nil)))
	r := follow(s)
	got, err := s.Set(30, volume.OriginCommand)
	if !errors.Is(err, volume.ErrStore) || got != 50 || s.Level() != 50 {
		t.Fatalf("set = %d, %v (level %d)", got, err, s.Level())
	}
	if len(r.applied) != 0 || len(r.heard) != 0 {
		t.Fatalf("applied %v, heard %v", r.applied, r.heard)
	}
	if !strings.Contains(logs.String(), "msg=volume_store_failed origin=command") {
		t.Fatalf("log: %s", logs)
	}
}

func TestSeedFillsOnlyAnUnknownLevel(t *testing.T) {
	dir := t.TempDir()
	s := volume.NewSetter(volume.NewStore(dir), 50, nil)
	r := follow(s)
	s.Seed(30)
	if s.Level() != 30 || len(r.heard) != 1 || r.heard[0] != (change{30, volume.OriginRestore}) {
		t.Fatalf("level %d, heard %v", s.Level(), r.heard)
	}
	if level, ok := volume.NewStore(dir).Load(); !ok || level != 30 {
		t.Fatalf("stored %d, %v", level, ok)
	}
	// seeded once: a later legacy level is ignored
	s.Seed(70)
	if s.Level() != 30 || len(r.heard) != 1 {
		t.Fatalf("second seed: level %d, heard %v", s.Level(), r.heard)
	}

	known := t.TempDir()
	stored(t, known, 20)
	k := volume.NewSetter(volume.NewStore(known), 50, nil)
	kr := follow(k)
	k.Seed(70)
	if k.Level() != 20 || len(kr.heard) != 0 || len(kr.applied) != 0 {
		t.Fatalf("seed over a local level: level %d, heard %v", k.Level(), kr.heard)
	}

	// a level set since boot is known too
	set := volume.NewSetter(volume.NewStore(t.TempDir()), 50, nil)
	if _, err := set.Set(40, volume.OriginCommand); err != nil {
		t.Fatal(err)
	}
	set.Seed(70)
	if set.Level() != 40 {
		t.Fatalf("seed over a set level: %d", set.Level())
	}
}

// a seed the store cannot keep may only lower the default, in memory, and the
// next seed tries the store again
func TestSeedThatCannotBeKept(t *testing.T) {
	dir := blockedDir(t)
	s := volume.NewSetter(volume.NewStore(dir), 50, nil)
	r := follow(s)
	s.Seed(30)
	if s.Level() != 30 || len(r.heard) != 1 || r.heard[0] != (change{30, volume.OriginRestore}) || len(r.applied) != 1 {
		t.Fatalf("level %d, heard %v, applied %v", s.Level(), r.heard, r.applied)
	}
	s.Seed(70)
	if s.Level() != 30 || len(r.heard) != 1 {
		t.Fatalf("a higher unkept seed: level %d, heard %v", s.Level(), r.heard)
	}
	if err := os.Remove(filepath.Join(dir, "state")); err != nil {
		t.Fatal(err)
	}
	s.Seed(30)
	if level, ok := volume.NewStore(dir).Load(); !ok || level != 30 {
		t.Fatalf("retried seed stored %d, %v", level, ok)
	}
	s.Seed(10)
	if s.Level() != 30 {
		t.Fatalf("a kept seed was replaced: %d", s.Level())
	}
}

func TestMemoryOnlySetter(t *testing.T) {
	s := volume.NewSetter(nil, 50, nil)
	if got, err := s.Set(30, volume.OriginCommand); err != nil || got != 30 {
		t.Fatalf("set = %d, %v", got, err)
	}
	s.Seed(70)
	if s.Level() != 30 {
		t.Fatalf("level = %d", s.Level())
	}
}

// a sender moves only a level the device knows as its own (rule 7)
func TestSetKnownRefusesTheDefaultLevel(t *testing.T) {
	dir := t.TempDir()
	s := volume.NewSetter(volume.NewStore(dir), 50, nil)
	r := follow(s)
	if level, known := s.Current(); level != 50 || known {
		t.Fatalf("current = %d, %v", level, known)
	}
	if got, err := s.SetKnown(70, volume.OriginSender); !errors.Is(err, volume.ErrUnknown) || got != 50 {
		t.Fatalf("set known while unknown = %d, %v", got, err)
	}
	if _, ok := volume.NewStore(dir).Load(); ok || len(r.heard) != 0 {
		t.Fatalf("an unknown level moved: stored %v, heard %v", ok, r.heard)
	}
	if _, err := s.Set(30, volume.OriginCommand); err != nil {
		t.Fatal(err)
	}
	if got, err := s.SetKnown(70, volume.OriginSender); err != nil || got != 70 {
		t.Fatalf("set known = %d, %v", got, err)
	}
	if level, known := s.Current(); level != 70 || !known {
		t.Fatalf("current = %d, %v", level, known)
	}
}

// a press steps from the level in force, never past the ends, and is kept like any change
func TestStepMovesFromTheLevelInForce(t *testing.T) {
	dir := t.TempDir()
	stored(t, dir, 90)
	s := volume.NewSetter(volume.NewStore(dir), 50, nil)
	r := follow(s)
	for _, c := range []struct{ delta, want int }{{5, 95}, {5, 100}, {5, 100}, {-5, 95}} {
		got, err := s.Step(c.delta, volume.OriginButton)
		if err != nil || got != c.want {
			t.Fatalf("step %+d = %d, %v", c.delta, got, err)
		}
	}
	if level, ok := volume.NewStore(dir).Load(); !ok || level != 95 {
		t.Fatalf("stored %d, %v", level, ok)
	}
	want := []change{{95, volume.OriginButton}, {100, volume.OriginButton}, {95, volume.OriginButton}}
	if fmt.Sprint(r.heard) != fmt.Sprint(want) {
		t.Fatalf("heard %v, want %v", r.heard, want)
	}

	low := volume.NewSetter(volume.NewStore(t.TempDir()), 3, nil)
	if got, err := low.Step(-5, volume.OriginButton); err != nil || got != 0 {
		t.Fatalf("step below 0 = %d, %v", got, err)
	}
}

// Cap only lowers: a level above max comes down to it, one at or below stays, kept or default alike
func TestCapOnlyLowers(t *testing.T) {
	dir := t.TempDir()
	s := volume.NewSetter(volume.NewStore(dir), 30, nil)
	r := follow(s)
	if got, err := s.Cap(40, volume.OriginSpeaker); err != nil || got != 30 {
		t.Fatalf("cap default = %d, %v", got, err)
	}
	if _, known := s.Current(); known {
		t.Fatal("a cap that moved nothing made the default the device's own")
	}
	if _, err := s.Set(70, volume.OriginCommand); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Cap(40, volume.OriginSpeaker); err != nil || got != 40 {
		t.Fatalf("cap 70 = %d, %v", got, err)
	}
	if level, ok := volume.NewStore(dir).Load(); !ok || level != 40 {
		t.Fatalf("stored %d %v", level, ok)
	}
	if fmt.Sprint(r.applied) != "[70 40]" || r.heard[1] != (change{40, volume.OriginSpeaker}) {
		t.Fatalf("applied %v heard %v", r.applied, r.heard)
	}
}
