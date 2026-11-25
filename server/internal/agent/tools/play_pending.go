package tools

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/playprogress"
	"git.shino.boo/devilreef/cadence/server/internal/providers/ytdlp"
)

const (
	// playBudget bounds a play from its call to its first frame or its
	// failure: a playlist's songs, yt-dlp's resolve, youtube's available_at
	// wait, a skip or two and the screen's first frame (video's own screen
	// bound sits inside it)
	playBudget = 90 * time.Second
	// playTimeout is the play tools' Timeout: past the budget, so the play's
	// own outcome answers first, and within agent.MaxHold
	playTimeout = playBudget + 5*time.Second
)

// PlayRecord is a play's final outcome for the conversation recorder. Args
// are the original call's; Result is its final envelope, not a second play.
type PlayRecord struct {
	DeviceID, TurnID, CallID, OperationID string
	Tool                                  string
	Args, Result                          json.RawMessage
}

// PlayHooks connect a pending play to its stable live card, the conversation
// and the screen's session reports. Nil Session means no first-frame follower
// (unit fakes and devices that cannot report one).
type PlayHooks struct {
	Attach  func(agent.Call, playprogress.CardInfo) func(playprogress.Update)
	Record  func(context.Context, PlayRecord)
	Session func(deviceID string) (playprogress.Session, bool)
	Changes func() <-chan struct{}
	// Collection shows a collection's play (CardInfo.Kind set) on the
	// target's screen while its songs are read; nil for none
	Collection func(ctx context.Context, info playprogress.CardInfo)
	// Started names the session a play created for history attribution.
	Started func(agent.Call, playprogress.CardInfo, playprogress.Session)
}

type pendingPlay struct {
	call  agent.Call
	info  playprogress.CardInfo
	hooks PlayHooks

	ctx    context.Context
	cancel context.CancelFunc

	done   chan struct{}
	finish sync.Once
	record sync.Once

	mu        sync.Mutex
	update    playprogress.Update
	out       json.RawMessage
	terr      *agent.ToolError
	pending   bool
	before    string
	card      func(playprogress.Update)
	sessionID string
}

// runPendingPlay runs a play on its own budget and answers its real outcome:
// its first frame (or the session's report), or its failure. The tool holds
// its turn meanwhile (agent.Tool.Holds). Only a turn that ends first (the
// user cancelled it) gets pending, and the outcome goes to the history late.
func runPendingPlay(ctx context.Context, call agent.Call, info playprogress.CardInfo, hooks PlayHooks,
	run func(context.Context) (json.RawMessage, *agent.ToolError)) (json.RawMessage, *agent.ToolError) {
	p := &pendingPlay{call: call, info: info, hooks: hooks, done: make(chan struct{})}
	p.update = playprogress.Update{Event: playprogress.Event{Stage: playprogress.FirstStage(info), RemainingMS: -1},
		State: playprogress.Resolving, BufferingPercent: -1}
	if hooks.Session != nil {
		if s, ok := hooks.Session(info.DeviceID); ok {
			p.before = s.ID
		}
	}
	if hooks.Attach != nil {
		p.card = hooks.Attach(call, info)
		p.publish()
	}
	p.ctx, p.cancel = context.WithTimeout(context.WithoutCancel(ctx), playBudget)
	playCtx := playprogress.WithItem(ytdlp.WithProgress(p.ctx, p.progress), p.item)
	go p.run(playCtx, run)

	select {
	case <-p.done:
		return p.answer()
	case <-ctx.Done():
		// the play is detached: the turn's end is not evidence of failure
		return p.pendingAnswer()
	}
}

func (p *pendingPlay) run(ctx context.Context, run func(context.Context) (json.RawMessage, *agent.ToolError)) {
	defer p.cancel()
	if p.info.Kind != "" && p.hooks.Collection != nil {
		p.hooks.Collection(ctx, p.info)
	}
	out, terr := run(ctx)
	if terr != nil {
		p.fail(terr)
		return
	}
	p.mu.Lock()
	p.out = out
	p.mu.Unlock()
	if p.hooks.Session == nil {
		p.succeed()
		return
	}
	p.follow()
}

func (p *pendingPlay) progress(ev ytdlp.ResolveEvent) {
	p.mu.Lock()
	if p.update.State != playprogress.Resolving {
		p.mu.Unlock()
		return
	}
	// elapsed and remaining belong to the latest update of a repeated stage too
	p.update.Event = ev
	p.update.BufferingPercent = -1
	p.mu.Unlock()
	p.publish()
}

// item is the song a collection's play fetches now: the card names it in
// place of the list, from its first resolve stage on.
func (p *pendingPlay) item(it playprogress.Item) {
	p.mu.Lock()
	if p.update.State != playprogress.Resolving {
		p.mu.Unlock()
		return
	}
	p.update.Item = &it
	p.update.Event = playprogress.Event{Stage: playprogress.StageCookies, RemainingMS: -1}
	p.mu.Unlock()
	p.publish()
}

func (p *pendingPlay) publish() {
	p.mu.Lock()
	u, card := p.update, p.card
	p.mu.Unlock()
	if card != nil {
		card(u)
	}
}

func (p *pendingPlay) pendingAnswer() (json.RawMessage, *agent.ToolError) {
	p.mu.Lock()
	if p.isFinishedLocked() {
		p.mu.Unlock()
		return p.answer()
	}
	p.pending = true
	stage := p.update.Stage
	p.mu.Unlock()
	p.recordIfFinished()
	target := p.info.DeviceName
	if target == "" {
		target = p.info.DeviceID
	}
	return encode(map[string]any{"pending": true, "starting": true, "startingOn": target, "stage": stage})
}

func (p *pendingPlay) answer() (json.RawMessage, *agent.ToolError) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.out, p.terr
}

func (p *pendingPlay) isFinishedLocked() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

func (p *pendingPlay) succeed() {
	p.finish.Do(func() {
		p.mu.Lock()
		p.update.State = playprogress.Playing
		p.update.Stage = playprogress.StageDone
		p.update.Done = true
		p.update.Failed = false
		p.update.Code = ""
		p.update.RemainingMS = -1
		p.mu.Unlock()
		close(p.done)
		p.publish()
		p.recordIfFinished()
	})
}

func (p *pendingPlay) fail(terr *agent.ToolError) {
	if terr == nil {
		terr = failed("play_failed")
	}
	p.finish.Do(func() {
		p.mu.Lock()
		p.terr = terr
		p.update.State = playprogress.Failed
		p.update.Failed = true
		p.update.Done = false
		p.update.Code = terr.Code
		p.update.RemainingMS = -1
		p.mu.Unlock()
		close(p.done)
		p.publish()
		p.recordIfFinished()
	})
}

// follow waits for the session the play created and its first real picture.
// A playing state before that is still buffering on the card.
func (p *pendingPlay) follow() {
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		if p.hooks.Session != nil {
			s, ok := p.hooks.Session(p.info.DeviceID)
			if p.session(s, ok) {
				return
			}
		}
		var changed <-chan struct{}
		if p.hooks.Changes != nil {
			changed = p.hooks.Changes()
		}
		select {
		case <-p.ctx.Done():
			p.fail(&agent.ToolError{Code: "timeout", Message: "The screen did not show the play in time."})
			return
		case <-changed:
		case <-tick.C:
		}
	}
}

// session consumes one report; true means the play reached a terminal state.
func (p *pendingPlay) session(s playprogress.Session, ok bool) bool {
	p.mu.Lock()
	started := false
	if p.sessionID == "" {
		if !ok || s.ID == "" || s.ID == p.before || s.State == "idle" {
			p.mu.Unlock()
			return false
		}
		p.sessionID = s.ID
		p.update.SessionID = s.ID
		started = true
	}
	if !ok || s.ID != p.sessionID || s.State == "idle" {
		p.mu.Unlock()
		p.fail(&agent.ToolError{Code: "play_failed", Message: "The screen stopped before showing the play."})
		return true
	}
	code := s.ErrorCode
	percent := s.BufferingPercent
	picture := s.PictureReady
	p.mu.Unlock()
	if started && p.hooks.Started != nil {
		p.hooks.Started(p.call, p.info, s)
	}
	if code != "" {
		p.fail(&agent.ToolError{Code: playerFailureCode(code), Message: "The screen could not play the stream."})
		return true
	}
	p.mu.Lock()
	if p.update.State == playprogress.Resolving || p.update.State == playprogress.Buffering {
		p.update.State = playprogress.Buffering
		p.update.BufferingPercent = percent
	}
	p.mu.Unlock()
	p.publish()
	if picture && (s.State == "playing" || s.State == "paused") {
		p.succeed()
		return true
	}
	return false
}

func playerFailureCode(code string) string {
	switch code {
	case "decode", "network", "unsupported", "unplayable", "sign_in_required", "timeout":
		return code
	}
	return "play_failed"
}

func (p *pendingPlay) recordIfFinished() {
	p.mu.Lock()
	pending, finished := p.pending, p.isFinishedLocked()
	p.mu.Unlock()
	if !pending || !finished || p.hooks.Record == nil {
		return
	}
	p.record.Do(func() {
		p.mu.Lock()
		out, terr := p.out, p.terr
		p.mu.Unlock()
		var result []byte
		if terr != nil {
			result, _ = json.Marshal(struct {
				OK    bool             `json:"ok"`
				Error *agent.ToolError `json:"error"`
			}{Error: terr})
		} else {
			var data map[string]any
			_ = json.Unmarshal(out, &data)
			if data == nil {
				data = map[string]any{}
			}
			data["pending"] = false
			data["late"] = true
			data["stage"] = playprogress.StageDone
			result, _ = json.Marshal(struct {
				OK   bool           `json:"ok"`
				Data map[string]any `json:"data"`
			}{OK: true, Data: data})
		}
		p.hooks.Record(context.Background(), PlayRecord{DeviceID: p.call.DeviceID, TurnID: p.call.TurnID, CallID: p.call.CallID,
			OperationID: p.info.ID, Tool: p.info.Tool, Args: p.call.Args, Result: result})
	})
}
