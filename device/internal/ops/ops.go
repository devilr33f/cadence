// Package ops is the daemon's half of a device update: the server sends an ops_start frame naming
// a run, the daemon fetches the run's resolved plan and its files over https with its own token,
// walks the fetch and exec steps, and answers each with an ops_progress frame. A plan whose last
// step restarts the daemon never answers with a result: the server closes the run when this
// device relinks and its hello names the plan's target version. Steps never run while a session
// is live or a ring sounds (rule 7); the run is refused instead.
package ops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/pkg/opsplan"
)

// Getter fetches one path from the server with the device's bearer token (otlp.Client).
type Getter interface {
	Get(ctx context.Context, path string) (io.ReadCloser, error)
}

const (
	fetchWait    = 10 * time.Minute
	execWait     = 10 * time.Minute
	maxTailLines = 8
)

// Executor runs plans the server offers over the link. One at a time.
type Executor struct {
	get   Getter
	shell string
	log   *slog.Logger
	busy  func() (string, bool)
	now   func() time.Time

	sendMu sync.Mutex
	send   func(*cadencev2.Envelope) error
}

// Deps wires the executor.
type Deps struct {
	Get   Getter
	Shell string
	Log   *slog.Logger
	// Busy answers why the daemon must not restart now (a live session, a ring)
	Busy func() (string, bool)
}

// New builds the executor; its sender is set once a link is up.
func New(d Deps) *Executor {
	if d.Shell == "" {
		d.Shell = "/bin/sh"
	}
	return &Executor{get: d.Get, shell: d.Shell, log: d.Log, busy: d.Busy, now: time.Now}
}

// SetSend points the executor at the current link's send.
func (e *Executor) SetSend(send func(*cadencev2.Envelope) error) {
	e.sendMu.Lock()
	e.send = send
	e.sendMu.Unlock()
}

func (e *Executor) frame(env *cadencev2.Envelope) {
	e.sendMu.Lock()
	send := e.send
	e.sendMu.Unlock()
	if send == nil {
		return
	}
	// the link may drop any moment (a restart step): a lost frame is the watchdog's business
	_ = send(env)
}

// Start runs one plan on its own goroutine. The result frame always goes out when the step list
// was walked to its end; a restart step that killed the daemon mid-run is the one case it never
// will, and the server closes that run on the relink.
func (e *Executor) Start(ctx context.Context, runID string) {
	go func() {
		e.frame(result(runID, e.run(ctx, runID)))
	}()
}

func (e *Executor) run(ctx context.Context, runID string) string {
	if e.busy != nil {
		if reason, busy := e.busy(); busy {
			e.log.Info("ops_refused", "run_id", runID, "reason", reason)
			return opsplan.ErrRefusedBusy
		}
	}
	body, err := e.get.Get(ctx, "/v2/device-plan/"+runID)
	if err != nil {
		e.log.Error("ops_plan_failed", "run_id", runID, "reason", "fetch")
		return opsplan.ErrUnknownPlan
	}
	b, err := io.ReadAll(body)
	_ = body.Close()
	if err != nil {
		return opsplan.ErrFetch
	}
	plan, err := opsplan.Unmarshal(b)
	if err != nil || plan.RunID != runID {
		e.log.Error("ops_plan_failed", "run_id", runID, "reason", "parse")
		return opsplan.ErrUnknownPlan
	}
	e.log.Info("ops_plan_started", "run_id", runID, "plan", plan.ID, "version", plan.TargetVersion,
		"steps", len(plan.Steps))
	for i, st := range plan.Steps {
		code, _ := e.step(ctx, runID, i, st)
		if code == "" {
			continue
		}
		e.log.Warn("ops_step_failed", "run_id", runID, "index", i, "code", code)
		if rb := e.rollback(ctx, runID, plan); rb != "" {
			return rb
		}
		return "rolled_back"
	}
	e.log.Info("ops_plan_done", "run_id", runID)
	return ""
}

// step runs one step, answering "" when it went (or was skipped), else its error code.
func (e *Executor) step(ctx context.Context, runID string, i int, st opsplan.Step) (string, string) {
	switch {
	case st.Fetch != nil:
		code := e.fetch(ctx, runID, i, st.Fetch)
		if code == "" || code == opsplan.ErrExit { // skipped rides as done
			return "", ""
		}
		return code, ""
	case st.Exec != nil:
		wait := execWait
		if st.Exec.TimeoutMs > 0 {
			wait = time.Duration(st.Exec.TimeoutMs) * time.Millisecond
		}
		cctx, cancel := context.WithTimeout(ctx, wait)
		defer cancel()
		cmd := exec.CommandContext(cctx, e.shell, "-c", st.Exec.Cmd) //nolint:gosec // the server's own plan step
		out, err := cmd.CombinedOutput()
		tail := opsplan.CleanTail(out, 2048)
		code := 0
		if err != nil {
			code = -1
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				code = ee.ExitCode()
			}
		}
		switch {
		case cctx.Err() != nil:
			e.progress(runID, i, opsplan.StateFailed, code, opsplan.ErrTimeout, tail)
			return opsplan.ErrTimeout, tail
		case code != st.Exec.Expect && !st.Exec.Terminal:
			e.progress(runID, i, opsplan.StateFailed, code, opsplan.ErrExit, tail)
			return opsplan.ErrExit, tail
		}
		// a terminal step that failed may be the one that killed us: the relink decides
		e.progress(runID, i, opsplan.StateDone, code, "", tail)
		return "", tail
	}
	return opsplan.ErrUnknownPlan, ""
}

// fetch materializes one file: skipped when the tree already holds its hash, else downloaded to a
// part file, hashed, renamed into place and chmod'ed.
func (e *Executor) fetch(ctx context.Context, runID string, i int, f *opsplan.FetchStep) string {
	if f.SkipIfSame {
		target := strings.TrimSuffix(f.Dst, ".new")
		if fileHash(target) == f.SHA256 {
			// the swap step still wants the staged file: copy what is already right
			if b, err := os.ReadFile(target); err == nil { //nolint:gosec // a path the server's plan names
				if err := writeStaged(f, b); err == nil {
					e.progress(runID, i, opsplan.StateSkipped, 0, "", "")
					return ""
				}
			}
		}
	}
	cctx, cancel := context.WithTimeout(ctx, fetchWait)
	defer cancel()
	p := strings.TrimPrefix(f.Src, "artifact:")
	path := "/v2/device-artifact/" + runID + "/" + p
	if p == "" {
		path = "/v2/device-artifact/" + runID
	}
	body, err := e.get.Get(cctx, path)
	if err != nil {
		e.progress(runID, i, opsplan.StateFailed, 0, opsplan.ErrFetch, "")
		return opsplan.ErrFetch
	}
	defer func() { _ = body.Close() }()
	if err := e.stream(cctx, f, body); err != nil {
		_ = os.Remove(f.Dst + ".part")
		e.progress(runID, i, opsplan.StateFailed, 0, opsplan.ErrHashMismatch, "")
		return opsplan.ErrHashMismatch
	}
	if err := os.Chmod(f.Dst, os.FileMode(modeOf(f))); err != nil {
		_ = os.Remove(f.Dst)
		e.progress(runID, i, opsplan.StateFailed, 0, opsplan.ErrFetch, "")
		return opsplan.ErrFetch
	}
	e.progress(runID, i, opsplan.StateDone, 0, "", fmt.Sprintf("%s (%d bytes)", f.Dst, f.Size))
	return ""
}

// stream writes body to the staged dst and checks its hash before the rename.
func (e *Executor) stream(_ context.Context, f *opsplan.FetchStep, body io.Reader) error {
	part := f.Dst + ".part"
	out, err := os.Create(part) //nolint:gosec // a path the server's plan names
	if err != nil {
		return err
	}
	sum := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(out, sum), body)
	closeErr := out.Close()
	if copyErr != nil || closeErr != nil {
		_ = os.Remove(part)
		return errors.New("ops: copy")
	}
	if hex.EncodeToString(sum.Sum(nil)) != f.SHA256 {
		_ = os.Remove(part)
		return errors.New("ops: hash")
	}
	if err := os.Rename(part, f.Dst); err != nil {
		_ = os.Remove(part)
		return err
	}
	return nil
}

// writeStaged copies the tree's own file into place with the mode it has (the download path
// chmods its own write; this one keeps what was already right).
func writeStaged(f *opsplan.FetchStep, b []byte) error {
	mode := os.FileMode(modeOf(f))
	if st, err := os.Stat(strings.TrimSuffix(f.Dst, ".new")); err == nil {
		mode = st.Mode().Perm()
	}
	if err := os.WriteFile(f.Dst+".part", b, mode); err != nil { //nolint:gosec // a path the server's plan names
		return err
	}
	return os.Rename(f.Dst+".part", f.Dst)
}

// rollback walks the plan's rollback steps; "" when the tree is back.
func (e *Executor) rollback(ctx context.Context, runID string, plan *opsplan.Plan) string {
	e.log.Warn("ops_rollback", "run_id", runID, "steps", len(plan.Rollback))
	base := len(plan.Steps)
	for i, st := range plan.Rollback {
		code, _ := e.step(ctx, runID, base+i, st)
		if code != "" {
			return opsplan.ErrRollback
		}
	}
	return ""
}

func (e *Executor) progress(runID string, i int, state string, exit int, errCode, tail string) {
	e.log.Info("ops_step", "run_id", runID, "index", i, "state", state, "code", errCode, "exit", exit)
	for _, line := range tailLines(tail) {
		e.log.Info("ops_output", "run_id", runID, "index", i, "line", line)
	}
	e.frame(&cadencev2.Envelope{Body: &cadencev2.Envelope_OpsProgress{OpsProgress: &cadencev2.OpsProgress{
		RunId: runID, State: state, ErrorCode: errCode,
		Index: uint32(i), ExitCode: int32(exit), //nolint:gosec // a plan's step index; an exit code, -1-255
		Tail: []byte(opsplan.CleanTail([]byte(tail), 1024)),
	}}})
}

func result(runID, code string) *cadencev2.Envelope {
	return &cadencev2.Envelope{Body: &cadencev2.Envelope_OpsResult{OpsResult: &cadencev2.OpsResult{
		RunId: runID, Code: code,
	}}}
}

func tailLines(tail string) []string {
	if tail == "" {
		return nil
	}
	lines := strings.Split(tail, "\n")
	if len(lines) > maxTailLines {
		lines = lines[len(lines)-maxTailLines:]
	}
	return lines
}

func fileHash(path string) string {
	f, err := os.Open(path) //nolint:gosec // a path the server's plan names
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	sum := sha256.New()
	if _, err := io.Copy(sum, f); err != nil {
		return ""
	}
	return hex.EncodeToString(sum.Sum(nil))
}

func modeOf(f *opsplan.FetchStep) uint32 {
	if f.Mode == 0 {
		return 0o644
	}
	return f.Mode
}
