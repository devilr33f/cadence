// Package atomicfile replaces a file so a reader sees the old or the new content, never a mix.
package atomicfile

import (
	"os"
	"path/filepath"
	"runtime"
)

// Write puts data in a temp file beside path (mode 0600), fsyncs it, renames it over path and fsyncs the directory.
func Write(path string, data []byte) error {
	commit, _, err := Stage(path, data, 0o600)
	if err != nil {
		return err
	}
	return commit()
}

// Stage writes and fsyncs the temp file beside path. commit renames it over path and fsyncs the directory;
// abort removes the temp. Call exactly one of them.
func Stage(path string, data []byte, mode os.FileMode) (commit func() error, abort func(), err error) {
	f, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return nil, nil, err
	}
	tmp := f.Name()
	fail := func(err error) (func() error, func(), error) {
		_ = f.Close()
		_ = os.Remove(tmp)
		return nil, nil, err
	}
	if _, err := f.Write(data); err != nil {
		return fail(err)
	}
	if err := f.Chmod(mode); err != nil && runtime.GOOS != "windows" {
		return fail(err)
	}
	if err := f.Sync(); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return nil, nil, err
	}
	commit = func() error {
		if err := os.Rename(tmp, path); err != nil {
			_ = os.Remove(tmp)
			return err
		}
		syncDir(filepath.Dir(path))
		return nil
	}
	abort = func() { _ = os.Remove(tmp) }
	return commit, abort, nil
}

// syncDir is best effort: opening a directory for sync is unsupported on windows
func syncDir(dir string) {
	if runtime.GOOS == "windows" {
		return
	}
	d, err := os.Open(dir) //nolint:gosec // the parent of a path the caller chose
	if err != nil {
		return
	}
	_ = d.Sync()
	_ = d.Close()
}
