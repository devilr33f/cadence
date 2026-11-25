package conversation

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/store/db"
)

var (
	// ErrNotFound is an unknown device, or a turn it does not have.
	ErrNotFound = errors.New("conversation: not found")
	// ErrCursor is a page cursor the store did not make.
	ErrCursor = errors.New("conversation: bad cursor")
	// errRead is any other failed read: a driver error may carry content.
	errRead = errors.New("conversation: read failed")
)

// Tokens are a turn's tokens summed over its model calls.
type Tokens struct{ Prompt, Completion int }

// Turn is a stored turn with its messages in order. Trigger and Mode are ""
// and Tokens nil where the turn recorded none (one stored before the turn
// table, or one that never reached the model).
type Turn struct {
	ID      string
	At      time.Time
	Trigger string
	Mode    string
	Tokens  *Tokens
	// Failure is the turn's codes-only outcome; nil for a successful turn
	Failure *TurnFailure
	// Skills are the skills it loaded, by name (the detail reads them)
	Skills   []string
	Messages []Message
}

// Message is a stored message and its place in its turn; a tool message's
// Code is the error code its result carries, read as the list reads it.
type Message struct {
	Seq int
	providers.Message
	Code string
}

type turnRow struct {
	id, trigger, mode string
	prompt, compl     pgtype.Int4
	at                time.Time
	failure           []byte
}

func (r turnRow) turn() Turn {
	t := Turn{ID: r.id, At: r.at.UTC(), Trigger: r.trigger, Mode: r.mode}
	if r.prompt.Valid && r.compl.Valid {
		t.Tokens = &Tokens{Prompt: int(r.prompt.Int32), Completion: int(r.compl.Int32)}
	}
	if len(r.failure) > 0 {
		var f TurnFailure
		if json.Unmarshal(r.failure, &f) == nil {
			t.Failure = failurePtr(&f)
		}
	}
	return t
}

// Summary is a turn as the list reads it: its row, and of each message only
// what the list shows (ConversationTurnsSummaries), never the message whole.
type Summary struct {
	Turn
	Lines []Line
}

// Line is what the list reads of one message: its role, a user or assistant
// text cut to 2000 characters, an assistant's tool calls by id and name, and
// the call a tool message answers with the code its result carries.
type Line struct {
	Role, Text         string
	CallIDs, CallNames []string
	ToolCallID, Code   string
}

// Turns is a page of the device's turns, newest first: the limit after the
// cursor before ("" from the newest), and the cursor of the page after it, ""
// for none. Two queries at most: the turn rows by their index, then what the
// list shows of their messages; an empty page reads the device instead, so an
// unknown one is ErrNotFound.
func (s *Store) Turns(ctx context.Context, deviceID, before string, limit int) ([]Summary, string, error) {
	limit = min(max(limit, 1), maxPage)
	at, after, err := decodeCursor(before)
	if err != nil {
		return nil, "", err
	}
	dev, err := parseDevice(deviceID)
	if err != nil {
		return nil, "", ErrNotFound
	}
	rows, err := s.q.ConversationTurnPage(ctx, db.ConversationTurnPageParams{
		DeviceID: dev, BeforeAt: at, BeforeID: after, MaxRows: int32(limit + 1),
	})
	if err != nil {
		return nil, "", readFailed(ctx)
	}
	if len(rows) == 0 {
		_, err := s.q.DeviceByID(ctx, dev)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, "", ErrNotFound
		}
		if err != nil {
			return nil, "", readFailed(ctx)
		}
		return []Summary{}, "", nil
	}
	next := ""
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[len(rows)-1]
		next = encodeCursor(last.CreatedAt.Time, last.TurnID)
	}
	turns := make([]Summary, len(rows))
	ids := make([]string, len(rows))
	place := make(map[string]int, len(rows))
	for i, r := range rows {
		turns[i].Turn = turnRow{id: r.TurnID, trigger: r.Trigger.String, mode: r.Mode.String, prompt: r.PromptTokens,
			compl: r.CompletionTokens, at: r.CreatedAt.Time, failure: r.Failure}.turn()
		ids[i], place[r.TurnID] = r.TurnID, i
	}
	lines, err := s.q.ConversationTurnsSummaries(ctx, db.ConversationTurnsSummariesParams{DeviceID: dev, TurnIds: ids})
	if err != nil {
		return nil, "", readFailed(ctx)
	}
	for _, l := range lines {
		if i, ok := place[l.TurnID]; ok {
			turns[i].Lines = append(turns[i].Lines, Line{
				Role: l.Role, Text: l.Text, CallIDs: l.CallIds, CallNames: l.CallNames, ToolCallID: l.ToolCallID, Code: l.Code,
			})
		}
	}
	return turns, next, nil
}

// Turn is one turn of the device, ErrNotFound when the device or the turn is
// unknown.
func (s *Store) Turn(ctx context.Context, deviceID, turnID string) (Turn, error) {
	dev, err := parseDevice(deviceID)
	if err != nil || !validID(turnID) {
		return Turn{}, ErrNotFound
	}
	r, err := s.q.GetConversationTurn(ctx, db.GetConversationTurnParams{DeviceID: dev, TurnID: turnID})
	if errors.Is(err, pgx.ErrNoRows) {
		return Turn{}, ErrNotFound
	}
	if err != nil {
		return Turn{}, readFailed(ctx)
	}
	turns := []Turn{turnRow{id: r.TurnID, trigger: r.Trigger.String, mode: r.Mode.String, prompt: r.PromptTokens,
		compl: r.CompletionTokens, at: r.CreatedAt.Time, failure: r.Failure}.turn()}
	turns[0].Skills = r.Skills
	if err := s.messages(ctx, dev, turns); err != nil {
		return Turn{}, err
	}
	return turns[0], nil
}

// messages fills the turns' messages, in one query.
func (s *Store) messages(ctx context.Context, dev pgtype.UUID, turns []Turn) error {
	ids := make([]string, len(turns))
	at := make(map[string]int, len(turns))
	for i, t := range turns {
		ids[i], at[t.ID] = t.ID, i
	}
	rows, err := s.q.ConversationTurnsMessages(ctx, db.ConversationTurnsMessagesParams{DeviceID: dev, TurnIds: ids})
	if err != nil {
		return readFailed(ctx)
	}
	for _, r := range rows {
		i, ok := at[r.TurnID]
		if !ok {
			continue
		}
		m, err := decode(r.Content)
		if err != nil {
			return errors.New("conversation: decode message")
		}
		turns[i].Messages = append(turns[i].Messages, Message{Seq: int(r.Seq), Message: m, Code: r.Code})
	}
	return nil
}

// readFailed prefers the request's own end over the driver's error.
func readFailed(ctx context.Context) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return errRead
}
