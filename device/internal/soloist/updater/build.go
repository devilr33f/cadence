// Package updater keeps soloist's build fresh from Spotify's CDN (reference
// §6): Spotify rebuilds it about daily at one url and stops each build 90 days
// after it was made.
package updater

import (
	"errors"
	"fmt"
	"time"
)

const (
	// Lifetime is how long Spotify lets a build run after it was made.
	Lifetime = 90 * 24 * time.Hour
	// a build with less left than this is replaced
	refreshWithin = 30 * 24 * time.Hour
	// a candidate with less left than this is not worth a switch
	minRemaining = 7 * 24 * time.Hour
)

// 2026-01-01T00:00:00Z: a clock before it has not been set yet, so no expiry
// can be judged
var plausibleFrom = time.Unix(1_767_225_600, 0)

// Build is what soloist --version says of a build.
type Build struct {
	Version string
	// unix seconds the build was made
	Epoch int64
}

func (b Build) ExpiresAt() time.Time { return time.Unix(b.Epoch, 0).Add(Lifetime).UTC() }

// Date is the build's day as its version line has it: yyyymmdd, UTC.
func (b Build) Date() string { return time.Unix(b.Epoch, 0).UTC().Format("20060102") }

// NeedsUpdate: no build, or one that expired or has fewer than 30 days left.
func NeedsUpdate(installed *Build, now time.Time) bool {
	return installed == nil || installed.ExpiresAt().Before(now.Add(refreshWithin))
}

// Plausible is false for a clock before 2026: one that has not been set yet.
func Plausible(now time.Time) bool { return !now.Before(plausibleFrom) }

// Failure is why an update failed: the token its log carries.
type Failure string

const (
	FailDNS     Failure = "dns"
	FailConnect Failure = "connect"
	FailTLS     Failure = "tls"
	FailHTTP    Failure = "http"
	FailSize    Failure = "size"
	FailFormat  Failure = "format"
	FailVersion Failure = "version"
	FailStale   Failure = "stale"
	FailIO      Failure = "io"
)

func (f Failure) Error() string { return "soloist update failed: " + string(f) }

// ErrNoAudiod is a Prober's when nothing ran the probe: no audiod, or none
// that answered. It is io, but says nothing of the build.
var ErrNoAudiod = fmt.Errorf("no audiod: %w", FailIO)

// Reason is err's failure; any other error is io.
func Reason(err error) Failure {
	var f Failure
	if errors.As(err, &f) {
		return f
	}
	return FailIO
}
