package ambient

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	// ManifestURL is Apple's Aerial manifest
	ManifestURL = "https://sylvan.apple.com/Aerials/resources-16.tar"
	// Refresh is how often the manifest is fetched again
	Refresh = 7 * 24 * time.Hour
	// Retry is the wait after a failed fetch
	Retry = time.Hour

	fetchTimeout = 2 * time.Minute
)

// DefaultHosts are the hosts the manifest may come from, redirects included.
var DefaultHosts = []string{"sylvan.apple.com"}

var errHost = errors.New("ambient: host not allowed")

// FetcherConfig configures the manifest fetch.
type FetcherConfig struct {
	// URL defaults to ManifestURL
	URL string
	// Hosts the url and every redirect must be on (https alone); defaults to DefaultHosts
	Hosts []string
	// Client defaults to one that refuses other hosts; a test's client keeps the host check
	Client *http.Client
	Log    *slog.Logger
	Now    func() time.Time
}

// Fetcher keeps the last good manifest, fetched again weekly.
type Fetcher struct {
	url    string
	hosts  []string
	client *http.Client
	log    *slog.Logger
	now    func() time.Time

	mu       sync.RWMutex
	manifest *Manifest
	fetched  time.Time
	etag     string
	modified string
}

// NewFetcher checks the config's url against its hosts.
func NewFetcher(c FetcherConfig) (*Fetcher, error) {
	f := &Fetcher{url: c.URL, hosts: c.Hosts, client: c.Client, log: c.Log, now: c.Now}
	if f.url == "" {
		f.url = ManifestURL
	}
	if len(f.hosts) == 0 {
		f.hosts = DefaultHosts
	}
	if f.log == nil {
		f.log = slog.New(slog.DiscardHandler)
	}
	if f.now == nil {
		f.now = time.Now
	}
	u, err := url.Parse(f.url)
	if err != nil || !f.allowed(u) {
		return nil, errHost
	}
	if f.client == nil {
		roots, err := manifestRoots()
		if err != nil {
			return nil, err
		}
		f.client = &http.Client{Transport: &http.Transport{
			TLSClientConfig:       &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: 15 * time.Second}).DialContext,
			TLSHandshakeTimeout:   15 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
		}}
	}
	// the host check holds for any client, redirects included
	client := *f.client
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 || !f.allowed(req.URL) {
			return errHost
		}
		return nil
	}
	f.client = &client
	return f, nil
}

func (f *Fetcher) allowed(u *url.URL) bool {
	if u.Scheme != "https" || u.User != nil {
		return false
	}
	return slices.Contains(f.hosts, strings.ToLower(u.Hostname()))
}

// Manifest is the last good manifest, nil before the first.
func (f *Fetcher) Manifest() *Manifest {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.manifest
}

// Fetch gets the manifest once, conditionally on the last one's validators; on any failure the last good one stays.
func (f *Fetcher) Fetch(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.url, nil)
	if err != nil {
		return err
	}
	f.mu.RLock()
	if f.manifest != nil {
		if f.etag != "" {
			req.Header.Set("If-None-Match", f.etag)
		}
		if f.modified != "" {
			req.Header.Set("If-Modified-Since", f.modified)
		}
	}
	f.mu.RUnlock()
	resp, err := f.client.Do(req)
	if err != nil {
		if errors.Is(err, errHost) {
			return errHost
		}
		return fmt.Errorf("ambient: fetch: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode == http.StatusNotModified:
		f.mu.Lock()
		f.fetched = f.now()
		f.mu.Unlock()
		return nil
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("ambient: fetch: status %d", resp.StatusCode)
	}
	m, err := ParseTar(resp.Body)
	if err != nil {
		return err
	}
	f.mu.Lock()
	f.manifest, f.fetched = m, f.now()
	f.etag, f.modified = resp.Header.Get("ETag"), resp.Header.Get("Last-Modified")
	f.mu.Unlock()
	f.log.Info("ambient_manifest", "clips", len(m.Clips), "languages", len(m.Strings))
	return nil
}

// Run fetches now, then every Refresh, Retry after a failure, until ctx ends.
func (f *Fetcher) Run(ctx context.Context) {
	for {
		wait := Refresh
		if err := f.Fetch(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			f.log.Warn("ambient_manifest_failed", "code", failureCode(err))
			wait = Retry
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
	}
}

// failureCode is a fetch error as a token for the logs (no url, no upstream text)
func failureCode(err error) string {
	var ne net.Error
	switch {
	case errors.Is(err, errHost):
		return "host"
	case errors.As(err, &ne) && ne.Timeout(), errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case strings.Contains(err.Error(), "status "):
		return "status"
	case strings.HasPrefix(err.Error(), "ambient: fetch"):
		return "network"
	default:
		return "parse"
	}
}
