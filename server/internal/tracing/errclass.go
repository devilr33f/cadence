package tracing

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"syscall"

	"github.com/coder/websocket"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// error classification: what failed, from an error's types and numbers alone,
// never its text (rule 8)
const (
	// AttrErrorGoType is the error's go type chain, outermost first, wrappers
	// left out: type names carry no data
	AttrErrorGoType = "error.go_type"
	// AttrWSCloseCode is a websocket's numeric close status
	AttrWSCloseCode = "websocket.close_code"
	// AttrOutcome is how a provider's stream or socket ended, a token its
	// provider defines (invalid_auth, closed…)
	AttrOutcome = "cadence.outcome"

	// the classes Classify answers
	ClassCanceled    = "canceled"
	ClassTimeout     = "timeout"
	ClassDNS         = "dns"
	ClassConnRefused = "conn_refused"
	ClassConnReset   = "conn_reset"
	ClassConnClosed  = "conn_closed"
	ClassEOF         = "eof"
	ClassTLS         = "tls"
	ClassWSClose     = "ws_close"
	ClassError       = "error"

	maxGoTypes = 3
)

// Class is an error's privacy-safe description: its type token, the
// websocket close status when a socket's close ended it (-1 otherwise) and
// its go type chain.
type Class struct {
	Type      string
	CloseCode int
	GoTypes   []string
}

// Classify names what err is from its types and numbers, never its text: a
// code it carries (Code() or ErrorCode()), a context's end, a websocket's
// close, a dns, connection, tls or eof failure, status_<n> for an error that
// carries an HTTP status (HTTPStatusCode()), else error.
func Classify(err error) Class {
	c := Class{Type: "", CloseCode: -1}
	if err == nil {
		return c
	}
	c.GoTypes = goTypes(err)
	if s := websocket.CloseStatus(err); s != -1 {
		c.CloseCode = int(s)
	}
	c.Type = classType(err, c.CloseCode)
	return c
}

func classType(err error, closeCode int) string {
	if code := ownCode(err); code != "" {
		return code
	}
	var dnsErr *net.DNSError
	var netErr net.Error
	var status interface{ HTTPStatusCode() int }
	switch {
	case errors.Is(err, context.Canceled):
		return ClassCanceled
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(err, syscall.ETIMEDOUT):
		return ClassTimeout
	case closeCode != -1:
		return ClassWSClose
	case errors.As(err, &dnsErr):
		if dnsErr.IsTimeout {
			return ClassTimeout
		}
		return ClassDNS
	case errors.Is(err, syscall.ECONNREFUSED):
		return ClassConnRefused
	case errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE) || errors.Is(err, syscall.ECONNABORTED):
		return ClassConnReset
	case errors.Is(err, net.ErrClosed):
		// read or written after this side closed it
		return ClassConnClosed
	case isTLS(err):
		return ClassTLS
	case errors.As(err, &netErr) && netErr.Timeout():
		return ClassTimeout
	case errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF):
		return ClassEOF
	case errors.As(err, &status) && status.HTTPStatusCode() > 0:
		return "status_" + strconv.Itoa(status.HTTPStatusCode())
	}
	return ClassError
}

// ownCode is the code an error states itself, when it is a code
func ownCode(err error) string {
	var c interface{ Code() string }
	if errors.As(err, &c) && statusCodeRe.MatchString(c.Code()) {
		return c.Code()
	}
	var ec interface{ ErrorCode() string }
	if errors.As(err, &ec) && statusCodeRe.MatchString(ec.ErrorCode()) {
		return ec.ErrorCode()
	}
	return ""
}

func isTLS(err error) bool {
	var (
		rh       tls.RecordHeaderError
		alert    tls.AlertError
		verify   *tls.CertificateVerificationError
		unknown  x509.UnknownAuthorityError
		hostname x509.HostnameError
		invalid  x509.CertificateInvalidError
	)
	return errors.As(err, &rh) || errors.As(err, &alert) || errors.As(err, &verify) || errors.As(err, &unknown) ||
		errors.As(err, &hostname) || errors.As(err, &invalid)
}

// goTypes are err's chain's type names, outermost first, at most three,
// without the fmt and errors.Join wrappers that say nothing
func goTypes(err error) []string {
	var out []string
	seen := map[string]bool{}
	for e := err; e != nil && len(out) < maxGoTypes; {
		name := fmt.Sprintf("%T", e)
		if name != "*fmt.wrapError" && name != "*fmt.wrapErrors" && name != "*errors.joinError" && !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
		switch u := e.(type) { //nolint:errorlint // walks the chain itself, one link at a time
		case interface{ Unwrap() error }:
			e = u.Unwrap()
		case interface{ Unwrap() []error }:
			if es := u.Unwrap(); len(es) > 0 {
				e = es[0]
			} else {
				e = nil
			}
		default:
			e = nil
		}
	}
	return out
}

// FailErr marks span failed by err's class (Classify): its type as the
// status and error.type, its go types, a websocket close status, and an
// exception event that carries those alone, never the error's text.
func FailErr(span trace.Span, err error) {
	if span == nil || err == nil {
		return
	}
	c := Classify(err)
	code := statusCode(c.Type)
	attrs := []attribute.KeyValue{attribute.String(AttrErrorType, code)}
	if len(c.GoTypes) > 0 {
		attrs = append(attrs, attribute.StringSlice(AttrErrorGoType, c.GoTypes))
	}
	if c.CloseCode != -1 {
		attrs = append(attrs, attribute.Int(AttrWSCloseCode, c.CloseCode))
	}
	span.SetAttributes(attrs...)
	event := []attribute.KeyValue{attribute.String(AttrErrorType, code)}
	if len(c.GoTypes) > 0 {
		event = append(event, attribute.String("exception.type", c.GoTypes[0]), attribute.StringSlice(AttrErrorGoType, c.GoTypes))
	}
	if c.CloseCode != -1 {
		event = append(event, attribute.Int(AttrWSCloseCode, c.CloseCode))
	}
	span.AddEvent("exception", trace.WithAttributes(event...))
	span.SetStatus(codes.Error, code)
}
