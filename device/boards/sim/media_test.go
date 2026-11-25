package sim_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"git.shino.boo/devilreef/cadence/device/boards/sim"
	"git.shino.boo/devilreef/cadence/device/internal/audio"
	"git.shino.boo/devilreef/cadence/device/internal/board"
	"git.shino.boo/devilreef/cadence/device/internal/core"
	"git.shino.boo/devilreef/cadence/device/internal/link"
	"git.shino.boo/devilreef/cadence/device/internal/syncstore"
	"git.shino.boo/devilreef/cadence/pkg/capschema"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/pkg/wire"
)

func ids(b board.Board) []string {
	var out []string
	for _, p := range b.Capabilities() {
		out = append(out, p.Descriptor().ID)
	}
	return out
}

func TestSimGroupOutputReachesState(t *testing.T) {
	hint := 145000
	b := sim.New(func(string) string { return "" }, t.TempDir(), nil, sim.WithAudiod("unused"),
		sim.WithGroupOutput(capschema.MediaGroupOutput{ID: "bt:aa:bb:cc:dd:ee:10", Kind: capschema.GroupOutputBluetooth, DelayHintUs: &hint}))
	for _, p := range b.Capabilities() {
		if p.Descriptor().ID != capschema.MediaGroupID {
			continue
		}
		select {
		case raw := <-p.State():
			var st capschema.MediaGroupStatus
			if json.Unmarshal(raw, &st) != nil || st.Output == nil || st.Output.ID != "bt:aa:bb:cc:dd:ee:10" ||
				st.Output.Kind != capschema.GroupOutputBluetooth || st.Output.DelayHintUs == nil || *st.Output.DelayHintUs != hint {
				t.Fatalf("state = %s", raw)
			}
			return
		case <-time.After(time.Second):
			t.Fatal("no group state")
		}
	}
	t.Fatal("no media.group")
}

func TestAudiodModeListsMediaCapabilities(t *testing.T) {
	b, dir, logs := newAudiodSim(t)
	if got := ids(b); !slices.Equal(got, []string{"audio.output", "audio.input", "media.player", "media.url", "media.eq", "media.viz", "media.soloist", "media.airplay", "bluetooth", "network.wifi", "media.group", "alarms"}) {
		t.Fatalf("capabilities = %v", got)
	}
	if mr, ok := b.(board.MediaReporter); !ok || mr.MediaSessions() == nil {
		t.Fatal("no media sessions in audiod mode")
	}
	// the press file's buttons and audiod's input events reach core's input; no mute switch
	if ib, ok := b.(board.Inputs); !ok || ib.Input() == nil {
		t.Fatal("no input in audiod mode")
	}
	aio := audiodIO(t, b, logs)
	waitRecord(t, dir, "configure_output", `channels=2 eq_bands=0 ca_file="/etc/ssl/certs/ca-certificates.crt"`)
	waitRecord(t, dir, "mute_state", "false")
	// soloist off until configured; the sim's level is the default, so no level follows
	waitRecord(t, dir, "soloist_config", fmt.Sprintf("enabled=false key_bytes=0 name=%q dir=%q scratch=%q ca=%q initial=50",
		"Cadence", filepath.Join(dir, "soloist"), filepath.Join(dir, "run", "soloist"), "/etc/ssl/certs/ca-certificates.crt"))

	// airplay off until configured, its dirs where the board keeps them
	waitRecord(t, dir, "airplay_config", fmt.Sprintf("enabled=false name=%q dir=%q scratch=%q",
		"Cadence", filepath.Join(dir, "airplay"), filepath.Join(dir, "run", "airplay")))
	// out of any group until the server says otherwise
	waitRecord(t, dir, "group_config", `enabled=false group="" role=GROUP_ROLE_UNSPECIFIED member="" host="" port=0 key_bytes=0 `+
		`channel=GROUP_CHANNEL_UNSPECIFIED latency=0 trim=0 budget=0 bitrate=0 pcm=false members=[] wait=0`)
	// the group clock's fake offset and drift reach audiod from cadenced's environment (the test's getenv says 1)
	waitRecord(t, dir, "sim_clock_env", `offset_ms="1" ppm="1"`)

	ducker, ok := aio.Speaker.(audio.Ducker)
	if !ok {
		t.Fatal("the sim speaker does not duck in audiod mode")
	}
	ducker.Duck(true)
	waitRecord(t, dir, "duck_set", "true")

	plain := sim.New(func(string) string { return "1" }, t.TempDir(), nil)
	if got := ids(plain); !slices.Equal(got, []string{"audio.output"}) {
		t.Fatalf("capabilities without audiod = %v", got)
	}
	if mr, ok := plain.(board.MediaReporter); ok && mr.MediaSessions() != nil {
		t.Fatal("media sessions without audiod")
	}
	if ib, ok := plain.(board.Inputs); ok && ib.Input() != nil {
		t.Fatal("an input without audiod")
	}
}

type linkPeer struct {
	t *testing.T
	c *websocket.Conn
}

func (p *linkPeer) send(env *cadencev2.Envelope) {
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

func (p *linkPeer) recv() *cadencev2.Envelope {
	p.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
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

// link runs the device's link to a test server and returns the server's end once it said hello.
func linkUp(t *testing.T, b board.Board, log *slog.Logger) *linkPeer {
	t.Helper()
	conns := make(chan *websocket.Conn, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c, err := websocket.Accept(w, r, nil); err == nil {
			conns <- c
		}
	}))
	t.Cleanup(srv.Close)
	store, err := syncstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s, err := core.NewSync(b, store, "v-test", log)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	wait := s.StartAudio(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = link.Run(ctx, link.Dialer{URL: srv.URL, Token: "t"}, s, log)
	}()
	t.Cleanup(func() { cancel(); <-done; wait() })
	var p *linkPeer
	select {
	case c := <-conns:
		t.Cleanup(func() { _ = c.CloseNow() })
		p = &linkPeer{t: t, c: c}
	case <-time.After(5 * time.Second):
		t.Fatal("device did not connect")
	}
	if p.recv().GetHello() == nil {
		t.Fatal("first frame is not a hello")
	}
	p.send(&cadencev2.Envelope{Body: &cadencev2.Envelope_Welcome{Welcome: &cadencev2.Welcome{ServerVersion: "test"}}})
	return p
}

// rule 8: a url and media metadata cross the device both ways, to audiod in a
// command and back to the server in the session state, and never reach its log
func TestMediaNeverReachesTheLog(t *testing.T) {
	b, _, logs := newAudiodSim(t, "--media-state")
	log := slog.New(slog.NewJSONHandler(logs, nil))
	p := linkUp(t, b, log)

	// audiod is up once its first state came
	for env := p.recv(); env.GetMediaSessionState() == nil; env = p.recv() {
		if env.GetStateDelta() == nil {
			t.Fatalf("frame before the media state: %v", env)
		}
	}
	const url, artwork = "https://media.example/SECRET-URL.flac", "https://art.example/SECRET-ART.jpg"
	md := map[string]string{"title": "SECRET-TITLE Песня", "artist": "SECRET-ARTIST", "album": "SECRET-ALBUM", "artworkUrl": artwork}
	args, _ := json.Marshal(map[string]any{"url": url, "metadata": md})
	p.send(&cadencev2.Envelope{Body: &cadencev2.Envelope_CapabilityCommand{CapabilityCommand: &cadencev2.CapabilityCommand{
		RequestId: "r1", Capability: "media.url", Name: "play", Json: string(args),
	}}})
	answered, playing := false, false
	for !answered || !playing {
		env := p.recv()
		if r := env.GetCommandResult(); r != nil {
			if r.GetRequestId() != "r1" || r.GetCode() != "" {
				t.Fatalf("result = %v", r)
			}
			answered = true
		}
		if s := env.GetMediaSessionState(); s.GetState() == cadencev2.PlaybackState_PLAYBACK_STATE_PLAYING {
			m := s.GetMetadata()
			if m.GetTitle() != md["title"] || m.GetArtist() != md["artist"] || m.GetAlbum() != md["album"] || m.GetArtworkUrl() != artwork {
				t.Fatalf("state metadata = %v", m)
			}
			playing = true
		}
	}

	out := logs.String()
	if !strings.Contains(out, `"msg":"command","capability":"media.url","name":"play","code":"ok"`) ||
		!strings.Contains(out, `"msg":"media_state","state":"playing","source":"url"`) {
		t.Fatalf("log lacks the command or the state:\n%s", out)
	}
	for _, secret := range []string{"SECRET", "media.example", "art.example", "Песня"} {
		if strings.Contains(out, secret) {
			t.Fatalf("log carries %q:\n%s", secret, out)
		}
	}
}
