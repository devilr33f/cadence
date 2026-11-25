package telemetry

import (
	"context"
	"errors"
	"time"

	"github.com/gofiber/fiber/v3"
	"google.golang.org/protobuf/proto"

	coltracev1 "git.shino.boo/devilreef/cadence/pkg/gen/otlp/collector/trace/v1"
	"git.shino.boo/devilreef/cadence/server/internal/tracing"
)

// TraceSink takes a device's decoded-on-arrival trace request; the tracing
// provider is one.
type TraceSink interface {
	Ingest(dev tracing.DeviceRef, body []byte, scrub func(string) string, received time.Time) (tracing.IngestResult, error)
}

const partialTracesError = "spans malformed, past the request's cap, out of clock or over a full trace"

// traces stores a device's spans, stamped with the device, its secrets
// scrubbed from every string as its log bodies are.
func (in *Ingest) traces(ctx context.Context, id, token string, body []byte, received time.Time) outcome {
	scrub, err := in.scrubber(ctx, id, token)
	if err != nil {
		return failed(fiber.StatusServiceUnavailable, "secrets")
	}
	dev := tracing.DeviceRef{ID: id}
	if in.d.Device != nil {
		// a failed lookup stamps the id alone: the spans are not worth a retry
		if d, err := in.d.Device(ctx, id); err == nil {
			dev.Name, dev.Board = d.Name, d.Board
		}
	}
	res, err := in.d.Traces.Ingest(dev, body, scrub.Scrub, received)
	if errors.Is(err, tracing.ErrDecode) {
		return failed(fiber.StatusBadRequest, "decode")
	}
	if err != nil {
		return failed(fiber.StatusServiceUnavailable, "store")
	}
	resp := &coltracev1.ExportTraceServiceResponse{}
	if res.Rejected > 0 {
		resp.PartialSuccess = &coltracev1.ExportTracePartialSuccess{RejectedSpans: int64(res.Rejected), ErrorMessage: partialTracesError}
	}
	out, err := proto.Marshal(resp)
	if err != nil {
		return failed(fiber.StatusInternalServerError, "encode")
	}
	return outcome{status: fiber.StatusOK, stored: res.Stored, rejected: res.Rejected, response: out}
}

// TraceStats are the trace ingest's counts since the server started.
func (in *Ingest) TraceStats() SignalStats { return in.counts[kindTraces].stats() }
