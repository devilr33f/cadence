// Package settings holds the global and per-device settings. Both scopes are
// sparse: a scope stores only the keys it sets, so false, empty and 0 can
// override.
package settings

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	_ "time/tzdata" // LoadLocation needs zone data in the distroless image
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"git.shino.boo/devilreef/cadence/server/internal/store/db"
)

const globalScope = "global"

// MinCompactAfter is the shortest compactAfter, in minutes.
const MinCompactAfter = 1

const (
	maxHistoryTurns     = 50
	maxCompactAfter     = 120 // minutes
	minLogRetentionDays = 1
	maxLogRetentionDays = 30
	// MinCompactionMaxTokens is the shortest compactionMaxTokens a scope may set.
	MinCompactionMaxTokens = 64
	// MaxCompactionMaxTokens is the longest compactionMaxTokens a scope may set.
	MaxCompactionMaxTokens = 32768
	// DefaultCompactionMaxTokens is the ceiling when no scope sets one. Thinking
	// counts against max_tokens even with reasoning off, so a tight cap ended a
	// compaction mid-thought with no summary.
	DefaultCompactionMaxTokens = 8192
)

var (
	ErrNotFound = errors.New("settings: device not found")
	ErrInvalid  = errors.New("settings: invalid")
)

// InvalidError is an ErrInvalid about one key: it names a mode or a provider
// instance that is not there.
type InvalidError struct{ Key string }

func (e *InvalidError) Error() string { return "settings: invalid " + e.Key }

func (e *InvalidError) Is(target error) bool { return target == ErrInvalid }

// Values is the effective settings of a device. Tools nil means every tool.
type Values struct {
	STT      string `json:"stt"`
	TTS      string `json:"tts"`
	Voice    string `json:"voice"`
	LLM      string `json:"llm"`
	Model    string `json:"model"`
	Language string `json:"language"`
	TimeZone string `json:"timeZone"`
	// Persona is the owner's own instructions for the assistant on this
	// device (who listens, how to talk with them, jokes), at most MaxPersona
	// characters; it follows every rule in the system prompt.
	Persona         string   `json:"persona"`
	Mode            string   `json:"mode"`
	AllowedModes    []string `json:"allowedModes"`
	ModeRevert      string   `json:"modeRevert"`
	Tools           []string `json:"tools"`
	Weather         string   `json:"weather"`
	Search          string   `json:"search"`
	Embed           string   `json:"embed"`
	WeatherLocation string   `json:"weatherLocation"`
	// Music names the music provider instances music_search asks
	// (youtube-music, spotify, yandex-music, zvuk, soundcloud, vk-music), in
	// order, at most 32; one name alone is read
	// as a list of it, and none leaves the music tools out.
	Music Names `json:"music"`
	// MusicPlayer names the player provider instance (yt-dlp) that turns
	// a found song into a stream the device plays.
	MusicPlayer string `json:"musicPlayer"`
	// XSearch names the X search provider instance (x-web) x_search asks;
	// unset leaves the tool out.
	XSearch string `json:"xSearch"`
	// HistoryTurns caps the raw turns after the latest compaction summary.
	HistoryTurns int `json:"historyTurns"`
	// CompactAfter is the idle minutes after which a conversation is compacted.
	CompactAfter int `json:"compactAfter"`
	// CompactionMaxTokens is the ceiling on the maintenance model's max_tokens
	// for a compaction and the learning call that follows, when CompactionMode
	// is empty. Thinking counts against it even with reasoning off.
	CompactionMaxTokens int `json:"compactionMaxTokens"`
	// CompactionMode is an agent mode that already exists, set for the summary.
	// It is not the mode the speaker is in. The summary uses that mode's model,
	// reasoning effort, max tokens, first-token timeout and max seconds. Empty
	// keeps MaintenanceModel, CompactionMaxTokens, no reasoning and a 60 s
	// attempt. Learning uses MemoryMode when that is set, and this mode otherwise.
	CompactionMode string `json:"compactionMode"`
	// MaintenanceModel runs compaction and learning; empty is Model.
	// CompactionMode, when set, names the model instead.
	MaintenanceModel string `json:"maintenanceModel"`
	// MemoryScopes names the memory scopes recalled, "global" and "device";
	// nil is both, an empty list none.
	MemoryScopes []string `json:"memoryScopes"`
	// MemoryMode is an agent mode that already exists, set for learning. It is
	// not the mode the speaker is in. The learning call uses that mode's model,
	// reasoning effort, max tokens, first-token timeout and max seconds. Empty
	// uses the compaction call's limits.
	MemoryMode string `json:"memoryMode"`
	// MemoryApproval makes learning propose (pending memories) instead of
	// changing memories itself.
	MemoryApproval bool `json:"memoryApproval"`
	// Hermes names the hermes provider instance that runs background tasks;
	// global only, read when the server starts.
	Hermes string `json:"hermes"`
	// HermesApprovals is who may answer a task's approval question: "voice"
	// (a wake or press session, or the dashboard) or "dashboard_only".
	HermesApprovals string `json:"hermesApprovals"`
	// HomeAssistant names the homeassistant provider instance; global only,
	// read when the server starts.
	HomeAssistant string `json:"homeAssistant"`
	// HAArea is the Home Assistant area id the device stands in, which
	// settles a name several devices share; device only.
	HAArea string `json:"haArea"`
	// HASensitive is what a sensitive home device (locks, alarms, gates…)
	// takes by voice: "woken" (a wake or press session) or "deny"; global only.
	HASensitive string `json:"haSensitive"`
	// HAFavorites are Home Assistant entity ids every turn sees first, in
	// this order; global only.
	HAFavorites []string `json:"haFavorites"`
	// MCP names the mcp provider instances whose servers run, at most 32;
	// global only, applied without a restart.
	MCP []string `json:"mcp"`
	// T3Code names the t3code provider instances whose threads the
	// subscriptions hub and the t3 tools follow, at most 32; none turns them
	// off; global only, applied without a restart.
	T3Code []string `json:"t3code"`
	// CatalogSources names the catalog's provider instances (torrplayer,
	// torrserver, youtube-music), in the order Home and search show them, at
	// most 32; none leaves the catalog empty; global only, applied without a
	// restart.
	CatalogSources []string `json:"catalogSources"`
	// YouTubeVotes adds a YouTube result's likes and estimated dislikes from
	// Return YouTube Dislike, a third party that then learns which video ids
	// the household looks at; off unless set; global only.
	YouTubeVotes bool `json:"youtubeVotes"`
	// CatalogVoiceovers are the dubs and sources the household prefers and
	// avoids, in order; global only.
	CatalogVoiceovers CatalogVoiceovers `json:"catalogVoiceovers"`
	// QuietHours hold task updates back on the device at night.
	QuietHours QuietHours `json:"quietHours"`
	// FollowUp is whether, and how long and how often in a row, a spoken
	// reply leaves the microphone open.
	FollowUp FollowUp `json:"followUp"`
	// Alarms is how alarms, timers and reminders ramp, how long they ring
	// and how they snooze; each key unset is the device's default.
	Alarms AlarmsSetting `json:"alarms"`
	// LogRetentionDays is how many days of stored logs, before today's, the
	// server keeps; global only.
	LogRetentionDays int `json:"logRetentionDays"`
	// SubscriptionDelivery is how a subscription made without one reaches
	// the device: "speaker" (a fixed spoken template) or "agent" (an event
	// turn).
	SubscriptionDelivery string `json:"subscriptionDelivery"`
	// SubscriptionApprovals is whether a subscription's approval question may
	// be answered by voice: "off" (the dashboard or a screen only) or "voice"
	// (a woken turn, once the question was told on the device).
	SubscriptionApprovals string `json:"subscriptionApprovals"`
	// TelemetryExport names the otlp provider instance stored logs and metrics
	// are forwarded to, "" for none; global only, applied without a restart.
	TelemetryExport string `json:"telemetryExport"`
	// Tracing is whether the server records spans, the share of new traces
	// sampled and the finished traces kept; global only, applied without a
	// restart. Spans go to the telemetryExport instance too.
	Tracing Tracing `json:"tracing"`
	// BusyEntities are the Home Assistant states that make every device busy
	// while they hold (busy mode), at most 32; global only.
	BusyEntities []BusyEntity `json:"busyEntities"`
	// AfterBusy is what becomes of the updates busy mode held once it ends:
	// "announce" speaks them, "inbox" leaves them for the next woken turn.
	AfterBusy string `json:"afterBusy"`
	// RemoteTargets is whether other devices' turns may drive this one
	// (play on it, move its volume, set its alarms): "allow", "room" (only
	// devices of its own room) or "deny". Its own turns always may.
	RemoteTargets string `json:"remoteTargets"`
	// ErrorSound is what a device plays when a request fails: "prompt" (a
	// spoken prompt in the device's language), "beep" or "off".
	ErrorSound string `json:"errorSound"`
}

// InboxAfterBusy says the updates held while the device was busy wait for
// the next woken turn rather than being announced when it ends.
func (v Values) InboxAfterBusy() bool { return v.AfterBusy == AfterBusyInbox }

const (
	ApprovalsVoice         = "voice"
	ApprovalsDashboardOnly = "dashboard_only"

	HASensitiveWoken = "woken"
	HASensitiveDeny  = "deny"

	AfterBusyAnnounce = "announce"
	AfterBusyInbox    = "inbox"

	SubscriptionSpeaker = "speaker"
	SubscriptionAgent   = "agent"

	SubscriptionApprovalsOff   = "off"
	SubscriptionApprovalsVoice = "voice"

	ErrorSoundPrompt = "prompt"
	ErrorSoundBeep   = "beep"
	ErrorSoundOff    = "off"

	RemoteTargetsAllow = "allow"
	RemoteTargetsRoom  = "room"
	RemoteTargetsDeny  = "deny"
)

// Overrides are the keys one scope sets, as raw JSON.
type Overrides map[string]json.RawMessage

var defaults = Overrides{
	"language":              json.RawMessage(`"en"`),
	"timeZone":              json.RawMessage(`"UTC"`),
	"mode":                  json.RawMessage(`"fast"`),
	"allowedModes":          json.RawMessage(`["fast","smart"]`),
	"modeRevert":            json.RawMessage(`"after_idle"`),
	"weatherLocation":       json.RawMessage(`"Bishkek, Kyrgyzstan"`),
	"historyTurns":          json.RawMessage(`20`),
	"compactAfter":          json.RawMessage(`10`),
	"compactionMaxTokens":   json.RawMessage(`8192`),
	"hermesApprovals":       json.RawMessage(`"voice"`),
	"quietHours":            json.RawMessage(`{"enabled":true,"start":"23:00","end":"08:00"}`),
	"haSensitive":           json.RawMessage(`"woken"`),
	"followUp":              json.RawMessage(`{"enabled":true,"windowMs":5000,"limit":3}`),
	"logRetentionDays":      json.RawMessage(`7`),
	"subscriptionDelivery":  json.RawMessage(`"speaker"`),
	"subscriptionApprovals": json.RawMessage(`"off"`),
	"afterBusy":             json.RawMessage(`"announce"`),
	"remoteTargets":         json.RawMessage(`"allow"`),
	"errorSound":            json.RawMessage(`"prompt"`),
	"tracing":               json.RawMessage(`{"enabled":true,"sampleRatio":1,"ringSize":500}`),
}

// Defaults is what a device gets with nothing set anywhere.
func Defaults() Values {
	v, _ := resolve(defaults)
	return v
}

func resolve(layers ...Overrides) (Values, error) {
	merged := Overrides{}
	for _, l := range layers {
		for k, raw := range l {
			merged[k] = raw
		}
	}
	raw, err := json.Marshal(merged)
	if err != nil {
		return Values{}, err
	}
	var v Values
	return v, json.Unmarshal(raw, &v)
}

// Merge is Defaults, then global, then device: per key, the last scope that
// sets it wins, whatever its value.
func Merge(global, device Overrides) (Values, error) { return resolve(defaults, global, device) }

func isNull(raw json.RawMessage) bool { return bytes.Equal(bytes.TrimSpace(raw), []byte("null")) }

func isEmptyString(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte(`""`))
}

// parse checks a request body: known keys with the right types and valid values.
var knownKeys = func() map[string]bool {
	k := map[string]bool{}
	t := reflect.TypeOf(Values{})
	for i := 0; i < t.NumField(); i++ {
		k[strings.Split(t.Field(i).Tag.Get("json"), ",")[0]] = true
	}
	return k
}()

// deviceNoEmpty are keys a device cannot set to "": null removes an override.
var deviceNoEmpty = []string{"mode", "modeRevert", "language", "timeZone", "hermesApprovals", "subscriptionDelivery", "subscriptionApprovals", "afterBusy", "remoteTargets", "errorSound"}

// globalOnly are keys a device cannot set at all.
var globalOnly = []string{"hermes", "homeAssistant", "haSensitive", "haFavorites", "mcp", "t3code", "catalogSources", "catalogVoiceovers", "youtubeVotes", "logRetentionDays", "telemetryExport", "tracing", "busyEntities"}

// deviceOnly are keys the global scope cannot set at all.
var deviceOnly = []string{"haArea"}

// haAreaRe is a Home Assistant area id: a slug.
var haAreaRe = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)

// haEntityRe is a Home Assistant entity id, at most maxHAEntityID long.
var haEntityRe = regexp.MustCompile(`^[a-z][a-z0-9_]*\.[a-z0-9_]+$`)

// instanceRe is a provider instance name, as providers takes it.
var instanceRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

const (
	maxHAEntityID  = 128
	maxHAFavorites = 32
	maxMCP         = 32
)

// MaxPersona is the longest persona in characters: it rides in every turn's
// system prompt, so it is a few paragraphs, not a document.
const MaxPersona = 2000

func parse(body []byte, device bool) (Overrides, error) {
	var o Overrides
	if err := json.Unmarshal(body, &o); err != nil || o == nil {
		return nil, ErrInvalid
	}
	normalizeNames(o)
	for k := range o {
		if !knownKeys[k] {
			return nil, ErrInvalid
		}
	}
	only := deviceOnly
	if device {
		only = globalOnly
	}
	for _, k := range only {
		if _, ok := o[k]; ok {
			return nil, ErrInvalid
		}
	}
	if device {
		for _, k := range deviceNoEmpty {
			if raw, ok := o[k]; ok && isEmptyString(raw) {
				return nil, ErrInvalid
			}
		}
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	var v Values
	if err := dec.Decode(&v); err != nil || dec.More() {
		return nil, ErrInvalid
	}
	if utf8.RuneCountInString(v.Persona) > MaxPersona {
		return nil, ErrInvalid
	}
	if v.HistoryTurns < 0 || v.HistoryTurns > maxHistoryTurns {
		return nil, ErrInvalid
	}
	if raw, ok := o["compactAfter"]; ok && !isNull(raw) && (v.CompactAfter < MinCompactAfter || v.CompactAfter > maxCompactAfter) {
		return nil, ErrInvalid
	}
	if raw, ok := o["compactionMaxTokens"]; ok && !isNull(raw) &&
		(v.CompactionMaxTokens < MinCompactionMaxTokens || v.CompactionMaxTokens > MaxCompactionMaxTokens) {
		return nil, ErrInvalid
	}
	if raw, ok := o["logRetentionDays"]; ok && !isNull(raw) && (v.LogRetentionDays < minLogRetentionDays || v.LogRetentionDays > maxLogRetentionDays) {
		return nil, ErrInvalid
	}
	if raw, ok := o["quietHours"]; ok && !isNull(raw) && !validQuietHours(raw) {
		return nil, ErrInvalid
	}
	if raw, ok := o["haArea"]; ok && !isNull(raw) && !haAreaRe.MatchString(v.HAArea) {
		return nil, ErrInvalid
	}
	if !validFavorites(v.HAFavorites) || !validInstances(v.MCP) || !validInstances(v.T3Code) || !validInstances(v.CatalogSources) ||
		(v.TelemetryExport != "" && !instanceRe.MatchString(v.TelemetryExport)) ||
		!validInstances(v.Music) || (v.MusicPlayer != "" && !instanceRe.MatchString(v.MusicPlayer)) ||
		(v.XSearch != "" && !instanceRe.MatchString(v.XSearch)) {
		return nil, ErrInvalid
	}
	if raw, ok := o["busyEntities"]; ok && !isNull(raw) && !validBusyEntities(raw) {
		return nil, ErrInvalid
	}
	if raw, ok := o["catalogVoiceovers"]; ok && !isNull(raw) && !validVoiceovers(raw) {
		return nil, ErrInvalid
	}
	if raw, ok := o["followUp"]; ok && !isNull(raw) && !validFollowUp(raw) {
		return nil, ErrInvalid
	}
	if raw, ok := o["alarms"]; ok && !isNull(raw) && !validAlarms(raw) {
		return nil, ErrInvalid
	}
	if raw, ok := o["tracing"]; ok && !isNull(raw) && !validTracing(raw) {
		return nil, ErrInvalid
	}
	if raw, ok := o["timeZone"]; ok && !isNull(raw) && !isEmptyString(raw) {
		if _, err := time.LoadLocation(v.TimeZone); err != nil {
			return nil, ErrInvalid
		}
	}
	for i, sc := range v.MemoryScopes {
		if (sc != "global" && sc != "device") || slices.Contains(v.MemoryScopes[:i], sc) {
			return nil, ErrInvalid
		}
	}
	switch v.ModeRevert {
	case "", "after_idle", "after_session", "never":
	default:
		return nil, ErrInvalid
	}
	switch v.HermesApprovals {
	case "", ApprovalsVoice, ApprovalsDashboardOnly:
	default:
		return nil, ErrInvalid
	}
	switch v.HASensitive {
	case "", HASensitiveWoken, HASensitiveDeny:
	default:
		return nil, ErrInvalid
	}
	switch v.SubscriptionDelivery {
	case "", SubscriptionSpeaker, SubscriptionAgent:
	default:
		return nil, ErrInvalid
	}
	switch v.SubscriptionApprovals {
	case "", SubscriptionApprovalsOff, SubscriptionApprovalsVoice:
	default:
		return nil, ErrInvalid
	}
	switch v.AfterBusy {
	case "", AfterBusyAnnounce, AfterBusyInbox:
	default:
		return nil, ErrInvalid
	}
	switch v.RemoteTargets {
	case "", RemoteTargetsAllow, RemoteTargetsRoom, RemoteTargetsDeny:
	default:
		return nil, ErrInvalid
	}
	switch v.ErrorSound {
	case "", ErrorSoundPrompt, ErrorSoundBeep, ErrorSoundOff:
	default:
		return nil, ErrInvalid
	}
	return o, nil
}

// validFavorites are at most maxHAFavorites distinct entity ids.
func validFavorites(ids []string) bool {
	if len(ids) > maxHAFavorites {
		return false
	}
	for i, id := range ids {
		if len(id) > maxHAEntityID || !haEntityRe.MatchString(id) || slices.Contains(ids[:i], id) {
			return false
		}
	}
	return true
}

// validInstances are at most maxMCP distinct instance names.
func validInstances(names []string) bool {
	if len(names) > maxMCP {
		return false
	}
	for i, n := range names {
		if !instanceRe.MatchString(n) || slices.Contains(names[:i], n) {
			return false
		}
	}
	return true
}

// Store is the database: queries, and transactions for a write; *pgxpool.Pool
// is one.
type Store interface {
	db.DBTX
	Begin(ctx context.Context) (pgx.Tx, error)
}

type Service struct {
	store Store
	q     *db.Queries

	mu      sync.Mutex
	checks  map[string]func(ctx context.Context, name string) error
	changed []func()
	// heard after each device set PutDevice stored
	deviceChanged []func(deviceID string)
}

func NewService(store Store) *Service { return &Service{store: store, q: db.New(store)} }

// CheckMCP makes PutGlobal refuse an mcp list with a name the list stored
// before lacks and check refuses: ErrInvalid is a bad name, any other error
// a failure. A name listed before passes, so an instance deleted since never
// blocks the other keys.
func (s *Service) CheckMCP(check func(ctx context.Context, name string) error) {
	s.CheckList("mcp", check)
}

// CheckList is CheckMCP for another key holding a list of instance names
// (t3code, catalogSources).
func (s *Service) CheckList(key string, check func(ctx context.Context, name string) error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.checks == nil {
		s.checks = map[string]func(context.Context, string) error{}
	}
	s.checks[key] = check
}

// OnDeviceChange calls fn with the device after each set PutDevice stored;
// fn must not block.
func (s *Service) OnDeviceChange(fn func(deviceID string)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deviceChanged = append(s.deviceChanged, fn)
}

// OnGlobalChange calls fn after each global set PutGlobal stored; fn must
// not block.
func (s *Service) OnGlobalChange(fn func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.changed = append(s.changed, fn)
}

func (s *Service) get(ctx context.Context, scope string) (Overrides, error) {
	return get(ctx, s.q, scope)
}

func get(ctx context.Context, q *db.Queries, scope string) (Overrides, error) {
	raw, err := q.GetSettings(ctx, scope)
	if errors.Is(err, pgx.ErrNoRows) {
		return Overrides{}, nil
	}
	if err != nil {
		return nil, err
	}
	o := Overrides{}
	if err := json.Unmarshal(raw, &o); err != nil {
		return nil, err
	}
	normalizeNames(o)
	return o, nil
}

func (s *Service) Global(ctx context.Context) (Overrides, error) { return s.get(ctx, globalScope) }

// PutGlobal replaces the whole global set: a key left out is unset, so a
// caller changing one key reads the set, changes it and sends it whole. An
// empty string or null means unset. It takes the settings lock and refuses a
// set whose effective values name a mode they did not name before and that
// has no row (checkModes), or a key that newly names a provider instance with
// no row (checkInstances).
func (s *Service) PutGlobal(ctx context.Context, body []byte) (Overrides, error) {
	in, err := parse(body, false)
	if err != nil {
		return nil, err
	}
	out := Overrides{}
	for k, raw := range in {
		if !isNull(raw) && !isEmptyString(raw) {
			out[k] = raw
		}
	}
	if err := s.checkNewMCP(ctx, out); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	after, err := globalModes(out)
	if err != nil {
		return nil, err
	}
	err = pgx.BeginFunc(ctx, s.store, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		if err := Lock(ctx, q); err != nil {
			return err
		}
		cur, err := get(ctx, q, globalScope)
		if err != nil {
			return err
		}
		before, err := globalModes(cur)
		if err != nil {
			return err
		}
		if err := checkModes(ctx, q, before, after); err != nil {
			return err
		}
		if err := checkInstances(ctx, q, overrideInstances(cur), overrideInstances(out)); err != nil {
			return err
		}
		return q.PutSettings(ctx, db.PutSettingsParams{Scope: globalScope, Data: raw})
	})
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	changed := slices.Clone(s.changed)
	s.mu.Unlock()
	for _, fn := range changed {
		fn()
	}
	return out, nil
}

// checkNewMCP runs each CheckList check on the names next adds to its
// stored list.
func (s *Service) checkNewMCP(ctx context.Context, next Overrides) error {
	s.mu.Lock()
	checks := maps.Clone(s.checks)
	s.mu.Unlock()
	keys := slices.Sorted(maps.Keys(checks))
	var cur Overrides
	for _, key := range keys {
		if next[key] == nil {
			continue
		}
		if cur == nil {
			var err error
			if cur, err = s.get(ctx, globalScope); err != nil {
				return err
			}
		}
		var before, after []string
		if raw := cur[key]; raw != nil {
			_ = json.Unmarshal(raw, &before)
		}
		if err := json.Unmarshal(next[key], &after); err != nil {
			return ErrInvalid
		}
		for _, name := range after {
			if slices.Contains(before, name) {
				continue
			}
			err := checks[key](ctx, name)
			if errors.Is(err, ErrInvalid) {
				return &InvalidError{Key: key}
			}
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Service) Device(ctx context.Context, id string) (Overrides, error) {
	scope, err := s.deviceScope(ctx, id)
	if err != nil {
		return nil, err
	}
	return s.get(ctx, scope)
}

// PutDevice patches the device's set atomically: keys present override (null
// removes the override), keys absent are untouched. The merged result must keep
// the effective mode among the effective allowed modes, and its own keys may
// name a mode they did not name before only if it has a row (checkModes), and
// a provider instance likewise (checkInstances). The patch holds the settings
// lock from its reads to its write, so every settings write and every mode or
// provider delete waits for it and the checks see what it stores.
func (s *Service) PutDevice(ctx context.Context, id string, body []byte) (Overrides, error) {
	scope, err := s.deviceScope(ctx, id)
	if err != nil {
		return nil, err
	}
	in, err := parse(body, true)
	if err != nil {
		return nil, err
	}
	var data []byte
	err = pgx.BeginFunc(ctx, s.store, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		if err := Lock(ctx, q); err != nil {
			return err
		}
		g, err := get(ctx, q, globalScope)
		if err != nil {
			return err
		}
		cur, err := get(ctx, q, scope)
		if err != nil {
			return err
		}
		before, beforeInstances := overrideModes(cur), overrideInstances(cur)
		patch, drop := Overrides{}, []string{}
		for k, raw := range in {
			if isNull(raw) {
				drop = append(drop, k)
				delete(cur, k)
			} else {
				patch[k] = raw
				cur[k] = raw
			}
		}
		eff, err := Merge(g, cur)
		if err != nil {
			return err
		}
		if !slices.Contains(eff.AllowedModes, eff.Mode) {
			return ErrInvalid
		}
		if err := checkModes(ctx, q, before, overrideModes(cur)); err != nil {
			return err
		}
		if err := checkInstances(ctx, q, beforeInstances, overrideInstances(cur)); err != nil {
			return err
		}
		raw, err := json.Marshal(patch)
		if err != nil {
			return err
		}
		dev, _ := parseID(scope)
		data, err = q.PatchSettings(ctx, db.PatchSettingsParams{DeviceID: dev, Patch: raw, Drop: drop})
		if errors.Is(err, pgx.ErrNoRows) {
			// the device was deleted since its check
			return ErrNotFound
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	changed := slices.Clone(s.deviceChanged)
	s.mu.Unlock()
	for _, fn := range changed {
		fn(id)
	}
	out := Overrides{}
	return out, json.Unmarshal(data, &out)
}

// Effective is Defaults, global and the device's own keys; a device with no row
// of its own still resolves.
func (s *Service) Effective(ctx context.Context, deviceID string) (Values, error) {
	g, err := s.get(ctx, globalScope)
	if err != nil {
		return Values{}, err
	}
	d := Overrides{}
	if u, ok := parseID(deviceID); ok {
		if d, err = s.get(ctx, u.String()); err != nil {
			return Values{}, err
		}
	}
	return Merge(g, d)
}

// EffectiveGlobal is Defaults and the global keys, without any device's.
func (s *Service) EffectiveGlobal(ctx context.Context) (Values, error) {
	g, err := s.get(ctx, globalScope)
	if err != nil {
		return Values{}, err
	}
	return Merge(g, nil)
}

func parseID(id string) (pgtype.UUID, bool) {
	var u pgtype.UUID
	return u, u.Scan(id) == nil
}

// deviceScope is the scope of an existing device: its id spelled as the hub and
// Effective's callers spell it, whatever spelling the path used.
func (s *Service) deviceScope(ctx context.Context, id string) (string, error) {
	u, ok := parseID(id)
	if !ok {
		return "", ErrNotFound
	}
	_, err := s.q.DeviceByID(ctx, u)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	return u.String(), nil
}
