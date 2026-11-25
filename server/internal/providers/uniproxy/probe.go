package uniproxy

import (
	"context"
	"errors"
	"net/http"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

// probeWait bounds the wait for the answer to SynchronizeState after the socket opened
const probeWait = 5 * time.Second

// probe opens a socket with the instance's credentials and waits for SynchronizeState's answer: nothing is
// recognized or synthesized, so nothing is billed. A refused upgrade (401, 403) or InvalidAuth answers auth; a socket
// that opened and stays quiet passes, as the server takes requests before it answers
func probe(ctx context.Context, v any) error {
	var c *config
	switch p := v.(type) {
	case *stt:
		c = p.cfg
	case *tts:
		c = p.cfg
	default:
		return providers.ProbeBadResponse
	}
	c = c.withLanguage("")
	answered := make(chan error, 1)
	answer := func(err error) {
		select {
		case answered <- err:
		default:
		}
	}
	t, err := dial(ctx, c, handlers{
		message: func(in incoming) {
			if in.name == evSynchronizeStateResponse {
				answer(nil)
			}
		},
		end: func(e ending) {
			switch e.outcome {
			case "":
			case outcomeInvalidAuth:
				answer(providers.ProbeAuth)
			default:
				answer(providers.ProbeBadResponse)
			}
		},
	})
	if err != nil {
		var de dialError
		if errors.As(err, &de) && (de.status == http.StatusUnauthorized || de.status == http.StatusForbidden) {
			return providers.ProbeAuth
		}
		if ctx.Err() != nil {
			return providers.ProbeTimeout
		}
		if errors.As(err, &de) && de.status != 0 {
			return providers.ProbeStatus(de.status)
		}
		return providers.ProbeUnreachable
	}
	defer t.close()
	wait := time.NewTimer(probeWait)
	defer wait.Stop()
	select {
	case err := <-answered:
		return err
	case <-wait.C:
		return nil
	case <-ctx.Done():
		return providers.ProbeTimeout
	}
}
