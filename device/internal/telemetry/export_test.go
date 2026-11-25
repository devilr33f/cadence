package telemetry

import (
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/telemetry/sources"
)

// SetKmsgOpen replaces how the logs open the kernel log; before Run.
func SetKmsgOpen(l *Logs, open func() (sources.KmsgFile, error)) { l.openKmsg = open }

// SetStateEvery replaces the 30 s between two states; before Run.
func SetStateEvery(l *Logs, d time.Duration) { l.stateEvery = d }

// SetMetricsUnit counts intervalS in d instead of seconds; before Run.
func SetMetricsUnit(m *Metrics, d time.Duration) { m.unit = d }
