package providers

import (
	"cmp"
	"maps"
	"slices"
)

// the fake package imports this one, so it registers itself here
var devFakeModules func() []Module

// SetDevFakes is called from the fake package's init.
func SetDevFakes(f func() []Module) { devFakeModules = f }

type Registry struct{ modules map[string]Module }

// NewRegistry adds the fake modules only with devFakes.
func NewRegistry(devFakes bool, modules ...Module) *Registry {
	r := &Registry{modules: map[string]Module{}}
	for _, m := range modules {
		r.modules[m.ID] = m
	}
	if devFakes && devFakeModules != nil {
		for _, m := range devFakeModules() {
			r.modules[m.ID] = m
		}
	}
	return r
}

func (r *Registry) Module(id string) (Module, bool) {
	m, ok := r.modules[id]
	return m, ok
}

// Modules are every registered module by kind, then id.
func (r *Registry) Modules() []Module {
	return slices.SortedFunc(maps.Values(r.modules), func(a, b Module) int {
		return cmp.Or(cmp.Compare(a.Kind, b.Kind), cmp.Compare(a.ID, b.ID))
	})
}
