package settings

import (
	"context"
	"encoding/json"
	"slices"

	"git.shino.boo/devilreef/cadence/server/internal/store/db"
)

// InstanceKeys are the keys whose value names a provider instance, in the
// order a refusal names them: mcp, t3code, catalogSources and music a list of them (music one name
// alone too), every other one alone.
// voice, model and maintenanceModel name what an instance offers, not one.
var InstanceKeys = []string{"stt", "tts", "llm", "embed", "weather", "search", "music", "musicPlayer", "xSearch", "hermes", "homeAssistant", "telemetryExport", "mcp", "t3code", "catalogSources"}

// overrideInstances are the instances one scope's own keys name.
func overrideInstances(o Overrides) keyNames {
	names := keyNames{}
	for _, key := range InstanceKeys {
		raw, ok := o[key]
		if !ok {
			continue
		}
		var list []string
		if key == "mcp" || key == "music" || key == "t3code" || key == "catalogSources" {
			var names Names
			_ = json.Unmarshal(raw, &names)
			list = names
		} else {
			var n string
			if json.Unmarshal(raw, &n) == nil && n != "" {
				list = []string{n}
			}
		}
		if len(list) > 0 {
			names[key] = list
		}
	}
	return names
}

// checkInstances refuses a write whose key names a provider instance that key
// did not name before and that has no row, with an InvalidError of that key.
// What a key named before passes, so an instance gone since blocks nothing;
// with deletes refused while a setting names their instance, what the
// settings name exists. q holds the settings lock.
func checkInstances(ctx context.Context, q *db.Queries, before, after keyNames) error {
	var added []string
	keyOf := map[string]string{}
	for _, key := range InstanceKeys {
		for _, n := range after[key] {
			if !slices.Contains(before[key], n) && keyOf[n] == "" {
				added = append(added, n)
				keyOf[n] = key
			}
		}
	}
	if len(added) == 0 {
		return nil
	}
	found, err := q.ExistingProviderInstances(ctx, added)
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

// NamesInstance says whether a stored setting names the provider instance:
// any scope's key of InstanceKeys. A provider delete reads it under the
// settings lock (Lock), which every settings write takes too.
func NamesInstance(ctx context.Context, q *db.Queries, name string) (bool, error) {
	rows, err := q.ListSettings(ctx)
	if err != nil {
		return false, err
	}
	for _, row := range rows {
		o := Overrides{}
		if err := json.Unmarshal(row.Data, &o); err != nil {
			return false, err
		}
		for _, list := range overrideInstances(o) {
			if slices.Contains(list, name) {
				return true, nil
			}
		}
	}
	return false, nil
}
