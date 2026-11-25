package media_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"google.golang.org/protobuf/proto"

	"git.shino.boo/devilreef/cadence/device/internal/capability"
	"git.shino.boo/devilreef/cadence/device/internal/media"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
)

type logBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// conn stands in for one audiod connection
type conn struct {
	sent chan *audiodv1.Envelope
	fail atomic.Bool
}

func (c *conn) send(env *audiodv1.Envelope) error {
	if c.fail.Load() {
		return errors.New("closed")
	}
	c.sent <- env
	return nil
}

type rig struct {
	m    *media.Media
	c    *conn
	log  *logBuf
	caps map[string]capability.Provider
}

func newRig(t *testing.T) *rig {
	t.Helper()
	logs := &logBuf{}
	m := media.New(slog.New(slog.NewJSONHandler(logs, nil)))
	r := &rig{m: m, c: &conn{sent: make(chan *audiodv1.Envelope, 64)}, log: logs, caps: map[string]capability.Provider{}}
	for _, p := range m.Capabilities() {
		r.caps[p.Descriptor().ID] = p
	}
	return r
}

func (r *rig) ready() { r.m.MediaReady(r.c.send) }

// up connects audiod and takes the settings every Ready gets
func (r *rig) up(t *testing.T) {
	t.Helper()
	r.ready()
	if r.next(t).GetMediaSettings() == nil {
		t.Fatal("no settings after ready")
	}
}

func (r *rig) next(t *testing.T) *audiodv1.Envelope {
	t.Helper()
	select {
	case env := <-r.c.sent:
		return env
	case <-time.After(2 * time.Second):
		t.Fatal("nothing sent to audiod")
		return nil
	}
}

func (r *rig) none(t *testing.T) {
	t.Helper()
	select {
	case env := <-r.c.sent:
		t.Fatalf("sent %v", env)
	case <-time.After(50 * time.Millisecond):
	}
}

func (r *rig) command(id, name, args string) <-chan error {
	return r.commandCtx(context.Background(), id, name, args)
}

func (r *rig) commandCtx(ctx context.Context, id, name, args string) <-chan error {
	done := make(chan error, 1)
	go func() {
		_, err := r.caps[id].Command(ctx, name, json.RawMessage(args))
		done <- err
	}()
	return done
}

func (r *rig) answer(id string, ok bool, code string) {
	r.m.MediaEnvelope(&audiodv1.Envelope{Body: &audiodv1.Envelope_CommandResult{CommandResult: &audiodv1.CommandResult{RequestId: id, Ok: ok, Code: code}}})
}

func wait(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(3 * time.Second):
		t.Fatal("command did not return")
		return nil
	}
}

func code(err error) string {
	var ce *capability.CommandError
	if errors.As(err, &ce) {
		return ce.Code
	}
	if err == nil {
		return ""
	}
	return "error: " + err.Error()
}

var requestID = regexp.MustCompile(`^r-[0-9]+$`)

func TestDescriptors(t *testing.T) {
	r := newRig(t)
	want := map[string][]string{
		"media.player": {"next", "pause", "previous", "resume", "seek", "stop"},
		"media.url":    {"play"},
		"media.eq":     nil,
		"media.viz":    nil,
	}
	if len(r.caps) != len(want) {
		t.Fatalf("capabilities = %v", r.caps)
	}
	for id, names := range want {
		p, ok := r.caps[id]
		if !ok {
			t.Fatalf("no %s", id)
		}
		d := p.Descriptor()
		if d.Version != 1 || len(d.Commands) != len(names) {
			t.Fatalf("%s: version %d, commands %v", id, d.Version, d.Commands)
		}
		schemas := []string{d.ConfigSchema}
		if d.StateSchema != "" {
			schemas = append(schemas, d.StateSchema)
		}
		for _, n := range names {
			s, ok := d.Commands[n]
			if !ok {
				t.Fatalf("%s lacks command %s", id, n)
			}
			schemas = append(schemas, s)
		}
		for _, s := range schemas {
			if !json.Valid([]byte(s)) || !strings.Contains(s, `"additionalProperties":false`) {
				t.Fatalf("%s: schema %s", id, s)
			}
		}
	}
}

func TestURLPlaySendsUrlPlay(t *testing.T) {
	r := newRig(t)
	r.up(t)
	title, artist, album := strings.Repeat("ж", 150), strings.Repeat("€", 100), strings.Repeat("😀", 80)
	args, _ := json.Marshal(map[string]any{
		"url": "https://radio.example/stream.mp3", "mode": "overlay", "startMs": 1500,
		"metadata": map[string]string{"title": title, "artist": artist, "album": album, "artworkUrl": "HTTP://art.example/a.jpg"},
	})
	done := r.command("media.url", "play", string(args))
	env := r.next(t)
	p := env.GetUrlPlay()
	if p == nil || !requestID.MatchString(p.GetRequestId()) || p.GetUrl() != "https://radio.example/stream.mp3" ||
		p.GetMode() != audiodv1.UrlMode_URL_MODE_OVERLAY || p.GetStartMs() != 1500 || p.GetMetadata().GetArtworkUrl() != "HTTP://art.example/a.jpg" {
		t.Fatalf("sent %v", env)
	}
	for _, c := range []struct {
		got, full string
		want      int
	}{
		{p.GetMetadata().GetTitle(), title, 254},
		{p.GetMetadata().GetArtist(), artist, 255},
		{p.GetMetadata().GetAlbum(), album, 252},
	} {
		if len(c.got) != c.want || !utf8.ValidString(c.got) || !strings.HasPrefix(c.full, c.got) {
			t.Fatalf("capped to %d bytes (valid %v), want %d", len(c.got), utf8.ValidString(c.got), c.want)
		}
	}
	r.answer(p.GetRequestId(), true, "")
	if err := wait(t, done); err != nil {
		t.Fatalf("play: %v", err)
	}

	// mode defaults to session; a url of 2047 bytes is the longest one taken
	long := "http://a.example/" + strings.Repeat("x", 2047-len("http://a.example/"))
	done = r.command("media.url", "play", `{"url":"`+long+`"}`)
	p = r.next(t).GetUrlPlay()
	if p.GetMode() != audiodv1.UrlMode_URL_MODE_SESSION || p.GetUrl() != long || p.GetMetadata() != nil || p.GetStartMs() != 0 {
		t.Fatalf("sent %v", p)
	}
	r.answer(p.GetRequestId(), false, "not_found")
	if c := code(wait(t, done)); c != "not_found" {
		t.Fatalf("code = %q", c)
	}
}

func TestURLPlayRefusesInvalidArgs(t *testing.T) {
	r := newRig(t)
	r.up(t)
	tooLong := "https://a.example/" + strings.Repeat("x", 2048-len("https://a.example/"))
	for _, args := range []string{
		`{"url":"ftp://a.example/x.mp3"}`,
		`{"url":"file:///etc/passwd"}`,
		`{"url":"https://"}`,
		`{"url":"httpx://a.example/x"}`,
		`{"url":"` + tooLong + `"}`,
		`{"url":"https://a.example/x","metadata":{"artworkUrl":"file:///a.jpg"}}`,
		`{"url":"https://a.example/x","metadata":{"artworkUrl":"` + tooLong + `"}}`,
		`{"url":"https://a.example/x","metadata":{"title":"t","lyrics":"l"}}`,
		`{"url":"https://a.example/x","mode":"loud"}`,
		`{"url":"https://a.example/x","startMs":-1}`,
		`{"url":"https://a.example/x","startMs":1.5}`,
		`{"url":"https://a.example/x","volume":100}`,
		`{"url":7}`,
		`{}`,
		`[]`,
		`not json`,
		``,
	} {
		if c := code(wait(t, r.command("media.url", "play", args))); c != "invalid" {
			t.Fatalf("%s: code = %q", args, c)
		}
	}
	r.none(t)
}

func TestPlayerCommandsMapToMediaCommand(t *testing.T) {
	r := newRig(t)
	r.up(t)
	for _, c := range []struct {
		name, args string
		action     audiodv1.MediaAction
		position   uint64
		ok         bool
		code       string
		want       string
	}{
		{"pause", `{}`, audiodv1.MediaAction_MEDIA_ACTION_PAUSE, 0, true, "", ""},
		{"resume", `{}`, audiodv1.MediaAction_MEDIA_ACTION_RESUME, 0, false, "refused", "refused"},
		{"stop", `{}`, audiodv1.MediaAction_MEDIA_ACTION_STOP, 0, false, "no_session", "no_session"},
		{"seek", `{"positionMs":42000}`, audiodv1.MediaAction_MEDIA_ACTION_SEEK, 42000, false, "", "failed"},
		{"seek", `{"positionMs":0}`, audiodv1.MediaAction_MEDIA_ACTION_SEEK, 0, true, "ok", ""},
		// m8-r2: the skips go to the session, which refuses one it does not list
		{"next", `{}`, audiodv1.MediaAction_MEDIA_ACTION_NEXT, 0, true, "", ""},
		{"previous", `{}`, audiodv1.MediaAction_MEDIA_ACTION_PREVIOUS, 0, true, "", ""},
		{"next", `{}`, audiodv1.MediaAction_MEDIA_ACTION_NEXT, 0, false, "unsupported", "unsupported"},
		{"previous", `{}`, audiodv1.MediaAction_MEDIA_ACTION_PREVIOUS, 0, false, "no_session", "no_session"},
	} {
		done := r.command("media.player", c.name, c.args)
		mc := r.next(t).GetMediaCommand()
		if mc == nil || !requestID.MatchString(mc.GetRequestId()) || mc.GetAction() != c.action || mc.GetPositionMs() != c.position {
			t.Fatalf("%s: sent %v", c.name, mc)
		}
		r.answer(mc.GetRequestId(), c.ok, c.code)
		if got := code(wait(t, done)); got != c.want {
			t.Fatalf("%s: code = %q, want %q", c.name, got, c.want)
		}
	}
	for _, c := range []struct{ name, args string }{
		{"seek", `{}`}, {"seek", `{"positionMs":-5}`}, {"seek", `{"positionMs":"1"}`}, {"pause", `{"now":true}`}, {"stop", `null`},
		{"next", `{"positionMs":5}`}, {"previous", `[]`},
	} {
		if got := code(wait(t, r.command("media.player", c.name, c.args))); got != "invalid" {
			t.Fatalf("%s %s: code = %q", c.name, c.args, got)
		}
	}
	if err := wait(t, r.command("media.player", "skip", `{}`)); !errors.Is(err, capability.ErrUnsupported) {
		t.Fatalf("skip: %v", err)
	}
	r.none(t)
}

func TestCommandsUnavailableWhileAudiodDown(t *testing.T) {
	r := newRig(t)
	if c := code(wait(t, r.command("media.player", "pause", `{}`))); c != "unavailable" {
		t.Fatalf("before ready: %q", c)
	}
	r.up(t)
	r.c.fail.Store(true)
	if c := code(wait(t, r.command("media.url", "play", `{"url":"https://a.example/x"}`))); c != "unavailable" {
		t.Fatalf("failed send: %q", c)
	}
	r.c.fail.Store(false)

	// a restart answers what is pending
	done := r.command("media.player", "stop", `{}`)
	mc := r.next(t).GetMediaCommand()
	r.m.MediaGone()
	if c := code(wait(t, done)); c != "unavailable" {
		t.Fatalf("pending at gone: %q", c)
	}
	r.answer(mc.GetRequestId(), true, "")
	if c := code(wait(t, r.command("media.player", "pause", `{}`))); c != "unavailable" {
		t.Fatalf("after gone: %q", c)
	}
	r.none(t)
}

func TestCommandTimesOut(t *testing.T) {
	r := newRig(t)
	media.SetCommandTimeout(r.m, 50*time.Millisecond)
	r.up(t)
	done := r.command("media.player", "pause", `{}`)
	mc := r.next(t).GetMediaCommand()
	if c := code(wait(t, done)); c != "audiod_timeout" {
		t.Fatalf("code = %q", c)
	}
	// a late answer finds nothing to answer
	r.answer(mc.GetRequestId(), true, "")

	ctx, cancel := context.WithCancel(context.Background())
	done = r.commandCtx(ctx, "media.player", "pause", `{}`)
	r.next(t)
	cancel()
	if err := wait(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled: %v", err)
	}
}

func settings(duck audiodv1.DuckMode, eq *audiodv1.EqSettings, viz *audiodv1.VizSettings) *audiodv1.MediaSettings {
	return &audiodv1.MediaSettings{DuckMode: duck, Eq: eq, Viz: viz}
}

func (r *rig) settings(t *testing.T, want *audiodv1.MediaSettings) {
	t.Helper()
	env := r.next(t)
	if got := env.GetMediaSettings(); !proto.Equal(got, want) {
		t.Fatalf("sent %v, want settings %v", env, want)
	}
}

func (r *rig) apply(t *testing.T, id, cfg string) {
	t.Helper()
	if err := r.caps[id].Apply(context.Background(), 1, json.RawMessage(cfg)); err != nil {
		t.Fatalf("%s %s: %v", id, cfg, err)
	}
}

func TestSettingsSentOnChangeAndAfterEveryReady(t *testing.T) {
	r := newRig(t)
	flat := &audiodv1.EqSettings{}
	byDefault := &audiodv1.VizSettings{Enabled: true, Source: audiodv1.VizSource_VIZ_SOURCE_MUSIC}
	eq := &audiodv1.EqSettings{PreampDb: -3, Bands: []*audiodv1.EqBand{
		{Type: audiodv1.EqType_EQ_TYPE_PEAKING, FreqHz: 1000, GainDb: 4, Q: 1.5},
		{Type: audiodv1.EqType_EQ_TYPE_HIGH_PASS, FreqHz: 20, GainDb: 0, Q: 0.1},
	}}
	on := &audiodv1.VizSettings{Enabled: true, Source: audiodv1.VizSource_VIZ_SOURCE_ALL}

	r.ready()
	r.settings(t, settings(audiodv1.DuckMode_DUCK_MODE_DUCK, flat, byDefault))
	r.apply(t, "media.eq", `{"preampDb":-3,"bands":[{"type":"peaking","freqHz":1000,"gainDb":4,"q":1.5},{"type":"highPass","freqHz":20,"gainDb":0,"q":0.1}]}`)
	r.settings(t, settings(audiodv1.DuckMode_DUCK_MODE_DUCK, eq, byDefault))
	r.apply(t, "media.viz", `{"enabled":true,"source":"all"}`)
	r.settings(t, settings(audiodv1.DuckMode_DUCK_MODE_DUCK, eq, on))
	r.apply(t, "media.player", `{"duckMode":"muffle"}`)
	r.settings(t, settings(audiodv1.DuckMode_DUCK_MODE_MUFFLE, eq, on))
	r.none(t)

	// audiod forgets them when its client goes: every Ready gets them all again
	r.m.MediaGone()
	r.apply(t, "media.player", `{}`)
	r.none(t)
	r.ready()
	r.settings(t, settings(audiodv1.DuckMode_DUCK_MODE_DUCK, eq, on))
	r.none(t)
}

func TestApplyRefusesInvalidConfig(t *testing.T) {
	r := newRig(t)
	r.up(t)
	band := func(s string) string { return `{"bands":[` + s + `]}` }
	elevenBands := band(strings.TrimSuffix(strings.Repeat(`{"type":"peaking","freqHz":100,"gainDb":1,"q":1},`, 11), ","))
	for id, cfgs := range map[string][]string{
		"media.eq": {
			`{"preampDb":12.5}`, `{"preampDb":-13}`, `{"preampDb":"0"}`, `{"gain":1}`, elevenBands,
			band(`{"type":"notch","freqHz":100,"gainDb":1,"q":1}`),
			band(`{"type":"peaking","freqHz":19,"gainDb":1,"q":1}`),
			band(`{"type":"peaking","freqHz":20001,"gainDb":1,"q":1}`),
			band(`{"type":"peaking","freqHz":100,"gainDb":12.1,"q":1}`),
			band(`{"type":"peaking","freqHz":100,"gainDb":1,"q":0.09}`),
			band(`{"type":"peaking","freqHz":100,"gainDb":1,"q":10.5}`),
			band(`{"type":"peaking","freqHz":100,"gainDb":1}`),
			band(`{"type":"peaking","freqHz":100,"gainDb":1,"q":1,"slope":2}`),
		},
		"media.viz":    {`{"enabled":"yes"}`, `{"source":"mic"}`, `{"fps":30}`, `{"style":"rainbow"}`},
		"media.player": {`{"duckMode":"mute"}`, `{"duck":"duck"}`},
		"media.url":    {`{"url":"https://a.example"}`, `[]`},
	} {
		for _, cfg := range cfgs {
			var ae *capability.ApplyError
			if err := r.caps[id].Apply(context.Background(), 1, json.RawMessage(cfg)); !errors.As(err, &ae) || ae.Code != "invalid" {
				t.Fatalf("%s %s: err = %v", id, cfg, err)
			}
		}
	}
	r.apply(t, "media.url", `{}`)
	r.none(t)
}

func state(t *testing.T, p capability.Provider) string {
	t.Helper()
	select {
	case s := <-p.State():
		return string(s)
	case <-time.After(time.Second):
		t.Fatalf("%s: no state", p.Descriptor().ID)
		return ""
	}
}

func TestStatesAreTheAppliedConfig(t *testing.T) {
	r := newRig(t)
	for id, want := range map[string]string{
		"media.player": `{"duckMode":"duck"}`,
		"media.eq":     `{"preampDb":0,"bands":[]}`,
		"media.viz":    `{"enabled":true,"source":"music","style":"bands"}`,
	} {
		if got := state(t, r.caps[id]); got != want {
			t.Fatalf("%s initial state = %s", id, got)
		}
	}
	if r.caps["media.url"].State() != nil {
		t.Fatal("media.url has a state")
	}
	r.apply(t, "media.player", `{"duckMode":"muffle"}`)
	r.apply(t, "media.eq", `{"bands":[{"type":"lowShelf","freqHz":120,"gainDb":-2.5,"q":0.7}]}`)
	r.apply(t, "media.viz", `{"enabled":false,"style":"spectrum"}`)
	for id, want := range map[string]string{
		"media.player": `{"duckMode":"muffle"}`,
		"media.eq":     `{"preampDb":0,"bands":[{"type":"lowShelf","freqHz":120,"gainDb":-2.5,"q":0.7}]}`,
		"media.viz":    `{"enabled":false,"source":"music","style":"spectrum"}`,
	} {
		if got := state(t, r.caps[id]); got != want {
			t.Fatalf("%s state = %s", id, got)
		}
	}
}

func (r *rig) mediaState(ms *audiodv1.MediaState) {
	r.m.MediaEnvelope(&audiodv1.Envelope{Body: &audiodv1.Envelope_MediaState{MediaState: ms}})
}

func session(t *testing.T, m *media.Media) *cadencev2.MediaSessionState {
	t.Helper()
	select {
	case s := <-m.Sessions():
		return s
	case <-time.After(time.Second):
		t.Fatal("no media session state")
		return nil
	}
}

func TestMediaStateBecomesSessionState(t *testing.T) {
	r := newRig(t)
	vol := uint32(40)
	r.mediaState(&audiodv1.MediaState{SessionId: "ms-1", State: audiodv1.PlaybackState_PLAYBACK_STATE_BUFFERING})
	r.mediaState(&audiodv1.MediaState{
		SessionId: "ms-3", Source: audiodv1.MediaSourceKind_MEDIA_SOURCE_KIND_URL, State: audiodv1.PlaybackState_PLAYBACK_STATE_PAUSED,
		PositionMs: 61000, PositionAtMs: 1_700_000_000_123, Rate: 0, DurationMs: 180000, Volume: &vol,
		Metadata: &audiodv1.MediaMetadata{Title: "SECRET-TITLE", Artist: "SECRET-ARTIST", Album: "SECRET-ALBUM", ArtworkUrl: "https://art.example/SECRET.jpg"},
		Actions: []audiodv1.MediaAction{
			audiodv1.MediaAction_MEDIA_ACTION_PAUSE, audiodv1.MediaAction_MEDIA_ACTION_RESUME, audiodv1.MediaAction_MEDIA_ACTION_STOP,
			audiodv1.MediaAction_MEDIA_ACTION_SEEK, audiodv1.MediaAction_MEDIA_ACTION_NEXT, audiodv1.MediaAction_MEDIA_ACTION_PREVIOUS,
			audiodv1.MediaAction_MEDIA_ACTION_UNSPECIFIED, audiodv1.MediaAction(99),
		},
	})
	want := &cadencev2.MediaSessionState{
		SessionId: "ms-3", Source: cadencev2.MediaSource_MEDIA_SOURCE_URL, State: cadencev2.PlaybackState_PLAYBACK_STATE_PAUSED,
		PositionMs: 61000, PositionAtMs: 1_700_000_000_123, DurationMs: 180000, Volume: &vol,
		Metadata: &cadencev2.MediaMetadata{Title: "SECRET-TITLE", Artist: "SECRET-ARTIST", Album: "SECRET-ALBUM", ArtworkUrl: "https://art.example/SECRET.jpg"},
		Actions:  []string{"pause", "resume", "stop", "seek", "next", "previous"},
	}
	// only the latest unread state is kept
	if got := session(t, r.m); !proto.Equal(got, want) {
		t.Fatalf("state = %v\nwant %v", got, want)
	}
	r.mediaState(&audiodv1.MediaState{SessionId: "ms-4", Source: audiodv1.MediaSourceKind_MEDIA_SOURCE_KIND_AIRPLAY, State: audiodv1.PlaybackState_PLAYBACK_STATE_PLAYING, Rate: 1})
	if got := session(t, r.m); got.GetSource() != cadencev2.MediaSource_MEDIA_SOURCE_AIRPLAY || got.GetState() != cadencev2.PlaybackState_PLAYBACK_STATE_PLAYING ||
		got.GetRate() != 1 || got.GetMetadata() != nil || got.Volume != nil {
		t.Fatalf("state = %v", got)
	}
	// a multiroom member's session plays its leader's stream
	r.mediaState(&audiodv1.MediaState{SessionId: "ms-5", Source: audiodv1.MediaSourceKind_MEDIA_SOURCE_KIND_GROUP, State: audiodv1.PlaybackState_PLAYBACK_STATE_PLAYING, Rate: 1})
	if got := session(t, r.m); got.GetSource() != cadencev2.MediaSource_MEDIA_SOURCE_GROUP {
		t.Fatalf("group state = %v", got)
	}

	// no audiod, no session
	r.m.MediaGone()
	if got := session(t, r.m); !proto.Equal(got, &cadencev2.MediaSessionState{State: cadencev2.PlaybackState_PLAYBACK_STATE_IDLE}) {
		t.Fatalf("state after gone = %v", got)
	}
	out := r.log.String()
	if !strings.Contains(out, `"msg":"media_state","state":"paused","source":"url"`) || strings.Contains(out, "SECRET") {
		t.Fatalf("log: %s", out)
	}
}

// media plays while the one session's latest state says so, and never once audiod went away (M6a-D8)
func TestPlayingFollowsTheSession(t *testing.T) {
	r := newRig(t)
	if r.m.Playing() {
		t.Fatal("playing before any state")
	}
	for _, c := range []struct {
		state audiodv1.PlaybackState
		want  bool
	}{
		{audiodv1.PlaybackState_PLAYBACK_STATE_BUFFERING, false},
		{audiodv1.PlaybackState_PLAYBACK_STATE_PLAYING, true},
		{audiodv1.PlaybackState_PLAYBACK_STATE_PAUSED, false},
		{audiodv1.PlaybackState_PLAYBACK_STATE_PLAYING, true},
		{audiodv1.PlaybackState_PLAYBACK_STATE_IDLE, false},
		{audiodv1.PlaybackState_PLAYBACK_STATE_PLAYING, true},
	} {
		r.mediaState(&audiodv1.MediaState{SessionId: "ms-1", State: c.state})
		if got := r.m.Playing(); got != c.want {
			t.Fatalf("%v: playing %v", c.state, got)
		}
	}
	r.m.MediaGone()
	if r.m.Playing() {
		t.Fatal("playing after audiod went away")
	}
}

func TestVizFramesKeepTheLatest(t *testing.T) {
	r := newRig(t)
	frame := func(b []byte) {
		r.m.MediaEnvelope(&audiodv1.Envelope{Body: &audiodv1.Envelope_VizFrame{VizFrame: &audiodv1.VizFrame{Bands: b}}})
	}
	first, second := bytes.Repeat([]byte{1}, 16), bytes.Repeat([]byte{2}, 16)
	second[15] = 255
	frame(first)
	frame(second)
	frame(make([]byte, 15))
	select {
	case got := <-r.m.Viz():
		if !bytes.Equal(got.Bands[:], second) || got.Style != "bands" {
			t.Fatalf("frame = %v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("no frame")
	}
	select {
	case got := <-r.m.Viz():
		t.Fatalf("second frame %v", got)
	default:
	}
}

// a command that comes while a new connection gets its settings never
// reaches audiod before them
func TestNothingPrecedesSettingsOnANewConnection(t *testing.T) {
	r := newRig(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var mu sync.Mutex
	var order []string
	send := func(env *audiodv1.Envelope) error {
		if env.GetMediaSettings() != nil {
			close(entered)
			<-release
		}
		mu.Lock()
		order = append(order, fmt.Sprintf("%T", env.GetBody()))
		mu.Unlock()
		if p := env.GetUrlPlay(); p != nil {
			go r.answer(p.GetRequestId(), true, "")
		}
		return nil
	}
	ready := make(chan struct{})
	go func() { defer close(ready); r.m.MediaReady(send) }()
	<-entered
	err := wait(t, r.command("media.url", "play", `{"url":"https://a.example/x"}`))
	close(release)
	<-ready
	if c := code(err); c != "unavailable" {
		t.Fatalf("play during the settings: code %q", c)
	}
	if err := wait(t, r.command("media.url", "play", `{"url":"https://a.example/x"}`)); err != nil {
		t.Fatalf("play after the settings: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(order) != 2 || order[0] != "*audiodv1.Envelope_MediaSettings" || order[1] != "*audiodv1.Envelope_UrlPlay" {
		t.Fatalf("order = %v", order)
	}
}

// a command whose link already went away is not sent at all
func TestCommandWithDoneContextNotSent(t *testing.T) {
	r := newRig(t)
	r.up(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := wait(t, r.commandCtx(ctx, "media.url", "play", `{"url":"https://a.example/x"}`)); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	r.none(t)
}
