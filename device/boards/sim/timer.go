package sim

import (
	"strconv"
	"time"
)

// TimerLimitFromEnv is how long the e2e's timers ring when nothing stops them: CADENCE_SIM_TIMER_SECONDS, 1 to 60, in
// place of a real board's minute, so the e2e sees one time out within seconds. Anything else keeps the minute.
func TimerLimitFromEnv(getenv func(string) string) (time.Duration, bool) {
	n, err := strconv.Atoi(getenv("CADENCE_SIM_TIMER_SECONDS"))
	if err != nil || n < 1 || n > 60 {
		return 0, false
	}
	return time.Duration(n) * time.Second, true
}

// WithTimerLimit rings the sim's timers for d when nothing stops them.
func WithTimerLimit(d time.Duration) Option { return func(s *sim) { s.timerLimit = d } }
