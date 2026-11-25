package streams_test

import (
	"log/slog"
	"strings"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/streams"
	"git.shino.boo/devilreef/cadence/server/internal/tracing"
	"git.shino.boo/devilreef/cadence/server/internal/tracing/tracingtest"
)

// a relayed fetch is a stream.relay span that counts the bytes it moved and
// keeps the token and the upstream's url off
func TestRelaySpanCountsBytes(t *testing.T) {
	p := tracingtest.Setup(t)
	client, _ := upstream(t, 0)
	r := streams.New(streams.Config{Client: client, Log: slog.New(slog.DiscardHandler)})
	token, err := r.Add("d1", "https://rr1---sn-x.googlevideo.com/videoplayback?sig=SIGNED")
	if err != nil {
		t.Fatal(err)
	}
	if _, body := get(t, app(r), "/v2/stream/"+token, ""); body != track {
		t.Fatalf("body %q", body)
	}
	var relay tracing.SpanView
	deadline := time.Now().Add(5 * time.Second)
	for relay.End == nil {
		if time.Now().After(deadline) {
			t.Fatal("no ended stream.relay")
		}
		for _, sum := range p.Store().List(tracing.ListQuery{}).Traces {
			if sum.Name == "stream.relay" {
				tr, _ := p.Store().Trace(sum.TraceID)
				relay = tracingtest.Find(t, tr.Spans, "stream.relay")
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if relay.Stream == nil || relay.Stream.BytesOut != int64(len(track)) || relay.Attrs[tracing.AttrHTTPStatus] != int64(200) ||
		relay.Attrs[tracing.AttrDeviceID] != "d1" {
		t.Fatalf("relay %+v %+v", relay, relay.Stream)
	}
	for k, v := range relay.Attrs {
		if s, ok := v.(string); ok && (strings.Contains(s, token) || strings.Contains(s, "SIGNED")) {
			t.Fatalf("%s carries the token or url", k)
		}
	}
}
