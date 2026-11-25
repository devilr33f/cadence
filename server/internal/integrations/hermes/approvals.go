package hermes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"git.shino.boo/devilreef/cadence/server/internal/tasks"
)

// secretQuestion is v1's filter: a clarify prompt that asks for a secret is
// never relayed, so nobody is asked to say one aloud.
var secretQuestion = regexp.MustCompile(`(?i)password|passphrase|api[- ]?key|secret|token|private[ -]?key|credential|` +
	`парол|токен|секрет|приватн\pL*\s+ключ|ключ\s+доступа`)

// token names a prompt's identity and payload, so an answer lands only on the
// prompt its giver saw.
func token(c Control) string {
	sum := sha256.Sum256([]byte(c.canonical()))
	return hex.EncodeToString(sum[:12])
}

// question is what a task asks for a prompt, and the code that refuses an
// answer from Cadence ("" when it may be answered). An approval shows the
// exact operation with the only answers Cadence ever gives, once or deny; a
// prompt is never cut, and one that asks for a secret is never relayed.
func question(c Control) (tasks.Question, string) {
	q := tasks.Question{Kind: tasks.QuestionClarify, Text: strings.TrimSpace(c.Question), Token: token(c)}
	switch {
	case c.Kind == controlApproval:
		q.Kind, q.Text, q.Options = tasks.QuestionApproval, approvalText(c), []string{tasks.DecisionOnce, tasks.DecisionDeny}
	case secretQuestion.MatchString(c.Question):
		return tasks.Question{Kind: q.Kind, Text: secretText, Token: q.Token}, CodeSecretPrompt
	case len(c.Choices) > 0:
		q.Options = append([]string(nil), c.Choices...)
	}
	if !fits(q) {
		return tasks.Question{Kind: q.Kind, Text: unshowable[c.Kind], Token: q.Token}, CodePromptUnshowable
	}
	return q, ""
}

func approvalText(c Control) string {
	desc, cmd := strings.TrimSpace(c.Description), strings.TrimSpace(c.Command)
	switch {
	case desc != "" && cmd != "":
		return desc + "\nCommand: " + cmd
	case cmd != "":
		return "Command: " + cmd
	}
	return desc
}

func fits(q tasks.Question) bool {
	if n := utf8.RuneCountInString(q.Text); n < 1 || n > tasks.MaxQuestionText || len(q.Options) > tasks.MaxOptions ||
		strings.ContainsRune(q.Text, 0) {
		return false
	}
	for _, o := range q.Options {
		if utf8.RuneCountInString(o) > tasks.MaxOption {
			return false
		}
	}
	return true
}

func find(live []Control, c Control) (Control, bool) {
	for _, l := range live {
		if l.same(c) {
			return l, true
		}
	}
	return Control{}, false
}

// reconcile matches the run's prompts with the task's question. A shown
// prompt that left or changed takes the question away first; a new one shows
// on a later poll, so an answer meant for the old prompt never meets the new.
func (k *Kind) reconcile(j *job, run Run) {
	now := k.now()
	for key := range j.answered {
		gone := true
		for _, c := range run.Controls {
			if c.canonical() == key {
				gone = false
				break
			}
		}
		if gone {
			delete(j.answered, key)
		}
	}
	if j.shown != nil {
		if live, ok := find(run.Controls, *j.shown); ok && live.canonical() == j.shown.canonical() {
			return
		}
		j.shown, j.refusal, j.quiet, j.phase, j.tool = nil, "", now.Add(k.d.PollEvery), run.Phase, run.Tool
		k.save(j)
		k.emit(tasks.Event{Ref: j.session, Kind: "progress", Progress: progressOf(run.Phase, run.Tool)})
		k.log.Info("hermes_control_withdrawn", "task", j.task.String())
		return
	}
	if now.Before(j.quiet) {
		return
	}
	for _, c := range run.Controls {
		if !j.answered[c.canonical()] {
			k.show(j, c, now)
			return
		}
	}
	if run.Phase != j.phase || run.Tool != j.tool {
		j.phase, j.tool = run.Phase, run.Tool
		k.emit(tasks.Event{Ref: j.session, Kind: "progress", Progress: progressOf(run.Phase, run.Tool)})
	}
}

func (k *Kind) show(j *job, c Control, now time.Time) {
	q, refusal := question(c)
	j.shown, j.refusal, j.seen = &c, refusal, now
	k.save(j)
	k.emit(tasks.Event{Ref: j.session, Kind: "waiting_input", Question: &q})
	if refusal == CodeSecretPrompt {
		k.log.Warn("hermes_secret_prompt", "task", j.task.String())
	}
	k.log.Info("hermes_control_shown", "task", j.task.String(), "kind", c.Kind, "refusal", refusal)
}

// Send answers the clarify prompt token names; a paused task resumes with the
// answer in a new run of the same session. It never answers an approval:
// that takes Approve.
func (k *Kind) Send(ctx context.Context, ref, tok, msg string) error {
	if strings.TrimSpace(msg) == "" {
		return fail(CodeBadInput)
	}
	return k.withPrompt(ctx, ref, tok, CodeNoQuestion, func(j *job) error {
		switch {
		case j.shown.Kind == controlApproval:
			return fail(CodeApprovalPending)
		case j.refusal != "":
			return fail(j.refusal)
		case j.state == statePaused:
			return k.resume(ctx, j, resumeWithAnswer(j.shown.Question, msg))
		}
		return k.answer(ctx, j, func(c Control) error {
			return k.d.Client.Clarify(ctx, j.run, c.PromptID, c.Generation, msg)
		})
	})
}

// Approve answers the approval prompt token names with once or deny, for that
// exact prompt only; the caller has made the policy checks. An expired
// approval is never replayed: the task resumes in a new run that asks afresh
// (once) or is told the operation was denied.
func (k *Kind) Approve(ctx context.Context, ref, tok, decision string) error {
	if decision != tasks.DecisionOnce && decision != tasks.DecisionDeny {
		return fail(CodeBadDecision)
	}
	return k.withPrompt(ctx, ref, tok, CodeNoApproval, func(j *job) error {
		switch {
		case j.shown.Kind != controlApproval:
			return fail(CodeNoApproval)
		case j.refusal != "":
			return fail(j.refusal)
		case j.state == statePaused && decision == tasks.DecisionOnce:
			return k.resume(ctx, j, resumeForFreshApproval(j.shown.Description, j.shown.Command))
		case j.state == statePaused:
			return k.resume(ctx, j, resumeAfterDenial(j.shown.Description, j.shown.Command))
		}
		return k.answer(ctx, j, func(c Control) error {
			return k.d.Client.Approve(ctx, j.run, c.PromptID, c.Generation, decision)
		})
	})
}

// withPrompt runs fn under j's lock once tok still names the prompt shown.
func (k *Kind) withPrompt(ctx context.Context, ref, tok, none string, fn func(j *job) error) error {
	j, err := k.job(ctx, ref)
	if err != nil {
		return err
	}
	if err := j.acquire(ctx); err != nil {
		return err
	}
	defer j.release()
	switch {
	case j.state == stateClosed, j.shown == nil:
		err = fail(none)
	case token(*j.shown) != tok:
		err = fail(CodeStale)
	case j.state == statePausing, j.state == stateStopping:
		err = fail(CodeBusy)
	default:
		err = fn(j)
	}
	if err != nil {
		k.log.Warn("hermes_answer_refused", "task", j.task.String(), "code", codeOf(err))
	}
	return err
}

// answer posts to the exact prompt the user saw, after a fresh poll shows
// it is still live with the same identity and payload.
func (k *Kind) answer(ctx context.Context, j *job, post func(Control) error) error {
	run, err := k.d.Client.GetRun(ctx, j.run)
	if err != nil {
		return asCode(err)
	}
	live, ok := find(run.Controls, *j.shown)
	if run.Status != statusRunning || !ok || live.canonical() != j.shown.canonical() {
		return fail(CodeStale)
	}
	if err := post(*j.shown); err != nil {
		return asCode(err)
	}
	j.answered[j.shown.canonical()] = true
	k.log.Info("hermes_control_answered", "task", j.task.String(), "kind", j.shown.Kind)
	j.shown, j.refusal, j.quiet = nil, "", k.now().Add(k.d.PollEvery)
	k.save(j)
	k.emit(tasks.Event{Ref: j.session, Kind: "progress", Progress: progressText[run.Phase]})
	return nil
}

// resume starts a new run in the task's session; the paused run's prompt is
// answered by its input, never replayed.
func (k *Kind) resume(ctx context.Context, j *job, input string) error {
	gen := max(j.gen, j.tried) + 1
	j.tried = gen
	// recorded first: a failed attempt's key is never used again, restarts included
	k.save(j)
	run, err := k.submit(ctx, j, input, gen)
	if err != nil {
		return err
	}
	prevRun, prevGen, prevShown := j.run, j.gen, j.shown
	j.run, j.gen, j.state, j.shown, j.refusal = run, gen, stateActive, nil, ""
	if err := k.persist(j); err != nil {
		// unrecorded, the new run would go unwatched after a restart: stop it
		k.stopUnwatched(run)
		j.run, j.gen, j.state, j.shown = prevRun, prevGen, statePaused, prevShown
		return err
	}
	j.answered, j.quiet, j.phase, j.stopSent = map[string]bool{}, k.now().Add(k.d.PollEvery), "", false
	j.failures, j.failingSince = 0, time.Time{}
	k.log.Info("hermes_task_resumed", "task", j.task.String(), "gen", gen)
	k.emit(tasks.Event{Ref: j.session, Kind: "progress", Progress: progressText["queued"]})
	k.follow(j)
	return nil
}
