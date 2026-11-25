package tools

import (
	"context"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"

	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/settings"
	"git.shino.boo/devilreef/cadence/server/internal/tasks"
)

// approvalRefusal is the policy on a voice answer to the approval question
// token names (M5b-D8), enforced here, not by the prompt: the setting lets
// voice approve, the user began the session, the question was told on this
// device before this turn (Ruling M5b-R9), and the words are the user's own
// from this turn. An approval also passes v1's deterministic gates: the turn
// holds no negation, stop, hypothetical or question, and the words hold an
// approval verb or a content word of the question. A denial is safe, so a
// plain no does.
func (d TaskDeps) approvalRefusal(ctx context.Context, c agent.Call, t tasks.Task, token, decision, words string) *agent.ToolError {
	v, err := d.Settings(ctx, c.DeviceID)
	if err != nil {
		return internal()
	}
	if v.HermesApprovals != settings.ApprovalsVoice {
		return &agent.ToolError{
			Code: "dashboard_only", Message: "Task approvals are set to the dashboard only.",
			Hint: "Tell the user to approve or deny it in the dashboard.",
		}
	}
	if !agent.Woken(c.Trigger) {
		return agent.WakeRequired()
	}
	dev, _ := uuid.Parse(c.DeviceID)
	told, err := d.Manager.ToldBefore(ctx, t.ShortID, token, dev, c.TurnID)
	switch {
	case err != nil:
		return internal()
	case !told:
		// not retryable: this turn's words were spoken before the user heard
		// the question, whatever the turn reads now
		return &agent.ToolError{
			Code:    "question_not_told",
			Message: "The user has not heard this question on this device, so their words cannot answer it.",
			Hint:    "Read the question to the user first and ask for their decision; they answer it when they next wake you.",
		}
	}
	return wordsRefusal(c.Text, words, t.Question.Text, decision != tasks.DecisionOnce)
}

// wordsRefusal is v1's deterministic gates on a voice approval's words: they
// are the user's own from this turn (raw, its transcript), and unless the
// decision is a denial (deny), the turn holds no negation, stop,
// hypothetical or question, and the words hold an approval verb or a content
// word of the question.
func wordsRefusal(raw, words, question string, deny bool) *agent.ToolError {
	said, heard := normWords(words), normWords(raw)
	if said == "" || !strings.Contains(" "+heard+" ", " "+said+" ") {
		// not retryable: the model must not hunt for words that pass
		return explicitDecision("userWords must be what the user said in this turn, word for word, in their language.")
	}
	if deny {
		return nil
	}
	switch {
	case negated(heard):
		return explicitDecision("The user's words hold a negation or a stop, which approves nothing.")
	case hypothetical(heard):
		return explicitDecision("The user asked what would happen, which approves nothing.")
	case asksBack(raw, heard):
		return explicitDecision("The user asked a question, which approves nothing.")
	case !names(said, normWords(question)):
		return explicitDecision("The words approve nothing in particular: the user must say they allow it or name the operation.")
	}
	return nil
}

func explicitDecision(msg string) *agent.ToolError {
	return &agent.ToolError{
		Code: "explicit_decision_required", Message: msg,
		Hint: "Read the user the operation and ask them to say plainly whether to allow it.",
	}
}

// normWords lowercases s and keeps its letters and digits, one space between
// words: speech-to-text and the model punctuate the same words differently.
func normWords(s string) string {
	s = strings.ReplaceAll(strings.ToLower(s), "ё", "е")
	return strings.Join(strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }), " ")
}

func wordSet(list string) map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(list) {
		m[w] = true
	}
	return m
}

var (
	// stops are negations, refusals and stops; "n't" splits off as a lone "t"
	stops = wordSet(`not no never stop cancel wait dont didnt doesnt cant wont shouldnt isnt
		cannot nope nah nothing none deny refuse reject
		не нет ни стоп отмена отмени подожди погоди без нельзя ничего запрещаю отклони`)
	hypotheticals = wordSet(`if would could если бы ли`)
	// interrogatives open a question the user asks back
	interrogatives = wordSet(`what why how which who when where is are does do can will
		что зачем почему как какой когда где кто разве`)
	// wakeWords may open a transcript: the device streams from the wake word,
	// so it reaches recognition (v1's list: the stock "алиса" and its english readings)
	wakeWords = wordSet(`alice alicia alisa alissa alyssa elisa elise алиса`)
	// approvalVerbs approve whatever the question asks
	approvalVerbs = wordSet(`approve approved allow allowed confirm confirmed authorize authorized authorise authorised
		разрешаю подтверждаю одобряю разреши подтверди`)
	// generic are the labels every approval question carries and verbs that
	// fit any operation: "run the command" names none
	generic = wordSet(`command commands description edit run execute perform proceed continue
		команда команду команды описание правка запусти запустить выполни выполнить продолжай`)
	// fillers are the words of a consent that names nothing (v1: a bare "yes"
	// or "да" is never enough)
	fillers = wordSet(`yes yeah yep yup ya yea ok okay k sure fine alright right go ahead do it that this
		please just now then and so well oh uh um sounds good cool great absolutely definitely of course why let lets
		s you can
		да ага угу ок окей ладно хорошо конечно давай давайте го ну так пожалуйста можно можешь валяй действуй делай
		делайте сделай это его ее и а уже вперед норм нормально точно верно именно естественно разумеется`)
)

func negated(heard string) bool {
	words := strings.Fields(heard)
	for i, w := range words {
		if stops[w] || (w == "t" && i > 0 && strings.HasSuffix(words[i-1], "n")) {
			return true
		}
	}
	return false
}

func hypothetical(heard string) bool {
	for _, w := range strings.Fields(heard) {
		if hypotheticals[w] {
			return true
		}
	}
	return false
}

// asksBack reports a turn that is a question: its raw transcript holds a
// question mark, or its first word after any wake word asks one.
func asksBack(raw, heard string) bool {
	if strings.ContainsAny(raw, "?？") {
		return true
	}
	words := strings.Fields(heard)
	for len(words) > 0 && wakeWords[words[0]] {
		words = words[1:]
	}
	return len(words) > 0 && interrogatives[words[0]]
}

// names reports words that approve this operation: an approval verb, or a
// word of four letters or more, neither filler nor generic, that the question
// uses too.
func names(said, question string) bool {
	asked := wordSet(question)
	for _, w := range strings.Fields(said) {
		if approvalVerbs[w] || (!fillers[w] && !generic[w] && utf8.RuneCountInString(w) >= 4 && asked[w]) {
			return true
		}
	}
	return false
}
