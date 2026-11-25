package t3code

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/subscriptions"
)

// ModuleName is the module's name in the provider catalog and in topics.
const ModuleName = "t3code"

// KindThread names a thread in the hub's spoken templates.
const KindThread = "thread"

// idRe is the T3 id a topic segment and a hub ref take: T3's own ids are
// uuid-like, so anything else (a hostile server's id carrying words) is
// left out rather than escaped into a topic.
var idRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

func validID(id string) bool { return idRe.MatchString(id) }

// ValidID reports an id Cadence takes from T3 (a topic segment, a tool's
// answer); others are left out.
func ValidID(id string) bool { return validID(id) }

// HubToken is the hub's token for a T3 token (<requestId>.<revision>, which
// the hub's charset refuses): 16 bytes of its SHA-256 in base64url, 22
// characters of [A-Za-z0-9_-]. The same prompt has the same token across
// reconnects and restarts, so an answer finds its request again from the
// thread's projection alone.
func HubToken(t3Token string) string {
	h := sha256.Sum256([]byte(t3Token))
	return base64.RawURLEncoding.EncodeToString(h[:16])
}

// ThreadTopic is a thread's topic, t3code:<instance>:thread:<id>.
func ThreadTopic(instance, threadID string) subscriptions.Topic {
	return subscriptions.NewTopic(ModuleName, instance, "thread", threadID)
}

// projectTopic is a thread's place under its project, which
// t3code:<instance>:project:<id>:* matches.
func projectTopic(instance, projectID, threadID string) subscriptions.Topic {
	return subscriptions.NewTopic(ModuleName, instance, "project", projectID, "thread", threadID)
}

// topicsOf are the topics an update of a thread is matched on.
func topicsOf(instance, projectID, threadID string) (subscriptions.Topic, []subscriptions.Topic) {
	var also []subscriptions.Topic
	if validID(projectID) {
		also = append(also, projectTopic(instance, projectID, threadID))
	}
	return ThreadTopic(instance, threadID), also
}

// hubUpdate is the hub's update for one of a thread's: resolved becomes a
// withdrawal alone. false for a thread whose id a topic cannot carry.
func hubUpdate(instance, projectID string, u Update, at time.Time) (subscriptions.Update, bool) {
	if !validID(u.ThreadID) {
		return subscriptions.Update{}, false
	}
	topic, also := topicsOf(instance, projectID, u.ThreadID)
	out := subscriptions.Update{
		Topic: topic, Also: also, Kind: KindThread, Title: u.Title, Ref: subscriptions.Ref{ID: u.ThreadID}, At: at,
	}
	switch u.Kind {
	case KindStopped:
		out.Event, out.Text = subscriptions.EventStopped, u.Text
	case KindError:
		out.Event, out.Text = subscriptions.EventError, u.Text
	case KindQuestion:
		out.Event, out.Question = subscriptions.EventQuestion, clarify(u)
	case KindApproval:
		out.Event, out.Question = subscriptions.EventApproval, approval(u)
	case KindResolved:
		out.Withdraw = true
	default:
		return subscriptions.Update{}, false
	}
	if (u.Kind == KindQuestion || u.Kind == KindApproval) && out.Question == nil {
		return subscriptions.Update{}, false
	}
	return out, true
}

func approval(u Update) *subscriptions.Question {
	if u.Approval == nil || u.Token == "" {
		return nil
	}
	text := u.Approval.Prompt
	if r := strings.TrimSpace(u.Approval.Reason); r != "" {
		text = strings.TrimSpace(text + " (" + r + ")")
	}
	return &subscriptions.Question{Kind: subscriptions.QuestionApproval, Token: HubToken(u.Token), Text: text}
}

// clarify is a user input request as one hub question. One T3 question keeps
// its options as ids "1", "2", …; several are put together: their texts
// numbered, their options "<question>.<option>" (one per question, several
// where it takes several), no free answer.
func clarify(u Update) *subscriptions.Question {
	if len(u.Questions) == 0 || u.Token == "" {
		return nil
	}
	q := &subscriptions.Question{Kind: subscriptions.QuestionClarify, Token: HubToken(u.Token)}
	if len(u.Questions) == 1 {
		one := u.Questions[0]
		q.Text, q.Multi, q.AllowCustom = one.Question, one.MultiSelect, one.CustomAllowed()
		for i, o := range one.Options {
			q.Options = append(q.Options, subscriptions.Option{ID: strconv.Itoa(i + 1), Label: o.Label})
		}
		return q
	}
	texts := make([]string, len(u.Questions))
	for i, one := range u.Questions {
		texts[i] = strconv.Itoa(i+1) + ". " + one.Question
		for j, o := range one.Options {
			label := o.Label
			if one.Header != "" {
				label = one.Header + ": " + label
			}
			q.Options = append(q.Options, subscriptions.Option{ID: strconv.Itoa(i+1) + "." + strconv.Itoa(j+1), Label: label})
		}
	}
	q.Text, q.Multi = strings.Join(texts, " "), true
	return q
}

// errAnswerShape is an answer that fits the hub's question but not T3's
// (a question of several left unanswered): the hub refuses it.
var errAnswerShape = errors.New("t3code: the answer does not fit the request")

// optionValue is what T3 takes for a chosen option: its value, else its label.
func optionValue(o QuestionOption) string {
	if o.Value != "" {
		return o.Value
	}
	return o.Label
}

// answersFor maps a hub answer to T3's answers by question id (a string, or
// a list for a multi-select question), and the same as one message's text
// for a request answered by message.
func answersFor(qs []Question, a subscriptions.Answer) (map[string]any, string, error) {
	if len(qs) == 0 {
		return nil, "", errAnswerShape
	}
	chosen := make([][]string, len(qs))
	if msg := strings.TrimSpace(a.Message); msg != "" {
		if len(qs) != 1 || !qs[0].CustomAllowed() {
			return nil, "", errAnswerShape
		}
		chosen[0] = []string{msg}
	} else {
		for _, id := range a.Answers {
			qi, oi, ok := optionIndex(id, len(qs))
			if !ok || oi >= len(qs[qi].Options) {
				return nil, "", errAnswerShape
			}
			chosen[qi] = append(chosen[qi], optionValue(qs[qi].Options[oi]))
		}
	}
	out := map[string]any{}
	var lines []string
	for i, q := range qs {
		got := chosen[i]
		if len(got) == 0 || len(got) > 1 && !q.MultiSelect {
			return nil, "", errAnswerShape
		}
		if q.MultiSelect {
			out[q.ID] = got
		} else {
			out[q.ID] = got[0]
		}
		line := strings.Join(got, ", ")
		if len(qs) > 1 {
			head := q.Header
			if head == "" {
				head = q.Question
			}
			line = head + ": " + line
		}
		lines = append(lines, line)
	}
	return out, strings.Join(lines, "\n"), nil
}

// optionIndex reads an option id clarify made: "<option>" for one question,
// "<question>.<option>" for several, 1-based.
func optionIndex(id string, questions int) (qi, oi int, ok bool) {
	if questions == 1 {
		n, err := strconv.Atoi(id)
		return 0, n - 1, err == nil && n >= 1
	}
	a, b, found := strings.Cut(id, ".")
	if !found {
		return 0, 0, false
	}
	q, err1 := strconv.Atoi(a)
	o, err2 := strconv.Atoi(b)
	if err1 != nil || err2 != nil || q < 1 || q > questions || o < 1 {
		return 0, 0, false
	}
	return q - 1, o - 1, true
}

// decisionFor maps the hub's approval answer to T3's decision.
func decisionFor(d string) (string, bool) {
	switch d {
	case subscriptions.DecisionOnce:
		return DecisionAccept, true
	case subscriptions.DecisionSession:
		return DecisionAcceptForSession, true
	case subscriptions.DecisionDeny:
		return DecisionDecline, true
	}
	return "", false
}
