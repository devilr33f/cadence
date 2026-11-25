// Package yandex provisions owner-managed Yandex devices through a restricted
// transparent https proxy. It never serves firmware or forwards telemetry.
package yandex

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/config"
)

var errBodyTooLarge = errors.New("body too large")

type Service struct {
	listen  string
	clients []netip.Prefix
	devices map[string]string
	logs    LogSink
	secrets SecretLookup
	log     *slog.Logger
	now     func() time.Time
	ca      *signingCA
	client  *http.Transport

	mu     sync.Mutex
	ln     net.Listener
	conns  map[net.Conn]struct{}
	closed bool
	wg     sync.WaitGroup
}

func New(cfg config.YandexProxyConfig, logs LogSink, secrets SecretLookup, log *slog.Logger) (*Service, error) {
	if cfg.Listen == "" {
		return nil, nil
	}
	if logs == nil {
		return nil, errors.New("yandex proxy: log store required")
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	ca, err := loadCA(cfg.CACertFile, cfg.CAKeyFile, time.Now)
	if err != nil {
		return nil, err
	}
	roots, err := x509.SystemCertPool()
	if err != nil {
		roots = x509.NewCertPool()
	}
	dialer := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
	if cfg.UpstreamDNS != "" {
		dialer.Resolver = &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, cfg.UpstreamDNS)
		}}
	}
	return &Service{
		listen: cfg.Listen, clients: cfg.Clients, devices: cfg.Devices,
		logs: logs, secrets: secrets, log: log, now: time.Now, ca: ca,
		client: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
			DialContext:     dialer.DialContext, ForceAttemptHTTP2: false,
			TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 45 * time.Second,
			IdleConnTimeout: 90 * time.Second,
		},
		conns: map[net.Conn]struct{}{},
	}, nil
}

// Start binds before the server reports ready; later accept failures only stop
// this auxiliary listener, never cadence's api.
func (s *Service) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return net.ErrClosed
	}
	ln, err := net.Listen("tcp", s.listen)
	if err != nil {
		s.mu.Unlock()
		return errors.New("yandex proxy: listen failed")
	}
	s.ln = ln
	s.wg.Add(1)
	s.mu.Unlock()
	go s.accept(ctx, ln)
	s.log.Info("yandex_proxy_listening", "listen", ln.Addr().String())
	return nil
}

func (s *Service) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ln == nil {
		return ""
	}
	return s.ln.Addr().String()
}

func (s *Service) accept(ctx context.Context, ln net.Listener) {
	defer s.wg.Done()
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			_ = ln.Close()
		case <-stop:
		}
	}()
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		if !s.allowedClient(conn.RemoteAddr()) {
			s.log.Warn("yandex_proxy_refused", "code", "client")
			_ = conn.Close()
			continue
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			_ = conn.Close()
			return
		}
		if len(s.conns) >= 64 {
			s.mu.Unlock()
			_ = conn.Close()
			s.log.Warn("yandex_proxy_refused", "code", "capacity")
			continue
		}
		s.conns[conn] = struct{}{}
		s.wg.Add(1)
		s.mu.Unlock()
		go s.serve(conn)
	}
}

func (s *Service) allowedClient(address net.Addr) bool {
	host, _, err := net.SplitHostPort(address.String())
	if err != nil {
		return false
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	addr = addr.Unmap()
	for _, prefix := range s.clients {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

func (s *Service) serve(conn net.Conn) {
	defer s.wg.Done()
	defer func() {
		s.mu.Lock()
		delete(s.conns, conn)
		s.mu.Unlock()
		_ = conn.Close()
	}()
	s.serveConn(conn)
}

func (s *Service) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	if s.ln != nil {
		_ = s.ln.Close()
	}
	for conn := range s.conns {
		_ = conn.Close()
	}
	s.mu.Unlock()
	s.wg.Wait()
	s.client.CloseIdleConnections()
}
