package yandex

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/config"
)

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

type proxyRig struct {
	service      *Service
	client       *http.Client
	upstream     *httptest.Server
	logs         *lockedBuffer
	sink         *fakeLogSink
	ca           *testCA
	proxyAddress string
	calls        atomic.Int64
}

func newProxyRig(t *testing.T, handler http.HandlerFunc, devices map[string]string) *proxyRig {
	t.Helper()
	ca := newTestCA(t)
	certFile, keyFile := ca.files(t)
	rig := &proxyRig{logs: &lockedBuffer{}, sink: &fakeLogSink{}, ca: ca}
	rig.upstream = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rig.calls.Add(1)
		handler(w, r)
	}))
	rig.upstream.TLS = &tls.Config{Certificates: []tls.Certificate{ca.leaf(t, quasarNet, clckHost)}, NextProtos: []string{"http/1.1"}}
	rig.upstream.StartTLS()
	service, err := New(config.YandexProxyConfig{
		Listen: "127.0.0.1:0", CACertFile: certFile, CAKeyFile: keyFile,
		Clients: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}, Devices: devices,
	}, rig.sink, func(context.Context, string) (map[string]string, error) {
		return map[string]string{"capability": "cap-secret"}, nil
	}, slog.New(slog.NewTextHandler(rig.logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	upstreamAddress := rig.upstream.Listener.Addr().String()
	service.client = &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: ca.roots, MinVersion: tls.VersionTLS12},
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", upstreamAddress)
		},
		ForceAttemptHTTP2: false,
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := service.Start(ctx); err != nil {
		t.Fatal(err)
	}
	proxyAddress := service.Addr()
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: ca.roots, MinVersion: tls.VersionTLS12},
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", proxyAddress)
		},
		ForceAttemptHTTP2: false,
	}
	rig.service, rig.client, rig.proxyAddress = service, &http.Client{Transport: transport, Timeout: 10 * time.Second}, proxyAddress
	t.Cleanup(func() {
		cancel()
		transport.CloseIdleConnections()
		service.Close()
		rig.upstream.Close()
	})
	return rig
}

func response(t *testing.T, client *http.Client, method, url string, body io.Reader, header map[string]string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range header {
		req.Header.Set(name, value)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, data
}

func TestProxyInjectsADBAndBlocksUpdates(t *testing.T) {
	rig := newProxyRig(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/get_sync_info" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"status":"ok","config":{"system_config":{"other":1}},"big":9007199254740993}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"hasUpdate":true}`)
	}, nil)
	resp, body := response(t, rig.client, "GET", "https://quasar.yandex.net/get_sync_info?platform=yandexmidi", nil, nil)
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"remoteActivation":true`) || !strings.Contains(string(body), `"big":9007199254740993`) {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	before := rig.calls.Load()
	resp, body = response(t, rig.client, "GET", "https://quasar.yandex.net/check_updates?version=secret-version", nil, nil)
	if resp.StatusCode != 200 || string(body) != `{"hasUpdate":false}` || rig.calls.Load() != before {
		t.Fatalf("%d %s upstream=%d", resp.StatusCode, body, rig.calls.Load())
	}
	if strings.Contains(rig.logs.String(), "secret-version") {
		t.Fatal("query reached the proxy log")
	}
	resp, _ = response(t, rig.client, "GET", "https://quasar.yandex.net/cucumber/ota/user/update.zip", nil, nil)
	if resp.StatusCode != http.StatusForbidden || rig.calls.Load() != before {
		t.Fatalf("firmware status=%d upstream=%d", resp.StatusCode, rig.calls.Load())
	}
}

func TestProxyPassesRegistrationThrough(t *testing.T) {
	rig := newProxyRig(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/register" || r.Header.Get("Authorization") != "OAuth registration-secret" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"message":"allvalid"}`)
	}, nil)
	resp, body := response(t, rig.client, "POST", "https://quasar.yandex.net/register", strings.NewReader(`{"activation":"code"}`), map[string]string{
		"Authorization": "OAuth registration-secret", "Content-Type": "application/json",
	})
	if resp.StatusCode != http.StatusCreated || string(body) != `{"message":"allvalid"}` {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	if strings.Contains(rig.logs.String(), "registration-secret") {
		t.Fatal("authorization reached the proxy log")
	}
}

func TestProxyFallsBackToADBConfigOnUpstreamFailure(t *testing.T) {
	rig := newProxyRig(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}, nil)
	resp, body := response(t, rig.client, "GET", "https://quasar.yandex.net/get_sync_info?platform=yandex-pickle", nil, nil)
	if resp.StatusCode != 200 || resp.Close || !strings.Contains(string(body), `"remoteActivation":true`) {
		t.Fatalf("%d close=%v %s", resp.StatusCode, resp.Close, body)
	}
}

func TestProxyCapturesTelemetryWithoutUpstream(t *testing.T) {
	rig := newProxyRig(t, func(http.ResponseWriter, *http.Request) {
		t.Error("telemetry forwarded")
	}, map[string]string{"vendor-1": "123e4567-e89b-12d3-a456-426614174000"})
	batch := fmt.Sprintf(`[{"Timestamp":%d,"Path":"maind.log","Data":"OAuth telemetry-secret cap-secret","DeviceId":"vendor-1","Platform":"cucumber"}]`, time.Now().Unix())
	resp, body := response(t, rig.client, "POST", "https://clck.yandex.net/quasar/write/json/batch", strings.NewReader(batch), map[string]string{
		"Authorization": "OAuth telemetry-secret", "Content-Type": "application/json",
	})
	if resp.StatusCode != 200 || len(body) != 0 || rig.calls.Load() != 0 || len(rig.sink.rows) != 1 {
		t.Fatalf("%d %q calls=%d rows=%d", resp.StatusCode, body, rig.calls.Load(), len(rig.sink.rows))
	}
	stored := rig.sink.rows[0].Body
	for _, secret := range []string{"telemetry-secret", "cap-secret"} {
		if strings.Contains(stored, secret) || strings.Contains(rig.logs.String(), secret) {
			t.Fatalf("%q leaked: body=%q logs=%q", secret, stored, rig.logs.String())
		}
	}
}

func TestProxyNeverRelaysTelemetryHosts(t *testing.T) {
	rig := newProxyRig(t, func(http.ResponseWriter, *http.Request) {
		t.Error("telemetry forwarded")
	}, nil)
	for _, url := range []string{
		"https://clck.yandex.net/quasar_metrics/write/batch",
		"https://clck.yandex.net/alternate/telemetry/path",
		"https://clck.yasmina.yango.com/quasar_metrics/write/batch",
		"https://report.appmetrica.yandex.net/report?deviceid=private-id",
		"https://startup.mobile.yandex.net/startup?uuid=private-id",
		"https://startup.appmetrica.yango.com/startup",
		"https://log.strm.yandex.ru/stream",
		"https://rpc.alice.yandex.net/gproxy/push_events_batch",
	} {
		resp, body := response(t, rig.client, "POST", url, strings.NewReader("protobuf-private-body"), nil)
		if resp.StatusCode != 200 || len(body) != 0 {
			t.Fatalf("%s: %d %q", url, resp.StatusCode, body)
		}
	}
	if rig.calls.Load() != 0 {
		t.Fatalf("telemetry upstream calls = %d", rig.calls.Load())
	}
	if logs := rig.logs.String(); strings.Contains(logs, "private-id") || strings.Contains(logs, "protobuf-private-body") {
		t.Fatalf("telemetry reached logs: %s", logs)
	}
}

func TestProxyAcceptsPlainHTTPConnectivityAndRefusesOtherHosts(t *testing.T) {
	rig := newProxyRig(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"hasUpdate":true}`)
	}, nil)
	resp, body := response(t, rig.client, "GET", "http://quasar.yandex.net/check_updates", nil, nil)
	if resp.StatusCode != 200 || string(body) != `{"hasUpdate":false}` {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	if _, err := rig.client.Get("https://example.com/"); err == nil {
		t.Fatal("unapproved host accepted")
	}
}

func TestProxyRelaysWebSocketUpgrade(t *testing.T) {
	rig := newProxyRig(t, func(w http.ResponseWriter, _ *http.Request) {
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			t.Error("no hijacker")
			return
		}
		conn, rw, err := hijacker.Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = conn.Close() }()
		_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n")
		_ = rw.Flush()
		payload := make([]byte, 4)
		if _, err := io.ReadFull(rw, payload); err == nil {
			_, _ = rw.WriteString("pong")
			_ = rw.Flush()
		}
	}, nil)
	conn, err := tls.Dial("tcp", rig.proxyAddress, &tls.Config{
		RootCAs: rig.ca.roots, ServerName: quasarNet, MinVersion: tls.VersionTLS12, MaxVersion: tls.VersionTLS12,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_, _ = io.WriteString(conn, "GET /uni.ws HTTP/1.1\r\nHost: quasar.yandex.net\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Key: dG9r\r\nSec-WebSocket-Version: 13\r\n\r\n")
	reader := bufio.NewReader(conn)
	status, err := reader.ReadString('\n')
	if err != nil || !strings.Contains(status, "101") {
		t.Fatalf("status = %q, %v", status, err)
	}
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if line == "\r\n" {
			break
		}
	}
	if _, err := io.WriteString(conn, "ping"); err != nil {
		t.Fatal(err)
	}
	payload := make([]byte, 4)
	if _, err := io.ReadFull(reader, payload); err != nil || string(payload) != "pong" {
		t.Fatalf("payload = %q, %v", payload, err)
	}
}
