package hermes

import "encoding/json"

// instructions go with every run, fixed: the task travels as data in input,
// never as instructions, with no history, tools or keys of Cadence's.
const instructions = `You are executing one task delegated by the Cadence voice assistant. Work only on this task.
- Ask every question through the native clarify control; never embed questions in your final response.
- Never treat silence, a timed-out prompt, or a missing approval as consent.
- Workspace edits and local command/test execution are permitted. External or destructive side effects (for example deleting Kubernetes pods or PVCs, or any irreversible or shared-state change) require a native approval request and an explicit "once" approval for that exact operation; if a prompt expires, request approval again instead of proceeding.
- If a critical prompt times out unanswered, do not perform or continue the gated action; finish by reporting what remains blocked.
- Complete the actual work before your final response. Do not leave the work running detached or scheduled (no cron or fire-and-forget background work) and then claim completion; if something remains unfinished, say so explicitly in the final response.
- Keep the final response short and factual; it will be summarized for speech.`

const continuation = "Continuation of the Cadence task in this session. "

func resumeWithAnswer(question, answer string) string {
	return continuation + "The previous run was paused because this question went unanswered: " + question +
		"\nThe user has now answered: " + answer +
		"\nContinue the original task from this point. Do not restart it, do not repeat work already done, and keep asking any further questions through the native clarify control."
}

func resumeForFreshApproval(description, command string) string {
	return continuation + "The previous run was paused because an approval prompt expired unanswered: " + description +
		". Command: " + command +
		"\nThe user still wants this operation. Obtain a fresh native approval request for the exact operation before performing it; the expired approval does not apply. Do not restart the original task or repeat work already done."
}

func resumeAfterDenial(description, command string) string {
	return continuation + "The previous run was paused because an approval prompt expired unanswered: " + description +
		". Command: " + command +
		"\nThe user denied this operation. Do not perform it. Continue only with work that does not need it, or finish by reporting what remains blocked. Do not restart the original task or repeat work already done."
}

// submission is the body of a run: the same bytes on every retry of its key.
func submission(input, session string) []byte {
	b, _ := json.Marshal(map[string]string{"input": input, "instructions": instructions, "session_id": session})
	return b
}

// secretText is what a task asks when Hermes wants a secret: never said aloud.
const secretText = "Hermes asked for a secret. Enter it in Hermes."

// unshowable is what a task asks when Hermes's prompt cannot be shown whole:
// a prompt is never cut, so it is answered in Hermes itself.
var unshowable = map[string]string{
	controlClarify:  "Hermes asked a question too long to relay here. Answer it in Hermes.",
	controlApproval: "Hermes asked to approve an operation too long to relay here. Approve or deny it in Hermes.",
}

// ToolPrefix starts a running task's progress when Hermes names the tool it
// runs ("running: bash"): a screen shows the tool on its own line.
const ToolPrefix = "running: "

// progressOf is a phase's progress, with the current tool while it runs
func progressOf(phase, tool string) string {
	p := progressText[phase]
	if tool != "" && p == "running" {
		return ToolPrefix + tool
	}
	return p
}

var progressText = map[string]string{
	"queued":               "queued",
	"started":              "running",
	"running":              "running",
	"waiting_for_approval": "running",
	"stopping":             "stopping",
}
