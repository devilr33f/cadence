package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"git.shino.boo/devilreef/cadence/server/internal/providers/httpx"
)

// ProbeError is a provider test's outcome code; it never carries upstream text.
type ProbeError string

func (e ProbeError) Error() string { return string(e) }

const (
	// ProbeAuth: the upstream refused the credential (401 or 403)
	ProbeAuth ProbeError = "auth"
	// ProbeUnreachable: no answer came (connect, dns, tls)
	ProbeUnreachable ProbeError = "unreachable"
	// ProbeTimeout: the test's deadline passed first
	ProbeTimeout ProbeError = "timeout"
	// ProbeBadResponse: an answer that is not what the module needs, as a
	// 5xx, a redirect or a body it cannot read
	ProbeBadResponse ProbeError = "bad_response"
	// ProbeUnsupported: the module has no free call to test with, or is no
	// longer registered
	ProbeUnsupported ProbeError = "unsupported"
	// ProbeRateLimited: the upstream answered 429
	ProbeRateLimited ProbeError = "rate_limited"
	// ProbeBuildFailed: the instance does not build from its stored config and
	// secrets
	ProbeBuildFailed ProbeError = "build_failed"
)

// ProbeCodes are every code a test answers.
var ProbeCodes = []ProbeError{
	ProbeAuth, ProbeUnreachable, ProbeTimeout, ProbeBadResponse, ProbeUnsupported, ProbeRateLimited, ProbeBuildFailed,
}

// ProbeCode is err's code: "" for nil, a ProbeError's own, timeout for a
// passed deadline, bad_response for anything else.
func ProbeCode(err error) string {
	var pe ProbeError
	switch {
	case err == nil:
		return ""
	case errors.As(err, &pe):
		return string(pe)
	case errors.Is(err, context.DeadlineExceeded):
		return string(ProbeTimeout)
	}
	return string(ProbeBadResponse)
}

// ProbeStatus is the code of an HTTP status that is not the success a probe
// wants.
func ProbeStatus(status int) error {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return ProbeAuth
	case http.StatusTooManyRequests:
		return ProbeRateLimited
	}
	return ProbeBadResponse
}

const maxProbeBody = 1 << 20

// ProbeGET sends one GET of url with header and no body, on a connection
// closed after it (a test's fresh client keeps no idle one), and wants 200
// with a JSON object of at most 1 MiB, which it returns.
func ProbeGET(ctx context.Context, client *http.Client, url string, header http.Header) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, ProbeBadResponse
	}
	req.Header = header.Clone()
	if req.Header == nil {
		req.Header = http.Header{}
	}
	req.Header.Set("Accept", "application/json")
	req.Close = true
	resp, err := client.Do(req)
	if err != nil {
		return nil, transportCode(ctx, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, ProbeStatus(resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxProbeBody+1))
	if err != nil {
		return nil, transportCode(ctx, err)
	}
	if len(b) > maxProbeBody || !jsonObject(b) {
		return nil, ProbeBadResponse
	}
	return b, nil
}

// transportCode names a request that got no usable answer: a refused
// redirect is the upstream's answer, a passed deadline a timeout.
func transportCode(ctx context.Context, err error) error {
	switch {
	case errors.Is(err, httpx.ErrRedirect):
		return ProbeBadResponse
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return ProbeTimeout
	}
	return ProbeUnreachable
}

func jsonObject(b []byte) bool {
	t := bytes.TrimSpace(b)
	return len(t) > 0 && t[0] == '{' && json.Valid(t)
}
