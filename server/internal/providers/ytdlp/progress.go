package ytdlp

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/playprogress"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

// ResolveEvent is a screen resolve's privacy-safe live stage.
type ResolveEvent = playprogress.Event

type progressKey struct{}

// WithProgress makes the next yt-dlp resolve on ctx report its stages to f.
// Reporters compose, so a play can feed its resolving screen and its tool card.
func WithProgress(ctx context.Context, f func(ResolveEvent)) context.Context {
	if f == nil {
		return ctx
	}
	if old := progressReporter(ctx); old != nil {
		return context.WithValue(ctx, progressKey{}, func(ev ResolveEvent) {
			old(ev)
			f(ev)
		})
	}
	return context.WithValue(ctx, progressKey{}, f)
}

// HasProgress reports whether a caller wants the detailed stages. The catalog
// uses it to leave out its coarser reading_stream step in that case.
func HasProgress(ctx context.Context) bool { return progressReporter(ctx) != nil }

func progressReporter(ctx context.Context) func(ResolveEvent) {
	f, _ := ctx.Value(progressKey{}).(func(ResolveEvent))
	return f
}

// stage ranks keep stderr lines from moving a resolve backwards.
var stageRank = map[playprogress.Stage]int{
	playprogress.StageCookies: 0, playprogress.StageExtract: 1, playprogress.StageChallenge: 2,
	playprogress.StageFormats: 3, playprogress.StagePick: 4, playprogress.StageWaitAvailable: 5,
	playprogress.StageRelay: 6,
}

type stageTiming struct {
	stage playprogress.Stage
	ms    int64
}

// resolveProgress owns one run's timings. The external reporter may outlive
// yt-dlp's return: relay's first byte is a late stage of the same play.
type resolveProgress struct {
	mode string
	log  *slog.Logger
	out  func(ResolveEvent)

	mu       sync.Mutex
	start    time.Time
	stage    playprogress.Stage
	stageAt  time.Time
	timings  []stageTiming
	finished bool
}

func (p *Player) beginProgress(ctx context.Context, mode string) (context.Context, func(error)) {
	r := &resolveProgress{mode: mode, log: p.cfg.Log, out: progressReporter(ctx), start: time.Now()}
	r.stageAt = r.start
	ctx = context.WithValue(ctx, progressKey{}, r.report)
	return ctx, r.finish
}

func (r *resolveProgress) report(ev ResolveEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	if r.finished {
		// only the relay has work after yt-dlp answered; do not close the last
		// extraction stage twice or rewrite the resolve's final summary
		if ev.Stage != playprogress.StageRelay {
			return
		}
		ev.ElapsedMS = now.Sub(r.start).Milliseconds()
		r.log.Info("ytdlp_resolve_stage", "mode", r.mode, "stage", ev.Stage, "ms", 0, "elapsed_ms", ev.ElapsedMS)
		if r.out != nil {
			r.out(ev)
		}
		return
	}
	// a real zero is meaningful at the countdown's end
	if ev.RemainingMS == 0 && ev.Stage != playprogress.StageWaitAvailable {
		ev.RemainingMS = -1
	}
	if r.stage == ev.Stage && ev.Stage != playprogress.StageWaitAvailable {
		return
	}
	if r.stage != "" && ev.Stage != r.stage {
		rank, known := stageRank[ev.Stage]
		if !known || rank < stageRank[r.stage] {
			return
		}
		ms := now.Sub(r.stageAt).Milliseconds()
		r.timings = append(r.timings, stageTiming{stage: r.stage, ms: ms})
		r.log.Info("ytdlp_resolve_stage", "mode", r.mode, "stage", r.stage, "ms", ms,
			"elapsed_ms", now.Sub(r.start).Milliseconds())
		r.stage = ""
	}
	if r.stage == "" {
		r.stage, r.stageAt = ev.Stage, now
	}
	ev.ElapsedMS = now.Sub(r.start).Milliseconds()
	if r.out != nil {
		r.out(ev)
	}
}

func (r *resolveProgress) finish(err error) {
	r.mu.Lock()
	if r.finished {
		r.mu.Unlock()
		return
	}
	r.finished = true
	now := time.Now()
	stage := r.stage
	if stage != "" {
		ms := now.Sub(r.stageAt).Milliseconds()
		r.timings = append(r.timings, stageTiming{stage: stage, ms: ms})
		r.log.Info("ytdlp_resolve_stage", "mode", r.mode, "stage", stage, "ms", ms,
			"elapsed_ms", now.Sub(r.start).Milliseconds())
	}
	result, code := "done", ""
	if err != nil {
		result, code = "failed", resolveFailureCode(err)
	}
	if r.out != nil && stage != "" {
		r.out(ResolveEvent{Stage: stage, ElapsedMS: now.Sub(r.start).Milliseconds(), RemainingMS: -1,
			Done: err == nil, Failed: err != nil, Code: code})
	}
	parts := make([]string, len(r.timings))
	for i, t := range r.timings {
		parts[i] = string(t.stage) + "=" + strconv.FormatInt(t.ms, 10)
	}
	attrs := []any{"mode", r.mode, "result", result, "stages", strings.Join(parts, ","), "elapsed_ms", now.Sub(r.start).Milliseconds()}
	if code != "" {
		attrs = append(attrs, "code", code, "failed_stage", stage)
	}
	r.log.Info("ytdlp_resolve_stages", attrs...)
	r.mu.Unlock()
}

func resolveFailureCode(err error) string {
	switch {
	case errors.Is(err, providers.ErrSignInRequired):
		return "sign_in_required"
	case errors.Is(err, providers.ErrUnplayable):
		return "unplayable"
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return "timeout"
	}
	return providers.CodePlayerFailed
}

// relayFirstByte reports the actual first body byte read from upstream. It is
// valid after the resolve returned: the player opens the relay only then.
func relayFirstByte(ctx context.Context) {
	if f := progressReporter(ctx); f != nil {
		f(ResolveEvent{Stage: playprogress.StageRelay, RemainingMS: -1})
	}
}

// RelayFirstByte is the relay's notification to a resolve's stage stream.
func RelayFirstByte(ctx context.Context) { relayFirstByte(ctx) }
