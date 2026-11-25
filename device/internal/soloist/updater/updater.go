package updater

import (
	"context"
	"errors"
	"os"
	"time"
)

const (
	daily = 24 * time.Hour
	retry = time.Hour
)

// Prober runs a build's --version in soloist's jail (audiod's SoloistProbe).
// Its error is ErrNoAudiod when nothing ran it, else a Failure: io when the
// build could not run or answer, version when its output does not parse.
type Prober interface {
	Probe(ctx context.Context, path string) (Build, error)
}

type Result int

const (
	Current Result = iota
	// the clock is not set yet: nothing fetched, no expiry judged
	Skipped
	Installed
	Failed
)

// Outcome is one check's.
type Outcome struct {
	Result  Result
	Failure Failure // with Failed
	// Installed is the build in place after the check, nil for none, when
	// Known: the check read it
	Installed *Build
	Known     bool
}

// Updater checks the build in Dir and replaces it with the CDN's when it
// needs one (reference §6.3): soloist, soloist.next while an update runs,
// soloist.previous after a swap.
type Updater struct {
	Dir   string
	Fetch Fetcher
	Probe Prober
	// the ELF e_machine of the board's builds
	Machine uint16
	Now     func() time.Time
}

// Once checks the build, and updates it when it needs one. A failure leaves
// no soloist.next, and until the swap soloist and soloist.previous are as
// they were.
func (u *Updater) Once(ctx context.Context) Outcome {
	now := u.Now()
	if !Plausible(now) {
		return Outcome{Result: Skipped}
	}
	p := layout(u.Dir)
	// whatever an update that crashed left, which no later step would remove while no update is due
	_ = os.Remove(p.next)
	var installed *Build
	if regular(p.binary) {
		b, err := u.Probe.Probe(ctx, p.binary)
		switch {
		case err == nil:
			installed = &b
		case errors.Is(err, ErrNoAudiod):
			// with nothing to probe it, a candidate could not be verified either: no download
			return Outcome{Result: Failed, Failure: FailIO}
		}
		// a build that does not run or say what it is counts as none (v1)
	}
	if !NeedsUpdate(installed, now) {
		return Outcome{Result: Current, Installed: installed, Known: true}
	}
	b, err := u.download(ctx, p, installed, now)
	if err != nil {
		_ = os.Remove(p.next)
		return Outcome{Result: Failed, Failure: Reason(err), Installed: installed, Known: true}
	}
	return Outcome{Result: Installed, Installed: &b, Known: true}
}

func (u *Updater) download(ctx context.Context, p paths, installed *Build, now time.Time) (Build, error) {
	// a board image ships the runtime in it; a first install may find it missing
	if os.MkdirAll(p.dir, 0o700) != nil {
		return Build{}, FailIO
	}
	body, err := u.Fetch.Get(ctx)
	if err != nil {
		return Build{}, err
	}
	err = extract(body, p.next, defaultLimits)
	_ = body.Close()
	if err != nil {
		return Build{}, err
	}
	if err := CheckELF(p.next, u.Machine); err != nil {
		return Build{}, err
	}
	candidate, err := u.Probe.Probe(ctx, p.next)
	if err != nil {
		return Build{}, err
	}
	if err := Accept(candidate, installed, now); err != nil {
		return Build{}, err
	}
	if err := install(p); err != nil {
		return Build{}, err
	}
	return candidate, nil
}

// Schedule is when the next check follows o. A failure of the network or the
// disk, or a clock not set yet, may be gone within the hour; what came wrong
// (a build too old, a version that does not parse, a bad tarball) is what the
// url serves until Spotify rebuilds, and fetching it hourly would only repeat
// the download.
func Schedule(o Outcome) time.Duration {
	switch {
	case o.Result == Skipped:
		return retry
	case o.Result != Failed:
		return daily
	}
	switch o.Failure {
	case FailStale, FailVersion, FailFormat, FailSize:
		return daily
	default:
		return retry
	}
}
