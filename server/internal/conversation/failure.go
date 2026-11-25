package conversation

import "strings"

// stages of a turn that can fail. A step is meaningful for llm and tool.
const (
	StageSTT      = "stt"
	StageLLM      = "llm"
	StageTool     = "tool"
	StageTTS      = "tts"
	StageSetup    = "setup"
	StageInternal = "internal"
)

// TurnFailure is why a stored turn ended without its answer. Every string is
// an id or a code; provider error text is deliberately not part of the record.
type TurnFailure struct {
	Code       string `json:"code" doc:"the turn's failure code"`
	Stage      string `json:"stage" enum:"stt,llm,tool,tts,setup,internal" doc:"the part of the turn that failed"`
	Step       int    `json:"step,omitempty" doc:"the agent step that failed, starting at 1; absent before the agent loop"`
	Provider   string `json:"provider,omitempty" doc:"the provider instance's name, where the failed stage had one"`
	Model      string `json:"model,omitempty" doc:"the model id requested, when the llm stage failed"`
	Tool       string `json:"tool,omitempty" doc:"the tool whose call was running when the turn failed"`
	HTTPStatus int    `json:"httpStatus,omitempty" doc:"the upstream HTTP status, when the provider returned one; never its body"`
	WaitedMs   int64  `json:"waitedMs,omitempty" doc:"how long the failed call ran, in milliseconds"`
	LimitMs    int64  `json:"limitMs,omitempty" doc:"the time limit a timeout ran against, in milliseconds"`
}

// cleanTurnFailure is the privacy boundary for a failure that reaches the
// store, the admin API or a screen: no free-form error string survives it.
func cleanTurnFailure(f TurnFailure) TurnFailure {
	f.Code = codeToken(f.Code, 64)
	f.Stage = codeToken(f.Stage, 32)
	f.Provider = idToken(f.Provider, 128)
	f.Model = idToken(f.Model, 256)
	f.Tool = codeToken(f.Tool, 128)
	if f.Step < 0 || f.Step > 1_000_000 {
		f.Step = 0
	}
	if f.HTTPStatus < 100 || f.HTTPStatus > 599 {
		f.HTTPStatus = 0
	}
	f.WaitedMs = milliseconds(f.WaitedMs)
	f.LimitMs = milliseconds(f.LimitMs)
	return f
}

func milliseconds(ms int64) int64 {
	// a year bounds corrupt input without truncating any real turn
	if ms < 0 || ms > 365*24*60*60*1000 {
		return 0
	}
	return ms
}

func codeToken(s string, limit int) string {
	if s == "" || len(s) > limit {
		return ""
	}
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || strings.ContainsRune("_.-", r) {
			continue
		}
		return ""
	}
	return s
}

// model ids add slashes, colons, at signs and plus signs to a code's alphabet.
func idToken(s string, limit int) string {
	if s == "" || len(s) > limit {
		return ""
	}
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
			strings.ContainsRune("_.-/:+@", r) {
			continue
		}
		return ""
	}
	return s
}

func failurePtr(f *TurnFailure) *TurnFailure {
	if f == nil {
		return nil
	}
	clean := cleanTurnFailure(*f)
	if clean.Code == "" || clean.Stage == "" {
		return nil
	}
	return &clean
}

// SanitizeTurnFailure bounds a failure before it crosses the store, admin or
// screen boundary. Invalid free-form strings are dropped, never truncated into
// a misleading code.
func SanitizeTurnFailure(f *TurnFailure) *TurnFailure { return failurePtr(f) }
