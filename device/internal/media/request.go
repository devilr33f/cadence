package media

import (
	"context"
	"strconv"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/capability"
	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
)

const (
	codeInvalid     = "invalid"
	codeUnavailable = "unavailable"
	// the device's own wait for audiod; audiod's own answer for a slow url stays "timeout"
	codeTimeout = "audiod_timeout"
	codeFailed  = "failed"
)

var errInvalid = &capability.CommandError{Code: codeInvalid}

// request sends the envelope build makes for a fresh request id and waits for
// audiod's answer; audiod's code becomes the command's.
func (m *Media) request(ctx context.Context, build func(id string) *audiodv1.Envelope) error {
	// a link already gone would discard the result of what audiod still did
	if err := ctx.Err(); err != nil {
		return err
	}
	id := "r-" + strconv.FormatUint(m.nextID.Add(1), 10)
	answer := make(chan string, 1)
	m.mu.Lock()
	send := m.send
	if send != nil {
		m.pending[id] = answer
	}
	m.mu.Unlock()
	if send == nil {
		return &capability.CommandError{Code: codeUnavailable}
	}
	// sent outside the lock: a slow write must not hold up the results audiod sends meanwhile
	if err := send(build(id)); err != nil {
		m.forget(id)
		return &capability.CommandError{Code: codeUnavailable}
	}
	t := time.NewTimer(m.commandTimeout)
	defer t.Stop()
	select {
	case code := <-answer:
		if code == "" {
			return nil
		}
		return &capability.CommandError{Code: code}
	case <-t.C:
		m.forget(id)
		return &capability.CommandError{Code: codeTimeout}
	case <-ctx.Done():
		m.forget(id)
		return ctx.Err()
	}
}

func (m *Media) forget(id string) {
	m.mu.Lock()
	delete(m.pending, id)
	m.mu.Unlock()
}

// answer hands a result to the request still waiting for it; any other is dropped.
func (m *Media) answer(r *audiodv1.CommandResult) {
	m.mu.Lock()
	ch, ok := m.pending[r.GetRequestId()]
	delete(m.pending, r.GetRequestId())
	m.mu.Unlock()
	if !ok {
		return
	}
	switch {
	case r.GetOk():
		ch <- ""
	case r.GetCode() != "":
		ch <- r.GetCode()
	default:
		ch <- codeFailed
	}
}
