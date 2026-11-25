package voice

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

// every new partial goes to the screens as stt gives it, before the final
// and the agent, then the final once; repeats are not sent again, and none
// of the text reaches a log (rule 8)
func TestPartialsReachTheScreensBeforeTheFinal(t *testing.T) {
	type heard struct {
		session, text string
		final         bool
	}
	var mu sync.Mutex
	var got []heard
	var atAgent int
	fine := says("Noon.")
	h := newHarness(t, func(s *setup) {
		// a partial a packet, one more word each, the final at the 6th packet
		s.stt = fakeModule(t, "fake-stt", `{"finalAfterPackets":6,"partialEvery":1,"text":"`+transcript+`"}`).(providers.STT)
		s.transcript = func(id, session, text string, final bool) {
			if id != dev {
				t.Errorf("transcript for %q", id)
			}
			mu.Lock()
			got = append(got, heard{session, text, final})
			mu.Unlock()
		}
		s.agent = func(ctx context.Context, tr agent.Turn, out chan<- agent.Out) agent.Result {
			mu.Lock()
			atAgent = len(got)
			mu.Unlock()
			return fine(ctx, tr, out)
		}
	})
	h.start("s1")
	h.mic("s1", 6, 1)
	if c := h.waitClose("s1"); c.GetCode() != "" {
		t.Fatalf("closed with %q", c.GetCode())
	}
	mu.Lock()
	defer mu.Unlock()
	words := strings.Fields(transcript)
	var want []heard
	for i := range words {
		want = append(want, heard{"s1", strings.Join(words[:i+1], " "), false})
	}
	// the last partial already holds every word: the final follows it
	want = append(want, heard{"s1", transcript, true})
	if !slices.Equal(got, want) {
		t.Fatalf("heard %v, want %v", got, want)
	}
	if atAgent != len(want) {
		t.Fatalf("the agent ran after %d of %d hypotheses", atAgent, len(want))
	}
	if strings.Contains(h.logs.String(), "tell me") {
		t.Fatalf("a log line carries the transcript:\n%s", h.logs)
	}
}
