package updater_test

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/soloist/updater"
)

func once(t *testing.T, u *updater.Updater) updater.Outcome {
	t.Helper()
	return u.Once(context.Background())
}

func TestCurrentBuildDownloadsNothing(t *testing.T) {
	dir := t.TempDir()
	install(t, dir, left(60))
	fetch := &fakeFetch{body: release(t, arm(now.Add(-day).Unix()))}
	o := once(t, newUpdater(dir, fetch, &fakeProber{}))
	if o.Result != updater.Current || o.Installed == nil || o.Installed.Epoch != left(60) || !o.Known {
		t.Fatalf("outcome = %+v", o)
	}
	if fetch.calls.Load() != 0 || !bytes.Equal(read(t, filepath.Join(dir, "soloist")), arm(left(60))) {
		t.Fatal("a current build was replaced")
	}
}

// before the clock is set nothing is fetched and no expiry judged
func TestImplausibleClockIsSkipped(t *testing.T) {
	dir := t.TempDir()
	fetch, probe := &fakeFetch{body: release(t, arm(now.Unix()))}, &fakeProber{}
	u := newUpdater(dir, fetch, probe)
	u.Now = func() time.Time { return time.Unix(1_000, 0) }
	if o := once(t, u); o.Result != updater.Skipped || o.Known {
		t.Fatalf("no build: %+v", o)
	}
	install(t, dir, left(0))
	if o := once(t, u); o.Result != updater.Skipped {
		t.Fatalf("an old build: %+v", o)
	}
	if fetch.calls.Load() != 0 || len(probe.probed()) != 0 {
		t.Fatal("an unset clock fetched or probed")
	}
}

func TestMissingBuildIsFetchedVerifiedAndInstalled(t *testing.T) {
	dir := t.TempDir()
	probe := &fakeProber{}
	o := once(t, newUpdater(dir, &fakeFetch{body: release(t, arm(now.Add(-day).Unix()))}, probe))
	if o.Result != updater.Installed || o.Installed == nil || o.Installed.Epoch != now.Add(-day).Unix() {
		t.Fatalf("outcome = %+v", o)
	}
	if !bytes.Equal(read(t, filepath.Join(dir, "soloist")), arm(now.Add(-day).Unix())) {
		t.Fatal("not installed")
	}
	if st, _ := os.Stat(filepath.Join(dir, "soloist")); runtime.GOOS != "windows" && st.Mode().Perm() != 0o755 {
		t.Fatalf("mode %v", st.Mode())
	}
	if got := files(t, dir); !slices.Equal(got, []string{"soloist"}) {
		t.Fatalf("files = %v", got)
	}
	// the candidate is probed where it was written, in soloist's jail
	if !slices.Equal(probe.probed(), []string{"soloist.next"}) {
		t.Fatalf("probed %v", probe.probed())
	}
}

func TestExpiringBuildIsReplacedAndKeptAsPrevious(t *testing.T) {
	dir := t.TempDir()
	install(t, dir, left(10))
	o := once(t, newUpdater(dir, &fakeFetch{body: release(t, arm(now.Add(-day).Unix()))}, &fakeProber{}))
	if o.Result != updater.Installed {
		t.Fatalf("outcome = %+v", o)
	}
	if !bytes.Equal(read(t, filepath.Join(dir, "soloist.previous")), arm(left(10))) {
		t.Fatal("the build before was not kept")
	}
	if got := files(t, dir); !slices.Equal(got, []string{"soloist", "soloist.previous"}) {
		t.Fatalf("files = %v", got)
	}
}

// a failure has its reason, leaves no next and keeps the installed build
func TestFailuresKeepTheInstalledBuild(t *testing.T) {
	whole := gz(t, tarball(t, file("soloist", bytes.Repeat(arm(now.Unix()), 30))))
	for _, c := range []struct {
		name  string
		fetch *fakeFetch
		want  updater.Failure
	}{
		{"dns", &fakeFetch{err: updater.FailDNS}, updater.FailDNS},
		{"size", &fakeFetch{err: updater.FailSize}, updater.FailSize},
		{"not an archive", &fakeFetch{body: []byte("<html>moved</html>")}, updater.FailFormat},
		{"no soloist", &fakeFetch{body: gz(t, tarball(t, file("CHANGELOG.md", []byte("x"))))}, updater.FailFormat},
		{"cut", &fakeFetch{body: whole[:len(whole)/2]}, updater.FailFormat},
		{"reset mid-body", &fakeFetch{body: whole[:len(whole)/2], cut: true}, updater.FailHTTP},
		{"not aarch64", &fakeFetch{body: release(t, elf(updater.MachineX8664, now.Unix()))}, updater.FailFormat},
		{"no version line", &fakeFetch{body: release(t, arm(now.Unix())[:64])}, updater.FailVersion},
		{"the same build", &fakeFetch{body: release(t, arm(left(10)))}, updater.FailStale},
		{"an older build", &fakeFetch{body: release(t, arm(left(9)))}, updater.FailStale},
		// newer than the installed build, with under 7 days left
		{"about to expire", &fakeFetch{body: release(t, arm(left(6)))}, updater.FailStale},
	} {
		dir := t.TempDir()
		install(t, dir, left(5))
		if c.name == "the same build" || c.name == "an older build" {
			install(t, dir, left(10))
		}
		installed := read(t, filepath.Join(dir, "soloist"))
		o := once(t, newUpdater(dir, c.fetch, &fakeProber{}))
		if o.Result != updater.Failed || o.Failure != c.want || !o.Known || o.Installed == nil {
			t.Fatalf("%s: outcome = %+v", c.name, o)
		}
		if !bytes.Equal(read(t, filepath.Join(dir, "soloist")), installed) {
			t.Fatalf("%s: the installed build changed", c.name)
		}
		if got := files(t, dir); !slices.Equal(got, []string{"soloist"}) {
			t.Fatalf("%s: files = %v", c.name, got)
		}
	}
}

// fail closed: with audiod down nothing can be verified, so nothing is fetched
func TestUnprobedInstalledBuildFetchesNothing(t *testing.T) {
	dir := t.TempDir()
	install(t, dir, left(5))
	fetch := &fakeFetch{body: release(t, arm(now.Unix()))}
	o := once(t, newUpdater(dir, fetch, &fakeProber{fail: updater.ErrNoAudiod}))
	if o.Result != updater.Failed || o.Failure != updater.FailIO || o.Known || fetch.calls.Load() != 0 {
		t.Fatalf("outcome = %+v, fetched %d", o, fetch.calls.Load())
	}
}

// a build audiod ran and could not get a version line from (an exec failure,
// a timeout: a corrupt file) counts as none, as v1: it is replaced
func TestInstalledBuildAudiodCannotRunIsReplaced(t *testing.T) {
	dir := t.TempDir()
	install(t, dir, left(60))
	probe := &fakeProber{failFor: map[string]error{"soloist": updater.FailIO}}
	o := once(t, newUpdater(dir, &fakeFetch{body: release(t, arm(now.Unix()))}, probe))
	if o.Result != updater.Installed || o.Installed == nil || o.Installed.Epoch != now.Unix() {
		t.Fatalf("outcome = %+v", o)
	}
	if !bytes.Equal(read(t, filepath.Join(dir, "soloist")), arm(now.Unix())) {
		t.Fatal("not replaced")
	}
}

// a build whose version does not parse counts as none: it is replaced
func TestUnparsableInstalledBuildIsReplaced(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "soloist"), []byte("\x7fELF not a build"))
	o := once(t, newUpdater(dir, &fakeFetch{body: release(t, arm(now.Unix()))}, &fakeProber{}))
	if o.Result != updater.Installed {
		t.Fatalf("outcome = %+v", o)
	}
}

// before the first download: a --version run that could not happen retries in
// an hour, a line that does not parse in a day
func TestFirstInstallProbeFailures(t *testing.T) {
	for _, c := range []struct {
		fail updater.Failure
		next time.Duration
	}{{updater.FailIO, time.Hour}, {updater.FailVersion, 24 * time.Hour}} {
		dir := t.TempDir()
		o := once(t, newUpdater(dir, &fakeFetch{body: release(t, arm(now.Unix()))}, &fakeProber{fail: c.fail}))
		if o.Result != updater.Failed || o.Failure != c.fail || updater.Schedule(o) != c.next {
			t.Fatalf("%s: outcome = %+v, next %v", c.fail, o, updater.Schedule(o))
		}
		if got := files(t, dir); len(got) != 0 {
			t.Fatalf("%s: files = %v", c.fail, got)
		}
	}
}

// whatever a crashed update left at next goes, due or not
func TestLeftoverNextGoes(t *testing.T) {
	dir := t.TempDir()
	install(t, dir, left(60))
	write(t, filepath.Join(dir, "soloist.next"), []byte("half a build"))
	if o := once(t, newUpdater(dir, &fakeFetch{}, &fakeProber{})); o.Result != updater.Current {
		t.Fatalf("outcome = %+v", o)
	}
	if got := files(t, dir); !slices.Equal(got, []string{"soloist"}) {
		t.Fatalf("files = %v", got)
	}
	dir = t.TempDir()
	write(t, filepath.Join(dir, "soloist.next"), []byte("half a build"))
	if o := once(t, newUpdater(dir, &fakeFetch{body: release(t, arm(now.Unix()))}, &fakeProber{})); o.Result != updater.Installed {
		t.Fatalf("outcome = %+v", o)
	}
}

// what came wrong is what the url serves until spotify rebuilds: a day; what
// failed on the way may be gone within the hour
func TestScheduleTable(t *testing.T) {
	daily, hourly := 24*time.Hour, time.Hour
	for _, c := range []struct {
		o    updater.Outcome
		want time.Duration
	}{
		{updater.Outcome{Result: updater.Current}, daily},
		{updater.Outcome{Result: updater.Installed}, daily},
		{updater.Outcome{Result: updater.Skipped}, hourly},
		{updater.Outcome{Result: updater.Failed, Failure: updater.FailStale}, daily},
		{updater.Outcome{Result: updater.Failed, Failure: updater.FailVersion}, daily},
		{updater.Outcome{Result: updater.Failed, Failure: updater.FailFormat}, daily},
		{updater.Outcome{Result: updater.Failed, Failure: updater.FailSize}, daily},
		{updater.Outcome{Result: updater.Failed, Failure: updater.FailDNS}, hourly},
		{updater.Outcome{Result: updater.Failed, Failure: updater.FailConnect}, hourly},
		{updater.Outcome{Result: updater.Failed, Failure: updater.FailTLS}, hourly},
		{updater.Outcome{Result: updater.Failed, Failure: updater.FailHTTP}, hourly},
		{updater.Outcome{Result: updater.Failed, Failure: updater.FailIO}, hourly},
	} {
		if got := updater.Schedule(c.o); got != c.want {
			t.Fatalf("%+v: %v", c.o, got)
		}
	}
}

// the whole path over https: the fake cdn's tarball becomes the installed build
func TestOnceOverHTTPS(t *testing.T) {
	body := release(t, arm(now.Add(-day).Unix()))
	c := newCDN(t, raw(func(w *bufio.Writer) {
		head("HTTP/1.1 200 OK", "Content-Length: "+strconv.Itoa(len(body)))(w)
		_, _ = w.Write(body)
	}))
	dir := t.TempDir()
	if o := once(t, newUpdater(dir, c.fetcher(), &fakeProber{})); o.Result != updater.Installed {
		t.Fatalf("outcome = %+v", o)
	}
	// a body the server cuts is the network's: retried within the hour
	cut := newCDN(t, raw(func(w *bufio.Writer) {
		head("HTTP/1.1 200 OK", "Content-Length: "+strconv.Itoa(len(body)))(w)
		_, _ = w.Write(body[:len(body)/2])
	}))
	o := once(t, newUpdater(t.TempDir(), cut.fetcher(), &fakeProber{}))
	if o.Result != updater.Failed || o.Failure != updater.FailHTTP {
		t.Fatalf("cut: %+v", o)
	}
}

// a first install makes the soloist dir when the board image did not
func TestFirstInstallMakesTheDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "soloist")
	if o := once(t, newUpdater(dir, &fakeFetch{body: release(t, arm(now.Unix()))}, &fakeProber{})); o.Result != updater.Installed {
		t.Fatalf("outcome = %+v", o)
	}
	if st, err := os.Stat(dir); err != nil || runtime.GOOS != "windows" && st.Mode().Perm() != 0o700 {
		t.Fatalf("dir %v, %v", st, err)
	}
}
