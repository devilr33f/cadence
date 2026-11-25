package screens

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	cardsv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/cards/v1"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/server/internal/cards"
	"git.shino.boo/devilreef/cadence/server/internal/events"
)

// turn is one session of a device whose room has a screen. Its id is the
// session's: one turn a session, a follow-up being a session of its own.
type turn struct {
	id     string
	origin string
	// pressedBy is the screen whose assistant button opened the session
	pressedBy string
	trigger   cadencev2.Trigger
	state     cadencev2.AssistantState
	mic       *cadencev2.MicSource

	// transcript is the final text; until it comes, stable and unstable are
	// the live partial: words two hypotheses agreed on, then the rest
	transcript       string
	stable, unstable string
	steps            []step
	reply            []string
	// cards are each screen's issued cards, in the order they came
	cards map[string][]*cardsv1.Card
	// attached are the subjects the tools attached (a web search's results, a catalog play's live card), for a screen
	// that joins later
	attached  []*attachment
	errorCode string
	failure   *cadencev2.TurnFailure
	window    uint32

	// reset: the next state clears the screen's chat and carries the confirmation
	reset   bool
	screens []string
	ducked  []string
	// the pressing screen asked to stop before the session started
	stopAsked bool
	rev       uint32
	started   time.Time
}

// attachment is a tool's own card: under its call's id once a step took it, else its own
type attachment struct {
	subj cards.Subject
	step string
	// key is the call the card is for; waiting is one that came before its call, shown once its step takes it
	key     string
	waiting bool
}

type step struct {
	id     string
	status cadencev2.StepStatus
	// the call as far as it went: its line and its card read it
	call cards.Call
	// the call's own card, "" while it shows none
	cardID string
	// attached is the card the call's tool attached itself, which stands for the call's card in every state
	attached cards.Subject
}

func (s *Service) fromDevice(ctx context.Context, dev string, env *cadencev2.Envelope) {
	switch b := env.GetBody().(type) {
	case *cadencev2.Envelope_SessionStart:
		s.start(ctx, dev, b.SessionStart)
	case *cadencev2.Envelope_SessionCancel:
		if t := s.turns[b.SessionCancel.GetSessionId()]; t != nil && t.origin == dev {
			t.state = cadencev2.AssistantState_ASSISTANT_STATE_DISMISSED
			s.end(ctx, t.id, t)
		}
	}
}

func (s *Service) start(ctx context.Context, dev string, st *cadencev2.SessionStart) {
	id := st.GetSessionId()
	if id == "" || s.turns[id] != nil {
		return
	}
	lctx, cancel := context.WithTimeout(ctx, lookupTimeout)
	defer cancel()
	screens, others := s.room(lctx, dev)
	p, pressed := s.takePending(id)
	if pressed && p.speaker == dev && !contains(screens, p.screen) {
		screens = append(screens, p.screen)
	}
	if len(screens) == 0 {
		return
	}
	t := &turn{
		id: id, origin: dev, trigger: st.GetTrigger(), state: cadencev2.AssistantState_ASSISTANT_STATE_LISTENING,
		cards: map[string][]*cardsv1.Card{}, screens: screens, started: s.d.Now(),
		mic: s.speakerMic(lctx, dev),
	}
	if pressed && p.speaker == dev {
		t.pressedBy = p.screen
		t.stopAsked = p.stopped
		s.setPressed(id, p.screen)
	}
	if contains(screens, dev) {
		t.mic = &cadencev2.MicSource{Kind: cadencev2.MicSourceKind_MIC_SOURCE_KIND_LOCAL}
	}
	s.turns[id] = t
	for _, o := range append(others, screens...) {
		if o != dev && s.d.Playing(o) && !contains(t.ducked, o) {
			t.ducked = append(t.ducked, o)
		}
	}
	s.duck(t, true)
	s.d.Log.Info("screen_turn", "device_id", dev, "session_id", id, "screens", len(screens), "ducked", len(t.ducked))
	s.send(ctx, t)
	s.stopIfAsked(t)
}

// stopIfAsked stops a turn whose screen asked before its session started.
// The voice service may take the session's start after this service does, so
// each of the session's frames tries again until a stop lands.
func (s *Service) stopIfAsked(t *turn) {
	if t.stopAsked && s.d.StopSession != nil && s.d.StopSession(t.origin, t.id) {
		t.stopAsked = false
		s.d.Log.Info("assistant_stop", "device_id", t.pressedBy, "session_id", t.id, "code", "ok")
	}
}

func (s *Service) toDevice(ctx context.Context, dev string, env *cadencev2.Envelope) {
	switch b := env.GetBody().(type) {
	case *cadencev2.Envelope_SessionEvent:
		t := s.turns[b.SessionEvent.GetSessionId()]
		if t == nil || t.origin != dev {
			return
		}
		s.stopIfAsked(t)
		switch b.SessionEvent.GetKind() {
		case cadencev2.SessionEventKind_SESSION_EVENT_KIND_THINKING:
			t.state = cadencev2.AssistantState_ASSISTANT_STATE_THINKING
		case cadencev2.SessionEventKind_SESSION_EVENT_KIND_LISTENING:
			t.state = cadencev2.AssistantState_ASSISTANT_STATE_LISTENING
		default:
			return
		}
		s.send(ctx, t)
	case *cadencev2.Envelope_StreamStart:
		t := s.turns[b.StreamStart.GetSessionId()]
		if t == nil || t.origin != dev || t.state == cadencev2.AssistantState_ASSISTANT_STATE_SPEAKING {
			return
		}
		t.state = cadencev2.AssistantState_ASSISTANT_STATE_SPEAKING
		s.send(ctx, t)
	case *cadencev2.Envelope_SessionClose:
		c := b.SessionClose
		t := s.turns[c.GetSessionId()]
		if t == nil || t.origin != dev {
			return
		}
		t.errorCode = errorToken(c.GetCode(), t.transcript != "")
		t.failure = c.GetFailure()
		if t.failure != nil && (t.errorCode == "" || t.errorCode == "upstream") {
			t.errorCode = t.failure.GetCode()
		}
		if c.GetFollowUpAllowed() && c.GetFollowUpWindowMs() > 0 {
			t.state, t.window = cadencev2.AssistantState_ASSISTANT_STATE_FOLLOW_UP, c.GetFollowUpWindowMs()
		} else {
			t.state = cadencev2.AssistantState_ASSISTANT_STATE_RESTING
		}
		s.end(ctx, t.id, t)
	}
}

// heard takes a hypothesis of the turn's utterance from the voice session:
// a partial is drawn at once, the final replaces it. A partial after the
// final (the bus's copy of it may come first) is dropped.
func (s *Service) heard(ctx context.Context, dev, session, text string, final bool) {
	t := s.turns[session]
	if t == nil || t.origin != dev || text == "" {
		return
	}
	if final {
		if t.transcript == text {
			return
		}
		t.transcript, t.stable, t.unstable = text, "", ""
		s.send(ctx, t)
		return
	}
	if t.transcript != "" {
		return
	}
	stable, unstable := splitPartial(joinWords(t.stable, t.unstable), text)
	if stable == t.stable && unstable == t.unstable {
		return
	}
	t.stable, t.unstable = stable, unstable
	s.send(ctx, t)
}

// splitPartial splits a hypothesis into the words the one before it already
// had, in the same places, and the rest; the last word is still forming, so
// it is never stable
func splitPartial(prev, next string) (stable, unstable string) {
	p, n := strings.Fields(prev), strings.Fields(next)
	k := 0
	for k < len(p) && k < len(n)-1 && p[k] == n[k] {
		k++
	}
	return strings.Join(n[:k], " "), strings.Join(n[k:], " ")
}

func joinWords(a, b string) string {
	if a == "" || b == "" {
		return a + b
	}
	return a + " " + b
}

// errorToken is a closed session's code as the screen's state block names it
// (not_heard, offline, upstream), "" for a turn that ended as meant
func errorToken(code string, heard bool) string {
	switch {
	case code == "" || code == "ok":
		return ""
	case code == "stt_failed" && !heard:
		return "not_heard"
	case code == "disconnected":
		return "offline"
	case strings.HasSuffix(code, "_failed"), strings.HasSuffix(code, "_unavailable"), code == "timeout",
		code == "internal", code == "not_configured":
		return "upstream"
	}
	return ""
}

func (s *Service) busEvent(ctx context.Context, e events.Event) {
	t := s.turns[e.SessionID]
	if t == nil || t.origin != e.DeviceID {
		return
	}
	data, _ := e.Data.(map[string]any)
	str := func(k string) string { v, _ := data[k].(string); return v }
	switch e.Type {
	case "transcript":
		// the voice session's final usually came first: nothing new to draw
		if t.transcript == str("text") {
			return
		}
		t.transcript, t.stable, t.unstable = str("text"), "", ""
	case "tool_call":
		if len(t.steps) == maxSteps {
			t.steps = t.steps[1:]
		}
		args, _ := data["args"].(json.RawMessage)
		t.steps = append(t.steps, step{id: str("id"), status: cadencev2.StepStatus_STEP_STATUS_RUNNING,
			call: cards.Call{Key: CallKey(e.TurnID, str("id")), Tool: str("name"), DeviceID: t.origin, Args: args}})
		// an attachment that came before its call is the call's card from the start
		st := &t.steps[len(t.steps)-1]
		s.linkAttached(t, st)
		s.showCall(ctx, t, st)
	case "tool_result":
		ok, _ := data["ok"].(bool)
		for i := range t.steps {
			st := &t.steps[i]
			if st.id != str("id") || st.call.Result != nil {
				continue
			}
			st.status = cadencev2.StepStatus_STEP_STATUS_DONE
			if !ok && !cards.Questions[str("code")] {
				st.status = cadencev2.StepStatus_STEP_STATUS_FAILED
			}
			// the bus carries the agent's envelope; the cards read the tool's own output
			envelope, _ := data["result"].(json.RawMessage)
			st.call.Result = &cards.ToolResult{Key: st.call.Key, Tool: st.call.Tool, OK: ok, Code: str("code"), Args: st.call.Args,
				Data: cards.ResultData(envelope), At: e.At, DeviceID: t.origin, Target: str("target")}
			s.showCall(ctx, t, st)
		}
	case "llm_text":
		s.flushAttached(ctx, t)
		if spoken, _ := data["spoken"].(bool); spoken {
			if text := strings.TrimSpace(str("text")); text != "" {
				t.reply = append(t.reply, text)
			}
		}
	default:
		return
	}
	s.send(ctx, t)
}

// showCall issues the call's cards in its current state on each of the turn's screens, replacing its earlier
// state in place, and puts it on the live feed
func (s *Service) showCall(ctx context.Context, t *turn, st *step) {
	if s.d.Calls == nil {
		return
	}
	id := ""
	for _, scr := range t.screens {
		subs, cid := s.callSubjects(st)
		id = cid
		s.issue(ctx, t, scr, subs)
		s.live(scr, st.call, subs)
	}
	if id != "" {
		st.cardID = id
	}
	s.publishCall(ctx, t, st)
}

// callSubjects are a step's cards now: the card its tool attached, which stands for the call's in every state, else
// the call's own
func (s *Service) callSubjects(st *step) ([]cards.Subject, string) {
	if st.attached != nil {
		return []cards.Subject{st.attached}, st.attached.ID()
	}
	if s.d.Calls == nil {
		return nil, ""
	}
	return s.d.Calls.CallSubjects(st.call)
}

// followTime is a card's own follow time (a catalog play's), zero for none
type followTime interface{ Follow() time.Duration }

// live lets a screen follow, under its lease, the entity cards of a Home Assistant write (only what the turn wrote
// to; a read or a search stays still) and any card with a follow time of its own (a catalog play's)
func (s *Service) live(scr string, c cards.Call, subs []cards.Subject) {
	if s.d.Live == nil {
		return
	}
	write := c.Tool == "ha_control" && c.Phase() == cards.Done
	for _, subj := range subs {
		if f, ok := subj.(followTime); write || ok && f.Follow() > 0 {
			s.d.Live(scr, subj)
		}
	}
}

// issue shows subjects on one screen as the turn's cards: a card shown again replaces its earlier revision in place
func (s *Service) issue(ctx context.Context, t *turn, scr string, subjects []cards.Subject) {
	if s.d.Cards == nil {
		return
	}
	for _, subj := range subjects {
		if subj == nil {
			continue
		}
		c, err := s.d.Cards.Show(ctx, scr, subj)
		if err != nil {
			continue
		}
		cs := t.cards[scr]
		replaced := false
		for i := range cs {
			if cs[i].GetId() == c.GetId() {
				cs[i], replaced = c, true
			}
		}
		if !replaced {
			cs = append(cs, c)
		}
		if len(cs) > maxTurnCards {
			cs = cs[len(cs)-maxTurnCards:]
		}
		t.cards[scr] = cs
	}
}

// attach takes a tool's own card (a search's results, a catalog play's live card) for the call key names: that
// call's step takes it as its card, under the call's id (cards.AsCall), so it replaces the step's card in place and
// the call keeps one card id in every state. The tool attaches it while its call runs, yet the bus may bring the call
// after it, or even its result first: a card whose call has not come waits, unseen, for its step (linkAttached). A
// card of no call shows under its own id.
func (s *Service) attach(ctx context.Context, t *turn, key string, subj cards.Subject) {
	if a, ok := subj.(cards.Attached); ok && key != "" {
		for i := range t.steps {
			st := &t.steps[i]
			if st.call.Key != key || st.call.Tool != a.ForTool() || st.attached != nil {
				continue
			}
			st.attached = cards.AsCall(key, subj)
			t.attached = append(t.attached, &attachment{subj: st.attached, step: st.id})
			s.showCall(ctx, t, st)
			return
		}
		t.attached = append(t.attached, &attachment{subj: subj, key: key, waiting: true})
		return
	}
	t.attached = append(t.attached, &attachment{subj: subj})
	s.showAttached(ctx, t, subj)
}

func (s *Service) showAttached(ctx context.Context, t *turn, subj cards.Subject) {
	for _, scr := range t.screens {
		s.issue(ctx, t, scr, []cards.Subject{subj})
		s.live(scr, cards.Call{}, []cards.Subject{subj})
	}
}

// linkAttached gives a call that just came the attachment waiting for it, under the call's id
func (s *Service) linkAttached(t *turn, st *step) {
	for _, a := range t.attached {
		if at, ok := a.subj.(cards.Attached); ok && a.waiting && a.key == st.call.Key && at.ForTool() == st.call.Tool {
			a.subj, a.step, a.waiting = cards.AsCall(st.call.Key, a.subj), st.id, false
			st.attached = a.subj
			return
		}
	}
}

// flushAttached shows, under their own ids, the attachments still waiting for a call the bus never brought: by the
// reply every call of the turn's steps came before it
func (s *Service) flushAttached(ctx context.Context, t *turn) {
	for _, a := range t.attached {
		if a.waiting {
			a.waiting = false
			s.showAttached(ctx, t, a.subj)
		}
	}
}

// joined brings a screen that linked into each running turn of its room: every call's card at its latest state (the
// screen's earlier cards went with its link), never their history
func (s *Service) joined(ctx context.Context, dev string) {
	for _, t := range s.turns {
		if t.origin == dev {
			continue
		}
		in := contains(t.screens, dev)
		if !in {
			lctx, cancel := context.WithTimeout(ctx, lookupTimeout)
			screens, _ := s.room(lctx, t.origin)
			cancel()
			if !contains(screens, dev) {
				continue
			}
			t.screens = append(t.screens, dev)
		}
		t.cards[dev] = nil
		for i := range t.steps {
			subs, _ := s.callSubjects(&t.steps[i])
			s.issue(ctx, t, dev, subs)
			s.live(dev, t.steps[i].call, subs)
		}
		for _, a := range t.attached {
			if a.step == "" && !a.waiting {
				s.issue(ctx, t, dev, []cards.Subject{a.subj})
				s.live(dev, cards.Call{}, []cards.Subject{a.subj})
			}
		}
		s.d.Log.Info("screen_turn_joined", "device_id", dev, "session_id", t.id, "cards", len(t.cards[dev]))
		s.sendTo(ctx, t, dev)
	}
}

// send puts the turn's state on each of its screens, its steps in each
// screen's language
func (s *Service) send(ctx context.Context, t *turn) {
	t.rev++
	for _, scr := range t.screens {
		s.sendTo(ctx, t, scr)
	}
}

// sendTo puts the turn's state on one screen, at the turn's revision
func (s *Service) sendTo(ctx context.Context, t *turn, scr string) {
	origin := t.origin
	if t.pressedBy != "" {
		origin = t.pressedBy
	}
	{
		lang := s.lang(ctx, scr)
		at := &cadencev2.AssistantTurn{
			TurnId: t.id, Revision: t.rev, OriginDeviceId: origin, Trigger: t.trigger, State: t.state, MicSource: t.mic,
			Cards: t.cards[scr], ErrorCode: t.errorCode, ConversationReset: t.reset, Failure: t.failure,
		}
		switch {
		case t.transcript != "":
			at.Transcript = &cadencev2.Transcript{Stable: t.transcript}
		case t.stable != "" || t.unstable != "":
			at.Transcript = &cadencev2.Transcript{Stable: t.stable, Unstable: t.unstable}
		}
		for _, st := range t.steps {
			at.Steps = append(at.Steps, &cadencev2.AssistantStep{Id: st.id, Label: s.stepLabel(lang, st.call), Status: st.status,
				CardId: st.cardID})
		}
		if t.reset {
			at.Reply = &cadencev2.AssistantReply{Text: s.d.Messages.Format(lang, "assistant.conversation_reset", nil)}
		} else if len(t.reply) > 0 {
			at.Reply = &cadencev2.AssistantReply{Text: strings.Join(t.reply, " ")}
		}
		if t.state == cadencev2.AssistantState_ASSISTANT_STATE_FOLLOW_UP {
			at.FollowUpWindowMs = t.window
		}
		_ = s.d.Send(scr, &cadencev2.Envelope{Body: &cadencev2.Envelope_AssistantTurn{AssistantTurn: at}})
	}
}

// end sends the turn's last state, lifts its duck and forgets it
func (s *Service) end(ctx context.Context, id string, t *turn) {
	s.send(ctx, t)
	s.duck(t, false)
	delete(s.turns, id)
	s.setPressed(id, "")
}

// duck lowers (or restores) the room's playing devices for the turn; the
// device applies it to its own mix, so it never raises a level (rule 7)
func (s *Service) duck(t *turn, active bool) {
	for _, dev := range t.ducked {
		d := &cadencev2.Duck{Active: active, RampMs: duckRampMs, Reason: cadencev2.DuckReason_DUCK_REASON_ASSISTANT, TurnId: t.id}
		if active {
			d.Level = DuckLevel
		}
		_ = s.d.Send(dev, &cadencev2.Envelope{Body: &cadencev2.Envelope_Duck{Duck: d}})
	}
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}
