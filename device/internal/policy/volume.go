package policy

// VolumeFacts are what the device knows when it would put the level in force back on the hardware (M6a-D8).
type VolumeFacts struct {
	// the level in force is the device's own: loaded from its store or set since, not the default
	Known bool
	// an alarm or a timer rings: an alarm's ramp holds the hardware above the level in force
	Ringing bool
}

// VolumeReassert says whether the level in force goes back on the hardware: never a default the device does not own,
// and never over a ring's ramp, which its own stop restores.
func VolumeReassert(f VolumeFacts) bool { return f.Known && !f.Ringing }
