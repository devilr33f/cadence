package agent

import (
	"log/slog"
	"time"
)

// Snapshot takes the tools a turn would begin with, logging skips to log.
func Snapshot(r *Registry, log *slog.Logger) []Tool { return r.snapshot(log) }

// SetLimits shortens the runner's time bounds for tests.
func SetLimits(r *Runner, firstChunk, reserve, forced time.Duration) {
	r.lim.firstChunk, r.lim.reserve, r.lim.forced = firstChunk, reserve, forced
}

// SetHistoryBound shortens the bound of the history read for tests.
func SetHistoryBound(r *Runner, d time.Duration) { r.lim.history = d }

// SetRecallBound shortens the bound of the memory recall for tests.
func SetRecallBound(r *Runner, d time.Duration) { r.lim.recall = d }

// SetTasksBound shortens the bound of the task read for tests.
func SetTasksBound(r *Runner, d time.Duration) { r.lim.tasks = d }

// SetHomeBound shortens the bound of the home note for tests.
func SetHomeBound(r *Runner, d time.Duration) { r.lim.home = d }

// TriggerNames are the triggers as the history stores them and the live feed
// publishes them.
var TriggerNames = triggerNames

// DevicesNote is the devices note of all as self's turn reads it.
func DevicesNote(all []DeviceInfo, self string) string { return devicesNote(split(all, self)) }
