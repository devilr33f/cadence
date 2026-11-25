// Package embedx is the HTTP embedder both embed modules share: POST
// {base}/embeddings with {model, input, encoding_format}, the OpenAI-style
// shape OpenRouter also documents. Inputs, vectors, keys and upstream bodies
// never reach an error or log.
package embedx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

const (
	DefaultTimeout = 5 * time.Second
	maxBodyBytes   = 8 << 20
	path           = "/embeddings"
)

type Config struct {
	// Module names the provider in a panic log
	Module  string
	BaseURL string
	Model   string
	// APIKey empty sends no Authorization header
	APIKey string
	// Timeout bounds one call; zero means 5 s
	Timeout time.Duration
	// Log receives provider_panic; nil uses slog default
	Log *slog.Logger
}

type embedder struct {
	cfg    Config
	base   string
	client *http.Client

	mu  sync.Mutex
	dim int
}

func New(c Config, client *http.Client) providers.Embedder {
	if c.Timeout <= 0 {
		c.Timeout = DefaultTimeout
	}
	return &embedder{cfg: c, base: strings.TrimRight(c.BaseURL, "/"), client: client}
}

func (e *embedder) Model() string { return e.cfg.Model }

func fail(code string) error { return errors.New(code) }

type request struct {
	Model          string   `json:"model"`
	Input          []string `json:"input"`
	EncodingFormat string   `json:"encoding_format"`
}

type response struct {
	Data []struct {
		Index     *int       `json:"index"`
		Embedding []*float32 `json:"embedding"`
	} `json:"data"`
}

func (e *embedder) Embed(ctx context.Context, inputs []string) ([][]float32, error) {
	in, err := providers.PrepareEmbedInputs(inputs)
	if err != nil {
		return nil, err
	}
	if len(in) == 0 {
		return [][]float32{}, nil
	}
	var out [][]float32
	if providers.Guard(e.cfg.Log, e.cfg.Module, providers.CodeEmbedFailed, func() { out, err = e.run(ctx, in) }) {
		return nil, fail(providers.CodeEmbedFailed)
	}
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (e *embedder) run(ctx context.Context, in []string) ([][]float32, error) {
	ctx, cancel := context.WithTimeout(ctx, e.cfg.Timeout)
	defer cancel()
	body, err := json.Marshal(request{Model: e.cfg.Model, Input: in, EncodingFormat: "float"})
	if err != nil {
		return nil, fail(providers.CodeEmbedFailed)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.base+path, bytes.NewReader(body))
	if err != nil {
		return nil, fail(providers.CodeEmbedFailed)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if e.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+e.cfg.APIKey)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return nil, transportErr(ctx)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fail(providers.CodeEmbedFailed)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if err != nil {
		return nil, transportErr(ctx)
	}
	if len(b) > maxBodyBytes {
		return nil, fail(providers.CodeEmbedFailed)
	}
	var r response
	if json.Unmarshal(b, &r) != nil {
		return nil, fail(providers.CodeEmbedFailed)
	}
	return e.vectors(r, len(in))
}

// transportErr is a timeout only when our own deadline ran out.
func transportErr(ctx context.Context) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fail(providers.CodeEmbedTimeout)
	}
	return fail(providers.CodeEmbedUnavailable)
}

// vectors puts the reply in input order and pins the dimension on first success.
func (e *embedder) vectors(r response, n int) ([][]float32, error) {
	if len(r.Data) != n {
		return nil, fail(providers.CodeEmbedFailed)
	}
	out := make([][]float32, n)
	for i, d := range r.Data {
		at := i
		if d.Index != nil {
			at = *d.Index
		}
		if at < 0 || at >= n || out[at] != nil || len(d.Embedding) == 0 {
			return nil, fail(providers.CodeEmbedFailed)
		}
		v := make([]float32, len(d.Embedding))
		for j, x := range d.Embedding {
			if x == nil {
				return nil, fail(providers.CodeEmbedFailed)
			}
			v[j] = *x
		}
		out[at] = v
	}
	dim := len(out[0])
	if slices.ContainsFunc(out, func(v []float32) bool { return len(v) != dim }) {
		return nil, fail(providers.CodeEmbedFailed)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.dim != 0 && e.dim != dim {
		return nil, fail(providers.CodeEmbedFailed)
	}
	e.dim = dim
	return out, nil
}

// Probe is a module's test that GETs {base}{path} with the key as a bearer
// (none without one), on a call that embeds nothing.
func Probe(path string) func(context.Context, any) error {
	return func(ctx context.Context, v any) error {
		e, ok := v.(*embedder)
		if !ok {
			return providers.ProbeBadResponse
		}
		h := http.Header{}
		if e.cfg.APIKey != "" {
			h.Set("Authorization", "Bearer "+e.cfg.APIKey)
		}
		_, err := providers.ProbeGET(ctx, e.client, e.base+path, h)
		return err
	}
}
