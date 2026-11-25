package sources_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/telemetry/logs"
	"git.shino.boo/devilreef/cadence/device/internal/telemetry/sources"
)

func appendFile(t *testing.T, path, text string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600) //nolint:gosec // test file
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteString(text); err != nil {
		t.Fatal(err)
	}
}

type tailRun struct {
	rec  *recorder
	tail *sources.Tail
	stop func()
}

func startTail(t *testing.T, path string) *tailRun {
	t.Helper()
	rec := &recorder{}
	tail := sources.NewTail("board-file", path, rec)
	sources.SetTailTimings(tail, 5*time.Millisecond, 20*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Go(func() { tail.Run(ctx) })
	var once sync.Once
	stop := func() { once.Do(func() { cancel(); wg.Wait() }) }
	t.Cleanup(stop)
	return &tailRun{rec: rec, tail: tail, stop: stop}
}

// what the file held before the start is never read again at a restart
func TestTailStartsAtEnd(t *testing.T) {
	path := filepath.Join(t.TempDir(), "board.log")
	appendFile(t, path, "old 1\nold 2\n")
	r := startTail(t, path)
	waitFor(t, "the tail to open", func() bool { return sources.TailOpened(r.tail) })
	appendFile(t, path, "new 1\nnew 2\n")
	waitBodies(t, r.rec, "new 1", "new 2")
	if got := r.rec.all()[0]; got.Source != "board-file" || got.Severity != logs.Info || got.Truncated {
		t.Fatalf("record %+v", got)
	}
}

// a line the writer has not ended yet waits for its newline
func TestTailPartialLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "board.log")
	appendFile(t, path, "")
	r := startTail(t, path)
	waitFor(t, "the tail to open", func() bool { return sources.TailOpened(r.tail) })
	appendFile(t, path, "par")
	time.Sleep(50 * time.Millisecond)
	appendFile(t, path, "tial\r\n\nnext\n")
	waitBodies(t, r.rec, "partial", "next")
}

// a line past the cap is cut there and marked; the rest of it is skipped
func TestTailLongLineCut(t *testing.T) {
	path := filepath.Join(t.TempDir(), "board.log")
	appendFile(t, path, "")
	r := startTail(t, path)
	waitFor(t, "the tail to open", func() bool { return sources.TailOpened(r.tail) })
	appendFile(t, path, strings.Repeat("a", logs.MaxLine+500)+"\nafter\n")
	waitFor(t, "two records", func() bool { return len(r.rec.all()) == 2 })
	got := r.rec.all()
	if got[0].Body != strings.Repeat("a", logs.MaxLine) || !got[0].Truncated || got[1].Body != "after" || got[1].Truncated {
		t.Fatalf("records: %d bytes truncated %v, then %q", len(got[0].Body), got[0].Truncated, got[1].Body)
	}
}

// a rotation moves the file away and starts a new one, read from its start;
// what the old one got before it went is read first
func TestTailRotationByRename(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "board.log")
	appendFile(t, path, "")
	r := startTail(t, path)
	waitFor(t, "the tail to open", func() bool { return sources.TailOpened(r.tail) })
	appendFile(t, path, "one\n")
	waitBodies(t, r.rec, "one")
	appendFile(t, path, "two\n")
	if err := os.Rename(path, path+".1"); err != nil {
		// windows refuses to move a file another handle holds open
		t.Skipf("the host cannot rename an open file: %v", err)
	}
	appendFile(t, path, "fresh\n")
	waitBodies(t, r.rec, "one", "two", "fresh")
}

// a truncation starts the file over
func TestTailRotationByTruncate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "board.log")
	appendFile(t, path, "")
	r := startTail(t, path)
	waitFor(t, "the tail to open", func() bool { return sources.TailOpened(r.tail) })
	appendFile(t, path, "a long first line\n")
	waitBodies(t, r.rec, "a long first line")
	if err := os.Truncate(path, 0); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the truncation seen", func() bool { return sources.TailOffset(r.tail) == 0 })
	appendFile(t, path, "short\n")
	waitBodies(t, r.rec, "a long first line", "short")
}

// a file missing at the start is looked for again, and read from its start once it is there
func TestTailMissingThenCreated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "board.log")
	r := startTail(t, path)
	time.Sleep(50 * time.Millisecond)
	appendFile(t, path, "hello\n")
	waitBodies(t, r.rec, "hello")
}

func TestTailStops(t *testing.T) {
	r := startTail(t, filepath.Join(t.TempDir(), "board.log"))
	done := make(chan struct{})
	go func() { r.stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the tail did not stop")
	}
}

// a line the old file never ended goes out at the rotation, marked cut: its end may be a secret's start
func TestTailUnendedLineCutAtRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "board.log")
	appendFile(t, path, "")
	r := startTail(t, path)
	waitFor(t, "the tail to open", func() bool { return sources.TailOpened(r.tail) })
	appendFile(t, path, "a whole line\nhalf a li")
	waitFor(t, "the whole line", func() bool { return len(r.rec.all()) == 1 })
	if err := os.Truncate(path, 0); err != nil {
		t.Fatal(err)
	}
	waitBodies(t, r.rec, "a whole line", "half a li")
	if got := r.rec.all(); got[0].Truncated || !got[1].Truncated {
		t.Fatalf("records %+v", got)
	}
}
