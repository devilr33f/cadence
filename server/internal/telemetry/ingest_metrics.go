package telemetry

import (
	"context"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5/pgtype"
	"google.golang.org/protobuf/proto"

	colmetricsv1 "git.shino.boo/devilreef/cadence/pkg/gen/otlp/collector/metrics/v1"
)

func (in *Ingest) metrics(ctx context.Context, device pgtype.UUID, body []byte, received time.Time) outcome {
	var (
		rows     []MetricRow
		rejected int
	)
	err := eachMetric(body, func(m *metric) error {
		kind, err := m.kind()
		if err != nil {
			return err
		}
		name := ""
		if kind != 0 {
			name = string(m.name)
		}
		return m.eachPoint(func(b []byte) error {
			if kind == 0 || len(rows) == maxPoints {
				rejected++
				return nil
			}
			p, valid, err := parsePoint(b)
			switch {
			case err != nil:
				return err
			case !valid:
				rejected++
			default:
				row := MetricRow{DeviceID: device, Name: name, Kind: kind, Attrs: p.attrs,
					Time: recordTime(p.time, 0, received, metricSkew), Value: p.value}
				if kind == KindSum {
					row.Start = startTime(p.start)
				}
				rows = append(rows, row)
			}
			return nil
		})
	})
	if err != nil {
		return failed(fiber.StatusBadRequest, "decode")
	}
	if err := in.d.Metrics.Insert(ctx, rows); err != nil {
		return failed(fiber.StatusServiceUnavailable, "store")
	}
	resp := &colmetricsv1.ExportMetricsServiceResponse{}
	if rejected > 0 {
		resp.PartialSuccess = &colmetricsv1.ExportMetricsPartialSuccess{RejectedDataPoints: int64(rejected), ErrorMessage: partialMetricErr}
	}
	out, err := proto.Marshal(resp)
	if err != nil {
		return failed(fiber.StatusInternalServerError, "encode")
	}
	return outcome{status: fiber.StatusOK, stored: len(rows), rejected: rejected, response: out}
}

// startTime is a sum's start as sent, kept however far from its point's time
// (the device's clock may have been set since): only unset, or past what a
// time holds, is none.
func startTime(n uint64) time.Time {
	if n == 0 || n > uint64(1<<63-1) {
		return time.Time{}
	}
	return time.Unix(0, int64(n)) //nolint:gosec // checked against the int64 range above
}
