// Package commands sends capability commands to devices and waits for their
// results. Args and results never reach a log.
package commands

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"log/slog"
	"regexp"
	"sync"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"google.golang.org/protobuf/proto"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/pkg/wire"
	"git.shino.boo/devilreef/cadence/server/internal/gateway"
	"git.shino.boo/devilreef/cadence/server/internal/jsonschemax"
)

const (
	defaultTimeout = 10 * time.Second
	maxInFlight    = 8
)

var (
	ErrOffline           = errors.New("commands: device offline")
	ErrBusy              = errors.New("commands: too many commands in flight")
	ErrTimeout           = errors.New("commands: no result in time")
	ErrArgumentsTooLarge = errors.New("commands: arguments too large")
)

// a device code that is no token is reported as failed, so device text never
// reaches a caller or a log
var token = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

type waitKey struct{}

// WithWait lets a command sent with ctx run up to d for its result, when d is longer than the usual wait (a group
// calibration's rounds take most of a minute); ctx's own deadline still ends it sooner.
func WithWait(ctx context.Context, d time.Duration) context.Context {
	return context.WithValue(ctx, waitKey{}, d)
}

func waitOf(ctx context.Context) time.Duration {
	d, _ := ctx.Value(waitKey{}).(time.Duration)
	return d
}

// InvalidArgumentsError carries only where the args failed, never their value.
type InvalidArgumentsError struct{ Path string }

func (e *InvalidArgumentsError) Error() string { return "commands: invalid arguments" }

// DeviceError is the device's failure code, a token: "failed" when the device's
// own was none.
type DeviceError struct {
	Code string
	// the device sent no token, so Code is this side's "failed"
	replaced bool
}

func (e *DeviceError) Error() string { return "commands: device answered " + e.Code }

// Detail is the device's own code, "" when it sent no token.
func (e *DeviceError) Detail() string {
	if e.replaced {
		return ""
	}
	return e.Code
}

// Lookup finds a command a device reported: the device's canonical id and the
// schema of the command's args.
type Lookup interface {
	Command(ctx context.Context, deviceID, capability, name string) (string, *jsonschema.Schema, error)
}

// link is a device's current connection; ctx is done once it closes or is replaced.
type link struct {
	conn gateway.Conn
	ctx  context.Context
}

// call waits for one result, which only the link it was sent on may give.
type call struct {
	conn   gateway.Conn
	result chan *cadencev2.CommandResult
}

type Service struct {
	lookup  Lookup
	log     *slog.Logger
	timeout time.Duration

	mu       sync.Mutex
	links    map[string]link
	inFlight map[string]int
	calls    map[string]*call // by request id

	// the clock session grants expire by
	now func() time.Time

	// intercept may answer a command in the device's place (the music
	// queue's skips); set once, before the links open
	intercept Interceptor
}

// Interceptor answers a command in the device's place when handled; else
// the command goes to the device.
type Interceptor func(ctx context.Context, deviceID, capability, name string, args []byte) (res json.RawMessage, handled bool, err error)

// Intercept sets the interceptor every Send consults first.
func (s *Service) Intercept(f Interceptor) { s.intercept = f }

func New(lookup Lookup, log *slog.Logger) *Service {
	return &Service{
		lookup: lookup, log: log, timeout: defaultTimeout, now: time.Now,
		links: map[string]link{}, inFlight: map[string]int{}, calls: map[string]*call{},
	}
}

// Linked makes c the device's link; call it from the gateway's OnHello with its ctx.
func (s *Service) Linked(ctx context.Context, c gateway.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.links[c.DeviceID()] = link{conn: c, ctx: ctx}
}

// Closed forgets the device's link; its waiting commands already saw its ctx end.
func (s *Service) Closed(deviceID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.links, deviceID)
}

// Handle consumes a CommandResult and reports whether env was one. A result
// for no waiting request, or from another link than the request's, is dropped.
func (s *Service) Handle(c gateway.Conn, env *cadencev2.Envelope) bool {
	r := env.GetCommandResult()
	if r == nil {
		return false
	}
	s.mu.Lock()
	w, ok := s.calls[r.GetRequestId()]
	mine := ok && w.conn == c
	if mine {
		delete(s.calls, r.GetRequestId())
	}
	s.mu.Unlock()
	if !mine {
		reason := "unknown"
		if ok {
			reason = "link"
		}
		s.log.Warn("command_result_dropped", "device_id", c.DeviceID(), "reason", reason)
		return true
	}
	w.result <- r
	return true
}

// Send runs one command on the device and returns its result, nil for none.
func (s *Service) Send(ctx context.Context, deviceID, capability, name string, args []byte) (json.RawMessage, error) {
	dev, sch, err := s.lookup.Command(ctx, deviceID, capability, name)
	if err != nil {
		return nil, err
	}
	start := time.Now()
	if s.intercept != nil {
		if res, handled, err := s.intercept(ctx, dev, capability, name, args); handled {
			s.log.Info("command", "device_id", dev, "capability", capability, "name", name, "code", codeOf(err),
				"ms", time.Since(start).Milliseconds(), "by", "server")
			return res, err
		}
	}
	res, err := s.send(ctx, dev, capability, name, sch, args)
	s.log.Info("command", "device_id", dev, "capability", capability, "name", name, "code", codeOf(err),
		"ms", time.Since(start).Milliseconds())
	return res, err
}

// Deliver sends the command to the device itself, past the interceptor.
func (s *Service) Deliver(ctx context.Context, deviceID, capability, name string, args []byte) (json.RawMessage, error) {
	dev, sch, err := s.lookup.Command(ctx, deviceID, capability, name)
	if err != nil {
		return nil, err
	}
	start := time.Now()
	res, err := s.send(ctx, dev, capability, name, sch, args)
	s.log.Info("command", "device_id", dev, "capability", capability, "name", name, "code", codeOf(err),
		"ms", time.Since(start).Milliseconds())
	return res, err
}

func (s *Service) send(ctx context.Context, dev, capability, name string, sch *jsonschema.Schema, args []byte) (json.RawMessage, error) {
	compact, err := checkArgs(sch, args)
	if err != nil {
		return nil, err
	}
	id := rand.Text()
	env := &cadencev2.Envelope{Body: &cadencev2.Envelope_CapabilityCommand{CapabilityCommand: &cadencev2.CapabilityCommand{
		RequestId: id, Capability: capability, Name: name, Json: compact, Grant: s.grant(ctx, dev, capability, name),
	}}}
	if proto.Size(env) > MaxArgumentsBytes {
		return nil, ErrArgumentsTooLarge
	}
	l, w, err := s.begin(dev, id)
	if err != nil {
		return nil, err
	}
	defer s.end(dev, id)
	if err := l.conn.Send(env); err != nil {
		switch {
		case errors.Is(err, gateway.ErrOffline):
			return nil, ErrOffline
		case errors.Is(err, gateway.ErrBackpressure):
			return nil, ErrBusy
		}
		return nil, err
	}
	timer := time.NewTimer(max(s.timeout, waitOf(ctx)))
	defer timer.Stop()
	select {
	case r := <-w.result:
		return outcome(r)
	case <-l.ctx.Done():
		// a result delivered just before the close still counts
		select {
		case r := <-w.result:
			return outcome(r)
		default:
			return nil, ErrOffline
		}
	case <-timer.C:
		return nil, ErrTimeout
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// begin takes one of the device's in-flight slots and registers the call.
func (s *Service) begin(dev, id string) (link, *call, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.links[dev]
	if !ok || l.ctx.Err() != nil {
		return link{}, nil, ErrOffline
	}
	if s.inFlight[dev] >= maxInFlight {
		return link{}, nil, ErrBusy
	}
	s.inFlight[dev]++
	w := &call{conn: l.conn, result: make(chan *cadencev2.CommandResult, 1)}
	s.calls[id] = w
	return l, w, nil
}

func (s *Service) end(dev, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.calls, id)
	if s.inFlight[dev]--; s.inFlight[dev] <= 0 {
		delete(s.inFlight, dev)
	}
}

// MaxArgumentsBytes bounds a command with its arguments: many reach audiod, whose socket takes 16 KiB frames
// (wire.MaxAudiodFrameBytes), and none needs more; the link itself takes far larger frames
const MaxArgumentsBytes = wire.MaxAudiodFrameBytes

// checkArgs validates args, an object (none is {}), and returns them compact.
func checkArgs(sch *jsonschema.Schema, args []byte) (string, error) {
	// args past one frame can never be sent, so they are not parsed
	if len(args) > MaxArgumentsBytes {
		return "", ErrArgumentsTooLarge
	}
	t := bytes.TrimSpace(args)
	if len(t) == 0 {
		return "{}", nil
	}
	if t[0] != '{' {
		return "", &InvalidArgumentsError{}
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, t); err != nil {
		return "", &InvalidArgumentsError{}
	}
	if path, ok := jsonschemax.Validate(sch, compact.Bytes()); !ok {
		return "", &InvalidArgumentsError{Path: path}
	}
	return compact.String(), nil
}

// outcome reads a device's result; ok JSON that does not parse is no result.
func outcome(r *cadencev2.CommandResult) (json.RawMessage, error) {
	if code := r.GetCode(); code != "" {
		if !token.MatchString(code) {
			return nil, &DeviceError{Code: "failed", replaced: true}
		}
		return nil, &DeviceError{Code: code}
	}
	var compact bytes.Buffer
	if json.Compact(&compact, []byte(r.GetJson())) != nil {
		return nil, nil
	}
	return compact.Bytes(), nil
}

// codeOf is the token for a Send outcome: "ok", a device code or this side's own.
func codeOf(err error) string {
	var invalid *InvalidArgumentsError
	var device *DeviceError
	switch {
	case err == nil:
		return "ok"
	case errors.As(err, &device):
		return device.Code
	case errors.As(err, &invalid):
		return "invalid_arguments"
	case errors.Is(err, ErrArgumentsTooLarge):
		return "arguments_too_large"
	case errors.Is(err, ErrOffline):
		return "device_offline"
	case errors.Is(err, ErrBusy):
		return "busy"
	case errors.Is(err, ErrTimeout):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	}
	return "internal"
}
