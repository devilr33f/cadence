package cards

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	pcards "git.shino.boo/devilreef/cadence/pkg/cards"
	cardsv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/cards/v1"
	"git.shino.boo/devilreef/cadence/server/internal/skills"
)

// Call is one tool call of a turn as far as it went: running from the agent's tool_call until its tool_result, then
// done or failed. A call has at most one card of its own, whose id (CallCardID) stays the same through every state,
// so a screen replaces it in place; the call's step line is that card's title.
type Call struct {
	// Key is the turn's and the call's ids
	Key  string
	Tool string
	// DeviceID is the turn's device: the music and video results the call names are its
	DeviceID string
	// Args are the model's arguments: titles read enumerated fields, a query, an id
	Args json.RawMessage
	// Result is the call's end, nil while it runs
	Result *ToolResult
}

// Phase is how far a call went.
type Phase int

const (
	Running Phase = iota
	Done
	Failed
	// Asked: the call answered a question for the user (which voiceover), no failure: the question is the reply
	Asked
)

// Questions are the error codes that ask the user rather than fail
var Questions = map[string]bool{"choose_voiceover": true}

func (c Call) Phase() Phase {
	switch {
	case c.Result == nil:
		return Running
	case c.Result.OK:
		return Done
	case Questions[c.Result.Code]:
		return Asked
	}
	return Failed
}

// Attached is a card a tool attaches itself (its provider's whole results, a play's live card): ForTool names the
// tool whose call it is, so the call's step takes it as its card.
type Attached interface {
	Subject
	ForTool() string
}

// AsCall is an attached card under its call's own id (CallCardID): it replaces the call's pending card in place and
// keeps its own title; a live one stays live, with its follow time.
func AsCall(key string, s Subject) Subject {
	cc := &callCard{inner: s, id: CallCardID(key)}
	if _, ok := s.(Live); ok {
		return &liveCallCard{callCard: cc}
	}
	return cc
}

type liveCallCard struct{ *callCard }

func (c *liveCallCard) Changed(ctx context.Context) bool { return c.inner.(Live).Changed(ctx) }

// Follow is the inner card's own follow time, else none of its own
func (c *liveCallCard) Follow() time.Duration {
	if f, ok := c.inner.(interface{ Follow() time.Duration }); ok {
		return f.Follow()
	}
	return 0
}

// CallCardID is the id of a call's own card: the same while it runs, once done and once failed.
func CallCardID(key string) string { return OpaqueID("call", key) }

// lifecycle is how a tool's call shows in each phase; a tool without one has its step label while it runs and
// its result's cards (Subjects) once done
type lifecycle struct {
	// running and done answer the step line; nil is the tool's step label
	running func(t *ToolCards, rr *Render, c Call) string
	done    func(t *ToolCards, rr *Render, c Call) string
	// failed names what failed, before the reason; nil is running's line
	failed func(t *ToolCards, rr *Render, c Call) string
	// pending is the call's card while it runs (a play loading): with one, a failure replaces it with the reason
	pending func(t *ToolCards, c Call) cardBuild
	// result answers the done cards in place of Subjects
	result func(t *ToolCards, c Call) []Subject
	// content keeps the done card's own title: it shows found things (a showcase card), the step names the search
	content bool
	// icon is the pending card's icon, for a question's card
	icon string
}

// cardBuild draws a call card's block and fallback in a phase
type cardBuild func(rr *Render) (*cardsv1.CardBlock, string)

// Title is the call's step line in lang: what it does, then how it ended. The card the call shows in the chat
// carries the same title.
func (t *ToolCards) Title(m Messages, lang string, c Call) string {
	if m == nil {
		m = Builtin
	}
	return t.title(&Render{Lang: lang, Messages: m}, c)
}

func (t *ToolCards) title(rr *Render, c Call) string {
	lc := lifecycles[c.Tool]
	running := func() string {
		if lc.running != nil {
			if s := lc.running(t, rr, c); s != "" {
				return s
			}
		}
		return stepLabel(rr, c.Tool)
	}
	var s string
	switch c.Phase() {
	case Running, Asked:
		s = running()
	case Done:
		if lc.done != nil {
			s = lc.done(t, rr, c)
		}
		if s == "" {
			s = running()
		}
	default:
		what := ""
		if lc.failed != nil {
			what = lc.failed(t, rr, c)
		}
		if what == "" {
			what = running()
		}
		s = what + " · " + reason(rr, c.Result.Code)
	}
	return clip(s, 80)
}

// stepLabel is a tool's own step word; a tool the catalog does not name (an MCP server's) is a plain "working"
func stepLabel(rr *Render, tool string) string {
	key := "step." + tool
	if l := rr.T(key); l != key && l != "" {
		return l
	}
	if strings.HasPrefix(tool, "task") {
		return rr.T("step.task")
	}
	return rr.T("step.tool")
}

// reason is a failed call's code in words; never the tool's own message, which may carry outside text
func reason(rr *Render, code string) string {
	key := "reason." + code
	if s := rr.T(key); s != key && s != "" {
		return s
	}
	return rr.T("reason.failed")
}

// CallSubjects are the call's cards now, fresh subjects each call (one per screen), and the id of the call's own
// card among them ("" for none): its pending card while it runs, its result's cards once done (the first of them
// becomes the call's card, titled as its step), its reason once failed.
func (t *ToolCards) CallSubjects(c Call) ([]Subject, string) {
	lc := lifecycles[c.Tool]
	if pendingPlayResult(c) && lc.pending != nil {
		b := lc.pending(t, c)
		if b != nil {
			return t.called(c, &toolCard{id: CallCardID(c.Key), build: b}, false)
		}
	}
	switch c.Phase() {
	case Running:
		if lc.pending == nil {
			return nil, ""
		}
		b := lc.pending(t, c)
		if b == nil {
			return nil, ""
		}
		return t.called(c, &toolCard{id: CallCardID(c.Key), build: b}, false)
	case Asked:
		if lc.pending == nil {
			return nil, ""
		}
		return t.called(c, &toolCard{id: CallCardID(c.Key), build: t.asked(c)}, false)
	case Failed:
		if !Known(c.Tool) {
			return t.called(c, genericCard(*c.Result), true)
		}
		if lc.pending == nil {
			return nil, ""
		}
		return t.called(c, &toolCard{id: CallCardID(c.Key), build: t.failure(c)}, false)
	}
	r := *c.Result
	r.DeviceID = c.DeviceID
	var subs []Subject
	if lc.result != nil {
		subs = lc.result(t, c)
	} else {
		subs = t.Subjects(r)
	}
	if len(subs) > 0 {
		switch s := subs[0].(type) {
		case *toolCard, *ttlCard:
			out, id := t.called(c, s, lc.content)
			return append(out, subs[1:]...), id
		}
		// a live card of its own (an entity's, a task's): under the call's id, so it replaces the call's pending
		// card in place, with its own title and still live
		out, id := t.called(c, subs[0], true)
		return append(out, subs[1:]...), id
	}
	if lc.pending != nil {
		// the pending card must not keep spinning: it ends as a plain done
		return t.called(c, &toolCard{id: CallCardID(c.Key), build: func(rr *Render) (*cardsv1.CardBlock, string) {
			title := t.title(rr, c)
			return done(rr, title, "check", rr.T("tool.done"), cardsv1.StateKind_STATE_KIND_ON, ""), title
		}}, false)
	}
	return nil, ""
}

// pendingPlayResult keeps a play accepted but still resolving in its running
// presentation, even though its tool call has already answered.
func pendingPlayResult(c Call) bool {
	if c.Tool != "music_play" && c.Tool != "video_play" {
		return false
	}
	var out struct {
		Pending bool `json:"pending"`
	}
	return c.data(&out) && out.Pending
}

// called is s as the call's own card: its id, and the step's title unless keepTitle; a live s stays live
func (t *ToolCards) called(c Call, s Subject, keepTitle bool) ([]Subject, string) {
	cc := &callCard{inner: s, id: CallCardID(c.Key)}
	if !keepTitle {
		cc.title = func(rr *Render) string { return t.title(rr, c) }
	}
	if _, ok := s.(Live); ok {
		return []Subject{&liveCallCard{callCard: cc}}, cc.id
	}
	return []Subject{cc}, cc.id
}

// failure is a call card that failed: what it was, and why
func (t *ToolCards) failure(c Call) cardBuild {
	lc := lifecycles[c.Tool]
	var media cardBuild
	if lc.pending != nil {
		media = lc.pending(t, Call{Key: c.Key, Tool: c.Tool, DeviceID: c.DeviceID, Args: c.Args})
	}
	return func(rr *Render) (*cardsv1.CardBlock, string) {
		title := t.title(rr, c)
		why := reason(rr, c.Result.Code)
		b := &cardsv1.CardBlock{Title: title, Icon: "warning", Tone: cardsv1.Tone_TONE_WARNING}
		if media != nil {
			if mb, _ := media(rr); mb != nil {
				b.Icon = mb.GetIcon()
				// what it was stays: the cover and its lines, without the spinner
				for _, n := range mb.GetChildren() {
					if n.GetProgress() == nil {
						b.Children = append(b.Children, n)
					}
				}
			}
		}
		b.Children = append(b.Children, pcards.N(&cardsv1.State{Label: clip(why, 40), Kind: cardsv1.StateKind_STATE_KIND_UNAVAILABLE}))
		return b, title
	}
}

// asked is a call card waiting on the user's answer: what it was, and the question, without the spinner
func (t *ToolCards) asked(c Call) cardBuild {
	fail := t.failure(c)
	return func(rr *Render) (*cardsv1.CardBlock, string) {
		b, title := fail(rr)
		b.Icon, b.Tone = lifecycles[c.Tool].icon, cardsv1.Tone_TONE_NEUTRAL
		if n := len(b.Children); n > 0 {
			if st := b.Children[n-1].GetState(); st != nil {
				st.Kind = cardsv1.StateKind_STATE_KIND_UNKNOWN
			}
		}
		return b, title
	}
}

// callCard is a call's own card: another subject under the call's id, its title the call's step line
type callCard struct {
	inner Subject
	id    string
	title func(rr *Render) string
}

func (c *callCard) ID() string { return c.id }

func (c *callCard) Render(ctx context.Context, r *Render) (*cardsv1.Card, error) {
	card, err := c.inner.Render(ctx, r)
	if err != nil || card == nil {
		return card, err
	}
	if b := card.GetRoot().GetCard(); b != nil && c.title != nil {
		if t := clip(c.title(r), 80); t != "" {
			b.Title = t
		}
	}
	return card, nil
}

// args reads the call's arguments, false when they are not an object
func (c Call) args(into any) bool { return len(c.Args) > 0 && json.Unmarshal(c.Args, into) == nil }

// data reads a done call's output
func (c Call) data(into any) bool {
	return c.Result != nil && len(c.Result.Data) > 0 && json.Unmarshal(c.Result.Data, into) == nil
}

// quoted is a query or a name as a step quotes it, «» around at most 48 characters
func quoted(s string) string {
	s = clip(s, 48)
	if s == "" {
		return ""
	}
	return "«" + s + "»"
}

// counted is n things in the language's plural form: the catalog's <noun>.one, .few and .many
func counted(rr *Render, n int, noun string) string {
	form := "many"
	switch {
	case rr.Lang != "" && strings.HasPrefix(strings.ToLower(rr.Lang), "ru"):
		m10, m100 := n%10, n%100
		switch {
		case m10 == 1 && m100 != 11:
			form = "one"
		case m10 >= 2 && m10 <= 4 && (m100 < 12 || m100 > 14):
			form = "few"
		}
	case n == 1:
		form = "one"
	}
	return rr.T("count."+noun+"."+form, "n", strconv.Itoa(n))
}

// searching is a search's lines: «<step> «query»» running, «<step> · N results» done
func searching(noun, field string) (running, done func(*ToolCards, *Render, Call) string) {
	running = func(_ *ToolCards, rr *Render, c Call) string {
		var a map[string]json.RawMessage
		var q string
		if c.args(&a) {
			_ = json.Unmarshal(a["query"], &q)
		}
		if q := quoted(q); q != "" {
			return stepLabel(rr, c.Tool) + " " + q
		}
		return ""
	}
	done = func(_ *ToolCards, rr *Render, c Call) string {
		var out map[string]json.RawMessage
		if !c.data(&out) {
			return ""
		}
		var list []json.RawMessage
		if json.Unmarshal(out[field], &list) != nil {
			return ""
		}
		if len(list) == 0 {
			return stepLabel(rr, c.Tool) + " · " + rr.T("call.nothing")
		}
		return stepLabel(rr, c.Tool) + " · " + counted(rr, len(list), noun)
	}
	return running, done
}

var lifecycles map[string]lifecycle

func init() {
	musicRun, musicDone := searching("results", "results")
	videoRun, videoDone := searching("results", "results")
	catRun, catDone := searching("results", "items")
	ytRun, ytDone := searching("results", "items")
	webRun, webDone := searching("results", "results")
	xRun, xDone := searching("posts", "posts")
	lifecycles = map[string]lifecycle{
		"music_search":    {running: musicRun, done: musicDone, result: musicSearchResult, content: true},
		"music_favorites": {running: musicRun, done: musicDone, result: musicSearchResult, content: true},
		"music_feedback":  {pending: plainPending("bookmark"), icon: "bookmark"},
		"music_play": {running: musicPlayRunning, done: musicPlayDone, failed: musicPlayFailed, pending: musicPlayPending,
			result: musicPlayResult, icon: "music"},
		"load_skill":     {running: loadSkillRunning},
		"video_search":   {running: videoRun, done: videoDone, content: true},
		"video_play":     {done: videoPlayDone, pending: plainPending("film"), icon: "film"},
		"youtube_search": {running: ytRun, done: ytDone, content: true},
		// the browse's card is titled by what it opened, the channel or playlist itself
		"youtube_browse": {done: youtubeBrowseDone, content: true},
		"catalog_search": {running: catRun, done: catDone, content: true},
		"catalog_item":   {done: catalogItemDone, content: true},
		// the play's own live card, attached by its tool, replaces the pending one (screens' attach)
		"catalog_play":         {done: catalogPlayDone, pending: plainPending("tv"), icon: "tv"},
		"catalog_alternatives": {done: catalogAltsDone},
		"web_search":           {running: webRun, done: webDone},
		"x_search":             {running: xRun, done: xDone},
		// the chat's cards: each a pending card from the call on, replaced in place by its result or its reason
		"weather":           {done: weatherDone, pending: plainPending("sun"), icon: "sun"},
		"timer":             {running: timerRunning, done: timerDone, pending: plainPending("timer"), icon: "timer"},
		"alarm":             {running: alarmRunning, done: alarmDone, pending: plainPending("alarm"), icon: "alarm"},
		"device_volume":     {done: volumeDone, pending: plainPending("volume"), icon: "volume"},
		"do_not_disturb":    {done: dndDone, pending: plainPending("moon"), icon: "moon"},
		"agent_mode":        {done: modeDone, pending: plainPending("sparkle"), icon: "sparkle"},
		"media_control":     {running: mediaRunning, done: mediaDone, pending: plainPending("music"), icon: "music", result: mediaResult},
		"media_switch":      {pending: plainPending("settings"), icon: "settings"},
		"media_item_action": {pending: plainPending("bookmark"), icon: "bookmark"},
		"ha_control":        {pending: plainPending("home"), icon: "home"},
		"ha_inspect":        {pending: plainPending("home"), icon: "home"},
		"task_start":        {pending: plainPending("task"), icon: "task"},
		"task_reply":        {pending: plainPending("task"), icon: "task"},
		"task_cancel":       {pending: plainPending("task"), icon: "task"},
	}
}

// loadSkillRunning names the instruction pack, never a generic «tool» card
func loadSkillRunning(_ *ToolCards, rr *Render, c Call) string {
	var a struct {
		Name string `json:"name"`
	}
	if !c.args(&a) || a.Name == "" {
		return stepLabel(rr, c.Tool)
	}
	name := skills.DisplayName(a.Name, rr.Lang)
	return stepLabel(rr, c.Tool) + " · " + clip(name, 48)
}

func videoPlayDone(_ *ToolCards, rr *Render, c Call) string {
	if pendingPlayResult(c) {
		return stepLabel(rr, c.Tool)
	}
	return rr.T("call.video_play.done")
}

// plainPending is a play loading with nothing to show of it yet: its line and a spinner
func plainPending(icon string) func(*ToolCards, Call) cardBuild {
	return func(t *ToolCards, c Call) cardBuild {
		return func(rr *Render) (*cardsv1.CardBlock, string) {
			title := t.title(rr, c)
			return &cardsv1.CardBlock{Title: title, Icon: icon, Tone: cardsv1.Tone_TONE_NEUTRAL,
				Children: pcards.Nodes(&cardsv1.Progress{Amount: &cardsv1.Progress_Indeterminate{Indeterminate: true}})}, title
		}
	}
}

func catalogItemDone(_ *ToolCards, rr *Render, c Call) string {
	var out struct {
		Title string `json:"title"`
	}
	if !c.data(&out) || quoted(out.Title) == "" {
		return ""
	}
	return rr.T("call.catalog_item.done", "title", quoted(out.Title))
}

// youtubeBrowseDone names what the browse opened, «Смотрю YouTube · «Купли Барамидзе»»
func youtubeBrowseDone(_ *ToolCards, rr *Render, c Call) string {
	var out struct {
		Item struct {
			Title string `json:"title"`
		} `json:"item"`
	}
	if !c.data(&out) || quoted(out.Item.Title) == "" {
		return ""
	}
	return stepLabel(rr, c.Tool) + " " + quoted(out.Item.Title)
}

func catalogAltsDone(_ *ToolCards, rr *Render, c Call) string {
	var out struct {
		Items []json.RawMessage `json:"items"`
	}
	if !c.data(&out) || len(out.Items) == 0 {
		return ""
	}
	return rr.T("catalog.alternatives") + " · " + strconv.Itoa(len(out.Items))
}

func catalogPlayDone(_ *ToolCards, rr *Render, c Call) string {
	var out struct {
		Episode string `json:"episode"`
	}
	s := rr.T("call.catalog_play.done")
	if c.data(&out) && clip(out.Episode, 32) != "" {
		s += " · " + rr.T("catalog.episode", "n", clip(out.Episode, 32))
	}
	return s
}

func weatherDone(_ *ToolCards, rr *Render, c Call) string {
	var out struct {
		Place string `json:"place"`
	}
	if !c.data(&out) || clip(out.Place, 60) == "" {
		return ""
	}
	return rr.T("weather.title", "place", clip(out.Place, 60))
}

func timerRunning(_ *ToolCards, rr *Render, c Call) string {
	var a struct {
		Action  string `json:"action"`
		Seconds int    `json:"seconds"`
	}
	if !c.args(&a) {
		return ""
	}
	switch a.Action {
	case "set":
		if a.Seconds > 0 {
			return rr.T("call.timer.setting", "length", durationLabel(rr, a.Seconds))
		}
	case "list":
		return rr.T("call.timer.listing")
	case "cancel":
		return rr.T("call.timer.cancelling")
	}
	return ""
}

func timerDone(_ *ToolCards, rr *Render, c Call) string {
	var out struct {
		Timer     *timerOut  `json:"timer"`
		Timers    []timerOut `json:"timers"`
		Cancelled []timerOut `json:"cancelled"`
	}
	if !c.data(&out) {
		return ""
	}
	switch {
	case out.Timer != nil:
		return rr.T("timer.fallback", "length", durationLabel(rr, out.Timer.Seconds))
	case out.Timers != nil:
		if len(out.Timers) == 0 {
			return rr.T("timer.none")
		}
		return rr.T("timer.count", "n", strconv.Itoa(len(out.Timers)))
	case out.Cancelled != nil:
		return rr.T("call.timer.cancelled")
	}
	return ""
}

func alarmRunning(_ *ToolCards, rr *Render, c Call) string {
	var a struct {
		Action string `json:"action"`
		Time   string `json:"time"`
	}
	if !c.args(&a) {
		return ""
	}
	switch a.Action {
	case "set":
		if t := clip(a.Time, 5); t != "" {
			return rr.T("call.alarm.setting", "time", t)
		}
	case "list":
		return rr.T("call.alarm.listing")
	case "cancel":
		return rr.T("call.alarm.cancelling")
	}
	return ""
}

func alarmDone(_ *ToolCards, rr *Render, c Call) string {
	var out struct {
		Alarm *struct {
			Time string `json:"time"`
		} `json:"alarm"`
		Alarms    []json.RawMessage `json:"alarms"`
		Cancelled []json.RawMessage `json:"cancelled"`
	}
	if !c.data(&out) {
		return ""
	}
	switch {
	case out.Alarm != nil && clip(out.Alarm.Time, 5) != "":
		return rr.T("call.alarm.set", "time", clip(out.Alarm.Time, 5))
	case out.Alarms != nil:
		if len(out.Alarms) == 0 {
			return rr.T("alarm.none")
		}
		return rr.T("alarm.count", "n", strconv.Itoa(len(out.Alarms)))
	case out.Cancelled != nil:
		return rr.T("call.alarm.cancelled")
	}
	return ""
}

func volumeDone(_ *ToolCards, rr *Render, c Call) string {
	var out struct {
		Level *int `json:"level"`
	}
	if !c.data(&out) || out.Level == nil {
		return ""
	}
	return rr.T("call.volume.done", "n", strconv.Itoa(min(max(*out.Level, 0), 100)))
}

func dndDone(_ *ToolCards, rr *Render, c Call) string {
	var out struct {
		On    bool   `json:"on"`
		Until string `json:"until"`
	}
	if !c.data(&out) {
		return ""
	}
	if out.On && clip(out.Until, 5) != "" {
		return rr.T("dnd.fallback", "until", clip(out.Until, 5))
	}
	return rr.T("dnd.title") + " · " + rr.T("dnd.off")
}

func modeDone(_ *ToolCards, rr *Render, c Call) string {
	var out struct {
		Mode string `json:"mode"`
	}
	if !c.data(&out) || clip(out.Mode, 24) == "" {
		return ""
	}
	return rr.T("mode.title") + ": " + clip(out.Mode, 24)
}
