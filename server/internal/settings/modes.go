package settings

import (
	"context"
	"encoding/json"
	"slices"

	"git.shino.boo/devilreef/cadence/server/internal/store/db"
)

// Lock takes the settings lock in q's transaction (db LockAgentModes): every
// settings write takes it first, and so do the mode and provider deletes that
// read the settings for their in-use check, so a delete never misses a
// setting that names its row and a setting never names a row a delete has
// just removed.
func Lock(ctx context.Context, q *db.Queries) error { return q.LockAgentModes(ctx) }

// keyNames are the names a scope's keys name, by key.
type keyNames map[string][]string

// modeKeys are the keys that name agent modes, in the order a refusal names
// them.
var modeKeys = []string{"mode", "allowedModes", "compactionMode", "memoryMode"}

// NamedModes are the agent modes the stored settings name, read through q:
// the effective global mode and allowed modes, the built-in defaults
// included, and each device's own overrides of either. A mode delete reads
// them under the settings lock (Lock), which every settings write takes too.
func NamedModes(ctx context.Context, q *db.Queries) ([]string, error) {
	rows, err := q.ListSettings(ctx)
	if err != nil {
		return nil, err
	}
	global := Overrides{}
	var names []string
	for _, row := range rows {
		o := Overrides{}
		if err := json.Unmarshal(row.Data, &o); err != nil {
			return nil, err
		}
		if row.Scope == globalScope {
			global = o
			continue
		}
		names = append(names, overrideModes(o).all()...)
	}
	g, err := globalModes(global)
	if err != nil {
		return nil, err
	}
	return append(names, g.all()...), nil
}

// all are the names of every key, in modeKeys order.
func (k keyNames) all() []string {
	var out []string
	for _, key := range modeKeys {
		out = append(out, k[key]...)
	}
	return out
}

// globalModes are the modes the effective global values of o name.
func globalModes(o Overrides) (keyNames, error) {
	v, err := Merge(o, nil)
	if err != nil {
		return nil, err
	}
	names := keyNames{"mode": {v.Mode}, "allowedModes": v.AllowedModes}
	if v.CompactionMode != "" {
		names["compactionMode"] = []string{v.CompactionMode}
	}
	if v.MemoryMode != "" {
		names["memoryMode"] = []string{v.MemoryMode}
	}
	return names, nil
}

// overrideModes are the modes one scope's own mode and allowedModes keys name.
func overrideModes(o Overrides) keyNames {
	names := keyNames{}
	var mode string
	if json.Unmarshal(o["mode"], &mode) == nil && mode != "" {
		names["mode"] = []string{mode}
	}
	var allowed []string
	if json.Unmarshal(o["allowedModes"], &allowed) == nil && len(allowed) > 0 {
		names["allowedModes"] = allowed
	}
	var compaction string
	if json.Unmarshal(o["compactionMode"], &compaction) == nil && compaction != "" {
		names["compactionMode"] = []string{compaction}
	}
	var memory string
	if json.Unmarshal(o["memoryMode"], &memory) == nil && memory != "" {
		names["memoryMode"] = []string{memory}
	}
	return names
}

// checkModes refuses a write that names a mode its scope did not name before
// (under either key) and that has no row, with an InvalidError of the key
// that names it. Names named before pass, so a row gone since blocks nothing;
// with deletes refused while a setting names their mode, what the settings
// name exists. q holds the settings lock.
func checkModes(ctx context.Context, q *db.Queries, before, after keyNames) error {
	was := before.all()
	var added []string
	keyOf := map[string]string{}
	for _, key := range modeKeys {
		for _, n := range after[key] {
			if n != "" && !slices.Contains(was, n) && keyOf[n] == "" {
				added = append(added, n)
				keyOf[n] = key
			}
		}
	}
	if len(added) == 0 {
		return nil
	}
	found, err := q.ExistingAgentModes(ctx, added)
	if err != nil {
		return err
	}
	for _, n := range added {
		if !slices.Contains(found, n) {
			return &InvalidError{Key: keyOf[n]}
		}
	}
	return nil
}
