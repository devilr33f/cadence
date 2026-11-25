package updater_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"git.shino.boo/devilreef/cadence/device/internal/soloist/updater"
)

// review focus 4: a kill between any two steps of the swap leaves a whole
// build at soloist, the old one or the new
func TestSwapKeepsABinaryAtEveryStep(t *testing.T) {
	old, next := arm(left(10)), arm(now.Unix())
	steps := len(updater.InstallSteps(t.TempDir()))
	for k := 0; k <= steps; k++ {
		dir := t.TempDir()
		write(t, filepath.Join(dir, "soloist"), old)
		write(t, filepath.Join(dir, "soloist.previous"), arm(left(1)))
		write(t, filepath.Join(dir, "soloist.next"), next)
		for _, step := range updater.InstallSteps(dir)[:k] {
			if err := step(); err != nil {
				t.Fatalf("step %d: %v", k, err)
			}
		}
		got := read(t, filepath.Join(dir, "soloist"))
		if !bytes.Equal(got, old) && !bytes.Equal(got, next) {
			t.Fatalf("after %d steps soloist is neither build", k)
		}
	}
	dir := t.TempDir()
	write(t, filepath.Join(dir, "soloist"), old)
	write(t, filepath.Join(dir, "soloist.next"), next)
	for _, step := range updater.InstallSteps(dir) {
		if err := step(); err != nil {
			t.Fatal(err)
		}
	}
	if !bytes.Equal(read(t, filepath.Join(dir, "soloist")), next) || !bytes.Equal(read(t, filepath.Join(dir, "soloist.previous")), old) {
		t.Fatal("the swap did not put the new build in place and the old one aside")
	}
	if got := files(t, dir); !slices.Equal(got, []string{"soloist", "soloist.previous"}) {
		t.Fatalf("files = %v", got)
	}
}

// a rollback restores the build before when it still runs, else deletes the
// silent one: there is no room on /data to keep it aside. A probe that could
// not run, with audiod gone, keeps both for another try.
func TestRollBack(t *testing.T) {
	silent := arm(now.Add(-day).Unix())
	for _, c := range []struct {
		name     string
		previous []byte // nil: none
		fail     error
		want     updater.Rollback
	}{
		{"an unexpired previous", arm(left(10)), nil, updater.Restored},
		{"an expired previous", arm(left(0)), nil, updater.Deleted},
		{"no previous", nil, nil, updater.Deleted},
		{"a previous with no version", []byte("\x7fELF garbage"), nil, updater.Deleted},
		{"a previous audiod could not run", arm(left(10)), updater.FailIO, updater.Deleted},
		{"audiod gone", arm(left(10)), updater.ErrNoAudiod, updater.Deferred},
	} {
		dir := t.TempDir()
		write(t, filepath.Join(dir, "soloist"), silent)
		if c.previous != nil {
			write(t, filepath.Join(dir, "soloist.previous"), c.previous)
		}
		probe := &fakeProber{fail: c.fail}
		got, build := newUpdater(dir, &fakeFetch{}, probe).RollBack(context.Background())
		if got != c.want {
			t.Fatalf("%s: rollback = %v", c.name, got)
		}
		if c.previous != nil && !slices.Equal(probe.probed(), []string{"soloist.previous"}) {
			t.Fatalf("%s: probed %v", c.name, probe.probed())
		}
		switch got {
		case updater.Restored:
			if !bytes.Equal(read(t, filepath.Join(dir, "soloist")), c.previous) || build == nil || build.Epoch != left(10) {
				t.Fatalf("%s: not restored, build %v", c.name, build)
			}
			if got := files(t, dir); !slices.Equal(got, []string{"soloist"}) {
				t.Fatalf("%s: files %v", c.name, got)
			}
		case updater.Deferred:
			if !bytes.Equal(read(t, filepath.Join(dir, "soloist")), silent) || !bytes.Equal(read(t, filepath.Join(dir, "soloist.previous")), c.previous) || build != nil {
				t.Fatalf("%s: files changed, build %v", c.name, build)
			}
		default:
			if _, err := os.Stat(filepath.Join(dir, "soloist")); !os.IsNotExist(err) || build != nil {
				t.Fatalf("%s: the silent build stayed (%v), build %v", c.name, err, build)
			}
		}
	}
}
