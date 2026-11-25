package settings

import (
	"reflect"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"git.shino.boo/devilreef/cadence/server/internal/api"
)

// PutSettingsRequest is a scope sparse body: any of the known keys, each
// with its value; null removes the key override. Unknown keys, wrong types
// and keys the scope cannot set answer settings_invalid.
type PutSettingsRequest struct{}

// Schema is an object of the settings keys, all optional, each nullable.
func (PutSettingsRequest) Schema(r huma.Registry) *huma.Schema { return settingsSchema(r, true) }

// SettingsReply is a scope's sparse set as answered: the keys set in that
// scope, each with its value. A put's null removed its key, so none is null.
type SettingsReply struct{}

// Schema is the put's object without the nulls, named once in the document
// for the four operations that answer it.
func (SettingsReply) Schema(r huma.Registry) *huma.Schema {
	const name = "SettingsReply"
	if _, ok := r.Map()[name]; !ok {
		r.Map()[name] = settingsSchema(r, false)
	}
	return &huma.Schema{Ref: "#/components/schemas/" + name}
}

// settingsSchema is an object of the settings keys, all optional; a key's
// value may be null when nullable (an object key's schema is a ref, which
// stays as it is).
func settingsSchema(r huma.Registry, nullable bool) *huma.Schema {
	props := map[string]*huma.Schema{}
	t := reflect.TypeFor[Values]()
	for i := range t.NumField() {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		s := r.Schema(t.Field(i).Type, true, "")
		if s.Ref == "" {
			c := *s
			c.Nullable = nullable
			s = &c
		}
		props[name] = s
	}
	return &huma.Schema{Type: huma.TypeObject, Properties: props, AdditionalProperties: false}
}

// namesNote is what both puts refuse of the names a key holds
var namesNote = "; mode, allowedModes, compactionMode or memoryMode naming an agent mode with no row that the scope did not name before is " +
	"refused, and so is a key naming a provider instance with no row that it did not name before (" +
	strings.Join(InstanceKeys, ", ") + "): settings_invalid with that key's path"

var docs = map[string]api.Doc{
	"get-global-settings": {Summary: "Get the global settings", Reply: SettingsReply{}},
	"put-global-settings": {
		Summary: "Replace the whole global set: read it, change it and send it whole", Body: PutSettingsRequest{},
		BodyRequired: true, Reply: SettingsReply{},
		BodyNote: "replaces the whole global set, so a key left out is unset: read, change, send it whole; keys the " +
			"global scope cannot set (" + strings.Join(deviceOnly, ", ") + ") are refused" + namesNote +
			"; for mode and allowedModes the effective value counts, so a set without allowedModes names the default list",
	},
	"get-device-settings": {Summary: "Get a device settings", Reply: SettingsReply{}},
	"put-device-settings": {
		Summary: "Set a device settings", Body: PutSettingsRequest{}, BodyRequired: true, Reply: SettingsReply{},
		BodyNote: "keys sent replace the device's own, null removes one, keys left out stay; keys only the global " +
			"scope sets (" + strings.Join(globalOnly, ", ") + ") are refused" + namesNote,
	},
}
