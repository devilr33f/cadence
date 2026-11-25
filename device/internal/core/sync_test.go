package core_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"

	"git.shino.boo/devilreef/cadence/device/internal/audio"
	"git.shino.boo/devilreef/cadence/device/internal/board"
	"git.shino.boo/devilreef/cadence/device/internal/capability"
	"git.shino.boo/devilreef/cadence/device/internal/core"
	"git.shino.boo/devilreef/cadence/device/internal/link"
	"git.shino.boo/devilreef/cadence/device/internal/syncstore"
	"git.shino.boo/devilreef/cadence/device/internal/telemetry/logs"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/pkg/wire"
)

const token = "cdd_test"

type applied struct {
	gen int64
	cfg string
}

type fakeProvider struct {
	id    string
	err   error
	state chan json.RawMessage
	mu    sync.Mutex
	calls []applied
}

func newProvider(id string) *fakeProvider {
	return &fakeProvider{id: id, state: make(chan json.RawMessage, 1)}
}

func (p *fakeProvider) Descriptor() capability.Descriptor {
	return capability.Descriptor{ID: p.id, Version: 1, ConfigSchema: `{"type":"object"}`, StateSchema: `{"type":"object"}`}
}

func (p *fakeProvider) Apply(_ context.Context, gen int64, cfg json.RawMessage) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, applied{gen, string(cfg)})
	return p.err
}

func (*fakeProvider) Command(context.Context, string, json.RawMessage) (json.RawMessage, error) {
	return nil, capability.ErrUnsupported
}

func (p *fakeProvider) State() <-chan json.RawMessage { return p.state }

// publish keeps only the latest state, like a real provider
func (p *fakeProvider) publish(s string) {
	select {
	case <-p.state:
	default:
	}
	p.state <- json.RawMessage(s)
}

func (p *fakeProvider) applied() []applied {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]applied(nil), p.calls...)
}

type fakeBoard struct {
	id   string
	caps []capability.Provider
	aio  *audio.IO
}

func (b fakeBoard) ID() string                          { return b.id }
func (fakeBoard) Fingerprint(context.Context) error     { return nil }
func (b fakeBoard) Capabilities() []capability.Provider { return b.caps }

func (b fakeBoard) AudioIO() (audio.IO, bool) {
	if b.aio == nil {
		return audio.IO{}, false
	}
	return *b.aio, true
}

// linkServer is the server side of the device link: it checks the bearer and hands over each accepted socket.
type linkServer struct {
	url   string
	dials atomic.Int32
	conns chan *websocket.Conn
}

func newLinkServer(t *testing.T) *linkServer {
	t.Helper()
	ls := &linkServer{conns: make(chan *websocket.Conn, 4)}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ls.dials.Add(1)
		if r.URL.Path != "/v2/device" || r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if c, err := websocket.Accept(w, r, nil); err == nil {
			// the server's own read limit (gateway): the link's, not the library's 32 KiB default
			c.SetReadLimit(wire.MaxFrameBytes)
			ls.conns <- c
		}
	}))
	t.Cleanup(srv.Close)
	ls.url = srv.URL
	return ls
}

type peer struct {
	t *testing.T
	c *websocket.Conn
}

func (ls *linkServer) accept(t *testing.T) *peer {
	t.Helper()
	select {
	case c := <-ls.conns:
		t.Cleanup(func() { _ = c.CloseNow() })
		return &peer{t: t, c: c}
	case <-time.After(5 * time.Second):
		t.Fatal("device did not connect")
		return nil
	}
}

func (p *peer) send(env *cadencev2.Envelope) {
	p.t.Helper()
	b, err := wire.Encode(env)
	if err != nil {
		p.t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := p.c.Write(ctx, websocket.MessageBinary, b); err != nil {
		p.t.Fatal(err)
	}
}

func (p *peer) recv() *cadencev2.Envelope {
	p.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, b, err := p.c.Read(ctx)
	if err != nil {
		p.t.Fatal(err)
	}
	env, err := wire.Decode(b)
	if err != nil {
		p.t.Fatal(err)
	}
	return env
}

// handshake answers the device's hello and returns it.
func (p *peer) handshake(fullReport bool) *cadencev2.Hello {
	p.t.Helper()
	h := p.recv().GetHello()
	if h == nil {
		p.t.Fatal("first frame is not a hello")
	}
	p.send(&cadencev2.Envelope{Body: &cadencev2.Envelope_Welcome{Welcome: &cadencev2.Welcome{ServerVersion: "test", SendFullReport: fullReport}}})
	return h
}

func (p *peer) configSet(capability string, gen int64, cfg string) *cadencev2.ConfigApplied {
	p.t.Helper()
	p.send(&cadencev2.Envelope{Body: &cadencev2.Envelope_ConfigSet{ConfigSet: &cadencev2.ConfigSet{Capability: capability, Generation: gen, Json: cfg}}})
	a := p.recv().GetConfigApplied()
	if a == nil {
		p.t.Fatal("no config_applied")
	}
	return a
}

func openStore(t *testing.T) *syncstore.Store {
	t.Helper()
	s, err := syncstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// startSync links a Sync for b to a fresh server until the test ends.
func startSync(t *testing.T, b board.Board, store *syncstore.Store, opts ...func(*core.Sync)) (*linkServer, *syncBuf) {
	t.Helper()
	ls, buf := newLinkServer(t), &syncBuf{}
	log := newLog(buf)
	s, err := core.NewSync(b, store, "v-test", log)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range opts {
		o(s)
	}
	ctx, cancel := context.WithCancel(context.Background())
	wait := s.StartAudio(ctx)
	done := make(chan error, 1)
	go func() { done <- link.Run(ctx, link.Dialer{URL: ls.url, Token: token}, s, log) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("link.Run = %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("link.Run did not return")
		}
		wait()
	})
	return ls, buf
}

func TestHelloThenReportWhenAsked(t *testing.T) {
	b := fakeBoard{id: "fake", caps: []capability.Provider{newProvider("b.two"), newProvider("a.one")}}
	ls, _ := startSync(t, b, openStore(t))
	p := ls.accept(t)
	want := capability.Report(b.caps)
	h := p.handshake(true)
	if h.GetProtocol() != 3 || h.GetDaemonVersion() != "v-test" || h.GetBoard() != "fake" || !bytes.Equal(h.GetCapabilitiesHash(), want.GetHash()) {
		t.Fatalf("hello = %v", h)
	}
	if r := p.recv().GetCapabilityReport(); !proto.Equal(r, want) {
		t.Fatalf("report = %v, want %v", r, want)
	}
}

// bigProvider's config schema is about size bytes long.
type bigProvider struct {
	*fakeProvider
	size int
}

func (p bigProvider) Descriptor() capability.Descriptor {
	d := p.fakeProvider.Descriptor()
	d.ConfigSchema = `{"description":"` + strings.Repeat("x", p.size) + `"}`
	return d
}

func TestChunkedReportSentWhenAsked(t *testing.T) {
	var caps []capability.Provider
	for i := range 40 {
		caps = append(caps, bigProvider{newProvider(fmt.Sprintf("cap.%02d", i)), wire.MaxFrameBytes / 16})
	}
	want, err := capability.Reports(caps)
	if err != nil || len(want) < 3 {
		t.Fatalf("%d parts, err = %v", len(want), err)
	}
	ls, _ := startSync(t, fakeBoard{id: "fake", caps: caps}, openStore(t))
	p := ls.accept(t)
	if h := p.handshake(true); !bytes.Equal(h.GetCapabilitiesHash(), capability.Report(caps).GetHash()) {
		t.Fatalf("hello hash = %x", h.GetCapabilitiesHash())
	}
	for i, w := range want {
		if r := p.recv().GetCapabilityReport(); !proto.Equal(r, w) {
			t.Fatalf("frame %d: part %d/%d with %d descriptors", i, r.GetPart(), r.GetParts(), len(r.GetDescriptors()))
		}
	}
}

func TestNoReportWhenNotAsked(t *testing.T) {
	pr := newProvider("audio.output")
	pr.publish(`{"level":1}`)
	ls, _ := startSync(t, fakeBoard{id: "fake", caps: []capability.Provider{pr}}, openStore(t))
	p := ls.accept(t)
	p.handshake(false)
	if env := p.recv(); env.GetStateDelta() == nil {
		t.Fatalf("first frame after welcome = %v, want the state", env)
	}
}

func TestConfigSetAppliesAndStores(t *testing.T) {
	pr := newProvider("audio.output")
	store := openStore(t)
	ls, _ := startSync(t, fakeBoard{id: "fake", caps: []capability.Provider{pr}}, store)
	p := ls.accept(t)
	p.handshake(false)
	a := p.configSet("audio.output", 4, `{"level":30}`)
	if a.GetCapability() != "audio.output" || a.GetGeneration() != 4 || a.GetErrorCode() != "" {
		t.Fatalf("applied = %v", a)
	}
	if got := pr.applied(); len(got) != 1 || got[0] != (applied{4, `{"level":30}`}) {
		t.Fatalf("apply calls = %v", got)
	}
	gen, cfg, ok, err := store.Load("audio.output")
	if err != nil || !ok || gen != 4 || string(cfg) != `{"level":30}` {
		t.Fatalf("stored gen = %d, cfg = %s, ok = %v, err = %v", gen, cfg, ok, err)
	}
}

func TestStaleConfigSetAcknowledged(t *testing.T) {
	pr := newProvider("audio.output")
	store := openStore(t)
	if err := store.Save("audio.output", 5, json.RawMessage(`{"level":10,"mute":false}`)); err != nil {
		t.Fatal(err)
	}
	ls, _ := startSync(t, fakeBoard{id: "fake", caps: []capability.Provider{pr}}, store)
	p := ls.accept(t)
	p.handshake(false)
	for _, c := range []struct {
		gen int64
		cfg string
	}{{5, `{"level":10,"mute":false}`}, {3, `{"mute": false, "level": 10}`}} {
		a := p.configSet("audio.output", c.gen, c.cfg)
		if a.GetGeneration() != c.gen || a.GetErrorCode() != "" {
			t.Fatalf("gen %d: applied = %v", c.gen, a)
		}
	}
	if got := pr.applied(); len(got) != 0 {
		t.Fatalf("stale config applied: %v", got)
	}
	if gen, cfg, _, _ := store.Load("audio.output"); gen != 5 || string(cfg) != `{"level":10,"mute":false}` {
		t.Fatalf("stored gen = %d, cfg = %s", gen, cfg)
	}
}

// the server starts a capability's generations over (it dropped out of a
// report, the device re-enrolled, the db was restored): an older generation
// with another config is applied, not acknowledged as already in force
func TestConfigAppliedAfterServerReset(t *testing.T) {
	pr := newProvider("audio.output")
	store := openStore(t)
	if err := store.Save("audio.output", 7, json.RawMessage(`{"level":10}`)); err != nil {
		t.Fatal(err)
	}
	ls, _ := startSync(t, fakeBoard{id: "fake", caps: []capability.Provider{pr}}, store)
	p := ls.accept(t)
	p.handshake(false)
	for _, c := range []struct {
		gen int64
		cfg string
	}{{1, `{"level":99}`}, {1, `{"level":99}`}, {2, `{"level":50}`}} {
		if a := p.configSet("audio.output", c.gen, c.cfg); a.GetGeneration() != c.gen || a.GetErrorCode() != "" {
			t.Fatalf("gen %d: applied = %v", c.gen, a)
		}
	}
	if got := pr.applied(); len(got) != 2 || got[0] != (applied{1, `{"level":99}`}) || got[1] != (applied{2, `{"level":50}`}) {
		t.Fatalf("apply calls = %v", got)
	}
	if gen, cfg, _, _ := store.Load("audio.output"); gen != 2 || string(cfg) != `{"level":50}` {
		t.Fatalf("stored gen = %d, cfg = %s", gen, cfg)
	}
}

func TestApplyErrorCode(t *testing.T) {
	invalid, broken := newProvider("a.invalid"), newProvider("b.broken")
	invalid.err = &capability.ApplyError{Code: "invalid"}
	broken.err = errors.New("driver said s3cr3t-cfg is wrong")
	store := openStore(t)
	ls, buf := startSync(t, fakeBoard{id: "fake", caps: []capability.Provider{invalid, broken}}, store)
	p := ls.accept(t)
	p.handshake(false)
	for _, c := range []struct{ capability, code string }{
		{"a.invalid", "invalid"},
		{"b.broken", "apply_failed"},
		{"c.unknown", "unknown_capability"},
	} {
		a := p.configSet(c.capability, 1, `{"key":"s3cr3t-cfg"}`)
		if a.GetCapability() != c.capability || a.GetGeneration() != 1 || a.GetErrorCode() != c.code {
			t.Fatalf("%s: applied = %v", c.capability, a)
		}
		if _, _, ok, _ := store.Load(c.capability); ok {
			t.Fatalf("%s: failed config stored", c.capability)
		}
	}
	if out := buf.String(); strings.Contains(out, "s3cr3t") || strings.Contains(out, "driver said") {
		t.Fatalf("log carries config or upstream text: %s", out)
	}
}

func TestStateForwardedAfterConnect(t *testing.T) {
	pr := newProvider("audio.output")
	pr.publish(`{"level":50}`)
	ls, _ := startSync(t, fakeBoard{id: "fake", caps: []capability.Provider{pr}}, openStore(t))
	state := func(p *peer, want string) {
		t.Helper()
		d := p.recv().GetStateDelta()
		if d.GetCapability() != "audio.output" || d.GetJson() != want {
			t.Fatalf("state = %v, want %s", d, want)
		}
	}
	p := ls.accept(t)
	p.handshake(false)
	state(p, `{"level":50}`)
	pr.publish(`{"level":60}`)
	state(p, `{"level":60}`)

	// a new link gets the latest state again, though the provider has nothing new
	_ = p.c.Close(websocket.StatusGoingAway, "restart")
	p = ls.accept(t)
	p.handshake(false)
	state(p, `{"level":60}`)
}

func TestStoredConfigReappliedAtBoot(t *testing.T) {
	stored, fresh := newProvider("audio.output"), newProvider("led.ring")
	store := openStore(t)
	if err := store.Save("audio.output", 7, json.RawMessage(`{"level":30}`)); err != nil {
		t.Fatal(err)
	}
	buf := &syncBuf{}
	s, err := core.NewSync(fakeBoard{id: "fake", caps: []capability.Provider{stored, fresh}}, store, "v-test", newLog(buf))
	if err != nil {
		t.Fatal(err)
	}
	s.Restore(context.Background())
	if got := stored.applied(); len(got) != 1 || got[0] != (applied{7, `{"level":30}`}) {
		t.Fatalf("apply calls = %v", got)
	}
	if got := fresh.applied(); len(got) != 0 {
		t.Fatalf("capability without a stored config applied: %v", got)
	}
}

// review focus 4: a config the server sends again on a new link is acknowledged, not applied twice
func TestPendingConfigPushedOnNewLink(t *testing.T) {
	pr := newProvider("audio.output")
	ls, _ := startSync(t, fakeBoard{id: "fake", caps: []capability.Provider{pr}}, openStore(t))
	p := ls.accept(t)
	p.handshake(false)
	if a := p.configSet("audio.output", 4, `{"level":30}`); a.GetGeneration() != 4 || a.GetErrorCode() != "" {
		t.Fatalf("link 1: applied = %v", a)
	}
	_ = p.c.Close(websocket.StatusGoingAway, "restart")
	p = ls.accept(t)
	p.handshake(false)
	if a := p.configSet("audio.output", 4, `{"level":30}`); a.GetGeneration() != 4 || a.GetErrorCode() != "" {
		t.Fatalf("link 2: applied = %v", a)
	}
	if got := pr.applied(); len(got) != 1 {
		t.Fatalf("apply calls = %v", got)
	}
}

// an ack would mark the generation applied on the server, which then never sends it again,
// while a restart brings back the older stored config
func TestConfigRetriedWhenStoreFails(t *testing.T) {
	pr := newProvider("audio.output")
	dir := filepath.Join(t.TempDir(), "config")
	store, err := syncstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ls, buf := startSync(t, fakeBoard{id: "fake", caps: []capability.Provider{pr}}, store)
	p := ls.accept(t)
	p.handshake(false)
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	p.send(&cadencev2.Envelope{Body: &cadencev2.Envelope_ConfigSet{ConfigSet: &cadencev2.ConfigSet{Capability: "audio.output", Generation: 4, Json: `{"level":30}`}}})
	// frames go out in order: the probe's answer comes first only if gen 4 got none
	if a := p.configSet("c.unknown", 1, `{}`); a.GetCapability() != "c.unknown" {
		t.Fatalf("gen 4 acknowledged though it was not stored: %v", a)
	}
	if out := buf.String(); !strings.Contains(out, `"msg":"config_apply_failed","capability":"audio.output","generation":4,"code":"store_failed"`) {
		t.Fatalf("log: %s", out)
	}

	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	_ = p.c.Close(websocket.StatusGoingAway, "restart")
	p = ls.accept(t)
	p.handshake(false)
	if a := p.configSet("audio.output", 4, `{"level":30}`); a.GetGeneration() != 4 || a.GetErrorCode() != "" {
		t.Fatalf("retry: applied = %v", a)
	}
	if got := pr.applied(); len(got) != 2 {
		t.Fatalf("apply calls = %v", got)
	}
	if gen, _, ok, err := store.Load("audio.output"); err != nil || !ok || gen != 4 {
		t.Fatalf("stored gen = %d, ok = %v, err = %v", gen, ok, err)
	}
}

// secretProvider declares one secret and takes configs with what came of it
type secretProvider struct {
	*fakeProvider
	mu      sync.Mutex
	secrets []map[string]string
}

func (p *secretProvider) Descriptor() capability.Descriptor {
	d := p.fakeProvider.Descriptor()
	d.Secrets = []capability.SecretSpec{{Name: "apiKey", Description: "key"}}
	return d
}

func (p *secretProvider) ApplySecrets(ctx context.Context, gen int64, cfg json.RawMessage, secrets map[string]string) error {
	p.mu.Lock()
	p.secrets = append(p.secrets, secrets)
	p.mu.Unlock()
	return p.Apply(ctx, gen, cfg)
}

func (p *secretProvider) got() []map[string]string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]map[string]string(nil), p.secrets...)
}

func configSetWith(p *peer, capability string, gen int64, cfg string, secrets map[string]string) *cadencev2.ConfigApplied {
	p.t.Helper()
	p.send(&cadencev2.Envelope{Body: &cadencev2.Envelope_ConfigSet{ConfigSet: &cadencev2.ConfigSet{Capability: capability, Generation: gen, Json: cfg, Secrets: secrets}}})
	a := p.recv().GetConfigApplied()
	if a == nil {
		p.t.Fatal("no config_applied")
	}
	return a
}

// a secret reaches only a provider that declares it, is kept beside the
// config, comes back at boot, and never reaches the log; the same config with
// another secret is applied again
func TestSecretsReachTheirProviderAndSurviveARestart(t *testing.T) {
	const key = "KEY-s3cr3t-value"
	sp, plain := &secretProvider{fakeProvider: newProvider("media.soloist")}, newProvider("audio.output")
	store := openStore(t)
	ls, buf := startSync(t, fakeBoard{id: "fake", caps: []capability.Provider{sp, plain}}, store)
	p := ls.accept(t)
	p.handshake(false)
	all := map[string]string{"apiKey": key, "other": "undeclared-x"}
	if a := configSetWith(p, "media.soloist", 2, `{"enabled":true}`, all); a.GetErrorCode() != "" {
		t.Fatalf("applied = %v", a)
	}
	if a := configSetWith(p, "audio.output", 2, `{}`, all); a.GetErrorCode() != "" {
		t.Fatalf("applied = %v", a)
	}
	if got := sp.got(); len(got) != 1 || len(got[0]) != 1 || got[0]["apiKey"] != key {
		t.Fatalf("secrets handed over = %v", got)
	}
	if e, ok, err := store.LoadEntry("media.soloist"); err != nil || !ok || len(e.Secrets) != 1 || e.Secrets["apiKey"] != key {
		t.Fatalf("stored = %+v, %v, %v", e, ok, err)
	}
	if e, _, _ := store.LoadEntry("audio.output"); e.Secrets != nil {
		t.Fatalf("a provider without secrets got %d stored", len(e.Secrets))
	}
	// the same generation and config, now with the key changed, is no stale repeat
	if a := configSetWith(p, "media.soloist", 2, `{"enabled":true}`, map[string]string{"apiKey": key + "2"}); a.GetErrorCode() != "" {
		t.Fatalf("applied = %v", a)
	}
	if got := sp.got(); len(got) != 2 || got[1]["apiKey"] != key+"2" {
		t.Fatalf("secrets handed over = %v", got)
	}

	restored := &secretProvider{fakeProvider: newProvider("media.soloist")}
	s, err := core.NewSync(fakeBoard{id: "fake", caps: []capability.Provider{restored}}, store, "v-test", newLog(buf))
	if err != nil {
		t.Fatal(err)
	}
	s.Restore(context.Background())
	if got := restored.got(); len(got) != 1 || got[0]["apiKey"] != key+"2" {
		t.Fatalf("restored secrets = %v", got)
	}
	if out := buf.String(); strings.Contains(out, key) || strings.Contains(out, "undeclared-x") {
		t.Fatalf("log carries a secret: %s", out)
	}
}

// secretSink is what the log scrubber is told
type secretSink struct {
	mu sync.Mutex
	m  map[string][]string
}

func (s *secretSink) Set(name string, values ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[name] = slices.Clone(values)
}

func (s *secretSink) Remove(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, name)
}

func (s *secretSink) all() map[string][]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return maps.Clone(s.m)
}

// scrubbedProvider counts the secrets it was handed before the sink knew them
type scrubbedProvider struct {
	*secretProvider
	sink    *secretSink
	unknown atomic.Int32
	fail    atomic.Bool
}

func (p *scrubbedProvider) ApplySecrets(ctx context.Context, gen int64, cfg json.RawMessage, secrets map[string]string) error {
	known := p.sink.all()
	for name, v := range secrets {
		if !slices.Contains(known[p.id+"."+name], v) {
			p.unknown.Add(1)
		}
	}
	if err := p.secretProvider.ApplySecrets(ctx, gen, cfg, secrets); err != nil {
		return err
	}
	if p.fail.Load() {
		return &capability.ApplyError{Code: "invalid"}
	}
	return nil
}

// every secret a provider is handed is known to the log scrubber first, as
// <capability>.<name>; a value no longer in force, refused, rotated away or
// deleted, stays redacted beside those in force, the four latest per name
func TestSecretsRegisteredForScrubbing(t *testing.T) {
	sink := &secretSink{m: map[string][]string{}}
	sp := &scrubbedProvider{secretProvider: &secretProvider{fakeProvider: newProvider("media.soloist")}, sink: sink}
	ls, _ := startSync(t, fakeBoard{id: "fake", caps: []capability.Provider{sp}}, openStore(t), core.WithSecrets(sink))
	p := ls.accept(t)
	p.handshake(false)
	step := func(gen int64, secrets map[string]string, code string, want ...string) {
		t.Helper()
		if a := configSetWith(p, "media.soloist", gen, `{}`, secrets); a.GetErrorCode() != code {
			t.Fatalf("gen %d applied = %v", gen, a)
		}
		got := sink.all()
		if len(want) == 0 && len(got) != 0 || len(want) > 0 && (len(got) != 1 || !slices.Equal(got["media.soloist.apiKey"], want)) {
			t.Fatalf("gen %d: scrubber knows %v, want media.soloist.apiKey = %v", gen, got, want)
		}
	}
	key := func(v string) map[string]string { return map[string]string{"apiKey": v} }
	step(1, map[string]string{"apiKey": "first-key-value", "other": "undeclared-x"}, "", "first-key-value")
	step(2, key("second-key-value"), "", "second-key-value", "first-key-value")
	sp.fail.Store(true)
	step(3, key("third-key-value"), "invalid", "second-key-value", "first-key-value", "third-key-value")
	// the key in force, then the four latest no longer in force
	for i := range 5 {
		step(int64(4+i), key(fmt.Sprintf("refused-key-%d", i)), "invalid",
			slices.Concat([]string{"second-key-value"}, []string{"first-key-value", "third-key-value", "refused-key-0", "refused-key-1", "refused-key-2", "refused-key-3", "refused-key-4"}[max(0, i+3-4):i+3])...)
	}
	sp.fail.Store(false)
	step(9, key("fourth-key-value"), "", "fourth-key-value", "refused-key-2", "refused-key-3", "refused-key-4", "second-key-value")
	step(10, nil, "", "refused-key-3", "refused-key-4", "second-key-value", "fourth-key-value")
	// a value back in force takes no place among those kept
	step(11, key("second-key-value"), "", "second-key-value", "refused-key-3", "refused-key-4", "fourth-key-value")
	if n := sp.unknown.Load(); n != 0 {
		t.Fatalf("%d secrets reached the provider before the scrubber", n)
	}
}

// a key rotated away or deleted stays redacted in the log pipeline's
// scrubber: the child that took it may log it until it ends
func TestRotatedAndDeletedSecretsStayRedacted(t *testing.T) {
	scrub := &logs.Scrubber{}
	sp := &secretProvider{fakeProvider: newProvider("media.soloist")}
	ls, _ := startSync(t, fakeBoard{id: "fake", caps: []capability.Provider{sp}}, openStore(t), core.WithSecrets(scrub))
	p := ls.accept(t)
	p.handshake(false)
	const a, b = "rotated-away-key-A", "rotated-in-key-B"
	apply := func(gen int64, secrets map[string]string) {
		t.Helper()
		if ack := configSetWith(p, "media.soloist", gen, `{}`, secrets); ack.GetErrorCode() != "" {
			t.Fatalf("gen %d applied = %v", gen, ack)
		}
	}
	redacted := func(when string, values ...string) {
		t.Helper()
		for _, v := range values {
			if out := scrub.Scrub("soloist: --api-key " + v + " exiting"); strings.Contains(out, v) || !strings.Contains(out, "media.soloist.apiKey") {
				t.Fatalf("%s: a line with %q ships as %q", when, v, out)
			}
		}
	}
	apply(1, map[string]string{"apiKey": a})
	apply(2, map[string]string{"apiKey": b})
	redacted("rotated", a, b)
	apply(3, nil)
	redacted("deleted", a, b)
}

func TestRestoredSecretsRegisteredForScrubbing(t *testing.T) {
	store := openStore(t)
	if err := store.SaveEntry("media.soloist", syncstore.Entry{Generation: 1, Config: json.RawMessage(`{}`), Secrets: map[string]string{"apiKey": "stored-key-value"}}); err != nil {
		t.Fatal(err)
	}
	sink := &secretSink{m: map[string][]string{}}
	sp := &scrubbedProvider{secretProvider: &secretProvider{fakeProvider: newProvider("media.soloist")}, sink: sink}
	s, err := core.NewSync(fakeBoard{id: "fake", caps: []capability.Provider{sp}}, store, "v-test", newLog(&syncBuf{}), core.WithSecrets(sink))
	if err != nil {
		t.Fatal(err)
	}
	s.Restore(context.Background())
	if got := sink.all(); !slices.Equal(got["media.soloist.apiKey"], []string{"stored-key-value"}) || len(got) != 1 || sp.unknown.Load() != 0 {
		t.Fatalf("scrubber knows %d secrets, %d reached the provider first", len(got), sp.unknown.Load())
	}
}
