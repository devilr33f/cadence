package bluez

import (
	"context"
	"errors"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/prop"

	"git.shino.boo/devilreef/cadence/device/internal/bluetooth"
)

const (
	playerPath  = "/org/cadence/player"
	ifaceMPRIS  = "org.mpris.MediaPlayer2.Player"
	ifaceMedia1 = "org.bluez.Media1"
)

// mediaPlayer is our org.mpris.MediaPlayer2.Player, which bluez's Media1 calls when a speaker (an avrcp controller)
// presses play, pause, next or previous, and whose PlaybackStatus bluez reports back to it
type mediaPlayer struct {
	ctl   bluetooth.PlayerControl
	props *prop.Properties
}

func (p *mediaPlayer) call(method string) *dbus.Error {
	p.ctl.PlayerCommand(method)
	return nil
}

func (p *mediaPlayer) Play() *dbus.Error      { return p.call("Play") }
func (p *mediaPlayer) Pause() *dbus.Error     { return p.call("Pause") }
func (p *mediaPlayer) PlayPause() *dbus.Error { return p.call("PlayPause") }
func (p *mediaPlayer) Stop() *dbus.Error      { return p.call("Stop") }
func (p *mediaPlayer) Next() *dbus.Error      { return p.call("Next") }
func (p *mediaPlayer) Previous() *dbus.Error  { return p.call("Previous") }

// playerProps is what bluez reads of a player: its status, and that it takes play, pause, next and previous; no
// Volume, which stays bluealsa's (the transport's absolute volume)
func playerProps() map[string]*prop.Prop {
	c := func(v any) *prop.Prop { return &prop.Prop{Value: v, Emit: prop.EmitConst} }
	return map[string]*prop.Prop{
		"PlaybackStatus": {Value: bluetooth.PlayerStopped, Emit: prop.EmitTrue},
		"LoopStatus":     c("None"),
		"Shuffle":        c(false),
		"Rate":           c(1.0),
		"Position":       c(int64(0)),
		"Metadata":       c(map[string]dbus.Variant{}),
		"Identity":       c("Cadence"),
		"CanControl":     c(true),
		"CanPlay":        c(true),
		"CanPause":       c(true),
		"CanGoNext":      c(true),
		"CanGoPrevious":  c(true),
		"CanSeek":        c(false),
	}
}

// RegisterPlayer exports our player once and registers it with the adapter's Media1, its properties in the call
func (b *Backend) RegisterPlayer(ctx context.Context, c bluetooth.PlayerControl) error {
	b.playerMu.Lock()
	if b.player == nil {
		p := &mediaPlayer{ctl: c}
		if err := b.conn.Export(p, playerPath, ifaceMPRIS); err != nil {
			b.playerMu.Unlock()
			return err
		}
		props, err := prop.Export(b.conn, playerPath, prop.Map{ifaceMPRIS: playerProps()})
		if err != nil {
			_ = b.conn.Export(nil, playerPath, ifaceMPRIS)
			b.playerMu.Unlock()
			return err
		}
		p.props = props
		b.player = p
	}
	values, derr := b.player.props.GetAll(ifaceMPRIS)
	b.playerMu.Unlock()
	if derr != nil {
		return derr
	}
	err := b.call(ctx, bluezName, b.adapter, ifaceMedia1+".RegisterPlayer", dbus.ObjectPath(playerPath), values)
	var e dbus.Error
	if err != nil && errors.As(err, &e) && e.Name == "org.bluez.Error.AlreadyExists" {
		return nil
	}
	return err
}

// SetPlayerStatus changes PlaybackStatus; prop emits PropertiesChanged, which bluez follows
func (b *Backend) SetPlayerStatus(_ context.Context, status string) error {
	switch status {
	case bluetooth.PlayerPlaying, bluetooth.PlayerPaused, bluetooth.PlayerStopped:
	default:
		return errors.New("bluez: unknown player status")
	}
	b.playerMu.Lock()
	defer b.playerMu.Unlock()
	if b.player == nil {
		return errors.New("bluez: no player")
	}
	// SetMust: PlaybackStatus is read only on the bus, ours to change
	b.player.props.SetMust(ifaceMPRIS, "PlaybackStatus", status)
	return nil
}
