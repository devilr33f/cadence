package alarms

import "time"

// Begin is what Run does before its loop; Step is one wake of it at now; Shutdown is its end.
func Begin(r *Ringer, hooks Hooks)                { r.begin(hooks) }
func Step(r *Ringer, now time.Time) time.Duration { return r.step(now) }
func Shutdown(r *Ringer)                          { r.shutdown() }
