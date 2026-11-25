// Package conversation keeps each device's messages and loads the history the
// agent uses as context.
package conversation

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/store/db"
)

type Store struct{ q *db.Queries }

func New(q *db.Queries) *Store { return &Store{q: q} }

type wireCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

type wireMessage struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCallID string     `json:"toolCallId,omitempty"`
	ToolCalls  []wireCall `json:"toolCalls,omitempty"`
}

func encode(m providers.Message) ([]byte, error) {
	w := wireMessage{Role: clean(m.Role), Content: clean(m.Content), ToolCallID: clean(m.ToolCallID)}
	for _, c := range m.ToolCalls {
		w.ToolCalls = append(w.ToolCalls, wireCall{ID: clean(c.ID), Name: clean(c.Name), Arguments: cleanJSON(c.Arguments)})
	}
	return json.Marshal(w)
}

func decode(b []byte) (providers.Message, error) {
	var w wireMessage
	if err := json.Unmarshal(b, &w); err != nil {
		return providers.Message{}, err
	}
	m := providers.Message{Role: w.Role, Content: w.Content, ToolCallID: w.ToolCallID}
	for _, c := range w.ToolCalls {
		m.ToolCalls = append(m.ToolCalls, providers.ToolCall{ID: c.ID, Name: c.Name, Arguments: c.Arguments})
	}
	return m, nil
}

func parseDevice(id string) (pgtype.UUID, error) {
	var u pgtype.UUID
	if err := u.Scan(id); err != nil {
		return u, errors.New("conversation: bad device id")
	}
	return u, nil
}

// TurnMeta is what a turn records beside its messages (ruling M7bs-R4): the
// trigger that started it and the mode it ended in, "" for none, its tokens
// summed over its model calls, nil when none reported any, and its failure.
type TurnMeta struct {
	Trigger string
	Mode    string
	Usage   *providers.Usage
	// Skills are the skills the turn loaded, by name; nil for none
	Skills []string
	// Failure is why the turn failed; nil for a successful one
	Failure *TurnFailure
}

// Append stores one turn's row and its messages in a single statement, so all
// or none land.
func (s *Store) Append(ctx context.Context, deviceID, turnID string, meta TurnMeta, msgs []providers.Message) error {
	if len(msgs) == 0 {
		return nil
	}
	dev, err := parseDevice(deviceID)
	if err != nil {
		return err
	}
	p := db.AppendConversationTurnParams{
		DeviceID: dev, TurnID: clean(turnID), Trigger: text(meta.Trigger), Mode: text(meta.Mode),
		Failure: encodeFailure(meta.Failure),
	}
	for _, name := range meta.Skills {
		p.Skills = append(p.Skills, clean(name))
	}
	if u := meta.Usage; u != nil {
		p.PromptTokens, p.CompletionTokens = count(u.PromptTokens), count(u.CompletionTokens)
	}
	for i, m := range msgs {
		b, err := encode(m)
		if err != nil {
			return errors.New("conversation: encode message")
		}
		p.Seqs = append(p.Seqs, int32(i))
		p.Roles = append(p.Roles, clean(m.Role))
		p.Contents = append(p.Contents, b)
	}
	return s.insert(ctx, p)
}

// RecordFailure stores a voice-stage failure, creating the turn when stt or
// setup failed before the agent had one, or updating the agent's committed
// turn when tts failed after it. No message is synthesized for a partial
// transcript: partials are never committed.
func (s *Store) RecordFailure(ctx context.Context, deviceID, turnID string, meta TurnMeta, failure TurnFailure) error {
	dev, err := parseDevice(deviceID)
	if err != nil {
		return err
	}
	f := failurePtr(&failure)
	if f == nil {
		return errors.New("conversation: invalid failure")
	}
	return s.q.RecordConversationFailure(ctx, db.RecordConversationFailureParams{
		DeviceID: dev, TurnID: clean(turnID), Trigger: text(meta.Trigger), Mode: text(meta.Mode), Failure: encodeFailure(f),
	})
}

func encodeFailure(f *TurnFailure) []byte {
	f = failurePtr(f)
	if f == nil {
		return nil
	}
	b, err := json.Marshal(f)
	if err != nil {
		return nil
	}
	return b
}

// History returns whole turns committed after the message afterID, oldest
// first: the newest maxTurns of them (M5a-D4: the raw turns after the
// latest compaction).
func (s *Store) History(ctx context.Context, deviceID string, afterID int64, maxTurns int) ([]providers.Message, error) {
	maxTurns = min(maxTurns, 1000)
	if maxTurns <= 0 {
		return nil, nil
	}
	dev, err := parseDevice(deviceID)
	if err != nil {
		return nil, err
	}
	turns, err := s.q.RecentConversationTurns(ctx, db.RecentConversationTurnsParams{DeviceID: dev, AfterID: afterID, MaxTurns: int32(maxTurns)})
	if err != nil || len(turns) == 0 {
		return nil, err
	}
	rows, err := s.q.ConversationTurnMessages(ctx, db.ConversationTurnMessagesParams{DeviceID: dev, AfterID: afterID, TurnIds: turns})
	if err != nil {
		return nil, err
	}
	// turns come newest first; two turns committed at once may interleave
	// their ids, so messages are grouped by turn, not taken in id order
	order := make(map[string]int, len(turns))
	for i, t := range turns {
		order[t] = len(turns) - 1 - i
	}
	byTurn := make([][]providers.Message, len(turns))
	for _, r := range rows {
		m, err := decode(r.Content)
		if err != nil {
			return nil, errors.New("conversation: decode message")
		}
		i := order[r.TurnID]
		byTurn[i] = append(byTurn[i], m)
	}
	out := make([]providers.Message, 0, len(rows))
	for _, ms := range byTurn {
		out = append(out, ms...)
	}
	return out, nil
}

// TriggerEvent is the trigger of an event turn (M5b-D6), whose user message
// is the server's own framing, not anything a person said.
const TriggerEvent = "event"

// TriggerNotice is the trigger of a notice turn: an update the server spoke
// unasked (the subscriptions hub's), stored as one assistant message with no
// user message and no turn loop. Its text quotes outside systems, so the
// agent's history and learning never read it (RecentConversationTurns,
// ConversationMessagesAfter); the history API shows it.
const TriggerNotice = "notice"

// Notice stores a spoken notice as a turn of its own: one assistant message.
func (s *Store) Notice(ctx context.Context, deviceID, text string) error {
	return s.Append(ctx, deviceID, "notice_"+rand.Text(), TurnMeta{Trigger: TriggerNotice},
		[]providers.Message{{Role: roleAssistant, Content: text}})
}

// TriggerScreen is the trigger of a card action a screen's remote ran
// without the model (W0-C item 12): its user message is the server's own
// framing too, and its tool call the action's.
const TriggerScreen = "screen"

// TriggerDashboard is the trigger of a turn the owner typed in the dashboard's
// assistant: its user message is the owner's own words, and it runs as a
// press would.
const TriggerDashboard = "dashboard"

// Stored is a committed message and its id, which compactions and memory
// evidence point at.
type Stored struct {
	ID int64
	// Trigger is the trigger of the message's turn, "" for a turn that
	// recorded none
	Trigger string
	providers.Message
}

// After returns the messages committed after the message afterID and
// created before before, oldest first; past limit, the newest limit of them.
func (s *Store) After(ctx context.Context, deviceID string, afterID int64, before time.Time, limit int) ([]Stored, error) {
	if limit <= 0 {
		return nil, nil
	}
	dev, err := parseDevice(deviceID)
	if err != nil {
		return nil, err
	}
	rows, err := s.q.ConversationMessagesAfter(ctx, db.ConversationMessagesAfterParams{
		DeviceID: dev, AfterID: afterID, Before: pgtype.Timestamptz{Time: before, Valid: true}, MaxRows: int32(min(limit, math.MaxInt32)),
	})
	if err != nil {
		return nil, err
	}
	out := make([]Stored, len(rows))
	for i, r := range rows {
		m, err := decode(r.Content)
		if err != nil {
			return nil, errors.New("conversation: decode message")
		}
		out[i] = Stored{ID: r.ID, Trigger: r.Trigger.String, Message: m}
	}
	return out, nil
}

// Pruned counts what a prune deleted.
type Pruned struct{ Messages, Turns int64 }

// Prune deletes the turns and messages created before olderThan and returns
// how many.
func (s *Store) Prune(ctx context.Context, olderThan time.Time) (Pruned, error) {
	n, err := s.q.PruneConversations(ctx, pgtype.Timestamptz{Time: olderThan, Valid: true})
	return Pruned{Messages: n.Messages, Turns: n.Turns}, err
}

// text is a nullable text column: "" is NULL.
func text(s string) pgtype.Text { return pgtype.Text{String: clean(s), Valid: s != ""} }

// count is a token count as the column holds it.
func count(n int) pgtype.Int4 {
	return pgtype.Int4{Int32: int32(min(max(n, 0), math.MaxInt32)), Valid: true}
}

// insert returns a fixed error: a driver error may carry message content.
func (s *Store) insert(ctx context.Context, p db.AppendConversationTurnParams) error {
	if err := s.q.AppendConversationTurn(ctx, p); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("conversation: append failed")
	}
	return nil
}
