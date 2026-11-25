package ytdlp_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/catalog"
	"git.shino.boo/devilreef/cadence/server/internal/playprogress"
	"git.shino.boo/devilreef/cadence/server/internal/providers/ytdlp"
)

func TestResolveScreenStreamsItsStagesAndLogsNoUpstreamText(t *testing.T) {
	body := "echo '[youtube] Extracting URL: https://private.example/watch?v=PRIVATE-ID' >&2; " +
		"echo '[jsc:deno] Solving JS challenges' >&2; " +
		"echo '[info] PRIVATE-TITLE: Downloading 1 format(s): PRIVATE-FORMAT' >&2; " +
		"cat '" + fixture(t, "screen.json") + "'"
	bin, _ := fakeBin(t, body)
	var log bytes.Buffer
	var events []ytdlp.ResolveEvent
	ctx := ytdlp.WithProgress(context.Background(), func(ev ytdlp.ResolveEvent) { events = append(events, ev) })
	p := ytdlp.New(ytdlp.Config{Bin: bin, TempDir: t.TempDir(), Log: logger(&log)})
	if _, err := p.ResolveScreen(ctx, "5qZQEq_C3vc", catalog.DeviceProfile{Screen: true, MaxHeight: 1080, Codecs: []string{"h264"}}); err != nil {
		t.Fatal(err)
	}
	want := []playprogress.Stage{playprogress.StageCookies, playprogress.StageExtract, playprogress.StageChallenge,
		playprogress.StageFormats, playprogress.StagePick}
	if len(events) != len(want)+1 {
		t.Fatalf("events %+v", events)
	}
	for i, stage := range want {
		if events[i].Stage != stage || events[i].ElapsedMS < 0 || events[i].Done || events[i].Failed {
			t.Errorf("event %d %+v", i, events[i])
		}
	}
	last := events[len(events)-1]
	if !last.Done || last.Failed || last.Stage != playprogress.StagePick {
		t.Fatalf("final %+v", last)
	}
	text := log.String()
	for _, required := range []string{"ytdlp_resolve_stage", "ytdlp_resolve_stages", "stage=cookies", "stage=extract",
		"stage=challenge", "stage=formats", "stage=pick", "cookies=", "extract=", "challenge=", "formats=", "pick="} {
		if !strings.Contains(text, required) {
			t.Errorf("log lacks %q: %s", required, text)
		}
	}
	for _, private := range []string{"PRIVATE", "private.example", "googlevideo", "A Song", "SECRET"} {
		if strings.Contains(text, private) {
			t.Errorf("log carries %q: %s", private, text)
		}
	}
	for _, ev := range events {
		if strings.Contains(ev.Code, "PRIVATE") || strings.Contains(ev.VCodec, "PRIVATE") || strings.Contains(ev.ACodec, "PRIVATE") {
			t.Fatalf("event carries upstream text: %+v", ev)
		}
	}
}

func TestResolveFailureEventKeepsItsStage(t *testing.T) {
	bin, _ := fakeBin(t, "echo '[jsc:deno] Solving JS challenges PRIVATE-ID' >&2; echo 'ERROR: PRIVATE-TEXT' >&2; exit 1")
	var events []ytdlp.ResolveEvent
	ctx := ytdlp.WithProgress(context.Background(), func(ev ytdlp.ResolveEvent) { events = append(events, ev) })
	var log bytes.Buffer
	p := ytdlp.New(ytdlp.Config{Bin: bin, TempDir: t.TempDir(), Log: logger(&log)})
	if _, err := p.ResolveScreen(ctx, "5qZQEq_C3vc", catalog.DeviceProfile{}); err == nil {
		t.Fatal("resolved")
	}
	last := events[len(events)-1]
	if !last.Failed || last.Stage != playprogress.StageChallenge || last.Code != "player_failed" {
		t.Fatalf("final %+v", last)
	}
	if strings.Contains(log.String(), "PRIVATE") {
		t.Fatalf("log: %s", log.String())
	}
}
