package tracing

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"syscall"
	"testing"

	"github.com/coder/websocket"
)

type codedErr string

func (e codedErr) Error() string { return "upstream said: " + string(e) }
func (e codedErr) Code() string  { return string(e) }

type kindErr string

func (e kindErr) Error() string     { return "text" }
func (e kindErr) ErrorCode() string { return string(e) }

type statusErr int

func (e statusErr) Error() string       { return "http https://api.example/v1?key=abc" }
func (e statusErr) HTTPStatusCode() int { return int(e) }

func opErr(err error) error {
	return &net.OpError{Op: "dial", Net: "tcp", Err: &os.SyscallError{Syscall: "connect", Err: err}}
}

func TestClassify(t *testing.T) {
	cases := map[string]struct {
		err   error
		want  string
		close int
		types string
	}{
		"own code":        {codedErr("stt_unavailable"), "stt_unavailable", -1, ""},
		"error code":      {kindErr("rate_limited"), "rate_limited", -1, ""},
		"text not a code": {codedErr("Bad Thing"), "error", -1, ""},
		"canceled":        {fmt.Errorf("read: %w", context.Canceled), ClassCanceled, -1, ""},
		"deadline":        {context.DeadlineExceeded, ClassTimeout, -1, ""},
		"dns":             {&net.DNSError{Err: "no such host", Name: "secret.example", IsNotFound: true}, ClassDNS, -1, "*net.DNSError"},
		"dns timeout":     {&net.DNSError{Err: "i/o timeout", IsTimeout: true}, ClassTimeout, -1, ""},
		"refused":         {opErr(syscall.ECONNREFUSED), ClassConnRefused, -1, "*net.OpError,*os.SyscallError,syscall.Errno"},
		"reset":           {opErr(syscall.ECONNRESET), ClassConnReset, -1, ""},
		"broken pipe":     {opErr(syscall.EPIPE), ClassConnReset, -1, ""},
		"closed":          {fmt.Errorf("x: %w", net.ErrClosed), ClassConnClosed, -1, ""},
		"eof":             {fmt.Errorf("read: %w", io.EOF), ClassEOF, -1, "*errors.errorString"},
		"unexpected eof":  {io.ErrUnexpectedEOF, ClassEOF, -1, ""},
		"tls record":      {tls.RecordHeaderError{Msg: "first record does not look like a TLS handshake"}, ClassTLS, -1, ""},
		"tls alert":       {fmt.Errorf("remote error: %w", tls.AlertError(40)), ClassTLS, -1, ""},
		"x509":            {&tls.CertificateVerificationError{Err: x509.UnknownAuthorityError{}}, ClassTLS, -1, ""},
		"hostname":        {x509.HostnameError{Host: "secret.example"}, ClassTLS, -1, ""},
		"ws close":        {fmt.Errorf("read: %w", websocket.CloseError{Code: websocket.StatusInternalError, Reason: "token abc expired"}), ClassWSClose, 1011, "websocket.CloseError"},
		"http status":     {fmt.Errorf("call: %w", statusErr(503)), "status_503", -1, ""},
		"net timeout":     {&net.OpError{Op: "read", Err: os.ErrDeadlineExceeded}, ClassTimeout, -1, ""},
		"plain":           {errors.New("dial https://user:pw@host/path?q=1"), ClassError, -1, "*errors.errorString"},
	}
	for name, c := range cases {
		got := Classify(c.err)
		if got.Type != c.want || got.CloseCode != c.close {
			t.Errorf("%s: %+v, want %s close %d", name, got, c.want, c.close)
		}
		if c.types != "" && strings.Join(got.GoTypes, ",") != c.types {
			t.Errorf("%s: go types %v, want %s", name, got.GoTypes, c.types)
		}
		if len(got.GoTypes) > maxGoTypes {
			t.Errorf("%s: %d go types", name, len(got.GoTypes))
		}
	}
	if c := Classify(nil); c.Type != "" || c.CloseCode != -1 {
		t.Fatalf("nil %+v", c)
	}
}

// the span takes the class, the go types and the close status, never the text
func TestFailErrRecordsTheClassAndAnException(t *testing.T) {
	p := setup(t)
	_, s := Start(context.Background(), "voice", "stt.stream")
	End(s, fmt.Errorf("ws: %w", websocket.CloseError{Code: 4401, Reason: "token sk-abc expired"}))
	tr, _ := p.Store().Trace(s.SpanContext().TraceID().String())
	v := tr.Spans[0]
	if v.StatusCode != ClassWSClose || v.Attrs[AttrErrorType] != ClassWSClose || v.Attrs[AttrWSCloseCode] != int64(4401) {
		t.Fatalf("span %+v", v)
	}
	if gt, _ := v.Attrs[AttrErrorGoType].([]string); len(gt) != 1 || gt[0] != "websocket.CloseError" {
		t.Fatalf("go types %#v", v.Attrs[AttrErrorGoType])
	}
	if len(v.Events) != 1 || v.Events[0].Name != "exception" || v.Events[0].Attrs["exception.type"] != "websocket.CloseError" ||
		v.Events[0].Attrs[AttrWSCloseCode] != int64(4401) {
		t.Fatalf("events %+v", v.Events)
	}
	if b, _ := json.Marshal(v); strings.Contains(string(b), "sk-abc") || strings.Contains(string(b), "expired") {
		t.Fatalf("the error's text reached the span: %s", b)
	}
}
