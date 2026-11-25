package audiod

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/telemetry/logs"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
)

// longer lines are never logged locally: libasound and other third-party text
// can land on audiod's stderr
const maxLogLine = 1024

// the line audiod's own source names in a LogLine
const audiodSource = "audiod"

var logLevels = map[string]slog.Level{
	"DEBUG": slog.LevelDebug, "INFO": slog.LevelInfo, "WARN": slog.LevelWarn, "ERROR": slog.LevelError,
}

var wireLevels = map[slog.Level]audiodv1.LogLevel{
	slog.LevelDebug: audiodv1.LogLevel_LOG_LEVEL_DEBUG, slog.LevelInfo: audiodv1.LogLevel_LOG_LEVEL_INFO,
	slog.LevelWarn: audiodv1.LogLevel_LOG_LEVEL_WARN, slog.LevelError: audiodv1.LogLevel_LOG_LEVEL_ERROR,
}

// keys the log handler writes itself; an audiod field with one of them is
// renamed, as its volume lines carry a numeric level
var reservedKeys = map[string]bool{"time": true, "level": true, "msg": true, "source": true}

// LogsHandler is cadenced's side of audiod's lines in the log pipeline.
// LogsReady runs after each Ready, before MediaReady, with send bound to that
// connection, so audiod learns at once whether its children's lines ship;
// LogsGone follows it once the connection ended. LogLine gets each line audiod
// wrote to stderr (source audiod, its level, else info; cut past the
// pipeline's cap) and each LogLine it sent, its own or a child's, and must
// never block.
type LogsHandler interface {
	LogsReady(send func(*audiodv1.Envelope) error)
	LogsGone()
	LogLine(line *audiodv1.LogLine)
}

// WithLogs hands audiod's lines to h; audiod's own then go to local, the local
// log alone, instead of the supervisor's log, as they enter the pipeline as
// audiod's, never as cadenced's. Without it, each Ready turns the children's
// lines off.
func WithLogs(h LogsHandler, local *slog.Logger) SupervisorOption {
	return func(s *Supervisor) {
		s.logs = h
		if local != nil {
			s.lineLog = local
		}
	}
}

// forwardStderr routes each line audiod writes to stderr until EOF.
func (s *Supervisor) forwardStderr(r io.ReadCloser) {
	defer func() { _ = r.Close() }()
	br := bufio.NewReaderSize(r, logs.MaxLine+1)
	for {
		line, err := br.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			s.stderrLine(line[:logs.MaxLine], true)
			for errors.Is(err, bufio.ErrBufferFull) {
				_, err = br.ReadSlice('\n')
			}
			if err != nil {
				return
			}
			continue
		}
		if len(line) > 0 {
			// one stderr ended without its newline is cut: its end may start a secret
			s.stderrLine(line, line[len(line)-1] != '\n')
		}
		if err != nil {
			return
		}
	}
}

// stderrLine logs one of audiod's stderr lines locally, as before, and hands
// it to the pipeline whatever it is
func (s *Supervisor) stderrLine(raw []byte, cut bool) {
	raw = bytes.TrimRight(raw, "\r\n")
	level := audiodv1.LogLevel_LOG_LEVEL_INFO
	if cut {
		s.dropLine()
	} else if l, ok := s.localLine(raw); ok {
		level = wireLevels[l]
	}
	if s.logs != nil && len(raw) > 0 {
		s.logs.LogLine(&audiodv1.LogLine{Source: audiodSource, Level: level, Text: string(raw), Truncated: cut})
	}
}

// logLine routes a LogLine audiod sent: its own goes to the local log as its
// stderr lines do, a child's never does; both go to the pipeline
func (s *Supervisor) logLine(l *audiodv1.LogLine) {
	if l.GetSource() == audiodSource {
		s.localLine([]byte(l.GetText()))
	}
	if s.logs != nil {
		s.logs.LogLine(l)
	}
}

// localLine logs one audiod log line locally, a json object of strings and
// numbers with a level and a msg (the stderr format, which audiod's own
// LogLine text also uses), and reports its level. Any other line is counted,
// never logged; a longer one too, though its level is still read.
func (s *Supervisor) localLine(raw []byte) (slog.Level, bool) {
	raw = bytes.TrimRight(raw, "\r\n")
	level, msg, attrs, ok := parseLogLine(raw)
	if !ok || len(raw) > maxLogLine {
		s.dropLine()
		return level, ok
	}
	s.lineLog.LogAttrs(context.Background(), level, msg, attrs...)
	return level, true
}

func parseLogLine(raw []byte) (slog.Level, string, []slog.Attr, bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return 0, "", nil, false
	}
	var level, msg string
	haveLevel, haveMsg := false, false
	attrs := []slog.Attr{slog.String("source", audiodSource)}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return 0, "", nil, false
		}
		key, _ := keyTok.(string)
		val, err := dec.Token()
		if err != nil {
			return 0, "", nil, false
		}
		switch v := val.(type) {
		case string:
			switch {
			case key == "level" && !haveLevel:
				level, haveLevel = v, true
			case key == "msg" && !haveMsg:
				msg, haveMsg = v, true
			default:
				attrs = append(attrs, slog.String(fieldKey(key), v))
			}
		case json.Number:
			if i, err := v.Int64(); err == nil {
				attrs = append(attrs, slog.Int64(fieldKey(key), i))
			} else if f, err := v.Float64(); err == nil {
				attrs = append(attrs, slog.Float64(fieldKey(key), f))
			} else {
				return 0, "", nil, false
			}
		default:
			return 0, "", nil, false
		}
	}
	if tok, err := dec.Token(); err != nil || tok != json.Delim('}') {
		return 0, "", nil, false
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return 0, "", nil, false
	}
	l, ok := logLevels[level]
	if !ok || msg == "" {
		return 0, "", nil, false
	}
	return l, msg, attrs, true
}

func fieldKey(k string) string {
	if reservedKeys[k] {
		return "audiod_" + k
	}
	return k
}

// dropLine counts a line that was not logged locally; the count goes out at
// once, then at most once per LogDropEvery.
func (s *Supervisor) dropLine() {
	s.dropMu.Lock()
	defer s.dropMu.Unlock()
	s.dropped++
	s.reportDropsLocked(false)
}

func (s *Supervisor) reportDrops(force bool) {
	s.dropMu.Lock()
	defer s.dropMu.Unlock()
	s.reportDropsLocked(force)
}

func (s *Supervisor) reportDropsLocked(force bool) {
	if s.dropped == 0 {
		return
	}
	if !force && !s.dropReported.IsZero() && time.Since(s.dropReported) < s.t.LogDropEvery {
		return
	}
	s.log.Warn("audiod_log_dropped", "count", s.dropped)
	s.dropped = 0
	s.dropReported = time.Now()
}
