package audio

import "log/slog"

// Ends is whether t only ends what runs, the switch or a ring, and begins nothing.
func (t Trigger) Ends() bool { return t.Mode == Mute || t.Mode == Ring }

// Enqueue puts t on q without blocking, for the one goroutine at a time that sends on q. An activation that finds q
// full is dropped. The switch and a ring never are: the activations queued before them go, as one among them would
// begin a session they end, and so does an older one of their own mode, so they always find room. Each activation
// dropped is logged by kind.
func Enqueue(q chan Trigger, t Trigger, log *slog.Logger) {
	if t.Ends() {
		var kept []Trigger
		for drained := false; !drained; {
			select {
			case old := <-q:
				switch {
				case !old.Ends():
					log.Info("trigger_dropped", "trigger", old.Kind.String(), "reason", reason(t))
				case old.Mode != t.Mode:
					kept = append(kept, old)
				}
			default:
				drained = true
			}
		}
		for _, k := range kept {
			q <- k
		}
	}
	select {
	case q <- t:
	default:
		log.Warn("trigger_dropped", "trigger", t.Kind.String(), "reason", "full")
	}
}

func reason(t Trigger) string {
	if t.Mode == Ring {
		return "ringing"
	}
	return "muted"
}
