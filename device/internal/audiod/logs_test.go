package audiod_test

import (
	"log/slog"
	"strings"
	"sync"
	"testing"

	"google.golang.org/protobuf/proto"

	"git.shino.boo/devilreef/cadence/device/internal/audiod"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
)

// logsHandler keeps audiod's lines and turns its children's off at each connect
type logsHandler struct {
	mu      sync.Mutex
	lines   []*audiodv1.LogLine
	readies int
	gones   int
}

func (h *logsHandler) LogsReady(send func(*audiodv1.Envelope) error) {
	h.mu.Lock()
	h.readies++
	h.mu.Unlock()
	_ = send(&audiodv1.Envelope{Body: &audiodv1.Envelope_LogsSet{LogsSet: &audiodv1.LogsSet{ThirdParty: false}}})
}

func (h *logsHandler) LogsGone() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.gones++
}

func (h *logsHandler) LogLine(l *audiodv1.LogLine) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.lines = append(h.lines, proto.Clone(l).(*audiodv1.LogLine))
}

func (h *logsHandler) find(fragment string) *audiodv1.LogLine {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, l := range h.lines {
		if strings.Contains(l.GetText(), fragment) {
			return l
		}
	}
	return nil
}

func (h *logsHandler) connections() (readies, gones int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.readies, h.gones
}

// audiod's own lines go to the local log as before and, with every other line it writes or sends, to the pipeline
// as audiod's; its children's lines go to the pipeline alone, under their own source, whatever they look like
func TestLogLinesRouted(t *testing.T) {
	h := &logsHandler{}
	local := &recorder{}
	hs := startSupervisorWith(t, 40, audiod.Timings{}, []audiod.SupervisorOption{audiod.WithLogs(h, slog.New(local))}, "--stderr")
	eventually(t, "every line handed on", func() bool {
		return h.find("fake_nested") != nil && h.find("SECRET_JSON_CHILD") != nil && h.find("fake_ipc_event") != nil
	})
	hs.waitRecord(t, 1, "logs_set", "false")
	hs.stop()

	for _, c := range []struct {
		fragment, source string
		level            audiodv1.LogLevel
		truncated        bool
	}{
		{"fake_event", "audiod", audiodv1.LogLevel_LOG_LEVEL_WARN, false},
		{"fake_volume", "audiod", audiodv1.LogLevel_LOG_LEVEL_INFO, false},
		{"libasound: SECRET_RAW", "audiod", audiodv1.LogLevel_LOG_LEVEL_INFO, false},
		{"fake_long", "audiod", audiodv1.LogLevel_LOG_LEVEL_INFO, false},
		{"fake_nested", "audiod", audiodv1.LogLevel_LOG_LEVEL_INFO, false},
		{"SECRET_IPC", "audiod", audiodv1.LogLevel_LOG_LEVEL_INFO, false},
		{"SECRET_CHILD track", "soloist", audiodv1.LogLevel_LOG_LEVEL_WARN, false},
		{"SECRET_JSON_CHILD", "mystery", audiodv1.LogLevel_LOG_LEVEL_ERROR, true},
	} {
		l := h.find(c.fragment)
		if l == nil || l.GetSource() != c.source || l.GetLevel() != c.level || l.GetTruncated() != c.truncated {
			t.Fatalf("%s: %v", c.fragment, l)
		}
	}
	// the stderr line as audiod wrote it, without its newline
	if got := h.find("fake_event").GetText(); got != `{"level":"WARN","msg":"fake_event","count":3,"device":"card0"}` {
		t.Fatalf("text %q", got)
	}

	if ev := local.all("fake_event"); len(ev) != 1 || ev[0].level != slog.LevelWarn || ev[0].attrs["source"] != "audiod" {
		t.Fatalf("local fake_event %v", ev)
	}
	if local.count("fake_ipc_event") != 1 {
		t.Fatalf("local log:\n%s", local.dump())
	}
	// cadenced's own log, which the pipeline tees, never gets them: they ship as audiod's
	if hs.log.count("fake_event") != 0 || hs.log.count("fake_ipc_event") != 0 {
		t.Fatalf("an audiod line in cadenced's log:\n%s", hs.log.dump())
	}
	for _, out := range []string{local.dump(), hs.log.dump()} {
		if strings.Contains(out, "SECRET") {
			t.Fatalf("a line the local log never keeps reached it:\n%s", out)
		}
	}
	if readies, gones := h.connections(); readies != 1 || gones != 1 {
		t.Fatalf("ready %d, gone %d", readies, gones)
	}
}

// a stderr line past the pipeline's cap, or one stderr ends without its newline, is cut; the local log never keeps it
func TestLongStderrLineCut(t *testing.T) {
	h := &logsHandler{}
	local := &recorder{}
	hs := startSupervisorWith(t, 40, audiod.Timings{}, []audiod.SupervisorOption{audiod.WithLogs(h, slog.New(local))}, "--stderr-long")
	eventually(t, "the line after the long one", func() bool { return h.find("fake_after_huge") != nil })
	hs.stop()
	long := h.find("fake_huge")
	if long == nil || len(long.GetText()) != 4096 || !long.GetTruncated() || long.GetLevel() != audiodv1.LogLevel_LOG_LEVEL_INFO {
		t.Fatalf("long line %d bytes, %v", len(long.GetText()), long.GetTruncated())
	}
	if after := h.find("fake_after_huge"); after.GetTruncated() || after.GetText() != `{"level":"INFO","msg":"fake_after_huge"}` {
		t.Fatalf("after %v", after)
	}
	if local.count("fake_huge") != 0 || local.count("fake_after_huge") != 1 || local.count("fake_unended") != 0 {
		t.Fatalf("local log:\n%s", local.dump())
	}
	// the line stderr ended without its newline, read once audiod was gone: cut, as its end may start a secret
	if unended := h.find("fake_unended"); unended == nil || !unended.GetTruncated() {
		t.Fatalf("unended %v", unended)
	}
}

// without a log pipeline nothing takes audiod's children's lines: audiod is told at Ready not to send them
func TestChildLinesOffWithoutPipeline(t *testing.T) {
	hs := startSupervisorWith(t, 40, audiod.Timings{}, nil)
	hs.waitRecord(t, 1, "logs_set", "false")
}
