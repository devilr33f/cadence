package deviceops

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/pkg/opsplan"
	"git.shino.boo/devilreef/cadence/server/internal/events"
	"git.shino.boo/devilreef/cadence/server/internal/store/db"
	"git.shino.boo/devilreef/cadence/server/internal/telemetry"
)

// RunView is a run as the dashboard sees it.
type RunView struct {
	ID            string               `json:"id"`
	DeviceID      string               `json:"deviceId"`
	ArtifactID    string               `json:"artifactId,omitempty"`
	Board         string               `json:"board"`
	TargetVersion string               `json:"targetVersion"`
	Transport     string               `json:"transport"`
	Status        string               `json:"status"`
	ErrorCode     string               `json:"errorCode,omitempty"`
	Steps         []opsplan.StepResult `json:"steps"`
	StartedAt     time.Time            `json:"startedAt"`
	FinishedAt    *time.Time           `json:"finishedAt,omitempty"`
}

// DeviceInfo is the device half Start needs.
type DeviceInfo struct {
	ID        string
	Board     string
	DaemonVer string
}

// DeviceLookup answers a device's board and daemon version; the wiring sets it from the store.
type DeviceLookup func(ctx context.Context, deviceID string) (DeviceInfo, error)

func (s *Service) lookup(ctx context.Context, deviceID string) (DeviceInfo, error) {
	if s.devices == nil {
		return DeviceInfo{}, ErrUnknownDevice
	}
	return s.devices(ctx, deviceID)
}

// StoreLookup answers from the devices table over the store's own queries.
func StoreLookup(q *db.Queries) DeviceLookup {
	return func(ctx context.Context, deviceID string) (DeviceInfo, error) {
		uid, ok := uuid(deviceID)
		if !ok {
			return DeviceInfo{}, ErrUnknownDevice
		}
		d, err := q.DeviceByID(ctx, uid)
		if errors.Is(err, pgx.ErrNoRows) {
			return DeviceInfo{}, ErrUnknownDevice
		}
		if err != nil {
			return DeviceInfo{}, err
		}
		return DeviceInfo{ID: d.ID.String(), Board: d.Model, DaemonVer: d.DaemonVersion}, nil
	}
}

// Start begins an update run: it resolves the plan for the artifact, stores the run and hands it
// to the transport the owner named (or the best one that reaches the device). One run at a time
// per device.
func (s *Service) Start(ctx context.Context, deviceID, artifactID, transport string) (RunView, error) {
	if _, err := s.lookup(ctx, deviceID); err != nil {
		return RunView{}, err
	}
	accs, err := s.access(ctx, deviceID)
	if err != nil {
		return RunView{}, err
	}
	if transport == "auto" {
		if transport = s.pickTransport(ctx, deviceID, artifactBoard(ctx, s, artifactID), accs); transport == "" {
			return RunView{}, ErrNoTransport
		}
	}
	if s.hasActiveRun(ctx, deviceID) {
		return RunView{}, ErrRunActive
	}
	r, err := s.createRun(ctx, deviceID, artifactID, transport, false)
	if err != nil {
		return RunView{}, err
	}
	switch transport {
	case TransportLink:
		if err := s.startLinkRun(r.deviceID.String(), r.id.String()); err != nil {
			s.failRun(r, opsplan.ErrTransport)
			return RunView{}, err
		}
	case TransportAdb:
		s.drive(r, &adbTransport{server: s.adb}, accs[TransportAdb])
	case TransportSSH:
		s.drive(r, &sshTransport{box: s.box, q: s.q}, accs[TransportSSH])
	default:
		s.failRun(r, opsplan.ErrTransport)
		return RunView{}, ErrNoTransport
	}
	return s.runView(ctx, r.id.String())
}

func artifactBoard(ctx context.Context, s *Service, artifactID string) string {
	a, err := s.loadArtifact(ctx, artifactID)
	if err != nil {
		return ""
	}
	return a.board
}

func (s *Service) hasActiveRun(ctx context.Context, deviceID string) bool {
	runs, err := s.q.ActiveOpsRun(ctx)
	if err != nil {
		return false
	}
	for _, r := range runs {
		if r.DeviceID.String() == deviceID {
			return true
		}
	}
	return false
}

// pickTransport prefers the daemon's own run (it survives adb address moves), then adb, then ssh.
func (s *Service) pickTransport(_ context.Context, deviceID, board string, accs map[string]access) string {
	prof, err := profileFor(board, artifactKindOf(board))
	if err != nil {
		return ""
	}
	if prof.supports(TransportLink) && s.link != nil && s.link.Online(deviceID) && s.helloProtocol(deviceID) >= opsProtocol {
		return TransportLink
	}
	if _, ok := accs[TransportAdb]; ok && s.adb != "" && prof.supports(TransportAdb) {
		return TransportAdb
	}
	if _, ok := accs[TransportSSH]; ok && prof.supports(TransportSSH) {
		return TransportSSH
	}
	return ""
}

func artifactKindOf(board string) string {
	switch board {
	case "android-tv":
		return "apk"
	case "rpi":
		return "deb"
	}
	return "tar"
}

// createRun resolves the plan and stores the run row.
func (s *Service) createRun(ctx context.Context, deviceID, artifactID, transport string, downgrade bool) (runRec, error) {
	dev, err := s.lookup(ctx, deviceID)
	if err != nil {
		return runRec{}, err
	}
	a, err := s.loadArtifact(ctx, artifactID)
	if err != nil {
		return runRec{}, err
	}
	prof, err := profileFor(a.board, a.kind)
	if err != nil {
		return runRec{}, err
	}
	if a.board != dev.Board {
		return runRec{}, ErrBoardMismatch
	}
	if !prof.supports(transport) {
		return runRec{}, ErrTransportRefused
	}
	a.downgrade = downgrade
	id := newID()
	plan, err := prof.build(a, id.String())
	if err != nil {
		return runRec{}, err
	}
	plan.RunID = id.String()
	if err := plan.Validate(); err != nil {
		return runRec{}, err
	}
	uid, _ := uuid(deviceID)
	aid, _ := uuid(artifactID)
	if _, err := s.q.PutOpsRun(ctx, db.PutOpsRunParams{
		ID: id, DeviceID: uid, ArtifactID: aid, Board: a.board, TargetVersion: a.version,
		Transport: transport, Plan: opsplan.Marshal(plan), Now: ts(s.now()),
	}); err != nil {
		return runRec{}, err
	}
	r := runRec{id: id, deviceID: uid, artifactID: aid, board: a.board, target: a.version,
		transport: transport, plan: plan, artifact: a}
	s.markActive(id.String())
	s.publish(EventRunStarted, deviceID, map[string]any{"runId": id.String(), "version": a.version, "transport": transport})
	s.log.Info("ops_run_started", "device_id", deviceID, "run_id", id.String(), "transport", transport, "version", a.version)
	s.opLog(deviceID, fmt.Sprintf("update run %s started: %s %s over %s", shortID(id.String()), a.board, a.version, transport))
	return r, nil
}

// runRec is a stored run in flight.
type runRec struct {
	id, deviceID, artifactID pgtype.UUID
	board, target, transport string
	plan                     *opsplan.Plan
	artifact                 *artifact
}

// drive runs a server-driven plan over a transport, on its own goroutine: the api answers with
// the run the moment it exists.
func (s *Service) drive(r runRec, tr transport, acc access) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), runTimeout)
		defer cancel()
		if acc.addr == "" {
			s.failRun(r, "no_access")
			return
		}
		if err := tr.probe(ctx, acc); err != nil {
			// codes and addresses only (the transport's own refusal text), never a secret
			s.log.Error("ops_transport_failed", "device_id", r.deviceID.String(), "run_id", r.id.String(),
				"reason", "probe", "err", err.Error())
			s.failRun(r, opsplan.ErrTransport)
			return
		}
		steps := make([]opsplan.StepResult, len(r.plan.Steps))
		restartAt, failed := s.runSteps(ctx, r, tr, acc, r.plan.Steps, steps)
		s.saveSteps(r.id, steps)
		switch {
		case failed == nil && restartAt >= 0:
			s.finishRun(r, StatusAwaitingRestart, "")
		case failed == nil:
			s.finishRun(r, StatusSucceeded, "")
		default:
			s.opLog(r.deviceID.String(), fmt.Sprintf("run %s failed at step %d (%s): rolling back",
				shortID(r.id.String()), failed.Index, failed.Result.ErrorCode))
			if err := s.rollbackSteps(ctx, r, tr, acc, steps); err != nil {
				s.finishRun(r, StatusFailed, opsplan.ErrRollback)
				return
			}
			s.finishRun(r, StatusRolledBack, failed.Result.ErrorCode)
		}
	}()
}

// stepFail is a step's failure, for the record.
type stepFail struct {
	Index  int
	Result opsplan.StepResult
}

// runSteps walks steps over the transport, recording each. It answers the index of the terminal
// restart step it reached (-1 when none ran) and the failure, if one did.
func (s *Service) runSteps(ctx context.Context, r runRec, tr transport, acc access, steps []opsplan.Step, out []opsplan.StepResult) (int, *stepFail) {
	for i, st := range steps {
		s.markActive(r.id.String())
		res := opsplan.StepResult{Index: i, Name: st.Name, State: opsplan.StateRunning, StartedAt: s.now()}
		out[i] = res
		s.stepEvent(r, res)
		if st.Fetch != nil {
			if err := s.doFetch(ctx, r, tr, acc, st.Fetch); err != nil {
				res.State, res.FinishedAt = opsplan.StateFailed, s.now()
				res.ErrorCode = stepCode(err)
				out[i] = res
				s.stepEvent(r, res)
				return -1, &stepFail{Index: i, Result: res}
			}
			out[i] = s.stepDone(r, res, 0, "")
			continue
		}
		code, tail, err := s.doExec(ctx, tr, acc, st.Exec, r, i)
		if err == nil && st.Exec.Terminal {
			out[i] = s.stepDone(r, res, code, tail)
			return i, nil
		}
		if err == nil && code == st.Exec.Expect {
			out[i] = s.stepDone(r, res, code, tail)
			continue
		}
		res.State, res.FinishedAt = opsplan.StateFailed, s.now()
		res.ExitCode = code
		if err != nil {
			res.ErrorCode = stepCode(err)
		} else {
			res.ErrorCode = opsplan.ErrExit
		}
		out[i] = res
		s.stepEvent(r, res)
		return -1, &stepFail{Index: i, Result: res}
	}
	return -1, nil
}

// stepCode maps an executor error to a step's error code.
func stepCode(err error) string {
	switch {
	case errors.Is(err, errHashMismatch):
		return opsplan.ErrHashMismatch
	case errors.Is(err, errTimeout):
		return opsplan.ErrTimeout
	case errors.Is(err, errFetchIO):
		return opsplan.ErrFetch
	case errors.Is(err, errExecIO):
		return opsplan.ErrExit
	default:
		return opsplan.ErrTransport
	}
}

var (
	errHashMismatch = errors.New("deviceops: hash mismatch")
	errTimeout      = errors.New("deviceops: exec timeout")
	errFetchIO      = errors.New("deviceops: fetch failed")
	errExecIO       = errors.New("deviceops: exec failed")
)

// doFetch writes one file through the transport and checks its hash on the device.
func (s *Service) doFetch(ctx context.Context, r runRec, tr transport, acc access, f *opsplan.FetchStep) error {
	b, err := r.artifact.open(f.Src)
	if err != nil {
		return errFetchIO
	}
	if err := tr.writeFile(ctx, acc, f.Dst, f.Mode, b); err != nil {
		return errFetchIO
	}
	got, err := tr.readHash(ctx, acc, f.Dst)
	if err != nil {
		return err
	}
	if got != f.SHA256 {
		return errHashMismatch
	}
	return nil
}

// doExec runs one command, recording its output tail in the device's logs beside the step.
func (s *Service) doExec(ctx context.Context, tr transport, acc access, e *opsplan.ExecStep, r runRec, index int) (int, string, error) {
	wait := execDefaultWait
	if e.TimeoutMs > 0 {
		wait = time.Duration(e.TimeoutMs) * time.Millisecond
	}
	code, out, err := tr.exec(ctx, acc, e.Cmd, wait)
	tail := opsplan.CleanTail([]byte(out), 2048)
	if tail != "" {
		s.opLog(r.deviceID.String(), fmt.Sprintf("run %s step %d (%s) output: %s", shortID(r.id.String()), index, e.Cmd, clip(tail, 1024)))
	}
	return code, tail, err
}

// rollbackSteps undoes a half-applied plan; its own failure is the run's.
func (s *Service) rollbackSteps(ctx context.Context, r runRec, tr transport, acc access, steps []opsplan.StepResult) error {
	out := make([]opsplan.StepResult, len(r.plan.Rollback))
	_, failed := s.runSteps(ctx, r, tr, acc, r.plan.Rollback, out)
	s.saveSteps(r.id, append(steps, out...))
	if failed != nil {
		return fmt.Errorf("rollback step %d: %s", failed.Index, failed.Result.ErrorCode)
	}
	return nil
}

func (s *Service) stepDone(r runRec, res opsplan.StepResult, code int, tail string) opsplan.StepResult {
	res.State = opsplan.StateDone
	res.ExitCode = code
	res.OutputTail = clip(tail, 1024)
	res.FinishedAt = s.now()
	s.stepEvent(r, res)
	return res
}

func (s *Service) stepEvent(r runRec, res opsplan.StepResult) {
	s.publish(EventRunStep, r.deviceID.String(), map[string]any{
		"runId": r.id.String(), "index": res.Index, "state": res.State, "code": res.ErrorCode, "exit": res.ExitCode,
	})
	s.log.Info("ops_step", "run_id", r.id.String(), "index", res.Index, "state", res.State, "code", res.ErrorCode)
	s.opLog(r.deviceID.String(), fmt.Sprintf("run %s step %d (%s) %s%s", shortID(r.id.String()), res.Index, res.Name, res.State,
		exitSuffix(res)))
}

func exitSuffix(res opsplan.StepResult) string {
	if res.State != opsplan.StateDone || res.ExitCode == 0 {
		return ""
	}
	return fmt.Sprintf(" (exit %d)", res.ExitCode)
}

func (s *Service) saveSteps(id pgtype.UUID, steps []opsplan.StepResult) {
	if s.q == nil {
		return
	}
	b, err := json.Marshal(steps)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.q.SetOpsRunSteps(ctx, db.SetOpsRunStepsParams{ID: id, Steps: b}); err != nil {
		s.log.Error("ops_steps_store_failed", "run_id", id.String())
	}
}

func (s *Service) finishRun(r runRec, status, errCode string) {
	s.finish(context.Background(), r.id, status, errCode)
	s.publish(EventRunEnded, r.deviceID.String(), map[string]any{"runId": r.id.String(), "status": status, "code": errCode})
	s.log.Info("ops_run_ended", "run_id", r.id.String(), "status", status, "code", errCode)
	s.opLog(r.deviceID.String(), fmt.Sprintf("run %s %s%s", shortID(r.id.String()), status, codeSuffix(errCode)))
}

func (s *Service) failRun(r runRec, errCode string) { s.finishRun(r, StatusFailed, errCode) }

func codeSuffix(c string) string {
	if c == "" {
		return ""
	}
	return " (" + c + ")"
}

// finish writes a run's status; terminal statuses take their finished_at.
func (s *Service) finish(ctx context.Context, id pgtype.UUID, status, errCode string) {
	s.markActive(id.String())
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	terminal := status != StatusRunning && status != StatusAwaitingRestart
	var err error
	if terminal {
		err = s.q.FinishOpsRun(cctx, db.FinishOpsRunParams{ID: id, Status: status, ErrorCode: errCode, Now: ts(s.now())})
	} else {
		err = s.q.SetOpsRunStatus(cctx, db.SetOpsRunStatusParams{ID: id, Status: status, ErrorCode: errCode})
	}
	if err != nil {
		s.log.Error("ops_status_store_failed", "run_id", id.String())
	}
}

// Get answers one run.
func (s *Service) Get(ctx context.Context, runID string) (RunView, error) {
	return s.runView(ctx, runID)
}

// Runs answers a device's runs, newest first.
func (s *Service) Runs(ctx context.Context, deviceID string) ([]RunView, error) {
	uid, ok := uuid(deviceID)
	if !ok {
		return nil, ErrUnknownDevice
	}
	rows, err := s.q.ListOpsRuns(ctx, uid)
	if err != nil {
		return nil, err
	}
	out := make([]RunView, 0, len(rows))
	for _, r := range rows {
		out = append(out, runViewFrom(r.ID, r.ArtifactID, r.Board, r.TargetVersion, r.Transport,
			r.Status, r.ErrorCode, r.Steps, r.StartedAt, r.FinishedAt, deviceID))
	}
	return out, nil
}

func (s *Service) runView(ctx context.Context, runID string) (RunView, error) {
	uid, ok := uuid(runID)
	if !ok {
		return RunView{}, ErrUnknownRun
	}
	r, err := s.q.GetOpsRun(ctx, uid)
	if errors.Is(err, pgx.ErrNoRows) {
		return RunView{}, ErrUnknownRun
	}
	if err != nil {
		return RunView{}, err
	}
	return runViewFrom(r.ID, r.ArtifactID, r.Board, r.TargetVersion, r.Transport,
		r.Status, r.ErrorCode, r.Steps, r.StartedAt, r.FinishedAt, r.DeviceID.String()), nil
}

func runViewFrom(id, artifactID pgtype.UUID, board, target, transport, status, errCode string,
	steps []byte, started, finished pgtype.Timestamptz, deviceID string) RunView {
	v := RunView{ID: id.String(), DeviceID: deviceID, Board: board, TargetVersion: target,
		Transport: transport, Status: status, ErrorCode: errCode, StartedAt: started.Time}
	if artifactID.Valid {
		v.ArtifactID = artifactID.String()
	}
	if finished.Valid {
		t := finished.Time
		v.FinishedAt = &t
	}
	var ss []opsplan.StepResult
	_ = json.Unmarshal(steps, &ss)
	if ss == nil {
		ss = []opsplan.StepResult{}
	}
	v.Steps = ss
	return v
}

// Rollback installs the artifact the device ran before the given run's (the dashboard's undo):
// the same profiles, an older build (the tv's with -d).
func (s *Service) Rollback(ctx context.Context, runID string) (RunView, error) {
	r, err := s.runView(ctx, runID)
	if err != nil {
		return RunView{}, err
	}
	runs, err := s.Runs(ctx, r.DeviceID)
	if err != nil {
		return RunView{}, err
	}
	for _, prev := range runs {
		if prev.StartedAt.Before(r.StartedAt) && prev.Status == StatusSucceeded &&
			prev.ArtifactID != "" && prev.ArtifactID != r.ArtifactID {
			return s.startDowngrade(ctx, r.DeviceID, prev.ArtifactID, r.Transport)
		}
	}
	return RunView{}, ErrUnknownArtifact
}

// startDowngrade is Start for an older build: the run records it was a rollback's install.
func (s *Service) startDowngrade(ctx context.Context, deviceID, artifactID, transport string) (RunView, error) {
	if s.hasActiveRun(ctx, deviceID) {
		return RunView{}, ErrRunActive
	}
	r, err := s.createRun(ctx, deviceID, artifactID, transport, true)
	if err != nil {
		return RunView{}, err
	}
	switch transport {
	case TransportAdb:
		accs, err := s.access(ctx, deviceID)
		if err != nil {
			return RunView{}, err
		}
		s.drive(r, &adbTransport{server: s.adb}, accs[TransportAdb])
	case TransportSSH:
		accs, err := s.access(ctx, deviceID)
		if err != nil {
			return RunView{}, err
		}
		s.drive(r, &sshTransport{box: s.box, q: s.q}, accs[TransportSSH])
	default:
		if err := s.startLinkRun(r.deviceID.String(), r.id.String()); err != nil {
			s.failRun(r, opsplan.ErrTransport)
			return RunView{}, err
		}
	}
	return s.runView(ctx, r.id.String())
}

// publish puts one ops event on the feed.
func (s *Service) publish(kind, deviceID string, data map[string]any) {
	if s.bus == nil {
		return
	}
	s.bus.Publish(events.Event{Type: kind, DeviceID: deviceID, At: s.now(), Data: data})
}

// opLog writes one line of a run into the device's log store, so the Logs page shows updater
// steps and command output beside the daemon's own lines (the owner's ask). Bodies carry step
// names, codes and output tails only.
func (s *Service) opLog(deviceID, body string) {
	if s.logs == nil || body == "" {
		return
	}
	id, ok := uuid(deviceID)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	now := s.now()
	_ = s.logs.Insert(ctx, []telemetry.LogRow{{
		Time: now, Observed: now, DeviceID: id, Source: "ops", Severity: 9, Body: clip(body, 2048),
	}})
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// markActive and lastActive feed the watchdog's idle checks.
func (s *Service) markActive(runID string) {
	s.mu.Lock()
	s.activity[runID] = s.now()
	s.mu.Unlock()
}

func (s *Service) lastActive(runID string) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.activity[runID]
}

// helloProtocol answers the protocol the device last linked with (0 when never seen).
func (s *Service) helloProtocol(deviceID string) uint32 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hellos[deviceID].protocol
}

// NoteHello records a device's hello: its daemon version is kept (a run's success is the relink
// that names its target) and an awaited restart whose target arrived is closed.
func (s *Service) NoteHello(ctx context.Context, deviceID string, protocol uint32, version string) {
	s.mu.Lock()
	s.hellos[deviceID] = hello{protocol: protocol, version: version}
	s.mu.Unlock()
	if id, ok := uuid(deviceID); ok && version != "" {
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if err := s.q.SetDeviceDaemonVersion(cctx, db.SetDeviceDaemonVersionParams{ID: id, Version: version}); err != nil {
			s.log.Error("ops_version_store_failed", "device_id", deviceID)
		}
	}
	s.closeOnVersion(ctx, deviceID, version)
}

func (s *Service) closeOnVersion(ctx context.Context, deviceID, version string) {
	runs, err := s.q.ActiveOpsRun(ctx)
	if err != nil {
		return
	}
	for _, r := range runs {
		if r.DeviceID.String() != deviceID || r.Status != StatusAwaitingRestart {
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		row, err := s.q.GetOpsRun(cctx, r.ID)
		cancel()
		if err != nil {
			continue
		}
		var plan opsplan.Plan
		if json.Unmarshal(row.Plan, &plan) != nil || plan.TargetVersion != version {
			continue
		}
		s.finishRun(runRec{id: r.ID, deviceID: r.DeviceID, target: version}, StatusSucceeded, "")
	}
}

// HandleProgress records the daemon's own progress frames (a link-run).
func (s *Service) HandleProgress(deviceID string, m *cadencev2.OpsProgress) {
	if m == nil {
		return
	}
	uid, ok := uuid(m.GetRunId())
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	row, err := s.q.GetOpsRun(ctx, uid)
	if err != nil || row.DeviceID.String() != deviceID || row.Status != StatusRunning {
		return
	}
	s.markActive(uid.String())
	var steps []opsplan.StepResult
	_ = json.Unmarshal(row.Steps, &steps)
	for len(steps) <= int(m.GetIndex()) {
		steps = append(steps, opsplan.StepResult{Index: len(steps), State: opsplan.StateDone})
	}
	i := int(m.GetIndex())
	steps[i] = opsplan.StepResult{Index: i, Name: stepName(row.Plan, i), State: m.GetState(), ExitCode: int(m.GetExitCode()),
		ErrorCode: m.GetErrorCode(), OutputTail: opsplan.CleanTail(m.GetTail(), 1024), FinishedAt: s.now()}
	s.saveSteps(uid, steps)
	s.stepEvent(runRec{id: uid, deviceID: row.DeviceID}, steps[i])
	if m.GetState() == opsplan.StateFailed {
		s.log.Info("ops_selfrun_step_failed", "device_id", deviceID, "run_id", uid.String(), "code", m.GetErrorCode())
	}
}

// HandleResult closes a link-run the daemon answered itself. A restart plan's daemon dies before
// this arrives: the relink closes it instead (NoteHello).
func (s *Service) HandleResult(deviceID string, m *cadencev2.OpsResult) {
	if m == nil {
		return
	}
	uid, ok := uuid(m.GetRunId())
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	row, err := s.q.GetOpsRun(ctx, uid)
	if err != nil || row.DeviceID.String() != deviceID {
		return
	}
	var plan opsplan.Plan
	_ = json.Unmarshal(row.Plan, &plan)
	switch {
	case m.GetCode() == "" && plan.Restart:
		// every step answered: the restart's relink (or the watchdog) closes it
		s.finishRun(runRec{id: uid, deviceID: row.DeviceID}, StatusAwaitingRestart, "")
	case m.GetCode() == "":
		s.finishRun(runRec{id: uid, deviceID: row.DeviceID}, StatusSucceeded, "")
	case m.GetCode() == StatusRolledBack:
		s.finishRun(runRec{id: uid, deviceID: row.DeviceID}, StatusRolledBack, "")
	default:
		s.finishRun(runRec{id: uid, deviceID: row.DeviceID}, StatusFailed, m.GetCode())
	}
}

func stepName(planJSON []byte, i int) string {
	var plan opsplan.Plan
	if json.Unmarshal(planJSON, &plan) != nil || i >= len(plan.Steps) {
		return ""
	}
	return plan.Steps[i].Name
}

// opsProtocol is the first link protocol whose daemon can run a plan itself.
const opsProtocol = 3

// startLinkRun nudges an online daemon to run its plan: the frames carry control only, the plan
// and its files come over https with the device's token.
func (s *Service) startLinkRun(deviceID, runID string) error {
	if s.link == nil {
		return errors.New("deviceops: no link")
	}
	return s.link.Send(deviceID, &cadencev2.Envelope{Body: &cadencev2.Envelope_OpsStart{OpsStart: &cadencev2.OpsStart{RunId: runID}}})
}

// newID makes a random uuid.
func newID() pgtype.UUID {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	b[6], b[9] = (b[6]&0x0f)|0x40, (b[9]&0x3f)|0x80
	var id pgtype.UUID
	_ = id.Scan(hex.EncodeToString(b))
	return id
}

// Handle consumes the device's own update-run frames off the link; it answers whether env was one.
func (s *Service) Handle(deviceID string, env *cadencev2.Envelope) bool {
	switch b := env.GetBody().(type) {
	case *cadencev2.Envelope_OpsProgress:
		s.HandleProgress(deviceID, b.OpsProgress)
	case *cadencev2.Envelope_OpsResult:
		s.HandleResult(deviceID, b.OpsResult)
	default:
		return false
	}
	return true
}
