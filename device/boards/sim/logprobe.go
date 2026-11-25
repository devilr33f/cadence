package sim

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"git.shino.boo/devilreef/cadence/device/internal/telemetry/logs"
	"git.shino.boo/devilreef/cadence/device/internal/telemetry/sources"
)

const (
	probeSource = "sim-probe"
	// a bigger probe file is dropped unread
	maxProbe = 64 << 10
)

// logProbe is the e2e's way into the log pipeline: each line of <data>/logprobe ships as source sim-probe at info, as
// a third-party line would (scrubbed, cut, stored by the server), and never reaches a local log, so a line the e2e
// writes with a secret in it shows what the scrub leaves. Real boards never read the file.
type logProbe struct {
	path string
	sink sources.Emitter
	log  *slog.Logger
}

func newLogProbe(dataDir string, sink sources.Emitter, log *slog.Logger) *logProbe {
	return &logProbe{path: filepath.Join(dataDir, "logprobe"), sink: sink, log: log}
}

func (p *logProbe) poll(ctx context.Context) { pollEach(ctx, p.read) }

// read ships the probe file's lines and removes it. A file whose last line has no newline yet may be half written,
// so it stays for the next poll; empty lines are skipped.
func (p *logProbe) read() {
	f, err := os.Open(p.path)
	if err != nil {
		return
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxProbe+1))
	_ = f.Close()
	if err != nil {
		return
	}
	if len(raw) > maxProbe {
		if os.Remove(p.path) == nil {
			p.log.Warn("sim_logprobe_too_large")
		}
		return
	}
	if len(raw) == 0 || raw[len(raw)-1] != '\n' {
		return
	}
	if os.Remove(p.path) != nil {
		return
	}
	for line := range strings.SplitSeq(strings.TrimSuffix(string(raw), "\n"), "\n") {
		if line = strings.TrimSuffix(line, "\r"); line == "" {
			continue
		}
		body, cut := logs.CapLine(line)
		p.sink.Emit(logs.Record{Severity: logs.Info, Source: probeSource, Body: body, Truncated: cut})
	}
}
