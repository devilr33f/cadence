package core

import (
	"maps"
	"slices"
	"sync"
)

// a value no longer in force stays redacted, the latest this many per name
const maxKeptValues = 4

// Secrets learns every capability secret before its provider is handed it,
// named <capability>.<secret name>, so the log pipeline can redact it.
type Secrets interface {
	Set(name string, values ...string)
	Remove(name string)
}

// WithSecrets tells secrets of every capability secret a config delivers.
func WithSecrets(secrets Secrets) SyncOption {
	return func(s *Sync) { s.secrets.sink = secrets }
}

// knownSecrets is what the sink was told per capability: the values of the
// config in force, and those kept since they left it: a refused config's,
// which the provider may have taken before it refused, and a rotated or
// deleted one's, which a child started with it may log until it ends.
type knownSecrets struct {
	sink Secrets

	mu      sync.Mutex
	inForce map[string]map[string]string
	kept    map[string]map[string][]string
	told    map[string]map[string]bool
}

// offer tells the sink of secrets before capability id's provider is handed
// them, beside those already told.
func (k *knownSecrets) offer(id string, secrets map[string]string) {
	if k.sink == nil {
		return
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	k.tell(id, k.values(id, secrets))
}

// settle puts secrets in force once the provider took them, keeping the
// values they replace or delete; a refused config's are kept beside those in
// force.
func (k *knownSecrets) settle(id string, secrets map[string]string, applied bool) {
	if k.sink == nil {
		return
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.inForce == nil {
		k.inForce, k.kept = map[string]map[string]string{}, map[string]map[string][]string{}
	}
	if !applied {
		for name, v := range secrets {
			k.keep(id, name, v)
		}
		k.tell(id, k.values(id, nil))
		return
	}
	for name, v := range k.inForce[id] {
		if now, ok := secrets[name]; !ok || now != v {
			k.keep(id, name, v)
		}
	}
	for name, v := range secrets {
		if vs := k.kept[id][name]; len(vs) > 0 {
			k.kept[id][name] = slices.DeleteFunc(vs, func(o string) bool { return o == v })
		}
	}
	k.inForce[id] = maps.Clone(secrets)
	k.tell(id, k.values(id, nil))
}

// keep adds v to name's kept values of id, the latest maxKeptValues; runs under mu.
func (k *knownSecrets) keep(id, name, v string) {
	r := k.kept[id]
	if r == nil {
		r = map[string][]string{}
		k.kept[id] = r
	}
	vs := slices.DeleteFunc(r[name], func(o string) bool { return o == v })
	r[name] = append(vs, v)[max(0, len(vs)+1-maxKeptValues):]
}

// values is, per name, what id holds in force, then its kept values, then extra's; runs under mu.
func (k *knownSecrets) values(id string, extra map[string]string) map[string][]string {
	out := map[string][]string{}
	add := func(name, v string) {
		if !slices.Contains(out[name], v) {
			out[name] = append(out[name], v)
		}
	}
	for name, v := range k.inForce[id] {
		add(name, v)
	}
	for name, vs := range k.kept[id] {
		for _, v := range vs {
			add(name, v)
		}
	}
	for name, v := range extra {
		add(name, v)
	}
	return out
}

// tell makes values what the sink knows of id; runs under mu.
func (k *knownSecrets) tell(id string, values map[string][]string) {
	if k.told == nil {
		k.told = map[string]map[string]bool{}
	}
	for name := range k.told[id] {
		if _, ok := values[name]; !ok {
			k.sink.Remove(id + "." + name)
		}
	}
	told := make(map[string]bool, len(values))
	for name, vs := range values {
		k.sink.Set(id+"."+name, vs...)
		told[name] = true
	}
	k.told[id] = told
}
