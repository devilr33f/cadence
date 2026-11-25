package logs

import "maps"

// levels is the minimum level per source, def for any other; it is replaced
// whole, never changed.
type levels struct {
	bySource map[string]Severity
	def      Severity
}

func newLevels(bySource map[string]Severity, def Severity) *levels {
	l := &levels{bySource: maps.Clone(bySource), def: def.level()}
	for s, sev := range l.bySource {
		l.bySource[s] = sev.level()
	}
	return l
}

func (l *levels) keeps(r Record) bool {
	floor, ok := l.bySource[r.Source]
	if !ok {
		floor = l.def
	}
	return r.Severity.level() >= floor
}
