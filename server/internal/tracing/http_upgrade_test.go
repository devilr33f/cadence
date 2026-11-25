package tracing

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// regression: the traced body hid the socket's io.ReadWriteCloser, so every
// websocket dial under a span failed after coder/websocket's 3 s body read,
// and the handshake span lasted that long, failed as "error"
func TestTransportEndsAnUpgradeAtTheHandshake(t *testing.T) {
	p := setup(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = c.CloseNow() }()
		typ, b, err := c.Read(r.Context())
		if err != nil {
			return
		}
		_ = c.Write(r.Context(), typ, b)
		_, _, _ = c.Read(r.Context())
	}))
	defer srv.Close()

	ctx, parent := Start(context.Background(), "voice", "stt.stream")
	dctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(dctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/asr?key=secret",
		&websocket.DialOptions{HTTPClient: &http.Client{Transport: Transport(nil)}})
	if err != nil {
		t.Fatalf("dial through the transport: %v", err)
	}
	if err := ws.Write(dctx, websocket.MessageText, []byte("hi")); err != nil {
		t.Fatal(err)
	}
	if _, b, err := ws.Read(dctx); err != nil || string(b) != "hi" {
		t.Fatalf("echo %q %v", b, err)
	}

	// the handshake's span ended at the upgrade, while the socket still runs
	tr, _ := p.Store().Trace(parent.SpanContext().TraceID().String())
	if len(tr.Spans) != 2 {
		t.Fatalf("spans %d", len(tr.Spans))
	}
	c := tr.Spans[1]
	if c.Name != "GET 127.0.0.1" || c.Live || c.Status == "error" || c.Attrs[AttrHTTPStatus] != int64(101) ||
		c.Attrs[AttrErrorType] != nil {
		t.Fatalf("handshake span %+v", c)
	}
	_ = ws.Close(websocket.StatusNormalClosure, "")
	End(parent, nil)
}
