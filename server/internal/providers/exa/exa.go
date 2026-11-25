// Package exa searches the web through Exa: highlight text for the model, and
// each result's url, site, date and image for a screen's link card; PageText
// fetches a found page's text for the screen's panel. Queries, urls, results
// and upstream bodies never reach an error or log.
package exa

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

const (
	defaultBaseURL  = "https://api.exa.ai"
	defaultTimeout  = 4 * time.Second
	numResults      = 3
	maxTextRunes    = 1000
	maxTitleRunes   = 200
	maxBodyBytes    = 1 << 20
	searchPath      = "/search"
	contentsPath    = "/contents"
	maxPageRunes    = 8000
	searchTypeFast  = "fast"
	highlightsJoint = " "
)

type Config struct {
	BaseURL string
	// Timeout bounds one search; zero means 4 s
	Timeout time.Duration
	// Log receives provider_panic; nil uses slog default
	Log *slog.Logger
}

type search struct {
	log     *slog.Logger
	base    string
	key     string
	timeout time.Duration
	client  *http.Client
}

func New(c Config, apiKey string, client *http.Client) providers.Search {
	if c.BaseURL == "" {
		c.BaseURL = defaultBaseURL
	}
	if c.Timeout <= 0 {
		c.Timeout = defaultTimeout
	}
	return &search{log: c.Log, base: strings.TrimRight(c.BaseURL, "/"), key: apiKey, timeout: c.Timeout, client: client}
}

var errFailed = errors.New(providers.CodeSearchFailed)

type request struct {
	Query      string         `json:"query"`
	Type       string         `json:"type"`
	NumResults int            `json:"numResults"`
	Contents   map[string]any `json:"contents"`
}

type response struct {
	Results []struct {
		Title         string   `json:"title"`
		URL           string   `json:"url"`
		PublishedDate string   `json:"publishedDate"`
		Image         string   `json:"image"`
		Highlights    []string `json:"highlights"`
	} `json:"results"`
}

func (s *search) Search(ctx context.Context, query string) ([]providers.SearchResult, error) {
	var out []providers.SearchResult
	var code string
	if providers.Guard(s.log, "exa", providers.CodeSearchFailed, func() { out, code = s.run(ctx, query) }) {
		return nil, errFailed
	}
	if code != "" {
		// a code alone: the query, the key and exa's body never reach the log
		s.logger().Warn("search_failed", "provider", "exa", "code", code)
		return nil, errFailed
	}
	return out, nil
}

func (s *search) logger() *slog.Logger {
	if s.log != nil {
		return s.log
	}
	return slog.Default()
}

// run answers the results, or why there are none: request, timeout, send, status_<n>, read or decode
func (s *search) run(ctx context.Context, query string) ([]providers.SearchResult, string) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	body, err := json.Marshal(request{
		Query: query, Type: searchTypeFast, NumResults: numResults,
		Contents: map[string]any{"highlights": true},
	})
	if err != nil {
		return nil, "request"
	}
	b, code := s.post(ctx, searchPath, body)
	if code != "" {
		return nil, code
	}
	if decodeHook != nil {
		decodeHook(b)
	}
	var r response
	if json.Unmarshal(b, &r) != nil {
		return nil, "decode"
	}
	out := make([]providers.SearchResult, 0, len(r.Results))
	for _, x := range r.Results {
		text := truncate(strings.TrimSpace(strings.Join(x.Highlights, highlightsJoint)), maxTextRunes)
		if text == "" {
			continue
		}
		res := providers.SearchResult{Title: truncate(x.Title, maxTitleRunes), Text: text}
		if u, err := url.Parse(x.URL); err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" {
			res.URL, res.Site = u.String(), strings.TrimPrefix(u.Hostname(), "www.")
		}
		if t, err := time.Parse(time.RFC3339, x.PublishedDate); err == nil {
			res.Published = t.UTC().Format(time.RFC3339)
		}
		if u, err := url.Parse(x.Image); err == nil && u.Scheme == "https" && u.Host != "" {
			res.ImageURL = u.String()
		}
		out = append(out, res)
		if len(out) == numResults {
			break
		}
	}
	return out, ""
}

// post sends one request and answers its body, or why there is none:
// request, timeout, send, status_<n> or read
func (s *search) post(ctx context.Context, path string, body []byte) ([]byte, string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.base+path, bytes.NewReader(body))
	if err != nil {
		return nil, "request"
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("x-api-key", s.key)
	resp, err := s.client.Do(req)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, "timeout"
		}
		return nil, "send"
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, "status_" + strconv.Itoa(resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return nil, "read"
	}
	return b, ""
}

// PageText is the page's text as Exa extracted it, at most 8000 runes.
func (s *search) PageText(ctx context.Context, pageURL string) (string, error) {
	var out, code string
	if providers.Guard(s.log, "exa", providers.CodeSearchFailed, func() { out, code = s.page(ctx, pageURL) }) {
		return "", errFailed
	}
	if code != "" {
		s.logger().Warn("page_text_failed", "provider", "exa", "code", code)
		return "", errFailed
	}
	return out, nil
}

func (s *search) page(ctx context.Context, pageURL string) (string, string) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	body, err := json.Marshal(map[string]any{
		"urls": []string{pageURL}, "text": map[string]any{"maxCharacters": maxPageRunes},
	})
	if err != nil {
		return "", "request"
	}
	b, code := s.post(ctx, contentsPath, body)
	if code != "" {
		return "", code
	}
	var r struct {
		Results []struct {
			Text string `json:"text"`
		} `json:"results"`
	}
	if json.Unmarshal(b, &r) != nil {
		return "", "decode"
	}
	if len(r.Results) == 0 || strings.TrimSpace(r.Results[0].Text) == "" {
		return "", "empty"
	}
	return truncate(r.Results[0].Text, maxPageRunes), ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
