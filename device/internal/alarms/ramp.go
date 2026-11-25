package alarms

import "time"

const (
	// RampDuration is the default length of an alarm's ramp from the level in force to its own (v1)
	RampDuration = 30 * time.Second
	// RampStep is how far one step of it moves the level, however long the ramp
	RampStep = 5
)

// RampLevel is where an alarm's ramp of length ramp stands elapsed after its ring began: from start towards level in
// RampStep steps, each once its points are earned, level itself from ramp on (at once for a ramp of 0). Never below
// start: an alarm set quieter than where it starts rings at that level (rule 7 as amended). Both are clamped to 0-100.
func RampLevel(start, level int, elapsed, ramp time.Duration) int {
	start, level = clampLevel(start), clampLevel(level)
	if level <= start {
		return start
	}
	if elapsed >= ramp {
		return level
	}
	if elapsed <= 0 {
		return start
	}
	earned := int(int64(level-start) * elapsed.Milliseconds() / ramp.Milliseconds())
	return start + earned/RampStep*RampStep
}

// RampNextStep is how long after elapsed the ramp moves next; false once it stands at level.
func RampNextStep(start, level int, elapsed, ramp time.Duration) (time.Duration, bool) {
	start, level = clampLevel(start), clampLevel(level)
	now := RampLevel(start, level, elapsed, ramp)
	if now >= level {
		return 0, false
	}
	next := ramp
	if points := now - start + RampStep; start+points < level {
		span := int64(level - start)
		// the first millisecond at which the points are earned
		ms := (int64(points)*ramp.Milliseconds() + span - 1) / span
		next = time.Duration(ms) * time.Millisecond
	}
	return next - max(elapsed, 0), true
}

func clampLevel(level int) int { return min(max(level, 0), MaxLevel) }
