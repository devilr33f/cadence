package bluetooth

import (
	"context"
	"encoding/json"

	"git.shino.boo/devilreef/cadence/device/internal/capability"
)

// provider is the bluetooth capability.
type provider struct{ b *Bluetooth }

func (*provider) Descriptor() capability.Descriptor {
	return capability.Descriptor{ID: "bluetooth", Version: 1, ConfigSchema: configSchema, StateSchema: stateSchema, Commands: commands()}
}

func (p *provider) Apply(_ context.Context, _ int64, raw json.RawMessage) error {
	cfg, ok := parseConfig(raw)
	if !ok {
		return &capability.ApplyError{Code: "invalid"}
	}
	p.b.configure(cfg)
	return nil
}

func (p *provider) Command(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, error) {
	return p.b.command(ctx, name, args)
}

func (p *provider) State() <-chan json.RawMessage { return p.b.state }

// configure takes a config: the adapter gets it at the next reconcile, audiod whether it receives at once
func (b *Bluetooth) configure(cfg Config) {
	b.sendMu.Lock()
	defer b.sendMu.Unlock()
	b.mu.Lock()
	was := b.cfg.Enabled && b.cfg.Receive
	b.cfg = cfg
	b.cfgGen++
	send := b.send
	b.publishLocked()
	b.mu.Unlock()
	if send != nil && was != (cfg.Enabled && cfg.Receive) {
		_ = send(b.configEnvelope())
	}
	b.wake()
}
