// Package deviceops runs device updates: it stores build artifacts, resolves a plan of fetch and
// exec steps for a device (opsplan), and executes it over one of three transports — the device
// link (the daemon runs the plan itself), adb through a sidecar server, or ssh to the board's
// host. Runs are the owner's alone (rule 7): the admin api behind the owner's session or an admin
// token, never an agent tool. Logs carry ids, codes and timings (rule 8); a step's output tail is
// kept in the run's record and the device's log store, both the dashboard's alone.
package deviceops

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/server/internal/events"
	"git.shino.boo/devilreef/cadence/server/internal/secrets"
	"git.shino.boo/devilreef/cadence/server/internal/store/db"
	"git.shino.boo/devilreef/cadence/server/internal/telemetry"
)

// Feed events (the ops topic).
const (
	EventRunStarted = "ops_run_started"
	EventRunStep    = "ops_step"
	EventRunEnded   = "ops_run_ended"
)

// Run statuses.
const (
	StatusRunning         = "running"
	StatusAwaitingRestart = "awaiting_restart"
	StatusSucceeded       = "succeeded"
	StatusFailed          = "failed"
	StatusRolledBack      = "rolled_back"
	StatusNeedsAttention  = "needs_attention"
)

// maxArtifact is the largest artifact accepted (the midi tarball is 55 MiB, with room to grow).
const maxArtifact = 512 << 20

// keepPerBoard is how many artifacts a board keeps; older ones go unless they are a device's last
// successful install.
const keepPerBoard = 3

// runTimeout caps a whole server-driven run, restartWatch a device that has not relinked on its
// target version yet, and stepIdleTimeout a self-run gone quiet mid-plan.
const (
	runTimeout      = 20 * time.Minute
	restartWatch    = 10 * time.Minute
	stepIdleTimeout = 5 * time.Minute
	execDefaultWait = 5 * time.Minute
)

var (
	ErrUnknownArtifact  = errors.New("deviceops: unknown artifact")
	ErrUnknownDevice    = errors.New("deviceops: unknown device")
	ErrUnknownRun       = errors.New("deviceops: unknown run")
	ErrRunActive        = errors.New("deviceops: a run is already active")
	ErrBoardMismatch    = errors.New("deviceops: artifact board differs from the device's")
	ErrNoTransport      = errors.New("deviceops: no transport reaches the device")
	ErrTransportRefused = errors.New("deviceops: transport not allowed for the board")
	ErrBadArtifact      = errors.New("deviceops: bad artifact")
	ErrBadAccess        = errors.New("deviceops: bad access config")
)

// LinkSender is the gateway hub's half the link transport needs.
type LinkSender interface {
	Send(deviceID string, env *cadencev2.Envelope) error
	Online(deviceID string) bool
}

// LogInserter is the telemetry log store: run lines land in the device's Logs page beside the
// daemon's own.
type LogInserter interface {
	Insert(ctx context.Context, rows []telemetry.LogRow) error
}

// Service owns artifacts, access rows and runs.
type Service struct {
	q       queries
	logs    LogInserter
	bus     events.Publisher
	link    LinkSender
	box     *secrets.Box
	adb     string // the adb server's host:port (the sidecar); "" disables the adb transport
	devices DeviceLookup
	now     func() time.Time
	log     *slog.Logger

	mu       sync.Mutex
	hellos   map[string]hello // deviceID -> last hello the link carried
	activity map[string]time.Time
}

// queries is the store half the service uses; *db.Queries satisfies it.
type queries interface {
	PutDeviceArtifact(ctx context.Context, arg db.PutDeviceArtifactParams) (pgtype.UUID, error)
	ListDeviceArtifacts(ctx context.Context, board string) ([]db.ListDeviceArtifactsRow, error)
	GetDeviceArtifact(ctx context.Context, id pgtype.UUID) (db.DeviceArtifact, error)
	DeleteDeviceArtifact(ctx context.Context, id pgtype.UUID) (int64, error)
	PutOpsRun(ctx context.Context, arg db.PutOpsRunParams) (db.PutOpsRunRow, error)
	GetOpsRun(ctx context.Context, id pgtype.UUID) (db.OpsRun, error)
	ListOpsRuns(ctx context.Context, deviceID pgtype.UUID) ([]db.ListOpsRunsRow, error)
	ActiveOpsRun(ctx context.Context) ([]db.ActiveOpsRunRow, error)
	SetOpsRunStatus(ctx context.Context, arg db.SetOpsRunStatusParams) error
	FinishOpsRun(ctx context.Context, arg db.FinishOpsRunParams) error
	SetOpsRunSteps(ctx context.Context, arg db.SetOpsRunStepsParams) error
	InterruptStaleOpsRuns(ctx context.Context, arg db.InterruptStaleOpsRunsParams) (int64, error)
	SetDeviceDaemonVersion(ctx context.Context, arg db.SetDeviceDaemonVersionParams) error
	PutDeviceAccess(ctx context.Context, arg db.PutDeviceAccessParams) error
	TouchDeviceAccess(ctx context.Context, arg db.TouchDeviceAccessParams) error
	DeleteDeviceAccess(ctx context.Context, arg db.DeleteDeviceAccessParams) (int64, error)
	ListDeviceAccess(ctx context.Context, deviceID pgtype.UUID) ([]db.ListDeviceAccessRow, error)
	GetDeviceAccessKey(ctx context.Context, deviceID pgtype.UUID) (string, error)
	SetDeviceAccessHostKey(ctx context.Context, arg db.SetDeviceAccessHostKeyParams) error
}

type hello struct {
	protocol uint32
	version  string
}

// Deps wires the service.
type Deps struct {
	Q       queries
	Logs    LogInserter
	Bus     events.Publisher
	Link    LinkSender
	Box     *secrets.Box
	Devices DeviceLookup
	// AdbServer is the adb server sidecar's host:port (CADENCE_ADB_SERVER); empty turns the adb
	// transport off
	AdbServer string
	Log       *slog.Logger
}

// New builds the service.
func New(d Deps) *Service {
	return &Service{
		q: d.Q, logs: d.Logs, bus: d.Bus, link: d.Link, box: d.Box, adb: d.AdbServer, devices: d.Devices,
		now: time.Now, log: d.Log,
		hellos: map[string]hello{}, activity: map[string]time.Time{},
	}
}

// Run keeps the runs honest: a server-driven run whose executor died with a server restart is
// failed, an awaited relink that never came needs the owner, a self-run gone quiet failed.
func (s *Service) Run(ctx context.Context) func() {
	stop := make(chan struct{})
	go s.watch(context.WithoutCancel(ctx), stop)
	return func() { close(stop) }
}

func (s *Service) watch(ctx context.Context, stop <-chan struct{}) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			s.tick(ctx)
		}
	}
}

func (s *Service) tick(ctx context.Context) {
	now := s.now()
	if n, err := s.q.InterruptStaleOpsRuns(ctx, db.InterruptStaleOpsRunsParams{
		Now: ts(now), Before: ts(now.Add(-runTimeout)),
	}); err != nil {
		s.log.Error("ops_watch_failed", "reason", "interrupt")
	} else if n > 0 {
		s.log.Info("ops_runs_interrupted", "count", n)
	}
	runs, err := s.q.ActiveOpsRun(ctx)
	if err != nil {
		s.log.Error("ops_watch_failed", "reason", "store")
		return
	}
	for _, r := range runs {
		last := s.lastActive(r.ID.String())
		switch {
		case r.Status == StatusAwaitingRestart && now.Sub(last) > restartWatch:
			s.finish(ctx, r.ID, StatusNeedsAttention, "relink_timeout")
		case r.Status == StatusRunning && now.Sub(last) > stepIdleTimeout:
			s.finish(ctx, r.ID, StatusFailed, "idle_timeout")
		}
	}
}

func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

func uuid(s string) (pgtype.UUID, bool) {
	var id pgtype.UUID
	return id, id.Scan(s) == nil
}
