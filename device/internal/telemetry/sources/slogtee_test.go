package sources_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/telemetry/logs"
	"git.shino.boo/devilreef/cadence/device/internal/telemetry/sources"
)

var at = time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)

func record(level slog.Level, msg string, attrs ...slog.Attr) slog.Record {
	r := slog.NewRecord(at, level, msg, 0)
	r.AddAttrs(attrs...)
	return r
}

// the tee writes byte for byte what cadenced's json handler wrote before it, and hands each line to the pipeline once
func TestTeeWritesAsBeforeAndEmitsOnce(t *testing.T) {
	var before, now bytes.Buffer
	plain := slog.NewJSONHandler(&before, nil).WithAttrs([]slog.Attr{slog.String("board", "sim")}).WithGroup("g")
	tee := sources.NewTee(&now)
	rec := &recorder{}
	tee.Attach(rec)
	h := tee.WithAttrs([]slog.Attr{slog.String("board", "sim")}).WithGroup("g")
	for _, r := range []slog.Record{
		record(slog.LevelInfo, "board_ready", slog.String("version", "dev")),
		record(slog.LevelWarn, "audiod_exit", slog.Int("code", 3)),
	} {
		if err := plain.Handle(context.Background(), r); err != nil {
			t.Fatal(err)
		}
		if err := h.Handle(context.Background(), r); err != nil {
			t.Fatal(err)
		}
	}
	if now.String() != before.String() {
		t.Fatalf("tee wrote\n%s\nwant\n%s", now.String(), before.String())
	}
	lines := strings.Split(strings.TrimSuffix(before.String(), "\n"), "\n")
	got := rec.all()
	if len(got) != 2 {
		t.Fatalf("emitted %d records", len(got))
	}
	for i, r := range got {
		if r.Source != "cadenced" || r.Body != lines[i] || !r.Time.Equal(at) || r.Truncated {
			t.Fatalf("record %d = %+v", i, r)
		}
	}
	if got[0].Severity != logs.Info || got[1].Severity != logs.Warn {
		t.Fatalf("severities %v %v", got[0].Severity, got[1].Severity)
	}
}

// before the pipeline is there, a line goes to the writer alone
func TestTeeBeforeAttach(t *testing.T) {
	var out bytes.Buffer
	tee := sources.NewTee(&out)
	slog.New(tee).Info("config_waiting")
	rec := &recorder{}
	tee.Attach(rec)
	slog.New(tee).Info("board_ready")
	if !strings.Contains(out.String(), "config_waiting") || len(rec.all()) != 1 || !strings.Contains(rec.all()[0].Body, "board_ready") {
		t.Fatalf("out %q, emitted %v", out.String(), rec.bodies())
	}
}

func TestTeeSeverity(t *testing.T) {
	tee := sources.NewTee(&bytes.Buffer{})
	rec := &recorder{}
	tee.Attach(rec)
	for _, l := range []slog.Level{slog.LevelDebug - 4, slog.LevelDebug, slog.LevelInfo, slog.LevelInfo + 2, slog.LevelWarn,
		slog.LevelError, slog.LevelError + 4} {
		if err := tee.Handle(context.Background(), record(l, "x")); err != nil {
			t.Fatal(err)
		}
	}
	want := []logs.Severity{logs.Debug, logs.Debug, logs.Info, logs.Info, logs.Warn, logs.Error, logs.Error}
	for i, r := range rec.all() {
		if r.Severity != want[i] {
			t.Fatalf("record %d: severity %v, want %v", i, r.Severity, want[i])
		}
	}
}

// the handler's level holds for both: a debug line is neither written nor emitted
func TestTeeEnabled(t *testing.T) {
	var out bytes.Buffer
	tee := sources.NewTee(&out)
	rec := &recorder{}
	tee.Attach(rec)
	slog.New(tee).Debug("noise")
	if out.Len() != 0 || len(rec.all()) != 0 {
		t.Fatalf("debug line went out: %q", out.String())
	}
}
