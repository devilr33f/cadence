package capability

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

type SecretSpec struct{ Name, Description string }

type Descriptor struct {
	ID           string
	Version      uint32
	ConfigSchema string
	StateSchema  string
	Secrets      []SecretSpec
	// Commands maps each command name to the JSON Schema (draft 2020-12) of its args.
	Commands map[string]string
}

// Provider is one board capability. State delivers the latest state, dropping older unread ones.
//
// Command runs on a goroutine of its own, only for a name in Commands, and
// must return once ctx is done. It checks its args itself, strictly: the
// schema is the server's check, not a guarantee.
type Provider interface {
	Descriptor() Descriptor
	Apply(ctx context.Context, gen int64, cfg json.RawMessage) error
	Command(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, error)
	State() <-chan json.RawMessage
}

// SecretApplier is optionally implemented by a provider that declares Secrets:
// its configs are applied through ApplySecrets in place of Apply, with the
// declared secrets that came with them (none is nil). A secret value never
// reaches a log, an error, a state or an event.
type SecretApplier interface {
	ApplySecrets(ctx context.Context, gen int64, cfg json.RawMessage, secrets map[string]string) error
}

// DeviceIdentified is optionally implemented by a provider that needs the device's enrolled id (media.group's member
// id): core hands it over before any config is applied.
type DeviceIdentified interface {
	SetDeviceID(id string)
}

// CommandWaiter is optionally implemented by a provider with a command that runs longer than core's usual wait
// (media.group's calibrate): core lets that command run CommandWait(name) when it is longer.
type CommandWaiter interface {
	CommandWait(name string) time.Duration
}

// LinkWatcher is optionally implemented by a provider whose work depends on the server link itself (network.wifi: a
// switch counts only once the link came back over the new network). Core calls SetRedial once, before any link,
// with a function that makes the link drop whatever it holds or waits for and dial again at once (the network under
// it changed); then LinkChanged(true) after each link's welcome and LinkChanged(false) once that link ended. Neither
// call, nor redial, blocks.
type LinkWatcher interface {
	SetRedial(redial func())
	LinkChanged(up bool)
}

var ErrUnsupported = errors.New("unsupported")

// ApplyError carries a code the server may see, never config values.
type ApplyError struct{ Code string }

func (e *ApplyError) Error() string { return "apply failed: " + e.Code }

// CommandError carries a code the server may see, never args or upstream text.
type CommandError struct{ Code string }

func (e *CommandError) Error() string { return "command failed: " + e.Code }
