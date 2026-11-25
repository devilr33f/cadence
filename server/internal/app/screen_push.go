package app

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	"git.shino.boo/devilreef/cadence/pkg/capschema"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/server/internal/devices"
	"git.shino.boo/devilreef/cadence/server/internal/events"
	"git.shino.boo/devilreef/cadence/server/internal/media"
	"git.shino.boo/devilreef/cadence/server/internal/settings"
)

const (
	// task changes come in bursts: one summary per screen after the burst
	summaryDebounce = 300 * time.Millisecond
	pushTimeout     = 5 * time.Second
)

// screenPush is what the server sends a screen of its own accord: the server-owned fields of the session it plays
// (media_session_state: the music queue's, a catalog session's), the tasks indicator (tasks_summary), the name and
// room the owner gave it (DeviceInfo), and the language setting, merged into its display.screen config. Each goes at
// the link's start and on change; nothing of it is logged but ids and counts.
type screenPush struct {
	send func(deviceID string, env *cadencev2.Envelope) error
	// isScreen is whether a device reports display.screen
	isScreen func(ctx context.Context, deviceID string) bool
	// fields are the session's server-owned fields, the first source that has them (the music queue, the catalog
	// sessions)
	fields []func(deviceID string) (*cadencev2.MediaSessionState, bool)
	// summary is a screen's tasks indicator; screens lists the linked devices
	summary func(ctx context.Context, deviceID string) (*cadencev2.TasksSummary, error)
	screens func(ctx context.Context) []string
	// info is the device's name and room
	info func(ctx context.Context, deviceID string) *cadencev2.DeviceInfo
	// the language setting's way into display.screen's desired config
	effective func(ctx context.Context, deviceID string) (settings.Values, error)
	desired   func(ctx context.Context, deviceID, capability string) (json.RawMessage, error)
	setConfig func(ctx context.Context, deviceID, capability string, cfg json.RawMessage) (int64, error)
	log       *slog.Logger

	mu sync.Mutex
	// the fields last sent, by device
	sent map[string]*cadencev2.MediaSessionState
	// devices whose summary is due ("" every screen), and the kick of the loop
	due  map[string]bool
	kick chan struct{}
}

func (p *screenPush) init() *screenPush {
	p.sent, p.due, p.kick = map[string]*cadencev2.MediaSessionState{}, map[string]bool{}, make(chan struct{}, 1)
	return p
}

func (p *screenPush) screen(ctx context.Context, dev string) bool {
	return p.isScreen != nil && p.isScreen(ctx, dev)
}

// Linked sends a screen what it lacks after a new link: the session's fields again and its tasks summary (the
// DeviceInfo is the welcome's).
func (p *screenPush) Linked(dev string) {
	p.mu.Lock()
	delete(p.sent, dev)
	p.mu.Unlock()
	p.FieldsChanged(dev)
	p.SummaryChanged(dev)
}

// FieldsChanged sends the device's session fields when they differ from those it last got; it never blocks on the
// device.
func (p *screenPush) FieldsChanged(dev string) {
	var st *cadencev2.MediaSessionState
	for _, f := range p.fields {
		if s, ok := f(dev); ok {
			st = s
			break
		}
	}
	p.mu.Lock()
	last := p.sent[dev]
	if st == nil || proto.Equal(st, last) {
		if st == nil {
			delete(p.sent, dev)
		}
		p.mu.Unlock()
		return
	}
	p.sent[dev] = st
	p.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), pushTimeout)
	defer cancel()
	if !p.screen(ctx, dev) {
		return
	}
	if err := p.send(dev, &cadencev2.Envelope{Body: &cadencev2.Envelope_MediaSessionState{MediaSessionState: st}}); err != nil {
		// not linked: the next link sends them
		p.mu.Lock()
		delete(p.sent, dev)
		p.mu.Unlock()
	}
}

// SummaryChanged has the device's tasks summary sent soon ("" every linked screen's).
func (p *screenPush) SummaryChanged(dev string) {
	p.mu.Lock()
	p.due[dev] = true
	p.mu.Unlock()
	select {
	case p.kick <- struct{}{}:
	default:
	}
}

// Run sends the summaries due, a burst of task changes at once, until ctx ends.
func (p *screenPush) Run(ctx context.Context) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-ctx.Done():
				return
			case <-p.kick:
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(summaryDebounce):
			}
			p.flushSummaries(ctx)
		}
	}()
	return func() { cancel(); <-done }
}

func (p *screenPush) flushSummaries(ctx context.Context) {
	p.mu.Lock()
	due := p.due
	p.due = map[string]bool{}
	p.mu.Unlock()
	if p.summary == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, pushTimeout)
	defer cancel()
	devs := map[string]bool{}
	for d := range due {
		if d != "" {
			devs[d] = true
		}
	}
	if due[""] && p.screens != nil {
		for _, d := range p.screens(ctx) {
			devs[d] = true
		}
	}
	for d := range devs {
		if !p.screen(ctx, d) {
			continue
		}
		sum, err := p.summary(ctx, d)
		if err != nil {
			p.log.Warn("tasks_summary_failed", "device_id", d)
			continue
		}
		_ = p.send(d, &cadencev2.Envelope{Body: &cadencev2.Envelope_TasksSummary{TasksSummary: sum}})
	}
}

// Renamed sends a linked device its new name and room (the bus's device_renamed: a rename or a room set).
func (p *screenPush) Renamed(dev string) {
	if p.info == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), pushTimeout)
	defer cancel()
	if info := p.info(ctx, dev); info != nil {
		_ = p.send(dev, &cadencev2.Envelope{Body: &cadencev2.Envelope_DeviceInfo{DeviceInfo: info}})
	}
}

// WatchRenames follows the bus's device_renamed until stop.
func (p *screenPush) WatchRenames(bus *events.Bus) (stop func()) {
	sub := bus.Watch(func(e events.Event) bool { return e.Type == events.DeviceRenamed })
	quit, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-quit:
				return
			case e := <-sub.C:
				p.Renamed(e.DeviceID)
			}
		}
	}()
	return func() { close(quit); <-done; sub.Close() }
}

// screenLanguages are the languages display.screen's config takes
var screenLanguages = map[string]bool{"en": true, "ru": true}

// LanguageChanged puts the device's language setting into its desired display.screen config when it differs, as
// settings.set does, so config sync hands it to the screen; a device without display.screen, or a language the screen
// does not take, is left alone. It runs in the background: the settings' writers must not wait for it.
func (p *screenPush) LanguageChanged(dev string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), pushTimeout)
		defer cancel()
		p.syncLanguage(ctx, dev)
	}()
}

// AllLanguagesChanged is LanguageChanged for every linked screen (a global set).
func (p *screenPush) AllLanguagesChanged() {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*pushTimeout)
		defer cancel()
		if p.screens == nil {
			return
		}
		for _, d := range p.screens(ctx) {
			p.syncLanguage(ctx, d)
		}
	}()
}

func (p *screenPush) syncLanguage(ctx context.Context, dev string) {
	if p.effective == nil || p.desired == nil || p.setConfig == nil {
		return
	}
	v, err := p.effective(ctx, dev)
	if err != nil || !screenLanguages[v.Language] {
		return
	}
	cur, err := p.desired(ctx, dev, capschema.DisplayScreenID)
	if errors.Is(err, devices.ErrUnknownCapability) || err != nil {
		return
	}
	var have struct {
		Language string `json:"language"`
	}
	if len(cur) > 0 && string(cur) != "null" && json.Unmarshal(cur, &have) != nil {
		return
	}
	if have.Language == v.Language {
		return
	}
	cfg, err := mergeScreen(cur, settingsSetArgs{Language: v.Language})
	if err != nil {
		return
	}
	if _, err := p.setConfig(ctx, dev, capschema.DisplayScreenID, cfg); err != nil {
		p.log.Warn("screen_language_failed", "device_id", dev)
		return
	}
	p.log.Info("screen_language_set", "device_id", dev)
}

// isScreen is whether a device reports display.screen
func isScreen(state func(ctx context.Context, deviceID, capability string) (json.RawMessage, bool)) func(context.Context, string) bool {
	return func(ctx context.Context, id string) bool {
		_, ok := state(ctx, id, capschema.DisplayScreenID)
		return ok
	}
}

// linkedDevices lists the enrolled devices linked now
func linkedDevices(all func(context.Context) []string, online func(string) bool) func(context.Context) []string {
	return func(ctx context.Context) []string {
		var out []string
		for _, id := range all(ctx) {
			if online(id) {
				out = append(out, id)
			}
		}
		return out
	}
}

// deviceInfo is a device's name and room as the owner set them; the room's name is its id (rooms are named, W0-C 9a)
func deviceInfo(dir deviceRooms) func(context.Context, string) *cadencev2.DeviceInfo {
	return func(ctx context.Context, id string) *cadencev2.DeviceInfo {
		room := dir.Room(ctx, id)
		return &cadencev2.DeviceInfo{Name: dir.Name(ctx, id), RoomId: room, RoomName: room}
	}
}

// currentSession is the device's session now and its position, run forward from the device's stamp while it plays
func currentSession(sessions *media.Store, now func() time.Time) func(string) (string, int64, bool) {
	return func(dev string) (string, int64, bool) {
		s, ok := sessions.Raw(dev)
		if !ok || s.SessionID == "" {
			return "", 0, false
		}
		pos := int64(min(s.PositionMs, 1<<40)) //nolint:gosec // bounded above
		if at, err := time.Parse(time.RFC3339Nano, s.PositionAt); err == nil && s.State == "playing" {
			if d := now().Sub(at); d > 0 && d < 24*time.Hour {
				pos += d.Milliseconds()
			}
		}
		return s.SessionID, pos, true
	}
}
