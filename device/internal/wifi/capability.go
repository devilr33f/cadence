package wifi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/atomicfile"
	"git.shino.boo/devilreef/cadence/device/internal/capability"
)

const (
	// CapabilityID manages the board's wi-fi: it joins the network the config
	// names, keeping every saved network as the fallback.
	CapabilityID = "network.wifi"

	// secretPSK is the password of the network the config names.
	secretPSK = "psk"

	// defaultPriority sits above the vendor's saved networks (yandex's own use
	// 2222), so a managed network wins the supplicant's own selection too.
	defaultPriority = 3000

	maxScanAPs = 40

	// codes the state carries
	codeSwitching  = "switching"
	codeRolledBack = "rolled_back"
	codePskMissing = "psk_missing"
	// a rollback's reasons
	reasonNoIP     = "no_ip"
	reasonNoServer = "no_server"

	configSchema = `{"type":"object","properties":` +
		`{"enabled":{"type":"boolean"},` +
		`"ssid":{"type":"string","minLength":1,"maxLength":32},` +
		`"bssid":{"type":"string","pattern":"^([0-9a-fA-F]{2}:){5}[0-9a-fA-F]{2}$"},` +
		`"priority":{"type":"integer","minimum":1,"maximum":9999}},` +
		`"required":["enabled"],"additionalProperties":false}`

	stateSchema = `{"type":"object","properties":` +
		`{"state":{"enum":["completed","associating","scanning","disconnected","inactive","unknown","unsupported"]},` +
		`"managed":{"type":"boolean"},` +
		`"ssid":{"type":"string"},` +
		`"bssid":{"type":"string"},` +
		`"freqMHz":{"type":"integer","minimum":0,"maximum":71200},` +
		`"ip":{"type":"string"},` +
		`"target":{"type":"string"},` +
		`"code":{"type":"string","pattern":"^[a-z0-9_]{1,64}$"},` +
		`"reason":{"enum":["no_ip","no_server"]}},` +
		`"required":["state","managed"],"additionalProperties":false}`

	scanArgsSchema = `{"type":"object","additionalProperties":false}`
)

// Timings are the watcher's. a switch must hold the new network with an
// address within IPWithin of its start, else it rolls back with no_ip; the
// server link must then come up over it within LinkWithin of that address,
// else no_server. the link is told to redial at once when the address comes,
// so LinkWithin covers its first dial (15 s at most) and the retries after it
// (1, 2, 4, 8 s apart, each up to 15 s): at least three whole dials. Settle is
// how long a boot waits before it re-selects the managed network, giving the
// supplicant time to associate on its own first; Poll is how often the link
// is read.
type Timings struct {
	IPWithin, LinkWithin, Settle, Poll time.Duration
}

// DefaultTimings: a failed switch costs the device its link for two minutes at
// most, then the time to rejoin the previous network and dial.
func DefaultTimings() Timings {
	return Timings{IPWithin: 60 * time.Second, LinkWithin: 60 * time.Second, Settle: 30 * time.Second, Poll: 5 * time.Second}
}

var bssidFormat = regexp.MustCompile(`^([0-9a-fA-F]{2}:){5}[0-9a-fA-F]{2}$`)

// settings are kept under the device's data dir: what to manage, the ssid a
// rollback gave up on with the config generation it gave up under (a newer
// generation naming it tries again), and the switch in flight, so a restart
// in the middle of one finishes it or rolls it back. the password is never
// kept: it reaches the supplicant alone.
type settings struct {
	Enabled          bool           `json:"enabled"`
	SSID             string         `json:"ssid,omitempty"`
	BSSID            string         `json:"bssid,omitempty"`
	Priority         int            `json:"priority,omitempty"`
	RolledBack       string         `json:"rolledBack,omitempty"`
	RolledBackGen    int64          `json:"rolledBackGen,omitempty"`
	RolledBackReason string         `json:"rolledBackReason,omitempty"`
	Pending          *pendingSwitch `json:"pending,omitempty"`
}

// state never carries the password, nor anything the supplicant said.
type state struct {
	State   string `json:"state"`
	Managed bool   `json:"managed"`
	SSID    string `json:"ssid,omitempty"`
	BSSID   string `json:"bssid,omitempty"`
	FreqMHz int    `json:"freqMHz,omitempty"`
	IP      string `json:"ip,omitempty"`
	Target  string `json:"target,omitempty"`
	Code    string `json:"code,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

// DialFunc has the board seam's shape; a test passes one that answers a fake.
type DialFunc = func(ctx context.Context) (Backend, error)

// Capability is network.wifi. it acts only when a config enables it and names
// a network: the network is added (nothing is ever removed) and selected, and
// the switch counts only once the server link came back over it; else the
// previous network is selected back. with the capability off, or after a
// rollback, it only reports the link.
type Capability struct {
	dial DialFunc
	path string
	log  *slog.Logger
	t    Timings

	mu         sync.Mutex
	kept       settings
	gen        int64  // the config generation in force
	st         Status // the link as last read
	cur        state
	pskMissing bool
	be         Backend
	sw         *switchState
	// the server link as core tells it
	linkUp   bool
	linkUpAt time.Time
	redial   func()
	// after a rollback, the ssid it gave up on: redial once another network
	// holds the link with an address
	rejoin string

	cancel context.CancelFunc
	done   chan struct{}
	kick   chan struct{}
	stateC chan json.RawMessage
}

// NewCapability reads the kept settings and starts reporting the link.
func NewCapability(ctx context.Context, dial DialFunc, path string, log *slog.Logger) *Capability {
	return NewCapabilityWith(ctx, dial, path, log, DefaultTimings())
}

// NewCapabilityWith is NewCapability with the watcher's timings, for tests.
func NewCapabilityWith(ctx context.Context, dial DialFunc, path string, log *slog.Logger, t Timings) *Capability {
	c := &Capability{
		dial: dial, path: path, log: log, t: t,
		st:     Status{State: StateUnknown, CurrentID: -1},
		redial: func() {},
		kick:   make(chan struct{}, 1), stateC: make(chan json.RawMessage, 1),
	}
	raw, err := os.ReadFile(path) //nolint:gosec // the device's own settings path
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		log.Warn("settings_invalid", "capability", CapabilityID, "reason", "read")
	default:
		if err := json.Unmarshal(raw, &c.kept); err != nil {
			c.kept = settings{}
			log.Warn("settings_invalid", "capability", CapabilityID, "reason", "format")
		}
	}
	c.resumeLocked(time.Now())
	c.cur = c.stateFor(c.st)
	cctx, cancel := context.WithCancel(ctx)
	c.cancel, c.done = cancel, make(chan struct{})
	go c.watch(cctx, time.Now())
	return c
}

func (*Capability) Descriptor() capability.Descriptor {
	return capability.Descriptor{
		ID: CapabilityID, Version: 1, ConfigSchema: configSchema, StateSchema: stateSchema,
		Secrets:  []capability.SecretSpec{{Name: secretPSK, Description: "the wi-fi password of the network named in the config"}},
		Commands: map[string]string{"scan": scanArgsSchema},
	}
}

// config is what a push carries; the password comes beside it as a secret.
type config struct {
	Enabled  bool
	SSID     string
	BSSID    string
	Priority int
}

func parseConfig(raw []byte) (config, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		return config{}, false
	}
	var f struct {
		Enabled  json.RawMessage `json:"enabled"`
		SSID     json.RawMessage `json:"ssid"`
		BSSID    json.RawMessage `json:"bssid"`
		Priority json.RawMessage `json:"priority"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return config{}, false
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return config{}, false
	}
	var out config
	if f.Enabled == nil || bytes.Equal(f.Enabled, []byte("null")) || json.Unmarshal(f.Enabled, &out.Enabled) != nil {
		return config{}, false
	}
	stringField := func(raw json.RawMessage, dst *string) bool {
		return raw == nil || bytes.Equal(raw, []byte("null")) || json.Unmarshal(raw, dst) == nil
	}
	if !stringField(f.SSID, &out.SSID) || !stringField(f.BSSID, &out.BSSID) {
		return config{}, false
	}
	if f.Priority != nil && !bytes.Equal(f.Priority, []byte("null")) && json.Unmarshal(f.Priority, &out.Priority) != nil {
		return config{}, false
	}
	// an absent priority is 0 and means the default; a bssid of "" pins none
	if len(out.SSID) > 32 || (out.BSSID != "" && !bssidFormat.MatchString(out.BSSID)) ||
		(out.Priority != 0 && (out.Priority < 1 || out.Priority > 9999)) {
		return config{}, false
	}
	return out, true
}

// Apply takes a config with no secret.
func (c *Capability) Apply(ctx context.Context, gen int64, cfg json.RawMessage) error {
	return c.ApplySecrets(ctx, gen, cfg, nil)
}

// ApplySecrets takes {enabled, ssid, bssid?, priority?} and the network's
// password. enabled without a ssid is refused; enabled with no usable password
// changes nothing: the link is only reported, as psk_missing. a switch in
// flight is never dropped: turning the capability off leaves it to finish or
// roll back.
func (c *Capability) ApplySecrets(ctx context.Context, gen int64, raw json.RawMessage, secrets map[string]string) error {
	cfg, ok := parseConfig(raw)
	if !ok || (cfg.Enabled && cfg.SSID == "") {
		return &capability.ApplyError{Code: "invalid"}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gen = gen
	c.pskMissing = false
	if !cfg.Enabled {
		c.kept = settings{Enabled: false, Pending: c.kept.Pending}
		c.save()
		c.setLocked(c.stateFor(c.st))
		return nil
	}
	kept := settings{Enabled: true, SSID: cfg.SSID, BSSID: cfg.BSSID, Priority: cfg.Priority, Pending: c.kept.Pending}
	if c.kept.RolledBack == cfg.SSID && (c.kept.RolledBackGen == gen || c.kept.RolledBackGen == 0) {
		// a rollback gave up on this network under this generation (or, kept
		// by an older build, under an unknown one: the one in force now): stay
		// put until a newer push names it, which a retry from the dashboard is
		kept.RolledBack, kept.RolledBackGen, kept.RolledBackReason = c.kept.RolledBack, gen, c.kept.RolledBackReason
		c.kept = kept
		c.save()
		c.setLocked(c.stateFor(c.st))
		return nil
	}
	c.kept = kept
	c.save()
	psk := secrets[secretPSK]
	if len(psk) < 8 || len(psk) > 63 {
		c.pskMissing = true
		c.setLocked(c.stateFor(c.st))
		return nil
	}
	if err := c.applyTarget(ctx, kept, psk); err != nil {
		c.log.Warn("wifi_apply_failed", "capability", CapabilityID, "code", "wpa")
		return &capability.ApplyError{Code: "wpa"}
	}
	return nil
}

// applyTarget upserts the network and starts a switch to it, unless the link
// already holds it with no switch in flight; callers hold mu.
func (c *Capability) applyTarget(ctx context.Context, kept settings, psk string) error {
	be, err := c.backend(ctx)
	if err != nil {
		return err
	}
	priority := kept.Priority
	if priority == 0 {
		priority = defaultPriority
	}
	id, err := be.Upsert(ctx, kept.SSID, psk, kept.BSSID, priority)
	if err != nil {
		return err
	}
	st, err := be.Status(ctx)
	if err != nil {
		return err
	}
	c.st = st
	on := onTarget(st, kept.SSID, kept.BSSID)
	if c.sw != nil && c.sw.target == kept.SSID && c.sw.bssid == kept.BSSID {
		// the same switch, resumed after a restart or pushed again: it keeps
		// its previous network and its clock
		c.sw.targetID = id
		c.persistSwitchLocked()
		if !on {
			if err := be.Select(ctx, id); err != nil {
				return err
			}
		}
		c.setLocked(c.stateFor(c.st))
		c.wake()
		return nil
	}
	if on && c.sw == nil {
		c.setLocked(c.stateFor(st))
		return nil
	}
	// the network to fall back to is the one that held the link before any
	// switch now in flight
	prevID, prevSSID := st.CurrentID, st.SSID
	if c.sw != nil {
		prevID, prevSSID = c.sw.previousID, c.sw.previousSSID
	}
	if !on {
		if err := be.Select(ctx, id); err != nil {
			return err
		}
	}
	c.beginSwitchLocked(kept.SSID, kept.BSSID, id, prevID, prevSSID)
	return nil
}

// onTarget says whether the status holds the network with an address.
func onTarget(st Status, ssid, bssid string) bool {
	if st.State != StateCompleted || st.IP == "" || st.SSID != ssid {
		return false
	}
	return bssid == "" || strings.EqualFold(st.BSSID, bssid)
}

// stateFor builds the state from a status and what the capability knows;
// callers hold mu.
func (c *Capability) stateFor(st Status) state {
	s := state{State: st.State, Managed: c.kept.Enabled, SSID: st.SSID, BSSID: st.BSSID,
		FreqMHz: st.FreqMHz, IP: st.IP}
	if c.kept.Enabled {
		s.Target = c.kept.SSID
	}
	switch {
	case c.sw != nil:
		s.Code, s.Target = codeSwitching, c.sw.target
	case c.kept.Enabled && c.kept.RolledBack != "" && c.kept.RolledBack == c.kept.SSID:
		s.Code, s.Reason = codeRolledBack, c.kept.RolledBackReason
	case c.kept.Enabled && c.pskMissing:
		s.Code = codePskMissing
	}
	return s
}

// Command runs scan: the networks around, best signal first.
func (c *Capability) Command(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, error) {
	if name != "scan" || !isEmptyObject(args) {
		return nil, capability.ErrUnsupported
	}
	be, err := c.dial(ctx)
	if err != nil {
		return nil, &capability.CommandError{Code: "unavailable"}
	}
	aps, err := be.Scan(ctx)
	if err != nil {
		return nil, &capability.CommandError{Code: "wpa"}
	}
	if len(aps) > maxScanAPs {
		aps = aps[:maxScanAPs]
	}
	out := make([]map[string]any, 0, len(aps))
	for _, ap := range aps {
		out = append(out, map[string]any{
			"ssid": ap.SSID, "bssid": ap.BSSID, "freqMHz": ap.FreqMHz,
			"signalDbm": ap.SignalDbm, "security": ap.Security,
		})
	}
	return json.Marshal(map[string]any{"networks": out})
}

// CommandWait says scan takes its time (the supplicant scans asynchronously).
func (*Capability) CommandWait(name string) time.Duration {
	if name == "scan" {
		return 20 * time.Second
	}
	return 0
}

func isEmptyObject(raw json.RawMessage) bool {
	raw = bytes.TrimSpace(raw)
	return len(raw) == 0 || string(raw) == "{}"
}

// State is the latest state, dropping older unread ones.
func (c *Capability) State() <-chan json.RawMessage { return c.stateC }

// Stop ends the watcher.
func (c *Capability) Stop() {
	c.cancel()
	<-c.done
}

// backend dials the board's mechanism once and keeps it: wpa_cli holds no
// state of ours between calls, so one dial that worked is kept.
func (c *Capability) backend(ctx context.Context) (Backend, error) {
	if c.be != nil {
		return c.be, nil
	}
	be, err := c.dial(ctx)
	if err != nil {
		return nil, err
	}
	c.be = be
	return be, nil
}

func (c *Capability) wake() {
	select {
	case c.kick <- struct{}{}:
	default:
	}
}

// setLocked offers a state when it changed; callers hold mu.
func (c *Capability) setLocked(s state) {
	if s == c.cur {
		return
	}
	c.cur = s
	b, err := json.Marshal(s)
	if err != nil {
		return
	}
	for {
		select {
		case c.stateC <- b:
			return
		default:
		}
		select {
		case <-c.stateC:
		default:
		}
	}
}

// save keeps c.kept; callers hold mu.
func (c *Capability) save() {
	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		c.log.Warn("settings_failed", "capability", CapabilityID, "reason", "write")
		return
	}
	if err := atomicfile.Write(c.path, mustJSON(c.kept)); err != nil {
		c.log.Warn("settings_failed", "capability", CapabilityID, "reason", "write")
	}
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("wifi: marshal: %v", err))
	}
	return b
}

// SettingsPath is where the capability keeps its settings under a device's
// data dir.
func SettingsPath(dataDir string) string { return filepath.Join(dataDir, "state", "wifi") }

var _ capability.Provider = (*Capability)(nil)
var _ capability.SecretApplier = (*Capability)(nil)
var _ capability.CommandWaiter = (*Capability)(nil)
var _ capability.LinkWatcher = (*Capability)(nil)
