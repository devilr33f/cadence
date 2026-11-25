package core

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"time"

	"google.golang.org/protobuf/proto"

	"git.shino.boo/devilreef/cadence/device/internal/capability"
	"git.shino.boo/devilreef/cadence/device/internal/link"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/pkg/wire"
)

const (
	commandWait = 10 * time.Second
	// per link; one more is answered busy
	commandsInFlight = 8
)

var commandToken = regexp.MustCompile(`^[a-z_]{1,32}$`)

// command answers a CapabilityCommand: at once when it cannot run, else from a
// goroutine of its own once the provider returns. A result for a link that
// went away is dropped.
func (s *Sync) command(ctx context.Context, sess link.Session, cc *cadencev2.CapabilityCommand) error {
	began := time.Now()
	id, name := cc.GetCapability(), cc.GetName()
	p, ok := s.byID[id]
	if !ok {
		return s.answer(sess, cc, "unknown_capability", nil, began)
	}
	if _, ok := s.commands[id][name]; !ok {
		return s.answer(sess, cc, "unknown_command", nil, began)
	}
	if !s.avail.Available(id) {
		return s.answer(sess, cc, "unavailable", nil, began)
	}
	slots := s.slots
	select {
	case slots <- struct{}{}:
	default:
		return s.answer(sess, cc, "busy", nil, began)
	}
	// a busy command never used its grant; one taken here is spent whatever the provider answers
	vouched, grant := s.grants.admit(s.deviceID, cc)
	if grant != "" && grant != "ok" {
		s.log.Warn("grant_refused", "capability", id, "name", name, "reason", grant)
	}
	s.wg.Go(func() {
		defer func() { <-slots }()
		wait := s.commandTimeout
		if w, ok := p.(capability.CommandWaiter); ok {
			wait = max(wait, w.CommandWait(name))
		}
		cctx, cancel := context.WithTimeout(ctx, wait)
		if grant == "ok" {
			cctx = capability.WithVouched(cctx, vouched)
		}
		res, err := p.Command(cctx, name, json.RawMessage(cc.GetJson()))
		cancel()
		code := commandCode(err)
		if ctx.Err() != nil {
			s.logCommand(cc, code, began, true)
			return
		}
		if code != "" {
			res = nil
		}
		_ = s.answer(sess, cc, code, res, began)
	})
	return nil
}

func (s *Sync) answer(sess link.Session, cc *cadencev2.CapabilityCommand, code string, res json.RawMessage, began time.Time) error {
	env := commandResult(cc, code, res)
	// a frame too large would never go out, and the server would wait for its timeout
	if proto.Size(env) > wire.MaxFrameBytes {
		code = "failed"
		env = commandResult(cc, code, nil)
	}
	s.logCommand(cc, code, began, false)
	return sess.Send(env)
}

func commandResult(cc *cadencev2.CapabilityCommand, code string, res json.RawMessage) *cadencev2.Envelope {
	r := &cadencev2.CommandResult{RequestId: cc.GetRequestId(), Code: code, Json: string(res)}
	return &cadencev2.Envelope{Body: &cadencev2.Envelope_CommandResult{CommandResult: r}}
}

// logCommand never logs the args or the result.
func (s *Sync) logCommand(cc *cadencev2.CapabilityCommand, code string, began time.Time, dropped bool) {
	if code == "" {
		code = "ok"
	}
	attrs := []any{"capability", cc.GetCapability(), "name", cc.GetName(), "code", code, "ms", time.Since(began).Milliseconds()}
	if cc.GetGrant() != nil {
		attrs = append(attrs, "granted", true)
	}
	if dropped {
		attrs = append(attrs, "dropped", true)
	}
	s.log.Info("command", attrs...)
}

// commandCode is the code the server may see: never the error's own text.
func commandCode(err error) string {
	var ce *capability.CommandError
	switch {
	case err == nil:
		return ""
	case errors.Is(err, capability.ErrUnsupported):
		return "unsupported"
	case errors.As(err, &ce):
		if commandToken.MatchString(ce.Code) {
			return ce.Code
		}
		return "failed"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	default:
		return "failed"
	}
}
