package voice

import (
	"context"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/tracing"
	"git.shino.boo/devilreef/cadence/server/internal/tracing/tracingtest"
)

// a session under the device's voice.turn (as the link layer hands it down)
// is one trace from stt through the agent and its model to tts
func TestTurnJoinsTheDeviceTrace(t *testing.T) {
	p := tracingtest.Setup(t)
	h := newHarness(t)
	h.ctx = tracingtest.Remote(context.Background())
	h.start("s1")
	h.mic("s1", 3, 1)
	h.waitClose("s1")
	h.waitTurn("s1")
	spans := tracingtest.Wait(t, p, tracingtest.DeviceTrace,
		"voice.session", "stt.stream", "agent.turn", "agent.step", "llm.request", "tts.stream")
	byID := map[string]tracing.SpanView{}
	for _, s := range spans {
		byID[s.SpanID] = s
	}
	parent := func(name string) string {
		return byID[tracingtest.Find(t, spans, name).ParentID].Name
	}
	sess := tracingtest.Find(t, spans, "voice.session")
	if sess.ParentID != tracingtest.DeviceSpan || !sess.RemoteParent || sess.Attrs["cadence.end_code"] != "ok" ||
		sess.Attrs[tracing.AttrSessionID] != "s1" || sess.Attrs[tracing.AttrDeviceID] != dev {
		t.Fatalf("session %+v", sess)
	}
	for name, want := range map[string]string{
		"stt.stream": "voice.session", "agent.turn": "voice.session", "tts.stream": "voice.session",
		"agent.step": "agent.turn", "llm.request": "agent.step",
	} {
		if got := parent(name); got != want {
			t.Fatalf("%s under %q, want %s", name, got, want)
		}
	}
	stt := tracingtest.Find(t, spans, "stt.stream")
	if stt.Attrs[tracing.AttrProvider] != defaultValues.STT || stt.Attrs["cadence.first_ms"] == nil || stt.Stream == nil || stt.Stream.BytesIn != 3 {
		t.Fatalf("stt %+v", stt)
	}
	tts := tracingtest.Find(t, spans, "tts.stream")
	if tts.Attrs[tracing.AttrProvider] != defaultValues.TTS || tts.Attrs["cadence.first_ms"] == nil ||
		tts.Attrs["cadence.packets"] != int64(30) || tts.Stream == nil || tts.Stream.BytesOut != 30 || tts.Stream.BytesIn != 28800 {
		t.Fatalf("tts %+v", tts)
	}
}

// a failed session's root names the stage, the span to open and the
// provider's cause, which the stt span carries too
func TestFailedSessionNamesItsCause(t *testing.T) {
	p := tracingtest.Setup(t)
	h := newHarness(t, func(s *setup) {
		s.stt = &recSTT{startErr: &providers.DetailError{Code: providers.CodeSTTUnavailable, Detail: "dial_status_401"}}
	})
	h.ctx = tracingtest.Remote(context.Background())
	h.start("s1")
	h.waitClose("s1")
	h.waitTurn("s1")
	spans := tracingtest.Wait(t, p, tracingtest.DeviceTrace, "voice.session", "stt.stream")
	sess := tracingtest.Find(t, spans, "voice.session")
	if sess.StatusCode != providers.CodeSTTUnavailable || sess.Attrs["cadence.failed_stage"] != "stt" ||
		sess.Attrs["cadence.failed_span"] != "stt.stream" || sess.Attrs["cadence.failed_detail"] != "dial_status_401" {
		t.Fatalf("session %+v", sess)
	}
	if stt := tracingtest.Find(t, spans, "stt.stream"); stt.Attrs["cadence.detail"] != "dial_status_401" {
		t.Fatalf("stt %+v", stt)
	}
}
