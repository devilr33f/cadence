package ytdlp

import (
	"bytes"
	"io"
	"strings"

	"git.shino.boo/devilreef/cadence/server/internal/playprogress"
)

// stderrStage recognizes only fixed yt-dlp phrases. The line itself, which
// may contain a title, id or signed url, is neither returned nor retained here.
func stderrStage(line string) (playprogress.Stage, bool) {
	l := strings.ToLower(strings.TrimSpace(line))
	switch {
	case strings.Contains(l, "[jsc:") && strings.Contains(l, "solving js challenge"):
		return playprogress.StageChallenge, true
	case strings.Contains(l, "downloading m3u8 information"),
		strings.Contains(l, "[info]") && strings.Contains(l, "downloading") && strings.Contains(l, "format"):
		return playprogress.StageFormats, true
	case strings.Contains(l, "[youtube]") && strings.Contains(l, "extracting url"),
		strings.Contains(l, "downloading webpage"),
		strings.Contains(l, "downloading player"),
		strings.Contains(l, "downloading tv client config"),
		strings.Contains(l, "downloading ios client config"):
		return playprogress.StageExtract, true
	}
	return "", false
}

// stageStderr keeps the bounded error text for classify while feeding complete
// lines to the stage parser. yt-dlp writes stderr unbuffered; this handles a
// line split across writes without ever forwarding its contents.
type stageStderr struct {
	limited *limited
	pending []byte
	stage   func(playprogress.Stage)
}

func newStageStderr(w io.Writer, n int, stage func(playprogress.Stage)) *stageStderr {
	return &stageStderr{limited: &limited{w: w, n: n}, stage: stage}
}

func (w *stageStderr) Write(p []byte) (int, error) {
	_, _ = w.limited.Write(p)
	w.pending = append(w.pending, p...)
	for {
		i := bytes.IndexByte(w.pending, '\n')
		if i < 0 {
			break
		}
		w.line(string(w.pending[:i]))
		w.pending = append(w.pending[:0], w.pending[i+1:]...)
	}
	// a line without a newline is still bounded; its content is not progress
	if len(w.pending) > 4096 {
		w.pending = append(w.pending[:0], w.pending[len(w.pending)-4096:]...)
	}
	return len(p), nil
}

func (w *stageStderr) line(line string) {
	if stage, ok := stderrStage(line); ok && w.stage != nil {
		w.stage(stage)
	}
}
