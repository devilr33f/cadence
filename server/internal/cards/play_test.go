package cards_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/cards"
	"git.shino.boo/devilreef/cadence/server/internal/playprogress"
)

func renderPlay(t *testing.T, c *cards.MediaPlay) string {
	t.Helper()
	card, err := c.Render(context.Background(), &cards.Render{Lang: "ru", Messages: cards.Builtin})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(card)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestMediaPlayAdvancesInPlaceThroughStagesToFirstFrame(t *testing.T) {
	c := cards.NewMediaPlay(playprogress.CardInfo{ID: "turn:call", Tool: "music_play", Title: "Song", DeviceName: "Bedroom TV"}, "")
	id := c.ID()
	if c.ForTool() != "music_play" {
		t.Fatalf("tool %q", c.ForTool())
	}
	c.Set(playprogress.Update{Event: playprogress.Event{Stage: playprogress.StageExtract, ElapsedMS: 20}, State: playprogress.Resolving})
	if got := renderPlay(t, c); !strings.Contains(got, "Спрашиваю YouTube") || c.ID() != id {
		t.Fatalf("extract card %s", got)
	}
	c.Set(playprogress.Update{Event: playprogress.Event{Stage: playprogress.StageWaitAvailable, RemainingMS: 2000}, State: playprogress.Resolving})
	if got := renderPlay(t, c); !strings.Contains(got, "YouTube отдаст через 2 с") || c.ID() != id {
		t.Fatalf("countdown card %s", got)
	}
	c.Set(playprogress.Update{Event: playprogress.Event{Stage: playprogress.StageDone, Done: true}, State: playprogress.Playing})
	if got := renderPlay(t, c); !strings.Contains(got, "Играет") || c.ID() != id {
		t.Fatalf("playing card %s", got)
	}
	c.Set(playprogress.Update{Event: playprogress.Event{Stage: playprogress.StageRelay, Failed: true, Code: "network"}, State: playprogress.Failed})
	if got := renderPlay(t, c); !strings.Contains(got, "Играет") || strings.Contains(got, "network") {
		t.Fatalf("a late failure moved a terminal card: %s", got)
	}
}

// a playlist's card names the list while its songs are read, then the song it fetches, in place
func TestMediaPlayNamesTheListThenItsSong(t *testing.T) {
	c := cards.NewMediaPlay(playprogress.CardInfo{ID: "turn:call", Tool: "music_play", Title: "Road Trip", Artist: "someone",
		Kind: playprogress.KindPlaylist}, "")
	id := c.ID()
	c.Set(playprogress.Update{Event: playprogress.Event{Stage: playprogress.StageCollection}, State: playprogress.Resolving})
	if got := renderPlay(t, c); !strings.Contains(got, "Загружаю плейлист") || !strings.Contains(got, "Road Trip") {
		t.Fatalf("list card %s", got)
	}
	c.Set(playprogress.Update{Event: playprogress.Event{Stage: playprogress.StageExtract}, State: playprogress.Resolving,
		Item: &playprogress.Item{Title: "Numb", Artist: "Linkin Park"}})
	if got := renderPlay(t, c); !strings.Contains(got, "Numb") || !strings.Contains(got, "Linkin Park") ||
		strings.Contains(got, "Road Trip") || c.ID() != id {
		t.Fatalf("song card %s", got)
	}
	artist := cards.NewMediaPlay(playprogress.CardInfo{ID: "turn:a", Tool: "music_play", Title: "Linkin Park",
		Kind: playprogress.KindArtist}, "")
	artist.Set(playprogress.Update{Event: playprogress.Event{Stage: playprogress.StageCollection}, State: playprogress.Resolving})
	if got := renderPlay(t, artist); !strings.Contains(got, "Загружаю исполнителя") {
		t.Fatalf("artist card %s", got)
	}
}

func TestMediaPlayFailureNamesItsStage(t *testing.T) {
	c := cards.NewMediaPlay(playprogress.CardInfo{ID: "turn:call", Tool: "video_play", Title: "Video"}, "")
	c.Set(playprogress.Update{Event: playprogress.Event{Stage: playprogress.StageRelay, Failed: true, Code: "unplayable"}, State: playprogress.Failed})
	if got := renderPlay(t, c); !strings.Contains(got, "Загружаю поток") || !strings.Contains(got, "Не включилось на шаге") {
		t.Fatalf("failure card %s", got)
	}
}

func TestLoadSkillIsAQuietLocalizedStepWithoutACard(t *testing.T) {
	tools := &cards.ToolCards{}
	call := cards.Call{Key: "turn:skill", Tool: "load_skill", Args: json.RawMessage(`{"name":"music"}`)}
	if got := tools.Title(cards.Builtin, "ru", call); got != "Навык · Музыка" {
		t.Fatalf("step %q", got)
	}
	if subs, id := tools.CallSubjects(call); len(subs) != 0 || id != "" {
		t.Fatalf("a full skill card: %v %q", subs, id)
	}
	call.Result = &cards.ToolResult{Key: call.Key, Tool: call.Tool, OK: true, Data: json.RawMessage(`{"instructions":"the whole skill body"}`)}
	if got := tools.Title(cards.Builtin, "en", call); got != "Skill · Music" {
		t.Fatalf("done step %q", got)
	}
	if subs, _ := tools.CallSubjects(call); len(subs) != 0 {
		t.Fatalf("a full done skill card: %v", subs)
	}
}
