package taskcards

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	cardsv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/cards/v1"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/pkg/rpc"
	"git.shino.boo/devilreef/cadence/server/internal/cards"
	"git.shino.boo/devilreef/cadence/server/internal/screens/calls"
	"git.shino.boo/devilreef/cadence/server/internal/settings"
	"git.shino.boo/devilreef/cadence/server/internal/tasks"
)

const (
	// a burst of task events is one new page
	settle = 500 * time.Millisecond
	// held toasts are looked at again this often
	recheck = time.Minute
	// a toast quiet hours held is dropped once this old
	holdFor   = tasks.ReportWindow
	maxHeld   = 32
	toastWait = 3 * time.Second
)

// Deps are the service's collaborators.
type Deps struct {
	Manager Manager
	// Cards renders pages and issues their actions
	Cards interface {
		Page(ctx context.Context, deviceID string, p *cards.Panel) (*cardsv1.Page, error)
	}
	// Screens are the screens a device's turns show on: its room's
	// (screens.Service.RoomScreens); nil sends no toasts
	Screens func(ctx context.Context, deviceID string) []string
	// Toast sends a display.screen toast to a screen
	Toast func(ctx context.Context, deviceID string, args []byte) error
	// Settings read a screen's quiet hours, time zone and language
	Settings func(ctx context.Context, deviceID string) (settings.Values, error)
	Messages cards.Messages
	Now      func() time.Time
	Log      *slog.Logger
}

// Service serves tasks.list and tasks.item and sends task toasts.
type Service struct {
	d  Deps
	ch *changes

	mu   sync.Mutex
	held map[string][]heldToast
	// when each screen last listed the tasks (tasks.list), from which Summary counts the finished ones
	opened   map[string]time.Time
	revision atomic.Uint64
	// changed hears that a screen's Summary may have changed ("": every screen's)
	changed func(deviceID string)
}

// OnSummaryChange has fn hear that Summary may answer otherwise: "" after a task's change (every screen's), a device
// after it listed the tasks (its finished count). fn must not block.
func (s *Service) OnSummaryChange(fn func(deviceID string)) {
	s.mu.Lock()
	s.changed = fn
	s.mu.Unlock()
}

func (s *Service) summaryChanged(dev string) {
	s.mu.Lock()
	fn := s.changed
	s.mu.Unlock()
	if fn != nil {
		fn(dev)
	}
}

type heldToast struct {
	key, title, short string
	at                time.Time
}

// New builds the service; Run follows the manager.
func New(d Deps) *Service {
	if d.Messages == nil {
		d.Messages = cards.Builtin
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Log == nil {
		d.Log = slog.Default()
	}
	return &Service{d: d, ch: newChanges(), held: map[string][]heldToast{}, opened: map[string]time.Time{}}
}

// Run follows the manager's events until ctx ends: they wake the cards on
// screen and send the toasts.
func (s *Service) Run(ctx context.Context) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	sub := s.d.Manager.Watch()
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(recheck)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case e := <-sub.C:
				l, ok := e.Data.(tasks.Live)
				if !ok {
					continue
				}
				s.ch.changed(l.ShortID)
				s.notice(ctx, l)
				s.summaryChanged("")
			case <-sub.Drops():
				// lost events: every card on screen looks again
				s.ch.changed("")
				s.summaryChanged("")
			case <-t.C:
				s.flush(ctx)
			}
		}
	}()
	return func() { cancel(); <-done; sub.Close() }
}

// ListMethod serves tasks.list {}: a page of task cards; subscribed, the page
// again (an RpcEvent with its page) after each change.
func (s *Service) ListMethod() calls.Method {
	return func(ctx context.Context, c calls.Call) calls.Reply {
		s.mu.Lock()
		s.opened[c.DeviceID] = s.d.Now()
		s.mu.Unlock()
		s.summaryChanged(c.DeviceID)
		page, err := s.listPage(ctx, c.DeviceID)
		if err != nil {
			return calls.Fail(rpc.StatusUnavailable, "unavailable")
		}
		return calls.Reply{Status: rpc.StatusOK, Page: page, Follow: s.follow("", func(ctx context.Context) (*cardsv1.Page, error) {
			return s.listPage(ctx, c.DeviceID)
		})}
	}
}

// summaryWindow bounds the finished tasks Summary counts for a screen that never listed them
const summaryWindow = 24 * time.Hour

// Summary is a screen's tasks indicator (the link's tasks_summary): the tasks running or queued, those waiting for an
// answer, and those ended since the screen last listed them (tasks.list; the last day for one that never did), each
// at most maxListed. Counts only: nothing private.
func (s *Service) Summary(ctx context.Context, deviceID string) (*cadencev2.TasksSummary, error) {
	open, err := s.d.Manager.List(ctx, tasks.Filter{
		Statuses: []string{tasks.StatusWaitingInput, tasks.StatusRunning, tasks.StatusQueued}, Limit: maxListed,
	})
	if err != nil {
		return nil, err
	}
	sum := &cadencev2.TasksSummary{}
	for _, t := range open {
		if t.Status == tasks.StatusWaitingInput {
			sum.Waiting++
		} else {
			sum.Running++
		}
	}
	done, err := s.d.Manager.List(ctx, tasks.Filter{
		Statuses: []string{tasks.StatusDone, tasks.StatusFailed, tasks.StatusCancelled}, Limit: maxListed,
	})
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	since, ok := s.opened[deviceID]
	s.mu.Unlock()
	if !ok {
		since = s.d.Now().Add(-summaryWindow)
	}
	for _, t := range done {
		if t.FinishedAt != nil && t.FinishedAt.After(since) {
			sum.Finished++
		}
	}
	sum.Revision = s.revision.Add(1)
	return sum, nil
}

func (s *Service) listPage(ctx context.Context, dev string) (*cardsv1.Page, error) {
	p, err := s.list(ctx)
	if err != nil {
		return nil, err
	}
	return s.d.Cards.Page(ctx, dev, p)
}

// ItemMethod serves tasks.item {taskId}: the task's panel; subscribed, it
// again after each change of the task.
func (s *Service) ItemMethod() calls.Method {
	return func(ctx context.Context, c calls.Call) calls.Reply {
		var a struct {
			TaskID string `json:"taskId"`
		}
		if json.Unmarshal(c.Args, &a) != nil || a.TaskID == "" || len(a.TaskID) > 64 {
			return calls.Fail(rpc.StatusInvalid, "invalid")
		}
		page, err := s.itemPage(ctx, c.DeviceID, a.TaskID)
		switch {
		case errors.Is(err, tasks.ErrNotFound):
			return calls.Fail(rpc.StatusNotFound, "not_found")
		case err != nil:
			return calls.Fail(rpc.StatusUnavailable, "unavailable")
		}
		return calls.Reply{Status: rpc.StatusOK, Page: page, Follow: s.follow(a.TaskID, func(ctx context.Context) (*cardsv1.Page, error) {
			return s.itemPage(ctx, c.DeviceID, a.TaskID)
		})}
	}
}

func (s *Service) itemPage(ctx context.Context, dev, short string) (*cardsv1.Page, error) {
	p, err := s.panel(ctx, short)
	if err != nil {
		return nil, err
	}
	return s.d.Cards.Page(ctx, dev, p)
}

// follow sends the page again after each change of short ("" any task)
func (s *Service) follow(short string, page func(context.Context) (*cardsv1.Page, error)) func(context.Context, *calls.Stream) {
	return func(ctx context.Context, st *calls.Stream) {
		for {
			if !wait(ctx, s.ch.of(short)) {
				return
			}
			// a burst is one page
			select {
			case <-time.After(settle):
			case <-ctx.Done():
				return
			}
			p, err := page(ctx)
			if err != nil {
				continue
			}
			if st.Event(nil, p) != nil {
				return
			}
		}
	}
}

// toastKind is the toast a task event asks for: a question, or an end
func toastKind(l tasks.Live) string {
	switch l.Kind {
	case "waiting_input":
		return "waiting"
	case "done":
		return "done"
	case "failed":
		return "failed"
	}
	return ""
}

func (s *Service) notice(ctx context.Context, l tasks.Live) {
	kind := toastKind(l)
	if kind == "" || s.d.Screens == nil || s.d.Toast == nil {
		return
	}
	t, err := s.d.Manager.Get(ctx, l.ShortID)
	if err != nil || t.Origin == uuid.Nil {
		return
	}
	for _, scr := range s.d.Screens(ctx, t.Origin.String()) {
		s.offer(ctx, scr, heldToast{key: kind, title: clip(t.Title, 120), short: t.ShortID, at: s.d.Now()})
	}
}

// offer sends a toast now, or holds it while the screen's quiet hours last
// (the rule voice announcements keep)
func (s *Service) offer(ctx context.Context, screen string, h heldToast) {
	v, err := s.d.Settings(ctx, screen)
	if err != nil {
		return
	}
	if v.QuietHours.Quiet(s.d.Now(), v.TimeZone) {
		s.mu.Lock()
		q := append(s.held[screen], h)
		if len(q) > maxHeld {
			q = q[len(q)-maxHeld:]
		}
		s.held[screen] = q
		s.mu.Unlock()
		return
	}
	s.send(ctx, screen, v.Language, h)
}

func (s *Service) send(ctx context.Context, screen, lang string, h heldToast) {
	kind := map[string]string{"waiting": "warning", "done": "success", "failed": "danger"}[h.key]
	toast := map[string]any{
		"text": clip(s.d.Messages.Format(lang, "task.toast."+h.key, map[string]string{"title": h.title}), 280),
		"kind": kind, "icon": "task",
	}
	if h.short != "" {
		// «Открыть» opens the task's panel on the screen itself (display.screen/toast's typed action)
		toast["action"] = map[string]string{
			"id": "open", "label": clip(s.d.Messages.Format(lang, "task.toast.open", nil), 40), "kind": "open",
			"route": "task", "ref": h.short,
		}
	}
	args, err := json.Marshal(toast)
	if err != nil {
		return
	}
	tctx, cancel := context.WithTimeout(ctx, toastWait)
	defer cancel()
	if err := s.d.Toast(tctx, screen, args); err != nil {
		s.d.Log.Info("task_toast_failed", "device_id", screen, "kind", h.key)
	}
}

// flush sends what quiet hours held once they end, and drops what grew old
func (s *Service) flush(ctx context.Context) {
	s.mu.Lock()
	held := s.held
	s.held = map[string][]heldToast{}
	s.mu.Unlock()
	now := s.d.Now()
	for screen, q := range held {
		v, err := s.d.Settings(ctx, screen)
		quiet := err != nil || v.QuietHours.Quiet(now, v.TimeZone)
		var keep []heldToast
		for _, h := range q {
			switch {
			case now.Sub(h.at) > holdFor:
			case quiet:
				keep = append(keep, h)
			default:
				s.send(ctx, screen, v.Language, h)
			}
		}
		if len(keep) > 0 {
			s.mu.Lock()
			s.held[screen] = append(keep, s.held[screen]...)
			s.mu.Unlock()
		}
	}
}
