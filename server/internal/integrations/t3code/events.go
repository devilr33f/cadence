package t3code

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
)

// normalized event kinds: the subscription hub's shared names, plus resolved
// for a question or approval that is no longer open (answered elsewhere,
// expired, cancelled), so the hub can withdraw it.
const (
	KindStopped  = "stopped"
	KindQuestion = "question"
	KindApproval = "approval"
	KindError    = "error"
	KindResolved = "resolved"
)

// Approval is a pending approval's prompt: what T3 shows on its card.
type Approval struct {
	RequestKind string
	Prompt      string
	Reason      string
	Options     []ApprovalOption
}

// Update is one normalized event of a thread. Question, Approval and the
// texts are outside content: they may reach TTS and the dashboard, never a
// log.
type Update struct {
	Kind     string
	ThreadID string
	Title    string
	RunID    string
	// Status is the run's for stopped (completed, interrupted, failed,
	// cancelled, rolled_back)
	Status string
	// Text is the run's last assistant message for stopped, the failure's
	// message for error
	Text string
	// RequestID and Token for question, approval and resolved
	RequestID string
	Token     string
	Questions []Question
	Approval  *Approval
	// ErrorClass and ErrorCode for error
	ErrorClass string
	ErrorCode  string
}

// Token ties an answer to the prompt the user heard: T3's request id, a dot,
// and a revision hashing the prompt's content, so a prompt changed since
// (new questions or options) no longer matches. It is stable across
// reconnects and snapshots.
func Token(requestID string, item TurnItem) string {
	var content any
	switch item.Type {
	case ItemUserInputRequest:
		content = item.Questions
	case ItemApprovalRequest:
		content = []any{item.RequestKind, item.Prompt, item.Reason, item.Options}
	}
	b, _ := json.Marshal(content)
	h := sha256.Sum256(b)
	return requestID + "." + hex.EncodeToString(h[:6])
}

// SplitToken is a token's request id and revision.
func SplitToken(token string) (requestID, revision string, ok bool) {
	i := strings.LastIndexByte(token, '.')
	if i <= 0 || i == len(token)-1 {
		return "", "", false
	}
	return token[:i], token[i+1:], true
}

// Tracker turns one thread's stream (snapshots and events) into Updates. A
// snapshot sets the baseline: runs that already ended and errors already in
// the timeline are not told again, while open questions and approvals are
// (each token once). Not safe for concurrent use: feed it from the
// subscription's handler.
type Tracker struct {
	threadID string
	title    string
	runs     map[string]string // run id -> status
	requests map[string]RuntimeRequest
	prompts  map[string]TurnItem // request id -> its request item
	errors   map[string]bool     // error turn item ids seen
	lastText map[string]string   // run id -> last finished assistant text
	told     map[string]bool     // tokens told
}

func NewTracker(threadID string) *Tracker {
	return &Tracker{threadID: threadID, runs: map[string]string{}, requests: map[string]RuntimeRequest{},
		prompts: map[string]TurnItem{}, errors: map[string]bool{}, lastText: map[string]string{}, told: map[string]bool{}}
}

// Title is the thread's title as last seen.
func (t *Tracker) Title() string { return t.title }

// Pending is the open question or approval token names, if its prompt is
// still the one the token was made from.
func (t *Tracker) Pending(token string) (Update, bool) {
	id, _, ok := SplitToken(token)
	if !ok {
		return Update{}, false
	}
	r, ok := t.requests[id]
	item, has := t.prompts[id]
	if !ok || !has || r.Status != RequestPending || Token(id, item) != token {
		return Update{}, false
	}
	return t.promptUpdate(r, item)
}

// Apply feeds one stream item and returns what it tells, in order.
func (t *Tracker) Apply(it ThreadItem) []Update {
	switch it.Kind {
	case ThreadSnapshotKind:
		if it.Projection != nil {
			return t.snapshot(*it.Projection)
		}
	case ThreadEventKind:
		if it.Event != nil {
			return t.event(*it.Event)
		}
	}
	return nil
}

// PendingFromProjection is the open questions and approvals of a
// projection, as a fresh Tracker tells them: what a shell's
// pendingRuntimeRequest points at, read in full.
func PendingFromProjection(p ThreadProjection) []Update {
	return NewTracker(p.Thread.ID).snapshot(p)
}

func (t *Tracker) snapshot(p ThreadProjection) []Update {
	if p.Thread.Title != "" {
		t.title = p.Thread.Title
	}
	for _, r := range p.Runs {
		t.runs[r.ID] = r.Status
	}
	for _, m := range p.Messages {
		t.message(m)
	}
	for _, item := range p.TurnItems {
		switch item.Type {
		case ItemError:
			t.errors[item.ID] = true
		case ItemUserInputRequest, ItemApprovalRequest:
			t.prompts[item.RequestID] = item
		}
	}
	var out []Update
	seen := map[string]bool{}
	for _, r := range p.RuntimeRequests {
		seen[r.ID] = true
		prev, had := t.requests[r.ID]
		t.requests[r.ID] = r
		if had && prev.Status == RequestPending && r.Status != RequestPending {
			out = append(out, t.resolved(r.ID))
		}
		out = append(out, t.tell(r.ID)...)
	}
	// a request the new snapshot no longer has is no longer open
	for id, r := range t.requests {
		if !seen[id] && r.Status == RequestPending {
			r.Status = "cancelled"
			t.requests[id] = r
			out = append(out, t.resolved(id))
		}
	}
	return out
}

func (t *Tracker) event(e Event) []Update {
	switch e.Type {
	case EventRunCreated, EventRunUpdated:
		var r Run
		if json.Unmarshal(e.Payload, &r) != nil || r.ID == "" {
			return nil
		}
		return t.run(r)
	case EventRuntimeRequestUpdated:
		var r RuntimeRequest
		if json.Unmarshal(e.Payload, &r) != nil || r.ID == "" {
			return nil
		}
		prev, had := t.requests[r.ID]
		t.requests[r.ID] = r
		if had && prev.Status == RequestPending && r.Status != RequestPending {
			return []Update{t.resolved(r.ID)}
		}
		return t.tell(r.ID)
	case EventTurnItemUpdated:
		var item TurnItem
		if json.Unmarshal(e.Payload, &item) != nil {
			return nil
		}
		return t.item(item)
	case EventMessageUpdated:
		var m Message
		if json.Unmarshal(e.Payload, &m) == nil {
			t.message(m)
		}
		return nil
	}
	if strings.HasPrefix(e.Type, "thread.") {
		var th struct {
			Title string `json:"title"`
		}
		if json.Unmarshal(e.Payload, &th) == nil && th.Title != "" {
			t.title = th.Title
		}
	}
	return nil
}

func (t *Tracker) run(r Run) []Update {
	prev := t.runs[r.ID]
	t.runs[r.ID] = r.Status
	if !Terminal(r.Status) || prev == r.Status {
		return nil
	}
	out := []Update{{Kind: KindStopped, ThreadID: t.threadID, Title: t.title, RunID: r.ID, Status: r.Status, Text: t.lastText[r.ID]}}
	if r.Status == RunFailed {
		out = append(out, Update{Kind: KindError, ThreadID: t.threadID, Title: t.title, RunID: r.ID, Status: r.Status})
	}
	return out
}

func (t *Tracker) item(item TurnItem) []Update {
	switch item.Type {
	case ItemUserInputRequest, ItemApprovalRequest:
		if item.RequestID == "" {
			return nil
		}
		t.prompts[item.RequestID] = item
		return t.tell(item.RequestID)
	case ItemError:
		if item.ID == "" || t.errors[item.ID] {
			return nil
		}
		t.errors[item.ID] = true
		u := Update{Kind: KindError, ThreadID: t.threadID, Title: t.title}
		if item.RunID != nil {
			u.RunID = *item.RunID
		}
		if f := item.Failure; f != nil {
			u.Text, u.ErrorClass = f.Message, f.Class
			if f.Code != nil {
				u.ErrorCode = *f.Code
			}
		}
		return []Update{u}
	}
	return nil
}

func (t *Tracker) message(m Message) {
	if m.Role == "assistant" && !m.Streaming && m.RunID != nil && strings.TrimSpace(m.Text) != "" {
		t.lastText[*m.RunID] = m.Text
	}
}

// tell is the question or approval of request id once both its request
// (pending) and its prompt item are known, and its token was not told yet.
func (t *Tracker) tell(id string) []Update {
	r, ok := t.requests[id]
	item, has := t.prompts[id]
	if !ok || !has || r.Status != RequestPending {
		return nil
	}
	u, ok := t.promptUpdate(r, item)
	if !ok || t.told[u.Token] {
		return nil
	}
	t.told[u.Token] = true
	return []Update{u}
}

func (t *Tracker) promptUpdate(r RuntimeRequest, item TurnItem) (Update, bool) {
	u := Update{ThreadID: t.threadID, Title: t.title, RequestID: r.ID, Token: Token(r.ID, item)}
	if item.RunID != nil {
		u.RunID = *item.RunID
	}
	switch {
	case r.Kind == RequestUserInput && item.Type == ItemUserInputRequest:
		u.Kind, u.Questions = KindQuestion, item.Questions
	case ApprovalKind(r.Kind) && item.Type == ItemApprovalRequest:
		u.Kind = KindApproval
		u.Approval = &Approval{RequestKind: item.RequestKind, Prompt: item.Prompt, Reason: item.Reason, Options: item.Options}
	default:
		return Update{}, false
	}
	return u, true
}

func (t *Tracker) resolved(id string) Update {
	u := Update{Kind: KindResolved, ThreadID: t.threadID, Title: t.title, RequestID: id}
	if item, ok := t.prompts[id]; ok {
		u.Token = Token(id, item)
	}
	return u
}
