package install

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"
)

// lines splits a file into its lines, each with its own ending
func lines(b []byte) [][]byte {
	var out [][]byte
	for len(b) > 0 {
		i := bytes.IndexByte(b, '\n')
		if i < 0 {
			return append(out, b)
		}
		out = append(out, b[:i+1])
		b = b[i+1:]
	}
	return out
}

// isEntry is whether a line is exactly entry, as busybox init reads it
func isEntry(line []byte, entry string) bool {
	return string(bytes.TrimSuffix(line, []byte("\n"))) == entry
}

func hasEntry(tab []byte, entry string) bool { return countEntry(tab, entry) > 0 }

func countEntry(tab []byte, entry string) int {
	n := 0
	for _, l := range lines(tab) {
		if isEntry(l, entry) {
			n++
		}
	}
	return n
}

// withoutEntry is tab without the lines that are exactly entry; every other byte stays as it was
func withoutEntry(tab []byte, entry string) []byte {
	var out []byte
	for _, l := range lines(tab) {
		if !isEntry(l, entry) {
			out = append(out, l...)
		}
	}
	return out
}

// withEntry is tab with entry exactly once, right after the first anchor line, as v1's install.sh placed its own
func withEntry(tab []byte, entry, anchor string) ([]byte, error) {
	var out []byte
	added := false
	for _, l := range lines(withoutEntry(tab, entry)) {
		out = append(out, l...)
		if !added && isEntry(l, anchor) {
			if !bytes.HasSuffix(l, []byte("\n")) {
				out = append(out, '\n')
			}
			out = append(out, entry+"\n"...)
			added = true
		}
	}
	if !added {
		return nil, fmt.Errorf("%w: the inittab has no %s entry", ErrRefused, anchor)
	}
	return out, nil
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// readInittab reads the inittab and makes sure the bytes are exactly the device's, by its sha256
func (in *Installer) readInittab(ctx context.Context) ([]byte, string, error) {
	tab, ok, err := in.dev.Read(ctx, in.p.Inittab)
	if err != nil {
		return nil, "", err
	}
	if !ok {
		return nil, "", fmt.Errorf("%w: %s is missing", ErrRefused, in.p.Inittab)
	}
	s, ok, err := in.dev.Sum(ctx, in.p.Inittab)
	if err != nil {
		return nil, "", err
	}
	if !ok || s != sum(tab) {
		return nil, "", fmt.Errorf("%s did not read back exactly", in.p.Inittab)
	}
	return tab, s, nil
}

// setInittab makes the inittab next, if it is not already: recovery access checked again (it may have gone since
// preflight), a backup of the current one on the device, the new one staged on /data, checked to have lost no line
// but entry and to keep every recovery entry, then swapped in; init reloads its table either way
func (in *Installer) setInittab(ctx context.Context, tag, entry string, change func([]byte) ([]byte, error)) error {
	cur, curSum, err := in.readInittab(ctx)
	if err != nil {
		return err
	}
	next, err := change(cur)
	if err != nil {
		return err
	}
	if bytes.Equal(next, cur) {
		return in.dev.ReloadInit(ctx)
	}
	if err := in.recovery(ctx); err != nil {
		return fmt.Errorf("before rewriting %s: %v", in.p.Inittab, err) //nolint:errorlint // a failure, not a refusal
	}
	if !bytes.Equal(withoutEntry(next, entry), withoutEntry(cur, entry)) {
		return fmt.Errorf("the new %s would lose lines", in.p.Inittab)
	}
	for _, r := range in.p.Recovery {
		if countEntry(next, r) != countEntry(cur, r) {
			return fmt.Errorf("the new %s would lose %s", in.p.Inittab, r)
		}
	}
	if err := in.dev.Mkdir(ctx, in.l.backup, 0o700); err != nil {
		return err
	}
	backup := path.Join(in.l.backup, "inittab."+in.o.now().UTC().Format("20060102T150405Z")+"."+tag)
	if err := in.dev.Copy(ctx, in.p.Inittab, backup, 0o600); err != nil {
		return err
	}
	if s, ok, err := in.dev.Sum(ctx, backup); !in.o.DryRun && (err != nil || !ok || s != curSum) {
		return errors.Join(errors.New("the inittab backup did not read back"), err)
	}
	in.say("%s backed up to %s", in.p.Inittab, backup)
	staged := path.Join(in.l.work, "inittab.next")
	if err := in.dev.Write(ctx, staged, next, 0o600); err != nil {
		return err
	}
	if err := in.dev.SwapInittab(ctx, staged, curSum, sum(next)); err != nil || in.o.DryRun {
		return err
	}
	// init reloaded: whatever it did, nothing goes on without recovery access (rule 2)
	if err := in.recovery(ctx); err != nil {
		return fmt.Errorf("recovery access lost after rewriting %s: %v", in.p.Inittab, err) //nolint:errorlint // a failure, not a refusal
	}
	return nil
}

// identity names one kind of cadence process
type identity struct {
	name  string
	match func(Proc) bool
}

// binary is a program run from its own path; a binary replaced while it runs reads as deleted
func binary(name, bin string) identity {
	return identity{name, func(p Proc) bool {
		return (p.Exe == bin || p.Exe == bin+" (deleted)") && len(p.Args) > 0 && p.Args[0] == bin
	}}
}

// script is a shell script init runs through its #!/bin/sh line: sh as the program, the script as its argument
func script(name, file string) identity {
	return identity{name, func(p Proc) bool {
		return p.Exe != "" && len(p.Args) == 2 && (p.Args[0] == "sh" || strings.HasSuffix(p.Args[0], "/sh")) && p.Args[1] == file
	}}
}

// these are the waits for processes to stop: TERM, then KILL
const (
	termPolls = 6
	killPolls = 3
	pollEvery = time.Second
)

// stop stops every process of ids, the ones before others: TERM, then KILL. The targets are the pid files' and a
// scan of /proc; each is signalled only once its exe and command line matched, again just before the signal. A pid
// file naming another process is reported and left alone
func (in *Installer) stop(ctx context.Context, ids []identity, pidfiles []string) error {
	procs, err := in.dev.Processes(ctx)
	if err != nil {
		return err
	}
	for _, f := range pidfiles {
		raw, ok, err := in.dev.Read(ctx, f)
		if err != nil || !ok {
			continue
		}
		pid, perr := strconv.Atoi(strings.TrimSpace(string(raw)))
		if perr != nil {
			continue
		}
		if i := slices.IndexFunc(procs, func(p Proc) bool { return p.Pid == pid }); i < 0 || matchOf(ids, procs[i]) == "" {
			in.say("%s names pid %d, which is not a cadence process: not signalled", f, pid)
		}
	}
	targets := matching(ids, procs)
	if len(targets) == 0 {
		return nil
	}
	for _, p := range targets {
		if sent, err := in.dev.Signal(ctx, p, "TERM"); err != nil {
			return err
		} else if sent && !in.o.DryRun {
			in.say("sent TERM to %s (pid %d)", matchOf(ids, p), p.Pid)
		}
	}
	if in.o.DryRun {
		return nil
	}
	for _, polls := range []int{termPolls, killPolls} {
		left, err := in.waitGone(ctx, ids, polls)
		if err != nil || len(left) == 0 {
			return err
		}
		if polls == killPolls {
			return fmt.Errorf("still running after KILL: %s (pid %d)", matchOf(ids, left[0]), left[0].Pid)
		}
		for _, p := range left {
			if sent, err := in.dev.Signal(ctx, p, "KILL"); err != nil {
				return err
			} else if sent {
				in.say("sent KILL to %s (pid %d)", matchOf(ids, p), p.Pid)
			}
		}
	}
	return nil
}

// waitGone polls until no process of ids runs, and returns those still running after polls tries
func (in *Installer) waitGone(ctx context.Context, ids []identity, polls int) ([]Proc, error) {
	var left []Proc
	for range polls {
		in.o.sleep(pollEvery)
		procs, err := in.dev.Processes(ctx)
		if err != nil {
			return nil, err
		}
		if left = matching(ids, procs); len(left) == 0 {
			return nil, nil
		}
	}
	return left, nil
}

// matching are the processes of ids, in the order of ids
func matching(ids []identity, procs []Proc) []Proc {
	var out []Proc
	for _, id := range ids {
		for _, p := range procs {
			if id.match(p) {
				out = append(out, p)
			}
		}
	}
	return out
}

func matchOf(ids []identity, p Proc) string {
	for _, id := range ids {
		if id.match(p) {
			return id.name
		}
	}
	return ""
}

// moveEntries moves every entry of from but the kept ones into to, which it creates; a name already in to refuses
func (in *Installer) moveEntries(ctx context.Context, from, to string, skip map[string]bool) (int, error) {
	names, err := in.dev.List(ctx, from)
	if err != nil {
		return 0, err
	}
	if err := in.dev.Mkdir(ctx, to, 0o700); err != nil {
		return 0, err
	}
	moved := 0
	for _, n := range names {
		if skip[n] {
			continue
		}
		if err := in.dev.Rename(ctx, path.Join(from, n), path.Join(to, n)); err != nil {
			return moved, err
		}
		moved++
	}
	return moved, nil
}
