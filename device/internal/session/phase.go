package session

// Phase is what the session is doing, for the lights.
type Phase uint8

const (
	PhaseIdle Phase = iota
	PhaseListening
	PhaseThinking
	PhaseSpeaking
)

func (p Phase) String() string {
	switch p {
	case PhaseListening:
		return "listening"
	case PhaseThinking:
		return "thinking"
	case PhaseSpeaking:
		return "speaking"
	}
	return "idle"
}

// WithPhases hands each change of phase to f, on the controller's goroutine; f must not block.
func WithPhases(f func(Phase)) Option { return func(c *Controller) { c.phases = f } }

func (c *Controller) setPhase(p Phase) {
	if p == c.phase {
		return
	}
	c.phase = p
	if c.phases != nil {
		c.phases(p)
	}
}
