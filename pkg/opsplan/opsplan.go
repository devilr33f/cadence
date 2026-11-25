// Package opsplan is the device-update plan both sides share: the server resolves one per device
// and run, the executor (the daemon on a device, or the server over adb or ssh) walks its steps.
// A plan is fetch and exec steps alone (the owner's primitive set), every path and hash resolved
// before it travels; nothing but control frames rides the device link, files come over https.
package opsplan

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Step states, in a StepResult and an OpsProgress frame.
const (
	StateRunning = "running"
	StateDone    = "done"
	StateFailed  = "failed"
	StateSkipped = "skipped"
)

// Error codes a step can fail with; the empty string is ok.
const (
	ErrFetch        = "fetch"
	ErrHashMismatch = "hash_mismatch"
	ErrExit         = "exit"
	ErrTimeout      = "timeout"
	ErrRefusedBusy  = "refused_busy"
	ErrUnknownPlan  = "unknown_plan"
	ErrTransport    = "transport"
	ErrRollback     = "rollback_failed"
	ErrInterrupted  = "interrupted"
)

// Plan is one resolved update: what to fetch, what to run, and how to undo a half-applied swap.
type Plan struct {
	// ID names the profile that built it, e.g. "yandex-hotswap"
	ID string `json:"id"`
	// RunID is the server's ops run this plan belongs to
	RunID string `json:"runId"`
	Board string `json:"board"`
	// TargetVersion is the daemon version the device reports once the update took
	TargetVersion string `json:"targetVersion"`
	// Vars are the resolved variables, kept for the run's record
	Vars map[string]string `json:"vars"`
	// Steps run in order; Rollback restores the device when Steps failed past staging
	Steps    []Step `json:"steps"`
	Rollback []Step `json:"rollback,omitempty"`
	// Restart is true when the last steps restart the daemon: the run ends awaiting the device's
	// relink on TargetVersion, not a step answer
	Restart bool `json:"restart"`
}

// Step is one of the two primitives: Fetch or Exec, never both.
type Step struct {
	// Name labels the step in the run's record and logs
	Name  string     `json:"name"`
	Fetch *FetchStep `json:"fetch,omitempty"`
	Exec  *ExecStep  `json:"exec,omitempty"`
}

// FetchStep materializes one file of the artifact at Dst. Src is "artifact:<path>" for a member of
// a tar artifact, or "artifact:" for the artifact's whole blob (an apk, the rpi tarball).
type FetchStep struct {
	Src string `json:"src"`
	// Dst is the absolute device path, usually a staged *.new one
	Dst string `json:"dst"`
	// SHA256 the executor verifies after writing; a mismatch aborts the run
	SHA256 string `json:"sha256"`
	// Mode is the file's posix mode (493 is 0755); 0 leaves the transport's default
	Mode uint32 `json:"mode,omitempty"`
	Size int64  `json:"size,omitempty"`
	// SkipIfSame skips the fetch when the file already at Dst (without its staging suffix) has
	// SHA256: the daemon's own refinement, cheaper than a re-push over adb
	SkipIfSame bool `json:"skipIfSame,omitempty"`
}

// ExecStep runs Cmd through the board's shell. Exit is the wrapped command's status: adbd has no
// shell protocol, so executors that go through it wrap Cmd with a status marker of their own.
type ExecStep struct {
	Cmd string `json:"cmd"`
	// Expect is the exit status that counts as ok
	Expect int `json:"expect"`
	// Terminal marks the step that restarts the executor's host (the daemon kills itself, or the
	// app is force-stopped by pm install): its answer may never arrive, and that is not a failure
	Terminal bool `json:"terminal,omitempty"`
	// TimeoutMs caps the command; 0 is the executor's default
	TimeoutMs int64 `json:"timeoutMs,omitempty"`
}

// StepResult is one step's outcome in the run's record.
type StepResult struct {
	Index      int       `json:"index"`
	Name       string    `json:"name"`
	State      string    `json:"state"`
	ExitCode   int       `json:"exitCode,omitempty"`
	ErrorCode  string    `json:"errorCode,omitempty"`
	OutputTail string    `json:"outputTail,omitempty"`
	Skipped    bool      `json:"skipped,omitempty"`
	StartedAt  time.Time `json:"startedAt"`
	FinishedAt time.Time `json:"finishedAt"`
}

// Validate checks a resolved plan's shape before it is stored or sent.
func (p *Plan) Validate() error {
	if p.ID == "" || p.RunID == "" || p.Board == "" || p.TargetVersion == "" {
		return errors.New("opsplan: id, runId, board and targetVersion are required")
	}
	if len(p.Steps) == 0 {
		return errors.New("opsplan: no steps")
	}
	for i := range p.Steps {
		if err := p.Steps[i].validate(); err != nil {
			return fmt.Errorf("opsplan: step %d: %w", i, err)
		}
	}
	for i := range p.Rollback {
		if err := p.Rollback[i].validate(); err != nil {
			return fmt.Errorf("opsplan: rollback step %d: %w", i, err)
		}
	}
	return nil
}

func (s *Step) validate() error {
	if (s.Fetch == nil) == (s.Exec == nil) {
		return errors.New("exactly one of fetch and exec")
	}
	if s.Name == "" {
		return errors.New("name is required")
	}
	if f := s.Fetch; f != nil {
		if !strings.HasPrefix(f.Src, "artifact:") {
			return errors.New("fetch src must name the artifact (artifact: or artifact:<path>)")
		}
		if !strings.HasPrefix(f.Dst, "/") {
			return errors.New("fetch dst must be absolute")
		}
		if !sha256Re.MatchString(f.SHA256) {
			return errors.New("fetch sha256 must be 64 hex digits")
		}
	}
	return nil
}

var sha256Re = regexp.MustCompile(`^[0-9a-f]{64}$`)

// HasTerminal reports whether the plan's own last step restarts the executor's host.
func (p *Plan) HasTerminal() bool {
	for i := range p.Steps {
		if p.Steps[i].Exec != nil && p.Steps[i].Exec.Terminal {
			return true
		}
	}
	return false
}

// Marshal keeps the plan's stored and travelling form one shape.
func Marshal(p *Plan) []byte {
	b, err := json.Marshal(p)
	if err != nil {
		return nil
	}
	return b
}

// Unmarshal parses and validates a plan.
func Unmarshal(b []byte) (*Plan, error) {
	var p Plan
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, fmt.Errorf("opsplan: bad json: %w", err)
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return &p, nil
}
