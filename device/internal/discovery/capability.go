package discovery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/atomicfile"
	"git.shino.boo/devilreef/cadence/device/internal/capability"
)

const (
	// CapabilityID switches the device's advertisement on its local network.
	CapabilityID = "network.discovery"

	configSchema = `{"type":"object","properties":{"enabled":{"type":"boolean"}},"additionalProperties":false}`
	stateSchema  = `{"type":"object","properties":{"enabled":{"type":"boolean"},"advertising":{"type":"boolean"}},` +
		`"required":["enabled","advertising"],"additionalProperties":false}`

	// how long a failed start waits before the next (the port taken, no interface yet)
	retryEvery = 30 * time.Second
)

type settings struct {
	Enabled bool `json:"enabled"`
}

type status struct {
	Enabled     bool `json:"enabled"`
	Advertising bool `json:"advertising"`
}

var errInvalid = errors.New("discovery: invalid settings")

// decode takes {"enabled": bool} over base; an omitted key keeps base, anything else is refused
func decode(raw []byte, base settings) (settings, error) {
	var f struct {
		Enabled json.RawMessage `json:"enabled"`
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		return settings{}, errInvalid
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return settings{}, errInvalid
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return settings{}, errInvalid
	}
	s := base
	if f.Enabled != nil && (bytes.Equal(f.Enabled, []byte("null")) || json.Unmarshal(f.Enabled, &s.Enabled) != nil) {
		return settings{}, errInvalid
	}
	return s, nil
}

// AdvertiseFunc has Advertise's shape; a test passes one that sends nothing.
type AdvertiseFunc = func(ctx context.Context, info Info, log *slog.Logger, ready func()) error

// Capability is network.discovery: {enabled}, on at first boot, kept at its path so it holds before the server's
// config arrives and without a link; its state says whether the device is being advertised.
type Capability struct {
	info  Info
	path  string
	log   *slog.Logger
	run   AdvertiseFunc
	retry time.Duration

	parent context.Context
	mu     sync.Mutex
	cur    status
	cancel context.CancelFunc
	done   chan struct{}
	state  chan json.RawMessage
}

// NewCapability reads the kept settings and, if enabled, starts advertising info until ctx is done or Stop.
func NewCapability(ctx context.Context, info Info, path string, log *slog.Logger) *Capability {
	return newCapability(ctx, info, path, log, Advertise, retryEvery)
}

// NewCapabilityWith is NewCapability advertising through run.
func NewCapabilityWith(ctx context.Context, info Info, path string, log *slog.Logger, run AdvertiseFunc) *Capability {
	return newCapability(ctx, info, path, log, run, retryEvery)
}

func newCapability(ctx context.Context, info Info, path string, log *slog.Logger, run AdvertiseFunc, retry time.Duration) *Capability {
	c := &Capability{info: info, path: path, log: log, run: run, retry: retry, parent: ctx, state: make(chan json.RawMessage, 1)}
	s := settings{Enabled: true}
	raw, err := os.ReadFile(path) //nolint:gosec // the device's own settings path
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		log.Warn("settings_invalid", "capability", CapabilityID, "reason", "read")
	default:
		if s, err = decode(raw, settings{Enabled: true}); err != nil {
			s = settings{Enabled: true}
			log.Warn("settings_invalid", "capability", CapabilityID, "reason", "format")
		}
	}
	c.mu.Lock()
	c.set(s)
	c.mu.Unlock()
	return c
}

func (*Capability) Descriptor() capability.Descriptor {
	return capability.Descriptor{ID: CapabilityID, Version: 1, ConfigSchema: configSchema, StateSchema: stateSchema}
}

func (c *Capability) Apply(_ context.Context, _ int64, cfg json.RawMessage) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	s, err := decode(cfg, settings{Enabled: c.cur.Enabled})
	if err != nil {
		return &capability.ApplyError{Code: "invalid"}
	}
	c.set(s)
	if err := c.save(s); err != nil {
		// the switch is live; only its keeping failed
		c.log.Warn("settings_failed", "capability", CapabilityID, "reason", "write")
	}
	return nil
}

func (*Capability) Command(context.Context, string, json.RawMessage) (json.RawMessage, error) {
	return nil, capability.ErrUnsupported
}

func (c *Capability) State() <-chan json.RawMessage { return c.state }

// Stop ends the advertisement, saying goodbye, and waits for it.
func (c *Capability) Stop() {
	c.mu.Lock()
	c.stopLocked()
	c.mu.Unlock()
}

// set starts or stops the advertiser to match s; callers hold mu
func (c *Capability) set(s settings) {
	switch {
	case s.Enabled && c.cancel == nil:
		c.startLocked()
	case !s.Enabled && c.cancel != nil:
		c.stopLocked()
	}
	c.cur.Enabled = s.Enabled
	c.offer()
}

func (c *Capability) startLocked() {
	ctx, cancel := context.WithCancel(c.parent)
	c.cancel, c.done = cancel, make(chan struct{})
	go func(done chan struct{}) {
		defer close(done)
		for {
			err := c.run(ctx, c.info, c.log, func() { c.advertising(true) })
			c.advertising(false)
			if ctx.Err() != nil {
				return
			}
			code := "listen"
			if errors.Is(err, errNoInterface) {
				code = "no_interface"
			}
			c.log.Warn("discovery_failed", "code", code)
			select {
			case <-ctx.Done():
				return
			case <-time.After(c.retry):
			}
		}
	}(c.done)
}

func (c *Capability) stopLocked() {
	if c.cancel == nil {
		return
	}
	c.cancel()
	done := c.done
	c.cancel, c.done = nil, nil
	// advertising(false) from the goroutine takes mu: wait without it. an Apply that comes in meanwhile sees the
	// advertiser already gone and may start the next one
	c.mu.Unlock()
	<-done
	c.mu.Lock()
}

func (c *Capability) advertising(on bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cur.Advertising == on {
		return
	}
	c.cur.Advertising = on
	c.offer()
}

// offer replaces an unread state with the latest; callers hold mu so states go out in order
func (c *Capability) offer() {
	b := mustJSON(c.cur)
	for {
		select {
		case c.state <- b:
			return
		default:
		}
		select {
		case <-c.state:
		default:
		}
	}
}

func (c *Capability) save(s settings) error {
	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		return err
	}
	return atomicfile.Write(c.path, mustJSON(s))
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// SettingsPath is where the capability keeps its switch under a device's data dir.
func SettingsPath(dataDir string) string { return filepath.Join(dataDir, "state", "discovery") }
