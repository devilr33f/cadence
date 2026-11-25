package providers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"git.shino.boo/devilreef/cadence/server/internal/api"
)

const (
	testDeadline = 10 * time.Second
	testEvery    = 5 * time.Second
)

// now is the test limiter's clock
var now = time.Now

// TestResult is a provider test's outcome: a code, never upstream text.
type TestResult struct {
	OK   bool   `json:"ok"`
	Code string `json:"code,omitempty" doc:"Why the test failed; absent when it passed."`
	MS   int64  `json:"ms" doc:"How long the test took, in milliseconds."`
}

// TransformSchema lists the codes a test answers, from ProbeCodes.
func (TestResult) TransformSchema(_ huma.Registry, s *huma.Schema) *huma.Schema {
	if c := s.Properties["code"]; c != nil {
		c.Enum = nil
		for _, code := range ProbeCodes {
			c.Enum = append(c.Enum, string(code))
		}
	}
	return s
}

type testOut struct{ Body TestResult }

func testDescription() string {
	codes := make([]string, len(ProbeCodes))
	for i, c := range ProbeCodes {
		codes[i] = string(c)
	}
	return fmt.Sprintf("Builds the instance afresh, never touching the one the runtime holds, and runs its module's cheapest call "+
		"that costs nothing, within %d s: ok, or a code (%s). A module with no such call answers unsupported. "+
		"At most one test per instance per %d s; past that the answer is 429 with Retry-After.",
		int(testDeadline/time.Second), strings.Join(codes, ", "), int(testEvery/time.Second))
}

func registerTest(a huma.API, s *Instances, log *slog.Logger) {
	lim := &testLimiter{last: map[string]time.Time{}}
	huma.Register(a, huma.Operation{
		OperationID: "test-provider", Method: http.MethodPost, Path: "/providers/{name}/test", Tags: []string{tag},
		Description: testDescription(),
	}, func(ctx context.Context, in *nameIn) (*testOut, error) {
		switch _, err := s.Ref(ctx, in.Name); {
		case errors.Is(err, ErrNotFound):
			return nil, api.Error(http.StatusNotFound, "not_found")
		case err != nil && !errors.Is(err, ErrUnknownModule):
			log.Error("provider_test_failed", instanceAttr(in.Name)...)
			return nil, api.Error(http.StatusInternalServerError, "internal")
		}
		if wait := lim.take(in.Name, now()); wait > 0 {
			retry := strconv.Itoa(max(1, int(math.Ceil(wait.Seconds()))))
			return nil, huma.ErrorWithHeaders(api.Error(http.StatusTooManyRequests, "rate_limited"), http.Header{"Retry-After": {retry}})
		}
		pctx, cancel := context.WithTimeout(ctx, testDeadline)
		defer cancel()
		start := time.Now()
		code, err := s.Probe(pctx, in.Name)
		ms := time.Since(start).Milliseconds()
		switch {
		case errors.Is(err, ErrNotFound):
			return nil, api.Error(http.StatusNotFound, "not_found")
		case err != nil:
			log.Error("provider_test_failed", instanceAttr(in.Name)...)
			return nil, api.Error(http.StatusInternalServerError, "internal")
		}
		logged := code
		if logged == "" {
			logged = "ok"
		}
		log.Info("provider_test", "instance", in.Name, "code", logged, "ms", ms)
		return &testOut{Body: TestResult{OK: code == "", Code: code, MS: ms}}, nil
	})
}

// testLimiter admits one test per instance per testEvery, so the button
// cannot hammer an upstream (or get Home Assistant to ban the server).
type testLimiter struct {
	mu   sync.Mutex
	last map[string]time.Time
}

// take admits a test of name at t, or says how long until one is; the
// entries past their window go, so the map holds only recent tests.
func (l *testLimiter) take(name string, t time.Time) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	if prev, ok := l.last[name]; ok {
		if wait := testEvery - t.Sub(prev); wait > 0 {
			return wait
		}
	}
	for k, at := range l.last {
		if t.Sub(at) >= testEvery {
			delete(l.last, k)
		}
	}
	l.last[name] = t
	return 0
}
