// Package wakeword is the custom wake word on the device (wake.custom): the model the owner trained on the server,
// fetched with the device token and cached under the data dir, run by pkg/kws on the microphone audiod streams while
// it listens (WakeListen, MicPcm). A detection begins a session as the wake engine's does, its capture starting with
// the speech right after the word. Audio is only ever in memory, a frame at a time (rule 8); logs carry codes and
// counts.
package wakeword

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
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
	"git.shino.boo/devilreef/cadence/device/internal/input"
	"git.shino.boo/devilreef/cadence/device/internal/telemetry/otlp"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
	"git.shino.boo/devilreef/cadence/pkg/kws"
)

const (
	// CapabilityID is the custom wake word: off unless a config enables it with a model
	CapabilityID = "wake.custom"

	configSchema = `{"type":"object","properties":` +
		`{"enabled":{"type":"boolean"},` +
		`"modelId":{"type":"string","pattern":"^[0-9a-f-]{36}$"},` +
		`"sha256":{"type":"string","pattern":"^[0-9a-f]{64}$"},` +
		`"sensitivity":{"type":"number","minimum":0,"maximum":1}},` +
		`"required":["enabled"],"additionalProperties":false}`

	stateSchema = `{"type":"object","properties":` +
		`{"state":{"enum":["off","loading","listening","failed"]},` +
		`"modelId":{"type":"string"},` +
		`"word":{"type":"string"},` +
		`"detections":{"type":"integer","minimum":0},` +
		`"code":{"type":"string","pattern":"^[a-z0-9_]{1,64}$"}},` +
		`"required":["state","detections"],"additionalProperties":false}`

	// the frames the detector may lag behind audiod: one second
	inboxFrames = 50
	fetchWait   = 30 * time.Second
	modelsDir   = "wakeword"
)

var modelID = regexp.MustCompile(`^[0-9a-f-]{36}$`)

// Getter fetches a server path with the device token (the telemetry client's Get).
type Getter interface {
	Get(ctx context.Context, path string) (io.ReadCloser, error)
}

type Options struct {
	// models are cached in <DataDir>/wakeword
	DataDir string
	// nil: models come from the cache alone
	Get Getter
	// a detection: input.Input's CustomWake
	Wake func(score float32, atMs int64)
	Log  *slog.Logger
}

type config struct {
	Enabled     bool     `json:"enabled"`
	ModelID     string   `json:"modelId"`
	SHA256      string   `json:"sha256"`
	Sensitivity *float32 `json:"sensitivity"`
}

type state struct {
	State      string `json:"state"`
	ModelID    string `json:"modelId,omitempty"`
	Word       string `json:"word,omitempty"`
	Detections int    `json:"detections"`
	Code       string `json:"code,omitempty"`
}

// Capability is wake.custom, and the input's Listener.
type Capability struct {
	o      Options
	stateC chan json.RawMessage

	mu   sync.Mutex
	cur  state
	send func(*audiodv1.Envelope) error
	run  *runner
	// the frames dropped since the last log
	dropped int
}

// runner is one model's detector on its own goroutine.
type runner struct {
	frames chan *audiodv1.MicPcm
	stop   chan struct{}
	done   chan struct{}
}

func New(o Options) *Capability {
	if o.Log == nil {
		o.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	c := &Capability{o: o, stateC: make(chan json.RawMessage, 1), cur: state{State: "off"}}
	c.publishLocked()
	return c
}

// Attach makes a custom wake word for in: it hears in's audiod connection and wakes through it.
func Attach(in *input.Input, o Options) *Capability {
	o.Wake = in.CustomWake
	c := New(o)
	in.UseListener(c)
	return c
}

var _ input.Listener = (*Capability)(nil)

func (*Capability) Descriptor() capability.Descriptor {
	return capability.Descriptor{ID: CapabilityID, Version: 1, ConfigSchema: configSchema, StateSchema: stateSchema}
}

func (c *Capability) State() <-chan json.RawMessage { return c.stateC }

func (*Capability) Command(context.Context, string, json.RawMessage) (json.RawMessage, error) {
	return nil, capability.ErrUnsupported
}

// Apply loads the model the config names (from the cache, else the server) and listens with it, or stops listening.
// Codes: invalid, model_unavailable (no fetch or a failed one), model_mismatch (bytes of another sha256),
// model_invalid.
func (c *Capability) Apply(ctx context.Context, _ int64, raw json.RawMessage) error {
	cfg, err := parse(raw)
	if err != nil {
		return err
	}
	if !cfg.Enabled {
		c.listen(nil, state{State: "off"})
		c.o.Log.Info("wakeword_off")
		return nil
	}
	c.setState(state{State: "loading", ModelID: cfg.ModelID, Detections: c.detections()})
	m, code := c.model(ctx, cfg)
	if code != "" {
		c.listen(nil, state{State: "failed", ModelID: cfg.ModelID, Code: code})
		c.o.Log.Warn("wakeword_failed", "code", code)
		return &capability.ApplyError{Code: code}
	}
	sensitivity := float32(kws.DefaultSensitivity)
	if cfg.Sensitivity != nil {
		sensitivity = *cfg.Sensitivity
	}
	det, err := kws.NewDetector(m, sensitivity)
	if err != nil {
		c.listen(nil, state{State: "failed", ModelID: cfg.ModelID, Code: "model_invalid"})
		return &capability.ApplyError{Code: "model_invalid"}
	}
	c.listen(det, state{State: "listening", ModelID: cfg.ModelID, Word: m.Word})
	c.o.Log.Info("wakeword_listening", "threshold", kws.Threshold(m, sensitivity))
	return nil
}

func parse(raw json.RawMessage) (config, error) {
	var cfg config
	dec := json.NewDecoder(bytes.NewReader(bytes.TrimSpace(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return cfg, &capability.ApplyError{Code: "invalid"}
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return cfg, &capability.ApplyError{Code: "invalid"}
	}
	if cfg.Sensitivity != nil && (*cfg.Sensitivity < 0 || *cfg.Sensitivity > 1) {
		return cfg, &capability.ApplyError{Code: "invalid"}
	}
	if cfg.Enabled {
		if _, err := hex.DecodeString(cfg.SHA256); err != nil || len(cfg.SHA256) != 64 ||
			cfg.SHA256 != strings.ToLower(cfg.SHA256) || !modelID.MatchString(cfg.ModelID) {
			return cfg, &capability.ApplyError{Code: "invalid"}
		}
	}
	return cfg, nil
}

// model is the config's model: the cached file when its sha256 is the config's, else fetched and cached in its place.
func (c *Capability) model(ctx context.Context, cfg config) (*kws.Model, string) {
	dir := filepath.Join(c.o.DataDir, modelsDir)
	path := filepath.Join(dir, cfg.SHA256+".json")
	b, err := os.ReadFile(path) //nolint:gosec // our own data dir and a hex name
	if err != nil || sum(b) != cfg.SHA256 {
		if c.o.Get == nil {
			return nil, "model_unavailable"
		}
		fctx, cancel := context.WithTimeout(ctx, fetchWait)
		defer cancel()
		body, err := c.o.Get.Get(fctx, "/v2/wakeword/"+cfg.ModelID)
		if err != nil {
			return nil, "model_unavailable"
		}
		b, err = io.ReadAll(io.LimitReader(body, kws.MaxModelBytes+1))
		_ = body.Close()
		if err != nil {
			return nil, "model_unavailable"
		}
		if sum(b) != cfg.SHA256 {
			return nil, "model_mismatch"
		}
		if c.o.DataDir != "" {
			if err := os.MkdirAll(dir, 0o700); err == nil {
				if err := atomicfile.Write(path, b); err != nil {
					c.o.Log.Warn("wakeword_cache_failed")
				}
				prune(dir, filepath.Base(path))
			}
		}
	}
	m, err := kws.ParseModel(b)
	if err != nil {
		return nil, "model_invalid"
	}
	return m, ""
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// prune removes every cached model but keep.
func prune(dir, keep string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.Name() != keep && strings.HasSuffix(e.Name(), ".json") {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

// listen runs det (nil: none) and reports st, telling audiod whether to listen.
func (c *Capability) listen(det *kws.Detector, st state) {
	c.mu.Lock()
	old := c.run
	c.run = nil
	c.mu.Unlock()
	if old != nil {
		close(old.stop)
		<-old.done
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	st.Detections = c.cur.Detections
	if det != nil {
		r := &runner{frames: make(chan *audiodv1.MicPcm, inboxFrames), stop: make(chan struct{}), done: make(chan struct{})}
		c.run = r
		go c.detect(det, r)
	}
	c.cur = st
	c.sendListenLocked()
	c.publishLocked()
}

func (c *Capability) detect(det *kws.Detector, r *runner) {
	defer close(r.done)
	var last uint64
	pcm := make([]int16, 0, 320)
	for {
		select {
		case <-r.stop:
			return
		case f := <-r.frames:
			if f.GetGap() || f.GetSeq() != last+1 {
				det.Reset()
			}
			last = f.GetSeq()
			raw := f.GetPcm()
			pcm = pcm[:0]
			for i := 0; i+1 < len(raw); i += 2 {
				pcm = append(pcm, int16(binary.LittleEndian.Uint16(raw[i:]))) //nolint:gosec // s16le's two's complement
			}
			var hit bool
			var score float32
			det.Push(pcm, func(s float32) { hit, score = true, s })
			if hit {
				c.hit(score, f.GetAtMs())
			}
		}
	}
}

func (c *Capability) hit(score float32, atMs int64) {
	c.mu.Lock()
	c.cur.Detections++
	n := c.cur.Detections
	c.publishLocked()
	c.mu.Unlock()
	c.o.Log.Info("wakeword_detected", "score", score, "count", n)
	if c.o.Wake != nil {
		c.o.Wake(score, atMs)
	}
}

// ListenReady tells a new audiod whether to listen.
func (c *Capability) ListenReady(send func(*audiodv1.Envelope) error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.send = send
	c.sendListenLocked()
}

func (c *Capability) ListenGone() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.send = nil
}

// MicPCM hands a frame to the detector, dropping it when the detector lags a second behind.
func (c *Capability) MicPCM(f *audiodv1.MicPcm) {
	c.mu.Lock()
	r := c.run
	c.mu.Unlock()
	if r == nil {
		return
	}
	select {
	case r.frames <- f:
	default:
		c.mu.Lock()
		c.dropped++
		n := c.dropped
		c.mu.Unlock()
		if n == 1 || n%500 == 0 {
			c.o.Log.Warn("wakeword_frames_dropped", "count", n)
		}
	}
}

func (c *Capability) sendListenLocked() {
	if c.send == nil {
		return
	}
	on := c.run != nil
	if err := c.send(&audiodv1.Envelope{Body: &audiodv1.Envelope_WakeListen{
		WakeListen: &audiodv1.WakeListen{Enabled: on}}}); err != nil {
		c.o.Log.Warn("wakeword_listen_failed")
	}
}

func (c *Capability) detections() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cur.Detections
}

func (c *Capability) setState(st state) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cur = st
	c.publishLocked()
}

func (c *Capability) publishLocked() {
	b, err := json.Marshal(c.cur)
	if err != nil {
		return
	}
	select {
	case <-c.stateC:
	default:
	}
	c.stateC <- b
}

// ServerGetter is the device's server client as a Getter, nil without one (a nil client in the interface would not
// be).
func ServerGetter(c *otlp.Client) Getter {
	if c == nil {
		return nil
	}
	return c
}
