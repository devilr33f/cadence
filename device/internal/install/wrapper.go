package install

import (
	"context"
	"path"
)

// startV2 installs cadence-run and its board's hook beside it from the tree, and gives init its line: init then runs
// the wrapper, which runs the board's prestart, blanks the lights and sets the volume to the persisted level before
// every start of cadenced (M2-D3, M2m-D2). a board that boots by an init script gets its launcher there instead,
// which starts the wrapper now and at every boot
func (in *Installer) startV2(ctx context.Context) error {
	if err := in.dev.Copy(ctx, path.Join(in.l.root, "share", "board-hook"), in.l.hook, 0o644); err != nil {
		return err
	}
	if err := in.dev.Copy(ctx, path.Join(in.l.root, "share", "cadence-run"), in.l.wrapper, 0o755); err != nil {
		return err
	}
	if in.byScript() {
		return in.startByScript(ctx)
	}
	if err := in.setInittab(ctx, "start-v2", in.l.v2Entry, func(cur []byte) ([]byte, error) {
		return withEntry(cur, in.l.v2Entry, in.p.Anchor)
	}); err != nil {
		return err
	}
	in.say("init runs %s", in.l.wrapper)
	return nil
}
