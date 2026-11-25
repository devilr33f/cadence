package cards

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	pcards "git.shino.boo/devilreef/cadence/pkg/cards"
	cardsv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/cards/v1"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/agent/tools"
	"git.shino.boo/devilreef/cadence/server/internal/integrations/ha"
)

// ScreenTrigger is the policy kind of a press on a screen's remote (W0-C item
// 12's trigger kind screen, server-side only: the link has no such trigger):
// a deliberate press, so HASensitivePolicy treats it as a pressed session.
// Sensitive entities still follow haSensitive (deny refuses), and every
// control of one carries an on-screen confirm.
const ScreenTrigger = cadencev2.Trigger_TRIGGER_BUTTON

const (
	// haSettle is how long an action waits for the cache to show what it did
	haSettle = time.Second
	// haCallTimeout bounds the service call
	haCallTimeout = 5 * time.Second
	// cadencePlatform is Cadence's own integration: its entities (a speaker's
	// volume) are never controlled through Home Assistant (rule 7, M5c-R8)
	cadencePlatform = "cadence"
	haNameRunes     = 80
)

// HADeps are the Home Assistant cards' collaborators: the same home, policy
// and usage as the agent's ha_control.
type HADeps struct {
	Home   *ha.Home
	Policy tools.HAPolicy
	// Usage nil counts nothing
	Usage tools.HAUsage
	// Confirmations is the same tracker ha_control uses; nil leaves ordinary
	// entity cards without a late-confirmation overlay.
	Confirmations *tools.HAConfirmations
}

// Entity is a card of one Home Assistant entity: its state, and the controls
// its domain takes. Each call answers a fresh subject: one per screen.
func (d HADeps) Entity(entityID string) Subject {
	return &haEntity{d: d, id: entityID}
}

type haEntity struct {
	d  HADeps
	id string
	// confirmation is set only on a ha_control result's card. initial keeps
	// its pending state renderable if the process no longer knows the operation.
	confirmation string
	initial      tools.HAConfirmationSnapshot

	mu   sync.Mutex
	last string
}

func (s *haEntity) ID() string { return OpaqueID("ha", s.id) }

// fingerprint is what the card shows of an entity; a change of it is a new
// revision
func fingerprint(e ha.Entity, ok bool) string {
	if !ok {
		return "-"
	}
	f := func(p *float64) string {
		if p == nil {
			return ""
		}
		return strconv.FormatFloat(*p, 'f', 2, 64)
	}
	return strings.Join([]string{e.State, e.Name, f(e.Attrs.Brightness), f(e.Attrs.Temperature),
		f(e.Attrs.CurrentTemperature), f(e.Attrs.MinTemp), f(e.Attrs.MaxTemp),
		f(e.Attrs.TargetTempLow), f(e.Attrs.TargetTempHigh)}, "\x00")
}

// Changed waits for the entity or this call's confirmation to show something
// new. The channels are taken before the fingerprint, so a change between the
// read and the wait closes one of them rather than getting lost.
func (s *haEntity) Changed(ctx context.Context) bool {
	if s.d.Home == nil {
		<-ctx.Done()
		return false
	}
	wait := time.Hour
	if dl, ok := ctx.Deadline(); ok {
		wait = time.Until(dl)
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		_, _, homeNext := s.d.Home.Watch(nil)
		var confirmationNext <-chan struct{}
		if s.confirmation != "" {
			_, confirmationNext, _ = s.d.Confirmations.Watch(s.confirmation)
		}
		e, ok := s.entity()
		now := s.seenFingerprint(e, ok)
		s.mu.Lock()
		last := s.last
		s.mu.Unlock()
		if now != last {
			return true
		}
		select {
		case <-homeNext:
		case <-confirmationNext:
		case <-timer.C:
			return false
		case <-ctx.Done():
			return false
		}
	}
}

func (s *haEntity) entity() (ha.Entity, bool) {
	if s.d.Home == nil {
		return ha.Entity{}, false
	}
	v, err := s.d.Home.View()
	if err != nil {
		return ha.Entity{}, false
	}
	return v.ByID(s.id)
}

// confirmationSnapshot prefers the tracker's terminal state to the pending
// snapshot carried by the tool result.
func (s *haEntity) confirmationSnapshot() (tools.HAConfirmationSnapshot, bool) {
	if s.confirmation == "" {
		return tools.HAConfirmationSnapshot{}, false
	}
	if snap, _, ok := s.d.Confirmations.Watch(s.confirmation); ok {
		return snap, true
	}
	return s.initial, s.initial.Phase != ""
}

func (s *haEntity) seenFingerprint(e ha.Entity, ok bool) string {
	f := fingerprint(e, ok)
	if snap, ok := s.confirmationSnapshot(); ok {
		f += "\x00" + string(snap.Phase) + "\x00" + snap.State + "\x00" + snap.Expected
	}
	return f
}

func name(e ha.Entity) string {
	if n := ha.Sanitize(e.Name, haNameRunes); n != "" {
		return n
	}
	return truncRunes(e.ID, haNameRunes)
}

// controllable: never a Cadence speaker's own entity, nor one whose
// platform the registry cannot show (it may be one, M8-D6)
func controllable(e ha.Entity) bool {
	return e.Platform != cadencePlatform && !e.PlatformUnknown && !e.EntryUnreadable && e.State != "unavailable"
}

func (s *haEntity) Render(_ context.Context, r *Render) (out *cardsv1.Card, err error) {
	e, ok := s.entity()
	s.mu.Lock()
	s.last = s.seenFingerprint(e, ok)
	s.mu.Unlock()
	defer func() {
		if err == nil && out != nil {
			s.renderConfirmation(out, r)
		}
	}()
	if !ok {
		state := r.T("ha.unavailable")
		block := &cardsv1.CardBlock{Title: state, Icon: "home", Tone: cardsv1.Tone_TONE_NEUTRAL,
			Children: pcards.Nodes(&cardsv1.State{Label: state, Kind: cardsv1.StateKind_STATE_KIND_UNAVAILABLE})}
		return pcards.NewCard("", 0, state, block), nil
	}
	n := name(e)
	block := &cardsv1.CardBlock{Title: n, Icon: haIcon(e), Tone: cardsv1.Tone_TONE_NEUTRAL}
	stateLabel, kind := haState(r, e)
	if e.Domain == "water_heater" {
		stateLabel, kind = heaterWord(r, e), heaterKind(e)
	}
	switch kind {
	case cardsv1.StateKind_STATE_KIND_ON:
		// what is on lights its icon; the word says which
		block.Tone = cardsv1.Tone_TONE_SUCCESS
	case cardsv1.StateKind_STATE_KIND_OPEN, cardsv1.StateKind_STATE_KIND_UNLOCKED:
		block.Tone = cardsv1.Tone_TONE_WARNING
	default:
	}
	can := controllable(e)
	confirm := tools.Sensitive(e)
	bind := func(b Binding, question string) *cardsv1.Action {
		if confirm {
			return r.BindConfirm(b, question)
		}
		return r.Bind(b)
	}
	state := func() *cardsv1.Node { return pcards.N(&cardsv1.State{Label: stateLabel, Kind: kind}) }
	switch {
	case can && toggles(e.Domain):
		on := e.State == "on"
		service := "turn_on"
		if on {
			service = "turn_off"
		}
		// the toggle's label is the state's whole word: the screen shows it beside the switch, once
		block.Children = append(block.Children, pcards.N(&cardsv1.Toggle{
			Label: stateLabel, On: on,
			Action: bind(s.call("homeassistant", service, nil, "toggle"), r.T("ha.confirm.toggle", "name", n)),
		}))
		if e.Domain == "light" && on && e.Attrs.Brightness != nil {
			pct := math.Round(*e.Attrs.Brightness / 255 * 100)
			block.Children = append(block.Children, pcards.N(&cardsv1.Slider{
				Value: pct, Min: 0, Max: 100, Step: 5, Unit: "%", Label: r.T("ha.brightness"),
				ValueLabel: fmt.Sprintf("%d %%", int(pct)),
				Action:     r.Bind(s.brightness()),
			}))
		}
	case can && e.Domain == "water_heater":
		// like home assistant's own dialog: the water now, then whether it
		// heats and in which mode, then the target on its true scale
		if cur := e.Attrs.CurrentTemperature; cur != nil {
			block.Children = append(block.Children, pcards.N(&cardsv1.Value{Value: decimal(*cur, r.Lang),
				Unit: "°C", Label: r.T("ha.now")}))
		}
		block.Children = append(block.Children, state())
		if target := e.Attrs.Temperature; target != nil {
			if lo, hi := e.Attrs.MinTemp, e.Attrs.MaxTemp; lo != nil && hi != nil && *hi > *lo {
				t := clamp(*target, *lo, *hi)
				block.Children = append(block.Children, pcards.N(&cardsv1.Slider{
					Value: t, Min: *lo, Max: *hi, Step: 1, Unit: "°C", Label: r.T("ha.heat_to"),
					ValueLabel: decimal(t, r.Lang) + " °C", Action: r.Bind(s.temperature(e.Domain)),
				}))
			} else {
				block.Children = append(block.Children, pcards.N(&cardsv1.Value{Value: decimal(*target, r.Lang),
					Unit: "°C", Label: r.T("ha.heat_to")}))
			}
		}
	case can && e.Domain == "climate":
		// the room now and the mode in one row; the target is the slider's
		row := &cardsv1.Row{Gap: cardsv1.Gap_GAP_L}
		if cur := e.Attrs.CurrentTemperature; cur != nil {
			row.Children = append(row.Children, pcards.N(&cardsv1.Value{Value: decimal(*cur, r.Lang), Unit: "°C",
				Label: r.T("ha.now")}))
		}
		row.Children = append(row.Children, state())
		block.Children = append(block.Children, pcards.N(row))
		switch {
		case e.Attrs.Temperature != nil && e.Attrs.MinTemp != nil && e.Attrs.MaxTemp != nil && *e.Attrs.MaxTemp > *e.Attrs.MinTemp:
			lo, hi := *e.Attrs.MinTemp, *e.Attrs.MaxTemp
			t := clamp(*e.Attrs.Temperature, lo, hi)
			block.Children = append(block.Children, pcards.N(&cardsv1.Slider{
				Value: t, Min: lo, Max: hi, Step: 0.5, Unit: "°C", Label: r.T("ha.target"),
				ValueLabel: decimal(t, r.Lang) + " °C", Action: r.Bind(s.temperature(e.Domain)),
			}))
		case e.Attrs.Temperature != nil:
			t := clamp(*e.Attrs.Temperature, 7, 35)
			block.Children = append(block.Children, pcards.N(&cardsv1.Slider{
				Value: t, Min: 7, Max: 35, Step: 0.5, Unit: "°C", Label: r.T("ha.target"),
				ValueLabel: decimal(t, r.Lang) + " °C", Action: r.Bind(s.temperature(e.Domain)),
			}))
		case e.Attrs.TargetTempLow != nil && e.Attrs.TargetTempHigh != nil:
			// a range climate has a window, not one number; nothing on the
			// card sets a window
			block.Children = append(block.Children, pcards.N(&cardsv1.Value{
				Value: decimal(*e.Attrs.TargetTempLow, r.Lang) + "–" + decimal(*e.Attrs.TargetTempHigh, r.Lang),
				Unit:  "°C", Label: r.T("ha.target")}))
		}
	case can && e.Domain == "lock":
		block.Children = append(block.Children, state())
		if e.State == "locked" {
			block.Actions = []*cardsv1.Button{{Label: r.T("ha.unlock"), Icon: "unlocked",
				Action: r.BindConfirm(s.call("lock", "unlock", nil, "unlock"), r.T("ha.confirm.unlock", "name", n))}}
		} else {
			block.Actions = []*cardsv1.Button{{Label: r.T("ha.lock"), Icon: "locked",
				Action: r.BindConfirm(s.call("lock", "lock", nil, "lock"), r.T("ha.confirm.lock", "name", n))}}
		}
	case can && e.Domain == "cover":
		block.Children = append(block.Children, state())
		block.Actions = []*cardsv1.Button{
			{Label: r.T("ha.open_cover"), Icon: "arrow_up", Action: bind(s.call("cover", "open_cover", nil, "open"), r.T("ha.confirm.open", "name", n))},
			{Label: r.T("ha.close_cover"), Icon: "arrow_down", Action: bind(s.call("cover", "close_cover", nil, "close"), r.T("ha.confirm.close", "name", n))},
		}
	case e.Domain == "sensor" && numeric(e.State):
		unit := ""
		if u := e.Attrs.UnitOfMeasurement; u != nil {
			unit = ha.Sanitize(*u, 12)
		}
		v, _ := strconv.ParseFloat(e.State, 64)
		block.Children = append(block.Children, pcards.N(&cardsv1.Value{Value: truncRunes(decimal(v, r.Lang), 24), Unit: unit,
			Label: sensorLabel(r, e)}))
	default:
		block.Children = append(block.Children, state())
	}
	fallback := truncRunes(r.T("ha.fallback", "name", n, "state", stateLabel), 280)
	return pcards.NewCard("", 0, fallback, block), nil
}

// renderConfirmation overlays one call's outcome without changing the entity
// card's id or its controls. The lease sees the same fingerprint transition
// even when a timeout produces no Home Assistant state event.
func (s *haEntity) renderConfirmation(c *cardsv1.Card, r *Render) {
	snap, ok := s.confirmationSnapshot()
	if !ok {
		return
	}
	b := c.GetRoot().GetCard()
	if b == nil {
		return
	}
	var label string
	var kind cardsv1.StateKind
	switch snap.Phase {
	case tools.HAConfirmationPending:
		label, kind = r.T("ha.control.pending"), cardsv1.StateKind_STATE_KIND_UNKNOWN
		b.Tone = cardsv1.Tone_TONE_NEUTRAL
		if snap.Expected != "" {
			// generic state words cover the fixed on/off targets; numeric
			// brightness, kelvin and temperature targets stay as written
			state, _ := haState(r, ha.Entity{State: snap.Expected})
			b.Children = append(b.Children, pcards.N(&cardsv1.Text{
				Text: truncRunes(r.T("ha.control.expected", "state", state), 80), Style: cardsv1.TextStyle_TEXT_STYLE_META,
			}))
		}
		b.Children = append(b.Children, pcards.N(&cardsv1.State{Label: label, Kind: kind}))
		b.Children = append(b.Children, pcards.N(&cardsv1.Progress{Amount: &cardsv1.Progress_Indeterminate{Indeterminate: true}}))
	case tools.HAConfirmationDone:
		label, kind = r.T("ha.control.done"), cardsv1.StateKind_STATE_KIND_ON
		b.Tone = cardsv1.Tone_TONE_SUCCESS
		b.Children = append(b.Children, pcards.N(&cardsv1.State{Label: label, Kind: kind}))
	case tools.HAConfirmationFailed:
		label, kind = r.T("ha.control.failed"), cardsv1.StateKind_STATE_KIND_UNAVAILABLE
		b.Tone = cardsv1.Tone_TONE_DANGER
		b.Children = append(b.Children, pcards.N(&cardsv1.State{Label: label, Kind: kind}))
	default:
		return
	}
	c.FallbackText = truncRunes(joined(" · ", c.FallbackText, label), 280)
}

func toggles(domain string) bool {
	switch domain {
	case "light", "switch", "fan", "input_boolean", "humidifier":
		return true
	}
	return false
}

// heaterOn says whether a water heater heats: any operation mode but off
func heaterOn(e ha.Entity) bool {
	switch e.State {
	case "off", "unavailable", "unknown", "":
		return false
	}
	return true
}

// heaterKind is a water heater's state kind: on while it heats
func heaterKind(e ha.Entity) cardsv1.StateKind {
	if e.State == "unavailable" {
		return cardsv1.StateKind_STATE_KIND_UNAVAILABLE
	}
	if heaterOn(e) {
		return cardsv1.StateKind_STATE_KIND_ON
	}
	return cardsv1.StateKind_STATE_KIND_OFF
}

// heaterWord words a water heater's power and mode as home assistant's own
// dialog does: off, or heating with its operation mode («Нагрев (эко)»)
func heaterWord(r *Render, e ha.Entity) string {
	if !heaterOn(e) {
		if e.State == "unavailable" {
			return r.T("ha.unavailable")
		}
		return r.T("ha.wh.off")
	}
	label := r.T("ha.wh.heating")
	if e.State != "on" {
		label += " (" + truncRunes(word(r, "ha.opmode.", e.State), 24) + ")"
	}
	return label
}

// haIcon is the domain's icon of the screen's set (a sensor by its class)
func haIcon(e ha.Entity) string {
	switch e.Domain {
	case "light":
		return "light"
	case "switch", "input_boolean":
		return "plug"
	case "fan", "humidifier":
		return "fan"
	case "lock":
		return "lock"
	case "climate":
		return "thermostat"
	case "water_heater":
		// the heating flame, not the thermometer: a heater is switched, not measured
		return "kettle"
	case "cover":
		// the screen's "blinds" glyph is tabler's accessibility figure, not a blind: a door reads right for a
		// garage, a gate and a blind alike
		return "closed"
	case "media_player":
		return "tv"
	case "sensor", "binary_sensor":
		switch deviceClass(e) {
		case "temperature":
			return "thermostat"
		case "door", "window", "opening", "garage_door":
			return "closed"
		}
		return "sensor"
	}
	return "home"
}

func deviceClass(e ha.Entity) string {
	if e.Attrs.DeviceClass == nil {
		return ""
	}
	return *e.Attrs.DeviceClass
}

// sensorLabel names what a numeric sensor measures, "" for a class without a word
func sensorLabel(r *Render, e ha.Entity) string {
	switch deviceClass(e) {
	case "temperature":
		return r.T("ha.temperature")
	case "humidity":
		return r.T("ha.humidity")
	}
	return ""
}

// haState words the state from the catalog, never Home Assistant's own text
// for a free-text domain
func haState(r *Render, e ha.Entity) (string, cardsv1.StateKind) {
	switch e.Domain {
	case "binary_sensor":
		switch deviceClass(e) {
		case "door", "window", "opening", "garage_door":
			if e.State == "on" {
				return r.T("ha.open"), cardsv1.StateKind_STATE_KIND_OPEN
			}
			if e.State == "off" {
				return r.T("ha.closed"), cardsv1.StateKind_STATE_KIND_CLOSED
			}
		case "motion", "occupancy", "presence", "moisture", "smoke", "gas":
			if e.State == "on" {
				return r.T("ha.detected"), cardsv1.StateKind_STATE_KIND_ON
			}
			if e.State == "off" {
				return r.T("ha.clear"), cardsv1.StateKind_STATE_KIND_OFF
			}
		}
	case "climate", "water_heater":
		if w := r.T("ha.hvac." + e.State); w != "ha.hvac."+e.State {
			if e.State == "off" {
				return w, cardsv1.StateKind_STATE_KIND_OFF
			}
			return w, cardsv1.StateKind_STATE_KIND_ON
		}
	case "media_player":
		switch e.State {
		case "playing":
			return r.T("ha.playing"), cardsv1.StateKind_STATE_KIND_ON
		case "paused":
			return r.T("ha.paused"), cardsv1.StateKind_STATE_KIND_OFF
		case "idle", "standby", "buffering":
			return r.T("ha.idle"), cardsv1.StateKind_STATE_KIND_OFF
		}
	}
	switch e.State {
	case "on":
		return r.T("ha.on"), cardsv1.StateKind_STATE_KIND_ON
	case "off":
		return r.T("ha.off"), cardsv1.StateKind_STATE_KIND_OFF
	case "open", "opening":
		return r.T("ha.open"), cardsv1.StateKind_STATE_KIND_OPEN
	case "closed", "closing":
		return r.T("ha.closed"), cardsv1.StateKind_STATE_KIND_CLOSED
	case "locked":
		return r.T("ha.locked"), cardsv1.StateKind_STATE_KIND_LOCKED
	case "unlocked":
		return r.T("ha.unlocked"), cardsv1.StateKind_STATE_KIND_UNLOCKED
	case "unavailable":
		return r.T("ha.unavailable"), cardsv1.StateKind_STATE_KIND_UNAVAILABLE
	}
	if s := ha.ModelState(e); s != "" {
		return truncRunes(s, 40), cardsv1.StateKind_STATE_KIND_UNKNOWN
	}
	return r.T("ha.unavailable"), cardsv1.StateKind_STATE_KIND_UNKNOWN
}

func numeric(s string) bool {
	_, err := strconv.ParseFloat(s, 64)
	return err == nil
}

func clamp(v, lo, hi float64) float64 { return math.Max(lo, math.Min(hi, v)) }

func (s *haEntity) brightness() Binding {
	return Binding{Idempotent: true, Run: func(ctx context.Context, a Act) Outcome {
		pct := int(math.Round(a.Value.GetNumber()))
		return s.act(ctx, a, "light", "turn_on", map[string]any{"brightness_pct": pct},
			map[string]any{"action": "set_brightness", "brightness": pct})
	}}
}

func (s *haEntity) temperature(domain string) Binding {
	return Binding{Idempotent: true, Run: func(ctx context.Context, a Act) Outcome {
		t := a.Value.GetNumber()
		return s.act(ctx, a, domain, "set_temperature", map[string]any{"temperature": t},
			map[string]any{"action": "set_temperature", "temperature": t})
	}}
}

// call is a fixed service; action names it in the history as ha_control would
func (s *haEntity) call(domain, service string, data map[string]any, action string) Binding {
	return Binding{Run: func(ctx context.Context, a Act) Outcome {
		return s.act(ctx, a, domain, service, data, map[string]any{"action": action})
	}}
}

// act runs one service call through the agent's policy and service layer
func (s *haEntity) act(ctx context.Context, a Act, domain, service string, data map[string]any, args map[string]any) Outcome {
	e, ok := s.entity()
	if !ok {
		return Outcome{Refused: "unavailable"}
	}
	if !controllable(e) {
		return Outcome{Refused: "unavailable"}
	}
	if s.d.Policy != nil {
		c := agent.Call{DeviceID: a.DeviceID, Trigger: ScreenTrigger}
		if te := s.d.Policy(ctx, c, e, domain, service); te != nil {
			return Outcome{Refused: refusalOf(te.Code)}
		}
	}
	args["target"] = e.ID
	rec := &Record{Tool: agent.HAControl, Args: args}
	cctx, cancel := context.WithTimeout(ctx, haCallTimeout)
	defer cancel()
	if err := s.d.Home.Client().Call(cctx, domain, service, data, e.ID); err != nil {
		rec.Result = map[string]any{"error": map[string]any{"code": "failed"}}
		return Outcome{Refused: "failed", Record: rec}
	}
	if s.d.Usage != nil {
		s.d.Usage.Record(ctx, a.DeviceID, e.ID)
	}
	before := fingerprint(e, true)
	after, _ := s.d.Home.Wait(ctx, e.ID, func(x ha.Entity) bool { return fingerprint(x, true) != before }, haSettle)
	rec.Result = map[string]any{"done": args["action"], "entity": map[string]any{
		"id": e.ID, "name": name(e), "state": ha.ModelState(after),
	}}
	return Outcome{Record: rec}
}

// refusalOf maps a policy refusal onto the catalog's toasts
func refusalOf(code string) string {
	switch code {
	case "not_supported", "wake_required", "internal":
		return code
	}
	return "unavailable"
}

func truncRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
