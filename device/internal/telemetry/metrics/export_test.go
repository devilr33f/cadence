package metrics

// SetRand replaces where the exporter draws its stretch of a server's wait, in [0, 1).
func SetRand(e *Exporter, rnd func() float64) { e.rnd = rnd }
