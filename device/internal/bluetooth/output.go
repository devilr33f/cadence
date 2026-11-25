package bluetooth

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/capability"
)

const (
	// a new speaker starts no louder than this (rule 7)
	NewSpeakerLevel = 40
	defaultScan     = 8 * time.Second

	outputConfigSchema = `{"type":"object","properties":{"speaker":{"type":"string","pattern":"^([0-9a-f]{12})?$"}},` +
		`"additionalProperties":false}`

	speakerSchema = `{"type":"object","properties":{"id":{"type":"string","pattern":"^[0-9a-f]{12}$"},` +
		`"name":{"type":"string"},"connected":{"type":"boolean"},"paired":{"type":"boolean"}},` +
		`"required":["id","name","connected","paired"],"additionalProperties":false}`

	outputStateSchema = `{"type":"object","properties":{"speakers":{"type":"array","items":` + speakerSchema + `},` +
		`"active":{"type":"string","pattern":"^([0-9a-f]{12})?$"},` +
		`"code":{"type":"string","pattern":"^([a-z0-9_]{1,64})?$"}},` +
		`"required":["speakers","active","code"],"additionalProperties":false}`
)

// OutputOptions turn on bluetooth.output: playing to a speaker. The board decides whether its stack qualifies.
type OutputOptions struct {
	// Route hears the active speaker's address each time it changes, "" while none plays; it runs on Run's
	// goroutine and must not block for long
	Route func(address string)
	// Lower caps the level at top before a speaker never active here plays (rule 7: a new speaker starts at
	// min(level, 40)); nil lowers nothing
	Lower func(top int)
	// Hardware hears whether the active speaker's own volume carries the level (avrcp absolute volume): on, the
	// board's own gain stays at full; off, it is the level again. It runs on Run's goroutine, before the speaker
	// changes (Route) and once the speaker holds the level, and must not block for long
	Hardware func(on bool)
	// SpeakerLevel hears a level the speaker's own volume buttons set (the owner's press on it, rule 7)
	SpeakerLevel func(level int)
	// Control hears the speaker's play, pause, next and previous buttons (Action*), one at a time on a goroutine of
	// Run's; it may block until ctx ends
	Control func(ctx context.Context, action string)
	// KnownFile keeps the ids of the speakers that were active here; "" keeps them in memory alone
	KnownFile string
	// how long a scan discovers; 0 is 8 s, inside the server's 10 s command timeout
	ScanFor time.Duration
	// the reconnect's backoff: its first wait (0 is 2 s), doubling up to ReconnectMax (0 is 60 s), given up once the
	// waits add up to ReconnectFor (0 is 10 min)
	ReconnectFirst, ReconnectMax, ReconnectFor time.Duration
}

// output is bluetooth.output's own part of the capability's state; callers hold Bluetooth.mu
type output struct {
	opts OutputOptions
	// the config's preferred speaker, "" for the most recently connected
	speaker string
	// set_route's choice, which wins over the config until the config changes
	chosen string
	// the active speaker's id and address, as Route last heard it
	active, address string
	routed          bool
	// device path -> the order it was seen connecting, for "most recently connected"
	connSeq map[string]uint64
	seq     uint64
	// the ids of the speakers active here, the most recent last
	known []string
	rc    reconnector
	// device paths we pair as the initiator, until when our agent accepts them
	outgoing map[string]time.Time
	shown    string
	state    chan json.RawMessage
	scanning bool
	// the level changed since the speaker's own volume was set
	levelDirty bool
	// when a speaker was last seen connecting: its own play on connect is not the owner's
	activeAt time.Time
	// the device's media status for our player, as the board set it
	status  string
	presses chan string
	// Run's goroutine alone: the speaker's own volume, our player registered and the status it shows
	hv          hardVolume
	playerUp    bool
	shownStatus string
}

// SpeakerState is a speaker as bluetooth.output's state shows it; its name is for the dashboard alone.
type SpeakerState struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Connected bool   `json:"connected"`
	Paired    bool   `json:"paired"`
}

type outputState struct {
	Speakers []SpeakerState `json:"speakers"`
	Active   string         `json:"active"`
	Code     string         `json:"code"`
}

// OutputDelay is the active speaker's own BlueZ delay report, in us, from the latest snapshot. ok is false when
// bluez does not expose MediaTransport1.Delay for it.
func (b *Bluetooth) OutputDelay(address string) (delay int, ok bool) {
	b.mu.Lock()
	snap := b.snap
	b.mu.Unlock()
	for _, d := range snap.Devices {
		if !strings.EqualFold(d.Address, address) {
			continue
		}
		for _, p := range snap.PCMs {
			if p.Device == d.Path && p.Transport == TransportA2DPSource && p.DelayKnown {
				return p.DelayHintUs, true
			}
		}
	}
	return 0, false
}

// OutputCapability is bluetooth.output, nil unless Options.Output turned it on.
func (b *Bluetooth) OutputCapability() capability.Provider {
	if b.out == nil {
		return nil
	}
	return &outputProvider{b: b}
}

type outputProvider struct{ b *Bluetooth }

func (*outputProvider) Descriptor() capability.Descriptor {
	return capability.Descriptor{ID: "bluetooth.output", Version: 1, ConfigSchema: outputConfigSchema,
		StateSchema: outputStateSchema, Commands: outputCommands()}
}

func (p *outputProvider) Apply(_ context.Context, _ int64, raw json.RawMessage) error {
	var c struct {
		Speaker *string `json:"speaker"`
	}
	if decodeObject(raw, &c) != nil || c.Speaker != nil && *c.Speaker != "" && !validID(*c.Speaker) {
		return &capability.ApplyError{Code: "invalid"}
	}
	b := p.b
	b.mu.Lock()
	b.out.speaker, b.out.chosen = "", ""
	if c.Speaker != nil {
		b.out.speaker = *c.Speaker
	}
	b.mu.Unlock()
	b.wake()
	return nil
}

func (p *outputProvider) Command(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, error) {
	return p.b.outputCommand(ctx, name, args)
}

func (p *outputProvider) State() <-chan json.RawMessage { return p.b.out.state }

func newOutput(opts OutputOptions) *output {
	if opts.ScanFor <= 0 {
		opts.ScanFor = defaultScan
	}
	if opts.ReconnectFirst <= 0 {
		opts.ReconnectFirst = defaultReconnectFirst
	}
	if opts.ReconnectMax <= 0 {
		opts.ReconnectMax = defaultReconnectMax
	}
	if opts.ReconnectFor <= 0 {
		opts.ReconnectFor = defaultReconnectFor
	}
	return &output{opts: opts, connSeq: map[string]uint64{}, known: loadKnown(opts.KnownFile),
		outgoing: map[string]time.Time{}, state: make(chan json.RawMessage, 1), presses: make(chan string, pressQueue)}
}

// isSpeaker: a device we can play to, by its a2dp sink uuid or its class
func isSpeaker(d Device) bool {
	if d.AudioSink {
		return true
	}
	k := kind(d.Icon, d.Class)
	return k == kindSpeaker || k == kindHeadphones
}

// sourcePCM is the speaker's a2dp source pcm: bluealsa has one while it is connected for us to play to
func sourcePCM(snap Snapshot, d Device) bool {
	return slices.ContainsFunc(snap.PCMs, func(p PCM) bool { return p.Device == d.Path && p.Transport == TransportA2DPSource })
}

func speakers(snap Snapshot) []SpeakerState {
	out := []SpeakerState{}
	if snap.Adapter == nil {
		return out
	}
	for _, d := range snap.Devices {
		if isSpeaker(d) && (d.Paired || d.Connected) {
			out = append(out, SpeakerState{ID: ShortID(snap.Adapter.Address, d.Address), Name: d.Name,
				Connected: d.Connected, Paired: d.Paired})
		}
	}
	slices.SortFunc(out, func(x, y SpeakerState) int { return strings.Compare(x.ID, y.ID) })
	return out
}

// pick is the speaker to play to: set_route's choice, else the config's, else the most recently connected, of the
// connected speakers that bluealsa has a source pcm for; callers hold mu
func (o *output) pick(snap Snapshot) (id, address string) {
	if snap.Adapter == nil || !snap.Bluealsa {
		return "", ""
	}
	type cand struct {
		id, address string
		seq         uint64
	}
	var cs []cand
	for _, d := range snap.Devices {
		if d.Connected && isSpeaker(d) && sourcePCM(snap, d) {
			cs = append(cs, cand{ShortID(snap.Adapter.Address, d.Address), d.Address, o.connSeq[d.Path]})
		}
	}
	for _, want := range []string{o.chosen, o.speaker} {
		if i := slices.IndexFunc(cs, func(c cand) bool { return want != "" && c.id == want }); i >= 0 {
			return cs[i].id, cs[i].address
		}
	}
	if len(cs) == 0 {
		return "", ""
	}
	best := slices.MaxFunc(cs, func(x, y cand) int {
		if x.seq != y.seq {
			return int(x.seq) - int(y.seq) //nolint:gosec // small counters
		}
		return strings.Compare(y.id, x.id)
	})
	return best.id, best.address
}

// connections orders the devices by when they were seen connecting; callers hold mu
func (o *output) connections(snap Snapshot, now time.Time) {
	var fresh []string
	for _, d := range snap.Devices {
		_, seen := o.connSeq[d.Path]
		switch {
		case d.Connected && !seen:
			fresh = append(fresh, d.Path)
		case !d.Connected && seen:
			delete(o.connSeq, d.Path)
		}
	}
	slices.Sort(fresh)
	if len(fresh) > 0 {
		o.activeAt = now
	}
	for _, p := range fresh {
		o.seq++
		o.connSeq[p] = o.seq
	}
	for p := range o.connSeq {
		if !slices.ContainsFunc(snap.Devices, func(d Device) bool { return d.Path == p }) {
			delete(o.connSeq, p)
		}
	}
}

// route follows the active speaker: a speaker never active here is first lowered to (rule 7), then the board hears
// the address its output plays to; only Run's goroutine calls it
func (b *Bluetooth) route(snap Snapshot) {
	o := b.out
	if o == nil {
		return
	}
	b.mu.Lock()
	o.connections(snap, b.opts.Now())
	id, address := o.pick(snap)
	if o.routed && id == o.active && address == o.address {
		b.mu.Unlock()
		return
	}
	if id != "" {
		o.activeAt = b.opts.Now()
	}
	o.routed, o.active, o.address = true, id, address
	lower := id != "" && !slices.Contains(o.known, id)
	// the most recently active speaker is the one a reconnect after a reboot goes to
	moved := id != "" && (len(o.known) == 0 || o.known[len(o.known)-1] != id)
	if moved {
		o.known = touchKnown(o.known, id)
	}
	known := slices.Clone(o.known)
	b.mu.Unlock()
	// lowered before it is saved as known, so a crash between lowers it once more rather than never
	if lower && o.opts.Lower != nil {
		o.opts.Lower(NewSpeakerLevel)
	}
	if moved {
		if err := saveKnown(o.opts.KnownFile, known); err != nil {
			b.log.Warn("bluetooth_known_failed")
		}
	}
	if lower {
		b.log.Info("bluetooth_speaker_new", "id", id, "max_level", NewSpeakerLevel)
	}
	b.log.Info("bluetooth_output_route", "id", id)
	// the board's gain is the level again before another speaker, at its own volume, plays
	b.setHard(false)
	o.hv.pcm = ""
	if o.opts.Route != nil {
		o.opts.Route(address)
	}
}

// publishOutputLocked offers bluetooth.output's state when it changed; callers hold mu
func (b *Bluetooth) publishOutputLocked() {
	o := b.out
	if o == nil {
		return
	}
	raw, err := json.Marshal(outputState{Speakers: speakers(b.snap), Active: o.active, Code: b.code})
	if err != nil || string(raw) == o.shown {
		return
	}
	o.shown = string(raw)
	offer(o.state, json.RawMessage(raw))
}
