package updater

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
)

// paths are the builds, all in one directory so that a rename moves one
type paths struct {
	dir      string
	binary   string // the installed build
	next     string // a candidate while an update runs
	previous string // a hard link to the build before the last swap
}

func layout(dir string) paths {
	return paths{
		dir: dir, binary: filepath.Join(dir, "soloist"), next: filepath.Join(dir, "soloist.next"),
		previous: filepath.Join(dir, "soloist.previous"),
	}
}

// install swaps next in: the installed build is linked aside as previous (the
// one before goes), then next replaces it by one rename, so soloist is never
// missing. A failure before the rename leaves soloist as it was.
func install(p paths) error {
	for _, step := range installSteps(p) {
		if err := step(); err != nil {
			return err
		}
	}
	return nil
}

// installSteps are install's steps, in order: a kill between any two leaves
// a whole build at soloist
func installSteps(p paths) []func() error {
	return []func() error{
		func() error {
			if !regular(p.next) {
				return FailIO
			}
			return nil
		},
		func() error {
			if !regular(p.binary) {
				return nil
			}
			if err := os.Remove(p.previous); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return FailIO
			}
			return nil
		},
		func() error {
			// /data must take hard links: ubifs does
			if regular(p.binary) && os.Link(p.binary, p.previous) != nil {
				return FailIO
			}
			return nil
		},
		func() error {
			if os.Rename(p.next, p.binary) != nil {
				return FailIO
			}
			return nil
		},
		func() error {
			syncDir(p.dir)
			return nil
		},
	}
}

// Rollback is how a rollback ended.
type Rollback int

const (
	Restored Rollback = iota
	Deleted
	// nothing could probe the build before: both are kept for another try
	Deferred
)

// RollBack restores the build before when its probe says it still runs, and
// otherwise deletes the installed one, which never answered: /data has no room
// to keep it aside. It returns how it ended, and the build in place.
func (u *Updater) RollBack(ctx context.Context) (Rollback, *Build) {
	p := layout(u.Dir)
	if regular(p.previous) {
		b, err := u.Probe.Probe(ctx, p.previous)
		if errors.Is(err, ErrNoAudiod) {
			return Deferred, nil
		}
		if err == nil && u.Now().Before(b.ExpiresAt()) && os.Rename(p.previous, p.binary) == nil {
			syncDir(p.dir)
			return Restored, &b
		}
	}
	_ = os.Remove(p.binary)
	syncDir(p.dir)
	return Deleted, nil
}

func regular(path string) bool {
	st, err := os.Lstat(path)
	return err == nil && st.Mode().IsRegular()
}

// the rename is done and seen: a failed sync only leaves it open to a power cut
func syncDir(dir string) {
	if runtime.GOOS == "windows" {
		return
	}
	d, err := os.Open(dir) //nolint:gosec // the soloist dir
	if err != nil {
		return
	}
	_ = d.Sync()
	_ = d.Close()
}
