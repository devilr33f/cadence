package group

import "git.shino.boo/devilreef/cadence/pkg/capschema"

// SetOutput names the output audiod plays through now. A route change republishes it and selects its stored trim;
// the rest of the audiod config stays byte-for-byte the same, so the group links stay up.
func (g *Group) SetOutput(out capschema.MediaGroupOutput) {
	g.sendMu.Lock()
	defer g.sendMu.Unlock()
	g.mu.Lock()
	if sameOutput(g.output, out) {
		g.mu.Unlock()
		return
	}
	g.output = capschema.MediaGroupOutput{}
	if copy := cloneOutput(out); copy != nil {
		g.output = *copy
	}
	g.publishLocked()
	g.mu.Unlock()
	g.sendIfChanged()
}

func cloneOutput(out capschema.MediaGroupOutput) *capschema.MediaGroupOutput {
	if out.ID == "" {
		return nil
	}
	copy := capschema.MediaGroupOutput{ID: out.ID, Kind: out.Kind}
	if out.DelayHintUs != nil {
		hint := *out.DelayHintUs
		copy.DelayHintUs = &hint
	}
	return &copy
}

func sameOutput(a, b capschema.MediaGroupOutput) bool {
	if a.ID != b.ID || a.Kind != b.Kind || (a.DelayHintUs == nil) != (b.DelayHintUs == nil) {
		return false
	}
	return a.DelayHintUs == nil || *a.DelayHintUs == *b.DelayHintUs
}
