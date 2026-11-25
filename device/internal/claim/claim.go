// Package claim enrols a screen device-first (tv plan W0-C item 8, design §22): it opens a claim with the server,
// has the screen show its code and qr, long-polls until the owner approves it in the dashboard, and then saves the
// device's id and token as enroll does. A declined or expired claim is followed by a fresh one; the 8-character
// dashboard code typed on the screen enrols as `cadenced enroll` would. Codes, tokens and claim ids are never logged.
package claim

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/enroll"
	screenv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/screen/v1"
)

const (
	pollWait    = 25 * time.Second
	pollTimeout = 40 * time.Second
	maxBody     = 64 << 10
	minBackoff  = 2 * time.Second
	maxBackoff  = time.Minute
	// a declined claim's notice stays on screen this long before the fresh code
	declinedPause = 3 * time.Second
)

// Options are enroll's (server, board, config, data dir, ca file, audiod), with the screen to show the claim on and
// the fallback codes it sends.
type Options struct {
	enroll.Options
	// Show puts the claim on the screen
	Show func(*screenv1.ClaimState)
	// Codes are the 8-character codes typed on the screen
	Codes <-chan string
	// Servers are the server addresses saved in the screen's settings: the open claim is dropped and a fresh one
	// opened on the new server at once (the config names it once approved); the same address again is a retry, which
	// cuts a backoff short. An address Save would refuse is shown failed with server_url and changes nothing
	Servers <-chan string
	// ServerChanged hears each address the claim moved to (the screen's LinkStatus.server_url); nil: none
	ServerChanged func(url string)
	Log           *slog.Logger
	// Now and Sleep are the clock; tests set them
	Now   func() time.Time
	Sleep func(ctx context.Context, d time.Duration) bool
}

type started struct {
	ID          string    `json:"id"`
	Code        string    `json:"code"`
	URL         string    `json:"url"`
	ExpiresAt   time.Time `json:"expiresAt"`
	PollAfterMs int       `json:"pollAfterMs"`
}

type polled struct {
	State    string `json:"state"`
	DeviceID string `json:"deviceId"`
	Token    string `json:"token"`
}

// errGone is a claim the server no longer knows
var errGone = errors.New("claim: gone")

// failure is a request that failed, with a token for the screen and the logs
type failure struct{ code string }

func (f failure) Error() string { return "claim: " + f.code }

type runner struct {
	o      Options
	server string
	client *http.Client
	log    *slog.Logger
}

// Run claims until the device is enrolled (nil) or ctx ends (its error).
func Run(ctx context.Context, o Options) error {
	if o.Show == nil {
		o.Show = func(*screenv1.ClaimState) {}
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Sleep == nil {
		o.Sleep = sleep
	}
	// a config Save would refuse is refused before a claim is opened: an approval it took could not be kept
	if err := enroll.Check(o.Options); err != nil {
		return err
	}
	r := &runner{o: o, server: strings.TrimRight(o.Server, "/"), log: o.Log}
	c, err := enroll.HTTPClient(o.Options)
	if err != nil {
		return err
	}
	c.Timeout = pollTimeout
	r.client = c
	backoff := minBackoff
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		done, err := r.once(ctx)
		switch {
		case done:
			return nil
		case err == nil, errors.Is(err, errRestart):
			backoff = minBackoff
			continue
		case ctx.Err() != nil:
			return ctx.Err()
		}
		code := "unreachable"
		var f failure
		if errors.As(err, &f) {
			code = f.code
		}
		wait := backoff
		if code == "rate_limited" {
			wait = maxBackoff
		}
		r.log.Warn("claim_failed", "code", code)
		r.o.Show(&screenv1.ClaimState{Phase: screenv1.ClaimPhase_CLAIM_PHASE_FAILED, ErrorCode: code})
		switch slept, restart := r.pause(ctx, wait); {
		case !slept:
			return ctx.Err()
		case restart:
			backoff = minBackoff
		default:
			backoff = min(backoff*2, maxBackoff)
		}
	}
}

// errRestart is a new server address (or the same one again) while a claim was open: a fresh claim starts at once
var errRestart = errors.New("claim: restart")

// pause sleeps d, cut short by a server address the screen saved: slept is false once ctx ended, restart true when an
// address cut it short
func (r *runner) pause(ctx context.Context, d time.Duration) (slept, restart bool) {
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan bool, 1)
	go func() { done <- r.o.Sleep(sctx, d) }()
	for {
		select {
		case ok := <-done:
			return ok, false
		case u := <-r.o.Servers:
			if !r.useServer(u) {
				continue
			}
			cancel()
			<-done
			return ctx.Err() == nil, true
		}
	}
}

// useServer takes an address from the screen when Save would keep it; the address itself is never logged
func (r *runner) useServer(u string) bool {
	next := r.o.Options
	next.Server = u
	if strings.TrimSpace(u) == "" || enroll.Check(next) != nil {
		r.log.Warn("claim_server_refused")
		r.o.Show(&screenv1.ClaimState{Phase: screenv1.ClaimPhase_CLAIM_PHASE_FAILED, ErrorCode: "server_url"})
		return false
	}
	r.o.Server, r.server = u, strings.TrimRight(u, "/")
	r.log.Info("claim_server_changed")
	if r.o.ServerChanged != nil {
		r.o.ServerChanged(u)
	}
	return true
}

// once holds one claim open until it ends: true once the device is enrolled; nil error for a claim that ended
// (declined, expired, removed), so the next starts at once
func (r *runner) once(ctx context.Context) (bool, error) {
	st, skew, err := r.start(ctx)
	if err != nil {
		return false, err
	}
	expires := r.o.Now().Add(time.Until(st.ExpiresAt) + skew)
	r.log.Info("claim_shown")
	r.o.Show(&screenv1.ClaimState{
		Phase: screenv1.ClaimPhase_CLAIM_PHASE_PENDING, Code: st.Code, Url: st.URL, ExpiresAtMs: expires.UnixMilli(),
	})
	backoff := minBackoff
	for {
		res, err := r.wait(ctx, st.ID)
		switch {
		case errors.Is(err, errGone):
			return false, nil
		case errors.Is(err, errCode):
			return true, nil
		case errors.Is(err, errRestart), ctx.Err() != nil:
			return false, err
		case err != nil:
			// the server away under the poll (a restart, the network): the claim it holds stays open, so its code,
			// which the owner may be typing, is polled again until it expires, rather than a fresh one opened
			if !r.o.Now().Before(expires) {
				return false, err
			}
			var f failure
			code := "unreachable"
			if errors.As(err, &f) {
				code = f.code
			}
			r.log.Warn("claim_poll_failed", "code", code)
			switch slept, restart := r.pause(ctx, backoff); {
			case !slept:
				return false, ctx.Err()
			case restart:
				return false, errRestart
			}
			backoff = min(backoff*2, maxBackoff)
			continue
		}
		backoff = minBackoff
		switch res.State {
		case "pending":
			continue
		case "approved":
			if err := enroll.Save(r.o.Options, res.DeviceID, res.Token); err != nil {
				return false, failure{"save"}
			}
			r.log.Info("claim_approved")
			r.o.Show(&screenv1.ClaimState{Phase: screenv1.ClaimPhase_CLAIM_PHASE_APPROVED})
			return true, nil
		case "declined":
			r.log.Info("claim_declined")
			r.o.Show(&screenv1.ClaimState{Phase: screenv1.ClaimPhase_CLAIM_PHASE_DECLINED})
			if !r.o.Sleep(ctx, declinedPause) {
				return false, ctx.Err()
			}
			return false, nil
		case "expired":
			r.log.Info("claim_expired")
			r.o.Show(&screenv1.ClaimState{Phase: screenv1.ClaimPhase_CLAIM_PHASE_EXPIRED})
			return false, nil
		default:
			// removed: approved, then the device was deleted before this poll
			r.log.Info("claim_ended", "state", "removed")
			r.o.Show(&screenv1.ClaimState{Phase: screenv1.ClaimPhase_CLAIM_PHASE_FAILED, ErrorCode: "removed"})
			if !r.o.Sleep(ctx, declinedPause) {
				return false, ctx.Err()
			}
			return false, nil
		}
	}
}

// start opens a claim; skew is how far the server's clock (its Date) runs behind this one's
func (r *runner) start(ctx context.Context) (started, time.Duration, error) {
	body, _ := json.Marshal(map[string]string{"board": r.o.Board})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.server+"/v2/claim", bytes.NewReader(body))
	if err != nil {
		return started{}, 0, failure{"server_url"}
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := r.client.Do(req)
	if err != nil {
		// the transport error may quote the url
		return started{}, 0, failure{"unreachable"}
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusCreated:
	case http.StatusTooManyRequests:
		return started{}, 0, failure{"rate_limited"}
	case http.StatusBadRequest:
		return started{}, 0, failure{"unknown_board"}
	default:
		return started{}, 0, failure{"status_" + strconv.Itoa(resp.StatusCode)}
	}
	var st started
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(&st); err != nil || st.ID == "" || st.Code == "" {
		return started{}, 0, failure{"malformed"}
	}
	var skew time.Duration
	if d, err := http.ParseTime(resp.Header.Get("Date")); err == nil {
		skew = time.Since(d).Truncate(time.Second)
	}
	return st, skew, nil
}

// errCode is the device enrolled with a code typed on the screen while the claim was open
var errCode = errors.New("claim: enrolled by code")

// wait is one long poll, cut short by a code typed on the screen
func (r *runner) wait(ctx context.Context, id string) (polled, error) {
	pctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type answer struct {
		p   polled
		err error
	}
	got := make(chan answer, 1)
	go func() {
		p, err := r.poll(pctx, id)
		got <- answer{p, err}
	}()
	for {
		select {
		case a := <-got:
			return a.p, a.err
		case code := <-r.o.Codes:
			if r.enrollCode(ctx, code) {
				return polled{}, errCode
			}
		case u := <-r.o.Servers:
			if r.useServer(u) {
				return polled{}, errRestart
			}
		}
	}
}

func (r *runner) enrollCode(ctx context.Context, code string) bool {
	err := enroll.Run(ctx, enroll.Options{
		Server: r.o.Server, Code: code, Board: r.o.Board, ConfigPath: r.o.ConfigPath, DataDir: r.o.DataDir,
		CAFile: r.o.CAFile, Audiod: r.o.Audiod, AudiodBinary: r.o.AudiodBinary, Client: r.o.Client,
	})
	if err == nil {
		r.log.Info("claim_enrolled_by_code")
		r.o.Show(&screenv1.ClaimState{Phase: screenv1.ClaimPhase_CLAIM_PHASE_APPROVED})
		return true
	}
	code = "code_failed"
	if errors.Is(err, enroll.ErrRefused) {
		code = "code_refused"
	}
	r.log.Info("claim_code_failed", "code", code)
	// the screen shows the refusal over the open claim, whose code stays valid
	r.o.Show(&screenv1.ClaimState{Phase: screenv1.ClaimPhase_CLAIM_PHASE_FAILED, ErrorCode: code})
	return false
}

func (r *runner) poll(ctx context.Context, id string) (polled, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.server+"/v2/claim/"+id+"?wait="+pollWait.String(), nil)
	if err != nil {
		return polled{}, failure{"server_url"}
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return polled{}, failure{"unreachable"}
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return polled{}, errGone
	default:
		return polled{}, failure{"status_" + strconv.Itoa(resp.StatusCode)}
	}
	var p polled
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(&p); err != nil || p.State == "" {
		return polled{}, failure{"malformed"}
	}
	return p, nil
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
