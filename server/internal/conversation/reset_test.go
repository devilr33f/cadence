package conversation_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/conversation"
)

type closedCompaction struct {
	device string
	upto   int64
}

type compactSpy struct {
	closed []closedCompaction
	manual int
	busy   bool
}

func (s *compactSpy) CompactClosed(deviceID string, uptoID int64) bool {
	s.closed = append(s.closed, closedCompaction{deviceID, uptoID})
	return !s.busy
}

func (s *compactSpy) RequestCompaction(string) bool {
	if s.busy {
		return false
	}
	s.manual++
	return true
}

func (r *rig) post(bearer, path string) (int, string) {
	r.t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1"+path, strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := r.app.Test(req)
	if err != nil {
		r.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var b bytes.Buffer
	_, _ = b.ReadFrom(resp.Body)
	return resp.StatusCode, strings.TrimSpace(b.String())
}

func TestResetKeepsStoredTurnsAndClosesTheSegment(t *testing.T) {
	r := newRig(t)
	r.turn("t1", r.now.Add(-2*time.Hour))
	r.turn("t2", r.now.Add(-time.Hour))
	before := r.count()
	notified := ""
	r.resets.OnReset(func(deviceID, sessionID string) { notified = deviceID + "/" + sessionID })
	boundary, err := r.resets.Reset(context.Background(), r.dev, &conversation.ResetTurn{
		ID: "voice-reset", Text: "Сброс", Reply: "Начинаем заново.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if boundary != r.lastID("voice-reset") || r.count() != before+2 {
		t.Fatalf("boundary %d, messages %d", boundary, r.count())
	}
	if got, want := r.compact.closed, []closedCompaction{{r.dev, boundary}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("closed compactions %+v, want %+v", got, want)
	}
	if notified != r.dev+"/voice-reset" {
		t.Fatalf("notification %q", notified)
	}
	start, err := r.s.Start(context.Background(), r.dev)
	if err != nil || start != boundary {
		t.Fatalf("start %d, %v", start, err)
	}
	// the stored reset is visible through the history API, but never read as a model turn
	code, body := r.call("x", "/devices/"+r.dev+"/turns/voice-reset")
	if code != 200 || !strings.Contains(body, `"trigger":"reset"`) || !strings.Contains(body, "Начинаем заново") {
		t.Fatalf("stored reset: %d %s", code, body)
	}
	seg, err := r.s.Through(context.Background(), r.dev, 0, boundary, 100)
	if err != nil {
		t.Fatal(err)
	}
	var contents []string
	for _, m := range seg {
		contents = append(contents, m.Content)
	}
	if want := []string{"t1 u", "t1 a", "t2 u", "t2 a"}; !reflect.DeepEqual(contents, want) {
		t.Fatalf("closed segment %v, want %v", contents, want)
	}
	r.turn("new", r.now)
	seg, err = r.s.Through(context.Background(), r.dev, 0, boundary, 100)
	if err != nil || len(seg) != 4 {
		t.Fatalf("a later turn entered the closed segment: %+v %v", seg, err)
	}
	history, err := r.s.History(context.Background(), r.dev, boundary, 10)
	if err != nil || len(history) != 2 || history[0].Content != "new u" {
		t.Fatalf("history after the boundary: %+v %v", history, err)
	}
	all, err := r.s.After(context.Background(), r.dev, 0, time.Now().Add(time.Hour), 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range all {
		if m.Trigger == conversation.TriggerReset {
			t.Fatalf("the reset turn became learning input: %+v", m)
		}
	}
}

func TestConversationResetAndCompactRoutes(t *testing.T) {
	r := newRig(t)
	r.turn("t1", r.now.Add(-time.Hour))
	for _, bearer := range []string{"ha-token", "metrics-token"} {
		if code, body := r.post(bearer, "/devices/"+r.dev+"/conversation/reset"); code != 403 || body != `{"error":"forbidden"}` {
			t.Fatalf("%s reset: %d %s", bearer, code, body)
		}
		if code, body := r.post(bearer, "/devices/"+r.dev+"/conversation/compact"); code != 403 || body != `{"error":"forbidden"}` {
			t.Fatalf("%s compact: %d %s", bearer, code, body)
		}
	}
	code, body := r.post("x", "/devices/"+r.dev+"/conversation/reset")
	if code != 200 || body != `{"reset":true}` {
		t.Fatalf("reset: %d %s", code, body)
	}
	if len(r.compact.closed) != 1 || r.count() != 2 {
		t.Fatalf("reset deleted turns or skipped compaction: %+v, %d", r.compact.closed, r.count())
	}
	if code, body = r.post("x", "/devices/"+r.dev+"/conversation/compact"); code != 202 || body != `{"accepted":true}` || r.compact.manual != 1 {
		t.Fatalf("compact: %d %s, calls %d", code, body, r.compact.manual)
	}
	r.compact.busy = true
	if code, body = r.post("x", "/devices/"+r.dev+"/conversation/compact"); code != 409 || body != `{"error":"busy"}` {
		t.Fatalf("busy compact: %d %s", code, body)
	}
	unknown := "/devices/00000000-0000-0000-0000-000000000000/conversation/"
	for _, action := range []string{"reset", "compact"} {
		if code, body = r.post("x", unknown+action); code != 404 || body != `{"error":"not_found"}` {
			t.Fatalf("unknown %s: %d %s", action, code, body)
		}
	}
	if _, err := r.s.Start(context.Background(), "not-a-device"); !errors.Is(err, conversation.ErrNotFound) {
		t.Fatalf("bad device: %v", err)
	}
}
