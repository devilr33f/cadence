package hermes

import (
	"bytes"
	"encoding/json"
	"math"
	"regexp"
	"slices"
	"time"
	"unicode/utf8"
)

// Run is one poll of a run. Status is running, completed, failed, cancelled or
// interrupted; Phase is the server's own word (queued, waiting_for_approval, …).
type Run struct {
	ID, Status, Phase, SessionID, Output string
	Controls                             []Control
	// Tool is the newest open tool call's name, "" between tools or when
	// Hermes does not say (its session authority's poll carries it, W0-C item
	// 9b stage 1); ProgressAt is that call's last frame, zero when unknown
	Tool       string
	ProgressAt time.Time
}

// toolNameRe is a tool name a screen may show as the task's current tool
var toolNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,63}$`)

// Control is a prompt a run waits on. Its JSON form is canonical: string
// equality of two encodings is payload identity.
type Control struct {
	Kind        string   `json:"kind"`
	PromptID    string   `json:"promptId"`
	Generation  int64    `json:"generation"`
	Question    string   `json:"question,omitempty"`
	Choices     []string `json:"choices"`
	MultiSelect *bool    `json:"multiSelect,omitempty"`
	Command     string   `json:"command,omitempty"`
	Description string   `json:"description,omitempty"`
	Edit        string   `json:"edit,omitempty"`
	TimeoutS    float64  `json:"timeoutS,omitempty"`
}

const (
	controlClarify  = "clarify"
	controlApproval = "approval"

	statusRunning     = "running"
	statusCompleted   = "completed"
	statusFailed      = "failed"
	statusCancelled   = "cancelled"
	statusInterrupted = "interrupted"
)

var (
	runStatuses      = []string{"queued", "running", "waiting_for_approval", "stopping", "completed", "failed", "cancelled", "interrupted", "unknown"}
	acceptedStatuses = append([]string{"started"}, runStatuses...)
	stopStatuses     = runStatuses[:len(runStatuses)-1]
)

func runes(s string, lo, hi int) bool {
	n := utf8.RuneCountInString(s)
	return n >= lo && n <= hi
}

func isTrue(raw json.RawMessage) bool { return bytes.Equal(bytes.TrimSpace(raw), []byte("true")) }

func absentOrTrue(raw json.RawMessage) bool { return raw == nil || isTrue(raw) }

func capabilitiesOK(body []byte) bool {
	var c struct {
		Features *struct {
			RunClarify           json.RawMessage `json:"run_clarify"`
			RunClarifyResponse   json.RawMessage `json:"run_clarify_response"`
			RunApprovalResponse  json.RawMessage `json:"run_approval_response"`
			RunUnknownResolution json.RawMessage `json:"run_unknown_resolution"`
			RunsIdempotency      *struct {
				Supported json.RawMessage `json:"supported"`
				Durable   json.RawMessage `json:"durable"`
			} `json:"runs_idempotency"`
		} `json:"features"`
		Endpoints map[string]json.RawMessage `json:"endpoints"`
	}
	if body == nil || json.Unmarshal(body, &c) != nil || c.Features == nil || c.Features.RunsIdempotency == nil || c.Endpoints == nil {
		return false
	}
	endpoint := func(name, method, path string) bool {
		var e map[string]any
		if json.Unmarshal(c.Endpoints[name], &e) != nil || e == nil {
			return false
		}
		return e["method"] == method && e["path"] == path
	}
	f := c.Features
	// the clarify route may go unlisted, but a listed one must be the canonical one
	_, clarifyListed := c.Endpoints["run_clarify"]
	return endpoint("runs", "POST", "/v1/runs") &&
		endpoint("run_status", "GET", "/v1/runs/{run_id}") &&
		endpoint("run_stop", "POST", "/v1/runs/{run_id}/stop") &&
		endpoint("run_approval", "POST", "/v1/runs/{run_id}/approval") &&
		isTrue(f.RunUnknownResolution) && isTrue(f.RunsIdempotency.Supported) && isTrue(f.RunsIdempotency.Durable) &&
		absentOrTrue(f.RunClarify) && absentOrTrue(f.RunClarifyResponse) && absentOrTrue(f.RunApprovalResponse) &&
		(!clarifyListed || endpoint("run_clarify", "POST", "/v1/runs/{run_id}/clarify"))
}

func parseAccepted(body []byte) (string, bool) {
	var a struct {
		RunID  *string `json:"run_id"`
		Status *string `json:"status"`
	}
	if body == nil || json.Unmarshal(body, &a) != nil || a.RunID == nil || a.Status == nil {
		return "", false
	}
	return *a.RunID, runes(*a.RunID, 1, 256) && slices.Contains(acceptedStatuses, *a.Status)
}

func parseStopReceipt(body []byte, run string) (string, bool) {
	var r struct {
		Status *string `json:"status"`
		RunID  *string `json:"run_id"`
	}
	if body == nil || json.Unmarshal(body, &r) != nil || r.Status == nil || !slices.Contains(stopStatuses, *r.Status) {
		return "", false
	}
	return *r.Status, r.RunID == nil || *r.RunID == run
}

// parseControlReceipt reports whether the answer resolved the prompt; a
// receipt for another run or prompt is not ok.
func parseControlReceipt(body []byte, run, prompt string) (resolved, ok bool) {
	var r struct {
		Status   *string `json:"status"`
		PromptID *string `json:"prompt_id"`
		RunID    *string `json:"run_id"`
	}
	if body == nil || json.Unmarshal(body, &r) != nil || r.Status == nil ||
		(*r.Status != "resolved" && *r.Status != "already_resolved") ||
		(r.PromptID != nil && *r.PromptID != prompt) || (r.RunID != nil && *r.RunID != run) {
		return false, false
	}
	return *r.Status == "resolved", true
}

func parseRun(body []byte, id string) (Run, error) {
	var r struct {
		RunID           *string           `json:"run_id"`
		Status          *string           `json:"status"`
		SessionID       *string           `json:"session_id"`
		Output          *string           `json:"output"`
		PendingControls []json.RawMessage `json:"pending_controls"`
		Completed       *bool             `json:"completed"`
		Partial         *bool             `json:"partial"`
		Tool            json.RawMessage   `json:"tool"`
		ProgressAt      json.RawMessage   `json:"progress_at"`
	}
	if body == nil || json.Unmarshal(body, &r) != nil || r.RunID == nil || *r.RunID != id || r.Status == nil ||
		!slices.Contains(runStatuses, *r.Status) || (r.SessionID != nil && !runes(*r.SessionID, 1, 256)) ||
		len(r.PendingControls) > 64 {
		return Run{}, fail(CodeIncompatible)
	}
	run := Run{ID: id, Phase: *r.Status}
	if r.SessionID != nil {
		run.SessionID = *r.SessionID
	}
	if r.Output != nil {
		run.Output = *r.Output
	}
	// optional and read-only: a value of another shape is ignored, never a refusal
	var tool string
	if len(r.Tool) > 0 && json.Unmarshal(r.Tool, &tool) == nil && toolNameRe.MatchString(tool) {
		run.Tool = tool
	}
	var at float64
	if len(r.ProgressAt) > 0 && json.Unmarshal(r.ProgressAt, &at) == nil && at > 0 && at < 1e11 {
		sec, frac := math.Modf(at)
		run.ProgressAt = time.Unix(int64(sec), int64(frac*1e9)).UTC()
	}
	for _, raw := range r.PendingControls {
		c, ok := parseControl(raw)
		if !ok {
			return Run{}, fail(CodeIncompatible)
		}
		run.Controls = append(run.Controls, c)
	}
	switch run.Phase {
	case "unknown":
		return Run{}, fail(CodeUnknown)
	case "queued", "waiting_for_approval", "stopping", statusRunning:
		run.Status = statusRunning
	case statusCompleted:
		// a completion that says it is partial is no completion
		if (r.Completed != nil && !*r.Completed) || (r.Partial != nil && *r.Partial) {
			return Run{}, fail(CodeIncompatible)
		}
		run.Status = statusCompleted
	default:
		run.Status = run.Phase
	}
	return run, nil
}

func validChoices(c []string, lo int) bool {
	if len(c) < lo || len(c) > 64 {
		return false
	}
	for _, s := range c {
		if !runes(s, 0, 512) {
			return false
		}
	}
	return true
}

// parseControl takes every field it uses, validated; unknown fields are dropped.
func parseControl(raw json.RawMessage) (Control, bool) {
	var r struct {
		Kind        string          `json:"kind"`
		PromptID    *string         `json:"prompt_id"`
		Generation  *float64        `json:"execution_generation"`
		TimeoutS    *float64        `json:"timeout_s"`
		Question    *string         `json:"question"`
		Choices     *[]string       `json:"choices"`
		MultiSelect *bool           `json:"multi_select"`
		Command     *string         `json:"command"`
		Description *string         `json:"description"`
		Edit        json.RawMessage `json:"edit"`
	}
	if json.Unmarshal(raw, &r) != nil || r.PromptID == nil || !runes(*r.PromptID, 1, 256) ||
		r.Generation == nil || *r.Generation < 0 || *r.Generation > 1<<53 || *r.Generation != math.Trunc(*r.Generation) ||
		(r.TimeoutS != nil && (*r.TimeoutS <= 0 || *r.TimeoutS > 86400)) || r.Choices == nil {
		return Control{}, false
	}
	c := Control{Kind: r.Kind, PromptID: *r.PromptID, Generation: int64(*r.Generation), Choices: *r.Choices}
	if r.TimeoutS != nil {
		c.TimeoutS = *r.TimeoutS
	}
	switch r.Kind {
	case controlClarify:
		if r.Question == nil || !runes(*r.Question, 1, 8192) || !validChoices(c.Choices, 0) || r.MultiSelect == nil {
			return Control{}, false
		}
		c.Question, c.MultiSelect = *r.Question, r.MultiSelect
	case controlApproval:
		if r.Command == nil || r.Description == nil || !runes(*r.Command, 0, 4096) || !runes(*r.Description, 0, 4096) || !validChoices(c.Choices, 1) {
			return Control{}, false
		}
		c.Command, c.Description = *r.Command, *r.Description
		if r.Edit != nil {
			c.Edit = editText(r.Edit)
		}
	default:
		return Control{}, false
	}
	return c, true
}

func editText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var b bytes.Buffer
	if json.Compact(&b, raw) != nil {
		return string(raw)
	}
	return b.String()
}

// canonical is the payload identity of a control.
func (c Control) canonical() string {
	b, _ := json.Marshal(c)
	return string(b)
}

func (c Control) same(o Control) bool {
	return c.Kind == o.Kind && c.PromptID == o.PromptID && c.Generation == o.Generation
}
