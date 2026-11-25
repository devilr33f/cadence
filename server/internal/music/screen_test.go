package music_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/pkg/rpc"
	"git.shino.boo/devilreef/cadence/server/internal/music"
	"git.shino.boo/devilreef/cadence/server/internal/screens/calls"
)

// a screen pages the queue its session plays and jumps or skips through it; another session's id is refused
func TestScreenQueueAndControl(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if err := r.svc.Play(ctx, "d1", ids(a, b, c), 0, music.Options{}); err != nil {
		t.Fatal(err)
	}
	// one router a call: its Close waits for the answer
	send := func(method, args string) *cadencev2.RpcResponse {
		t.Helper()
		var answers []*cadencev2.RpcResponse
		router := calls.New(func(_ string, env *cadencev2.Envelope) error {
			if rp := env.GetRpcResponse(); rp != nil {
				answers = append(answers, rp)
			}
			return nil
		}, slog.New(slog.NewTextHandler(io.Discard, nil)))
		r.svc.RegisterScreen(router)
		router.Register(rpc.MediaControl, r.svc.ControlMethod())
		router.Handle("d1", &cadencev2.Envelope{Body: &cadencev2.Envelope_RpcRequest{RpcRequest: &cadencev2.RpcRequest{
			Id: "q", Method: method, Json: args,
		}}})
		router.Close()
		if len(answers) != 1 {
			t.Fatalf("%s: %d answers", method, len(answers))
		}
		return answers[0]
	}
	sess := "s" + a
	got := send(rpc.MediaQueue, `{"sessionId":"`+sess+`"}`)
	var page music.QueuePage
	if got.GetStatus() != rpc.StatusOK || json.Unmarshal([]byte(got.GetJson()), &page) != nil || len(page.Items) != 2 ||
		page.Items[0].Title != "Song "+b || page.Items[1].Ref != "mq:2" || page.Next != "" {
		t.Fatalf("queue %v", got)
	}
	if got := send(rpc.MediaQueue, `{"sessionId":"other"}`); got.GetStatus() != rpc.StatusNotFound {
		t.Fatalf("another session %v", got)
	}
	if got := send(rpc.MediaControl, `{"sessionId":"`+sess+`","action":"jump","ref":"mq:2"}`); got.GetStatus() != rpc.StatusOK {
		t.Fatalf("jump %v", got)
	}
	if got := r.playedIDs(); got != a+","+c {
		t.Fatalf("played %s", got)
	}
	sess = "s" + c
	if got := send(rpc.MediaControl, `{"sessionId":"`+sess+`","action":"previous"}`); got.GetStatus() != rpc.StatusOK {
		t.Fatalf("previous %v", got)
	}
	sess = "s" + b
	for args, status := range map[string]string{
		`{"sessionId":"` + sess + `","action":"jump","ref":"x"}`:         rpc.StatusExpired,
		`{"sessionId":"` + sess + `","action":"play_next","ref":"mq:0"}`: rpc.StatusExpired,
		`{"sessionId":"` + sess + `","action":"remove","ref":"mq:1"}`:    rpc.StatusExpired,
		`{"sessionId":"` + sess + `","action":"pause"}`:                  rpc.StatusInvalid,
		`{"sessionId":"` + sess + `","action":"next","url":"x"}`:         rpc.StatusInvalid,
	} {
		if got := send(rpc.MediaControl, args); got.GetStatus() != status {
			t.Errorf("%s: %v, want %s", args, got, status)
		}
	}
}

// the audio page's sidebar gets the whole play order, its collection and proxied covers; a song behind the current one
// can be jumped to as well as one ahead, and the current song is a no-op
func TestScreenQueueSidebar(t *testing.T) {
	r := newRig(t)
	r.image = func(string) string { return "https://cadence.example/v2/images/proxied" }
	tracks := ids(a, b, c)
	for i := range tracks {
		tracks[i].ArtworkURL = "https://art.example/track.jpg"
	}
	ctx := context.Background()
	if err := r.svc.Play(ctx, "d1", tracks, 1, music.Options{Name: "Evening", ArtworkURL: "https://art.example/list.jpg"}); err != nil {
		t.Fatal(err)
	}
	var answers []*cadencev2.RpcResponse
	router := calls.New(func(_ string, env *cadencev2.Envelope) error {
		if rp := env.GetRpcResponse(); rp != nil {
			answers = append(answers, rp)
		}
		return nil
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	r.svc.RegisterScreen(router)
	router.Handle("d1", &cadencev2.Envelope{Body: &cadencev2.Envelope_RpcRequest{RpcRequest: &cadencev2.RpcRequest{
		Id: "sidebar", Method: rpc.MediaQueue, Json: `{"sessionId":"s` + b + `","all":true}`,
	}}})
	router.Close()
	if len(answers) != 1 || answers[0].GetStatus() != rpc.StatusOK {
		t.Fatalf("sidebar %v", answers)
	}
	var page music.QueuePage
	if err := json.Unmarshal([]byte(answers[0].GetJson()), &page); err != nil {
		t.Fatal(err)
	}
	if page.Name != "Evening" || page.ArtworkURL != "https://cadence.example/v2/images/proxied" || page.Total != 3 ||
		page.Position != 1 || len(page.Items) != 3 || page.Items[0].Ref != "mq:0" || page.Items[2].Title != "Song "+c ||
		page.Items[0].ArtworkURL != "https://cadence.example/v2/images/proxied" || page.Next != "" {
		t.Fatalf("sidebar page %+v", page)
	}
	args := json.RawMessage(`{"sessionId":"s` + b + `","action":"jump","ref":"mq:0"}`)
	if got := r.svc.ControlMethod()(ctx, calls.Call{DeviceID: "d1", Method: rpc.MediaControl, Args: args}); got.Status != rpc.StatusOK {
		t.Fatalf("backward jump %v", got)
	}
	if got := r.playedIDs(); got != b+","+a {
		t.Fatalf("played %s", got)
	}
	args = json.RawMessage(`{"sessionId":"s` + a + `","action":"jump","ref":"mq:0"}`)
	if got := r.svc.ControlMethod()(ctx, calls.Call{DeviceID: "d1", Method: rpc.MediaControl, Args: args}); got.Status != rpc.StatusOK {
		t.Fatalf("current jump %v", got)
	}
	if got := r.playedIDs(); got != b+","+a {
		t.Fatalf("the current song played again: %s", got)
	}
}

func TestSessionFieldsOfTheQueuesSession(t *testing.T) {
	r := newRig(t)
	if _, ok := r.svc.SessionFields("d1"); ok {
		t.Fatal("fields without a queue")
	}
	if err := r.svc.Play(context.Background(), "d1", ids(a, b, c), 0, music.Options{}); err != nil {
		t.Fatal(err)
	}
	st, ok := r.svc.SessionFields("d1")
	if !ok || st.GetSessionId() != "s"+a || len(st.GetUpNext()) != 2 || st.GetUpNext()[0].GetRef() != "mq:1" ||
		st.GetUpNext()[1].GetTitle() != "Song "+c || st.GetUpNext()[0].GetDurationMs() != 180_000 ||
		st.GetQueuePosition() != 0 || st.GetActions()[0] != "next" {
		t.Fatalf("fields %v", st)
	}
	// nothing the device reports is set
	if st.GetState() != cadencev2.PlaybackState_PLAYBACK_STATE_UNSPECIFIED || st.GetMetadata() != nil {
		t.Fatalf("device fields set %v", st)
	}
}

// play_next moves a queued song right after the playing one and remove takes one out; the refs of the rest stay, the
// playing song is neither, and each change is heard so the session's fields go out again
func TestScreenQueuePlayNextAndRemove(t *testing.T) {
	r := newRig(t)
	var changed []string
	r.svc.OnFieldsChange(func(dev string) { changed = append(changed, dev) })
	ctx := context.Background()
	if err := r.svc.Play(ctx, "d1", ids(a, b, c, x), 0, music.Options{}); err != nil {
		t.Fatal(err)
	}
	control := func(action, ref string) string {
		t.Helper()
		args, _ := json.Marshal(map[string]string{"sessionId": "s" + a, "action": action, "ref": ref})
		return r.svc.ControlMethod()(ctx, calls.Call{DeviceID: "d1", Method: rpc.MediaControl, Args: args}).Status
	}
	upNext := func() string {
		st, _ := r.svc.SessionFields("d1")
		out := ""
		for _, it := range st.GetUpNext() {
			out += it.GetRef() + " "
		}
		return out
	}
	if got := control("play_next", "mq:3"); got != rpc.StatusOK || upNext() != "mq:3 mq:1 mq:2 " {
		t.Fatalf("play_next %s: %s", got, upNext())
	}
	if got := control("remove", "mq:1"); got != rpc.StatusOK || upNext() != "mq:3 mq:2 " {
		t.Fatalf("remove %s: %s", got, upNext())
	}
	if got := control("remove", "mq:0"); got != rpc.StatusExpired {
		t.Fatalf("the playing song removed: %s", got)
	}
	if got := control("remove", "mq:1"); got != rpc.StatusExpired {
		t.Fatalf("removed twice: %s", got)
	}
	if len(changed) != 2 || changed[0] != "d1" {
		t.Fatalf("changes heard %v", changed)
	}
}
