package updater_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/soloist/updater"
)

func TestBuildDatesAndExpiry(t *testing.T) {
	b := updater.Build{Version: "1.3.10.18", Epoch: 1_793_059_327}
	if b.Date() != "20261027" || b.ExpiresAt().Unix() != 1_800_835_327 {
		t.Fatalf("date %s, expires %d", b.Date(), b.ExpiresAt().Unix())
	}
}

// missing, expired, or fewer than 30 days left
func TestNeedsUpdate(t *testing.T) {
	build := func(epoch int64) *updater.Build { return &updater.Build{Epoch: epoch} }
	for _, c := range []struct {
		b    *updater.Build
		want bool
	}{
		{nil, true},
		{build(left(0)), true},
		{build(now.Add(-200 * day).Unix()), true},
		// dated after a slow clock: not expiring
		{build(now.Add(day).Unix()), false},
		{build(left(29)), true},
		{build(left(30)), false},
		{build(left(31)), false},
		{build(left(89)), false},
	} {
		if got := updater.NeedsUpdate(c.b, now); got != c.want {
			t.Fatalf("%+v: %v", c.b, got)
		}
	}
}

// a clock before 2026-01-01 UTC has not been set yet
func TestPlausibleStartsAt2026(t *testing.T) {
	for _, c := range []struct {
		at   int64
		want bool
	}{{0, false}, {1_767_225_599, false}, {1_767_225_600, true}, {1_793_100_000, true}} {
		if updater.Plausible(time.Unix(c.at, 0)) != c.want {
			t.Fatalf("%d", c.at)
		}
	}
}

// 64-bit little-endian for the board's machine: an x86 build, a 32-bit one or
// a script is a wrong download
func TestCheckELF(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "soloist.next")
	ok := arm(1)
	for _, c := range []struct {
		name string
		b    []byte
		err  error
	}{
		{"aarch64", ok, nil},
		{"x86-64", elf(updater.MachineX8664, 1), updater.FailFormat},
		{"32-bit", func() []byte { b := append([]byte(nil), ok...); b[4] = 1; return b }(), updater.FailFormat},
		{"big-endian", func() []byte { b := append([]byte(nil), ok...); b[5] = 2; return b }(), updater.FailFormat},
		{"script", []byte("#!/bin/sh\necho soloist 1 build 2 (20260101)\n"), updater.FailFormat},
		{"short", ok[:19], updater.FailFormat},
		{"empty", nil, updater.FailFormat},
	} {
		write(t, path, c.b)
		if err := updater.CheckELF(path, updater.MachineAArch64); !errors.Is(err, c.err) {
			t.Fatalf("%s: %v", c.name, err)
		}
	}
	if err := updater.CheckELF(path, updater.MachineX8664); !errors.Is(err, updater.FailFormat) {
		t.Fatalf("empty for x86: %v", err)
	}
	write(t, path, elf(updater.MachineX8664, 1))
	if err := updater.CheckELF(path, updater.MachineX8664); err != nil {
		t.Fatalf("the sim's own machine: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := updater.CheckELF(path, updater.MachineAArch64); !errors.Is(err, updater.FailIO) {
		t.Fatalf("missing: %v", err)
	}
}

// the cdn can serve yesterday's build, or one about to stop: neither is worth a switch
func TestAccept(t *testing.T) {
	installed := &updater.Build{Epoch: left(10)}
	for _, c := range []struct {
		name      string
		candidate int64
		installed *updater.Build
		err       error
	}{
		{"newer", now.Add(-day).Unix(), installed, nil},
		{"none installed", now.Add(-day).Unix(), nil, nil},
		{"the same", left(10), installed, updater.FailStale},
		{"older", left(9), installed, updater.FailStale},
		{"7 days left", left(7), nil, nil},
		{"under 7 days left", left(7) - 1, nil, updater.FailStale},
	} {
		err := updater.Accept(updater.Build{Epoch: c.candidate}, c.installed, now)
		if !errors.Is(err, c.err) {
			t.Fatalf("%s: %v", c.name, err)
		}
	}
}

func TestReasonsAreTheLoggedTokens(t *testing.T) {
	all := []updater.Failure{updater.FailDNS, updater.FailConnect, updater.FailTLS, updater.FailHTTP, updater.FailSize,
		updater.FailFormat, updater.FailVersion, updater.FailStale, updater.FailIO}
	want := []string{"dns", "connect", "tls", "http", "size", "format", "version", "stale", "io"}
	for i, f := range all {
		if string(f) != want[i] {
			t.Fatalf("%d: %s", i, f)
		}
	}
	if updater.Reason(errors.New("anything else")) != updater.FailIO {
		t.Fatal("an unknown error is not io")
	}
}
