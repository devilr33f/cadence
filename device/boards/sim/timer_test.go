package sim_test

import (
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/boards/sim"
)

func TestTimerLimitFromEnv(t *testing.T) {
	env := map[string]string{}
	getenv := func(k string) string { return env[k] }
	for _, v := range []string{"", "0", "61", "5s"} {
		env["CADENCE_SIM_TIMER_SECONDS"] = v
		if d, ok := sim.TimerLimitFromEnv(getenv); ok {
			t.Fatalf("%q: a timer limit of %v", v, d)
		}
	}
	for v, want := range map[string]time.Duration{"1": time.Second, "60": time.Minute} {
		env["CADENCE_SIM_TIMER_SECONDS"] = v
		if d, ok := sim.TimerLimitFromEnv(getenv); !ok || d != want {
			t.Fatalf("%q: got %v %v, want %v", v, d, ok, want)
		}
	}
}
