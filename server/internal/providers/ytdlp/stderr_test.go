package ytdlp

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/playprogress"
)

func TestStderrStageFixtures(t *testing.T) {
	for _, c := range []struct {
		file string
		want []playprogress.Stage
	}{
		{"stderr-extract.log", []playprogress.Stage{playprogress.StageExtract, playprogress.StageExtract,
			playprogress.StageExtract, playprogress.StageExtract, playprogress.StageFormats}},
		{"stderr-challenge.log", []playprogress.Stage{playprogress.StageExtract, playprogress.StageChallenge,
			playprogress.StageChallenge, playprogress.StageFormats}},
	} {
		raw, err := os.ReadFile(filepath.Join("testdata", c.file))
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
		if len(lines) != len(c.want) {
			t.Fatalf("%s: %d lines, want %d", c.file, len(lines), len(c.want))
		}
		for i, line := range lines {
			stage, ok := stderrStage(line)
			if !ok || stage != c.want[i] {
				t.Errorf("%s line %d: %q %v, want %q", c.file, i+1, stage, ok, c.want[i])
			}
		}
	}
	for _, line := range []string{"ERROR: <redacted>", "[download] <redacted>", ""} {
		if stage, ok := stderrStage(line); ok {
			t.Errorf("%q became %q", line, stage)
		}
	}
}

func TestStageStderrReadsSplitLinesWithoutForwardingThem(t *testing.T) {
	var kept bytes.Buffer
	var stages []playprogress.Stage
	w := newStageStderr(&kept, 1024, func(s playprogress.Stage) { stages = append(stages, s) })
	_, _ = w.Write([]byte("[jsc:deno] Solving JS chal"))
	_, _ = w.Write([]byte("lenges\n[info] <redacted>: Downloading 1 format(s): <redacted>\n"))
	if len(stages) != 2 || stages[0] != playprogress.StageChallenge || stages[1] != playprogress.StageFormats {
		t.Fatalf("stages %v", stages)
	}
	if !strings.Contains(kept.String(), "Solving JS challenges") {
		t.Fatal("the bounded error classifier lost stderr")
	}
}
