package app

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/server/internal/commands"
	"git.shino.boo/devilreef/cadence/server/internal/jsonschemax"
	"git.shino.boo/devilreef/cadence/server/internal/media"
)

// a stop request pauses the device's media only while its session plays (M6a-D6)
func TestMediaPlaying(t *testing.T) {
	sessions := media.NewStore(nil, slog.New(slog.DiscardHandler))
	playing := mediaPlaying(sessions)
	if playing("dev-1") {
		t.Fatal("no session plays")
	}
	for state, want := range map[cadencev2.PlaybackState]bool{
		cadencev2.PlaybackState_PLAYBACK_STATE_PLAYING:   true,
		cadencev2.PlaybackState_PLAYBACK_STATE_PAUSED:    false,
		cadencev2.PlaybackState_PLAYBACK_STATE_BUFFERING: false,
	} {
		sessions.Handle("dev-1", &cadencev2.Envelope{Body: &cadencev2.Envelope_MediaSessionState{MediaSessionState: &cadencev2.MediaSessionState{
			SessionId: "ms-1", State: state,
		}}})
		if got := playing("dev-1"); got != want {
			t.Errorf("%v: playing %v", state, got)
		}
	}
}

type pauseLookup struct{}

func (pauseLookup) Command(_ context.Context, dev, capability, name string) (string, *jsonschema.Schema, error) {
	if capability != "media.player" || name != "pause" {
		return "", nil, errors.New("unknown command")
	}
	sch, err := jsonschemax.Compile(`{"type":"object","additionalProperties":false}`)
	return dev, sch, err
}

// answeringConn answers every command it is sent with ok
type answeringConn struct {
	cmds *commands.Service
	sent chan *cadencev2.CapabilityCommand
}

func (c *answeringConn) DeviceID() string { return "dev-1" }

func (c *answeringConn) Send(env *cadencev2.Envelope) error {
	cmd := env.GetCapabilityCommand()
	if cmd == nil {
		return nil
	}
	c.sent <- cmd
	go c.cmds.Handle(c, &cadencev2.Envelope{Body: &cadencev2.Envelope_CommandResult{CommandResult: &cadencev2.CommandResult{
		RequestId: cmd.GetRequestId(),
	}}})
	return nil
}

func TestPauseMediaSendsThePause(t *testing.T) {
	cmds := commands.New(pauseLookup{}, slog.New(slog.DiscardHandler))
	conn := &answeringConn{cmds: cmds, sent: make(chan *cadencev2.CapabilityCommand, 1)}
	cmds.Linked(context.Background(), conn)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := pauseMedia(cmds)(ctx, "dev-1"); err != nil {
		t.Fatal(err)
	}
	cmd := <-conn.sent
	if cmd.GetCapability() != "media.player" || cmd.GetName() != "pause" || cmd.GetJson() != "{}" {
		t.Fatalf("sent %v", cmd)
	}
}
