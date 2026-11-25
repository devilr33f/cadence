package agent

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
)

// Need is what a tool asks of the device it acts on: a capability, whether
// only a screen will do (then a speaker's default is its room's screen), and
// whether the call changes anything there (the cross-device policy).
type Need struct {
	Capability string
	// ScreenAlt is a capability a screen may have instead (music on a screen's own player: media.video)
	ScreenAlt string
	Screen    bool
	Write     bool
}

// met says whether d has what the need asks: its capability, or a screen's alternative
func (n Need) met(d DeviceInfo) bool {
	return n.Capability == "" || d.Has(n.Capability) || (n.ScreenAlt != "" && d.Screen() && d.Has(n.ScreenAlt))
}

// Target is the device a call resolved to.
type Target struct {
	ID, Name string
	// Self is the turn's own device
	Self bool
}

// Resolver resolves a tool's device argument (TV plan item 14).
type Resolver struct {
	Household Household
	// Pressed, when set, is the screen whose assistant button opened a running session ("" for none)
	Pressed func(sessionID string) string
	// Groups, when set, are the multiroom groups by name: a group's name resolves to its leader
	Groups func(ctx context.Context) ([]GroupName, error)

	mu sync.Mutex
	// last is each room's screen a screen tool last acted on, by its normalized name
	last map[string]string
}

// GroupName is a multiroom group as the resolver matches it: its name and its elected leader ("" for none).
type GroupName struct{ Name, LeaderID string }

// words for "this device" a model may send instead of leaving the argument out
var hereWords = []string{"here", "this", "this device", "тут", "здесь", "сюда", "этот"}

// words for a kind of device
var kindWords = map[string][]string{
	KindScreen:  {"tv", "screen", "television", "телевизор", "телек", "тв", "экран"},
	KindSpeaker: {"speaker", "колонка", "колонку", "станция", "станцию"},
}

// ResolveTarget resolves arg (a device's name, a room, a kind of device,
// "here", or "" for here) to the device of the household that has
// need.Capability: an exact name, then a room (its device with the
// capability: "в гостиной" with video is the living-room screen), then a
// kind, then a fuzzy name. A screen-only need left at here takes the room's
// screen when this device is none. Ambiguity answers the candidates, as
// ha_control does. A write on another device needs a woken session and the
// target's remoteTargets to allow it; the resolved target is recorded with
// the call's result in the history.
func (r *Resolver) ResolveTarget(ctx context.Context, c Call, arg string, need Need) (Target, *ToolError) {
	// without a directory only this device is known, and nothing of it
	d := Devices{Self: DeviceInfo{ID: c.DeviceID, Kind: KindSpeaker}}
	if r != nil && r.Household != nil {
		all, err := r.Household(ctx)
		if err != nil {
			return Target{}, &ToolError{Code: "unavailable", Message: "The household's devices could not be read.", Retryable: true}
		}
		d = split(all, c.DeviceID)
	}
	if r != nil && r.Groups != nil {
		// unread groups leave the devices' names alone
		d.groups, _ = r.Groups(ctx)
	}
	if r != nil {
		d.settle = r.tiebreak(c)
	}
	t, e := resolve(d, arg, need)
	if e != nil {
		return Target{}, e
	}
	if e := allowed(c, d.Self, t, need); e != nil {
		return Target{}, e
	}
	if r != nil && need.Screen && t.Screen() {
		r.used(t)
	}
	recordTarget(ctx, t.ID)
	return Target{ID: t.ID, Name: t.Name, Self: t.ID == c.DeviceID}, nil
}

func resolve(d Devices, arg string, need Need) (DeviceInfo, *ToolError) {
	key := normalizeName(arg)
	if key == "" || slices.Contains(hereWords, key) {
		return here(d, need)
	}
	everyone := append([]DeviceInfo{d.Self}, d.Others...)
	// an exact name, online or not: an offline one says so
	if ms := byName(everyone, key, exactName); len(ms) > 0 {
		return pick(d, ms, need, true)
	}
	if g, ok := byGroup(d.groups, key, exactName); ok {
		return groupLeader(d, everyone, g, need)
	}
	if ms := byRoom(everyone, key); len(ms) > 0 {
		return pick(d, ms, need, false)
	}
	for kind, words := range kindWords {
		if slices.ContainsFunc(words, func(w string) bool { return fuzzy(key, w) }) {
			return pick(d, ofKind(everyone, kind), need, false)
		}
	}
	if ms := byName(everyone, key, fuzzy); len(ms) > 0 {
		return pick(d, ms, need, true)
	}
	if g, ok := byGroup(d.groups, key, fuzzy); ok {
		return groupLeader(d, everyone, g, need)
	}
	return DeviceInfo{}, &ToolError{
		Code: "unknown_device", Message: "No device of this household has that name or room.",
		Hint: "Use one of the candidates, or leave device out for this device.", Retryable: true,
		Candidates: candidates(d, need),
	}
}

// here is this device, or for a screen-only need on a device that is none,
// the one screen of its room, or for a bluetooth output need on a device
// without it, the one device that has it
func here(d Devices, need Need) (DeviceInfo, *ToolError) {
	switch {
	case !d.Known:
		return d.Self, nil
	case need.Screen && !d.Self.Screen():
		return roomScreen(d, need)
	case need.Capability == CapBluetoothOutput && !need.met(d.Self):
		return bluetoothHost(d, need)
	case !need.met(d.Self):
		return DeviceInfo{}, noCapability(d, need)
	}
	return d.Self, nil
}

func roomScreen(d Devices, need Need) (DeviceInfo, *ToolError) {
	var ms []DeviceInfo
	for _, o := range d.Others {
		if o.Online && o.Screen() && sameRoom(o.Room, d.Self.Room) && need.met(o) {
			ms = append(ms, o)
		}
	}
	switch len(ms) {
	case 0:
		return DeviceInfo{}, &ToolError{
			Code: "no_screen", Message: "This device has no screen, and none is online in its room.",
			Hint:       "Name a screen from the candidates, or tell the user there is no screen to show it on.",
			Candidates: candidates(d, need),
		}
	case 1:
		return ms[0], nil
	}
	if x, ok := d.settleAmong(ms); ok {
		return x, nil
	}
	return DeviceInfo{}, ambiguousDevice(ms)
}

// bluetoothHost is the device that sends to bluetooth speakers for one that cannot: the one of its room, else the
// household's only one, so «подключи колонку» on a receive-only speaker reaches the host without being named
func bluetoothHost(d Devices, need Need) (DeviceInfo, *ToolError) {
	var room, all []DeviceInfo
	for _, o := range d.Others {
		if !o.Online || !need.met(o) {
			continue
		}
		all = append(all, o)
		if sameRoom(o.Room, d.Self.Room) {
			room = append(room, o)
		}
	}
	ms := room
	if len(ms) == 0 {
		ms = all
	}
	switch len(ms) {
	case 0:
		return DeviceInfo{}, &ToolError{
			Code: "no_capability", Message: "No device here can play to a Bluetooth speaker.",
			Hint: "Tell the user no device of this household can connect a Bluetooth speaker.",
		}
	case 1:
		return ms[0], nil
	}
	return DeviceInfo{}, ambiguousDevice(ms)
}

// sameRoom is two rooms the owner named alike (case, ё and separators aside); no room is no one's
func sameRoom(a, b string) bool {
	a = normalizeName(a)
	return a != "" && a == normalizeName(b)
}

// pick narrows the devices a name, room or kind matched to the one with the
// capability; named says the user named a device, so one without the
// capability is the answer's subject rather than a candidate left out
func pick(d Devices, ms []DeviceInfo, need Need, named bool) (DeviceInfo, *ToolError) {
	able := slices.DeleteFunc(slices.Clone(ms), func(x DeviceInfo) bool {
		return !need.met(x)
	})
	if need.Screen {
		able = slices.DeleteFunc(able, func(x DeviceInfo) bool { return !x.Screen() })
	}
	if len(able) == 0 {
		if named && len(ms) == 1 && !ms[0].Online {
			return DeviceInfo{}, offline()
		}
		return DeviceInfo{}, noCapability(d, need)
	}
	online := slices.DeleteFunc(slices.Clone(able), func(x DeviceInfo) bool { return !x.Online && x.ID != d.Self.ID })
	switch {
	case len(online) == 0:
		return DeviceInfo{}, offline()
	case len(online) == 1:
		return online[0], nil
	}
	// a room or kind that holds this device means this device
	if i := slices.IndexFunc(online, func(x DeviceInfo) bool { return x.ID == d.Self.ID }); i >= 0 {
		return online[i], nil
	}
	// else the one in this device's room, and of several there the one the tiebreak settles
	var near []DeviceInfo
	for _, x := range online {
		if sameRoom(x.Room, d.Self.Room) {
			near = append(near, x)
		}
	}
	if len(near) == 1 {
		return near[0], nil
	}
	if x, ok := d.settleAmong(near); ok {
		return x, nil
	}
	return DeviceInfo{}, ambiguousDevice(online)
}

// allowed is the cross-device policy: a write on another device needs a turn
// the user spoke (never an event turn; a follow-up is the same conversation)
// and the target's remoteTargets to let this device drive it. rule 7's volume
// raise keeps its own woken check in the volume tool
func allowed(c Call, self, t DeviceInfo, need Need) *ToolError {
	if !need.Write || t.ID == c.DeviceID {
		return nil
	}
	switch {
	case EventTurn(c.Trigger):
		return NotInEventTurn()
	case !mayDrive(self, t):
		return &ToolError{
			Code:    "remote_refused",
			Message: "That device does not take commands from this one.",
			Hint:    "Tell the user to ask on that device itself.",
		}
	}
	return nil
}

func offline() *ToolError {
	return &ToolError{Code: "device_offline", Message: "That device is offline."}
}

func noCapability(d Devices, need Need) *ToolError {
	if need.Screen {
		return &ToolError{
			Code: "no_screen", Message: "That device has no screen to do this on.",
			Hint: "Name a screen from the candidates.", Candidates: candidates(d, need),
		}
	}
	return &ToolError{
		Code: "no_capability", Message: "That device cannot do this.",
		Hint: "Name a device from the candidates.", Candidates: candidates(d, need),
	}
}

func ambiguousDevice(ms []DeviceInfo) *ToolError {
	names := labels(ms)
	return &ToolError{
		Code: "ambiguous_device", Message: "Several devices match: " + strings.Join(names, ", ") + ".",
		Hint: "Ask the user which one they mean.", Retryable: true, Candidates: names,
	}
}

// candidates are the online devices that could take the call
func candidates(d Devices, need Need) []string {
	var ms []DeviceInfo
	for _, x := range append([]DeviceInfo{d.Self}, d.Others...) {
		if (x.Online || x.ID == d.Self.ID) && need.met(x) &&
			(!need.Screen || x.Screen()) {
			ms = append(ms, x)
		}
	}
	return labels(ms)
}

// labels are the devices' names, with the room where a name repeats
func labels(ms []DeviceInfo) []string {
	out := make([]string, len(ms))
	for i, x := range ms {
		out[i] = x.Name
		for j, o := range ms {
			if j != i && normalizeName(o.Name) == normalizeName(x.Name) && x.Room != "" {
				out[i] = x.Name + " (" + x.Room + ")"
				break
			}
		}
	}
	return out
}

func byName(ds []DeviceInfo, key string, match func(a, b string) bool) []DeviceInfo {
	var out []DeviceInfo
	for _, x := range ds {
		if n := normalizeName(x.Name); n != "" && match(key, n) {
			out = append(out, x)
		}
	}
	return out
}

func byRoom(ds []DeviceInfo, key string) []DeviceInfo {
	var out []DeviceInfo
	for _, x := range ds {
		if r := normalizeName(x.Room); r != "" && fuzzy(key, r) {
			out = append(out, x)
		}
	}
	return out
}

// byGroup is the one group whose name matches key
func byGroup(gs []GroupName, key string, match func(a, b string) bool) (GroupName, bool) {
	var out []GroupName
	for _, g := range gs {
		if n := normalizeName(g.Name); n != "" && match(key, n) {
			out = append(out, g)
		}
	}
	if len(out) != 1 {
		return GroupName{}, false
	}
	return out[0], true
}

// groupLeader is a group's target: its leader plays for every member; a group that elected none is offline
func groupLeader(d Devices, everyone []DeviceInfo, g GroupName, need Need) (DeviceInfo, *ToolError) {
	i := slices.IndexFunc(everyone, func(x DeviceInfo) bool { return x.ID == g.LeaderID })
	if g.LeaderID == "" || i < 0 {
		return DeviceInfo{}, offline()
	}
	return pick(d, everyone[i:i+1], need, true)
}

func ofKind(ds []DeviceInfo, kind string) []DeviceInfo {
	var out []DeviceInfo
	for _, x := range ds {
		if x.Kind == kind {
			out = append(out, x)
		}
	}
	return out
}

func exactName(a, b string) bool { return a == b }

// normalizeName is lower case, ё as е, separators and punctuation as single
// spaces, and leading prepositions dropped ("на телевизоре", "в гостиной")
func normalizeName(s string) string {
	s = strings.ReplaceAll(strings.ToLower(s), "ё", "е")
	words := strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	for len(words) > 1 && slices.Contains([]string{"на", "в", "во", "on", "in", "the", "at"}, words[0]) {
		words = words[1:]
	}
	return strings.Join(words, " ")
}

// fuzzy matches a spoken name against a device's word by word: every word
// of the shorter is one of the longer's, or the same but for an inflected
// ending ("телевизоре" and "телевизор", "гостиной" and "гостиная")
func fuzzy(a, b string) bool {
	wa, wb := strings.Fields(a), strings.Fields(b)
	if len(wa) == 0 || len(wb) == 0 {
		return false
	}
	if len(wa) > len(wb) {
		wa, wb = wb, wa
	}
	for _, w := range wa {
		if !slices.ContainsFunc(wb, func(o string) bool { return w == o || stemMatch(w, o) }) {
			return false
		}
	}
	return true
}

// stemMatch is two words that share all but an ending of up to two runes,
// and at least four runes
func stemMatch(a, b string) bool {
	ra, rb := []rune(a), []rune(b)
	n := 0
	for n < len(ra) && n < len(rb) && ra[n] == rb[n] {
		n++
	}
	return n >= 4 && len(ra)-n <= 2 && len(rb)-n <= 2
}

// the target a call resolved, carried in its ctx so the runner records it
type targetSlot struct {
	mu sync.Mutex
	id string
}

type targetKey struct{}

func withTargetSlot(ctx context.Context) (context.Context, *targetSlot) {
	s := &targetSlot{}
	return context.WithValue(ctx, targetKey{}, s), s
}

func recordTarget(ctx context.Context, id string) {
	if s, ok := ctx.Value(targetKey{}).(*targetSlot); ok {
		s.mu.Lock()
		s.id = id
		s.mu.Unlock()
	}
}

func (s *targetSlot) get() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.id
}

// tiebreak settles several screens of a room for one call, deterministically, in order: (a) the screen the user is
// looking at (its assistant button opened the session, else the one screen whose drawer is open), (b) the one
// screen that is on and playing, else the one the user touched last, (c) the room's screen a screen tool used
// last. false when none of them separates the screens: the call asks.
func (r *Resolver) tiebreak(c Call) func([]DeviceInfo) (DeviceInfo, bool) {
	return func(ms []DeviceInfo) (DeviceInfo, bool) {
		if len(ms) < 2 || !slices.ContainsFunc(ms, func(x DeviceInfo) bool { return x.Screen() }) {
			return DeviceInfo{}, false
		}
		if r.Pressed != nil && c.SessionID != "" {
			if id := r.Pressed(c.SessionID); id != "" {
				if i := slices.IndexFunc(ms, func(x DeviceInfo) bool { return x.ID == id }); i >= 0 {
					return ms[i], true
				}
			}
		}
		if x, ok := only(ms, func(x DeviceInfo) bool { return x.Watch.DrawerOpen && !x.Watch.Off }); ok {
			return x, true
		}
		if x, ok := only(ms, func(x DeviceInfo) bool { return x.Watch.Playing && !x.Watch.Off }); ok {
			return x, true
		}
		on := slices.DeleteFunc(slices.Clone(ms), func(x DeviceInfo) bool { return x.Watch.Off })
		if x, ok := latest(on); ok {
			return x, true
		}
		if id := r.lastUsed(ms[0].Room); id != "" {
			if i := slices.IndexFunc(ms, func(x DeviceInfo) bool { return x.ID == id }); i >= 0 {
				return ms[i], true
			}
		}
		return DeviceInfo{}, false
	}
}

// only is the one device that is so, false for none or several
func only(ms []DeviceInfo, is func(DeviceInfo) bool) (DeviceInfo, bool) {
	var hit []DeviceInfo
	for _, x := range ms {
		if is(x) {
			hit = append(hit, x)
		}
	}
	if len(hit) != 1 {
		return DeviceInfo{}, false
	}
	return hit[0], true
}

// latest is the device whose last input is the newest, false when none has one or two share it
func latest(ms []DeviceInfo) (DeviceInfo, bool) {
	var best DeviceInfo
	var at time.Time
	tie := false
	for _, x := range ms {
		switch t := x.Watch.LastInput; {
		case t.IsZero():
		case t.After(at):
			best, at, tie = x, t, false
		case t.Equal(at):
			tie = true
		}
	}
	return best, !at.IsZero() && !tie
}

func (r *Resolver) used(t DeviceInfo) {
	room := normalizeName(t.Room)
	if room == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.last == nil {
		r.last = map[string]string{}
	}
	r.last[room] = t.ID
}

func (r *Resolver) lastUsed(room string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.last[normalizeName(room)]
}
