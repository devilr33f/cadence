package conversation

import (
	"encoding/json"
	"strings"
	"time"
)

// TurnPage is a page of a device's turns, newest first.
type TurnPage struct {
	Turns []TurnView `json:"turns" nullable:"false"`
	Next  string     `json:"next,omitempty" doc:"the cursor of the page after this one, sent back as before; absent on the last page"`
}

// TurnView is one turn in the list: what it heard and said and the tools it
// called, never their arguments or results.
type TurnView struct {
	TurnID  string       `json:"turnId"`
	At      time.Time    `json:"at" doc:"when the turn was stored"`
	Trigger string       `json:"trigger,omitempty" enum:"wake,button,follow_up,event,word,notice,screen,reset,dashboard" doc:"what started it (dashboard: a request the owner typed in the dashboard; notice: an update the server spoke unasked, one assistant message and no user message; screen: a card action a remote ran, without the model); absent for a turn stored before turns recorded it, or of no known trigger"`
	Mode    string       `json:"mode,omitempty" doc:"the agent mode its last step ran in; absent when none was resolved"`
	User    string       `json:"user,omitempty" doc:"the turn's first user message, at most 2000 characters"`
	Reply   string       `json:"reply,omitempty" doc:"the last text the assistant wrote, at most 2000 characters"`
	Tools   []TurnTool   `json:"tools" nullable:"false" doc:"the assistant's tool calls by name, in order"`
	Tokens  *TurnTokens  `json:"tokens,omitempty" doc:"summed over the turn's model calls; absent when none reported any"`
	Failure *TurnFailure `json:"failure,omitempty" doc:"why the turn failed: codes, ids and timings only; absent for a successful turn"`
}

// TurnTool is one tool call in the list.
type TurnTool struct {
	Name string `json:"name"`
	Code string `json:"code,omitempty" doc:"the error code its result carries: a string code or error field, or the code of its error object, read from results of at most 4 KiB"`
}

// TurnTokens are a turn's tokens.
type TurnTokens struct {
	Prompt     int `json:"prompt"`
	Completion int `json:"completion"`
}

// TurnDetail is one turn with its messages as stored, tool arguments and
// results included: the dashboard is where rule 8 lets them be read.
type TurnDetail struct {
	TurnID   string        `json:"turnId"`
	At       time.Time     `json:"at" doc:"when the turn was stored"`
	Trigger  string        `json:"trigger,omitempty" enum:"wake,button,follow_up,event,word,notice,screen,reset,dashboard" doc:"what started it (dashboard: a request the owner typed in the dashboard; notice: an update the server spoke unasked, one assistant message and no user message; screen: a card action a remote ran, without the model); absent for a turn stored before turns recorded it, or of no known trigger"`
	Mode     string        `json:"mode,omitempty" doc:"the agent mode its last step ran in; absent when none was resolved"`
	Tokens   *TurnTokens   `json:"tokens,omitempty" doc:"summed over the turn's model calls; absent when none reported any"`
	Failure  *TurnFailure  `json:"failure,omitempty" doc:"why the turn failed: codes, ids and timings only; absent for a successful turn"`
	Skills   []string      `json:"skills,omitempty" doc:"the skills the turn loaded, by name, in the order it loaded them; absent when it loaded none"`
	Messages []TurnMessage `json:"messages" nullable:"false"`
}

// TurnMessage is a stored message.
type TurnMessage struct {
	Seq        int        `json:"seq"`
	Role       string     `json:"role" doc:"user, assistant or tool"`
	Content    string     `json:"content" doc:"the text; a tool message's is the tool's result as the model read it"`
	ToolCallID string     `json:"toolCallId,omitempty" doc:"the call a tool message answers"`
	Code       string     `json:"code,omitempty" doc:"a tool message's error code, as the list reads it: a string code or error field, or the code of its error object, from results of at most 4 KiB"`
	ToolCalls  []TurnCall `json:"toolCalls,omitempty" nullable:"false"`
}

// TurnCall is one tool call an assistant message made.
type TurnCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty" doc:"the arguments as the model sent them"`
}

func tokensView(t *Tokens) *TurnTokens {
	if t == nil {
		return nil
	}
	return &TurnTokens{Prompt: t.Prompt, Completion: t.Completion}
}

// summarize is a turn as the list shows it, from what the list read of its
// messages: the cuts and the codes come from the query.
func summarize(t Summary) TurnView {
	v := TurnView{TurnID: t.ID, At: t.At, Trigger: t.Trigger, Mode: t.Mode, Tools: []TurnTool{}, Tokens: tokensView(t.Tokens), Failure: failurePtr(t.Failure)}
	codes := map[string]string{}
	for _, l := range t.Lines {
		if _, seen := codes[l.ToolCallID]; l.Role == roleTool && l.ToolCallID != "" && !seen {
			codes[l.ToolCallID] = l.Code
		}
	}
	heard := false
	for _, l := range t.Lines {
		switch l.Role {
		case roleUser:
			if !heard {
				v.User, heard = l.Text, true
			}
		case roleAssistant:
			if strings.TrimSpace(l.Text) != "" {
				v.Reply = l.Text
			}
			for i, name := range l.CallNames {
				tv := TurnTool{Name: name}
				if i < len(l.CallIDs) && l.CallIDs[i] != "" {
					tv.Code = codes[l.CallIDs[i]]
				}
				v.Tools = append(v.Tools, tv)
			}
		}
	}
	return v
}

// detailOf is a turn as stored.
func detailOf(t Turn) TurnDetail {
	d := TurnDetail{
		TurnID: t.ID, At: t.At, Trigger: t.Trigger, Mode: t.Mode, Tokens: tokensView(t.Tokens), Failure: failurePtr(t.Failure),
		Skills: t.Skills, Messages: make([]TurnMessage, len(t.Messages)),
	}
	for i, m := range t.Messages {
		mv := TurnMessage{Seq: m.Seq, Role: m.Role, Content: m.Content, ToolCallID: m.ToolCallID, Code: m.Code}
		for _, c := range m.ToolCalls {
			mv.ToolCalls = append(mv.ToolCalls, TurnCall(c))
		}
		d.Messages[i] = mv
	}
	return d
}

const (
	roleUser      = "user"
	roleAssistant = "assistant"
	roleTool      = "tool"
)
