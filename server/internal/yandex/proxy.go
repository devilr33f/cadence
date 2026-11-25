package yandex

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"strconv"
	"strings"
	"time"
)

const maxSyncBody = 8 << 20

var (
	errTelemetryStore  = errors.New("telemetry store")
	noUpdateBody       = []byte(`{"hasUpdate":false}`)
	emptyTelemetryBody = []byte{}
)

func (s *Service) serveConn(conn net.Conn) {
	_ = conn.SetReadDeadline(time.Now().Add(20 * time.Second))
	reader := bufio.NewReaderSize(conn, 32<<10)
	prefix, err := reader.Peek(1)
	if err != nil {
		return
	}
	if prefix[0] != 0x16 {
		s.serveHTTP(conn, reader, "")
		return
	}
	tlsConn := tlsServer(conn, reader, s.ca.tlsConfig())
	handshake, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	err = tlsConn.HandshakeContext(handshake)
	cancel()
	if err != nil {
		s.log.Warn("yandex_proxy_refused", "code", "tls")
		return
	}
	_ = tlsConn.SetDeadline(time.Time{})
	s.serveHTTP(tlsConn, bufio.NewReaderSize(tlsConn, 32<<10), canonicalHost(tlsConn.ConnectionState().ServerName))
}

func (s *Service) serveHTTP(conn io.ReadWriteCloser, reader *bufio.Reader, sni string) {
	writer := bufio.NewWriterSize(conn, 32<<10)
	for {
		if setter, ok := conn.(interface{ SetReadDeadline(time.Time) error }); ok {
			_ = setter.SetReadDeadline(time.Now().Add(90 * time.Second))
		}
		req, err := http.ReadRequest(reader)
		if err != nil {
			return
		}
		host := canonicalHost(req.Host)
		if req.URL.IsAbs() {
			host = canonicalHost(req.URL.Host)
		}
		if host == "" {
			host = sni
		}
		if !allowedHost(host) {
			s.writeLocal(writer, req, syntheticResponse(http.StatusForbidden, "application/json", []byte(`{"error":"host_refused"}`)), "host_refused", http.StatusForbidden)
			return
		}
		keep := s.handle(conn, reader, writer, req, host)
		if !keep {
			return
		}
	}
}

func (s *Service) handle(conn io.ReadWriteCloser, reader *bufio.Reader, writer *bufio.Writer, req *http.Request, host string) bool {
	ctx := req.Context()
	cancel := func() {}
	if req.Header.Get("Upgrade") == "" {
		ctx, cancel = context.WithTimeout(ctx, 60*time.Second)
	}
	defer cancel()
	req = req.WithContext(ctx)
	path := req.URL.Path
	switch {
	case path == "/generate_204":
		if !discardRequest(req) {
			return false
		}
		resp := syntheticResponse(http.StatusNoContent, "", nil)
		resp.Header.Set("X-Start-Time", strconv.FormatInt(s.now().UnixMicro(), 10))
		return s.writeLocal(writer, req, resp, "connectivity", http.StatusNoContent)
	case quasarHost(host) && updatePath(path):
		if !discardRequest(req) {
			return false
		}
		return s.writeLocal(writer, req, syntheticResponse(http.StatusOK, "application/json", noUpdateBody), "update_blocked", http.StatusOK)
	case quasarHost(host) && firmwarePath(path):
		if !discardRequest(req) {
			return false
		}
		return s.writeLocal(writer, req, syntheticResponse(http.StatusForbidden, "application/json", []byte(`{"error":"update_blocked"}`)), "firmware_blocked", http.StatusForbidden)
	case quasarHost(host) && syncPath(path):
		return s.handleSync(writer, req, host)
	case telemetryHost(host) && path == "/quasar/write/json/batch":
		return s.handleTelemetry(writer, req)
	case droppedTelemetry(host, path):
		if !discardRequest(req) {
			return false
		}
		s.log.Info("yandex_telemetry", "kind", "unsupported", "stored", 0)
		return s.writeLocal(writer, req, syntheticResponse(http.StatusOK, "text/plain", emptyTelemetryBody), "telemetry_ack", http.StatusOK)
	default:
		return s.handlePassthrough(conn, reader, writer, req, host)
	}
}

func (s *Service) handleSync(writer *bufio.Writer, req *http.Request, host string) bool {
	out := outgoing(req, host, false)
	out.Header.Set("Accept-Encoding", "identity")
	resp, err := s.client.RoundTrip(out)
	platform := req.URL.Query().Get("platform")
	if err != nil {
		s.log.Warn("yandex_proxy_upstream", "route", "sync", "code", "fallback")
		return s.writeLocal(writer, req, syntheticResponse(http.StatusOK, "application/json", fallbackSync(platform)), "sync_fallback", http.StatusOK)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxSyncBody))
		return s.writeLocal(writer, req, syntheticResponse(http.StatusOK, "application/json", fallbackSync(platform)), "sync_fallback", http.StatusOK)
	}
	if resp.StatusCode != http.StatusOK {
		return s.writeUpstream(writer, req, resp, "sync")
	}
	body, err := responseJSON(resp)
	if err == nil {
		body, err = injectADB(body, platform)
	}
	if err != nil {
		return s.writeLocal(writer, req, syntheticResponse(http.StatusOK, "application/json", fallbackSync(platform)), "sync_fallback", http.StatusOK)
	}
	return s.writeLocal(writer, req, syntheticResponse(http.StatusOK, "application/json", body), "sync_adb", http.StatusOK)
}

func (s *Service) handleTelemetry(writer *bufio.Writer, req *http.Request) bool {
	stored, err := s.captureLogs(req.Context(), req)
	if err != nil {
		status, code := http.StatusBadRequest, "decode"
		if errors.Is(err, errBodyTooLarge) {
			status, code = http.StatusRequestEntityTooLarge, "too_large"
		} else if errors.Is(err, errTelemetryStore) {
			status, code = http.StatusServiceUnavailable, "store"
		}
		s.log.Warn("yandex_telemetry_failed", "code", code)
		return s.writeLocal(writer, req, syntheticResponse(status, "application/json", []byte(`{"error":"telemetry_refused"}`)), "telemetry_refused", status)
	}
	s.log.Info("yandex_telemetry", "kind", "daemon_logs", "stored", stored)
	return s.writeLocal(writer, req, syntheticResponse(http.StatusOK, "text/plain", emptyTelemetryBody), "telemetry_stored", http.StatusOK)
}

func (s *Service) handlePassthrough(conn io.ReadWriteCloser, reader *bufio.Reader, writer *bufio.Writer, req *http.Request, host string) bool {
	upgrade := req.Header.Get("Upgrade") != ""
	resp, err := s.client.RoundTrip(outgoing(req, host, upgrade))
	if err != nil {
		s.log.Warn("yandex_proxy_upstream", "route", "passthrough", "code", "unavailable")
		_ = writeResponse(writer, req, syntheticResponse(http.StatusBadGateway, "application/json", []byte(`{"error":"bad_gateway"}`)))
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	if upgrade && resp.StatusCode == http.StatusSwitchingProtocols {
		s.log.Info("yandex_proxy_request", "route", "upgrade", "status", resp.StatusCode)
		return s.relayUpgrade(conn, reader, writer, resp)
	}
	return s.writeUpstream(writer, req, resp, "passthrough")
}

func (s *Service) writeUpstream(writer *bufio.Writer, req *http.Request, resp *http.Response, route string) bool {
	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusNotModified {
		resp.Body = http.NoBody
		resp.ContentLength = 0
	}
	closeAfter := req.Close || resp.Close
	if err := writeResponse(writer, req, resp); err != nil {
		return false
	}
	s.log.Info("yandex_proxy_request", "route", route, "status", resp.StatusCode)
	return !closeAfter
}

func (s *Service) writeLocal(writer *bufio.Writer, req *http.Request, resp *http.Response, route string, status int) bool {
	resp.Close = status >= 400
	if err := writeResponse(writer, req, resp); err != nil {
		return false
	}
	s.log.Info("yandex_proxy_request", "route", route, "status", status)
	return !req.Close && status < 400
}

func outgoing(req *http.Request, host string, upgrade bool) *http.Request {
	out := req.Clone(req.Context())
	out.URL.Scheme = "https"
	out.URL.Host = host
	out.Host = host
	out.RequestURI = ""
	out.Close = false
	upgradeName := out.Header.Get("Upgrade")
	stripHopHeaders(out.Header, upgrade)
	if upgrade {
		out.Header.Set("Connection", "Upgrade")
		out.Header.Set("Upgrade", upgradeName)
	}
	return out
}

var hopHeaders = []string{
	"Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "Te", "Trailer",
	"Transfer-Encoding", "Proxy-Connection", "Forwarded", "X-Forwarded-For", "Expect",
}

func stripHopHeaders(header http.Header, upgrade bool) {
	for _, connection := range header.Values("Connection") {
		for _, name := range strings.Split(connection, ",") {
			header.Del(strings.TrimSpace(name))
		}
	}
	for _, name := range hopHeaders {
		header.Del(name)
	}
	if !upgrade {
		header.Del("Connection")
		header.Del("Upgrade")
	}
}

func syntheticResponse(status int, contentType string, body []byte) *http.Response {
	header := http.Header{}
	if contentType != "" {
		header.Set("Content-Type", contentType)
	}
	return &http.Response{
		StatusCode: status, Status: fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header: header, Body: io.NopCloser(bytes.NewReader(body)), ContentLength: int64(len(body)),
	}
}

func writeResponse(writer *bufio.Writer, req *http.Request, resp *http.Response) error {
	if _, err := fmt.Fprintf(writer, "HTTP/1.1 %d %s\r\n", resp.StatusCode, http.StatusText(resp.StatusCode)); err != nil {
		return err
	}
	header := resp.Header.Clone()
	stripHopHeaders(header, false)
	header.Del("Content-Length")
	for name, values := range header {
		for _, value := range values {
			if _, err := fmt.Fprintf(writer, "%s: %s\r\n", name, value); err != nil {
				return err
			}
		}
	}
	closeAfter := req.Close || resp.Close
	if closeAfter {
		if _, err := writer.WriteString("Connection: close\r\n"); err != nil {
			return err
		}
	}
	body := resp.Body
	length := resp.ContentLength
	if req.Method == http.MethodHead || resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusNotModified {
		body = http.NoBody
		if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusNotModified {
			length = 0
		}
	}
	if length >= 0 {
		if _, err := fmt.Fprintf(writer, "Content-Length: %d\r\n\r\n", length); err != nil {
			return err
		}
		if req.Method != http.MethodHead {
			if _, err := io.Copy(writer, body); err != nil {
				return err
			}
			return writer.Flush()
		}
		return writer.Flush()
	}
	if _, err := writer.WriteString("Transfer-Encoding: chunked\r\n\r\n"); err != nil {
		return err
	}
	chunked := httputil.NewChunkedWriter(writer)
	if _, err := io.Copy(chunked, body); err != nil {
		return err
	}
	if err := chunked.Close(); err != nil {
		return err
	}
	if _, err := writer.WriteString("\r\n"); err != nil {
		return err
	}
	return writer.Flush()
}

func responseJSON(resp *http.Response) ([]byte, error) {
	wire, err := readLimited(resp.Body, maxSyncBody)
	if err != nil {
		return nil, err
	}
	if strings.EqualFold(resp.Header.Get("Content-Encoding"), "gzip") || len(wire) >= 2 && wire[0] == 0x1f && wire[1] == 0x8b {
		reader, err := gzip.NewReader(bytes.NewReader(wire))
		if err != nil {
			return nil, err
		}
		defer func() { _ = reader.Close() }()
		return readLimited(reader, maxSyncBody)
	}
	return wire, nil
}

func discardRequest(req *http.Request) bool {
	if req.Body == nil {
		return true
	}
	n, err := io.Copy(io.Discard, io.LimitReader(req.Body, maxTelemetryWire+1))
	return err == nil && n <= maxTelemetryWire
}

func droppedTelemetry(host, path string) bool {
	switch canonicalHost(host) {
	case clckHost, "clck.yasmina.yango.com", "report.appmetrica.yandex.net",
		"startup.mobile.yandex.net", "startup.appmetrica.yango.com", "log.strm.yandex.ru":
		return true
	case "rpc.alice.yandex.net":
		return path == "/gproxy/push_events_batch"
	default:
		return false
	}
}

func (s *Service) relayUpgrade(conn io.ReadWriteCloser, reader *bufio.Reader, writer *bufio.Writer,
	resp *http.Response) bool {
	upstream, ok := resp.Body.(io.ReadWriteCloser)
	if !ok {
		return false
	}
	if _, err := fmt.Fprintf(writer, "HTTP/1.1 101 Switching Protocols\r\n"); err != nil {
		return false
	}
	for name, values := range resp.Header {
		for _, value := range values {
			if _, err := fmt.Fprintf(writer, "%s: %s\r\n", name, value); err != nil {
				return false
			}
		}
	}
	if _, err := writer.WriteString("\r\n"); err != nil {
		return false
	}
	if err := writer.Flush(); err != nil {
		return false
	}
	if setter, ok := conn.(interface{ SetDeadline(time.Time) error }); ok {
		_ = setter.SetDeadline(time.Time{})
	}
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(upstream, reader)
		if writer, ok := upstream.(interface{ CloseWrite() error }); ok {
			_ = writer.CloseWrite()
		}
		close(done)
	}()
	_, _ = io.Copy(conn, upstream)
	_ = upstream.Close()
	_ = conn.Close()
	<-done
	return false
}

// tlsServer keeps bytes peeked while detecting the protocol in the tls stream.
func tlsServer(conn net.Conn, reader *bufio.Reader, cfg *tls.Config) *tls.Conn {
	return tls.Server(&bufferedConn{Conn: conn, reader: reader}, cfg)
}

type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.reader.Read(p) }
