package telemetry

import (
	"context"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5/pgtype"
	"google.golang.org/protobuf/proto"

	collogsv1 "git.shino.boo/devilreef/cadence/pkg/gen/otlp/collector/logs/v1"
)

// the device's own name for its token in its scrubber
const tokenSecret = "device_token"

// pendingLog is a valid record before its body is scrubbed; body points into the request
type pendingLog struct {
	time      time.Time
	severity  int16
	source    string
	body      []byte
	truncated bool
	continued bool
}

func (in *Ingest) logs(ctx context.Context, device pgtype.UUID, id, token string, body []byte, received time.Time) outcome {
	var (
		pending  []pendingLog
		rejected int
	)
	err := eachLogRecord(body, func(r *logRecord) {
		if !r.valid() || len(pending) == maxLogRecords {
			rejected++
			return
		}
		pending = append(pending, pendingLog{
			time: recordTime(r.time, r.observed, received, logSkew), severity: int16(r.severity), //nolint:gosec // at most maxSeverity
			source: string(r.source.s), body: r.body.s, truncated: r.truncated, continued: r.continued,
		})
	})
	if err != nil {
		return failed(fiber.StatusBadRequest, "decode")
	}
	if len(pending) > 0 {
		scrub, err := in.scrubber(ctx, id, token)
		if err != nil {
			// a body the server cannot scrub is never stored: the device sends it again
			return failed(fiber.StatusServiceUnavailable, "secrets")
		}
		rows := make([]LogRow, len(pending))
		for i, p := range pending {
			kept, cut := keepBody(scrub, string(p.body), p.truncated, p.continued)
			rows[i] = LogRow{Time: p.time, Observed: received, DeviceID: device, Source: p.source, Severity: p.severity,
				Body: kept, Truncated: cut, Continued: p.continued}
		}
		if err := in.d.Logs.Insert(ctx, rows); err != nil {
			return failed(fiber.StatusServiceUnavailable, "store")
		}
	}
	resp := &collogsv1.ExportLogsServiceResponse{}
	if rejected > 0 {
		resp.PartialSuccess = &collogsv1.ExportLogsPartialSuccess{RejectedLogRecords: int64(rejected), ErrorMessage: partialLogsError}
	}
	out, err := proto.Marshal(resp)
	if err != nil {
		return failed(fiber.StatusInternalServerError, "encode")
	}
	return outcome{status: fiber.StatusOK, stored: len(pending), rejected: rejected, response: out}
}

// scrubber knows the request's own bearer token and the device's capability
// secrets: the server stores only the token's hash, so the request is where
// it has it.
func (in *Ingest) scrubber(ctx context.Context, id, token string) (*Scrubber, error) {
	secrets, err := in.d.Secrets(ctx, id)
	if err != nil {
		return nil, err
	}
	s := &Scrubber{}
	s.Set(tokenSecret, token)
	for name, v := range secrets {
		s.Set(name, v)
	}
	return s, nil
}
