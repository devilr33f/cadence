package telemetry

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"git.shino.boo/devilreef/cadence/server/internal/store/db"
)

// ErrUnknownDevice is a device id no device has.
var ErrUnknownDevice = errors.New("telemetry: unknown device")

// DeviceInfo is what stamps a device's telemetry: its board is its model.
type DeviceInfo struct{ ID, Name, Board string }

// readTimeout bounds one admin read on the server too, should its client be gone
const readTimeout = "10s"

// errReadTimeout is a read the server cut at its statement timeout
var errReadTimeout = errors.New("telemetry: read timed out")

// Reader answers the admin API, Prometheus and the export from the store.
type Reader struct {
	pool *pgxpool.Pool
	q    *db.Queries
}

func NewReader(pool *pgxpool.Pool) *Reader { return &Reader{pool: pool, q: db.New(pool)} }

// readOnly runs fn in a read-only transaction whose statements the server cuts
// at readTimeout and plans for their own arguments: a generic plan of a
// query with optional filters would scan for the filters it was not given.
func (r *Reader) readOnly(ctx context.Context, op string, fn func(q *db.Queries) error) error {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return storeError(op, err)
	}
	defer func() {
		// a read cut at its deadline still ends its transaction, and a rollback postgres never answers holds it a second
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()
		_ = tx.Rollback(ctx)
	}()
	if _, err := tx.Exec(ctx, "SET LOCAL plan_cache_mode = force_custom_plan"); err != nil {
		return storeError(op, err)
	}
	if _, err := tx.Exec(ctx, "SET LOCAL statement_timeout = '"+readTimeout+"'"); err != nil {
		return storeError(op, err)
	}
	return storeError(op, fn(db.New(tx)))
}

// LogQuery selects stored lines, newest first.
type LogQuery struct {
	// Device not valid: any device and the server
	Device      pgtype.UUID
	Source      string
	MinSeverity int16
	// Since is inclusive, Until exclusive
	Since, Until time.Time
	// Contains is a case-insensitive substring of the body; "" is any
	Contains string
	Limit    int
}

// LogView is one stored line as the admin API shows it; the device fields
// are empty on the server's own lines.
type LogView struct {
	Time       time.Time `json:"ts"`
	Observed   time.Time `json:"observedTs"`
	DeviceID   string    `json:"deviceId,omitempty"`
	DeviceName string    `json:"deviceName,omitempty"`
	Board      string    `json:"board,omitempty"`
	Source     string    `json:"source"`
	Severity   int16     `json:"severity"`
	Body       string    `json:"body"`
	Truncated  bool      `json:"truncated"`
	Continued  bool      `json:"continued"`
}

func (r *Reader) Logs(ctx context.Context, lq LogQuery) ([]LogView, error) {
	p := db.ListDeviceLogsParams{
		Since: pgtype.Timestamptz{Time: lq.Since, Valid: true}, Until: pgtype.Timestamptz{Time: lq.Until, Valid: true},
		MinSeverity: lq.MinSeverity, DeviceID: lq.Device, MaxRows: int32(lq.Limit), //nolint:gosec // the API caps it at 1000
		Source:  pgtype.Text{String: lq.Source, Valid: lq.Source != ""},
		Pattern: pgtype.Text{String: "%" + likeEscape(lq.Contains) + "%", Valid: lq.Contains != ""},
	}
	var rows []db.ListDeviceLogsRow
	err := r.readOnly(ctx, "list logs", func(q *db.Queries) (err error) {
		rows, err = q.ListDeviceLogs(ctx, p)
		return err
	})
	if err != nil {
		return nil, err
	}
	out := make([]LogView, len(rows))
	for i, row := range rows {
		out[i] = LogView{Time: row.Ts.Time.UTC(), Observed: row.ObservedTs.Time.UTC(), Source: row.Source, Severity: row.Severity,
			Body: row.Body, Truncated: row.Truncated, Continued: row.Continued}
		if row.DeviceID.Valid {
			out[i].DeviceID, out[i].DeviceName, out[i].Board = row.DeviceID.String(), row.DeviceName.String, row.Board.String
		}
	}
	return out, nil
}

// likeEscape makes s match itself in a LIKE pattern, whose escape is backslash
var likeEscape = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace

// Device is the device of id, ErrUnknownDevice for none (or no uuid).
func (r *Reader) Device(ctx context.Context, id string) (DeviceInfo, error) {
	u, err := parseDevice(id)
	if err != nil {
		return DeviceInfo{}, ErrUnknownDevice
	}
	row, err := r.q.DeviceByID(ctx, u)
	if errors.Is(err, pgx.ErrNoRows) {
		return DeviceInfo{}, ErrUnknownDevice
	}
	if err != nil {
		return DeviceInfo{}, storeError("device", err)
	}
	return DeviceInfo{ID: row.ID.String(), Name: row.Name, Board: row.Model}, nil
}

// Devices are every device, to stamp what is exported.
func (r *Reader) Devices(ctx context.Context) ([]DeviceInfo, error) {
	rows, err := r.q.ListDevices(ctx)
	if err != nil {
		return nil, storeError("devices", err)
	}
	out := make([]DeviceInfo, len(rows))
	for i, row := range rows {
		out[i] = DeviceInfo{ID: row.ID.String(), Name: row.Name, Board: row.Model}
	}
	return out, nil
}

// Series is the latest point of one stored series.
type Series struct {
	DeviceID, DeviceName, Board, Name string
	Kind                              int16
	// Attrs is the point's attributes, a JSON object
	Attrs []byte
	Time  time.Time
	Value float64
}

// Latest is the latest point of each series with one since since, at most
// perDevice series of each device, by name and attributes.
func (r *Reader) Latest(ctx context.Context, since time.Time, perDevice int) ([]Series, error) {
	rows, err := r.q.LatestMetricSeries(ctx, db.LatestMetricSeriesParams{
		Since: pgtype.Timestamptz{Time: since, Valid: true}, PerDevice: int64(perDevice),
	})
	if err != nil {
		return nil, storeError("latest metrics", err)
	}
	out := make([]Series, len(rows))
	for i, row := range rows {
		out[i] = Series{DeviceID: row.DeviceID.String(), DeviceName: row.DeviceName, Board: row.Board, Name: row.Name,
			Kind: row.Kind, Attrs: row.Attrs, Time: row.Ts.Time, Value: row.Value}
	}
	return out, nil
}

// Point is one stored point of a device; Start is zero for none.
type Point struct {
	Name        string
	Kind        int16
	Attrs       []byte
	Time, Start time.Time
	Value       float64
}

// Points are the device's points of names since since, by name, attributes
// and time, at most limit.
func (r *Reader) Points(ctx context.Context, device pgtype.UUID, names []string, since time.Time, limit int) ([]Point, error) {
	var rows []db.DeviceMetricPointsRow
	err := r.readOnly(ctx, "metric points", func(q *db.Queries) (err error) {
		rows, err = q.DeviceMetricPoints(ctx, db.DeviceMetricPointsParams{
			DeviceID: device, Names: names, Since: pgtype.Timestamptz{Time: since, Valid: true},
			MaxRows: int32(limit), //nolint:gosec // a constant of the API
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	out := make([]Point, len(rows))
	for i, row := range rows {
		out[i] = Point{Name: row.Name, Kind: row.Kind, Attrs: row.Attrs, Time: row.Ts.Time, Value: row.Value}
		if row.StartTs.Valid {
			out[i].Start = row.StartTs.Time
		}
	}
	return out, nil
}
