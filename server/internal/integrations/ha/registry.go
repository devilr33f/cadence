package ha

import (
	"encoding/json"
	"slices"
	"strings"
)

// Area is one entry of Home Assistant's area registry.
type Area struct {
	ID, Name string
	Aliases  []string
}

// regEntry is what the cache takes from an entity registry entry.
type regEntry struct {
	name, originalName, area, device, platform string
	disabled                                   bool
	// unreadable is an entry kept for its id alone: what else it holds was
	// malformed, its platform too
	unreadable bool
	aliases    []string
}

// registries are Home Assistant's area, device and entity registries as the
// cache keeps them; each is replaced whole when it is listed again.
type registries struct {
	areas []Area // sorted by id
	// devices maps a device id to its area id
	devices  map[string]string
	entities map[string]regEntry
	// entitiesBehind is true while entities may lack an entity that has a
	// state: from a registry event or a load until a list lands
	entitiesBehind bool
}

func validRegistryID(id string) bool {
	return len(id) <= maxRegistryID && registryIDRe.MatchString(id)
}

// list decodes a registry list into its entries; a result that is no list is
// a bad response.
func list(raw json.RawMessage) ([]json.RawMessage, error) {
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil {
		return nil, fail(CodeBadResponse)
	}
	return items, nil
}

// optionalID reads a null or a registry id; ok is false for anything else.
func optionalID(raw json.RawMessage) (string, bool) {
	var id string
	if !text(raw, maxRegistryID, &id) || id != "" && !validRegistryID(id) {
		return "", false
	}
	return id, true
}

// aliases keeps the non-empty strings of at most maxName code points, up to
// maxAliases; a null alias (the entity's own name) is skipped. ok is false
// when raw is neither null nor a list.
func aliases(raw json.RawMessage) ([]string, bool) {
	if isNull(raw) {
		return nil, true
	}
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil {
		return nil, false
	}
	var out []string
	for _, item := range items {
		var s string
		if len(out) == maxAliases || isNull(item) || !text(item, maxName, &s) || strings.TrimSpace(s) == "" {
			continue
		}
		out = append(out, s)
	}
	return out, true
}

// parseAreas reads config/area_registry/list, sorted by id.
func parseAreas(raw json.RawMessage) ([]Area, error) {
	items, err := list(raw)
	if err != nil {
		return nil, err
	}
	var out []Area
	for _, item := range items {
		var a struct {
			AreaID  string          `json:"area_id"`
			Name    json.RawMessage `json:"name"`
			Aliases json.RawMessage `json:"aliases"`
		}
		if json.Unmarshal(item, &a) != nil || !validRegistryID(a.AreaID) {
			continue
		}
		var name string
		al, ok := aliases(a.Aliases)
		if !ok || !text(a.Name, maxName, &name) || name == "" {
			continue
		}
		out = append(out, Area{ID: a.AreaID, Name: name, Aliases: al})
	}
	slices.SortFunc(out, func(a, b Area) int { return strings.Compare(a.ID, b.ID) })
	return slices.CompactFunc(out, func(a, b Area) bool { return a.ID == b.ID }), nil
}

// parseDevices reads config/device_registry/list into device id -> area id.
func parseDevices(raw json.RawMessage) (map[string]string, error) {
	items, err := list(raw)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(items))
	for _, item := range items {
		var d struct {
			ID     string          `json:"id"`
			AreaID json.RawMessage `json:"area_id"`
		}
		if json.Unmarshal(item, &d) != nil || !validRegistryID(d.ID) {
			continue
		}
		if area, ok := optionalID(d.AreaID); ok {
			out[d.ID] = area
		}
	}
	return out, nil
}

// parseEntityRegistry reads config/entity_registry/list; aliases come from
// get_entries, since the list leaves them out.
func parseEntityRegistry(raw json.RawMessage) (map[string]regEntry, error) {
	items, err := list(raw)
	if err != nil {
		return nil, err
	}
	out := make(map[string]regEntry, len(items))
	for _, item := range items {
		var r struct {
			EntityID     string          `json:"entity_id"`
			Name         json.RawMessage `json:"name"`
			OriginalName json.RawMessage `json:"original_name"`
			AreaID       json.RawMessage `json:"area_id"`
			DeviceID     json.RawMessage `json:"device_id"`
			DisabledBy   json.RawMessage `json:"disabled_by"`
			Platform     json.RawMessage `json:"platform"`
		}
		if json.Unmarshal(item, &r) != nil || !validEntityID(r.EntityID) {
			continue
		}
		e := regEntry{disabled: !isNull(r.DisabledBy)}
		var areaOK, deviceOK bool
		e.area, areaOK = optionalID(r.AreaID)
		e.device, deviceOK = optionalID(r.DeviceID)
		if !areaOK || !deviceOK || !text(r.Name, maxName, &e.name) || !text(r.OriginalName, maxName, &e.originalName) ||
			!text(r.Platform, maxDomain, &e.platform) {
			// a dropped entry would read as no entry at all: an entity of any
			// platform, Cadence's own included
			e = regEntry{disabled: e.disabled, unreadable: true}
		}
		out[r.EntityID] = e
	}
	return out, nil
}

// aliasIDs are the enabled registry entries with a state to ask get_entries
// about, sorted; the states cap their number at maxEntities.
func aliasIDs(entities map[string]regEntry, withState func(id string) bool) []string {
	ids := make([]string, 0, min(len(entities), maxEntities))
	for id, e := range entities {
		if !e.disabled && !e.unreadable && withState(id) {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return ids
}

// parseAliases adds the aliases of config/entity_registry/get_entries (entity
// id -> entry or null) to entities.
func parseAliases(raw json.RawMessage, entities map[string]regEntry) error {
	var byID map[string]json.RawMessage
	if json.Unmarshal(raw, &byID) != nil {
		return fail(CodeBadResponse)
	}
	for id, item := range byID {
		e, known := entities[id]
		if !known || isNull(item) {
			continue
		}
		var r struct {
			Aliases json.RawMessage `json:"aliases"`
		}
		if json.Unmarshal(item, &r) != nil {
			continue
		}
		if al, ok := aliases(r.Aliases); ok {
			e.aliases = al
			entities[id] = e
		}
	}
	return nil
}

// derive builds the entity of state s from the registries; ok is false for
// an entity the registry has disabled.
func (r *registries) derive(s entityState) (Entity, bool) {
	e := Entity{ID: s.id, Domain: domainOf(s.id), State: s.state, Attrs: s.attrs}
	reg, known := r.entities[s.id]
	if known && reg.disabled {
		return Entity{}, false
	}
	e.Name = firstNonEmpty(reg.name, s.friendly, reg.originalName, s.id)
	e.Platform = reg.platform
	e.PlatformUnknown = reg.unreadable || !known && r.entitiesBehind
	e.EntryUnreadable = reg.unreadable
	e.Area = reg.area
	if e.Area == "" && reg.device != "" {
		e.Area = r.devices[reg.device]
	}
	e.Aliases = reg.aliases
	return e, true
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}
